package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// ordersUnavailable adalah jawaban bila layanan pesanan belum terpasang.
func ordersUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pesanan belum tersedia.")
}

// draftLineRequest adalah satu baris yang dikirim editor pesanan.
//
// Hanya product_id dan qty yang diterima: harga satuan, subtotal, dan total
// dihitung server. Field lain apa pun (mis. "harga" atau "total") ditolak
// dekoder JSON, jadi angka dari klien tidak pernah sampai ke domain.
type draftLineRequest struct {
	ProductID string `json:"product_id" validate:"required"`
	Qty       int    `json:"qty" validate:"required,gt=0"`
}

// draftRequest adalah body POST /api/v1/orders dan PUT /api/v1/orders/{id}.
type draftRequest struct {
	Catatan string             `json:"catatan" validate:"omitempty,max=500"`
	Lines   []draftLineRequest `json:"lines" validate:"required,min=1,max=100,dive"`
}

// draftInput mengubah body menjadi masukan domain.
func (r draftRequest) draftInput() order.DraftInput {
	lines := make([]order.LineInput, 0, len(r.Lines))
	for _, l := range r.Lines {
		lines = append(lines, order.LineInput{ProductID: strings.TrimSpace(l.ProductID), Qty: l.Qty})
	}
	return order.DraftInput{Catatan: r.Catatan, Lines: lines}
}

// ordersList mengembalikan daftar pesanan cabang sesi, dapat difilter status
// (PRD CP-ORD-01). Nilai "semua"/kosong berarti tanpa filter.
func (a *API) ordersList(w http.ResponseWriter, r *http.Request) error {
	if a.d.Orders == nil {
		return ordersUnavailable()
	}
	f := order.Filter{Status: strings.TrimSpace(r.URL.Query().Get("status"))}
	list, err := a.d.Orders.List(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// orderCreate membuat draft pesanan baru (PRD CP-ORD-03).
func (a *API) orderCreate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Orders == nil {
		return ordersUnavailable()
	}
	var req draftRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	o, err := a.d.Orders.CreateDraft(r.Context(), auth.ActorFrom(r.Context()), req.draftInput())
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(o))
	return nil
}

// orderGet mengembalikan detail satu pesanan milik cabang sesi.
func (a *API) orderGet(w http.ResponseWriter, r *http.Request) error {
	if a.d.Orders == nil {
		return ordersUnavailable()
	}
	o, err := a.d.Orders.Get(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(o))
	return nil
}

// orderUpdate mengubah draft milik cabang sesi (PRD CP-ORD-03).
func (a *API) orderUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Orders == nil {
		return ordersUnavailable()
	}
	var req draftRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	o, err := a.d.Orders.UpdateDraft(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"), req.draftInput())
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(o))
	return nil
}

// orderDelete menghapus draft milik cabang sesi (PRD CP-ORD-03). Pesanan
// Submitted/Done tidak bisa dihapus cabang (PRD CP-ORD-09).
func (a *API) orderDelete(w http.ResponseWriter, r *http.Request) error {
	if a.d.Orders == nil {
		return ordersUnavailable()
	}
	if err := a.d.Orders.DeleteDraft(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id")); err != nil {
		return mapDomainErr(err)
	}
	httpx.NoContent(w)
	return nil
}
