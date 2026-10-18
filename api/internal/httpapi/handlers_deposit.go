package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/deposit"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// Deposit cabang C2 (D3 §6): saldo, journal, top-up pending, keputusan admin,
// alokasi ke order Submitted, dan reversal admin. Semua aturan otorisasi ada
// di deposit.Service; handler hanya menerjemahkan HTTP.

const headerIdempotency = "Idempotency-Key"

func depositUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, "DEPOSIT_UNAVAILABLE", "Layanan deposit belum tersedia.")
}

// depositErrs memetakan error domain deposit ke kontrak error D3 §6.3.
var depositErrs = []struct {
	err     error
	status  int
	code    string
	message string
}{
	{deposit.ErrForbidden, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke tindakan ini."},
	{deposit.ErrCreditActorInvalid, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki akses ke tindakan ini."},
	{deposit.ErrNoBranch, http.StatusForbidden, "FORBIDDEN", "Akun Anda belum terhubung ke cabang."},
	{deposit.ErrTopupNotFound, http.StatusNotFound, "TOPUP_NOT_FOUND", "Top-up deposit tidak ditemukan."},
	{deposit.ErrAllocationNotFound, http.StatusNotFound, "ALLOCATION_NOT_FOUND", "Alokasi deposit tidak ditemukan."},
	{deposit.ErrBranchMismatch, http.StatusNotFound, "NOT_FOUND", "Data tidak ditemukan."},
	{deposit.ErrInsufficientBalance, http.StatusConflict, "DEPOSIT_BALANCE_INSUFFICIENT", "Saldo deposit tidak cukup."},
	{deposit.ErrVersionConflict, http.StatusConflict, "DEPOSIT_VERSION_CONFLICT", "Saldo atau pesanan sudah berubah. Muat ulang lalu coba lagi."},
	{deposit.ErrExceedsOrder, http.StatusConflict, "DEPOSIT_ALLOCATION_EXCEEDS_ORDER", "Alokasi deposit melebihi total pesanan."},
	{deposit.ErrTopupDecided, http.StatusConflict, "TOPUP_ALREADY_DECIDED", "Top-up ini sudah diputuskan."},
	{deposit.ErrProofUsed, http.StatusConflict, "PAYMENT_PROOF_ALREADY_USED", "Bukti ini sudah dipakai untuk top-up lain."},
	{deposit.ErrIdempotencyReused, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key sudah dipakai untuk permintaan lain."},
	{deposit.ErrReversalExceeds, http.StatusConflict, "REVERSAL_EXCEEDS_AVAILABLE", "Nominal reversal melebihi alokasi yang masih aktif."},
	{deposit.ErrNegativeBalance, http.StatusConflict, "REVERSAL_WOULD_NEGATIVE_BALANCE", "Koreksi akan membuat saldo deposit negatif."},
	{deposit.ErrBankUnavailable, http.StatusConflict, "BRANCH_BANK_ACCOUNT_UNAVAILABLE", "Cabang belum memiliki rekening tujuan aktif."},
	{deposit.ErrBankNotAllowed, http.StatusConflict, "BANK_ACCOUNT_NOT_ALLOWED", "Rekening tujuan tidak diizinkan untuk cabang ini."},
	{deposit.ErrCreditSourceInvalid, http.StatusConflict, "BANK_ACCOUNT_NOT_ALLOWED", "Bukti atau rekening tujuan tidak lagi sah untuk kredit."},
	{deposit.ErrOrderDone, http.StatusConflict, "ORDER_ALREADY_DONE", "Pesanan sudah selesai."},
	{deposit.ErrOrderNotSubmitted, http.StatusConflict, "ORDER_NOT_SUBMITTED", "Pesanan belum dikirim."},
	{deposit.ErrReconciliation, http.StatusServiceUnavailable, "DEPOSIT_RECONCILIATION_REQUIRED", "Saldo deposit perlu direkonsiliasi. Hubungi admin Pusat."},
	{deposit.ErrNotFound, http.StatusNotFound, "NOT_FOUND", "Data tidak ditemukan."},
	{deposit.ErrTopupCNCOnly, http.StatusForbidden, "DEPOSIT_CNC_ONLY", "Top-up deposit hanya untuk cabang Cash & Carry."},
	{order.ErrDepositChanged, http.StatusConflict, "DEPOSIT_CHANGED", "Saldo deposit berubah sejak ditampilkan. Periksa lagi rincian pembayaran lalu kirim ulang."},
	// Settlement campuran dan revisi berdeposit (C3; D3 §6.3, C0 §5.3).
	{payment.ErrBreakdownMismatch, http.StatusUnprocessableEntity, "PAYMENT_BREAKDOWN_MISMATCH", "Rincian deposit dan transfer tidak tepat sama dengan total pesanan."},
	{payment.ErrOverpayDecisionRequired, http.StatusConflict, "OVERPAY_DECISION_REQUIRED", "Ada lebih bayar yang harus diputuskan sebagai kredit deposit oleh Superadmin."},
	{payment.ErrOrderVersionConflict, http.StatusConflict, "ORDER_VERSION_CONFLICT", "Pesanan sudah berubah. Muat ulang lalu coba lagi."},
	{payment.ErrDepositCreditForbidden, http.StatusForbidden, "FORBIDDEN", "Hanya Superadmin yang boleh mengkreditkan lebih bayar ke deposit."},
	{payment.ErrIdempotencyReused, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key sudah dipakai untuk keputusan lain."},
	{order.ErrReversalPlanRequired, http.StatusConflict, "REVISION_REVERSAL_PLAN_REQUIRED", "Total revisi lebih kecil dari deposit terpakai; sertakan rencana reversal deposit yang cukup."},
	{order.ErrReversalPlanInvalid, http.StatusConflict, "REVISION_REVERSAL_PLAN_INVALID", "Rencana reversal deposit revisi tidak valid."},
}

var depositValidation = []struct {
	err   error
	field string
	pesan string
}{
	{deposit.ErrIdempotencyRequired, "idempotency_key", "header Idempotency-Key wajib"},
	{deposit.ErrReasonRequired, "reason", "wajib diisi"},
	{deposit.ErrInvalidAmount, "amount", "harus lebih dari nol"},
	{deposit.ErrInvalidAction, "action", "harus review, reject, atau verify"},
	{deposit.ErrProofRequired, "proof", "wajib diunggah"},
	{payment.ErrSettlementKeyRequired, "idempotency_key", "header Idempotency-Key wajib untuk kredit deposit"},
	{order.ErrRevisionKeyRequired, "idempotency_key", "header Idempotency-Key wajib untuk reversal plan"},
}

// depositErr menerjemahkan error domain deposit; ok=false bila bukan miliknya.
func depositErr(err error) (error, bool) {
	for _, v := range depositValidation {
		if errors.Is(err, v.err) {
			return apierr.Validation("Permintaan deposit tidak valid.", map[string]string{v.field: v.pesan}), true
		}
	}
	for _, e := range depositErrs {
		if errors.Is(err, e.err) {
			return apierr.New(e.status, e.code, e.message), true
		}
	}
	return nil, false
}

func idempotencyKey(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(headerIdempotency))
}

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func pageMeta(total, limit, offset int) map[string]any {
	return map[string]any{"total": total, "limit": limit, "offset": offset}
}

// GET /deposit
func (a *API) depositSummary(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	out, err := a.d.Deposits.Summary(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

// GET /deposit/transactions
func (a *API) depositTransactions(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	limit, offset := queryInt(r, "limit"), queryInt(r, "offset")
	items, total, err := a.d.Deposits.Transactions(r.Context(), auth.ActorFrom(r.Context()), limit, offset)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, map[string]any{"data": items, "meta": pageMeta(total, limit, offset)})
	return nil
}

// GET /deposit/topups
func (a *API) depositTopups(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	limit, offset := queryInt(r, "limit"), queryInt(r, "offset")
	items, total, err := a.d.Deposits.MyTopups(r.Context(), auth.ActorFrom(r.Context()), limit, offset)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, map[string]any{"data": items, "meta": pageMeta(total, limit, offset)})
	return nil
}

// POST /deposit/topups — multipart: proof, declared_amount, bank_account_id.
func (a *API) depositTopupCreate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	actor := auth.ActorFrom(r.Context())
	if a.d.ProofLimiter != nil && !a.d.ProofLimiter.Allow(string(actor.Kind)+":"+actor.UserID) {
		return apierr.RateLimited("Terlalu banyak unggahan bukti. Coba lagi beberapa saat lagi.")
	}
	batas := int64(defaultMaxRequestBytes)
	if a.d.Payments != nil {
		var err error
		if batas, err = a.d.Payments.MaxRequestBytes(r.Context()); err != nil {
			return mapDomainErr(err)
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, batas)
	if err := r.ParseMultipartForm(memoryBerkas); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apierr.New(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Ukuran bukti melebihi batas unggahan.")
		}
		return apierr.Validation("Permintaan top-up tidak valid.", map[string]string{"proof": "kirim bukti pada field proof"})
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	amount, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("declared_amount")), 10, 64)
	if err != nil {
		return apierr.Validation("Permintaan top-up tidak valid.", map[string]string{"declared_amount": "harus bilangan bulat rupiah"})
	}
	req := deposit.TopupRequest{BankAccountID: r.FormValue("bank_account_id"), DeclaredAmount: amount,
		IdempotencyKey: idempotencyKey(r)}
	if f, h, err := r.FormFile("proof"); err == nil {
		defer func() { _ = f.Close() }()
		req.NamaFile, req.Content, req.Size = h.Filename, f, h.Size
	}
	out, replayed, err := a.d.Deposits.CreateTopup(r.Context(), actor, req)
	if err != nil {
		return mapDomainErr(err)
	}
	if replayed {
		httpx.OK(w, data(out))
		return nil
	}
	httpx.Created(w, data(out))
	return nil
}

// GET /deposit/topups/{id}/proof dan GET /admin/deposit-topups/{id}/proof
func (a *API) depositTopupProof(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	out, err := a.d.Deposits.TopupProofURL(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

// GET /orders/{id}/deposit-allocations dan versi admin.
func (a *API) depositOrderAllocations(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	out, err := a.d.Deposits.OrderAllocations(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

// GET /orders/{id}/payment dan versi admin: rincian pembayaran order.
func (a *API) depositOrderPayment(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	out, err := a.d.Deposits.OrderPayment(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

type depositAllocateRequest struct {
	Amount                 int64  `json:"amount"`
	ExpectedJournalVersion int64  `json:"expected_journal_version"`
	ExpectedOrderVersion   int32  `json:"expected_order_version"`
	Reason                 string `json:"reason"`
}

// POST /orders/{id}/deposit-allocations dan versi admin.
func (a *API) depositAllocate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	var req depositAllocateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := a.d.Deposits.Allocate(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"),
		deposit.AllocationRequest{Amount: req.Amount, ExpectedJournalVersion: req.ExpectedJournalVersion,
			ExpectedOrderVersion: req.ExpectedOrderVersion, IdempotencyKey: idempotencyKey(r), Reason: req.Reason})
	if err != nil {
		return mapDomainErr(err)
	}
	if out.Replayed {
		httpx.OK(w, data(out))
		return nil
	}
	httpx.Created(w, data(out))
	return nil
}

type depositReverseRequest struct {
	Amount                 int64  `json:"amount"`
	ExpectedJournalVersion int64  `json:"expected_journal_version"`
	Reason                 string `json:"reason"`
}

// POST /admin/orders/{id}/deposit-allocations/{allocation_id}/reverse
func (a *API) depositReverse(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	var req depositReverseRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := a.d.Deposits.Reverse(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"),
		chi.URLParam(r, "allocation_id"), deposit.ReversalRequest{Amount: req.Amount,
			ExpectedJournalVersion: req.ExpectedJournalVersion, Reason: req.Reason, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

// GET /admin/deposits
func (a *API) adminDeposits(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	out, err := a.d.Deposits.AdminOverview(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

// GET /admin/deposits/{branch_id}
func (a *API) adminDepositBranch(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	out, err := a.d.Deposits.AdminBranch(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "branch_id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

// GET /admin/deposit-topups
func (a *API) adminDepositTopups(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	q := r.URL.Query()
	f := deposit.TopupFilter{BranchID: q.Get("branch_id"), Status: q.Get("status"),
		Limit: queryInt(r, "limit"), Offset: queryInt(r, "offset")}
	items, total, err := a.d.Deposits.AdminTopups(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, map[string]any{"data": items, "meta": pageMeta(total, f.Limit, f.Offset)})
	return nil
}

type depositDecisionRequest struct {
	Action         string `json:"action"`
	ApprovedAmount int64  `json:"approved_amount"`
	BankAccountID  string `json:"bank_account_id"`
	Reason         string `json:"reason"`
}

// POST /admin/deposit-topups/{id}/decision
func (a *API) adminDepositDecision(w http.ResponseWriter, r *http.Request) error {
	if a.d.Deposits == nil {
		return depositUnavailable()
	}
	var req depositDecisionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := a.d.Deposits.DecideTopup(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"),
		deposit.DecisionRequest{Action: req.Action, ApprovedAmount: req.ApprovedAmount,
			BankAccountID: req.BankAccountID, Reason: req.Reason, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}
