// Package edge is the gateway: OIDC authentication, server-side sessions, CSRF/origin checks, rate
// limiting, security headers and the reverse proxy to the unmodified draw.io (ADR-0004). It performs a
// COARSE check only (valid session + client role). Which document a user may open is decided by the
// domain PDP in later phases; nothing here is an authorization source of truth.
package edge

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/session"
)

//go:embed web
var webFS embed.FS

// Server wires the gateway.
type Server struct {
	cfg    config.Gateway
	log    *slog.Logger
	store  session.Store
	oidc   *OIDC
	signer *identity.Signer
	lim    *limiter
	proxy  http.Handler
	static fs.FS
	cookie string
	bind   string
}

// New builds a Server.
func New(cfg config.Gateway, log *slog.Logger, store session.Store, o *OIDC, signer *identity.Signer) (*Server, error) {
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: log, store: store, oidc: o, signer: signer, static: static,
		lim: newLimiter(cfg.RateRPS, cfg.RateBurst), cookie: "__Host-drawio_sid", bind: "__Host-drawio_login"}
	if cfg.InsecureCookie {
		s.cookie, s.bind = "drawio_sid", "drawio_login"
	}
	s.proxy = newEditorProxy(cfg.DrawioUpstream, log)
	return s, nil
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /login", s.limited(s.login))
	mux.HandleFunc("GET /auth/callback", s.limited(s.callback))
	mux.HandleFunc("GET /logged-out", s.loggedOut)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /logout", s.limited(s.logout))
	mux.HandleFunc("GET /api/me", s.limited(s.authed(s.me)))
	mux.HandleFunc("GET /static/", s.authed(s.staticFile))
	mux.HandleFunc("GET /{$}", s.authed(s.hostPage))
	// Every method reaches the proxy so the CSRF/origin rule applies uniformly; draw.io itself is mostly GET.
	mux.Handle("/editor/", s.authedHandler(s.editor))
	return observe(s.log, s.csrfOrigin(mux))
}

func (s *Server) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.lim.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.oidc.Ready(); err != nil {
		s.log.Warn("not ready: oidc discovery failed", "err", err.Error())
		http.Error(w, "identity provider unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ready"))
}

// csrfOrigin rejects state-changing requests that do not originate from this site. SameSite=Lax already
// blocks cross-site POSTs with the cookie; this is the second layer (ADR-0004) and also guards WebSocket
// upgrades added in later phases (they are GET with an Origin header).
func (s *Server) csrfOrigin(next http.Handler) http.Handler {
	origin := s.cfg.PublicURL.Scheme + "://" + s.cfg.PublicURL.Host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		upgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if unsafe || upgrade {
			o := r.Header.Get("Origin")
			if o == "" && unsafe {
				if ref := r.Header.Get("Referer"); strings.HasPrefix(ref, origin+"/") {
					o = origin
				}
			}
			if o != origin {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		}
		next.ServeHTTP(w, r)
	})
}

type authedFunc func(w http.ResponseWriter, r *http.Request, sess *session.Session)

func (s *Server) authed(next authedFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.authedHandler(next).ServeHTTP(w, r) }
}

func (s *Server) authedHandler(next authedFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cookie)
		if err != nil {
			s.unauthenticated(w, r)
			return
		}
		sess, err := s.store.GetSession(r.Context(), c.Value)
		switch {
		case errors.Is(err, session.ErrNotFound):
			s.clearCookie(w)
			s.unauthenticated(w, r)
			return
		case err != nil:
			// Fail closed when the session store is down: never serve or redirect-loop.
			s.log.Error("session store error", "request_id", RequestID(r.Context()), "err", err.Error())
			http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
			return
		}
		if h, ok := r.Context().Value(keyActor).(*actorHolder); ok {
			h.sub = sess.Subject
		}
		next(w, r, sess)
	})
}

