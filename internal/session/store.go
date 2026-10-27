// Package session keeps browser sessions and in-flight login state server-side (ADR-0004). The cookie
// carries only an opaque random id; Redis holds the data under sha256(id), so a Redis dump cannot be
// replayed as a cookie.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound means there is no (valid) session or login state for the key.
var ErrNotFound = errors.New("not found")

// Session is the server-side record of a logged-in browser.
type Session struct {
	Subject   string    `json:"sub"`
	Username  string    `json:"username"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Roles     []string  `json:"roles"`
	CSRF      string    `json:"csrf"`
	IDToken   string    `json:"idToken"`
	Idp       string    `json:"idp,omitempty"` // "local" or "sso"
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"` // absolute limit
}

// Login is the state of one authorization-code attempt.
type Login struct {
	Nonce       string `json:"nonce"`
	Verifier    string `json:"verifier"`
	ReturnTo    string `json:"returnTo"`
	BindingHash string `json:"bindingHash"` // sha256 of the login-binding cookie
}

// Store is the persistence port; Redis is the only production implementation.
type Store interface {
	CreateSession(ctx context.Context, s *Session) (id string, err error)
	GetSession(ctx context.Context, id string) (*Session, error)
	DeleteSession(ctx context.Context, id string) error
	PutLogin(ctx context.Context, state string, l Login) error
	TakeLogin(ctx context.Context, state string) (Login, error)
	Ping(ctx context.Context) error
	// Fail counts one failed local sign-in under key (window = how long the counter lives); Failures reads it; ClearFail resets it.
	Fail(ctx context.Context, key string, window time.Duration) (int64, error)
	Failures(ctx context.Context, key string) (int64, error)
	ClearFail(ctx context.Context, key string) error
	PutTOTPSetup(ctx context.Context, token string, data any, ttl time.Duration) error
	TakeTOTPSetup(ctx context.Context, token string, out any) error
}

// Redis implements Store.
type Redis struct {
	c     *redis.Client
	idle  time.Duration
	login time.Duration
	now   func() time.Time
}

// NewRedis wraps a client. idle is the sliding session timeout; the absolute limit is Session.ExpiresAt.
func NewRedis(c *redis.Client, idle time.Duration) *Redis {
	return &Redis{c: c, idle: idle, login: 10 * time.Minute, now: time.Now}
}

// WithClock replaces the time source (tests: the absolute session limit must follow the same clock as Redis TTLs).
func (r *Redis) WithClock(now func() time.Time) *Redis { r.now = now; return r }

// RandomToken returns n random bytes, base64url without padding.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("crypto/rand unavailable: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash returns the hex sha256 used for Redis keys and cookie binding.
func Hash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

func sessKey(id string) string  { return "drawio:sess:" + Hash(id) }
func loginKey(st string) string { return "drawio:login:" + Hash(st) }

// CreateSession stores s and returns the opaque cookie value.
func (r *Redis) CreateSession(ctx context.Context, s *Session) (string, error) {
	id := RandomToken(32)
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	ttl := min(r.idle, s.ExpiresAt.Sub(r.now()))
	if ttl <= 0 {
		return "", errors.New("session already expired")
	}
	if err := r.c.Set(ctx, sessKey(id), b, ttl).Err(); err != nil {
		return "", err
	}
	return id, nil
}

// GetSession returns the session and slides the idle timeout, never past the absolute limit.
func (r *Redis) GetSession(ctx context.Context, id string) (*Session, error) {
	if id == "" || len(id) > 128 {
		return nil, ErrNotFound
	}
	b, err := r.c.Get(ctx, sessKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, ErrNotFound
	}
	remaining := s.ExpiresAt.Sub(r.now())
	if remaining <= 0 {
		_ = r.c.Del(ctx, sessKey(id)).Err()
		return nil, ErrNotFound
	}
	if err := r.c.Expire(ctx, sessKey(id), min(r.idle, remaining)).Err(); err != nil {
		return nil, err
	}
	return &s, nil
}

// DeleteSession removes the session (logout).
func (r *Redis) DeleteSession(ctx context.Context, id string) error {
	return r.c.Del(ctx, sessKey(id)).Err()
}

// PutLogin stores the login state for 10 minutes.
func (r *Redis) PutLogin(ctx context.Context, state string, l Login) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return r.c.Set(ctx, loginKey(state), b, r.login).Err()
}

// TakeLogin returns and deletes the login state in one step: a state value works exactly once.
func (r *Redis) TakeLogin(ctx context.Context, state string) (Login, error) {
	b, err := r.c.GetDel(ctx, loginKey(state)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Login{}, ErrNotFound
	}
	if err != nil {
		return Login{}, err
	}
	var l Login
	if err := json.Unmarshal(b, &l); err != nil {
		return Login{}, ErrNotFound
	}
	return l, nil
}

// Ping checks connectivity (readiness).
func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

func failKey(k string) string { return "drawio:lf:" + Hash(k) }

// Fail increments the failed-sign-in counter of key and (re)arms its expiry on the first failure.
func (r *Redis) Fail(ctx context.Context, key string, window time.Duration) (int64, error) {
	n, err := r.c.Incr(ctx, failKey(key)).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		_ = r.c.Expire(ctx, failKey(key), window).Err()
	}
	return n, nil
}

// Failures returns the current failed-sign-in count (0 when none).
func (r *Redis) Failures(ctx context.Context, key string) (int64, error) {
	n, err := r.c.Get(ctx, failKey(key)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

// ClearFail forgets the counter after a successful sign-in.
func (r *Redis) ClearFail(ctx context.Context, key string) error {
	return r.c.Del(ctx, failKey(key)).Err()
}

func totpSetupKey(token string) string { return "drawio:totp_setup:" + Hash(token) }

// PutTOTPSetup stores transient TOTP enrollment data for an administrator.
func (r *Redis) PutTOTPSetup(ctx context.Context, token string, data any, ttl time.Duration) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return r.c.Set(ctx, totpSetupKey(token), b, ttl).Err()
}

// TakeTOTPSetup retrieves and removes the transient TOTP setup data (one-time use).
func (r *Redis) TakeTOTPSetup(ctx context.Context, token string, out any) error {
	b, err := r.c.GetDel(ctx, totpSetupKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
