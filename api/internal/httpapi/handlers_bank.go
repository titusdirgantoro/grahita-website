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

type replaceBranchBankAccountsRequest struct {
	ExpectedVersion int64    `json:"expected_version" validate:"min=0"`
	BankAccountIDs  []string `json:"bank_account_ids" validate:"dive,uuid"`
}

// bankAccountsPublic mengembalikan HANYA rekening AKTIF untuk instruksi
// pembayaran Customer Portal dan referensi pencocokan rekening tujuan pada
// verifikasi OCR (PRD §4.6, §5). Rekening nonaktif tidak pernah muncul.
func (a *API) bankAccountsPublic(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening bank belum tersedia.")
	}
	set, err := a.d.Bank.EffectiveForActor(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	available := len(set.Accounts) > 0
	reason := ""
	if !available {
		reason = "NO_ASSIGNED_BANK_ACCOUNT"
	}
	items := make([]map[string]any, 0, len(set.Accounts))
	for _, account := range set.Accounts {
		items = append(items, map[string]any{
			"id": account.ID, "bank": account.Bank, "nama_pemilik": account.NamaPemilik,
			"nomor": account.Nomor, "display_nomor": account.DisplayNomor(), "is_active": account.IsActive,
		})
	}
	httpx.OK(w, map[string]any{"data": items, "meta": map[string]any{
		"payment_available": available, "allowlist_version": set.Version, "reason": reason,
	}})
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

func (a *API) adminBranchBankAccounts(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening bank belum tersedia.")
	}
	out, err := a.d.Bank.ListAssignments(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

func (a *API) adminBranchBankAccountsReplace(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening bank belum tersedia.")
	}
	var req replaceBranchBankAccountsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := a.d.Bank.ReplaceAssignments(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"), bank.ReplaceInput{
		ExpectedVersion: req.ExpectedVersion, BankAccountIDs: req.BankAccountIDs,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, map[string]any{"data": out, "meta": map[string]any{"changed": out.Changed}})
	return nil
}

// adminOrderBankAccounts mengembalikan rekening efektif cabang pesanan untuk
// selector verifikasi manual. Nomor selalu masked; Manager boleh membaca lewat
// admin.verify tetapi tidak memperoleh endpoint mutasi assignment.
func (a *API) adminOrderBankAccounts(w http.ResponseWriter, r *http.Request) error {
	if a.d.Bank == nil || a.d.AdminOrders == nil || a.d.Settings == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan rekening pesanan belum tersedia.")
	}
	actor := auth.ActorFrom(r.Context())
	order, err := a.d.AdminOrders.Get(r.Context(), actor, chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	set, err := a.d.Bank.EffectiveForBranch(r.Context(), order.BranchID)
	if err != nil {
		return mapDomainErr(err)
	}
	ai, err := a.d.Settings.AISettings(r.Context())
	if err != nil {
		return mapDomainErr(err)
	}
	items := make([]map[string]any, 0, len(set.Accounts))
	for _, account := range set.Accounts {
		items = append(items, map[string]any{
			"id":            account.ID,
			"bank":          account.Bank,
			"nama_pemilik":  account.NamaPemilik,
			"display_nomor": account.DisplayNomor(),
		})
	}
	httpx.OK(w, data(map[string]any{
		"accounts":          items,
		"allowlist_version": set.Version,
		"ai_active":         ai.Aktif,
	}))
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
