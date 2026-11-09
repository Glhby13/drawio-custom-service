package edge

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/edge/mailer"
	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/session"
)

type fakeMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (f *fakeMailer) Send(_ context.Context, m mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

// inviteEnv is a gateway Server wired to a fake platform: only what the invitation handlers touch.
type inviteEnv struct {
	s            *Server
	mail         *fakeMailer
	mu           sync.Mutex
	created      int
	released     []string          // email-failed calls: the day's e-mail given back
	contact      map[string]string // username -> e-mail known to the platform
	status       int               // answer of the platform create (default 201)
	canShare     bool              // answer of the owner check (GET permissions)
	contactCalls int
}

const testToken = "dsi_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func newInviteEnv(t *testing.T) *inviteEnv {
	t.Helper()
	e := &inviteEnv{mail: &fakeMailer{}, contact: map[string]string{}, status: http.StatusCreated, canShare: true}
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/permissions"):
			if !e.canShare {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"owner":"alice","permissions":[]}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/contacts/"):
			e.contactCalls++
			u := strings.TrimPrefix(r.URL.Path, "/api/v1/internal/contacts/")
			_ = json.NewEncoder(w).Encode(map[string]any{"email": e.contact[u], "name": "Grace G", "found": true})
		case strings.HasSuffix(r.URL.Path, "/email-failed"):
			e.released = append(e.released, r.URL.Path)
			_, _ = io.WriteString(w, `{"released":true}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/resend"):
			_, _ = io.WriteString(w, `{"invitation":{"id":"i1","kind":"user","subject":"grace","role":"editor","status":"pending"},"token":"`+testToken+`","documentTitle":"test"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invitations"):
			e.created++
			w.WriteHeader(e.status)
			if e.status != http.StatusCreated {
				_, _ = io.WriteString(w, `{"error":{"code":"email_cooldown","message":"already sent today"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"invitation":{"id":"i1","kind":"user","subject":"grace","role":"editor","expiresAt":"2026-11-16T16:59:00Z","status":"pending"},"token":"`+testToken+`","documentTitle":"test"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(plat.Close)
	pu, _ := url.Parse("https://drawio.test")
	plu, _ := url.Parse(plat.URL)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.s = &Server{cfg: config.Gateway{PlatformUpstream: plu, PublicURL: pu}, log: log, signer: identity.NewSigner(priv, "k", "drawio-gateway"), platform: newPlatformProxy(plu, log), mailer: e.mail, lookupLim: newLimiter(100, 100)}
	return e
}

var inviteSess = &session.Session{Subject: "sub-alice", Username: "alice", Name: "Alice A", CSRF: "csrf-tok", CreatedAt: time.Now()}

func (e *inviteEnv) post(h func(http.ResponseWriter, *http.Request, *session.Session), body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/documents/d1/invitations", bytes.NewBufferString(body))
	r.SetPathValue("id", "d1")
	r.Header.Set("X-CSRF-Token", "csrf-tok")
	w := httptest.NewRecorder()
	h(w, r, inviteSess)
	return w
}

func decodeRec(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("not json: %q", w.Body.String())
	}
	return m
}

func TestCreateInviteMailsAndHidesToken(t *testing.T) {
	e := newInviteEnv(t)
	e.contact["grace"] = "grace@btn.co.id"
	w := e.post(e.s.createInvite, `{"kind":"user","subject":"Grace","role":"editor","sendEmail":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	out := decodeRec(t, w)
	if out["emailed"] != true {
		t.Fatalf("emailed = %v", out["emailed"])
	}
	if _, has := out["token"]; has || strings.Contains(w.Body.String(), "dsi_") {
		t.Fatalf("the token must not reach the browser when the mail went out: %s", w.Body.String())
	}
	if len(e.mail.sent) != 1 {
		t.Fatalf("mails sent = %d", len(e.mail.sent))
	}
	m := e.mail.sent[0]
	if m.ToEmail != "grace@btn.co.id" || m.Subject != "Undangan akses diagram 'test'" {
		t.Errorf("mail = %+v", m)
	}
	if !strings.Contains(m.HTML, "https://drawio.test/invite/"+testToken) || !strings.Contains(m.Text, "https://drawio.test/invite/"+testToken) {
		t.Error("the mail does not carry the invitation link")
	}
	if !strings.Contains(m.HTML, "Alice A") {
		t.Error("the inviter's name is missing from the mail")
	}
	if len(e.released) != 0 {
		t.Errorf("a mail that went out must keep the day's slot: %v", e.released)
	}
}

func TestCreateInviteWithoutKnownEmailIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	e := newInviteEnv(t) // no contact e-mail, no Keycloak
	w := e.post(e.s.createInvite, `{"kind":"user","subject":"nomail","role":"viewer","sendEmail":true}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "no_email") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if e.created != 0 || len(e.mail.sent) != 0 {
		t.Errorf("nothing may be created or mailed: created=%d mails=%d", e.created, len(e.mail.sent))
	}
}

func TestCreateInviteKeepsTheLinkWhenTheMailFails(t *testing.T) {
	e := newInviteEnv(t)
	e.contact["grace"] = "grace@btn.co.id"
	e.mail.err = errors.New("smtp down")
	w := e.post(e.s.createInvite, `{"kind":"user","subject":"grace","role":"editor","sendEmail":true}`)
	out := decodeRec(t, w)
	if w.Code != http.StatusCreated || out["emailed"] != false || out["emailError"] != "send_failed" || out["token"] != testToken {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(e.released) != 1 || !strings.Contains(e.released[0], "/i1/email-failed") {
		t.Errorf("a failed send must give the day's e-mail back: %v", e.released)
	}
}

func TestCreateInviteDailyLimitFromPlatformSendsNothing(t *testing.T) {
	e := newInviteEnv(t)
	e.contact["grace"] = "grace@btn.co.id"
	e.status = http.StatusTooManyRequests
	w := e.post(e.s.createInvite, `{"kind":"user","subject":"grace","role":"editor","sendEmail":true}`)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "email_cooldown") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(e.mail.sent) != 0 || len(e.released) != 0 {
		t.Error("no mail after the platform refused")
	}
}

func TestLinkAndHandCopiedInvitationsPassStraightThrough(t *testing.T) {
	e := newInviteEnv(t)
	for _, body := range []string{`{"kind":"link","role":"viewer"}`, `{"kind":"user","subject":"grace","role":"editor","sendEmail":false}`} {
		w := e.post(e.s.createInvite, body)
		if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), testToken) {
			t.Fatalf("%s -> %d %s", body, w.Code, w.Body.String())
		}
	}
	if len(e.mail.sent) != 0 {
		t.Error("link invitations never send mail")
	}
}

func TestEmailUnavailableWhenNoSMTP(t *testing.T) {
	e := newInviteEnv(t)
	e.s.mailer = nil
	w := e.post(e.s.createInvite, `{"kind":"user","subject":"grace","role":"editor","sendEmail":true}`)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "email_unavailable") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if e.created != 0 {
		t.Error("nothing may be created when the mail cannot be sent")
	}
}

func TestValidEmail(t *testing.T) {
	for _, ok := range []string{"a@b.co", "grace.survinta@btn.co.id"} {
		if !validEmail(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "a", "a@b.co\r\nBcc: x@y.z", "Grace <g@b.co>", "a@b.co,c@d.co", "a b@c.co", "<a@b.co>", "a@b.co;c@d.co"} {
		if validEmail(bad) {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestInvitePageIsNoReferrerAndNoStore(t *testing.T) {
	e := newEnv(t, nil)
	j := jar{}
	if res := e.loginWith(j, codeEntry{roles: []string{"user"}}); res.StatusCode != http.StatusFound {
		t.Fatalf("login status %d", res.StatusCode)
	}
	r := e.do(j, "GET", "/invite/"+testToken, nav)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d", r.StatusCode)
	}
	if r.Header.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(r.Header.Get("Cache-Control"), "no-store") {
		t.Errorf("headers: %v", r.Header)
	}
}

func TestInvitePageNeedsASessionAndKeepsTheLink(t *testing.T) {
	e := newEnv(t, nil)
	r := e.do(nil, "GET", "/invite/"+testToken, nav)
	if r.StatusCode != http.StatusFound || !strings.Contains(r.Header.Get("Location"), "/login?return_to="+url.QueryEscape("/invite/"+testToken)) {
		t.Fatalf("status %d location %q", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestCreateInviteRefusesAMalformedSubjectBeforeLookingAnythingUp(t *testing.T) {
	e := newInviteEnv(t)
	for _, subj := range []string{"x\r\nBcc: a@b.c", "a b", "", "../etc", "<script>", "a;b"} {
		body, _ := json.Marshal(map[string]any{"kind": "user", "subject": subj, "role": "viewer", "sendEmail": true})
		w := e.post(e.s.createInvite, string(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q -> %d %s", subj, w.Code, w.Body.String())
		}
	}
	if e.created != 0 || len(e.mail.sent) != 0 {
		t.Error("nothing may be created or mailed for a malformed subject")
	}
}

func TestCreateInviteRejectsAnUnparsableBodyInsteadOfForwardingIt(t *testing.T) {
	e := newInviteEnv(t)
	e.contact["grace"] = "grace@btn.co.id"
	// the platform used to read only the first value and ignore the rest: the gateway then never saw "sendEmail"
	w := e.post(e.s.createInvite, `{"kind":"user","subject":"grace","role":"viewer","sendEmail":true} {}`)
	if w.Code != http.StatusBadRequest || e.created != 0 {
		t.Fatalf("status %d created %d: %s", w.Code, e.created, w.Body.String())
	}
}

func TestCreateInviteLooksNothingUpForSomebodyWhoMayNotShare(t *testing.T) {
	e := newInviteEnv(t)
	e.canShare = false
	w := e.post(e.s.createInvite, `{"kind":"user","subject":"grace","role":"viewer","sendEmail":true}`)
	if w.Code != http.StatusForbidden || e.contactCalls != 0 || e.created != 0 {
		t.Fatalf("status %d, contact look-ups %d, created %d: %s", w.Code, e.contactCalls, e.created, w.Body.String())
	}
}

func TestContactLookupsAreThrottledPerPerson(t *testing.T) {
	e := newInviteEnv(t)
	e.s.lookupLim = newLimiter(0.000001, 3)
	e.status = http.StatusTooManyRequests // each attempt stops at the platform; only the look-up budget matters here
	got429 := false
	for i := 0; i < 8; i++ {
		w := e.post(e.s.createInvite, `{"kind":"user","subject":"grace","role":"viewer","sendEmail":true}`)
		if w.Code == http.StatusTooManyRequests && strings.Contains(w.Body.String(), "rate_limited") {
			got429 = true
		}
	}
	if !got429 || e.contactCalls > 3 {
		t.Fatalf("look-ups were not throttled: 429=%v contact calls=%d", got429, e.contactCalls)
	}
}

func TestResendWithoutKnownEmailGivesTheDaysEmailBack(t *testing.T) {
	e := newInviteEnv(t) // the platform knows no address for grace and there is no Keycloak
	r := httptest.NewRequest(http.MethodPost, "/api/v1/documents/d1/invitations/i1/resend", strings.NewReader(`{}`))
	r.SetPathValue("id", "d1")
	r.SetPathValue("iid", "i1")
	r.Header.Set("X-CSRF-Token", "csrf-tok")
	w := httptest.NewRecorder()
	e.s.resendInvite(w, r, inviteSess)
	if len(e.mail.sent) != 0 || len(e.released) != 1 || !strings.Contains(w.Body.String(), "no_email") || !strings.Contains(w.Body.String(), testToken) {
		t.Fatalf("mails %d released %v body %s", len(e.mail.sent), e.released, w.Body.String())
	}
}

func TestPlatformAPIRefusesStateChangesWithoutTheCSRFToken(t *testing.T) {
	e := newInviteEnv(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r := httptest.NewRequest(method, "/api/v1/documents/d1/invitations", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		e.s.platformAPI(w, r, inviteSess)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "csrf") {
			t.Errorf("%s without token -> %d %s", method, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/documents/d1/permissions", nil)
	w := httptest.NewRecorder()
	e.s.platformAPI(w, r, inviteSess)
	if w.Code != http.StatusOK {
		t.Errorf("a read needs no token: %d", w.Code)
	}
}
