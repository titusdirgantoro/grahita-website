package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/alexedwards/argon2id"

	"github.com/grahita/portal/api/internal/platform/clock"
)

// Credentials adalah data akun yang dibutuhkan untuk login.
type Credentials struct {
	UserID   string
	Kind     Kind
	Role     Role
	BranchID string
	Nama     string
	Email    string
	// PasswordHash kosong untuk admin yang belum diaktivasi (undangan).
	PasswordHash string
	// Active berarti akun boleh login: akunnya aktif, cabangnya aktif (untuk
	// akun cabang), dan admin sudah diaktivasi. Repository yang menghitungnya
	// dari skema, bukan handler.
	Active bool
	// IsActive adalah flag `is_active` akun apa adanya (tanpa syarat aktivasi).
	// Dipakai untuk membedakan admin UNDANGAN (is_active true, belum punya
	// kata sandi) dari admin NONAKTIF (is_active false): hanya yang pertama
	// boleh memakai tautan undangan untuk mengaktifkan akun (PRD AP-USR-02).
	IsActive bool
}

// ErrNoUser dikembalikan store bila akun tidak ditemukan.
var ErrNoUser = errors.New("akun tidak ditemukan")

// UserStore memuat dan memperbarui akun cabang maupun admin.
type UserStore interface {
	AdminByEmail(ctx context.Context, email string) (Credentials, error)
	AdminByID(ctx context.Context, id string) (Credentials, error)
	BranchByEmail(ctx context.Context, email string) (Credentials, error)
	BranchByID(ctx context.Context, id string) (Credentials, error)
	TouchLogin(ctx context.Context, kind Kind, id string) error
	// UpdatePassword menyimpan hash Argon2id baru untuk satu akun.
	UpdatePassword(ctx context.Context, kind Kind, id, passwordHash string) error
	// ActivateAdmin menyimpan hash Argon2id DAN activated_at sekaligus untuk
	// admin hasil undangan. Keduanya wajib terisi bersamaan: aturan
	// boleh_login di SQL menolak akun yang salah satunya NULL.
	ActivateAdmin(ctx context.Context, id, passwordHash string) error
	// UpdateName menyimpan nama tampilan baru untuk satu akun admin. Akun
	// cabang tidak punya nama sendiri (namanya dari master cabang), jadi
	// implementasinya menolak KindBranch.
	UpdateName(ctx context.Context, kind Kind, id, nama string) error
}

// Service menyatukan login, pemuatan aktor dari sesi, logout, dan siklus
// hidup kata sandi (lupa, atur ulang, ganti).
type Service struct {
	users    UserStore
	sessions SessionStore
	resets   ResetStore
	mailer   ResetMailer
	clock    clock.Clock
	idle     time.Duration
	absolute time.Duration
}

// NewService membuat service auth. idle 8 jam dan absolute 30 hari adalah
// nilai dari Architecture.md §6.
func NewService(users UserStore, sessions SessionStore, resets ResetStore, clk clock.Clock, idle, absolute time.Duration) *Service {
	if clk == nil {
		clk = clock.System()
	}
	return &Service{
		users:    users,
		sessions: sessions,
		resets:   resets,
		clock:    clk,
		idle:     idle,
		absolute: absolute,
	}
}

// SetResetMailer memasang pengirim email tautan atur ulang. Bila tidak
// dipasang, token tetap dibuat dan disimpan, tetapi email tidak dikirim.
func (s *Service) SetResetMailer(m ResetMailer) { s.mailer = m }

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
		// Termasuk admin undangan yang belum punya kata sandi sama sekali:
		// jawabannya identik dengan kata sandi salah supaya email yang belum
		// diaktivasi tidak bisa dienumerasi.
		return IssuedSession{}, ErrInvalidCredentials
	}
	// Kredensial benar tetapi akun belum/tidak boleh masuk: akun nonaktif,
	// cabang induk nonaktif, atau admin yang belum diaktivasi.
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
		slog.Warn("gagal mencatat last_login_at", "err", err, "kind", creds.Kind, "user_id", creds.UserID)
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
		return Anonymous(), err
	}
	now := s.clock.Now()
	if sess.Expired(now, s.idle) {
		if err := s.sessions.Revoke(ctx, sess.ID); err != nil {
			slog.Warn("gagal mencabut sesi kedaluwarsa", "err", err, "session_id", sess.ID)
		}
		return Anonymous(), ErrSessionInvalid
	}

	creds, err := s.findByID(ctx, sess.Kind, sess.ActorID)
	if err != nil {
		return Anonymous(), ErrSessionInvalid
	}
	// Akun yang dinonaktifkan (atau cabangnya dinonaktifkan) langsung
	// kehilangan sesinya, bukan hanya ditolak saat login berikutnya.
	if !creds.Active {
		if err := s.sessions.Revoke(ctx, sess.ID); err != nil {
			slog.Warn("gagal mencabut sesi akun nonaktif", "err", err, "session_id", sess.ID)
		}
		return Anonymous(), ErrInactiveAccount
	}
	if err := s.sessions.Touch(ctx, sess.ID, now); err != nil {
		slog.Warn("gagal memperbarui last_seen_at", "err", err, "session_id", sess.ID)
	}

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
// atur ulang kata sandi (PRD CP-AUT-07).
func (s *Service) RevokeAllForUser(ctx context.Context, userID string) error {
	return s.sessions.RevokeForUser(ctx, userID)
}

