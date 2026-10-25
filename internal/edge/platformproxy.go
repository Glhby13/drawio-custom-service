package edge

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/session"
)

// platformAudience is the `aud` of the internal token the platform accepts.
const platformAudience = "platform"

// newPlatformProxy forwards /api/v1/* and /ws/v1/* (REST and WebSocket upgrades) to the platform. The browser's
// cookies are dropped; the platform only ever sees a short-lived signed internal token minted per request (ADR-0004).
func newPlatformProxy(upstream *url.URL, log *slog.Logger) http.Handler {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &httputil.ReverseProxy{
		Transport:     tr,
		FlushInterval: -1, // stream: WebSocket frames and large exports are never buffered
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("X-Csrf-Token")
			pr.Out.Header.Del("Origin") // the edge already verified it; the platform trusts the token, not the origin
			pr.SetXForwarded()
		},
		ModifyResponse: func(resp *http.Response) error {
			h := resp.Header
			h.Del("Server")
			h.Del("Set-Cookie")
			h.Set("X-Content-Type-Options", "nosniff")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("platform upstream error", "request_id", RequestID(r.Context()), "err", err.Error())
			http.Error(w, "platform unavailable", http.StatusBadGateway)
		},
	}
}

func (s *Server) platformAPI(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256([]byte(sess.Subject + sess.CreatedAt.String()))
	tok, err := s.signer.Sign(identity.Claims{
		Subject: sess.Subject, Audience: platformAudience, ActorType: identity.ActorHuman,
		Username: sess.Username, Name: sess.Name, Roles: sess.Roles, SessionID: hex.EncodeToString(sum[:6]),
	}, time.Minute)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	r.Header.Del("Authorization") // never trust a client-supplied one
	r.Header.Set("Authorization", "Bearer "+tok)
	s.platform.ServeHTTP(w, r)
}
