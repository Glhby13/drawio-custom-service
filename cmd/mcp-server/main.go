// Command mcp-server is the MCP edge of drawio-custom-service (ADR-0008): AI agents connect with a per-diagram access token
// ("dsp_…", ADR-0016) or, when MCP_OAUTH_ENABLED=true, an OAuth 2.1 bearer token from Keycloak, and work on diagrams
// through the same platform API and authorisation as people.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/mcpsrv"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("mcp-server stopped", "err", err.Error())
		os.Exit(1)
	}
}

func need(k string) (string, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return "", fmt.Errorf("%s is required", k)
	}
	return v, nil
}

func run(log *slog.Logger) error {
	var errs []error
	get := func(k string) string { v, err := need(k); errs = append(errs, err); return v }
	oauth := strings.EqualFold(os.Getenv("MCP_OAUTH_ENABLED"), "true") // off by default: per-diagram tokens only (ADR-0016)
	publicURL, platform, seedB64 := strings.TrimRight(get("PUBLIC_URL"), "/"), get("PLATFORM_UPSTREAM"), get("INTERNAL_KEY_SEED")
	var issuer, audience, appClient string
	if oauth {
		issuer, audience, appClient = strings.TrimRight(get("ISSUER_URL"), "/"), get("MCP_AUDIENCE"), get("APP_CLIENT_ID")
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	for _, u := range []string{publicURL, issuer} {
		if u == "" {
			continue
		}
		if pu, err := url.Parse(u); err != nil || pu.Scheme != "https" {
			return fmt.Errorf("%q must be an https URL", u)
		}
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("INTERNAL_KEY_SEED must be base64 of 32 bytes")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if f := os.Getenv("EXTRA_CA_FILE"); f != "" {
		pem, err := os.ReadFile(f) //nolint:gosec // operator-provided CA bundle path (EXTRA_CA_FILE)
		if err != nil {
			return fmt.Errorf("EXTRA_CA_FILE: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("EXTRA_CA_FILE contains no certificate")
		}
	}
	kid := os.Getenv("INTERNAL_KEY_ID")
	if kid == "" {
		kid = "mcp-1"
	}
	role := os.Getenv("REQUIRED_ROLE")
	if role == "" {
		role = "user"
	}
	cfg := mcpsrv.Config{PublicURL: publicURL, IssuerURL: issuer, PlatformURL: platform, RequiredRole: role, Log: log,
		Signer: identity.NewSigner(ed25519.NewKeyFromSeed(seed), kid, "drawio-mcp")}
	if oauth {
		cfg.Verifier = mcpsrv.NewKeycloakVerifier(issuer, audience, appClient, pool, tls.VersionTLS12)
	}
	srv := mcpsrv.New(cfg)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	hs := &http.Server{Addr: ":" + port, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("mcp-server listening", "addr", hs.Addr, "resource", publicURL+"/mcp")
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
