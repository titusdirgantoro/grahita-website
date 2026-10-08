// Package postgres adalah implementasi repository master cabang di
// PostgreSQL.
//
// Setiap metode yang mengubah data menuliskan entri audit di dalam transaksi
// yang sama, sehingga perubahan dan catatannya selalu sejalan
// (SystemDesign.md §5.4).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo mengimplementasikan branch.Repository di atas PostgreSQL.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository master cabang.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// List memuat cabang sesuai filter.
func (r *Repo) List(ctx context.Context, f branch.Filter) ([]branch.Branch, error) {
	arg := sqlc.ListBranchDetailsParams{
		Aktif:   pgtypes.BoolPtr(f.Active),
		Cnc:     pgtypes.BoolPtr(f.CNC),
		Special: pgtypes.BoolPtr(f.Special),
	}
	if f.Q != "" {
		arg.Q = pgtypes.Text("%" + f.Q + "%")
	}
	rows, err := r.q.ListBranchDetails(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("query ListBranchDetails: %w", err)
	}
	out := make([]branch.Branch, 0, len(rows))
	for _, row := range rows {
		out = append(out, branch.Branch{
			ID: row.ID, Nama: row.Nama, NamaKepala: row.NamaKepala, Email: row.Email,
			Telepon: row.Telepon, IsCNC: row.IsCnc, IsSpecial: row.IsSpecial,
			PajakIn: row.PajakIn, IsActive: row.IsActive,
			TokenPrefix: row.TokenPrefix, TokenExpiresAt: row.TokenExpiresAt,
		})
	}
	return out, nil
}

// Get memuat satu cabang.
func (r *Repo) Get(ctx context.Context, id string) (branch.Branch, error) {
	row, err := r.q.GetBranchDetail(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return branch.Branch{}, branch.ErrNotFound
	}
	if err != nil {
		return branch.Branch{}, fmt.Errorf("query GetBranchDetail: %w", err)
	}
	return branch.Branch{
		ID: row.ID, Nama: row.Nama, NamaKepala: row.NamaKepala, Email: row.Email,
		Telepon: row.Telepon, IsCNC: row.IsCnc, IsSpecial: row.IsSpecial,
		PajakIn: row.PajakIn, IsActive: row.IsActive,
		TokenPrefix: row.TokenPrefix, TokenExpiresAt: row.TokenExpiresAt,
	}, nil
}

// Create menyimpan cabang baru beserta catatan auditnya.
func (r *Repo) Create(ctx context.Context, in branch.Branch, rec audit.Entry) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("mulai transaksi buat cabang: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := r.q.WithTx(tx).CreateBranchDetail(ctx, sqlc.CreateBranchDetailParams{
		Nama:       in.Nama,
		NamaKepala: in.NamaKepala,
		Email:      in.Email,
		Telepon:    pgtypes.Text(in.Telepon),
		IsCnc:      in.IsCNC,
		IsSpecial:  in.IsSpecial,
		PajakIn:    in.PajakIn,
		IsActive:   in.IsActive,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return "", branch.ErrEmailTaken
		}
		return "", fmt.Errorf("query CreateBranchDetail: %w", err)
	}

	rec.EntityID = id
	// Snapshot "after" memakai cabang yang sama dengan yang baru disimpan
	// (lengkap dengan id barunya) supaya audit tidak kehilangan id.
	in.ID = id
	rec.After = in
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit buat cabang: %w", err)
	}
	return id, nil
}

// Update menyimpan perubahan penuh satu cabang. Email akun cabang ikut
// diselaraskan bila email cabangnya berubah.
func (r *Repo) Update(ctx context.Context, next branch.Branch, rec audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi ubah cabang: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Kunci baris sekaligus memastikan cabangnya benar-benar ada, dan baca
	// email lama untuk tahu apakah email login akun perlu ikut diubah.
	var emailLama string
	err = tx.QueryRow(ctx, `SELECT email::text FROM branches WHERE id = $1 FOR UPDATE`, next.ID).Scan(&emailLama)
	if errors.Is(err, pgx.ErrNoRows) {
		return branch.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("baca cabang saat ini: %w", err)
	}

	q := r.q.WithTx(tx)
	if err := q.UpdateBranchDetail(ctx, sqlc.UpdateBranchDetailParams{
		Nama:       next.Nama,
		NamaKepala: next.NamaKepala,
		Email:      next.Email,
		Telepon:    pgtypes.Text(next.Telepon),
		IsCnc:      next.IsCNC,
		IsSpecial:  next.IsSpecial,
		PajakIn:    next.PajakIn,
		IsActive:   next.IsActive,
		ID:         next.ID,
	}); err != nil {
		if isUniqueViolation(err) {
			return branch.ErrEmailTaken
		}
		return fmt.Errorf("query UpdateBranchDetail: %w", err)
	}
	if next.Email != emailLama {
		if err := q.UpdateBranchLoginEmail(ctx, sqlc.UpdateBranchLoginEmailParams{
			Email: next.Email, BranchID: next.ID,
		}); err != nil {
			if isUniqueViolation(err) {
				return branch.ErrEmailTaken
			}
			return fmt.Errorf("query UpdateBranchLoginEmail: %w", err)
		}
	}

	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ubah cabang: %w", err)
	}
	return nil
}

