// Package postgres menuliskan entri audit ke tabel audit_logs.
//
// Penulisan dilakukan lewat Execer sehingga entri dapat disimpan di dalam
// transaksi mutasi (pgx.Tx) maupun langsung pada pool — keduanya memenuhi
// interface yang sama (SystemDesign.md §5.4).
package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/audit"
)

// Execer adalah bagian dari pgx yang dibutuhkan penulis audit.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// insertSQL menyimpan satu entri audit. Kolom kosong dikirim sebagai NULL
// (actor_id/entity_id/ip) supaya cocok dengan tipe kolomnya.
const insertSQL = `
INSERT INTO audit_logs
       (actor_kind, actor_id, actor_nama, ip, entity, entity_id, action, before, after)
VALUES ($1::actor_kind, $2::uuid, $3::text, $4::inet, $5::text, $6::uuid, $7::text, $8::jsonb, $9::jsonb)`

// Insert menyimpan satu entri audit memakai executor yang diberikan.
func Insert(ctx context.Context, db Execer, e audit.Entry) error {
	if e.Entity == "" || e.Action == "" || e.ActorID == "" {
		return fmt.Errorf("entri audit tidak lengkap: entity=%q action=%q actor=%q", e.Entity, e.Action, e.ActorID)
	}
	if e.ActorKind == "" {
		return fmt.Errorf("entri audit tanpa jenis aktor (entity=%s action=%s)", e.Entity, e.Action)
	}
	before, err := jsonOrNil(e.Before)
	if err != nil {
		return fmt.Errorf("audit %s %s: %w", e.Entity, e.Action, err)
	}
	after, err := jsonOrNil(e.After)
	if err != nil {
		return fmt.Errorf("audit %s %s: %w", e.Entity, e.Action, err)
	}
	if _, err := db.Exec(ctx, insertSQL,
		string(e.ActorKind), nullIfEmpty(e.ActorID), nullIfEmpty(e.ActorNama), nullIfEmpty(e.IP),
		e.Entity, nullIfEmpty(e.EntityID), e.Action, before, after); err != nil {
		return fmt.Errorf("catat audit %s %s: %w", e.Entity, e.Action, err)
	}
	return nil
}

// jsonOrNil mengubah snapshot menjadi JSON; nil tetap NULL di database.
func jsonOrNil(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot audit: %w", err)
	}
	return string(b), nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
