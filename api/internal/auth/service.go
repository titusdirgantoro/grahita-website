package auth

import (
	"context"
	"errors"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
)

// Credentials adalah data akun yang dibutuhkan untuk login.
type Credentials struct {
	UserID       string
	Kind         Kind
	Role         Role
	BranchID     string
	Nama         string
	Email        string
	PasswordHash string
	Active       bool
}

// ErrNoUser dikembalikan store bila akun tidak ditemukan.
var ErrNoUser = errors.New("akun tidak ditemukan")

// UserStore memuat akun cabang maupun admin.
type UserStore interface {
	AdminByEmail(ctx context.Context, email string) (Credentials, error)
	AdminByID(ctx context.Context, id string) (Credentials, error)
	BranchByEmail(ctx context.Context, email string) (Credentials, error)
	BranchByID(ctx context.Context, id string) (Credentials, error)
	TouchLogin(ctx context.Context, kind Kind, id string) error
}

// Service menyatukan login, pemuatan aktor dari sesi, dan logout.
type Service struct {
	users    UserStore
	sessions SessionStore
	clock    clock.Clock
	idle     time.Duration
	absolute time.Duration
}

// NewService membuat service auth. idle 8 jam dan absolute 30 hari adalah
// nilai dari Architecture.md §6.
func NewService(users UserStore, sessions SessionStore, clk clock.Clock, idle, absolute time.Duration) *Service {
	if clk == nil {
		clk = clock.System()
	}
	return &Service{users: users, sessions: sessions, clock: clk, idle: idle, absolute: absolute}
}

// IssuedSession adalah hasil login: aktor + token mentah yang harus
// dituliskan ke cookie oleh lapisan transport.
type IssuedSession struct {
	Actor     Actor
	Token     string
	ExpiresAt time.Time
}

// Login memverifikasi kredensial dan membuat sesi baru.
//
// Pencarian akun dilakukan pada admin lalu cabang sehingga satu endpoint
// login melayani kedua portal; identitas ditentukan konteks akun, bukan
// hostname (keputusan domain user).
func (s *Service) Login(ctx context.Context, email, password string, meta RequestMeta) (IssuedSession, error) {
	email = NormalizeEmail(email)
	if email == "" || password == "" {
		return IssuedSession{}, ErrInvalidCredentials
	}

	creds, err := s.findByEmail(ctx, email)
	if err != nil {
		return IssuedSession{}, err
	}
	if !VerifyPassword(creds.PasswordHash, password) {
		return IssuedSession{}, ErrInvalidCredentials
	}
	if !creds.Active {
		return IssuedSession{}, ErrInactiveAccount
	}

	token, hash, err := NewToken()
	if err != nil {
		return IssuedSession{}, err
	}
	now := s.clock.Now()
	sess, err := s.sessions.Create(ctx, Session{
		ActorID:   creds.UserID,
		Kind:      creds.Kind,
		BranchID:  creds.BranchID,
		ExpiresAt: now.Add(s.absolute),
	}, hash, meta)
	if err != nil {
		return IssuedSession{}, err
	}
	if err := s.users.TouchLogin(ctx, creds.Kind, creds.UserID); err != nil {
		// Kegagalan mencatat waktu login tidak boleh menggagalkan login.
		_ = err
	}

	return IssuedSession{
		Actor: Actor{
			UserID:    creds.UserID,
			Kind:      creds.Kind,
			Role:      creds.Role,
			BranchID:  creds.BranchID,
			Nama:      creds.Nama,
			Email:     creds.Email,
			SessionID: sess.ID,
		},
		Token:     token,
		ExpiresAt: sess.ExpiresAt,
	}, nil
}

// Authenticate memuat aktor dari token cookie.
func (s *Service) Authenticate(ctx context.Context, token string) (Actor, error) {
	if token == "" {
		return Anonymous(), ErrSessionInvalid
	}
	sess, err := s.sessions.ByTokenHash(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, ErrSessionInvalid) {
			return Anonymous(), err
		}
		return Anonymous(), err
	}
	now := s.clock.Now()
	if sess.Expired(now, s.idle) {
		_ = s.sessions.Revoke(ctx, sess.ID)
		return Anonymous(), ErrSessionInvalid
	}

	creds, err := s.findByID(ctx, sess.Kind, sess.ActorID)
	if err != nil {
		return Anonymous(), ErrSessionInvalid
	}
	if !creds.Active {
		_ = s.sessions.Revoke(ctx, sess.ID)
		return Anonymous(), ErrInactiveAccount
	}
	_ = s.sessions.Touch(ctx, sess.ID, now)

	return Actor{
		UserID:    creds.UserID,
		Kind:      creds.Kind,
		Role:      creds.Role,
		BranchID:  creds.BranchID,
		Nama:      creds.Nama,
		Email:     creds.Email,
		SessionID: sess.ID,
	}, nil
}

// Logout mencabut sesi milik token tersebut (idempoten).
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	sess, err := s.sessions.ByTokenHash(ctx, HashToken(token))
	if err != nil {
		return nil
	}
	return s.sessions.Revoke(ctx, sess.ID)
}

// RevokeAllForUser mencabut seluruh sesi seorang pengguna — dipakai setelah
// ganti/reset password (PRD CP-AUT-07).
func (s *Service) RevokeAllForUser(ctx context.Context, userID string) error {
	return s.sessions.RevokeForUser(ctx, userID)
}

func (s *Service) findByEmail(ctx context.Context, email string) (Credentials, error) {
	if creds, err := s.users.AdminByEmail(ctx, email); err == nil {
		return creds, nil
	} else if !errors.Is(err, ErrNoUser) {
		return Credentials{}, err
	}
	if creds, err := s.users.BranchByEmail(ctx, email); err == nil {
		return creds, nil
	} else if !errors.Is(err, ErrNoUser) {
		return Credentials{}, err
	}
	// Akun tidak ada: tetap jalankan verifikasi hash palsu agar waktu respons
	// seragam dan tidak bisa dipakai menebak email terdaftar.
	_ = VerifyPassword(dummyHash, "x")
	return Credentials{}, ErrInvalidCredentials
}

func (s *Service) findByID(ctx context.Context, kind Kind, id string) (Credentials, error) {
	if kind == KindAdmin {
		return s.users.AdminByID(ctx, id)
	}
	return s.users.BranchByID(ctx, id)
}

// dummyHash adalah hash Argon2id valid atas string acak, hanya untuk
// menyeragamkan waktu respons.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$c29tZXNhbHR2YWx1ZQ$3a6dQ0N3xT0nE9Q5jV6mZ8sJ0kZ9F1yH2pL4wR7uV2o"
