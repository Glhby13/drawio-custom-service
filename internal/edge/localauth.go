package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/session"
	"github.com/Glhby13/drawio-custom-service/internal/totp"
)

// Local sign-in (DrawSpace accounts). The gateway never sees a password hash: it forwards the credentials to the platform over the
// cluster network with a short-lived service token, and the platform answers yes (identity + role) or no. Failures are counted per
// username and per client address in Redis; five failures lock that username for the rest of a 15 minute window.
const (
	maxLocalFailures = 5
	failWindow       = 15 * time.Minute
)

type localIdentity struct {
	Sub, Username, Name, Email, Role string
}

var (
	errBadCredentials = errors.New("invalid credentials")
	errAuthBackend    = errors.New("platform unavailable")
)

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	// already signed in? go where the user wanted to be
	if c, err := r.Cookie(s.cookie); err == nil {
		if _, err := s.store.GetSession(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, safeReturn(r.URL.Query().Get("return_to")), http.StatusFound) //nolint:gosec // safeReturn only yields a same-site absolute path
			return
		}
	}
	h := w.Header()
	h.Set("Content-Security-Policy", s.loginCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.loginBody)
}

func (s *Server) loginAD(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int, msg string, desc ...string) {
		w.WriteHeader(code)
		res := map[string]string{"error": msg}
		if len(desc) > 0 && desc[0] != "" {
			res["error_description"] = desc[0]
		}
		_ = json.NewEncoder(w).Encode(res)
	}
	if s.oidc == nil {
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
		ReturnTo string `json:"return_to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || in.Username == "" || in.Password == "" || len(in.Username) > 64 || len(in.Password) > 256 {
		fail(http.StatusBadRequest, "bad_request")
		return
	}
	user := strings.ToLower(strings.TrimSpace(in.Username))
	ip := clientIP(r)
	ctx := r.Context()
	for _, k := range []string{"u:" + user, "ip:" + ip} {
		if n, err := s.store.Failures(ctx, k); err != nil {
			fail(http.StatusServiceUnavailable, "unavailable")
			return
		} else if n >= maxLocalFailures*4 && strings.HasPrefix(k, "ip:") || n >= maxLocalFailures && strings.HasPrefix(k, "u:") {
			w.Header().Set("Retry-After", "900")
			s.log.Warn("ad sign-in locked", "request_id", RequestID(ctx), "key", strings.SplitN(k, ":", 2)[0])
			fail(http.StatusTooManyRequests, "locked")
			return
		}
	}
	id, err := s.oidc.DirectGrant(ctx, user, in.Password, strings.TrimSpace(in.TOTP))
	switch {
	case errors.Is(err, ErrTOTPRequired):
		fail(http.StatusUnauthorized, "totp_required")
		return
	case errors.Is(err, ErrAccessDenied):
		s.log.Info("ad sign-in access denied", "request_id", RequestID(ctx), "user", user)
		fail(http.StatusForbidden, "access_denied", "Access denied: your account has no access to this application.")
		return
	case errors.Is(err, ErrBadCredentials):
		if in.TOTP == "" && s.oidc.IsAdminUser(ctx, user) {
			// Keycloak direct grant with admin-OTP subflow rejected the initial request because OTP is required.
			// Trigger TOTP flow (either setup QR modal or 6-digit challenge modal).
			totpStatus, terr := s.getPlatformTOTP(ctx, user)
			if terr == nil && !totpStatus.Configured {
				secret, serr := totp.GenerateSecret()
				if serr == nil {
					qr, qerr := totp.QRCodeBase64(secret, user, "DrawSpace")
					if qerr == nil {
						setupToken := session.RandomToken(32)
						data := totpSetupData{
							Username: user,
							Secret:   secret,
							Subject:  user,
							Name:     user,
							Email:    user + "@lab.local",
							Roles:    []string{"admin", s.cfg.RequiredRole},
							ReturnTo: in.ReturnTo,
						}
						if s.store.PutTOTPSetup(ctx, setupToken, data, 5*time.Minute) == nil {
							_ = json.NewEncoder(w).Encode(map[string]any{
								"error":       "totp_setup_required",
								"setup_token": setupToken,
								"qr_code":     qr,
								"secret":      secret,
							})
							return
						}
					}
				}
			}
			fail(http.StatusUnauthorized, "totp_required")
			return
		}
		_, _ = s.store.Fail(ctx, "u:"+user, failWindow)
		_, _ = s.store.Fail(ctx, "ip:"+ip, failWindow)
		s.log.Info("ad sign-in refused", "request_id", RequestID(ctx))
		fail(http.StatusUnauthorized, "invalid_credentials")
		return
	case err != nil:
		s.log.Error("ad sign-in backend", "request_id", RequestID(ctx), "err", err.Error())
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}

	isAdmin := slices.Contains(id.Roles, "admin")
	if isAdmin {
		totpStatus, err := s.getPlatformTOTP(ctx, user)
		if err != nil {
			s.log.Error("platform totp lookup failed", "request_id", RequestID(ctx), "err", err.Error(), "user", user)
			fail(http.StatusServiceUnavailable, "unavailable")
			return
		}
		if !totpStatus.Configured {
			secret, err := totp.GenerateSecret()
			if err != nil {
				fail(http.StatusInternalServerError, "internal")
				return
			}
			qr, err := totp.QRCodeBase64(secret, user, "DrawSpace")
			if err != nil {
				fail(http.StatusInternalServerError, "internal")
				return
			}
			setupToken := session.RandomToken(32)
			data := totpSetupData{
				Username: user,
				Secret:   secret,
				Subject:  id.Subject,
				Name:     id.Name,
				Email:    id.Email,
				Roles:    id.Roles,
				IDToken:  id.IDToken,
				ReturnTo: in.ReturnTo,
			}
			if err := s.store.PutTOTPSetup(ctx, setupToken, data, 5*time.Minute); err != nil {
				fail(http.StatusServiceUnavailable, "unavailable")
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":       "totp_setup_required",
				"qr_code":     qr,
				"secret":      secret,
				"setup_token": setupToken,
			})
			return
		}

		code := strings.TrimSpace(in.TOTP)
		if code == "" {
			fail(http.StatusUnauthorized, "totp_required")
			return
		}
		if !totp.Validate(totpStatus.Secret, code) {
			_, _ = s.store.Fail(ctx, "u:"+user, failWindow)
			_, _ = s.store.Fail(ctx, "ip:"+ip, failWindow)
			s.log.Info("ad totp invalid", "request_id", RequestID(ctx), "user", user)
			fail(http.StatusUnauthorized, "invalid_totp", "Kode authenticator tidak valid. Coba lagi.")
			return
		}
	}

	_ = s.store.ClearFail(ctx, "u:"+user)
	now := time.Now()
	roles := id.Roles
	if !slices.Contains(roles, "admin") && !slices.Contains(roles, s.cfg.RequiredRole) {
		roles = append(roles, s.cfg.RequiredRole)
	}
	sess := &session.Session{
		Subject:   id.Subject,
		Username:  id.Username,
		Name:      id.Name,
		Email:     id.Email,
		Roles:     roles,
		CSRF:      session.RandomToken(24),
		IDToken:   id.IDToken,
		Idp:       "sso",
		CreatedAt: now,
		ExpiresAt: now.Add(s.cfg.SessionTTL),
	}
	sid, err := s.store.CreateSession(ctx, sess)
	if err != nil {
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	http.SetCookie(w, s.secureCookie(s.cookie, sid, int(s.cfg.SessionTTL.Seconds())))
	s.log.Info("login ok", "request_id", RequestID(ctx), "actor", id.Subject, "actor_type", identity.ActorHuman, "idp", "sso")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect": safeReturn(in.ReturnTo)})
}

func (s *Server) loginLocal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if s.platform == nil || s.cfg.PlatformUpstream == nil {
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		ReturnTo string `json:"return_to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || in.Username == "" || in.Password == "" || len(in.Username) > 64 || len(in.Password) > 256 {
		fail(http.StatusBadRequest, "bad_request")
		return
	}
	user := strings.ToLower(strings.TrimSpace(in.Username))
	ip := clientIP(r)
	ctx := r.Context()
	for _, k := range []string{"u:" + user, "ip:" + ip} {
		if n, err := s.store.Failures(ctx, k); err != nil {
			fail(http.StatusServiceUnavailable, "unavailable")
			return
		} else if n >= maxLocalFailures*4 && strings.HasPrefix(k, "ip:") || n >= maxLocalFailures && strings.HasPrefix(k, "u:") {
			w.Header().Set("Retry-After", "900")
			s.log.Warn("local sign-in locked", "request_id", RequestID(ctx), "key", strings.SplitN(k, ":", 2)[0])
			fail(http.StatusTooManyRequests, "locked")
			return
		}
	}
	id, err := s.verifyLocal(ctx, user, in.Password)
	switch {
	case errors.Is(err, errBadCredentials):
		_, _ = s.store.Fail(ctx, "u:"+user, failWindow)
		_, _ = s.store.Fail(ctx, "ip:"+ip, failWindow)
		s.log.Info("local sign-in refused", "request_id", RequestID(ctx))
		fail(http.StatusUnauthorized, "invalid_credentials")
		return
	case err != nil:
		s.log.Error("local sign-in backend", "request_id", RequestID(ctx), "err", err.Error())
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	_ = s.store.ClearFail(ctx, "u:"+user)
	now := time.Now()
	sess := &session.Session{Subject: id.Sub, Username: id.Username, Name: id.Name, Email: id.Email, Roles: []string{id.Role},
		CSRF: session.RandomToken(24), Idp: "local", CreatedAt: now, ExpiresAt: now.Add(s.cfg.SessionTTL)}
	sid, err := s.store.CreateSession(ctx, sess)
	if err != nil {
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	http.SetCookie(w, s.secureCookie(s.cookie, sid, int(s.cfg.SessionTTL.Seconds())))
	s.log.Info("login ok", "request_id", RequestID(ctx), "actor", id.Sub, "actor_type", identity.ActorHuman, "idp", "local")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect": safeReturn(in.ReturnTo)})
}

// verifyLocal asks the platform whether the credentials are valid.
func (s *Server) verifyLocal(ctx context.Context, username, password string) (*localIdentity, error) {
	tok, err := s.signer.Sign(identity.Claims{Subject: "gateway-auth", Audience: platformAudience, ActorType: identity.ActorService}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	u := strings.TrimRight(s.cfg.PlatformUpstream.String(), "/") + "/api/v1/internal/auth/local"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, errAuthBackend
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var id localIdentity
		var raw struct{ Sub, Username, Name, Email, Role string }
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&raw); err != nil || raw.Sub == "" || (raw.Role != "user" && raw.Role != "admin") {
			return nil, errAuthBackend
		}
		id = localIdentity(raw)
		return &id, nil
	case http.StatusUnauthorized:
		return nil, errBadCredentials
	}
	return nil, errAuthBackend
}

