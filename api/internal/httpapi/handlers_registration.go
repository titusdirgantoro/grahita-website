package httpapi

import (
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/registration"
)

// validateRegistrationTokenRequest adalah body POST
// /api/v1/auth/registration/validate (langkah 1, PRD CP-REG-02).
type validateRegistrationTokenRequest struct {
	Token string `json:"token" validate:"required,max=64"`
}

// registerRequest adalah body POST /api/v1/auth/register (langkah 2).
//
// Hanya field yang memang diisi pengguna: token, nomor telepon, kata sandi,
// dan konfirmasinya (PRD CP-REG-04). Nama cabang, nama kepala cabang, dan
// email TIDAK ada di sini dengan sengaja — ketiganya dibaca server dari data
// cabang, dan DecodeJSON menolak field tak dikenal sehingga data read-only
// itu tidak bisa dipalsukan lewat payload (PRD CP-REG-03).
type registerRequest struct {
	Token              string `json:"token" validate:"required,max=64"`
	Telepon            string `json:"telepon" validate:"required,max=40"`
	Password           string `json:"password" validate:"required,min=8,max=200"`
	PasswordKonfirmasi string `json:"password_konfirmasi" validate:"required,max=200"`
}

// registrationValidate menangani langkah 1: memeriksa token registrasi lalu
// mengembalikan data cabang yang akan tampil read-only di langkah 2.
//
// Endpoint publik: dikecualikan dari CSRF dan dibatasi per IP.
func (a *API) registrationValidate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Registration == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pendaftaran belum tersedia.")
	}
	var req validateRegistrationTokenRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if a.d.RegistrationLimiter != nil && !a.d.RegistrationLimiter.Allow("registration.validate:"+httpx.ClientIP(r)) {
		return apierr.RateLimited(rateLimitedLoginMsg)
	}
	cabang, err := a.d.Registration.Validate(r.Context(), req.Token)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(cabang))
	return nil
}

// registrationRegister menangani langkah 2: memakai token lalu membuat akun
// cabang. Akun yang terbentuk langsung bisa dipakai login dengan email cabang
// dan kata sandi yang baru saja diisi.
//
// Endpoint publik: dikecualikan dari CSRF dan dibatasi per IP.
func (a *API) registrationRegister(w http.ResponseWriter, r *http.Request) error {
	if a.d.Registration == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pendaftaran belum tersedia.")
	}
	var req registerRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if a.d.RegistrationLimiter != nil && !a.d.RegistrationLimiter.Allow("registration.register:"+httpx.ClientIP(r)) {
		return apierr.RateLimited(rateLimitedLoginMsg)
	}
	akun, err := a.d.Registration.Register(r.Context(), registration.RegisterInput{
		Token:              req.Token,
		Telepon:            req.Telepon,
		Password:           req.Password,
		PasswordKonfirmasi: req.PasswordKonfirmasi,
	}, requestMeta(r))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(map[string]any{"user": userDTO{
		ID:       akun.UserID,
		Kind:     string(auth.KindBranch),
		Role:     string(auth.RoleBranch),
		BranchID: akun.BranchID,
		Nama:     akun.Nama,
		Email:    akun.Email,
	}}))
	return nil
}