// Delete menghapus cabang beserta akun, sesi, dan tokennya. Pemanggil sudah
// memastikan cabang ini belum punya pesanan (PRD AP-CAB-04).
func (r *Repo) Delete(ctx context.Context, id string, rec audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi hapus cabang: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ada bool
	if err := tx.QueryRow(ctx, `SELECT true FROM branches WHERE id = $1 FOR UPDATE`, id).Scan(&ada); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return branch.ErrNotFound
		}
		return fmt.Errorf("baca cabang yang dihapus: %w", err)
	}

	q := r.q.WithTx(tx)
	// Sesi dan akun cabang tidak memakai ON DELETE CASCADE, jadi dibersihkan
	// eksplisit; token ikut terhapus bersama cabangnya.
	if err := q.DeleteBranchSessions(ctx, pgtypes.UUID(id)); err != nil {
		return fmt.Errorf("query DeleteBranchSessions: %w", err)
	}
	if err := q.DeleteBranchUsers(ctx, id); err != nil {
		return fmt.Errorf("query DeleteBranchUsers: %w", err)
	}
	if err := q.DeleteBranchRegistrationTokens(ctx, id); err != nil {
		return fmt.Errorf("query DeleteBranchRegistrationTokens: %w", err)
	}
	if err := q.DeleteBranch(ctx, id); err != nil {
		return fmt.Errorf("query DeleteBranch: %w", err)
	}

	rec.EntityID = id
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit hapus cabang: %w", err)
	}
	return nil
}

// IssueToken menggantikan token lama yang belum terpakai dengan token baru.
// Pemeriksaan ulang (cabang ada, aktif, belum punya akun) dilakukan di dalam
// transaksi yang memegang kunci baris cabang supaya tidak bisa dilewati oleh
// dua permintaan bersamaan (1 cabang = 1 akun, PRD CP-REG-06).
func (r *Repo) IssueToken(ctx context.Context, branchID string, tokenHash []byte, prefix string, expiresAt time.Time, rec audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi token registrasi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var aktif bool
	err = tx.QueryRow(ctx, `SELECT is_active FROM branches WHERE id = $1 FOR UPDATE`, branchID).Scan(&aktif)
	if errors.Is(err, pgx.ErrNoRows) {
		return branch.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("baca cabang penerima token: %w", err)
	}
	if !aktif {
		return branch.ErrInactive
	}
	var punyaAkun bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM branch_users WHERE branch_id = $1)`, branchID).Scan(&punyaAkun); err != nil {
		return fmt.Errorf("periksa akun cabang: %w", err)
	}
	if punyaAkun {
		return branch.ErrHasAccount
	}

	q := r.q.WithTx(tx)
	if err := q.ReplaceRegistrationToken(ctx, branchID); err != nil {
		return fmt.Errorf("query ReplaceRegistrationToken: %w", err)
	}
	if _, err := q.CreateRegistrationToken(ctx, sqlc.CreateRegistrationTokenParams{
		BranchID:    branchID,
		TokenHash:   tokenHash,
		TokenPrefix: prefix,
		ExpiresAt:   expiresAt,
		CreatedBy:   pgtypes.UUID(rec.ActorID),
	}); err != nil {
		return fmt.Errorf("query CreateRegistrationToken: %w", err)
	}

	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit token registrasi: %w", err)
	}
	return nil
}

// HasOrders melaporkan apakah cabang sudah punya pesanan.
func (r *Repo) HasOrders(ctx context.Context, id string) (bool, error) {
	ada, err := r.q.BranchHasOrders(ctx, id)
	if err != nil {
		return false, fmt.Errorf("query BranchHasOrders: %w", err)
	}
	return ada, nil
}

// HasAccount melaporkan apakah cabang sudah punya akun.
func (r *Repo) HasAccount(ctx context.Context, id string) (bool, error) {
	ada, err := r.q.BranchHasAccount(ctx, id)
	if err != nil {
		return false, fmt.Errorf("query BranchHasAccount: %w", err)
	}
	return ada, nil
}

// EmailTaken melaporkan apakah email sudah dipakai cabang lain.
func (r *Repo) EmailTaken(ctx context.Context, email, exceptID string) (bool, error) {
	ada, err := r.q.BranchEmailTaken(ctx, sqlc.BranchEmailTakenParams{
		Email: email, ExceptID: pgtypes.UUID(exceptID),
	})
	if err != nil {
		return false, fmt.Errorf("query BranchEmailTaken: %w", err)
	}
	return ada, nil
}

// AdminEmailTaken melaporkan apakah email sudah dipakai akun admin.
func (r *Repo) AdminEmailTaken(ctx context.Context, email string) (bool, error) {
	ada, err := r.q.AdminEmailTaken(ctx, email)
	if err != nil {
		return false, fmt.Errorf("query AdminEmailTaken: %w", err)
	}
	return ada, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
