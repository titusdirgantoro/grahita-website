// Package postgres menghubungkan domain auth ke tabel admin_users,
// branch_users, dan sessions lewat query sqlc.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo mengimplementasikan auth.UserStore dan auth.SessionStore.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository auth.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// AdminByEmail memuat akun admin berdasarkan email.
func (r *Repo) AdminByEmail(ctx context.Context, email string) (auth.Credentials, error) {
	row, err := r.q.GetAdminUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Credentials{}, auth.ErrNoUser
	}
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("query GetAdminUserByEmail: %w", err)
	}
	return auth.Credentials{
		UserID:       row.ID,
		Kind:         auth.KindAdmin,
		Role:         auth.Role(row.Role),
		Nama:         row.Nama,
		Email:        row.Email,
		PasswordHash: pgtypes.TextOr(row.PasswordHash),
		Active:       row.BolehLogin,
		IsActive:     row.IsActive,
	}, nil
}

// AdminByID memuat akun admin berdasarkan id.
func (r *Repo) AdminByID(ctx context.Context, id string) (auth.Credentials, error) {
	row, err := r.q.GetAdminUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Credentials{}, auth.ErrNoUser
	}
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("query GetAdminUserByID: %w", err)
	}
	return auth.Credentials{
		UserID:       row.ID,
		Kind:         auth.KindAdmin,
		Role:         auth.Role(row.Role),
		Nama:         row.Nama,
		Email:        row.Email,
		PasswordHash: pgtypes.TextOr(row.PasswordHash),
		Active:       row.BolehLogin,
		IsActive:     row.IsActive,
	}, nil
}

// BranchByEmail memuat akun cabang berdasarkan email.
func (r *Repo) BranchByEmail(ctx context.Context, email string) (auth.Credentials, error) {
	row, err := r.q.GetBranchUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Credentials{}, auth.ErrNoUser
	}
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("query GetBranchUserByEmail: %w", err)
	}
	return auth.Credentials{
		UserID:       row.ID,
		Kind:         auth.KindBranch,
		Role:         auth.RoleBranch,
		BranchID:     row.BranchID,
		Nama:         row.BranchNama,
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		Active:       row.BolehLogin,
	}, nil
}

// BranchByID memuat akun cabang berdasarkan id.
func (r *Repo) BranchByID(ctx context.Context, id string) (auth.Credentials, error) {
	row, err := r.q.GetBranchUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Credentials{}, auth.ErrNoUser
	}
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("query GetBranchUserByID: %w", err)
	}
	return auth.Credentials{
		UserID:       row.ID,
		Kind:         auth.KindBranch,
		Role:         auth.RoleBranch,
		BranchID:     row.BranchID,
		Nama:         row.BranchNama,
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		Active:       row.BolehLogin,
	}, nil
}

// TouchLogin mencatat waktu login terakhir.
func (r *Repo) TouchLogin(ctx context.Context, kind auth.Kind, id string) error {
	if kind == auth.KindAdmin {
		return r.q.TouchAdminLogin(ctx, id)
	}
	return r.q.TouchBranchLogin(ctx, id)
}

// Create menyimpan sesi baru dan mengembalikan barisnya.
func (r *Repo) Create(ctx context.Context, s auth.Session, tokenHash []byte, meta auth.RequestMeta) (auth.Session, error) {
	row, err := r.q.CreateSession(ctx, sqlc.CreateSessionParams{
		ActorKind: sqlc.ActorKind(s.Kind),
		UserID:    s.ActorID,
		BranchID:  pgtypes.UUID(s.BranchID),
		TokenHash: tokenHash,
		Ip:        meta.IP,
		UserAgent: meta.UserAgent,
		ExpiresAt: s.ExpiresAt,
	})
	if err != nil {
		return auth.Session{}, fmt.Errorf("query CreateSession: %w", err)
	}
	s.ID = row.ID
	s.ExpiresAt = row.ExpiresAt
	s.LastSeenAt = time.Now().UTC()
	return s, nil
}

