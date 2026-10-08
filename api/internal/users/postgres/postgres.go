// Package postgres adalah implementasi repository pengelolaan pengguna.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/users"
)

// AdminMutationLockKey adalah kunci penasihat tetap untuk seluruh mutasi admin.
// UpdateAdmin mengambilnya di awal transaksi sehingga dua permintaan yang
// datang bersamaan diserialkan: permintaan kedua melihat hasil commit pertama
// dan tidak bisa menjatuhkan superadmin aktif terakhir (PRD AP-USR-03).
// Nilainya arbitrer asalkan konsisten antar instance; diekspor agar pengujian
// dapat membuktikan mutasi benar-benar menunggu kunci ini.
const AdminMutationLockKey int64 = 4815162342

// Repo mengimplementasikan users.Repository di atas PostgreSQL.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository pengguna.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// ListBranchUsers mengembalikan satu baris per cabang beserta akunnya.
func (r *Repo) ListBranchUsers(ctx context.Context) ([]users.BranchUser, error) {
	rows, err := r.q.ListBranchAccountRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListBranchAccountRows: %w", err)
	}
	out := make([]users.BranchUser, 0, len(rows))
	for _, row := range rows {
		out = append(out, users.BranchUser{
			BranchID:       row.BranchID,
			UserID:         row.UserID,
			Nama:           row.Nama,
			NamaKepala:     row.NamaKepala,
			Email:          row.Email,
			Telepon:        row.Telepon,
			HasAccount:     row.UserID != "",
			IsActive:       row.UserActive,
			BranchActive:   row.BranchActive,
			Status:         users.BranchStatus(row.UserID != "", row.UserActive, row.BranchActive),
			LastLoginAt:    row.LastLoginAt,
			TokenPrefix:    row.TokenPrefix,
			TokenExpiresAt: row.TokenExpiresAt,
		})
	}
	return out, nil
}

// BranchUserByID mencari akun cabang berdasarkan id branch_users.
func (r *Repo) BranchUserByID(ctx context.Context, id string) (users.BranchUser, error) {
	row, err := r.q.GetBranchAccountRow(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.BranchUser{}, users.ErrNotFound
	}
	if err != nil {
		return users.BranchUser{}, fmt.Errorf("query GetBranchAccountRow: %w", err)
	}
	return users.BranchUser{
		BranchID:       row.BranchID,
		UserID:         row.UserID,
		Nama:           row.Nama,
		NamaKepala:     row.NamaKepala,
		Email:          row.Email,
		Telepon:        row.Telepon,
		HasAccount:     row.UserID != "",
		IsActive:       row.UserActive,
		BranchActive:   row.BranchActive,
		Status:         users.BranchStatus(row.UserID != "", row.UserActive, row.BranchActive),
		LastLoginAt:    row.LastLoginAt,
		TokenPrefix:    row.TokenPrefix,
		TokenExpiresAt: row.TokenExpiresAt,
	}, nil
}

// SetBranchUserActive mengubah status aktif satu akun cabang.
func (r *Repo) SetBranchUserActive(ctx context.Context, id string, active bool) (users.BranchUser, error) {
	if _, err := r.q.GetBranchUserBrief(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return users.BranchUser{}, users.ErrNotFound
	} else if err != nil {
		return users.BranchUser{}, fmt.Errorf("query GetBranchUserBrief: %w", err)
	}
	if err := r.q.SetBranchUserActive(ctx, sqlc.SetBranchUserActiveParams{ID: id, IsActive: active}); err != nil {
		return users.BranchUser{}, fmt.Errorf("query SetBranchUserActive: %w", err)
	}
	return r.BranchUserByID(ctx, id)
}

// ListAdmins mengembalikan seluruh akun admin.
func (r *Repo) ListAdmins(ctx context.Context) ([]users.AdminUser, error) {
	rows, err := r.q.ListAdminUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListAdminUsers: %w", err)
	}
	out := make([]users.AdminUser, 0, len(rows))
	for _, row := range rows {
		out = append(out, users.AdminUser{
			ID:              row.ID,
			Nama:            row.Nama,
			Email:           row.Email,
			Role:            auth.Role(row.Role),
			NotifInvoice:    row.NotifInvoice,
			NotifSuratJalan: row.NotifSuratJalan,
			IsActive:        row.IsActive,
			Status:          users.AdminStatus(row.IsActive, row.ActivatedAt != nil),
			InvitedAt:       row.InvitedAt,
			ActivatedAt:     row.ActivatedAt,
			LastLoginAt:     row.LastLoginAt,
		})
	}
	return out, nil
}

// AdminByID mencari akun admin berdasarkan id.
func (r *Repo) AdminByID(ctx context.Context, id string) (users.AdminUser, error) {
	row, err := r.q.GetAdminUserRow(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.AdminUser{}, users.ErrNotFound
	}
	if err != nil {
		return users.AdminUser{}, fmt.Errorf("query GetAdminUserRow: %w", err)
	}
	return adminFromRow(row.ID, row.Nama, row.Email, row.Role, row.NotifInvoice,
		row.NotifSuratJalan, row.IsActive, row.InvitedAt, row.ActivatedAt, row.LastLoginAt), nil
}

