package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// emailsUnavailable adalah jawaban bila layanan email belum terpasang.
func emailsUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan email belum tersedia.")
}

// adminOrderEmails mengembalikan riwayat email satu pesanan beserta statusnya
// (PRD EM-02): antre, terkirim, atau gagal — termasuk pesan kegagalan terakhir
// supaya chip "email gagal" bisa menjelaskan sebabnya.
func (a *API) adminOrderEmails(w http.ResponseWriter, r *http.Request) error {
	if a.d.Emails == nil {
		return emailsUnavailable()
	}
	list, err := a.d.Emails.ListByOrder(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// adminOrderEmailResend mengirim ulang satu email dokumen pesanan
// (POST /api/v1/admin/orders/{id}/emails/{kind}/resend, SystemDesign.md §6).
//
// kind adalah jenis email dokumen (invoice_cabang, invoice_admin,
// surat_jalan_admin). Penerima dan lampiran dihitung ulang di service; admin
// nonaktif/undangan tidak pernah ikut, dan aksi ini tercatat di audit_logs.
func (a *API) adminOrderEmailResend(w http.ResponseWriter, r *http.Request) error {
	if a.d.Emails == nil {
		return emailsUnavailable()
	}
	email, err := a.d.Emails.ResendOrder(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), chi.URLParam(r, "kind"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(email))
	return nil
}