// ForgotPassword membuat tautan atur ulang dan mengirimkannya lewat email.
//
// Kontrak keamanan (PRD CP-AUT-03): pemanggil selalu mendapat hasil yang sama.
// Akun yang tidak ada, tidak aktif, atau gagal kirim email TIDAK dibedakan —
// kegagalan teknis hanya dicatat di log supaya endpoint tidak bisa dipakai
// untuk menebak email terdaftar.
func (s *Service) ForgotPassword(ctx context.Context, email string, ttl time.Duration, _ RequestMeta) error {
	email = NormalizeEmail(email)
	if email == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultResetTTL
	}

	creds, err := s.findByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			return nil // akun tidak ada — jangan membocorkan
		}
		return err
	}
	if !creds.Active {
		return nil
	}

	token, hash, err := NewToken()
	if err != nil {
		return err
	}
	rt := ResetToken{
		Kind:      creds.Kind,
		UserID:    creds.UserID,
		ExpiresAt: s.clock.Now().Add(ttl),
	}
	if err := s.resets.CreateResetToken(ctx, rt, hash); err != nil {
		return fmt.Errorf("simpan token atur ulang: %w", err)
	}
	if s.mailer == nil {
		slog.Warn("pengirim email atur ulang belum dipasang; tautan tidak dikirim",
			"kind", creds.Kind, "user_id", creds.UserID)
		return nil
	}
	if err := s.mailer.SendPasswordReset(ctx, ResetMessage{
		To:        creds.Email,
		Nama:      creds.Nama,
		Kind:      creds.Kind,
		Token:     token,
		ExpiresAt: rt.ExpiresAt,
		TTL:       ttl,
	}); err != nil {
		slog.Error("gagal mengirim email atur ulang kata sandi", "err", err, "kind", creds.Kind)
	}
	return nil
}

// ResetPassword menetapkan kata sandi baru dari token sekali pakai lalu
// mencabut SELURUH sesi pengguna itu (PRD CP-AUT-04, CP-AUT-07).
//
// Kata sandi divalidasi sebelum token dipakai supaya percobaan dengan kata
// sandi lemah tidak membakar tautan yang masih berlaku.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string, _ RequestMeta) error {
	if token == "" {
		return ErrResetInvalid
	}
	if err := CheckPasswordStrength(newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	rt, err := s.resets.ConsumeResetToken(ctx, HashToken(token), s.clock.Now())
	if err != nil {
		return err
	}
	creds, err := s.findByID(ctx, rt.Kind, rt.UserID)
	if err != nil {
		return ErrResetInvalid
	}
	// Admin hasil undangan memakai tautan yang sama untuk AKTIVASI: akunnya
	// masih is_active=true tetapi password_hash dan activated_at NULL. Yang
	// boleh memakai jalur ini hanya akun yang masih aktif; admin yang sudah
	// dinonaktifkan tetap ditolak (PRD AP-USR-02).
	if rt.Kind == KindAdmin {
		if !creds.IsActive {
			return ErrInactiveAccount
		}
		// Aktivasi mengisi password_hash DAN activated_at bersamaan
		// (kewajiban turunan F1-04); untuk admin yang sudah aktif ini sama
		// dengan mengganti kata sandi.
		if err := s.users.ActivateAdmin(ctx, rt.UserID, hash); err != nil {
			return err
		}
	} else {
		if !creds.Active {
			return ErrInactiveAccount
		}
		if err := s.users.UpdatePassword(ctx, rt.Kind, rt.UserID, hash); err != nil {
			return err
		}
	}
	if err := s.sessions.RevokeForUser(ctx, rt.UserID); err != nil {
		return err
	}
	return nil
}

// IssuedReset adalah tautan atur ulang yang sudah dibuat tetapi BELUM dikirim.
type IssuedReset struct {
	// Token adalah nilai mentah satu-satunya; yang tersimpan di database
	// hanya SHA-256-nya.
	Token     string
	ExpiresAt time.Time
}

// IssuePasswordResetFor membuat tautan atur ulang sekali pakai untuk satu akun
// TANPA mengirim email apa pun. Dipakai admin saat mengundang admin baru:
// tautan aktivasi dikirim lewat outbox undangan (F1-16), bukan mailer reset.
//
// Aturan penerima sama dengan pengiriman tautan: akun cabang harus boleh
// login, admin harus masih aktif (undangan boleh, nonaktif tidak).
func (s *Service) IssuePasswordResetFor(ctx context.Context, kind Kind, userID string, ttl time.Duration) (IssuedReset, error) {
	creds, rt, err := s.issueResetFor(ctx, kind, userID, ttl)
	if err != nil {
		return IssuedReset{}, err
	}
	_ = creds
	return IssuedReset{Token: rt.token, ExpiresAt: rt.expiresAt}, nil
}

