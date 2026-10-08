// Package postgres adalah implementasi repository cadangan di PostgreSQL.
//
// Repository ini hanya menyentuh tabel yang memang miliknya (`backup_runs`)
// plus pembacaan data pendukung lewat query: email Superadmin aktif, kunci
// objek bukti transfer dan dokumen, serta penulisan entri audit. Tidak ada
// domain lain yang dibaca langsung dari sini.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/backup"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository cadangan berbasis query hasil generate sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository cadangan.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// Mulai menyisipkan baris backup_runs (selesai/sukses NULL).
func (r *Repo) Mulai(ctx context.Context, jenis string, mulai time.Time) (backup.Run, error) {
	row, err := r.q.InsertBackupRun(ctx, sqlc.InsertBackupRunParams{
		Jenis: jenis,
		Mulai: mulai,
	})
	if err != nil {
		return backup.Run{}, fmt.Errorf("query InsertBackupRun: %w", err)
	}
	return runDari(row.ID, row.Jenis, row.Mulai, row.Selesai, row.Sukses, row.UkuranByte, row.Tujuan, row.Pesan), nil
}

// Selesai menutup baris backup_runs.
func (r *Repo) Selesai(ctx context.Context, id string, selesai time.Time, sukses bool, ukuranByte int64, tujuan, pesan string) error {
	err := r.q.SelesaiBackupRun(ctx, sqlc.SelesaiBackupRunParams{
		ID:         id,
		Selesai:    &selesai,
		Sukses:     pgtype.Bool{Bool: sukses, Valid: true},
		UkuranByte: pgtype.Int8{Int64: ukuranByte, Valid: true},
		Tujuan:     pgtypes.Text(tujuan),
		Pesan:      pgtypes.Text(pesan),
	})
	if err != nil {
		return fmt.Errorf("query SelesaiBackupRun: %w", err)
	}
	return nil
}

// Terakhir mengembalikan run terbaru.
func (r *Repo) Terakhir(ctx context.Context, limit int) ([]backup.Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.ListBackupRuns(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("query ListBackupRuns: %w", err)
	}
	out := make([]backup.Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, runDari(row.ID, row.Jenis, row.Mulai, row.Selesai, row.Sukses, row.UkuranByte, row.Tujuan, row.Pesan))
	}
	return out, nil
}

// Sejak melaporkan run satu jenis yang mulai sejak waktu tertentu.
func (r *Repo) Sejak(ctx context.Context, jenis string, sejak time.Time) (backup.Run, bool, error) {
	row, err := r.q.BackupRunSejak(ctx, sqlc.BackupRunSejakParams{Jenis: jenis, Mulai: sejak})
	if errors.Is(err, pgx.ErrNoRows) {
		return backup.Run{}, false, nil
	}
	if err != nil {
		return backup.Run{}, false, fmt.Errorf("query BackupRunSejak: %w", err)
	}
	return runDari(row.ID, row.Jenis, row.Mulai, row.Selesai, row.Sukses, row.UkuranByte, row.Tujuan, row.Pesan), true, nil
}

// SuperadminEmails mengembalikan email superadmin aktif yang sudah diaktivasi.
func (r *Repo) SuperadminEmails(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListSuperadminEmails(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListSuperadminEmails: %w", err)
	}
	return rows, nil
}

// KunciProofs mengembalikan storage_key baris payment_proofs.
func (r *Repo) KunciProofs(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListKunciBukti(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListKunciBukti: %w", err)
	}
	return rows, nil
}

// KunciDokumen mengembalikan storage_key baris documents.
func (r *Repo) KunciDokumen(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListKunciDokumen(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListKunciDokumen: %w", err)
	}
	return rows, nil
}

// Audit menyimpan satu entri audit.
func (r *Repo) Audit(ctx context.Context, e audit.Entry) error {
	return auditpg.Insert(ctx, r.pool, e)
}

// runDari memetakan kolom backup_runs ke tipe domain.
func runDari(id, jenis string, mulai time.Time, selesai *time.Time, sukses pgtype.Bool, ukuranByte pgtype.Int8, tujuan, pesan pgtype.Text) backup.Run {
	run := backup.Run{ID: id, Jenis: jenis, Mulai: mulai, Selesai: selesai}
	if sukses.Valid {
		nilai := sukses.Bool
		run.Sukses = &nilai
	}
	if ukuranByte.Valid {
		run.UkuranByte = ukuranByte.Int64
	}
	run.Tujuan = pgtypes.TextOr(tujuan)
	run.Pesan = pgtypes.TextOr(pesan)
	return run
}
