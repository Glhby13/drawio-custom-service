package platform

import (
	"context"
	"embed"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Glhby13/drawio-custom-service/internal/config"
)

//go:embed migrations/*.sql
var migFS embed.FS

// SchemaVersion is the schema the code needs; the platform refuses to start on an older schema (ADR-0007).
const SchemaVersion = 2

// OpenPG opens the pool.
func OpenPG(ctx context.Context, cfg config.Platform) (*pgxpool.Pool, error) {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s&pool_max_conns=24", url.QueryEscape(cfg.PGUser), url.QueryEscape(cfg.PGPassword), cfg.PGHost, cfg.PGPort, cfg.PGDB, cfg.PGSSLMode)
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	return pool, pool.Ping(ctx)
}

// Migrate applies the embedded migrations under a session advisory lock so several replicas can start together.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(7342001)`); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(7342001)`) }()
	var cur int
	var reg *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('schema_version')::text`).Scan(&reg); err != nil {
		return err
	}
	if reg != nil {
		if err := conn.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM schema_version`).Scan(&cur); err != nil {
			return err
		}
	}
	entries, err := migFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		v, err := strconv.Atoi(strings.SplitN(n, "_", 2)[0])
		if err != nil || v <= cur {
			continue
		}
		sqlText, _ := migFS.ReadFile("migrations/" + n)
		// simple protocol: the file holds many statements; wrap them in one transaction
		if _, err := conn.Conn().PgConn().Exec(ctx, "BEGIN;\n"+string(sqlText)+"\nCOMMIT;").ReadAll(); err != nil {
			_, _ = conn.Conn().PgConn().Exec(context.Background(), "ROLLBACK").ReadAll()
			return fmt.Errorf("migration %s: %w", n, err)
		}
	}
	return nil
}

// CheckSchema fails when the database is older than the code.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var v int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM schema_version`).Scan(&v); err != nil {
		return fmt.Errorf("schema_version unreadable: %w", err)
	}
	if v < SchemaVersion {
		return fmt.Errorf("database schema_version %d < required %d: apply the DDL first", v, SchemaVersion)
	}
	return nil
}
