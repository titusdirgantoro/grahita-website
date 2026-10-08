// Package postgres adalah implementasi repository pengaturan di PostgreSQL.
//
// Penyimpanan perubahan dilakukan dalam satu transaksi bersama catatan
// auditnya: bila salah satu gagal, tidak ada pengaturan yang berubah dan tidak
// ada event audit yang tertinggal (SystemDesign.md §5.4).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
	"github.com/grahita/portal/api/internal/settings"
)

// Repo adalah repository pengaturan berbasis query yang dibangkitkan sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository pengaturan.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// List memuat seluruh pengaturan.
func (r *Repo) List(ctx context.Context) ([]settings.Value, error) {
	rows, err := r.q.ListSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListSettings: %w", err)
	}
	out := make([]settings.Value, 0, len(rows))
	for _, row := range rows {
		out = append(out, settings.Value{Key: row.Key, Value: row.Value, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

// Get memuat satu pengaturan.
func (r *Repo) Get(ctx context.Context, key string) ([]byte, error) {
	raw, err := r.q.GetSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, settings.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query GetSetting: %w", err)
	}
	return raw, nil
}

// Save menyimpan seluruh perubahan beserta catatan auditnya dalam satu
// transaksi. updated_by diisi dari aktor mutasi.
func (r *Repo) Save(ctx context.Context, actorID string, changes []settings.Change, recs []audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi pengaturan: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	for _, c := range changes {
		if err := q.UpsertSetting(ctx, sqlc.UpsertSettingParams{
			Key:       c.Key,
			Value:     c.Value,
			UpdatedBy: pgtypes.UUID(actorID),
		}); err != nil {
			return fmt.Errorf("query UpsertSetting %s: %w", c.Key, err)
		}
	}
	for _, rec := range recs {
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pengaturan: %w", err)
	}
	return nil
}
