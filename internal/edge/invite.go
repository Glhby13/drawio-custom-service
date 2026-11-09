package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/edge/mailer"
	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/session"
)

// Share invitations, the gateway's part (ADR-0021). The platform owns the invitation (rules, tokens, the daily e-mail
// limit); the gateway owns what the platform deliberately cannot do: look an e-mail address up in Keycloak and send the
// mail (the platform has no route to Keycloak or the MTA, ADR-0004).

// mailSender is what the gateway needs from the mailer (a fake in tests).
type mailSender interface {
	Send(ctx context.Context, m mailer.Message) error
}

func newMailer(cfg mailerConfig) mailSender {
	if cfg.Host == "" || cfg.FromEmail == "" {
		return nil
	}
	return &mailer.SMTPMailer{Cfg: mailer.Config{Host: cfg.Host, Port: cfg.Port, FromEmail: cfg.FromEmail, FromName: cfg.FromName,
		ClientHostname: cfg.ClientHostname, TimeoutSeconds: cfg.TimeoutSeconds, StartTLS: cfg.StartTLS, User: cfg.User, Password: cfg.Password}}
}

type mailerConfig struct {
	Host, FromEmail, FromName, StartTLS, ClientHostname, User, Password string
	Port, TimeoutSeconds                                                int
}

