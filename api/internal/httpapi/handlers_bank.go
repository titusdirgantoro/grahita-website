package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// createBankAccountRequest adalah body POST /api/v1/admin/bank-accounts
// (PRD §4.6). Field: bank (BCA/BRI), nama_pemilik, nomor, is_active.
type createBankAccountRequest struct {
	Bank        string `json:"bank" validate:"required,oneof=BCA BRI"`
	NamaPemilik string `json:"nama_pemilik" validate:"required,min=2,max=120"`
	Nomor       string `json:"nomor" validate:"required,min=6,max=34"`
	// IsActive kosong berarti rekening baru aktif.
	IsActive *bool `json:"is_active"`
}

// updateBankAccountRequest adalah body PUT /api/v1/admin/bank-accounts/{id}.
// Semua field opsional: yang dikirim saja yang diubah.
type updateBankAccountRequest struct {
	Bank        *string `json:"bank" validate:"omitempty,oneof=BCA BRI"`
	NamaPemilik *string `json:"nama_pemilik" validate:"omitempty,min=2,max=120"`
	Nomor       *string `json:"nomor" validate:"omitempty,min=6,max=34"`
	IsActive    *bool   `json:"is_active"`
}

// bankAccountsPublic mengembalikan HANYA rekening AKTIF untuk instruksi
// pembayaran Customer Portal dan referensi pencocokan rekening tujuan pada
// verifikasi OCR (PRD §4.6, §5). Rekening nonaktif tidak pernah muncul.
func (a *API) bankAccountsPublic(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening bank belum tersedia.")
	}
	list, err := a.d.Bank.Active(r.Context())
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// adminBankAccounts adalah pengelolaan rekening: seluruh rekening (aktif maupun
// nonaktif) untuk Superadmin (PRD §2.1).
func (a *API) adminBankAccounts(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening bank belum tersedia.")
	}
	list, err := a.d.Bank.List(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// adminBankAccountCreate menambahkan rekening baru (Superadmin).
func (a *API) adminBankAccountCreate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening bank belum tersedia.")
	}
	var req createBankAccountRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	saved, err := a.d.Bank.Create(r.Context(), auth.ActorFrom(r.Context()), bank.NewAccount{
		Bank:        req.Bank,
		NamaPemilik: req.NamaPemilik,
		Nomor:       req.Nomor,
		IsActive:    req.IsActive,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(saved))
	return nil
}

// adminBankAccountUpdate mengubah rekening, termasuk mengaktifkan atau
// menonaktifkannya (Superadmin).
func (a *API) adminBankAccountUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening bank belum tersedia.")
	}
	var req updateBankAccountRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	saved, err := a.d.Bank.Update(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), bank.Patch{
			Bank:        req.Bank,
			NamaPemilik: req.NamaPemilik,
			Nomor:       req.Nomor,
			IsActive:    req.IsActive,
		})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(saved))
	return nil
}
