// Pembacaan audit log dari PostgreSQL (kartu F2-28).
//
// Berkas ini melengkapi penulis (postgres.go) dengan dua hal:
//   - pembaca berhalaman (List/Count) yang dipakai service audit, dan
//   - Record, penulis satu entri untuk mutasi yang transaksinya dimiliki
//     domain lain (mis. penerbitan tautan atur ulang di paket auth).
package postgres

import (
	"context"
	"fmt"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
)

// Repo membaca dan menulis audit_logs.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository audit.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// List mengembalikan satu batch audit log (urutan `at DESC, id DESC`).
//
// Isi jsonb diurai menjadi bentuk generik; pemanggil (service) yang
// meredaksinya kembali sebelum dikirim ke klien.
func (r *Repo) List(ctx context.Context, q audit.Query) ([]audit.Log, error) {
	rows, err := r.q.ListAuditLogs(ctx, listParams(q))
	if err != nil {
		return nil, fmt.Errorf("query ListAuditLogs: %w", err)
	}
	out := make([]audit.Log, 0, len(rows))
	for _, row := range rows {
		before, err := audit.Snapshot(row.Before)
		if err != nil {
			return nil, fmt.Errorf("urai snapshot before %s: %w", row.ID, err)
		}
		after, err := audit.Snapshot(row.After)
		if err != nil {
			return nil, fmt.Errorf("urai snapshot after %s: %w", row.ID, err)
		}
		out = append(out, audit.Log{
			ID:        row.ID,
			At:        row.At.UTC(),
			ActorKind: row.ActorKind,
			ActorID:   row.ActorID,
			ActorNama: row.ActorNama,
			IP:        row.Ip,
			Entity:    row.Entity,
			EntityID:  row.EntityID,
			Action:    row.Action,
			Before:    before,
			After:     after,
		})
	}
	return out, nil
}

// Count mengembalikan jumlah seluruh baris yang cocok dengan filter.
func (r *Repo) Count(ctx context.Context, q audit.Query) (int64, error) {
	total, err := r.q.CountAuditLogs(ctx, sqlc.CountAuditLogsParams{
		Entitas:   q.Entitas,
		Aksi:      q.Aksi,
		ActorID:   q.ActorID,
		ActorNama: q.ActorNama,
		Dari:      q.Dari,
		Sampai:    q.Sampai,
	})
	if err != nil {
		return 0, fmt.Errorf("query CountAuditLogs: %w", err)
	}
	return total, nil
}

// Record menyimpan satu entri audit lewat pool (tanpa transaksi domain lain).
// Dipakai mutasi yang perubahannya berada di repositori domain lain.
func (r *Repo) Record(ctx context.Context, e audit.Entry) error {
	return Insert(ctx, r.pool, e)
}

// listParams menyusun parameter query daftar; entitas kosong dikirim sebagai
// array kosong (bukan NULL) supaya `cardinality(...) = 0` berperilaku sama.
func listParams(q audit.Query) sqlc.ListAuditLogsParams {
	entitas := q.Entitas
	if entitas == nil {
		entitas = []string{}
	}
	return sqlc.ListAuditLogsParams{
		Entitas:    entitas,
		Aksi:       q.Aksi,
		ActorID:    q.ActorID,
		ActorNama:  q.ActorNama,
		Dari:       q.Dari,
		Sampai:     q.Sampai,
		OffsetRows: int32(q.Offset),
		LimitRows:  int32(q.Batch),
	}
}
