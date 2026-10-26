package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/receivable"
)

func tempoErr(err error) error {
	switch {
	case errors.Is(err, receivable.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke tagihan Tempo.")
	case errors.Is(err, receivable.ErrNotFound):
		return apierr.NotFound("Tagihan atau pesanan Tempo tidak ditemukan.")
	case errors.Is(err, receivable.ErrInvalid):
		return apierr.Validation("Data pelunasan Tempo tidak valid.", map[string]string{"tempo": err.Error()})
	case errors.Is(err, receivable.ErrSuperadmin):
		return apierr.Conflict("TEMPO_CREDIT_SUPERADMIN", "Kelebihan bayar hanya dapat diputuskan Superadmin sebagai saldo Tempo.")
	case errors.Is(err, receivable.ErrNoBankAccount):
		return apierr.Conflict("BRANCH_BANK_ACCOUNT_UNAVAILABLE", "Cabang Tempo belum memiliki rekening tujuan aktif. Atur rekening sebelum menyetujui pesanan.")
	case errors.Is(err, receivable.ErrDuplicateProof):
		return apierr.Conflict("TEMPO_PROOF_DUPLICATE", "Bukti pelunasan ini sudah pernah diunggah. Periksa riwayat pelunasan.")
	case errors.Is(err, receivable.ErrConflict):
		return apierr.Conflict("TEMPO_CONFLICT", "Status tagihan berubah. Muat ulang lalu coba lagi.")
	default:
		return mapDomainErr(err)
	}
}

func (a *API) adminApproveTempo(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	due, err := a.d.Receivables.Approve(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(map[string]string{"tempo_due_date": due}))
	return nil
}
func (a *API) tempoRepayments(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	rows, err := a.d.Receivables.List(r.Context(), auth.ActorFrom(r.Context()), false)
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(rows))
	return nil
}
func (a *API) adminTempoRepayments(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	rows, err := a.d.Receivables.List(r.Context(), auth.ActorFrom(r.Context()), r.URL.Query().Get("pending") == "true")
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(rows))
	return nil
}
func (a *API) tempoUploadRepayment(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	if a.d.ProofLimiter != nil && !a.d.ProofLimiter.Allow("tempo:"+auth.ActorFrom(r.Context()).UserID) {
		return apierr.RateLimited("Terlalu banyak unggahan bukti.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 11<<20)
	if err := r.ParseMultipartForm(11 << 20); err != nil {
		return apierr.Validation("Berkas terlalu besar atau form tidak valid.", map[string]string{"file": err.Error()})
	}
	amount, err := strconv.ParseInt(r.FormValue("amount"), 10, 64)
	if err != nil {
		return apierr.Validation("Nominal tidak valid.", map[string]string{"amount": "masukkan Rupiah bulat"})
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return apierr.Validation("Bukti pelunasan wajib diunggah.", map[string]string{"file": "wajib"})
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (10<<20)+1))
	if err != nil {
		return err
	}
	out, err := a.d.Receivables.UploadProof(r.Context(), auth.ActorFrom(r.Context()), a.d.Storage, receivable.UploadInput{
		Amount: amount, BankAccountID: r.FormValue("bank_account_id"), Filename: header.Filename, Body: body})
	if err != nil {
		return tempoErr(err)
	}
	httpx.Created(w, data(out))
	return nil
}
func (a *API) adminTempoDecision(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	var body struct {
		Decision string `json:"decision"`
		Amount   int64  `json:"amount"`
		Reason   string `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	out, err := a.d.Receivables.Decide(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"), body.Decision, body.Amount, body.Reason)
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

func (a *API) tempoStatements(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	rows, err := a.d.Receivables.Statements(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(rows))
	return nil
}

func (a *API) tempoOverview(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	out, err := a.d.Receivables.Overview(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}

func (a *API) adminTempoExport(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	content, err := a.d.Receivables.ExportXLSX(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return tempoErr(err)
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="piutang-tempo.xlsx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
	return nil
}
func (a *API) tempoStatementDownload(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	url, err := a.d.Receivables.StatementURL(r.Context(), auth.ActorFrom(r.Context()), a.d.Storage, chi.URLParam(r, "id"))
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(map[string]any{"url": url, "expires_in": 600}))
	return nil
}
func (a *API) tempoRepaymentProof(w http.ResponseWriter, r *http.Request) error {
	if a.d.Receivables == nil {
		return apierr.New(http.StatusServiceUnavailable, "TEMPO_UNAVAILABLE", "Layanan Tempo belum tersedia.")
	}
	url, err := a.d.Receivables.ProofURL(r.Context(), auth.ActorFrom(r.Context()), a.d.Storage, chi.URLParam(r, "id"))
	if err != nil {
		return tempoErr(err)
	}
	httpx.OK(w, data(map[string]any{"url": url, "expires_in": 600}))
	return nil
}
