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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.Session{}, fmt.Errorf("mulai transaksi sesi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialkan penerbitan sesi dengan mutasi identitas. Mutasi cabang selalu
	// mengunci branches lebih dulu, lalu mencabut credential/sesi. Jalur ini
	// memakai urutan yang sama dan memeriksa ulang kelayakan setelah lock:
	// bila login menang, revokasi berikutnya pasti melihat sesi baru; bila
	// revokasi menang, INSERT ditolak setelah menunggu commit-nya.
	locked, err := lockEligibleActor(ctx, tx, s.Kind, s.ActorID, s.BranchID, false)
	if err != nil {
		return auth.Session{}, err
	}
	if locked.passwordHash != s.ExpectedPasswordHash {
		return auth.Session{}, auth.ErrInvalidCredentials
	}
	row, err := r.q.WithTx(tx).CreateSession(ctx, sqlc.CreateSessionParams{
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
	if err := tx.Commit(ctx); err != nil {
		return auth.Session{}, fmt.Errorf("commit sesi: %w", err)
	}
	s.ID = row.ID
	s.ExpiresAt = row.ExpiresAt
	s.LastSeenAt = time.Now().UTC()
	s.ExpectedPasswordHash = ""
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

// ChangePassword memvalidasi ulang generasi credential dan sesi peminta lalu
// mengganti hash serta mencabut sesi lain dalam satu transaksi. Untuk cabang,
// lockEligibleActor menjaga urutan branches -> branch_users.
func (r *Repo) ChangePassword(ctx context.Context, kind auth.Kind, id, sessionID, expectedPasswordHash, passwordHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi ganti kata sandi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockEligibleActor(ctx, tx, kind, id, "", false)
	if err != nil {
		if errors.Is(err, auth.ErrInactiveAccount) || errors.Is(err, auth.ErrNoUser) {
			return auth.ErrSessionInvalid
		}
		return err
	}
	if locked.passwordHash != expectedPasswordHash {
		return auth.ErrSessionInvalid
	}
	var lockedSession string
	err = tx.QueryRow(ctx, `
SELECT id
  FROM sessions
 WHERE id = $1 AND actor_kind = $2 AND user_id = $3
   AND revoked_at IS NULL AND expires_at > now()
 FOR UPDATE`, sessionID, kind, id).Scan(&lockedSession)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrSessionInvalid
	}
	if err != nil {
		return fmt.Errorf("kunci sesi ganti kata sandi: %w", err)
	}
	qtx := r.q.WithTx(tx)
	if kind == auth.KindAdmin {
		if err := qtx.UpdateAdminPassword(ctx, sqlc.UpdateAdminPasswordParams{ID: id, PasswordHash: pgtypes.Text(passwordHash)}); err != nil {
			return fmt.Errorf("query UpdateAdminPassword: %w", err)
		}
	} else if err := qtx.UpdateBranchPassword(ctx, sqlc.UpdateBranchPasswordParams{ID: id, PasswordHash: passwordHash}); err != nil {
		return fmt.Errorf("query UpdateBranchPassword: %w", err)
	}
	if err := qtx.RevokeOtherSessions(ctx, sqlc.RevokeOtherSessionsParams{UserID: id, ID: sessionID}); err != nil {
		return fmt.Errorf("query RevokeOtherSessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ganti kata sandi: %w", err)
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
// Pembatalan dan penyimpanan terjadi dalam SATU transaksi. Sebelum CTE
// penggantian token dijalankan, transaksi mengunci identitas pemilik dan
// memeriksa ulang kelayakannya supaya revokasi identitas tidak dapat disusul
// INSERT token baru yang lolos dari pencabutan.
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi token atur ulang: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Untuk cabang, urutannya branches -> branch_users, sama dengan mutasi
	// identitas. Admin dikunci langsung pada admin_users; undangan admin yang
	// masih aktif memang boleh menerima token aktivasi meski belum punya hash.
	locked, err := lockEligibleActor(ctx, tx, t.Kind, t.UserID, "", true)
	if err != nil {
		return err
	}
	if auth.NormalizeEmail(locked.email) != auth.NormalizeEmail(t.ExpectedEmail) ||
		locked.passwordHash != t.ExpectedPasswordHash {
		return auth.ErrInactiveAccount
	}
	if err := r.q.WithTx(tx).ReplacePasswordResetToken(ctx, params); err != nil {
		return fmt.Errorf("query ReplacePasswordResetToken: %w", err)
	}
	if t.Audit != nil {
		if err := auditpg.Insert(ctx, tx, resetAuditEntry(t.Audit)); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit token atur ulang: %w", err)
	}
	return nil
}

// lockEligibleActor mengunci boundary identitas sebelum artefak autentikasi
// diterbitkan. Urutan lock cabang wajib branches -> branch_users agar sama
// dengan branch/postgres.Update dan users/postgres.SetBranchUserActive.
type lockedActor struct {
	email        string
	passwordHash string
}

func lockEligibleActor(ctx context.Context, tx pgx.Tx, kind auth.Kind, userID, branchID string, allowAdminInvitation bool) (lockedActor, error) {
	if kind == auth.KindAdmin {
		var active, activated bool
		var email string
		var passwordHash *string
		err := tx.QueryRow(ctx, `
SELECT is_active, password_hash IS NOT NULL AND activated_at IS NOT NULL, email, password_hash
  FROM admin_users
 WHERE id = $1
	 FOR UPDATE`, userID).Scan(&active, &activated, &email, &passwordHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return lockedActor{}, auth.ErrNoUser
		}
		if err != nil {
			return lockedActor{}, fmt.Errorf("kunci identitas admin: %w", err)
		}
		if !active || (!allowAdminInvitation && !activated) {
			return lockedActor{}, auth.ErrInactiveAccount
		}
		locked := lockedActor{email: email}
		if passwordHash != nil {
			locked.passwordHash = *passwordHash
		}
		return locked, nil
	}

	if branchID == "" {
		if err := tx.QueryRow(ctx, `SELECT branch_id FROM branch_users WHERE id = $1`, userID).Scan(&branchID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return lockedActor{}, auth.ErrNoUser
			}
			return lockedActor{}, fmt.Errorf("baca cabang pemilik akun: %w", err)
		}
	}
	var branchActive, identityReady bool
	err := tx.QueryRow(ctx, `
SELECT is_active,
       identity_status = 'registered' AND nama_kepala IS NOT NULL AND email IS NOT NULL
  FROM branches
 WHERE id = $1
	 FOR UPDATE`, branchID).Scan(&branchActive, &identityReady)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedActor{}, auth.ErrNoUser
	}
	if err != nil {
		return lockedActor{}, fmt.Errorf("kunci identitas cabang: %w", err)
	}

	var userActive, correctBranch bool
	var email, passwordHash string
	err = tx.QueryRow(ctx, `
SELECT is_active, branch_id = $2, email, password_hash
  FROM branch_users
 WHERE id = $1
	 FOR UPDATE`, userID, branchID).Scan(&userActive, &correctBranch, &email, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedActor{}, auth.ErrNoUser
	}
	if err != nil {
		return lockedActor{}, fmt.Errorf("kunci akun cabang: %w", err)
	}
	if !branchActive || !identityReady || !userActive || !correctBranch {
		return lockedActor{}, auth.ErrInactiveAccount
	}
	return lockedActor{email: email, passwordHash: passwordHash}, nil
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

// ResetPassword mengunci identitas, mengonsumsi token, mengganti credential,
// dan mencabut seluruh sesi dalam satu transaksi. Token dibaca tanpa lock
// hanya untuk menemukan boundary identitas; keputusan berlaku dibuat kembali
// oleh UPDATE kondisional setelah lock branches -> branch_users diperoleh.
func (r *Repo) ResetPassword(ctx context.Context, tokenHash []byte, now time.Time, passwordHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi atur ulang kata sandi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var kindText, userID string
	err = tx.QueryRow(ctx, `
SELECT actor_kind::text, user_id
  FROM password_reset_tokens
 WHERE token_hash = $1 AND revoked_at IS NULL AND used_at IS NULL AND expires_at > $2`, tokenHash, now).Scan(&kindText, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrResetInvalid
	}
	if err != nil {
		return fmt.Errorf("baca pemilik token atur ulang: %w", err)
	}
	kind := auth.Kind(kindText)
	if _, err := lockEligibleActor(ctx, tx, kind, userID, "", true); err != nil {
		if errors.Is(err, auth.ErrInactiveAccount) || errors.Is(err, auth.ErrNoUser) {
			return auth.ErrResetInvalid
		}
		return err
	}

	qtx := r.q.WithTx(tx)
	consumed, err := qtx.ConsumePasswordResetToken(ctx, sqlc.ConsumePasswordResetTokenParams{TokenHash: tokenHash, ExpiresAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrResetInvalid
	}
	if err != nil {
		return fmt.Errorf("query ConsumePasswordResetToken: %w", err)
	}
	if consumed.ActorKind != kindText || consumed.UserID != userID {
		return auth.ErrResetInvalid
	}
	if kind == auth.KindAdmin {
		if err := qtx.ActivateAdminUser(ctx, sqlc.ActivateAdminUserParams{ID: userID, PasswordHash: pgtypes.Text(passwordHash)}); err != nil {
			return fmt.Errorf("query ActivateAdminUser: %w", err)
		}
	} else if err := qtx.UpdateBranchPassword(ctx, sqlc.UpdateBranchPasswordParams{ID: userID, PasswordHash: passwordHash}); err != nil {
		return fmt.Errorf("query UpdateBranchPassword: %w", err)
	}
	if err := qtx.RevokeSessionsForUser(ctx, userID); err != nil {
		return fmt.Errorf("query RevokeSessionsForUser: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit atur ulang kata sandi: %w", err)
	}
	return nil
}
