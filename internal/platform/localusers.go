package platform

// Local accounts: DrawSpace users that do not come from SSO. An application admin creates them (or the first admin is
// bootstrapped from a Secret), they sign in with a password on the DrawSpace login page, and from then on they are ordinary
// principals: the gateway puts them in a session and mints the same internal token, with idp="local". They collaborate with SSO
// users through the same share list (usernames are unique across both worlds, enforced here and in authn).

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/model"
)

var (
	usernameRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)
	errLocalCollision  = errors.New("username belongs to a local DrawSpace account")
	errLocalNotAllowed = errors.New("local account disabled or removed")
)

const (
	// argon2id parameters (OWASP minimum profile: 19 MiB, 2 iterations, 1 lane) - stored in each hash so they can be raised later
	argonTime, argonMem, argonThreads, argonKey = 2, 19 * 1024, 1, 32
	minPassword, maxPassword                    = 10, 128
)

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	k := argon2.IDKey([]byte(pw), salt, argonTime, argonMem, argonThreads, argonKey)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMem, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(k)), nil
}

func verifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m > 256*1024 || t > 10 || p == 0 || p > 8 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want))) //nolint:gosec // len(want) <= 64 is checked above
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash makes "unknown user" cost the same as "wrong password".
var dummyHash = func() string { h, _ := hashPassword("not-a-real-password"); return h }()

func validPassword(pw, username string) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < minPassword:
		return fmt.Errorf("password must be at least %d characters", minPassword)
	case n > maxPassword:
		return fmt.Errorf("password must be at most %d characters", maxPassword)
	case strings.EqualFold(pw, username):
		return errors.New("password must not be the username")
	}
	return nil
}

// ---- store ---------------------------------------------------------------------------------------------------------

// LocalUser is a local account without its hash.
type LocalUser struct {
	Username  string     `json:"username"`
	Name      string     `json:"name"`
	Email     string     `json:"email"`
	Role      string     `json:"role"`
	Disabled  bool       `json:"disabled"`
	CreatedAt time.Time  `json:"createdAt"`
	CreatedBy string     `json:"createdBy"`
	LastLogin *time.Time `json:"lastLogin,omitempty"`
}

type localRow struct {
	LocalUser
	hash string
}

var errNoLocalUser = errors.New("no such local user")

func (s *Store) LocalGet(ctx context.Context, username string) (*localRow, error) {
	var r localRow
	err := s.pool.QueryRow(ctx, `SELECT username, display_name, email, role, disabled, created_at, created_by, last_login, pass_hash FROM local_users WHERE username=$1`, username).
		Scan(&r.Username, &r.Name, &r.Email, &r.Role, &r.Disabled, &r.CreatedAt, &r.CreatedBy, &r.LastLogin, &r.hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoLocalUser
	}
	return &r, err
}

func (s *Store) LocalList(ctx context.Context) ([]LocalUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT username, display_name, email, role, disabled, created_at, created_by, last_login FROM local_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LocalUser{}
	for rows.Next() {
		var u LocalUser
		if err := rows.Scan(&u.Username, &u.Name, &u.Email, &u.Role, &u.Disabled, &u.CreatedAt, &u.CreatedBy, &u.LastLogin); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) LocalCreate(ctx context.Context, u LocalUser, hash string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO local_users (username, display_name, email, role, pass_hash, created_by) VALUES ($1,$2,$3,$4,$5,$6)`, u.Username, u.Name, u.Email, u.Role, hash, u.CreatedBy)
	return err
}

func (s *Store) LocalCountAdmins(ctx context.Context, except string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM local_users WHERE role='admin' AND NOT disabled AND username <> $1`, except).Scan(&n)
	return n, err
}

func (s *Store) LocalAnyAdmin(ctx context.Context) (bool, error) {
	n, err := s.LocalCountAdmins(ctx, "")
	return n > 0, err
}

func (s *Store) LocalTouch(ctx context.Context, username string) {
	_, _ = s.pool.Exec(ctx, `UPDATE local_users SET last_login=now() WHERE username=$1`, username)
}

