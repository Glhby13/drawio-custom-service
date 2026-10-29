package platform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/model"
)

// Per-diagram MCP access tokens (ADR-0016). A token opens exactly one document with at most the role it was created with.
// Only its SHA-256 is stored; the token itself is returned once, to the owner who created it.

// TokenPrefix marks a DrawSpace MCP token (it also lets secret scanners recognise a leaked one).
const TokenPrefix = "dsp_"

const (
	maxTokensPerDoc  = 20
	maxTokenLifetime = 365 * 24 * time.Hour
)

// DocToken is a row of document_tokens (never the secret).
type DocToken struct {
	ID         string     `json:"id"`
	DocumentID string     `json:"documentId,omitempty"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint"` // last characters of the token, to tell tokens apart
	Role       string     `json:"role"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

// newMCPToken returns a fresh token (256 bits of randomness) and its storage hash.
func newMCPToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

const dtCols = `id, document_id, name, hint, role, created_by, created_at, expires_at, last_used_at, revoked_at`

func scanToken(row pgx.Row) (*DocToken, error) {
	var t DocToken
	if err := row.Scan(&t.ID, &t.DocumentID, &t.Name, &t.Hint, &t.Role, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errNotFound
		}
		return nil, err
	}
	return &t, nil
}

// AddToken stores a new token hash.
func (s *Store) AddToken(ctx context.Context, t DocToken, hash string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO document_tokens (id, document_id, name, token_hash, hint, role, created_by, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.DocumentID, t.Name, hash, t.Hint, t.Role, t.CreatedBy, t.ExpiresAt)
	return err
}

// Tokens lists the tokens of a document (newest first), revoked ones included so the owner sees the history.
func (s *Store) Tokens(ctx context.Context, docID string) ([]DocToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+dtCols+` FROM document_tokens WHERE document_id=$1 ORDER BY created_at DESC LIMIT 100`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DocToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		t.DocumentID = ""
		out = append(out, *t)
	}
	return out, rows.Err()
}

// LiveTokenCount counts tokens that can still be used.
func (s *Store) LiveTokenCount(ctx context.Context, docID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM document_tokens WHERE document_id=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, docID).Scan(&n)
	return n, err
}

// GetToken returns one token of a document.
func (s *Store) GetToken(ctx context.Context, docID, id string) (*DocToken, error) {
	return scanToken(s.pool.QueryRow(ctx, `SELECT `+dtCols+` FROM document_tokens WHERE document_id=$1 AND id=$2`, docID, id))
}

// RevokeToken marks a token revoked (idempotent); it reports whether a live token was revoked.
func (s *Store) RevokeToken(ctx context.Context, docID, id, by string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE document_tokens SET revoked_at=now(), revoked_by=$3 WHERE document_id=$1 AND id=$2 AND revoked_at IS NULL`, docID, id, by)
	return tag.RowsAffected() > 0, err
}

// PurgeToken deletes the row of a token that can no longer be used (revoked or expired); it reports whether a row was deleted.
func (s *Store) PurgeToken(ctx context.Context, docID, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM document_tokens WHERE document_id=$1 AND id=$2 AND (revoked_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at <= now()))`, docID, id)
	return tag.RowsAffected() > 0, err
}

// RevokeAllTokens revokes every live token of a document.
func (s *Store) RevokeAllTokens(ctx context.Context, docID, by string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE document_tokens SET revoked_at=now(), revoked_by=$2 WHERE document_id=$1 AND revoked_at IS NULL`, docID, by)
	return tag.RowsAffected(), err
}

