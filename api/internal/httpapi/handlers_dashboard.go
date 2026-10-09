package httpapi

import (
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/dashboard"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// dashboardUnavailable adalah jawaban bila layanan dashboard belum terpasang.
func dashboardUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan dashboard belum tersedia.")
}

// adminDashboard mengembalikan metrik dashboard admin Pusat beserta chip
// "perlu tindakan" (PRD §4.1 AP-DSH-01..06).
//
// Filter ada di query string supaya bisa di-bookmark/di-share (SystemDesign
// §7.2): `cabang` (kosong/`semua` = seluruh cabang) dan `periode`
// (`hari|minggu|bulan|tahun`, kosong = bulan).
//
// Nilai rupiah TIDAK difilter di sini: service-lah yang menghilangkan field
// `nilai` untuk Manager (AP-DSH-02), karena Manager boleh membuka dashboard dan
// hanya angka uangnya yang tidak boleh keluar.
func (a *API) adminDashboard(w http.ResponseWriter, r *http.Request) error {
	if a.d.Dashboard == nil {
		return dashboardUnavailable()
	}
	q := r.URL.Query()
	f := dashboard.Filter{Cabang: q.Get("cabang"), Periode: q.Get("periode")}
	d, err := a.d.Dashboard.Get(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(d))
	return nil
}
