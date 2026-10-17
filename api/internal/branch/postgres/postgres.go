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
	"github.com/grahita/portal/api/internal/auth"
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
			JabatanKepala: row.JabatanKepala, IdentityStatus: row.IdentityStatus,
			Telepon: row.Telepon, IsCNC: row.IsCnc, IsSpecial: row.IsSpecial,
			PajakIn: row.PajakIn, IsActive: row.IsActive,
			BankAllowlistVersion:      row.BankAllowlistVersion,
			EffectiveBankAccountCount: row.EffectiveBankAccountCount,
			TokenPrefix:               row.TokenPrefix, TokenExpiresAt: row.TokenExpiresAt,
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
		JabatanKepala: row.JabatanKepala, IdentityStatus: row.IdentityStatus,
		Telepon: row.Telepon, IsCNC: row.IsCnc, IsSpecial: row.IsSpecial,
		PajakIn: row.PajakIn, IsActive: row.IsActive,
		BankAllowlistVersion:      row.BankAllowlistVersion,
		EffectiveBankAccountCount: row.EffectiveBankAccountCount,
		TokenPrefix:               row.TokenPrefix, TokenExpiresAt: row.TokenExpiresAt,
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
		Nama:           in.Nama,
		NamaKepala:     pgtypes.Text(in.NamaKepala),
		Email:          pgtypes.Text(in.Email),
		JabatanKepala:  pgtypes.Text(in.JabatanKepala),
		IdentityStatus: sqlc.BranchIdentityStatus(in.IdentityStatus),
		Telepon:        pgtypes.Text(in.Telepon),
		IsCnc:          in.IsCNC,
		IsSpecial:      in.IsSpecial,
		PajakIn:        in.PajakIn,
		IsActive:       in.IsActive,
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

