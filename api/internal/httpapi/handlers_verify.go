package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// verifyUnavailable adalah jawaban bila layanan verifikasi belum terpasang.
func verifyUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan verifikasi pembayaran belum tersedia.")
}

// verifyRequest adalah body POST /api/v1/admin/orders/{id}/verify
// (PRD §4.2 AP-ORD-07, §5).
//
// `metode` SENGAJA tidak diterima dari klien: endpoint ini adalah verifikasi
// MANUAL, jadi servernya menetapkan `manual`. Jalur AI (Fase 2) memanggil
// service yang sama dengan metode `ai` dari worker OCR, bukan lewat endpoint ini.
type verifyRequest struct {
	// Hasil: "cocok" (setujui → Done) atau "perlu_review" (tandai review).
	Hasil string `json:"hasil"`
	// TotalTerbaca adalah nominal yang dibaca admin dari bukti (atau diisi
	// manual untuk bukti non-OCR). Kosong berarti tidak ada nominal terbaca.
	TotalTerbaca *int64 `json:"total_terbaca"`
	// Alasan wajib bila Hasil = perlu_review: kurang_bayar, lebih_bayar,
	// tidak_terbaca, rekening_beda, lainnya.
	Alasan string `json:"alasan"`
	// Catatan bebas opsional.
	Catatan           string                     `json:"catatan"`
	ProofDestinations []payment.ProofDestination `json:"proof_destinations"`
	// Rincian settlement campuran (C3, D3 §6.2). Kehadiran salah satu field
	// ini memilih jalur settlement; expected_order_version lalu wajib.
	ExpectedOrderVersion *int32              `json:"expected_order_version"`
	DepositAllocationIDs []string            `json:"deposit_allocation_ids"`
	ProofSplits          []proofSplitRequest `json:"proof_splits"`
}

// proofSplitRequest menerima nama D3 (`credited_to_deposit`, attempt pertama)
// maupun nama kumulatif C0 (`credited_to_deposit_total`).
type proofSplitRequest struct {
	PaymentProofID         string `json:"payment_proof_id"`
	BankAccountID          string `json:"bank_account_id"`
	ApprovedNominal        int64  `json:"approved_nominal"`
	AppliedToOrder         int64  `json:"applied_to_order"`
	CreditedToDeposit      *int64 `json:"credited_to_deposit"`
	CreditedToDepositTotal *int64 `json:"credited_to_deposit_total"`
}

// settlementInput menyusun rincian settlement; nil bila jalur transfer saja.
func (req verifyRequest) settlementInput(key string) (*payment.SettlementInput, error) {
	if req.ExpectedOrderVersion == nil && req.DepositAllocationIDs == nil && req.ProofSplits == nil {
		return nil, nil
	}
	if req.ExpectedOrderVersion == nil {
		return nil, apierr.Validation("Rincian settlement tidak valid.", map[string]string{"expected_order_version": "wajib diisi"})
	}
	out := &payment.SettlementInput{ExpectedOrderVersion: *req.ExpectedOrderVersion,
		DepositAllocationIDs: req.DepositAllocationIDs, IdempotencyKey: key}
	for _, sp := range req.ProofSplits {
		var credited int64
		switch {
		case sp.CreditedToDepositTotal != nil && sp.CreditedToDeposit != nil && *sp.CreditedToDepositTotal != *sp.CreditedToDeposit:
			return nil, apierr.Validation("Rincian settlement tidak valid.", map[string]string{"proof_splits": "credited_to_deposit dan credited_to_deposit_total berbeda"})
		case sp.CreditedToDepositTotal != nil:
			credited = *sp.CreditedToDepositTotal
		case sp.CreditedToDeposit != nil:
			credited = *sp.CreditedToDeposit
		}
		out.ProofSplits = append(out.ProofSplits, payment.ProofSplit{PaymentProofID: sp.PaymentProofID,
			BankAccountID: sp.BankAccountID, ApprovedNominal: sp.ApprovedNominal,
			AppliedToOrder: sp.AppliedToOrder, CreditedToDepositTotal: credited})
	}
	return out, nil
}

// adminOrderVerify memverifikasi pembayaran satu pesanan: menyetujui
// (Submitted → Done, lengkap dengan dokumen + email) atau menandainya perlu
// review dengan alasan (pesanan TETAP Submitted).
//
// Otorisasi ditegakkan DUA lapis dengan kapabilitas yang sama: rute dijaga
// requireCap(CapAdminVerify) dan layer service memeriksa `admin.verify`
// (Manager maupun Superadmin, PRD §2.1); akun cabang ditolak, dan id yang
// bukan UUID dijawab 404.
func (a *API) adminOrderVerify(w http.ResponseWriter, r *http.Request) error {
	if a.d.Verify == nil {
		return verifyUnavailable()
	}
	var req verifyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	settlement, err := req.settlementInput(strings.TrimSpace(r.Header.Get(headerIdempotency)))
	if err != nil {
		return err
	}
	out, err := a.d.Verify.MarkVerified(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"), payment.VerifyInput{
		Hasil:             req.Hasil,
		Metode:            payment.MetodeManual,
		TotalTerbaca:      req.TotalTerbaca,
		Alasan:            req.Alasan,
		Catatan:           req.Catatan,
		ProofDestinations: req.ProofDestinations,
		Settlement:        settlement,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

// adminOrderReevaluateDestination hanya menjadwalkan pemakaian ulang hasil OCR
// tersimpan. Provider tidak dipanggil oleh request ini.
func (a *API) adminOrderReevaluateDestination(w http.ResponseWriter, r *http.Request) error {
	if a.d.Reevaluate == nil {
		return verifyUnavailable()
	}
	out, err := a.d.Reevaluate.Enqueue(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.JSON(w, http.StatusAccepted, data(out))
	return nil
}
