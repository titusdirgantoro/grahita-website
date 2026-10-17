package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// reviseUnavailable adalah jawaban bila layanan revisi belum terpasang.
func reviseUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan revisi invoice belum tersedia.")
}

// reviseLineRequest adalah satu baris pada daftar baris FINAL revisi.
type reviseLineRequest struct {
	LineNo      int    `json:"line_no"`
	ProductID   string `json:"product_id"`
	Qty         int    `json:"qty"`
	HargaSatuan int64  `json:"harga_satuan"`
}

// reviseRequest adalah body POST /api/v1/admin/orders/{id}/revise (F2-25).
type reviseRequest struct {
	Alasan  string              `json:"alasan"`
	Catatan string              `json:"catatan"`
	Lines   []reviseLineRequest `json:"lines"`
	// DepositReversals: reversal plan revisi turun (C0 §5.1).
	DepositReversals []order.DepositReversalInput `json:"deposit_reversals"`
}

// adminOrderRevise merevisi invoice satu pesanan Done (PRD §4.3 AP-REV-01..05).
//
// Otorisasi ditegakkan layer service: hanya staf Pusat ber-kapabilitas
// `admin.revise` — dan Manager hanya bila setelan `manager_boleh_revisi`
// sedang aktif (PRD §4.9). Guard rute memakai kapabilitas yang sama supaya
// penjaga transport dan penjaga domain tidak pernah berbeda.
func (a *API) adminOrderRevise(w http.ResponseWriter, r *http.Request) error {
	if a.d.Revise == nil {
		return reviseUnavailable()
	}
	var req reviseRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	lines := make([]order.ReviseLineInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		lines = append(lines, order.ReviseLineInput{
			LineNo:      l.LineNo,
			ProductID:   l.ProductID,
			Qty:         l.Qty,
			HargaSatuan: l.HargaSatuan,
		})
	}
	out, err := a.d.Revise.Revise(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"),
		order.ReviseInput{Alasan: req.Alasan, Catatan: req.Catatan, Lines: lines,
			DepositReversals: req.DepositReversals, IdempotencyKey: r.Header.Get(headerIdempotency)})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}
