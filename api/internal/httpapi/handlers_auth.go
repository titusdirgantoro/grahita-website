package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// data membungkus payload sukses dengan amplop yang konsisten.
func data(v any) map[string]any { return map[string]any{"data": v} }

// userDTO adalah representasi akun yang boleh dilihat frontend.
type userDTO struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Role     string `json:"role"`
	BranchID string `json:"branch_id,omitempty"`
	Nama     string `json:"nama"`
	Email    string `json:"email"`
}

func actorDTO(a actor) userDTO {
	return userDTO{
		ID:       a.UserID,
		Kind:     string(a.Kind),
		Role:     string(a.Role),
		BranchID: a.BranchID,
		Nama:     a.Nama,
		Email:    a.Email,
	}
}

// loginRequest adalah body POST /api/v1/auth/login.
type loginRequest struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=8,max=200"`
}

// forgotPasswordRequest adalah body POST /api/v1/auth/password/forgot.
type forgotPasswordRequest struct {
	Email string `json:"email" validate:"required,email,max=254"`
}

// resetPasswordRequest adalah body POST /api/v1/auth/password/reset.
type resetPasswordRequest struct {
	Token    string `json:"token" validate:"required,max=512"`
	Password string `json:"password" validate:"required,min=8,max=200"`
}

// changePasswordRequest adalah body POST /api/v1/auth/password/change.
type changePasswordRequest struct {
	PasswordLama string `json:"password_lama" validate:"required,min=8,max=200"`
	PasswordBaru string `json:"password_baru" validate:"required,min=8,max=200"`
}

// requestMeta merangkum metadata request untuk entri audit pengguna.
func requestMeta(r *http.Request) auth.RequestMeta {
	return auth.RequestMeta{IP: httpx.ClientIP(r), UserAgent: r.UserAgent()}
}

// loginAccountKey adalah kunci pembatas percobaan gagal per AKUN; selalu
// dipakai bersama kunci per IP (PRD CP-AUT-05).
func loginAccountKey(email string) string { return "akun:" + email }

// login menangani POST /api/v1/auth/login untuk kedua portal.
//
// Pembatas laju menghitung percobaan GAGAL per akun DAN per IP; percobaan
// yang sudah melewati batas ditolak 429 sebelum kata sandi diperiksa.
func (a *API) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	email := auth.NormalizeEmail(req.Email)
	accountKey := loginAccountKey(email)
	ipKey := httpx.ByIP(r)
	if a.d.LoginLimiter != nil {
		if a.d.LoginLimiter.Exceeded(accountKey) || a.d.LoginLimiter.Exceeded(ipKey) {
			return apierr.RateLimited(rateLimitedLoginMsg)
		}
	}

	issued, err := a.d.Auth.Login(r.Context(), email, req.Password, requestMeta(r))
	if err != nil {
		if a.d.LoginLimiter != nil && !errors.Is(err, auth.ErrSessionInvalid) {
			// Hanya kegagalan yang menambah hitungan; login yang berhasil
			// tidak boleh memakan jatah percobaan pengguna.
			a.d.LoginLimiter.Allow(accountKey)
			a.d.LoginLimiter.Allow(ipKey)
		}
		return mapDomainErr(err)
	}
	if a.d.LoginLimiter != nil {
		// Jatah akun dibersihkan saat login berhasil. Jatah IP sengaja
		// dibiarkan kedaluwarsa sendiri agar kegagalan lintas akun dari satu
		// IP tetap terhitung.
		a.d.LoginLimiter.Reset(accountKey)
	}

	http.SetCookie(w, sessionCookie(a.d.Config, issued.Token, issued.ExpiresAt))
	httpx.OK(w, data(map[string]any{"user": actorDTO(issued.Actor)}))
	return nil
}

// logout menangani POST /api/v1/auth/logout (idempoten).
func (a *API) logout(w http.ResponseWriter, r *http.Request) error {
	if token := sessionToken(a.d.Config, r); token != "" {
		_ = a.d.Auth.Logout(r.Context(), token)
	}
	http.SetCookie(w, clearSessionCookie(a.d.Config))
	httpx.NoContent(w)
	return nil
}

// me mengembalikan profil + role + cabang aktor yang sedang login.
func (a *API) me(w http.ResponseWriter, r *http.Request) error {
	actor := auth.ActorFrom(r.Context())
	httpx.OK(w, data(map[string]any{"user": actorDTO(actor)}))
	return nil
}