// ResolveToken finds a live (not revoked, not expired) token by its hash and notes the use (at most once a minute).
func (s *Store) ResolveToken(ctx context.Context, hash string) (*DocToken, error) {
	t, err := scanToken(s.pool.QueryRow(ctx, `SELECT `+dtCols+` FROM document_tokens WHERE token_hash=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, hash))
	if err != nil {
		return nil, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE document_tokens SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, t.ID)
	return t, nil
}

// ---- API: the owner manages tokens ---------------------------------------------------------------------------------------

type tokenReq struct {
	Name          string `json:"name"`
	Role          string `json:"role"`
	ExpiresInDays int    `json:"expiresInDays"` // 0 = never expires
	EnableAI      bool   `json:"enableAI"`      // switch the diagram's "Allow AI agents" on when it is off
}

// issueToken creates a token. Only the owner may do it (an AI actor never, an application admin neither).
func (s *Server) issueToken(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, role, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	if role != RoleOwner || p.IsAI || p.TokenDoc != "" {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "mcp-token-create", "denied", map[string]string{"role": role})
		writeErr(w, http.StatusForbidden, "forbidden", "only the owner of a diagram can create MCP tokens")
		return
	}
	var in tokenReq
	if !readJSON(w, r, &in, 4096) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 60 {
		writeErr(w, http.StatusBadRequest, "bad_request", "name 1..60 characters")
		return
	}
	if in.Role != RoleViewer && in.Role != RoleCommenter && in.Role != RoleEditor {
		writeErr(w, http.StatusBadRequest, "bad_request", "role must be viewer, commenter or editor")
		return
	}
	if in.ExpiresInDays < 0 || time.Duration(in.ExpiresInDays)*24*time.Hour > maxTokenLifetime {
		writeErr(w, http.StatusBadRequest, "bad_request", "expiresInDays 0..365 (0 = never)")
		return
	}
	if !d.AI.Allowed {
		if !in.EnableAI {
			writeErr(w, http.StatusConflict, "ai_disabled", "AI agents are not allowed on this diagram: allow them first, or pass enableAI")
			return
		}
		pol := AIPolicy{Allowed: true, MaxOpsPerMin: d.AI.MaxOpsPerMin, MaxDelete: d.AI.MaxDelete}
		if pol.MaxOpsPerMin < 1 {
			pol.MaxOpsPerMin = s.cfg.AIDefaultMaxOpsPerMin
		}
		if err := s.store.SetAIPolicy(r.Context(), d.ID, pol); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "ai-policy", "ok", pol)
	}
	if n, err := s.store.LiveTokenCount(r.Context(), d.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	} else if n >= maxTokensPerDoc {
		writeErr(w, http.StatusConflict, "too_many_tokens", "revoke a token before creating another one")
		return
	}
	token, hash, err := newMCPToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	t := DocToken{ID: "t" + newID(6), DocumentID: d.ID, Name: name, Hint: token[len(token)-4:], Role: in.Role, CreatedBy: p.Username}
	if in.ExpiresInDays > 0 {
		exp := time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour).UTC()
		t.ExpiresAt = &exp
	}
	if err := s.store.AddToken(r.Context(), t, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "mcp-token-create", "ok", map[string]any{"tokenId": t.ID, "name": name, "role": in.Role, "expiresAt": t.ExpiresAt})
	s.notifyPerm(d.ID)
	t.CreatedAt = time.Now().UTC()
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "tokenInfo": t, "note": "copy the token now: it cannot be shown again"})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, role, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	if role != RoleOwner || p.IsAI || p.TokenDoc != "" {
		writeErr(w, http.StatusForbidden, "forbidden", "only the owner of a diagram can see its MCP tokens")
		return
	}
	ts, err := s.store.Tokens(r.Context(), d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": ts})
}

// revokeToken: the owner, or an application admin as an emergency switch. With ?purge=1 it deletes the record of a token that is
// already revoked or expired (a live token has to be revoked first); the audit trail keeps the creation, revocation and deletion.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, role, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	if p.IsAI || p.TokenDoc != "" || (role != RoleOwner && !p.IsAdmin) {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "mcp-token-revoke", "denied", map[string]string{"role": role})
		writeErr(w, http.StatusForbidden, "forbidden", "only the owner can revoke MCP tokens")
		return
	}
	id := r.PathValue("tid")
	t, err := s.store.GetToken(r.Context(), d.ID, id)
	if err != nil {
		if errors.Is(err, errNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "token not found")
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", "")
		}
		return
	}
	if r.URL.Query().Get("purge") == "1" {
		gone, err := s.store.PurgeToken(r.Context(), d.ID, id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
		if !gone {
			writeErr(w, http.StatusConflict, "token_live", "revoke the token before deleting it")
			return
		}
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "mcp-token-delete", "ok", map[string]string{"tokenId": id, "name": t.Name})
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return
	}
	if _, err := s.store.RevokeToken(r.Context(), d.ID, id, p.Username); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "mcp-token-revoke", "ok", map[string]string{"tokenId": id})
	s.notifyPerm(d.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// ---- internal: the MCP server asks who a token belongs to --------------------------------------------------------------

// authMCP accepts only the service token the MCP server mints for itself (the platform has no other way to learn what a
// "dsp_" token is; the MCP server holds no database credentials, ADR-0008).
func (s *Server) authMCP(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return false
	}
	c, err := s.verify.Verify(tok, Audience)
	return err == nil && c.ActorType == identity.ActorService && c.Issuer == "drawio-mcp" && c.Subject == "mcp-resolver"
}

func (s *Server) resolveToken(w http.ResponseWriter, r *http.Request) {
	if !s.authMCP(r) {
		writeErr(w, http.StatusForbidden, "forbidden", "MCP service token required")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &in, 1024) {
		return
	}
	deny := func(why string) {
		_ = s.store.Audit(r.Context(), model.Actor{ID: "drawio-mcp", Type: identity.ActorService}, "", "mcp-token-use", "denied", map[string]string{"why": why})
		writeErr(w, http.StatusUnauthorized, "invalid_token", "token not accepted")
	}
	if !strings.HasPrefix(in.Token, TokenPrefix) || len(in.Token) > 128 {
		deny("format")
		return
	}
	t, err := s.store.ResolveToken(r.Context(), hashToken(in.Token))
	if err != nil {
		if errors.Is(err, errNotFound) {
			deny("unknown, revoked or expired")
		} else {
			writeErr(w, http.StatusInternalServerError, "internal", "")
		}
		return
	}
	d, err := s.store.GetDocument(r.Context(), t.DocumentID)
	if err != nil || !d.AI.Allowed {
		deny("diagram missing or AI not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokenId": t.ID, "name": t.Name, "documentId": d.ID, "role": minRole(t.Role, RoleEditor), "owner": d.Owner})
}