// SSOUserExists reports a known SSO principal with that username (kind human or ai, not a local account).
func (s *Store) SSOUserExists(ctx context.Context, username string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username=$1 AND sub NOT LIKE 'local:%'`, username).Scan(&n)
	return n > 0, err
}

func (s *Store) OwnedDocCount(ctx context.Context, username string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE owner=$1 AND deleted_at IS NULL`, username).Scan(&n)
	return n, err
}

// ---- state cache used by authn ------------------------------------------------------------------------------------------

type localState struct {
	exists, disabled bool
	role             string
	at               time.Time
}

type localCache struct {
	mu sync.Mutex
	m  map[string]localState
}

const localCacheTTL = 15 * time.Second

func (s *Server) localState(ctx context.Context, username string) (localState, error) {
	s.local.mu.Lock()
	if st, ok := s.local.m[username]; ok && time.Since(st.at) < localCacheTTL {
		s.local.mu.Unlock()
		return st, nil
	}
	s.local.mu.Unlock()
	st := localState{at: time.Now()}
	r, err := s.store.LocalGet(ctx, username)
	switch {
	case err == nil:
		st.exists, st.disabled, st.role = true, r.Disabled, r.Role
	case errors.Is(err, errNoLocalUser):
	default:
		return localState{}, err
	}
	s.local.mu.Lock()
	if s.local.m == nil || len(s.local.m) > 5000 {
		s.local.m = map[string]localState{}
	}
	s.local.m[username] = st
	s.local.mu.Unlock()
	return st, nil
}

func (s *Server) forgetLocal(username string) {
	s.local.mu.Lock()
	delete(s.local.m, username)
	s.local.mu.Unlock()
}

// ---- service endpoint: the gateway verifies a login ------------------------------------------------------------------------

// authService accepts only a service token minted by the gateway (never a browser-derived token, never the MCP edge).
func (s *Server) authService(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return false
	}
	c, err := s.verify.Verify(tok, Audience)
	return err == nil && c.ActorType == identity.ActorService && c.Issuer == "drawio-gateway" && c.Subject == "gateway-auth"
}

