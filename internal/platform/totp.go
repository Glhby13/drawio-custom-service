package platform

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Glhby13/drawio-custom-service/internal/totp"
	"github.com/jackc/pgx/v5"
)

// GetAdminTOTP reads the TOTP secret for an administrator from admin_totp.
func (s *Store) GetAdminTOTP(ctx context.Context, username string) (string, error) {
	var secret string
	err := s.pool.QueryRow(ctx, "SELECT secret FROM admin_totp WHERE username = $1", strings.ToLower(username)).Scan(&secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return secret, err
}

// SetAdminTOTP stores or updates the TOTP secret for an administrator.
func (s *Store) SetAdminTOTP(ctx context.Context, username, secret string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO admin_totp (username, secret, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (username) DO UPDATE SET secret = EXCLUDED.secret, updated_at = now()
	`, strings.ToLower(username), strings.TrimSpace(secret))
	return err
}

// totpGetInternal is called by drawio-gateway to check whether an admin has enrolled TOTP.
func (s *Server) totpGetInternal(w http.ResponseWriter, r *http.Request) {
	if !s.authService(r) {
		writeErr(w, http.StatusForbidden, "forbidden", "service token required")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "username required")
		return
	}
	secret, err := s.store.GetAdminTOTP(r.Context(), name)
	if err != nil {
		s.log.Error("get admin totp", "err", err.Error(), "user", name)
		writeErr(w, http.StatusInternalServerError, "internal", "database error")
		return
	}
	if secret == "" {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "secret": secret})
}

// totpSetInternal is called by drawio-gateway to persist TOTP during initial AD enrollment.
func (s *Server) totpSetInternal(w http.ResponseWriter, r *http.Request) {
	if !s.authService(r) {
		writeErr(w, http.StatusForbidden, "forbidden", "service token required")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "username required")
		return
	}
	var in struct {
		Secret string `json:"secret"`
	}
	if !readJSON(w, r, &in, 4096) || in.Secret == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "secret required")
		return
	}
	if err := s.store.SetAdminTOTP(r.Context(), name, in.Secret); err != nil {
		s.log.Error("set admin totp", "err", err.Error(), "user", name)
		writeErr(w, http.StatusInternalServerError, "internal", "database error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// meTOTPSetup initiates an interactive TOTP enrollment or reset from the canvas user menu.
func (s *Server) meTOTPSetup(w http.ResponseWriter, _ *http.Request, p *Principal) {
	if !s.requireAdmin(w, p) {
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "failed to generate totp secret")
		return
	}
	qr, err := totp.QRCodeBase64(secret, p.Username, "DrawSpace")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "failed to generate qr code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret":  secret,
		"qr_code": qr,
	})
}

// meTOTPVerify validates a 6-digit code for a newly scanned TOTP secret and saves it.
func (s *Server) meTOTPVerify(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.requireAdmin(w, p) {
		return
	}
	var in struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if !readJSON(w, r, &in, 4096) || in.Secret == "" || in.Code == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "secret and code required")
		return
	}
	if !totp.Validate(in.Secret, in.Code) {
		writeErr(w, http.StatusBadRequest, "invalid_code", "Kode authenticator tidak valid. Coba lagi.")
		return
	}
	if err := s.store.SetAdminTOTP(r.Context(), p.Username, in.Secret); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "database error")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, "", "admin-totp-reset", "ok", map[string]string{"username": p.Username})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
