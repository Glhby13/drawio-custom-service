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