func isNavigation(r *http.Request) bool {
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate" && r.Method == http.MethodGet
	}
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (s *Server) unauthenticated(w http.ResponseWriter, r *http.Request) {
	if isNavigation(r) {
		rt := r.URL.Path
		if r.URL.RawQuery != "" {
			rt += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(rt), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthenticated","login":"/login"}`))
}

// safeReturn only accepts a same-site absolute path.
func safeReturn(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return "/"
	}
	if u, err := url.Parse(p); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return p
}

func (s *Server) secureCookie(name, value string, maxAge int) *http.Cookie {
	//nolint:gosec // Secure is false only with INSECURE_COOKIE, which config refuses together with an https PUBLIC_URL
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: !s.cfg.InsecureCookie, SameSite: http.SameSiteLaxMode}
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, s.secureCookie(s.cookie, "", -1))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	state, nonce, verifier, binding := session.RandomToken(24), session.RandomToken(24), session.RandomToken(48), session.RandomToken(24)
	if err := s.store.PutLogin(r.Context(), state, session.Login{Nonce: nonce, Verifier: verifier, ReturnTo: safeReturn(r.URL.Query().Get("return_to")), BindingHash: session.Hash(binding)}); err != nil {
		s.log.Error("login state store", "request_id", RequestID(r.Context()), "err", err.Error())
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	u, err := s.oidc.AuthURL(state, nonce, verifier)
	if err != nil {
		s.log.Error("oidc unavailable", "request_id", RequestID(r.Context()), "err", err.Error())
		http.Error(w, "identity provider unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, s.secureCookie(s.bind, binding, 600))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	http.SetCookie(w, s.secureCookie(s.bind, "", -1))
	if q.Get("error") != "" {
		s.log.Info("login refused by idp", "request_id", RequestID(r.Context()), "error", q.Get("error"))
		http.Error(w, "login failed", http.StatusForbidden)
		return
	}
	state, code := q.Get("state"), q.Get("code")
	if state == "" || code == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	st, err := s.store.TakeLogin(r.Context(), state)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			http.Error(w, "login expired or replayed", http.StatusBadRequest)
		} else {
			http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	// Login CSRF: the state must come back in the same browser that started the login.
	bc, err := r.Cookie(s.bind)
	if err != nil || subtle.ConstantTimeCompare([]byte(session.Hash(bc.Value)), []byte(st.BindingHash)) != 1 {
		http.Error(w, "login state does not belong to this browser", http.StatusBadRequest)
		return
	}
	id, err := s.oidc.Exchange(r.Context(), code, st.Verifier, st.Nonce)
	if err != nil {
		s.log.Warn("login verification failed", "request_id", RequestID(r.Context()), "err", err.Error())
		http.Error(w, "login failed", http.StatusForbidden)
		return
	}
	if !slices.Contains(id.Roles, s.cfg.RequiredRole) && !slices.Contains(id.Roles, "admin") {
		s.log.Info("login denied: missing client role", "request_id", RequestID(r.Context()), "actor", id.Subject)
		http.Error(w, "access denied: your account has no access to this application", http.StatusForbidden)
		return
	}
	now := time.Now()
	sess := &session.Session{Subject: id.Subject, Username: id.Username, Name: id.Name, Email: id.Email, Roles: id.Roles,
		CSRF: session.RandomToken(24), IDToken: id.IDToken, CreatedAt: now, ExpiresAt: now.Add(s.cfg.SessionTTL)}
	sid, err := s.store.CreateSession(r.Context(), sess)
	if err != nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, s.secureCookie(s.cookie, sid, int(s.cfg.SessionTTL.Seconds())))
	s.log.Info("login ok", "request_id", RequestID(r.Context()), "actor", id.Subject, "actor_type", identity.ActorHuman)
	http.Redirect(w, r, safeReturn(st.ReturnTo), http.StatusFound)
}

func (s *Server) csrfOK(r *http.Request, sess *session.Session) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRF)) == 1
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	redirect := "/logged-out"
	if c, err := r.Cookie(s.cookie); err == nil {
		if sess, err := s.store.GetSession(r.Context(), c.Value); err == nil {
			if !s.csrfOK(r, sess) {
				http.Error(w, "csrf token required", http.StatusForbidden)
				return
			}
			if u, err := s.oidc.EndSessionURL(sess.IDToken, s.cfg.PublicURL.String()+"/logged-out"); err == nil {
				redirect = u
			}
			if err := s.store.DeleteSession(r.Context(), c.Value); err != nil {
				http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
				return
			}
			s.log.Info("logout", "request_id", RequestID(r.Context()), "actor", sess.Subject)
		}
	}
	s.clearCookie(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect": redirect})
}

func (s *Server) loggedOut(w http.ResponseWriter, _ *http.Request) {
	pageHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Signed out</title><p>You have been signed out. <a href="/login">Sign in again</a></p>`))
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, sess *session.Session) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sub": sess.Subject, "username": sess.Username, "name": sess.Name,
		"email": sess.Email, "roles": sess.Roles, "csrf": sess.CSRF, "expiresAt": sess.ExpiresAt})
}

func (s *Server) hostPage(w http.ResponseWriter, r *http.Request, _ *session.Session) {
	pageHeaders(w.Header())
	http.ServeFileFS(w, r, s.static, "host.html")
}

func (s *Server) staticFile(w http.ResponseWriter, r *http.Request, _ *session.Session) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	if name == "" || strings.Contains(name, "/") || name == "host.html" {
		http.NotFound(w, r)
		return
	}
	pageHeaders(w.Header())
	// name is a single path element (no "/") looked up in an embedded read-only FS, never the host filesystem.
	http.ServeFileFS(w, r, s.static, name) //nolint:gosec // see comment above
}

func (s *Server) editor(w http.ResponseWriter, r *http.Request, _ *session.Session) {
	s.proxy.ServeHTTP(w, r)
}
