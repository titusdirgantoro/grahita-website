package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/ocrsecret"
	"github.com/grahita/portal/api/internal/platform/database"
)

type Repo struct{ pool *database.Pool }

func New(pool *database.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) Load(ctx context.Context) (ocrsecret.Record, error) {
	var out ocrsecret.Record
	err := r.pool.QueryRow(ctx, `SELECT ciphertext,nonce,version,verified_at,updated_at
FROM ocr_credentials WHERE singleton=true`).Scan(&out.Ciphertext, &out.Nonce,
		&out.Version, &out.VerifiedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ocrsecret.ErrNotConfigured
	}
	return out, err
}

func lockAIInactive(ctx context.Context, tx pgx.Tx) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT COALESCE((value->>'aktif')::boolean,false)
FROM settings WHERE key='ai' FOR UPDATE`).Scan(&active)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if active {
		return ocrsecret.ErrAIActive
	}
	return nil
}

func (r *Repo) Save(ctx context.Context, actor auth.Actor, rec ocrsecret.Record) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAIInactive(ctx, tx); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ocr_credentials(singleton,ciphertext,nonce,updated_by)
VALUES(true,$1,$2,$3)
ON CONFLICT(singleton) DO UPDATE SET ciphertext=EXCLUDED.ciphertext,
nonce=EXCLUDED.nonce,version=ocr_credentials.version+1,verified_at=NULL,
updated_at=now(),updated_by=EXCLUDED.updated_by`, rec.Ciphertext, rec.Nonce, actor.UserID)
	if err != nil {
		return fmt.Errorf("simpan kunci OCR terenkripsi: %w", err)
	}
	if err := auditpg.Insert(ctx, tx, audit.NewEntry(ctx, actor, "ocr_credential", "", audit.ActionUpdate,
		nil, map[string]any{"provider": "gemini", "key_configured": true, "key_verified": false})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repo) MarkVerified(ctx context.Context, actor auth.Actor, version int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE ocr_credentials SET verified_at=now(),updated_at=now(),updated_by=$2
WHERE singleton=true AND version=$1`, version, actor.UserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ocrsecret.ErrStale
	}
	if err := auditpg.Insert(ctx, tx, audit.NewEntry(ctx, actor, "ocr_credential", "", audit.ActionVerify,
		nil, map[string]any{"provider": "gemini", "key_verified": true})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repo) Delete(ctx context.Context, actor auth.Actor) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAIInactive(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ocr_credentials WHERE singleton=true`); err != nil {
		return err
	}
	if err := auditpg.Insert(ctx, tx, audit.NewEntry(ctx, actor, "ocr_credential", "", audit.ActionDelete,
		nil, map[string]any{"provider": "gemini", "key_configured": false})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
