package edge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyActor
)

var reqIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// actorHolder lets the access log learn who the request belonged to after the handler authenticated it.
type actorHolder struct{ sub string }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.status == 0 {
		w.status = c
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap keeps http.ResponseController (flush, hijack for WebSocket upgrades) working through the wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RequestID returns the id attached to the request context.
func RequestID(ctx context.Context) string { s, _ := ctx.Value(keyRequestID).(string); return s }

// observe assigns a request id and writes one structured access log line. The query string is never
// logged (it carries the authorization code and state on /auth/callback).
func observe(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !reqIDPattern.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		holder := &actorHolder{}
		ctx := context.WithValue(context.WithValue(r.Context(), keyRequestID, id), keyActor, holder)
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(ctx))
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}
		log.Info("request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "actor", holder.sub, "actor_type", actorTypeOf(holder.sub))
	})
}

func actorTypeOf(sub string) string {
	if sub == "" {
		return ""
	}
	return "human"
}

// pageHeaders applies the gateway CSP and friends to everything the gateway serves itself.
func pageHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'self'; object-src 'none'; frame-ancestors 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Cache-Control", "no-store")
}

// clientIP uses X-Forwarded-For only when the direct peer is private (the ingress); otherwise the peer address.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a, err := netip.ParseAddr(host); err == nil && (a.IsPrivate() || a.IsLoopback()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if _, err := netip.ParseAddr(first); err == nil {
				return first
			}
		}
	}
	return host
}

type limiter struct {
	mu    sync.Mutex
	m     map[string]*entry
	rps   rate.Limit
	burst int
}

type entry struct {
	l    *rate.Limiter
	seen time.Time
}

func newLimiter(rps float64, burst int) *limiter {
	return &limiter{m: map[string]*entry{}, rps: rate.Limit(rps), burst: burst}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.m) > 10000 {
		for k, e := range l.m {
			if now.Sub(e.seen) > 5*time.Minute {
				delete(l.m, k)
			}
		}
	}
	e, ok := l.m[key]
	if !ok {
		e = &entry{l: rate.NewLimiter(l.rps, l.burst)}
		l.m[key] = e
	}
	e.seen = now
	return e.l.Allow()
}
