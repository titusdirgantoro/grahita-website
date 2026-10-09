package httpapi

import (
	"net/http"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// adminAuditLogs melayani GET /api/v1/admin/audit-logs (Superadmin, kartu F2-28).
//
// Seluruh filter ada di query string supaya daftar terfilter bisa
// di-bookmark/di-share (SystemDesign.md §7.2). Otorisasi ditegakkan router
// (kapabilitas `admin.audit` yang hanya dimiliki Superadmin) DAN layer service,
// jadi Manager tidak bisa membacanya walau URL-nya ditebak.
func (a *API) adminAuditLogs(w http.ResponseWriter, r *http.Request) error {
	if a.d.Audit == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable,
			"Layanan audit log belum tersedia.")
	}
	f, err := filterAuditQuery(r)
	if err != nil {
		return err
	}
	page, err := a.d.Audit.List(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(page))
	return nil
}

// filterAuditQuery membaca filter audit dari query string.
//
// `entitas` boleh diulang maupun dipisah koma; `aktor` menerima id akun (UUID)
// atau potongan nama. Nilai tak dikenal DITOLAK (bukan diam-diam diabaikan)
// supaya salah ketik tidak membuat admin mengira tidak ada aktivitas sama
// sekali.
func filterAuditQuery(r *http.Request) (audit.Filter, error) {
	q := r.URL.Query()
	f := audit.Filter{
		Entitas: q["entitas"],
		Aksi:    q.Get("aksi"),
		Aktor:   q.Get("aktor"),
		Dari:    q.Get("dari"),
		Sampai:  q.Get("sampai"),
	}
	var err error
	if f.Halaman, err = intParam(q.Get("halaman"), "halaman"); err != nil {
		return audit.Filter{}, err
	}
	if f.PerHalaman, err = intParam(q.Get("per_halaman"), "per_halaman"); err != nil {
		return audit.Filter{}, err
	}
	return f, nil
}
