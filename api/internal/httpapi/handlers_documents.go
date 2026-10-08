package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// documentsUnavailable adalah jawaban bila layanan dokumen belum terpasang.
func documentsUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan dokumen pesanan belum tersedia.")
}

// orderDocuments mengembalikan dokumen satu pesanan (F1-17, PRD §6.1).
//
// Portal cabang memakai endpoint ini untuk daftar INVOICE-nya sendiri; portal
// admin memakai jalur yang sama untuk daftar lengkap dokumen cabang mana pun.
// Pembatasan jenis dokumen per peran ada di layer service (bukan di UI) supaya
// tidak bisa dilewati dengan memanggil API langsung.
func (a *API) orderDocuments(w http.ResponseWriter, r *http.Request) error {
	if a.d.Documents == nil {
		return documentsUnavailable()
	}
	list, err := a.d.Documents.List(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// documentDownload mengembalikan tautan unduh berumur pendek untuk satu
// dokumen (`/api/v1/documents/{id}/download`), sesuai SystemDesign.md §8:
// akses dokumen SELALU lewat presigned URL, kunci storage tidak pernah dikirim.
//
// Otorisasi ada di layer service: akun cabang hanya boleh mengunduh invoice
// pesanan cabang sesinya, staf Pusat (CapAdminOrders) boleh semuanya.
func (a *API) documentDownload(w http.ResponseWriter, r *http.Request) error {
	if a.d.Documents == nil {
		return documentsUnavailable()
	}
	dl, err := a.d.Documents.DownloadURL(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(dl))
	return nil
}

// adminOrderDocuments memastikan ketiga dokumen versi berjalan (invoice PDF,
// surat jalan XLSX, surat jalan PDF) tersedia untuk satu pesanan yang sudah
// Done (SystemDesign.md §4.4).
//
// Sifatnya IDEMPOTEN: memanggil ulang tidak menghasilkan dokumen ganda dan
// tidak menimpa berkas versi yang sudah ada. Endpoint ini dipakai portal admin
// untuk membuat/menyegarkan dokumen (mis. setelah worker render gagal karena
// mesin PDF sempat tidak siap); transisi Done sendiri memanggil service yang
// sama dari domain verifikasi (F1-18).
func (a *API) adminOrderDocuments(w http.ResponseWriter, r *http.Request) error {
	if a.d.Documents == nil {
		return documentsUnavailable()
	}
	list, err := a.d.Documents.Generate(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}