func (s *Server) localVerify(w http.ResponseWriter, r *http.Request) {
	if !s.authService(r) {
		writeErr(w, http.StatusForbidden, "forbidden", "service token required")
		return
	}
	var in struct{ Username, Password string }
	if !readJSON(w, r, &in, 4096) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(in.Username))
	row, err := s.store.LocalGet(r.Context(), name)
	hash := dummyHash
	if err == nil {
		hash = row.hash
	}
	okPw := verifyPassword(hash, in.Password)
	if err != nil || !okPw || row.Disabled {
		if err != nil && !errors.Is(err, errNoLocalUser) {
			writeErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
		_ = s.store.Audit(r.Context(), model.Actor{ID: "gateway", Type: identity.ActorService}, "", "local-login", "denied", map[string]string{"username": name})
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	s.store.LocalTouch(r.Context(), name)
	_ = s.store.UpsertUser(r.Context(), "local:"+name, name, row.Name, row.Email, "human")
	_ = s.store.Audit(r.Context(), model.Actor{ID: name, Type: identity.ActorHuman}, "", "local-login", "ok", map[string]string{})
	writeJSON(w, http.StatusOK, map[string]any{"sub": "local:" + name, "username": name, "name": row.Name, "email": row.Email, "role": row.Role})
}

// ---- admin + self service --------------------------------------------------------------------------------------------------------

func (s *Server) requireAdmin(w http.ResponseWriter, p *Principal) bool {
	if !p.IsAdmin || p.IsAI {
		writeErr(w, http.StatusForbidden, "forbidden", "application administrators only")
		return false
	}
	return true
}

func (s *Server) listLocalUsers(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.requireAdmin(w, p) {
		return
	}
	us, err := s.store.LocalList(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": us})
}

func (s *Server) createLocalUser(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.requireAdmin(w, p) {
		return
	}
	var in struct{ Username, Name, Email, Role, Password string }
	if !readJSON(w, r, &in, 8192) {
		return
	}
	u := LocalUser{Username: strings.ToLower(strings.TrimSpace(in.Username)), Name: strings.TrimSpace(in.Name), Email: strings.TrimSpace(in.Email), Role: in.Role, CreatedBy: p.Username}
	switch {
	case !usernameRe.MatchString(u.Username):
		writeErr(w, http.StatusBadRequest, "bad_request", "username: 3-32 characters, a-z 0-9 . _ - , starting with a letter or digit")
		return
	case u.Role != "user" && u.Role != "admin":
		writeErr(w, http.StatusBadRequest, "bad_request", "role must be user or admin")
		return
	case len(u.Name) > 80 || len(u.Email) > 120:
		writeErr(w, http.StatusBadRequest, "bad_request", "name/email too long")
		return
	}
	if u.Name == "" {
		u.Name = u.Username
	}
	if err := validPassword(in.Password, u.Username); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_password", err.Error())
		return
	}
	if taken, err := s.store.SSOUserExists(r.Context(), u.Username); err != nil || taken {
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
		writeErr(w, http.StatusConflict, "username_taken", "that username belongs to an SSO account")
		return
	}
	h, err := hashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if err := s.store.LocalCreate(r.Context(), u, h); err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeErr(w, http.StatusConflict, "username_taken", "that username already exists")
			return
		}
		s.log.Error("create local user", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.UpsertUser(r.Context(), "local:"+u.Username, u.Username, u.Name, u.Email, "human")
	s.forgetLocal(u.Username)
	_ = s.store.Audit(r.Context(), p.Actor, "", "local-user.create", "ok", map[string]string{"username": u.Username, "role": u.Role})
	writeJSON(w, http.StatusCreated, map[string]any{"user": u})
}

func (s *Server) patchLocalUser(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.requireAdmin(w, p) {
		return
	}
	name := r.PathValue("username")
	var in struct {
		Name, Email, Role, Password *string
		Disabled                    *bool
	}
	if !readJSON(w, r, &in, 8192) {
		return
	}
	row, err := s.store.LocalGet(r.Context(), name)
	if errors.Is(err, errNoLocalUser) {
		writeErr(w, http.StatusNotFound, "not_found", "no such local user")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	newRole, newDisabled := row.Role, row.Disabled
	if in.Role != nil {
		if *in.Role != "user" && *in.Role != "admin" {
			writeErr(w, http.StatusBadRequest, "bad_request", "role must be user or admin")
			return
		}
		newRole = *in.Role
	}
	if in.Disabled != nil {
		newDisabled = *in.Disabled
	}
	if row.Role == "admin" && !row.Disabled && (newRole != "admin" || newDisabled) {
		if n, err := s.store.LocalCountAdmins(r.Context(), name); err != nil || n == 0 {
			writeErr(w, http.StatusConflict, "last_admin", "this is the last enabled local administrator")
			return
		}
	}
	if name == p.Username && (newDisabled || newRole != row.Role) {
		writeErr(w, http.StatusConflict, "self", "you cannot disable or demote your own account")
		return
	}
	display, email := row.Name, row.Email
	if in.Name != nil {
		display = strings.TrimSpace(*in.Name)
	}
	if in.Email != nil {
		email = strings.TrimSpace(*in.Email)
	}
	if len(display) > 80 || len(email) > 120 || display == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "name 1-80 characters, email up to 120")
		return
	}
	hash := row.hash
	if in.Password != nil && *in.Password != "" {
		if err := validPassword(*in.Password, name); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_password", err.Error())
			return
		}
		if hash, err = hashPassword(*in.Password); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
	}
	if _, err := s.store.pool.Exec(r.Context(), `UPDATE local_users SET display_name=$2, email=$3, role=$4, disabled=$5, pass_hash=$6, pass_changed_at = CASE WHEN pass_hash <> $6 THEN now() ELSE pass_changed_at END WHERE username=$1`,
		name, display, email, newRole, newDisabled, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.UpsertUser(r.Context(), "local:"+name, name, display, email, "human")
	s.forgetLocal(name)
	_ = s.store.Audit(r.Context(), p.Actor, "", "local-user.update", "ok", map[string]any{"username": name, "role": newRole, "disabled": newDisabled, "passwordReset": hash != row.hash})
	writeJSON(w, http.StatusOK, map[string]any{"user": LocalUser{Username: name, Name: display, Email: email, Role: newRole, Disabled: newDisabled, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy, LastLogin: row.LastLogin}})
}

