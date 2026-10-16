// Package postgres adalah implementasi repository pengelolaan pengguna.
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
			IdentityStatus: row.IdentityStatus,
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
	return branchUserByID(ctx, r.q, id)
}

// branchUserByID memuat satu baris akun cabang memakai query set yang diberikan
// (pool atau transaksi).
func branchUserByID(ctx context.Context, q *sqlc.Queries, id string) (users.BranchUser, error) {
	row, err := q.GetBranchAccountRow(ctx, id)
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
		IdentityStatus: row.IdentityStatus,
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

// SetBranchUserActive mengubah status aktif satu akun cabang beserta catatan
// auditnya dalam SATU transaksi (F2-28): baris dikunci lebih dulu
// (FOR UPDATE) supaya snapshot before/after tidak pernah berasal dari keadaan
// yang sudah basi, dan kegagalan menulis audit membatalkan perubahannya.
func (r *Repo) SetBranchUserActive(ctx context.Context, id string, active bool, recs []audit.Entry) (users.BranchUser, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return users.BranchUser{}, fmt.Errorf("mulai transaksi ubah akun cabang: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	// Urutan lock harus sama dengan perubahan master cabang: branches dahulu,
	// baru branch_users. Dengan begitu PUT cabang yang menurunkan identitas dan
	// aktivasi akun tidak dapat saling melewati atau membentuk deadlock silang.
	var branchID string
	if err := tx.QueryRow(ctx, `SELECT branch_id FROM branch_users WHERE id = $1`, id).Scan(&branchID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return users.BranchUser{}, users.ErrNotFound
		}
		return users.BranchUser{}, fmt.Errorf("baca cabang akun: %w", err)
	}
	var identityStatus, namaKepala, email string
	if err := tx.QueryRow(ctx, `
SELECT identity_status::text, COALESCE(nama_kepala, ''), COALESCE(email::text, '')
  FROM branches WHERE id = $1 FOR UPDATE`, branchID).Scan(&identityStatus, &namaKepala, &email); err != nil {
		return users.BranchUser{}, fmt.Errorf("kunci cabang akun: %w", err)
	}
	var kunci string
	if err := tx.QueryRow(ctx, `SELECT id FROM branch_users WHERE id = $1 FOR UPDATE`, id).Scan(&kunci); err != nil {
		return users.BranchUser{}, fmt.Errorf("kunci akun cabang: %w", err)
	}
	if active && (identityStatus != "registered" || namaKepala == "" || email == "") {
		return users.BranchUser{}, users.ErrIdentityIncomplete
	}
	cur, err := branchUserByID(ctx, q, id)
	if err != nil {
		return users.BranchUser{}, err
	}
	if cur.IsActive == active {
		return cur, nil
	}
	if err := q.SetBranchUserActive(ctx, sqlc.SetBranchUserActiveParams{ID: id, IsActive: active}); err != nil {
		return users.BranchUser{}, fmt.Errorf("query SetBranchUserActive: %w", err)
	}
	next, err := branchUserByID(ctx, q, id)
	if err != nil {
		return users.BranchUser{}, err
	}
	for i := range recs {
		recs[i].EntityID = next.UserID
		recs[i].Before = audit.Redact(users.BranchUserSnapshot(cur))
		recs[i].After = audit.Redact(users.BranchUserSnapshot(next))
	}
	for _, rec := range recs {
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return users.BranchUser{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return users.BranchUser{}, fmt.Errorf("commit ubah akun cabang: %w", err)
	}
	return next, nil
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

// EmailTakenByBranch melaporkan apakah email sudah dipakai cabang, baik sebagai
// email login akun cabang (branch_users) maupun email cabang itu sendiri
// (branches). Menjaga kesimetrisan dengan branch.AdminEmailTaken (F1-06).
func (r *Repo) EmailTakenByBranch(ctx context.Context, email string) (bool, error) {
	taken, err := r.q.EmailTakenByBranch(ctx, users.NormalizeEmail(email))
	if err != nil {
		return false, fmt.Errorf("query EmailTakenByBranch: %w", err)
	}
	return taken, nil
}

// CreateAdmin membuat undangan admin: password_hash dan activated_at NULL.
//
// Pembuatan akun dan entri auditnya ditulis dalam SATU transaksi (F2-28):
// kegagalan mencatat audit membatalkan undangannya, dan pelanggaran unik email
// tidak meninggalkan entri audit apa pun.
func (r *Repo) CreateAdmin(ctx context.Context, in users.NewAdmin, recs []audit.Entry) (users.AdminUser, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return users.AdminUser{}, fmt.Errorf("mulai transaksi undangan admin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := r.q.WithTx(tx).CreateAdminInvitation(ctx, sqlc.CreateAdminInvitationParams{
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
	admin := adminFromRow(row.ID, row.Nama, row.Email, row.Role, row.NotifInvoice,
		row.NotifSuratJalan, row.IsActive, row.InvitedAt, row.ActivatedAt, row.LastLoginAt)
	for i := range recs {
		recs[i].EntityID = admin.ID
		recs[i].Before = nil
		recs[i].After = audit.Redact(users.AdminSnapshot(admin))
	}
	for _, rec := range recs {
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return users.AdminUser{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return users.AdminUser{}, fmt.Errorf("commit undangan admin: %w", err)
	}
	return admin, nil
}

// UpdateAdmin menyimpan perubahan admin dalam satu transaksi yang memegang
// kunci penasihat, sehingga larangan menurunkan/menonaktifkan superadmin aktif
// terakhir tetap berlaku walaupun dua permintaan datang bersamaan.
//
// Entri audit (F2-28) ditulis di transaksi yang sama dengan snapshot sebelum
// (baris yang dikunci) dan sesudah (hasil UPDATE) — tanpa email. Penolakan
// (ErrLastSuperadmin) membatalkan transaksi sehingga tidak ada entri
// tertinggal.
func (r *Repo) UpdateAdmin(ctx context.Context, next users.AdminUser, recs []audit.Entry) (users.AdminUser, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return users.AdminUser{}, fmt.Errorf("mulai transaksi ubah admin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

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
	curRow, err := q.GetAdminUserRow(ctx, next.ID)
	if err != nil {
		return users.AdminUser{}, fmt.Errorf("baca baris admin saat ini: %w", err)
	}
	cur := adminFromRow(curRow.ID, curRow.Nama, curRow.Email, curRow.Role, curRow.NotifInvoice,
		curRow.NotifSuratJalan, curRow.IsActive, curRow.InvitedAt, curRow.ActivatedAt, curRow.LastLoginAt)

	// Turun role / nonaktifkan superadmin aktif: wajib masih ada superadmin
	// aktif lain. Pemeriksaan dan UPDATE berada di transaksi yang sama dan
	// diserialkan kunci penasihat di atas.
	//
	// "Superadmin aktif lain" di sini WAJIB benar-benar bisa login
	// (password_hash DAN activated_at terisi). Superadmin yang masih berstatus
	// undangan tidak dihitung: ia sendiri tidak bisa masuk, sehingga bila ikut
	// dihitung sistem bisa ditinggalkan tanpa satu pun superadmin yang bisa
	// login (temuan QA F1-05).
	if curRole == string(auth.RoleSuperadmin) && curActive &&
		(next.Role != auth.RoleSuperadmin || !next.IsActive) {
		var sisa int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM admin_users
			  WHERE role = 'superadmin' AND is_active
			    AND password_hash IS NOT NULL AND activated_at IS NOT NULL
			    AND id <> $1`,
			next.ID).Scan(&sisa); err != nil {
			return users.AdminUser{}, fmt.Errorf("hitung superadmin aktif: %w", err)
		}
		if sisa == 0 {
			return users.AdminUser{}, users.ErrLastSuperadmin
		}
	}

	row, err := q.UpdateAdminUserRow(ctx, sqlc.UpdateAdminUserRowParams{
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
	sesudah := adminFromRow(row.ID, row.Nama, row.Email, row.Role, row.NotifInvoice,
		row.NotifSuratJalan, row.IsActive, row.InvitedAt, row.ActivatedAt, row.LastLoginAt)
	for i := range recs {
		recs[i].EntityID = sesudah.ID
		recs[i].Before = audit.Redact(users.AdminSnapshot(cur))
		recs[i].After = audit.Redact(users.AdminSnapshot(sesudah))
	}
	for _, rec := range recs {
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return users.AdminUser{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return users.AdminUser{}, fmt.Errorf("commit ubah admin: %w", err)
	}
	return sesudah, nil
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
