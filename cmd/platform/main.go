// Command platform is the drawio-custom-service "platform" deployable: documents, authorization, the sequenced
// operation log, snapshots/versions, realtime collaboration, comments and audit (ADR-0005).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/platform"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := &http.Client{Timeout: 3 * time.Second}
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
		r, err := c.Do(req)
		if err != nil {
			os.Exit(1)
		}
		_ = r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", "platform")
	cfg, err := config.LoadPlatform(os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "err", err.Error())
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := platform.OpenPG(ctx, cfg)
	if err != nil {
		log.Error("postgres", "err", err.Error())
		os.Exit(1)
	}
	defer pool.Close()
	if os.Getenv("MIGRATE") != "false" {
		if err := platform.Migrate(ctx, pool); err != nil {
			log.Error("migrate", "err", err.Error())
			os.Exit(1)
		}
	}
	if err := platform.CheckSchema(ctx, pool); err != nil {
		log.Error("schema", "err", err.Error())
		os.Exit(1)
	}
	blob, err := platform.NewBlob(cfg)
	if err != nil {
		log.Error("s3", "err", err.Error())
		os.Exit(1)
	}
	ro := &redis.Options{Addr: cfg.RedisAddr, Username: cfg.RedisUser, Password: cfg.RedisPassword}
	if cfg.CAFile != "" && os.Getenv("REDIS_TLS") == "true" {
		pem, _ := os.ReadFile(cfg.CAFile)
		pool2, _ := x509.SystemCertPool()
		if pool2 == nil {
			pool2 = x509.NewCertPool()
		}
		pool2.AppendCertsFromPEM(pem)
		ro.TLSConfig = &tls.Config{RootCAs: pool2, MinVersion: tls.VersionTLS12}
	}
	rdb := redis.NewClient(ro)
	defer func() { _ = rdb.Close() }()

	srv := platform.New(cfg, log, platform.NewStore(pool), blob, rdb)
	srv.Start(ctx)
	hs := &http.Server{Addr: ":" + cfg.Port, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		log.Info("platform listening", "addr", hs.Addr, "schema", platform.SchemaVersion)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err.Error())
			stop()
		}
	}()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
}
