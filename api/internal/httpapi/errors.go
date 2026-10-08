package httpapi

import (
	"errors"
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// httpErr menuliskan error apa pun lewat bentuk seragam (dipakai middleware
// dan router yang tidak punya akses ke httpx.Fail).
func httpErr(w http.ResponseWriter, r *http.Request, err error) {
	httpx.Fail(w, r, err)
}

func notFoundErr() error {
	return apierr.NotFound("Endpoint tidak ditemukan.")
}

func methodNotAllowedErr() error {
	return apierr.New(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Metode HTTP tidak didukung untuk endpoint ini.")
}

// mapDomainErr menerjemahkan sentinel error domain ke bentuk HTTP.
// Semua sentinel dipetakan di satu tempat supaya kode status konsisten.
func mapDomainErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrInvalidCredentials):
		return apierr.Unauthenticated("Email atau kata sandi salah.")
	case errors.Is(err, auth.ErrInactiveAccount):
		return apierr.Forbidden("Akun Anda sedang tidak aktif. Hubungi admin Pusat.")
	case errors.Is(err, auth.ErrSessionInvalid):
		return apierr.Unauthenticated("Sesi Anda sudah berakhir. Silakan masuk kembali.")
	case errors.Is(err, settings.ErrNotFound), errors.Is(err, catalog.ErrNotFound):
		return apierr.NotFound("Data yang diminta tidak ditemukan.")
	case errors.Is(err, settings.ErrForbidden), errors.Is(err, catalog.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke tindakan ini.")
	case errors.Is(err, catalog.ErrNoBranch):
		return apierr.New(http.StatusConflict, "BRANCH_MISSING", "Akun ini belum terhubung ke cabang mana pun.")
	default:
		return err
	}
}