// platformInternal calls a gateway-only platform endpoint with a short-lived service token.
func (s *Server) platformInternal(ctx context.Context, method, path string, in, out any) (int, error) {
	tok, err := s.signer.Sign(identity.Claims{Subject: "gateway-auth", Audience: platformAudience, ActorType: identity.ActorService}, 30*time.Second)
	if err != nil {
		return 0, err
	}
	var rd io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.cfg.PlatformUpstream.String(), "/")+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil && err != io.EOF {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// recipientContact finds the e-mail of an account: the platform first (local accounts, people who signed in), then the
// Keycloak directory. Empty email = unknown (the owner is told to use the link instead).
func (s *Server) recipientContact(ctx context.Context, username string) (email, name string) {
	var c struct {
		Email, Name string
		Found       bool
	}
	if code, err := s.platformInternal(ctx, http.MethodGet, "/api/v1/internal/contacts/"+url.PathEscape(username), nil, &c); err == nil && code == http.StatusOK {
		email, name = c.Email, c.Name
	}
	if validEmail(email) {
		return email, name
	}
	email = ""
	if s.oidc != nil {
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		if e, n, err := s.oidc.LookupContact(cctx, username); err == nil && validEmail(e) {
			return e, firstNonEmpty(n, name)
		}
	}
	return "", name
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.@\-]{1,128}$`)

func validUsername(u string) bool { return usernameRe.MatchString(u) }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// validEmail accepts one plain address (no display name, no line breaks).
func validEmail(e string) bool {
	if e == "" || strings.ContainsAny(e, "\r\n<>,;\" ") {
		return false
	}
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e
}

func writeGwErr(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": errCode, "message": msg}})
}

type platformInvite struct {
	Invitation    map[string]any `json:"invitation"`
	Token         string         `json:"token"`
	DocumentTitle string         `json:"documentTitle"`
}

// callPlatform runs the request through the normal signed platform path and records the answer.
func (s *Server) callPlatform(r *http.Request, sess *session.Session, body []byte) *httptest.ResponseRecorder {
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	s.platformAPI(rec, r2, sess)
	return rec
}

// createInvite handles POST /api/v1/documents/{id}/invitations. A link, or a user invitation the owner will copy by hand,
// goes straight to the platform. An e-mailed invitation first needs a known address, then the platform's checks
// (including the daily limit), then the mail, and only then the "sent" mark.
func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		writeGwErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	var in struct {
		Kind, Subject string
		SendEmail     bool
	}
	if json.Unmarshal(body, &in) != nil || in.Kind != "user" || !in.SendEmail {
		copyRecorded(w, s.callPlatform(r, sess, body))
		return
	}
	if s.mailer == nil {
		writeGwErr(w, http.StatusServiceUnavailable, "email_unavailable", "e-mail is not configured: copy the invitation link instead")
		return
	}
	subject := strings.ToLower(strings.TrimSpace(in.Subject))
	if !validUsername(subject) { // same rule as the platform, checked BEFORE anything is looked up or mailed
		writeGwErr(w, http.StatusBadRequest, "bad_request", "subject must be a username")
		return
	}
	to, toName := s.recipientContact(r.Context(), subject)
	if to == "" {
		writeGwErr(w, http.StatusUnprocessableEntity, "no_email", "no e-mail address is known for this person: copy the invitation link instead")
		return
	}
	rec := s.callPlatform(r, sess, body)
	if rec.Code != http.StatusCreated {
		copyRecorded(w, rec)
		return
	}
	s.mailAndAnswer(w, r, sess, rec, to, toName, subject)
}

// resendInvite handles POST .../invitations/{iid}/resend: the platform rotates the token, the gateway mails the new link.
func (s *Server) resendInvite(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 2048))
	if s.mailer == nil {
		writeGwErr(w, http.StatusServiceUnavailable, "email_unavailable", "e-mail is not configured: copy the invitation link instead")
		return
	}
	rec := s.callPlatform(r, sess, body)
	if rec.Code != http.StatusOK {
		copyRecorded(w, rec)
		return
	}
	var pi platformInvite
	if json.Unmarshal(rec.Body.Bytes(), &pi) != nil {
		copyRecorded(w, rec)
		return
	}
	subject, _ := pi.Invitation["subject"].(string)
	to, toName := s.recipientContact(r.Context(), subject)
	if to == "" {
		s.answerInvite(w, http.StatusOK, pi, false, "no_email")
		return
	}
	s.mailAndAnswer(w, r, sess, rec, to, toName, subject)
}

// mailAndAnswer renders and sends the mail for the invitation the platform just created/rotated, then answers the browser.
// The token is handed back only when the mail did NOT go out (so the owner can copy the link); otherwise it never reaches the browser.
func (s *Server) mailAndAnswer(w http.ResponseWriter, r *http.Request, sess *session.Session, rec *httptest.ResponseRecorder, to, toName, subject string) {
	var pi platformInvite
	if json.Unmarshal(rec.Body.Bytes(), &pi) != nil || pi.Token == "" || pi.Invitation == nil {
		copyRecorded(w, rec)
		return
	}
	var exp *time.Time
	if v, ok := pi.Invitation["expiresAt"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			exp = &t
		}
	}
	role, _ := pi.Invitation["role"].(string)
	inviter := firstNonEmpty(sess.Name, sess.Username)
	id, _ := pi.Invitation["id"].(string)
	// the platform already took the day's e-mail when it created/rotated the invitation: give it back if the mail cannot go out
	fail := func(reason string) {
		if code, err := s.platformInternal(r.Context(), http.MethodPost, "/api/v1/internal/invitations/"+url.PathEscape(id)+"/email-failed", map[string]string{}, nil); err != nil || code != http.StatusOK {
			s.log.Warn("invite e-mail slot not released", "request_id", RequestID(r.Context()))
		}
		s.answerInvite(w, rec.Code, pi, false, reason)
	}
	rendered, err := mailer.RenderInvite(mailer.Invite{ToName: firstNonEmpty(toName, subject), InviterName: inviter, InviterUsername: sess.Username,
		RecipientUsername: subject, DocTitle: pi.DocumentTitle, Role: role, ExpiresAt: exp,
		Link: strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/invite/" + pi.Token, SiteHost: s.cfg.PublicURL.Host})
	if err != nil {
		s.log.Error("invite mail render", "request_id", RequestID(r.Context()), "err", err.Error())
		fail("send_failed")
		return
	}
	sctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.mailer.Send(sctx, mailer.Message{ToEmail: to, ToName: toName, Subject: rendered.Subject, HTML: rendered.HTML, Text: rendered.Text, Inline: rendered.Inline}); err != nil {
		// no address, no token in the log: only the fact and the error
		s.log.Warn("invite mail failed", "request_id", RequestID(r.Context()), "err", err.Error())
		fail("send_failed")
		return
	}
	pi.Token = ""
	s.answerInvite(w, rec.Code, pi, true, "")
}

func (s *Server) answerInvite(w http.ResponseWriter, code int, pi platformInvite, emailed bool, emailError string) {
	out := map[string]any{"invitation": pi.Invitation, "emailed": emailed}
	if !emailed {
		out["token"] = pi.Token
		out["emailError"] = emailError
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(out)
}
