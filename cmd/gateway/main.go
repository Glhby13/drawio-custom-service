// Command gateway is the internet-facing edge of drawio-custom-service (ADR-0005).
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/edge"
	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/session"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("gateway stopped", "err", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.LoadGateway(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return fmt.Errorf("EXTRA_CA_FILE: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("EXTRA_CA_FILE contains no certificate")
		}
	}
	rc := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Username: cfg.RedisUser, Password: cfg.RedisPassword,
		DialTimeout: 3 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second})
	defer func() { _ = rc.Close() }()
	store := session.NewRedis(rc, cfg.SessionIdle)
	oc := edge.NewOIDC(cfg.IssuerURL, cfg.ClientID, cfg.ClientSecret, cfg.PublicURL.String()+"/auth/callback", pool)
	signer := identity.NewSigner(cfg.InternalKey, cfg.InternalKeyID, "drawio-gateway")
	srv, err := edge.New(cfg, log, store, oc, signer)
	if err != nil {
		return err
	}
	hs := &http.Server{Addr: ":" + cfg.Port, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("gateway listening", "addr", hs.Addr, "public_url", cfg.PublicURL.String())
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