// ByTokenHash memuat sesi yang belum dicabut.
func (r *Repo) ByTokenHash(ctx context.Context, tokenHash []byte) (auth.Session, error) {
	row, err := r.q.GetSessionByToken(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Session{}, auth.ErrSessionInvalid
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("query GetSessionByToken: %w", err)
	}
	return auth.Session{
		ID:         row.ID,
		ActorID:    row.UserID,
		Kind:       auth.Kind(row.ActorKind),
		BranchID:   pgtypes.UUIDString(row.BranchID),
		ExpiresAt:  row.ExpiresAt,
		LastSeenAt: row.LastSeenAt,
	}, nil
}

// Touch memperbarui last_seen_at.
func (r *Repo) Touch(ctx context.Context, id string, _ time.Time) error {
	return r.q.TouchSession(ctx, id)
}

// Revoke mencabut satu sesi.
func (r *Repo) Revoke(ctx context.Context, id string) error {
	return r.q.RevokeSession(ctx, id)
}

// RevokeForUser mencabut seluruh sesi satu pengguna.
func (r *Repo) RevokeForUser(ctx context.Context, userID string) error {
	return r.q.RevokeSessionsForUser(ctx, userID)
}

// RevokeOthers mencabut seluruh sesi satu pengguna kecuali sesi yang dipakai.
func (r *Repo) RevokeOthers(ctx context.Context, userID, keepID string) error {
	return r.q.RevokeOtherSessions(ctx, sqlc.RevokeOtherSessionsParams{UserID: userID, ID: keepID})
}

// UpdatePassword menyimpan hash Argon2id baru untuk satu akun.
func (r *Repo) UpdatePassword(ctx context.Context, kind auth.Kind, id, passwordHash string) error {
	if kind == auth.KindAdmin {
		if err := r.q.UpdateAdminPassword(ctx, sqlc.UpdateAdminPasswordParams{ID: id, PasswordHash: pgtypes.Text(passwordHash)}); err != nil {
			return fmt.Errorf("query UpdateAdminPassword: %w", err)
		}
		return nil
	}
	if err := r.q.UpdateBranchPassword(ctx, sqlc.UpdateBranchPasswordParams{ID: id, PasswordHash: passwordHash}); err != nil {
		return fmt.Errorf("query UpdateBranchPassword: %w", err)
	}
	return nil
}

// ActivateAdmin menyimpan kata sandi baru DAN menandai admin sudah diaktivasi
// (password_hash + activated_at). Keduanya wajib terisi bersamaan.
func (r *Repo) ActivateAdmin(ctx context.Context, id, passwordHash string) error {
	if err := r.q.ActivateAdminUser(ctx, sqlc.ActivateAdminUserParams{ID: id, PasswordHash: pgtypes.Text(passwordHash)}); err != nil {
		return fmt.Errorf("query ActivateAdminUser: %w", err)
	}
	return nil
}

// UpdateName menyimpan nama tampilan akun ADMIN. Akun cabang ditolak karena
// namanya berasal dari master cabang (PRD §3.8).
//
// Perubahan dan catatan auditnya (F2-28) ditulis dalam SATU transaksi: nama
// lama dibaca dengan FOR UPDATE, dan entri audit menyimpan before/after dari
// baris yang sama sehingga snapshot-nya tidak pernah basi. Mengirim nama yang
// sama dengan yang berlaku adalah no-op: tidak ada UPDATE dan tidak ada entri
// audit. Kegagalan menulis entri audit membatalkan perubahan nama sekaligus.
func (r *Repo) UpdateName(ctx context.Context, kind auth.Kind, id, nama string) error {
	if kind != auth.KindAdmin {
		return auth.ErrProfileNotEditable
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi ubah nama admin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lama string
	if err := tx.QueryRow(ctx, `SELECT nama FROM admin_users WHERE id = $1 FOR UPDATE`, id).Scan(&lama); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrNoUser
		}
		return fmt.Errorf("baca nama admin saat ini: %w", err)
	}
	if lama == nama {
		// Tidak ada perubahan nyata: tidak ada tulisan, tidak ada audit.
		return nil
	}
	if err := r.q.WithTx(tx).UpdateAdminNama(ctx, sqlc.UpdateAdminNamaParams{ID: id, Nama: nama}); err != nil {
		return fmt.Errorf("query UpdateAdminNama: %w", err)
	}
	rec := audit.Entry{
		ActorKind: auth.KindAdmin,
		ActorID:   id,
		// Nama pelaku yang tercatat adalah nama yang berlaku saat aksi
		// dilakukan (nama lama) — identitas yang dikenal pada saat itu.
		ActorNama: lama,
		IP:        audit.RequestMetaFrom(ctx).IP,
		Entity:    audit.EntityAdminUser,
		EntityID:  id,
		Action:    audit.ActionUpdate,
		Before:    map[string]any{"nama": lama},
		After:     map[string]any{"nama": nama},
	}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ubah nama admin: %w", err)
	}
	return nil
}

