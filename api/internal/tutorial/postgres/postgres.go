// Package postgres adalah implementasi repository konten tutorial di
// PostgreSQL.
//
// Seluruh perubahan (tambah/ubah/hapus) dan catatan auditnya ditulis dalam
// SATU transaksi: bila satu langkah gagal, tidak ada konten yang berubah dan
// tidak ada event audit yang tertinggal (SystemDesign.md §5.4).
package postgres

import (
	"context"
	"fmt"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
	"github.com/grahita/portal/api/internal/tutorial"
)

// Repo mengimplementasikan tutorial.Repository di atas PostgreSQL.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository konten tutorial.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// List memuat seluruh topik tutorial sesuai urutan tampil.
func (r *Repo) List(ctx context.Context) ([]tutorial.Topic, error) {
	rows, err := r.q.ListTutorialTopics(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListTutorialTopics: %w", err)
	}
	out := make([]tutorial.Topic, 0, len(rows))
	for _, row := range rows {
		langkah, err := tutorial.LangkahDariJSON(row.Langkah)
		if err != nil {
			return nil, err
		}
		out = append(out, tutorial.Topic{
			ID:        row.ID,
			Ikon:      row.Ikon,
			Judul:     row.Judul,
			Ringkas:   row.Ringkas,
			Langkah:   langkah,
			Urutan:    int(row.Urutan),
			UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

// Apply menyimpan tambah/ubah/hapus beserta catatan auditnya dalam satu
// transaksi. updated_by diisi aktor mutasi (Superadmin) pada setiap tulisan.
func (r *Repo) Apply(ctx context.Context, actorID string, tambah, ubah []tutorial.Topic, hapus []string, recs []audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi tutorial: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	for _, t := range tambah {
		langkah, err := tutorial.LangkahJSON(t.Langkah)
		if err != nil {
			return err
		}
		if _, err := q.InsertTutorialTopic(ctx, sqlc.InsertTutorialTopicParams{
			Ikon:      t.Ikon,
			Judul:     t.Judul,
			Ringkas:   t.Ringkas,
			Langkah:   langkah,
			Urutan:    int32(t.Urutan),
			UpdatedBy: pgtypes.UUID(actorID),
		}); err != nil {
			return fmt.Errorf("query InsertTutorialTopic: %w", err)
		}
	}
	for _, t := range ubah {
		langkah, err := tutorial.LangkahJSON(t.Langkah)
		if err != nil {
			return err
		}
		n, err := q.UpdateTutorialTopic(ctx, sqlc.UpdateTutorialTopicParams{
			Ikon:      t.Ikon,
			Judul:     t.Judul,
			Ringkas:   t.Ringkas,
			Langkah:   langkah,
			Urutan:    int32(t.Urutan),
			UpdatedBy: pgtypes.UUID(actorID),
			ID:        t.ID,
		})
		if err != nil {
			return fmt.Errorf("query UpdateTutorialTopic %s: %w", t.ID, err)
		}
		if n == 0 {
			// Topik hilang di antara pembacaan dan penulisan (mis. dihapus
			// permintaan lain): batalkan seluruh perubahan.
			return tutorial.ErrNotFound
		}
	}
	for _, id := range hapus {
		n, err := q.DeleteTutorialTopic(ctx, id)
		if err != nil {
			return fmt.Errorf("query DeleteTutorialTopic %s: %w", id, err)
		}
		if n == 0 {
			return tutorial.ErrNotFound
		}
	}
	for _, rec := range recs {
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tutorial: %w", err)
	}
	return nil
}
