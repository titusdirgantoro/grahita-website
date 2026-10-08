package httpapi

import (
	"errors"
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
	"github.com/grahita/portal/api/internal/users"
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
	case errors.Is(err, auth.ErrResetInvalid):
		return apierr.New(http.StatusBadRequest, "RESET_INVALID",
			"Tautan atur ulang tidak valid, sudah dipakai, atau kedaluwarsa. Minta tautan baru.")
	case errors.Is(err, auth.ErrWrongPassword):
		return apierr.Validation("Kata sandi lama tidak cocok.",
			map[string]string{"password_lama": "kata sandi lama salah"})
	case errors.Is(err, auth.ErrPasswordTooWeak):
		return apierr.Validation("Kata sandi belum memenuhi syarat.",
			map[string]string{"password": err.Error()})
	case errors.Is(err, auth.ErrBranchForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke data cabang ini.")
	case errors.Is(err, settings.ErrNotFound), errors.Is(err, catalog.ErrNotFound):
		return apierr.NotFound("Data yang diminta tidak ditemukan.")
	case errors.Is(err, settings.ErrInvalidValue):
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			return apierr.Validation("Nilai pengaturan tidak valid.", ve.Fields)
		}
		return apierr.Validation("Nilai pengaturan tidak valid.", nil)
	case errors.Is(err, settings.ErrForbidden), errors.Is(err, catalog.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke tindakan ini.")
	case errors.Is(err, catalog.ErrNoBranch):
		return apierr.New(http.StatusConflict, "BRANCH_MISSING", "Akun ini belum terhubung ke cabang mana pun.")
	case errors.Is(err, branch.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke master cabang.")
	case errors.Is(err, branch.ErrNotFound):
		return apierr.NotFound("Cabang yang diminta tidak ditemukan.")
	case errors.Is(err, branch.ErrEmailTaken):
		return apierr.Conflict("EMAIL_TAKEN", "Email tersebut sudah dipakai cabang atau akun admin lain.")
	case errors.Is(err, branch.ErrHasOrders):
		return apierr.Conflict("BRANCH_HAS_ORDERS",
			"Cabang ini sudah punya pesanan sehingga tidak bisa dihapus; nonaktifkan saja cabangnya.")
	case errors.Is(err, branch.ErrHasAccount):
		return apierr.Conflict("BRANCH_HAS_ACCOUNT",
			"Cabang ini sudah punya akun, jadi token registrasi tidak diperlukan lagi. Pemulihan akses lewat lupa kata sandi.")
	case errors.Is(err, branch.ErrInactive):
		return apierr.Conflict("BRANCH_INACTIVE",
			"Cabang sedang nonaktif; aktifkan dulu sebelum membuat token registrasi.")
	case errors.Is(err, branch.ErrInvalidData):
		return apierr.Validation("Data cabang tidak valid.", map[string]string{"cabang": err.Error()})
	case errors.Is(err, bank.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke rekening bank.")
	case errors.Is(err, bank.ErrNotFound):
		return apierr.NotFound("Rekening yang diminta tidak ditemukan.")
	case errors.Is(err, bank.ErrDuplicate):
		return apierr.Conflict("BANK_DUPLICATE", "Rekening dengan bank dan nomor tersebut sudah terdaftar.")
	case errors.Is(err, bank.ErrInvalidData):
		return apierr.Validation("Data rekening tidak valid.", map[string]string{"rekening": err.Error()})
	case errors.Is(err, users.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke tindakan ini.")
	case errors.Is(err, users.ErrNotFound):
		return apierr.NotFound("Akun yang diminta tidak ditemukan.")
	case errors.Is(err, users.ErrEmailTaken):
		return apierr.Conflict("EMAIL_TAKEN", "Email tersebut sudah dipakai akun lain.")
	case errors.Is(err, users.ErrLastSuperadmin):
		return apierr.Conflict("LAST_SUPERADMIN",
			"Superadmin aktif terakhir tidak boleh dinonaktifkan atau diturunkan menjadi Manager.")
	case errors.Is(err, users.ErrNoAccount):
		return apierr.Conflict("NO_ACCOUNT",
			"Cabang ini belum punya akun. Buat token registrasi lebih dahulu di halaman Cabang.")
	case errors.Is(err, users.ErrInvalidData):
		return apierr.Validation("Data pengguna tidak valid.", nil)
	default:
		return err
	}
}