// SendPasswordResetFor membuat tautan atur ulang untuk satu akun lalu
// mengirimkannya lewat ResetMailer. Dipakai admin saat mengirim ulang tautan
// reset akun cabang (PRD §4.8) — jalur email yang sama dengan lupa kata sandi,
// sehingga pemindahan ke email_outbox (F1-16) cukup di satu tempat.
func (s *Service) SendPasswordResetFor(ctx context.Context, kind Kind, userID string, ttl time.Duration) error {
	creds, rt, err := s.issueResetFor(ctx, kind, userID, ttl)
	if err != nil {
		return err
	}
	if s.mailer == nil {
		slog.Warn("pengirim email atur ulang belum dipasang; tautan tidak dikirim",
			"kind", kind, "user_id", userID)
		return nil
	}
	if err := s.mailer.SendPasswordReset(ctx, ResetMessage{
		To:        creds.Email,
		Nama:      creds.Nama,
		Kind:      creds.Kind,
		Token:     rt.token,
		ExpiresAt: rt.expiresAt,
		TTL:       rt.ttl,
	}); err != nil {
		slog.Error("gagal mengirim email atur ulang kata sandi", "err", err, "kind", kind)
		return fmt.Errorf("kirim email atur ulang: %w", err)
	}
	return nil
}

type issuedReset struct {
	token     string
	expiresAt time.Time
	ttl       time.Duration
}

// issueResetFor adalah inti bersama: validasi akun, batalkan tautan lama,
// lalu simpan tautan baru.
func (s *Service) issueResetFor(ctx context.Context, kind Kind, userID string, ttl time.Duration) (Credentials, issuedReset, error) {
	if ttl <= 0 {
		ttl = DefaultResetTTL
	}
	creds, err := s.findByID(ctx, kind, userID)
	if err != nil {
		return Credentials{}, issuedReset{}, err
	}
	if !s.mayHoldResetLink(creds) {
		return Credentials{}, issuedReset{}, ErrInactiveAccount
	}
	token, hash, err := NewToken()
	if err != nil {
		return Credentials{}, issuedReset{}, err
	}
	expiresAt := s.clock.Now().Add(ttl)
	if err := s.resets.CreateResetToken(ctx, ResetToken{
		Kind:      creds.Kind,
		UserID:    creds.UserID,
		ExpiresAt: expiresAt,
	}, hash); err != nil {
		return Credentials{}, issuedReset{}, fmt.Errorf("simpan token atur ulang: %w", err)
	}
	return creds, issuedReset{token: token, expiresAt: expiresAt, ttl: ttl}, nil
}

// mayHoldResetLink melaporkan apakah akun boleh memegang tautan atur ulang /
// aktivasi: akun cabang harus boleh login (akun & cabang aktif), sedangkan
// admin harus is_active — undangan yang belum diaktivasi boleh, admin
// nonaktif tidak (PRD AP-USR-02).
func (s *Service) mayHoldResetLink(creds Credentials) bool {
	if creds.Kind == KindAdmin {
		return creds.IsActive
	}
	return creds.Active
}

// ChangePassword mengganti kata sandi pengguna yang sedang login setelah
// memverifikasi kata sandi lama, lalu mencabut seluruh sesi LAIN miliknya
// sementara sesi yang sedang dipakai tetap hidup (PRD CP-AUT-06, CP-AUT-07).
func (s *Service) ChangePassword(ctx context.Context, actor Actor, oldPassword, newPassword string, _ RequestMeta) error {
	if !actor.IsAuthenticated() {
		return ErrSessionInvalid
	}
	creds, err := s.findByID(ctx, actor.Kind, actor.UserID)
	if err != nil {
		return ErrSessionInvalid
	}
	if !VerifyPassword(creds.PasswordHash, oldPassword) {
		return ErrWrongPassword
	}
	if VerifyPassword(creds.PasswordHash, newPassword) {
		// Kata sandi baru identik dengan yang lama: tidak ada yang berubah,
		// dan mencabut sesi lain hanya akan mengganggu tanpa manfaat.
		return ErrPasswordTooWeak
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.users.UpdatePassword(ctx, actor.Kind, actor.UserID, hash); err != nil {
		return err
	}
	if err := s.sessions.RevokeOthers(ctx, actor.UserID, actor.SessionID); err != nil {
		return err
	}
	return nil
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

// dummyHash adalah hash Argon2id sungguhan atas string tetap, hanya untuk
// menyeragamkan waktu respons saat akun tidak ditemukan.
var dummyHash = func() string {
	h, err := argon2id.CreateHash("grahita-dummy-password", &PasswordPolicy.ArgonParams)
	if err != nil {
		return ""
	}
	return h
}()
