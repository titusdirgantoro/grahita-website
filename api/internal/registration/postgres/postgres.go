// Package postgres menghubungkan domain pendaftaran akun cabang ke tabel
// registration_tokens, branches, dan branch_users lewat query sqlc.
//
// Seluruh pemakaian token berjalan di dalam SATU transaksi: baris token
// dikunci dengan FOR UPDATE, keadaannya diperiksa ULANG, akun dibuat, token
// ditandai terpakai, dan entri audit ditulis sebelum COMMIT (SystemDesign.md
// §4.1 dan §5.4).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
	"github.com/grahita/portal/api/internal/registration"
)

// Repo mengimplementasikan registration.Repository di atas PostgreSQL.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository pendaftaran.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// Lookup memuat keadaan token tanpa mengunci barisnya.
func (r *Repo) Lookup(ctx context.Context, tokenHash []byte) (registration.Snapshot, error) {
	row, err := r.q.LookupRegistrationToken(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return registration.Snapshot{}, registration.ErrTokenInvalid
	}
	if err != nil {
		return registration.Snapshot{}, fmt.Errorf("query LookupRegistrationToken: %w", err)
	}
	return snapshotFromLookup(row), nil
}

// Register memakai token dan membuat akun cabang dalam satu transaksi.
func (r *Repo) Register(ctx context.Context, req registration.RegisterRequest, rec audit.Entry) (registration.Akun, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return registration.Akun{}, fmt.Errorf("mulai transaksi pendaftaran: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	// Ambil branch_id tanpa lock, lalu kunci cabang sebelum token dan akun.
	// Pembacaan token yang menentukan tetap diulang setelah lock, sehingga
	// token yang dicabut sambil menunggu tetap ditolak. Urutan branch→token→
	// branch_users konsisten dengan revokasi identitas.
	var branchID string
	if err := tx.QueryRow(ctx, `SELECT branch_id FROM registration_tokens WHERE token_hash=$1`, req.TokenHash).Scan(&branchID); errors.Is(err, pgx.ErrNoRows) {
		return registration.Akun{}, registration.ErrTokenInvalid
	} else if err != nil {
		return registration.Akun{}, fmt.Errorf("baca cabang token registrasi: %w", err)
	}
	var lockedBranchID string
	if err := tx.QueryRow(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, branchID).Scan(&lockedBranchID); errors.Is(err, pgx.ErrNoRows) {
		return registration.Akun{}, registration.ErrTokenInvalid
	} else if err != nil {
		return registration.Akun{}, fmt.Errorf("kunci cabang registrasi: %w", err)
	}
	// Kunci baris token: permintaan kedua untuk token yang sama menunggu
	// commit permintaan pertama, lalu membaca ulang used_at yang sudah terisi
	// sehingga jawabannya TOKEN_USED, bukan dua akun untuk satu cabang.
	row, err := q.LockRegistrationToken(ctx, req.TokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return registration.Akun{}, registration.ErrTokenInvalid
	}
	if err != nil {
		return registration.Akun{}, fmt.Errorf("query LockRegistrationToken: %w", err)
	}
	snap := snapshotFromLock(row)
	if err := snap.Cek(req.Now); err != nil {
		return registration.Akun{}, err
	}

	// Email akun SELALU dari data cabang, bukan dari payload klien.
	akun, err := q.CreateBranchAccount(ctx, sqlc.CreateBranchAccountParams{
		BranchID:     snap.BranchID,
		Email:        snap.Email,
		PasswordHash: req.PasswordHash,
		Telepon:      pgtypes.Text(req.Telepon),
	})
	if err != nil {
		switch {
		case isUniqueViolation(err, "branch_id"):
			return registration.Akun{}, registration.ErrBranchHasAccount
		case isUniqueViolation(err, ""):
			return registration.Akun{}, registration.ErrEmailTaken
		}
		return registration.Akun{}, fmt.Errorf("query CreateBranchAccount: %w", err)
	}

	terpakai, err := q.MarkRegistrationTokenUsed(ctx, sqlc.MarkRegistrationTokenUsedParams{
		ID:     snap.TokenID,
		UsedBy: pgtypes.UUID(akun.ID),
	})
	if err != nil {
		return registration.Akun{}, fmt.Errorf("query MarkRegistrationTokenUsed: %w", err)
	}
	if terpakai != 1 {
		// Tidak mungkin terjadi selama kunci baris dipegang, tetapi kalau
		// terjadi seluruh transaksi dibatalkan: token sudah dipakai oleh orang
		// lain dan akun yang baru dibuat tidak boleh ikut tersimpan.
		return registration.Akun{}, registration.ErrTokenUsed
	}
	if tag, err := tx.Exec(ctx, `UPDATE branches SET identity_status='registered', updated_at=now()
 WHERE id=$1 AND identity_status='ready' AND nama_kepala IS NOT NULL AND email IS NOT NULL`, snap.BranchID); err != nil {
		return registration.Akun{}, fmt.Errorf("tandai identitas registered: %w", err)
	} else if tag.RowsAffected() != 1 {
		return registration.Akun{}, registration.ErrIdentityIncomplete
	}

	rec.ActorID = akun.ID
	rec.ActorNama = snap.Nama
	rec.EntityID = akun.ID
	rec.After = map[string]any{
		"user_id": akun.ID, "branch_id": snap.BranchID, "nama": snap.Nama, "email": akun.Email,
	}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return registration.Akun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return registration.Akun{}, fmt.Errorf("commit pendaftaran: %w", err)
	}

	return registration.Akun{
		UserID: akun.ID, BranchID: snap.BranchID, Nama: snap.Nama,
		Email: akun.Email, Telepon: req.Telepon,
	}, nil
}

func snapshotFromLookup(row sqlc.LookupRegistrationTokenRow) registration.Snapshot {
	return registration.Snapshot{
		TokenID: row.TokenID, BranchID: row.BranchID, ExpiresAt: row.ExpiresAt,
		UsedAt: row.UsedAt, Nama: row.Nama, NamaKepala: row.NamaKepala, Email: row.Email,
		IdentityStatus: row.IdentityStatus,
		BranchActive:   row.BranchActive, BranchHasAccount: row.BranchHasAccount,
	}
}

func snapshotFromLock(row sqlc.LockRegistrationTokenRow) registration.Snapshot {
	return registration.Snapshot{
		TokenID: row.TokenID, BranchID: row.BranchID, ExpiresAt: row.ExpiresAt,
		UsedAt: row.UsedAt, Nama: row.Nama, NamaKepala: row.NamaKepala, Email: row.Email,
		IdentityStatus: row.IdentityStatus,
		BranchActive:   row.BranchActive, BranchHasAccount: row.BranchHasAccount,
	}
}

// isUniqueViolation melaporkan pelanggaran indeks unik. Bila needle diisi,
// hanya pelanggaran pada indeks/detail yang memuat needle itu yang dihitung.
func isUniqueViolation(err error, needle string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	if needle == "" {
		return true
	}
	return strings.Contains(pgErr.ConstraintName, needle) || strings.Contains(pgErr.Detail, needle)
}