// AdminByEmail mencari akun admin berdasarkan email.
func (r *Repo) AdminByEmail(ctx context.Context, email string) (users.AdminUser, error) {
	row, err := r.q.GetAdminUserRowByEmail(ctx, users.NormalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return users.AdminUser{}, users.ErrNotFound
	}
	if err != nil {
		return users.AdminUser{}, fmt.Errorf("query GetAdminUserRowByEmail: %w", err)
	}
	return adminFromRow(row.ID, row.Nama, row.Email, row.Role, row.NotifInvoice,
		row.NotifSuratJalan, row.IsActive, row.InvitedAt, row.ActivatedAt, row.LastLoginAt), nil
}

// CreateAdmin membuat undangan admin: password_hash dan activated_at NULL.
func (r *Repo) CreateAdmin(ctx context.Context, in users.NewAdmin) (users.AdminUser, error) {
	row, err := r.q.CreateAdminInvitation(ctx, sqlc.CreateAdminInvitationParams{
		Nama:            in.Nama,
		Email:           in.Email,
		Role:            sqlc.AdminRole(in.Role),
		NotifInvoice:    in.NotifInvoice,
		NotifSuratJalan: in.NotifSuratJalan,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return users.AdminUser{}, users.ErrEmailTaken
		}
		return users.AdminUser{}, fmt.Errorf("query CreateAdminInvitation: %w", err)
	}
	return adminFromRow(row.ID, row.Nama, row.Email, row.Role, row.NotifInvoice,
		row.NotifSuratJalan, row.IsActive, row.InvitedAt, row.ActivatedAt, row.LastLoginAt), nil
}

// UpdateAdmin menyimpan perubahan admin dalam satu transaksi yang memegang
// kunci penasihat, sehingga larangan menurunkan/menonaktifkan superadmin aktif
// terakhir tetap berlaku walaupun dua permintaan datang bersamaan.
func (r *Repo) UpdateAdmin(ctx context.Context, next users.AdminUser) (users.AdminUser, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return users.AdminUser{}, fmt.Errorf("mulai transaksi ubah admin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, AdminMutationLockKey); err != nil {
		return users.AdminUser{}, fmt.Errorf("ambil kunci mutasi admin: %w", err)
	}

	var curRole string
	var curActive bool
	err = tx.QueryRow(ctx, `SELECT role::text, is_active FROM admin_users WHERE id = $1 FOR UPDATE`, next.ID).
		Scan(&curRole, &curActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.AdminUser{}, users.ErrNotFound
	}
	if err != nil {
		return users.AdminUser{}, fmt.Errorf("baca admin saat ini: %w", err)
	}

	// Turun role / nonaktifkan superadmin aktif: wajib masih ada superadmin
	// aktif lain. Pemeriksaan dan UPDATE berada di transaksi yang sama dan
	// diserialkan kunci penasihat di atas.
	if curRole == string(auth.RoleSuperadmin) && curActive &&
		(next.Role != auth.RoleSuperadmin || !next.IsActive) {
		var sisa int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM admin_users WHERE role = 'superadmin' AND is_active AND id <> $1`,
			next.ID).Scan(&sisa); err != nil {
			return users.AdminUser{}, fmt.Errorf("hitung superadmin aktif: %w", err)
		}
		if sisa == 0 {
			return users.AdminUser{}, users.ErrLastSuperadmin
		}
	}

	row, err := r.q.WithTx(tx).UpdateAdminUserRow(ctx, sqlc.UpdateAdminUserRowParams{
		ID:              next.ID,
		Nama:            next.Nama,
		Role:            sqlc.AdminRole(next.Role),
		NotifInvoice:    next.NotifInvoice,
		NotifSuratJalan: next.NotifSuratJalan,
		IsActive:        next.IsActive,
	})
	if err != nil {
		// Trigger pengaman di database menolak dengan 23514 bila superadmin
		// aktif terakhir hendak dijatuhkan; perlakukan sama seperti guard.
		if isCheckViolation(err) {
			return users.AdminUser{}, users.ErrLastSuperadmin
		}
		return users.AdminUser{}, fmt.Errorf("query UpdateAdminUserRow: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return users.AdminUser{}, fmt.Errorf("commit ubah admin: %w", err)
	}
	return adminFromRow(row.ID, row.Nama, row.Email, row.Role, row.NotifInvoice,
		row.NotifSuratJalan, row.IsActive, row.InvitedAt, row.ActivatedAt, row.LastLoginAt), nil
}

// DocumentRecipients mengembalikan email admin ber-flag yang sudah diaktivasi.
func (r *Repo) DocumentRecipients(ctx context.Context, kind users.DocKind) ([]string, error) {
	switch kind {
	case users.DocInvoice:
		rows, err := r.q.ListInvoiceRecipients(ctx)
		if err != nil {
			return nil, fmt.Errorf("query ListInvoiceRecipients: %w", err)
		}
		return rows, nil
	case users.DocSuratJalan:
		rows, err := r.q.ListSuratJalanRecipients(ctx)
		if err != nil {
			return nil, fmt.Errorf("query ListSuratJalanRecipients: %w", err)
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("%w: jenis dokumen %q", users.ErrInvalidData, kind)
	}
}

func adminFromRow(id, nama, email, role string, notifInvoice, notifSuratJalan, isActive bool, invitedAt, activatedAt, lastLoginAt *time.Time) users.AdminUser {
	return users.AdminUser{
		ID:              id,
		Nama:            nama,
		Email:           email,
		Role:            auth.Role(role),
		NotifInvoice:    notifInvoice,
		NotifSuratJalan: notifSuratJalan,
		IsActive:        isActive,
		Status:          users.AdminStatus(isActive, activatedAt != nil),
		InvitedAt:       invitedAt,
		ActivatedAt:     activatedAt,
		LastLoginAt:     lastLoginAt,
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}
