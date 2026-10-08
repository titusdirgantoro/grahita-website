// Package postgres menghubungkan domain auth ke tabel admin_users,
// branch_users, dan sessions lewat query sqlc.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo mengimplementasikan auth.UserStore dan auth.SessionStore.
type Repo struct {
	q *sqlc.Queries
}

// New membuat repository auth.
func New(pool *database.Pool) *Repo { return &Repo{q: sqlc.New(pool)} }

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
		Active:       row.IsActive,
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
		Active:       row.IsActive,
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
		Active:       row.IsActive,
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
		Active:       row.IsActive,
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
