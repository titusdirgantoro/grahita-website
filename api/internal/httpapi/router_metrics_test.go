package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/observability"
)

// TestEndpointMetricsDanPenghitungPermintaan membuktikan /metrics tersedia
// tanpa sesi (seperti /healthz) dan middleware metrik benar-benar menghitung
// permintaan lewat router produksi (Architecture.md §8).
func TestEndpointMetricsDanPenghitungPermintaan(t *testing.T) {
	metrik := observability.New(observability.Options{
		Version: "test-versi",
		Env:     "staging",
		Mounts:  []string{"/"},
	})
	router := NewRouter(Deps{
		Config:  config.Config{CustomerHost: "grahita-branch.tiusclaw.com", AdminHost: "grahita-admin.tiusclaw.com"},
		Version: "test-versi",
		Metrics: metrik,
	})

	// Dua permintaan /healthz: satu 200, satu 404 lewat rute tak dikenal.
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/tidak-ada", nil))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, ingin 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	isi := string(body)
	for _, wajib := range []string{
		"grahita_build_info",
		`grahita_http_requests_total{method="GET",route="/healthz",status="200"} 1`,
	} {
		if !strings.Contains(isi, wajib) {
			t.Errorf("/metrics tidak memuat %q", wajib)
		}
	}
	if !strings.Contains(isi, `route="/tidak-ada"`) {
		t.Errorf("/metrics tidak menghitung permintaan 404; isi:\n%s", isi)
	}
}

// TestMetrikMencatat500SaatHandlerPanic menjaga urutan middleware produksi:
// metrik dipasang PALING LUAR (sebelum Recoverer), sehingga status akhir 500
// yang tercatat — bukan 200 sementara. Bila urutannya dibalik, rasio error pada
// alarm Architecture.md §8 akan mengecil dan panic tidak akan terlihat.
func TestMetrikMencatat500SaatHandlerPanic(t *testing.T) {
	metrik := observability.New(observability.Options{Version: "test", Env: "local", Mounts: []string{"/"}})

	// Susunan ini meniru NewRouter: Metrics -> (middleware lain) -> Recoverer.
	r := chi.NewRouter()
	r.Use(metrik.Middleware)
	r.Use(httpx.Recoverer)
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("ledakan uji") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status panic = %d, ingin 500", rec.Code)
	}

	keluarga, err := metrik.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	jumlah := 0.0
	for _, k := range keluarga {
		if k.GetName() != "grahita_http_requests_total" {
			continue
		}
		for _, m := range k.GetMetric() {
			label := map[string]string{}
			for _, lp := range m.GetLabel() {
				label[lp.GetName()] = lp.GetValue()
			}
			if label["route"] == "/boom" && label["status"] == "500" {
				jumlah += m.GetCounter().GetValue()
			}
		}
	}
	if jumlah != 1 {
		t.Fatalf("permintaan panic tercatat sebagai 500 sebanyak %v, ingin 1", jumlah)
	}
}

// TestMetrikNolTidakMengubahRouter membuktikan router tanpa Metrics tetap
// berjalan dan tidak mengekspos /metrics (dipakai pengujian lain).
func TestMetrikNolTidakMengubahRouter(t *testing.T) {
	router := NewRouter(Deps{
		Config:  config.Config{CustomerHost: "a.test", AdminHost: "b.test"},
		Version: "test",
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, ingin 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /metrics tanpa metrik = %d, ingin 404", rec.Code)
	}
}
