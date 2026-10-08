// Package database menyediakan koneksi PostgreSQL dan menjalankan migrasi
// goose dari file SQL yang di-embed ke binary.
//
// Satu binary memakai pool pgx untuk melayani request, dan koneksi
// database/sql (driver pgx stdlib) khusus untuk goose.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // driver "pgx" untuk database/sql (goose)
	"github.com/pressly/goose/v3"

	"github.com/grahita/portal/api/db/migrations"
)

// Pool adalah alias agar paket domain tidak mengimpor pgx secara langsung.
type Pool = pgxpool.Pool

// MigrateDir adalah direktori di dalam embedded FS tempat file migrasi berada.
const MigrateDir = "."

// Connect membuka pool pgx dan menunggu database benar-benar siap.
//
// Postgres di Docker Compose kadang menerima koneksi sesaat sebelum siap
// melayani, jadi ping diulang dengan backoff singkat.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
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

type gooseLogger struct{}

func (gooseLogger) Printf(format string, v ...any) {
	slog.Info("goose", "msg", fmt.Sprintf(format, v...))
}

func (gooseLogger) Fatalf(format string, v ...any) {
	slog.Error("goose", "msg", fmt.Sprintf(format, v...))
}

func open(ctx context.Context, url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("buka koneksi migrasi: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping migrasi: %w", err)
	}
	goose.SetLogger(gooseLogger{})
	if err := goose.SetDialect("postgres"); err != nil {
		_ = db.Close()
		return nil, err
	}
	fss, err := fs.Sub(migrations.FS, ".")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	goose.SetBaseFS(fss)
	return db, nil
}

// Up menjalankan seluruh migrasi yang belum diterapkan.
func Up(ctx context.Context, url string) error {
	db, err := open(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return goose.UpContext(ctx, db, MigrateDir)
}

// DownTo menurunkan skema sampai versi tertentu (0 = kosong). Dipakai
// pengujian "migrasi bersih" untuk memastikan setiap migrasi punya Down.
func DownTo(ctx context.Context, url string, version int64) error {
	db, err := open(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return goose.DownToContext(ctx, db, MigrateDir, version)
}

// Version mengembalikan versi migrasi terakhir yang sudah diterapkan.
func Version(ctx context.Context, url string) (int64, error) {
	db, err := open(ctx, url)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	return goose.GetDBVersionContext(ctx, db)
}
