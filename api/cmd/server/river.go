package main

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
)

// migrateRiver membuat/memperbarui tabel internal River (river_job, dll).
// Dijalankan setelah migrasi goose agar worker langsung bisa dipakai.
func migrateRiver(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return err
	}
	versi := 0
	for _, v := range res.Versions {
		if v.Version > versi {
			versi = v.Version
		}
	}
	logger.Info("migrasi river selesai", "migrasi_diterapkan", len(res.Versions), "versi_terakhir", versi)
	return nil
}
