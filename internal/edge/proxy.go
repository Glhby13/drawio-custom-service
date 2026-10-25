package edge

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// newEditorProxy forwards /editor/* to the unmodified draw.io without stripping the path (the image serves
// under /editor because DRAWIO_SERVER_URL says so). The browser's cookies and any Authorization header are
// removed: draw.io must never see the session, and it never receives identity.
func newEditorProxy(upstream *url.URL, log *slog.Logger) http.Handler {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &httputil.ReverseProxy{
		Transport: tr,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Csrf-Token")
			pr.SetXForwarded()
		},
		ModifyResponse: func(resp *http.Response) error {
			h := resp.Header
			h.Del("Server")
			h.Del("X-Powered-By")
			h.Del("Set-Cookie") // draw.io is stateless; a stray JSESSIONID must not reach the browser
			h.Set("X-Frame-Options", "SAMEORIGIN")
			// Additive: draw.io keeps its own meta CSP; this adds the framing rule it cannot express (ADR-0001).
			h.Add("Content-Security-Policy", "frame-ancestors 'self'")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error("editor upstream error", "request_id", RequestID(r.Context()), "err", err.Error())
			code := http.StatusBadGateway
			if errors.Is(err, r.Context().Err()) && r.Context().Err() != nil {
				code = 499
			}
			http.Error(w, "editor unavailable", code)
		},
	}
}