func (s *Server) deleteLocalUser(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.requireAdmin(w, p) {
		return
	}
	name := r.PathValue("username")
	row, err := s.store.LocalGet(r.Context(), name)
	if errors.Is(err, errNoLocalUser) {
		writeErr(w, http.StatusNotFound, "not_found", "no such local user")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if name == p.Username {
		writeErr(w, http.StatusConflict, "self", "you cannot delete your own account")
		return
	}
	if row.Role == "admin" && !row.Disabled {
		if n, err := s.store.LocalCountAdmins(r.Context(), name); err != nil || n == 0 {
			writeErr(w, http.StatusConflict, "last_admin", "this is the last enabled local administrator")
			return
		}
	}
	if n, err := s.store.OwnedDocCount(r.Context(), name); err != nil || n > 0 {
		writeErr(w, http.StatusConflict, "owns_documents", fmt.Sprintf("this user owns %d diagram(s): disable the account instead, or delete the diagrams first", n))
		return
	}
	_, _ = s.store.pool.Exec(r.Context(), `DELETE FROM permissions WHERE subject=$1`, name)
	_, _ = s.store.pool.Exec(r.Context(), `DELETE FROM users WHERE sub=$1`, "local:"+name)
	if _, err := s.store.pool.Exec(r.Context(), `DELETE FROM local_users WHERE username=$1`, name); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	s.forgetLocal(name)
	_ = s.store.Audit(r.Context(), p.Actor, "", "local-user.delete", "ok", map[string]string{"username": name})
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// changeOwnPassword lets a local account change its own password (SSO accounts do that at their identity provider).
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request, p *Principal) {
	if p.Idp != "local" {
		writeErr(w, http.StatusBadRequest, "not_local", "this account signs in with SSO: change the password at your identity provider")
		return
	}
	var in struct{ Current, New string }
	if !readJSON(w, r, &in, 4096) {
		return
	}
	row, err := s.store.LocalGet(r.Context(), p.Username)
	if err != nil || !verifyPassword(row.hash, in.Current) {
		writeErr(w, http.StatusForbidden, "invalid_credentials", "current password is wrong")
		return
	}
	if err := validPassword(in.New, p.Username); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_password", err.Error())
		return
	}
	h, err := hashPassword(in.New)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if _, err := s.store.pool.Exec(r.Context(), `UPDATE local_users SET pass_hash=$2, pass_changed_at=now() WHERE username=$1`, p.Username, h); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, "", "local-user.password", "ok", map[string]string{})
	writeJSON(w, http.StatusOK, map[string]bool{"changed": true})
}

// EnsureLocalAdmin creates the first local administrator from LOCAL_ADMIN_* when no enabled local admin exists yet.
func (s *Server) EnsureLocalAdmin(ctx context.Context) error {
	if s.cfg.LocalAdminPassword == "" {
		return nil
	}
	any, err := s.store.LocalAnyAdmin(ctx)
	if err != nil || any {
		return err
	}
	name := strings.ToLower(s.cfg.LocalAdminUser)
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("LOCAL_ADMIN_USERNAME %q is not a valid username", name)
	}
	if err := validPassword(s.cfg.LocalAdminPassword, name); err != nil {
		return fmt.Errorf("LOCAL_ADMIN_PASSWORD: %w", err)
	}
	if taken, err := s.store.SSOUserExists(ctx, name); err != nil || taken {
		if err != nil {
			return err
		}
		return fmt.Errorf("bootstrap admin %q collides with an SSO account", name)
	}
	if _, err := s.store.LocalGet(ctx, name); err == nil { // exists but disabled / demoted: do not silently overwrite
		return fmt.Errorf("local user %q exists but is not an enabled admin; fix it from the UI or the database", name)
	}
	h, err := hashPassword(s.cfg.LocalAdminPassword)
	if err != nil {
		return err
	}
	if err := s.store.LocalCreate(ctx, LocalUser{Username: name, Name: "Local Administrator", Role: "admin", CreatedBy: "bootstrap"}, h); err != nil {
		return err
	}
	_ = s.store.UpsertUser(ctx, "local:"+name, name, "Local Administrator", "", "human")
	_ = s.store.Audit(ctx, model.Actor{ID: "bootstrap", Type: identity.ActorService}, "", "local-user.bootstrap", "ok", map[string]string{"username": name})
	s.log.Info("bootstrapped the first local administrator", "username", name)
	return nil
}
