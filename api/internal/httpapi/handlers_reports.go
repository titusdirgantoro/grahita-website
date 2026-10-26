package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/report"
)

// reportsUnavailable adalah jawaban bila layanan laporan belum terpasang.
func reportsUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan laporan belum tersedia.")
}

// exportLaporanRequest adalah body POST /api/v1/admin/reports/export:
// filter yang sama dengan GET (PRD §4.7 AP-REP-02) dalam bentuk JSON.
type exportLaporanRequest struct {
	Dari    string   `json:"dari" validate:"omitempty,datetime=2006-01-02"`
	Sampai  string   `json:"sampai" validate:"omitempty,datetime=2006-01-02"`
	Cabang  []string `json:"cabang"`
	Status  string   `json:"status"`
	Jenis   string   `json:"jenis"`
	Cnc     *bool    `json:"cnc"`
	Pi      *bool    `json:"pi"`
}

// Filter mengubah body menjadi filter domain.
func (r exportLaporanRequest) Filter() report.Filter {
	return report.Filter{
		Dari:    r.Dari,
		Sampai:  r.Sampai,
		Cabang:  r.Cabang,
		Status:  r.Status,
		Jenis:   r.Jenis,
		Cnc:     r.Cnc,
		Pi:      r.Pi,
	}
}

// adminReportOrderLines mengembalikan data laporan order line (satu baris per
// order line) dengan paginasi server (PRD §4.7 AP-REP-01..03).
//
// Seluruh filter ada di query string supaya bisa di-bookmark/di-share
// (SystemDesign.md §7.2). Otorisasi Superadmin ditegakkan router
// (CapAdminReports) DAN service.
func (a *API) adminReportOrderLines(w http.ResponseWriter, r *http.Request) error {
	if a.d.Reports == nil {
		return reportsUnavailable()
	}
	f, err := filterLaporanQuery(r)
	if err != nil {
		return err
	}
	page, err := a.d.Reports.List(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(page))
	return nil
}

// adminReportExport membuat pekerjaan ekspor XLSX asinkron (AP-REP-04) dan
// mengembalikan job-nya (status awal `queued`). Jawabannya 202 Accepted: berkas
// belum ada saat permintaan dijawab; client memantau lewat endpoint status.
func (a *API) adminReportExport(w http.ResponseWriter, r *http.Request) error {
	if a.d.Reports == nil {
		return reportsUnavailable()
	}
	var req exportLaporanRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	job, err := a.d.Reports.Request(r.Context(), auth.ActorFrom(r.Context()), req.Filter())
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.JSON(w, http.StatusAccepted, data(job))
	return nil
}

// adminReportExportStatus mengembalikan status satu pekerjaan ekspor
// (queued|proses|selesai|gagal) beserta jumlah baris dan pesan kegagalannya.
func (a *API) adminReportExportStatus(w http.ResponseWriter, r *http.Request) error {
	if a.d.Reports == nil {
		return reportsUnavailable()
	}
	job, err := a.d.Reports.Job(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(job))
	return nil
}

// adminReportExportDownload mengembalikan tautan unduh berumur pendek untuk
// hasil ekspor yang sudah selesai (AP-REP-04: "lalu diunduh").
func (a *API) adminReportExportDownload(w http.ResponseWriter, r *http.Request) error {
	if a.d.Reports == nil {
		return reportsUnavailable()
	}
	dl, err := a.d.Reports.Download(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(dl))
	return nil
}

// filterLaporanQuery membaca filter laporan dari query string. Nilai boolean
// yang tidak dikenal DITOLAK (bukan diam-diam dianggap false) supaya salah
// ketik tidak menghilangkan filter tanpa terlihat.
func filterLaporanQuery(r *http.Request) (report.Filter, error) {
	q := r.URL.Query()
	f := report.Filter{
		Dari:   q.Get("dari"),
		Sampai: q.Get("sampai"),
		Cabang: cabangParam(q["cabang"]),
		Status: q.Get("status"),
		Jenis:  q.Get("jenis"),
	}
	var err error
	if f.Cnc, err = boolParam(q.Get("cnc"), "cnc"); err != nil {
		return report.Filter{}, err
	}
	if f.Pi, err = boolParam(q.Get("pi"), "pi"); err != nil {
		return report.Filter{}, err
	}
	if f.Halaman, err = intParam(q.Get("halaman"), "halaman"); err != nil {
		return report.Filter{}, err
	}
	if f.PerHalaman, err = intParam(q.Get("per_halaman"), "per_halaman"); err != nil {
		return report.Filter{}, err
	}
	return f, nil
}

// cabangParam menerima pilihan cabang multi dalam dua bentuk yang lazim:
// parameter berulang (`?cabang=a&cabang=b`) dan daftar berkoma
// (`?cabang=a,b`). Keduanya digabung.
func cabangParam(nilai []string) []string {
	out := make([]string, 0, len(nilai))
	for _, v := range nilai {
		for _, bagian := range strings.Split(v, ",") {
			bagian = strings.TrimSpace(bagian)
			if bagian != "" {
				out = append(out, bagian)
			}
		}
	}
	return out
}
