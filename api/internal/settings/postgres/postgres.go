// Package postgres adalah implementasi repository pengaturan di PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
	"github.com/grahita/portal/api/internal/settings"
)

// Repo adalah repository pengaturan berbasis query yang dibangkitkan sqlc.
type Repo struct {
	q *sqlc.Queries
}

// New membuat repository pengaturan.
func New(pool *database.Pool) *Repo { return &Repo{q: sqlc.New(pool)} }

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

// Upsert menyimpan satu pengaturan.
func (r *Repo) Upsert(ctx context.Context, key string, value []byte, updatedBy string) error {
	err := r.q.UpsertSetting(ctx, sqlc.UpsertSettingParams{
		Key:       key,
		Value:     value,
		UpdatedBy: pgtypes.UUID(updatedBy),
	})
	if err != nil {
		return fmt.Errorf("query UpsertSetting: %w", err)
	}
	return nil
}
