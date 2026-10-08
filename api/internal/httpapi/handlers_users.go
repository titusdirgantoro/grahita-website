package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/users"
)

// defaultInvitationDays adalah masa berlaku tautan undangan/aktivasi admin
// bila pengaturan `token_hari` tidak tersedia (PRD §4.9: default 14 hari).
const defaultInvitationDays = 14

// updateBranchUserRequest adalah body PUT /api/v1/admin/users/branch/{id}.
type updateBranchUserRequest struct {
	IsActive *bool `json:"is_active" validate:"required"`
}

// inviteAdminRequest adalah body POST /api/v1/admin/users/admin.
type inviteAdminRequest struct {
	Nama            string `json:"nama" validate:"required,max=120"`
	Email           string `json:"email" validate:"required,email,max=254"`
	Role            string `json:"role" validate:"required,oneof=manager superadmin"`
	NotifInvoice    bool   `json:"notif_invoice"`
	NotifSuratJalan bool   `json:"notif_surat_jalan"`
}

// updateAdminRequest adalah body PUT /api/v1/admin/users/admin/{id}.
// Semua field opsional: yang dikirim saja yang diubah.
type updateAdminRequest struct {
	Nama            *string `json:"nama" validate:"omitempty,min=1,max=120"`
	Role            *string `json:"role" validate:"omitempty,oneof=manager superadmin"`
	NotifInvoice    *bool   `json:"notif_invoice"`
	NotifSuratJalan *bool   `json:"notif_surat_jalan"`
	IsActive        *bool   `json:"is_active"`
}

// adminBranchUsers mengembalikan daftar akun cabang (PRD §4.8 tab User cabang).
func (a *API) adminBranchUsers(w http.ResponseWriter, r *http.Request) error {
	if a.d.Users == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengguna belum tersedia.")
	}
	list, err := a.d.Users.BranchUsers(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// adminBranchUserUpdate mengaktifkan atau menonaktifkan akun cabang.
func (a *API) adminBranchUserUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Users == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengguna belum tersedia.")
	}
	var req updateBranchUserRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	u, err := a.d.Users.SetBranchUserActive(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), *req.IsActive)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(u))
	return nil
}

// adminBranchUserReset mengirim ulang tautan atur ulang kata sandi akun cabang.
func (a *API) adminBranchUserReset(w http.ResponseWriter, r *http.Request) error {
	if a.d.Users == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengguna belum tersedia.")
	}
	err := a.d.Users.ResendBranchReset(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), a.passwordResetTTL(r))
	if errors.Is(err, auth.ErrInactiveAccount) {
		// Akun/cabang nonaktif memang tidak boleh memegang tautan: pesan
		// khusus supaya admin tahu harus mengaktifkan akunnya lebih dulu.
		return apierr.Conflict("ACCOUNT_INACTIVE",
			"Akun cabang ini sedang nonaktif; aktifkan dulu sebelum mengirim tautan atur ulang.")
	}
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(map[string]any{
		"pesan": "Tautan atur ulang kata sandi sudah dikirim ke email cabang.",
	}))
	return nil
}

// adminUsers mengembalikan daftar akun admin (Superadmin saja).
func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) error {
	if a.d.Users == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengguna belum tersedia.")
	}
	list, err := a.d.Users.Admins(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// adminUserCreate mengundang admin baru. Email undangan diantre lewat outbox
// (F1-16); tautan aslinya tidak pernah dikembalikan API.
func (a *API) adminUserCreate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Users == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengguna belum tersedia.")
	}
	var req inviteAdminRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	admin, err := a.d.Users.InviteAdmin(r.Context(), auth.ActorFrom(r.Context()), users.NewAdmin{
		Nama:            req.Nama,
		Email:           req.Email,
		Role:            auth.Role(req.Role),
		NotifInvoice:    req.NotifInvoice,
		NotifSuratJalan: req.NotifSuratJalan,
	}, a.invitationTTL(r))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(admin))
	return nil
}

// adminUserResendInvite membuat ulang tautan aktivasi admin undangan dan
// mengantre ulang email undangannya (PRD §4.8, F1-16). Ini jalan pemulihan bila
// email undangan pertama gagal terkirim. Tautan aslinya tidak pernah
// dikembalikan API.
func (a *API) adminUserResendInvite(w http.ResponseWriter, r *http.Request) error {
	if a.d.Users == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengguna belum tersedia.")
	}
	admin, err := a.d.Users.ResendInviteAdmin(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), a.invitationTTL(r))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(admin))
	return nil
}

// adminUserUpdate mengubah nama, role, flag notifikasi, dan status aktif admin.
func (a *API) adminUserUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Users == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengguna belum tersedia.")
	}
	var req updateAdminRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	patch := users.AdminPatch{
		Nama:            req.Nama,
		NotifInvoice:    req.NotifInvoice,
		NotifSuratJalan: req.NotifSuratJalan,
		IsActive:        req.IsActive,
	}
	if req.Role != nil {
		role := auth.Role(*req.Role)
		patch.Role = &role
	}
	admin, err := a.d.Users.UpdateAdmin(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), patch)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(admin))
	return nil
}

// invitationTTL membaca pengaturan `token_hari` (PRD §4.9, default 14 hari) —
// masa berlaku tautan undangan/aktivasi admin.
func (a *API) invitationTTL(r *http.Request) time.Duration {
	if a.d.Settings != nil {
		if raw, err := a.d.Settings.Get(r.Context(), "token_hari"); err == nil {
			var hari int
			if json.Unmarshal(raw, &hari) == nil && hari > 0 {
				return time.Duration(hari) * 24 * time.Hour
			}
		}
	}
	return defaultInvitationDays * 24 * time.Hour
}