// CreateResetToken membatalkan token atur ulang lama milik pengguna lalu
// menyimpan yang baru — hanya satu tautan berlaku pada satu waktu.
//
// Pembatalan dan penyimpanan terjadi dalam SATU pernyataan SQL (data-modifying
// CTE) supaya kegagalan separuh jalan tidak mungkin meninggalkan akun tanpa
// tautan aktif (catatan QA F1-04 untuk F1-16).
//
// Bila t.Audit terisi (penerbitan tautan oleh admin, F2-28), entri auditnya
// ditulis di dalam TRANSAKSI YANG SAMA dengan token barunya: kegagalan
// menulis entri audit membatalkan token itu, sehingga tidak mungkin ada
// tautan yang terbit tanpa catatan (temuan QA putaran 1 untuk F2-28).
func (r *Repo) CreateResetToken(ctx context.Context, t auth.ResetToken, tokenHash []byte) error {
	params := sqlc.ReplacePasswordResetTokenParams{
		ActorKind: sqlc.ActorKind(t.Kind),
		UserID:    t.UserID,
		TokenHash: tokenHash,
		ExpiresAt: t.ExpiresAt,
	}
	if t.Audit == nil {
		// Alur publik (lupa kata sandi): token saja, tanpa catatan audit.
		if err := r.q.ReplacePasswordResetToken(ctx, params); err != nil {
			return fmt.Errorf("query ReplacePasswordResetToken: %w", err)
		}
		return nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi token atur ulang: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.q.WithTx(tx).ReplacePasswordResetToken(ctx, params); err != nil {
		return fmt.Errorf("query ReplacePasswordResetToken: %w", err)
	}
	if err := auditpg.Insert(ctx, tx, resetAuditEntry(t.Audit)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit token atur ulang: %w", err)
	}
	return nil
}

// resetAuditEntry menerjemahkan fakta audit paket auth menjadi audit.Entry.
// Penerjemahan ini hidup di lapisan penyimpanan karena paket auth tidak boleh
// mengimpor paket audit (audit mengimpor auth).
func resetAuditEntry(a *auth.ResetAudit) audit.Entry {
	return audit.Entry{
		ActorKind: a.ActorKind,
		ActorID:   a.ActorID,
		ActorNama: a.ActorNama,
		IP:        a.IP,
		Entity:    a.Entity,
		EntityID:  a.EntityID,
		Action:    a.Action,
		Before:    audit.Redact(a.Before),
		After:     audit.Redact(a.After),
	}
}

// ConsumeResetToken memakai token sekali pakai secara atomik.
func (r *Repo) ConsumeResetToken(ctx context.Context, tokenHash []byte, now time.Time) (auth.ResetToken, error) {
	row, err := r.q.ConsumePasswordResetToken(ctx, sqlc.ConsumePasswordResetTokenParams{
		TokenHash: tokenHash,
		ExpiresAt: now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ResetToken{}, auth.ErrResetInvalid
	}
	if err != nil {
		return auth.ResetToken{}, fmt.Errorf("query ConsumePasswordResetToken: %w", err)
	}
	return auth.ResetToken{
		ID:        row.ID,
		Kind:      auth.Kind(row.ActorKind),
		UserID:    row.UserID,
		ExpiresAt: row.ExpiresAt,
	}, nil
}