// Update menerapkan patch terhadap state terkini setelah mengunci cabang lalu
// seluruh akun cabangnya. Urutan ini mencegah snapshot pra-revokasi ditulis
// kembali oleh PUT parsial yang lama.
func (r *Repo) Update(ctx context.Context, id string, p branch.Patch, actor auth.Actor) (branch.Branch, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return branch.Branch{}, fmt.Errorf("mulai transaksi ubah cabang: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(22102, 2)`); err != nil {
		return branch.Branch{}, fmt.Errorf("kunci kebijakan rekening cabang: %w", err)
	}
	accountRows, err := tx.Query(ctx, `SELECT ba.id FROM bank_accounts ba
 JOIN branch_bank_accounts x ON x.bank_account_id=ba.id
 WHERE x.branch_id=$1 AND x.is_allowed ORDER BY ba.id FOR UPDATE`, id)
	if err != nil {
		return branch.Branch{}, fmt.Errorf("kunci rekening assignment cabang: %w", err)
	}
	for accountRows.Next() {
		var accountID string
		if err := accountRows.Scan(&accountID); err != nil {
			accountRows.Close()
			return branch.Branch{}, err
		}
	}
	accountRows.Close()

	var cur branch.Branch
	var namaKepala, email, jabatanKepala *string
	err = tx.QueryRow(ctx, `SELECT id, nama, nama_kepala, email::text, jabatan_kepala,
       COALESCE(telepon, ''), is_cnc, is_special, pajak_in, is_active,
       identity_status::text
  FROM branches WHERE id = $1 FOR UPDATE`, id).Scan(
		&cur.ID, &cur.Nama, &namaKepala, &email, &jabatanKepala, &cur.Telepon,
		&cur.IsCNC, &cur.IsSpecial, &cur.PajakIn, &cur.IsActive, &cur.IdentityStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return branch.Branch{}, branch.ErrNotFound
	}
	if err != nil {
		return branch.Branch{}, fmt.Errorf("baca cabang saat ini: %w", err)
	}
	cur.NamaKepala = textOrEmpty(namaKepala)
	cur.Email = textOrEmpty(email)
	cur.JabatanKepala = textOrEmpty(jabatanKepala)

	rows, err := tx.Query(ctx, `SELECT id FROM branch_users WHERE branch_id=$1 FOR UPDATE`, id)
	if err != nil {
		return branch.Branch{}, fmt.Errorf("kunci akun cabang: %w", err)
	}
	hasAccount := rows.Next()
	rows.Close()
	if rows.Err() != nil {
		return branch.Branch{}, fmt.Errorf("baca akun cabang: %w", rows.Err())
	}

	next, err := branch.ApplyPatch(cur, p, hasAccount)
	if err != nil {
		return branch.Branch{}, err
	}
	if next.Email != "" && next.Email != cur.Email {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM branches WHERE email=$1 AND id<>$2
 UNION ALL SELECT 1 FROM admin_users WHERE email=$1
)`, next.Email, id).Scan(&taken); err != nil {
			return branch.Branch{}, fmt.Errorf("periksa email cabang: %w", err)
		}
		if taken {
			return branch.Branch{}, branch.ErrEmailTaken
		}
	}

	q := r.q.WithTx(tx)
	if err := q.UpdateBranchDetail(ctx, sqlc.UpdateBranchDetailParams{
		Nama:           next.Nama,
		NamaKepala:     pgtypes.Text(next.NamaKepala),
		Email:          pgtypes.Text(next.Email),
		JabatanKepala:  pgtypes.Text(next.JabatanKepala),
		IdentityStatus: sqlc.BranchIdentityStatus(next.IdentityStatus),
		Telepon:        pgtypes.Text(next.Telepon),
		IsCnc:          next.IsCNC,
		IsSpecial:      next.IsSpecial,
		PajakIn:        next.PajakIn,
		IsActive:       next.IsActive,
		ID:             next.ID,
	}); err != nil {
		if isUniqueViolation(err) {
			return branch.Branch{}, branch.ErrEmailTaken
		}
		return branch.Branch{}, fmt.Errorf("query UpdateBranchDetail: %w", err)
	}
	if next.IsActive != cur.IsActive {
		if _, err := tx.Exec(ctx, `UPDATE branches SET bank_allowlist_version=bank_allowlist_version+1 WHERE id=$1`, id); err != nil {
			return branch.Branch{}, fmt.Errorf("naikkan versi allowlist status cabang: %w", err)
		}
	}
	if next.Email != "" && next.Email != cur.Email {
		if err := q.UpdateBranchLoginEmail(ctx, sqlc.UpdateBranchLoginEmailParams{
			Email: next.Email, BranchID: next.ID,
		}); err != nil {
			if isUniqueViolation(err) {
				return branch.Branch{}, branch.ErrEmailTaken
			}
			return branch.Branch{}, fmt.Errorf("query UpdateBranchLoginEmail: %w", err)
		}
	}
	if next.IdentityStatus == branch.IdentityIncomplete {
		perintah := []string{
			`UPDATE branch_users
 SET is_active=false, password_hash='!identity-revoked!', updated_at=now()
 WHERE branch_id=$1 AND (is_active OR password_hash <> '!identity-revoked!')`,
			`UPDATE registration_tokens
 SET revoked_at=now(), used_at=COALESCE(used_at, now())
 WHERE branch_id=$1 AND used_at IS NULL AND revoked_at IS NULL`,
			`UPDATE password_reset_tokens SET revoked_at=now()
 WHERE actor_kind='branch' AND used_at IS NULL AND revoked_at IS NULL
   AND user_id IN (SELECT id FROM branch_users WHERE branch_id=$1)`,
			`UPDATE sessions SET revoked_at=now()
 WHERE revoked_at IS NULL AND (branch_id=$1 OR user_id IN (SELECT id FROM branch_users WHERE branch_id=$1))`,
		}
		for _, query := range perintah {
			if _, err := tx.Exec(ctx, query, next.ID); err != nil {
				return branch.Branch{}, fmt.Errorf("cabut akses identitas incomplete: %w", err)
			}
		}
	}

	rec := audit.NewEntry(ctx, actor, audit.EntityBranch, id, audit.ActionUpdate, cur, next)
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return branch.Branch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return branch.Branch{}, fmt.Errorf("commit ubah cabang: %w", err)
	}
	return next, nil
}

func textOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
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
	// Konfigurasi cabang tanpa riwayat ekonomi (HasOrders sudah menolak cabang
	// bersejarah): assignment rekening D2 dan akun deposit kosong ikut dihapus.
	// FK keduanya RESTRICT untuk menjaga histori, bukan untuk memblokir cabang
	// yang belum pernah bertransaksi.
	for _, stmt := range []string{
		`DELETE FROM branch_bank_accounts WHERE branch_id = $1`,
		`DELETE FROM branch_deposit_accounts WHERE branch_id = $1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			return fmt.Errorf("hapus konfigurasi cabang: %w", err)
		}
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
	var status string
	var lengkap bool
	err = tx.QueryRow(ctx, `SELECT is_active, identity_status::text,
       (nama_kepala IS NOT NULL AND email IS NOT NULL)
  FROM branches WHERE id = $1 FOR UPDATE`, branchID).Scan(&aktif, &status, &lengkap)
	if errors.Is(err, pgx.ErrNoRows) {
		return branch.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("baca cabang penerima token: %w", err)
	}
	if !aktif {
		return branch.ErrInactive
	}
	if status != branch.IdentityReady || !lengkap {
		return branch.ErrIdentityIncomplete
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
		Email: pgtypes.Text(email), ExceptID: pgtypes.UUID(exceptID),
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