// updateMeRequest adalah body PUT /api/v1/auth/me — profil sendiri (PRD §4.10).
//
// Hanya `nama` yang diterima: email dan role read-only, dan field asing
// ditolak oleh DecodeJSON (DisallowUnknownFields) sehingga percobaan mengubah
// `email`/`role` lewat endpoint ini dijawab 400, bukan diabaikan diam-diam.
type updateMeRequest struct {
	Nama string `json:"nama" validate:"required,min=2,max=120"`
}

// meUpdate menangani PUT /api/v1/auth/me.
//
// Kebijakan per jenis akun ditegakkan di service: hanya akun ADMIN yang boleh
// mengganti nama tampilannya; akun cabang dijawab 403 karena nama/kepala/email/
// telepon cabang adalah data master Pusat yang read-only di portal cabang
// (PRD §3.8, §4.10).
func (a *API) meUpdate(w http.ResponseWriter, r *http.Request) error {
	var req updateMeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	baru, err := a.d.Auth.UpdateOwnProfile(r.Context(), auth.ActorFrom(r.Context()), req.Nama)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(map[string]any{"user": actorDTO(baru)}))
	return nil
}

// forgotPassword menangani POST /api/v1/auth/password/forgot.
//
// Respons SELALU sama — akun ada atau tidak, aktif atau tidak — supaya
// endpoint ini tidak bisa dipakai menebak email terdaftar (PRD CP-AUT-03).
func (a *API) forgotPassword(w http.ResponseWriter, r *http.Request) error {
	var req forgotPasswordRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	if a.d.ForgotLimiter != nil {
		if !a.d.ForgotLimiter.Allow("forgot:" + httpx.ClientIP(r)) {
			return apierr.RateLimited(rateLimitedLoginMsg)
		}
	}

	email := auth.NormalizeEmail(req.Email)
	if err := a.d.Auth.ForgotPassword(r.Context(), email, a.passwordResetTTL(r), requestMeta(r)); err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(map[string]any{
		"pesan": "Jika email terdaftar, tautan atur ulang kata sandi sudah dikirim. Tautan berlaku terbatas dan hanya bisa dipakai sekali.",
	}))
	return nil
}

// resetPassword menangani POST /api/v1/auth/password/reset.
func (a *API) resetPassword(w http.ResponseWriter, r *http.Request) error {
	var req resetPasswordRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if a.d.ForgotLimiter != nil {
		if !a.d.ForgotLimiter.Allow("reset:" + httpx.ClientIP(r)) {
			return apierr.RateLimited(rateLimitedLoginMsg)
		}
	}

	err := a.d.Auth.ResetPassword(r.Context(), req.Token, req.Password, requestMeta(r))
	switch {
	case err == nil:
		httpx.NoContent(w)
		return nil
	case errors.Is(err, auth.ErrPasswordTooWeak):
		return apierr.Validation("Kata sandi baru belum memenuhi syarat.",
			map[string]string{"password": err.Error()})
	default:
		return mapDomainErr(err)
	}
}

// changePassword menangani POST /api/v1/auth/password/change (butuh login).
func (a *API) changePassword(w http.ResponseWriter, r *http.Request) error {
	var req changePasswordRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	actor := auth.ActorFrom(r.Context())
	err := a.d.Auth.ChangePassword(r.Context(), actor, req.PasswordLama, req.PasswordBaru, requestMeta(r))
	switch {
	case err == nil:
		httpx.NoContent(w)
		return nil
	case errors.Is(err, auth.ErrWrongPassword):
		return apierr.Validation("Kata sandi lama tidak cocok.",
			map[string]string{"password_lama": "kata sandi lama salah"})
	case errors.Is(err, auth.ErrPasswordTooWeak):
		return apierr.Validation("Kata sandi baru belum memenuhi syarat.",
			map[string]string{"password_baru": err.Error()})
	default:
		return mapDomainErr(err)
	}
}

// passwordResetTTL membaca pengaturan `reset_menit` (PRD §4.9, default 60).
// Nilai yang tidak masuk akal jatuh ke default supaya tautan tidak pernah
// dibuat dengan masa berlaku nol atau negatif.
func (a *API) passwordResetTTL(r *http.Request) time.Duration {
	if a.d.Settings != nil {
		if raw, err := a.d.Settings.Get(r.Context(), "reset_menit"); err == nil {
			var menit int
			if json.Unmarshal(raw, &menit) == nil && menit > 0 {
				return time.Duration(menit) * time.Minute
			}
		}
	}
	return auth.DefaultResetTTL
}

const rateLimitedLoginMsg = "Terlalu banyak percobaan. Coba lagi beberapa saat lagi."
