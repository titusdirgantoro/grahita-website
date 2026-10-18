package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// adminOrdersUnavailable adalah jawaban bila layanan riwayat admin belum
// terpasang.
func adminOrdersUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan riwayat pesanan belum tersedia.")
}

// noteRequest adalah body POST /api/v1/admin/orders/{id}/notes (PRD AP-ORD-05).
type noteRequest struct {
	Catatan string `json:"catatan" validate:"max=2000"`
}

// adminOrdersList mengembalikan riwayat pemesanan SELURUH cabang dengan
// pencarian, filter, grouping, dan paginasi server (PRD AP-ORD-01..03).
//
// Seluruh filter ada di query string supaya bisa di-bookmark/di-share
// (SystemDesign §7.2); tidak ada branch_id dari klien yang dipercaya apa pun
// bentuknya — pilihan cabang hanyalah filter daftar.
func (a *API) adminOrdersList(w http.ResponseWriter, r *http.Request) error {
	if a.d.AdminOrders == nil {
		return adminOrdersUnavailable()
	}
	q := r.URL.Query()
	f := order.AdminFilter{
		Cari:        q.Get("cari"),
		BranchID:    q.Get("cabang"),
		Status:      q.Get("status"),
		VerifStatus: q.Get("verif"),
		Dari:        q.Get("dari"),
		Sampai:      q.Get("sampai"),
		Grup:        q.Get("grup"),
	}
	var err error
	if f.IsCNC, err = boolParam(q.Get("cnc"), "cnc"); err != nil {
		return err
	}
	if f.EmailGagal, err = boolParam(q.Get("email_gagal"), "email_gagal"); err != nil {
		return err
	}
	if f.Halaman, err = intParam(q.Get("halaman"), "halaman"); err != nil {
		return err
	}
	if f.PerHalaman, err = intParam(q.Get("per_halaman"), "per_halaman"); err != nil {
		return err
	}

	page, err := a.d.AdminOrders.List(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(page))
	return nil
}

// adminOrderDetail mengembalikan detail lengkap satu pesanan mana pun
// (PRD AP-ORD-04): header + cabang, baris + serial, timeline, catatan internal,
// dokumen, dan log verifikasi.
//
// Bukti transfer dan riwayat email sengaja TIDAK diduplikasi di sini: keduanya
// sudah punya endpoint sendiri (F1-13/F1-16) yang juga dibatasi
// `CapAdminOrders`, dan UI memanggilnya terpisah supaya satu respons tetap
// kecil.
func (a *API) adminOrderDetail(w http.ResponseWriter, r *http.Request) error {
	if a.d.AdminOrders == nil {
		return adminOrdersUnavailable()
	}
	d, err := a.d.AdminOrders.Get(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(d))
	return nil
}

// adminOrderNote menyimpan catatan internal pesanan (PRD AP-ORD-05) dan
// mengembalikan detail terbarunya. Catatan internal tidak pernah muncul di
// respons portal cabang.
func (a *API) adminOrderNote(w http.ResponseWriter, r *http.Request) error {
	if a.d.AdminOrders == nil {
		return adminOrdersUnavailable()
	}
	var req noteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	d, err := a.d.AdminOrders.SetNote(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), req.Catatan)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(d))
	return nil
}

// boolParam membaca parameter boolean opsional. Nilai kosong berarti "tanpa
// filter" (nil), sedangkan nilai yang tidak dikenal DITOLAK — bukan diam-diam
// dianggap false — supaya salah ketik tidak menghilangkan filter tanpa terlihat.
func boolParam(v, nama string) (*bool, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "":
		return nil, nil
	case "true", "1", "ya", "yes":
		b := true
		return &b, nil
	case "false", "0", "tidak", "no":
		b := false
		return &b, nil
	default:
		return nil, apierr.Validation("Filter tidak valid.",
			map[string]string{nama: "harus true atau false"})
	}
}

// intParam membaca parameter bilangan bulat opsional; kosong berarti 0
// (service memakai nilai bawaan), sedangkan bukan angka ditolak 400.
func intParam(v, nama string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, apierr.Validation("Filter tidak valid.",
			map[string]string{nama: "harus berupa angka"})
	}
	return n, nil
}
