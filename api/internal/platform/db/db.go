// Package db menyediakan koneksi PostgreSQL dan migrasi sederhana
// berbasis file SQL yang di-embed ke dalam binary.
package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grahita/portal/api/migrations"
)

// Pool adalah alias agar paket lain tidak perlu mengimpor pgx langsung.
type Pool = pgxpool.Pool

// Connect membuka connection pool dan memastikan database benar-benar siap.
// Postgres di Docker kadang menerima koneksi sesaat sebelum siap melayani,
// jadi ping diulang beberapa kali.
func Connect(ctx context.Context, url string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("buat pool: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= 15; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		lastErr = pool.Ping(pingCtx)
		cancel()
		if lastErr == nil {
			return pool, nil
		}
		slog.Warn("menunggu database", "attempt", attempt, "err", lastErr)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	pool.Close()
	return nil, fmt.Errorf("database tidak merespons: %w", lastErr)
}

// Migrate menjalankan semua file .sql di folder migrations secara berurutan.
// Setiap file dijalankan dalam satu transaksi dan dicatat di schema_migrations,
// sehingga aman dijalankan berulang kali.
func Migrate(ctx context.Context, pool *Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// Advisory lock supaya dua instance tidak bermigrasi bersamaan.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(728411)`); err != nil {
		return fmt.Errorf("ambil lock migrasi: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(728411)`)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("buat schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)

	for _, name := range entries {
		if applied[name] {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}

		slog.Info("menjalankan migrasi", "file", name)
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrasi %s gagal: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migrasi %s: %w", name, err)
		}
	}
	return nil
}