type platformTOTPStatus struct {
	Configured bool   `json:"configured"`
	Secret     string `json:"secret"`
}

type totpSetupData struct {
	Username string   `json:"username"`
	Secret   string   `json:"secret"`
	Subject  string   `json:"sub"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	IDToken  string   `json:"idToken"`
	ReturnTo string   `json:"returnTo"`
}

func (s *Server) getPlatformTOTP(ctx context.Context, username string) (*platformTOTPStatus, error) {
	if s.platform == nil || s.cfg.PlatformUpstream == nil {
		return nil, errors.New("platform upstream nil")
	}
	tok, err := s.signer.Sign(identity.Claims{Subject: "gateway-auth", Audience: platformAudience, ActorType: identity.ActorService}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(s.cfg.PlatformUpstream.String(), "/") + "/api/v1/internal/totp/" + url.PathEscape(username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("platform totp get returned %d", resp.StatusCode)
	}
	var res platformTOTPStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (s *Server) setPlatformTOTP(ctx context.Context, username, secret string) error {
	if s.platform == nil || s.cfg.PlatformUpstream == nil {
		return errors.New("platform upstream nil")
	}
	tok, err := s.signer.Sign(identity.Claims{Subject: "gateway-auth", Audience: platformAudience, ActorType: identity.ActorService}, 30*time.Second)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"secret": secret})
	u := strings.TrimRight(s.cfg.PlatformUpstream.String(), "/") + "/api/v1/internal/totp/" + url.PathEscape(username)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("platform totp set returned %d", resp.StatusCode)
	}
	return nil
}

func (s *Server) loginADSetupTOTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int, msg string, desc ...string) {
		w.WriteHeader(code)
		res := map[string]string{"error": msg}
		if len(desc) > 0 && desc[0] != "" {
			res["error_description"] = desc[0]
		}
		_ = json.NewEncoder(w).Encode(res)
	}
	var in struct {
		SetupToken string `json:"setup_token"`
		Code       string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || in.SetupToken == "" || in.Code == "" {
		fail(http.StatusBadRequest, "bad_request")
		return
	}
	ctx := r.Context()
	var data totpSetupData
	if err := s.store.TakeTOTPSetup(ctx, in.SetupToken, &data); err != nil || data.Username == "" || data.Secret == "" {
		fail(http.StatusBadRequest, "invalid_or_expired_token", "Sesi setup telah kedaluwarsa. Silakan login kembali.")
		return
	}
	if !totp.Validate(data.Secret, in.Code) {
		_ = s.store.PutTOTPSetup(ctx, in.SetupToken, data, 5*time.Minute)
		fail(http.StatusUnauthorized, "invalid_code", "Kode authenticator tidak valid. Coba lagi.")
		return
	}
	if err := s.setPlatformTOTP(ctx, data.Username, data.Secret); err != nil {
		s.log.Error("failed to persist totp to platform", "request_id", RequestID(ctx), "err", err.Error(), "user", data.Username)
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	_ = s.store.ClearFail(ctx, "u:"+data.Username)
	now := time.Now()
	roles := data.Roles
	if !slices.Contains(roles, "admin") && !slices.Contains(roles, s.cfg.RequiredRole) {
		roles = append(roles, s.cfg.RequiredRole)
	}
	sess := &session.Session{
		Subject:   data.Subject,
		Username:  data.Username,
		Name:      data.Name,
		Email:     data.Email,
		Roles:     roles,
		CSRF:      session.RandomToken(24),
		IDToken:   data.IDToken,
		Idp:       "sso",
		CreatedAt: now,
		ExpiresAt: now.Add(s.cfg.SessionTTL),
	}
	sid, err := s.store.CreateSession(ctx, sess)
	if err != nil {
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	http.SetCookie(w, s.secureCookie(s.cookie, sid, int(s.cfg.SessionTTL.Seconds())))
	s.log.Info("login ok after totp setup", "request_id", RequestID(ctx), "actor", data.Subject, "actor_type", identity.ActorHuman, "idp", "sso")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect": safeReturn(data.ReturnTo)})
}
