package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	dto "github.com/prometheus/client_model/go"
)

// nilaiMencari mengembalikan nilai satu deret waktu dari registri berdasarkan
// nama metrik dan pasangan label. Gagal (t.Fatal) bila deret tidak ada.
func nilaiMencari(t *testing.T, m *Metrics, nama string, labels map[string]string) float64 {
	t.Helper()
	keluarga, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, k := range keluarga {
		if k.GetName() != nama {
			continue
		}
		for _, metrik := range k.GetMetric() {
			if cocokLabel(metrik, labels) {
				switch {
				case metrik.GetGauge() != nil:
					return metrik.GetGauge().GetValue()
				case metrik.GetCounter() != nil:
					return metrik.GetCounter().GetValue()
				case metrik.GetHistogram() != nil:
					return float64(metrik.GetHistogram().GetSampleCount())
				}
			}
		}
	}
	t.Fatalf("metrik %s dengan label %v tidak ditemukan", nama, labels)
	return 0
}

func cocokLabel(metrik *dto.Metric, labels map[string]string) bool {
	dimiliki := map[string]string{}
	for _, lp := range metrik.GetLabel() {
		dimiliki[lp.GetName()] = lp.GetValue()
	}
	for k, v := range labels {
		if dimiliki[k] != v {
			return false
		}
	}
	return true
}

func metrikBaru(t *testing.T, sumber StatsSource) *Metrics {
	t.Helper()
	return New(Options{
		Version: "v1.2.3",
		Env:     "staging",
		Sumber:  sumber,
		Mounts:  []string{"/"},
		jam:     func() time.Time { return time.Date(2026, 10, 9, 5, 0, 0, 0, time.UTC) },
		statfs: func(string) (uint64, uint64, uint64, error) {
			return 1000, 200, 200, nil // terpakai 80%
		},
	})
}

// sumberPalsu adalah implementasi StatsSource untuk pengujian.
type sumberPalsu struct {
	outbox  OutboxStat
	orders  map[string]int64
	jobs    map[string]int64
	backup  map[string]BackupStat
	errLuar error
}

func (s sumberPalsu) EmailOutbox(context.Context) (OutboxStat, error) {
	return s.outbox, s.errLuar
}
func (s sumberPalsu) Orders(context.Context) (map[string]int64, error) {
	return s.orders, s.errLuar
}
func (s sumberPalsu) RiverJobs(context.Context) (map[string]int64, error) {
	return s.jobs, s.errLuar
}
func (s sumberPalsu) BackupRuns(context.Context) (map[string]BackupStat, error) {
	return s.backup, s.errLuar
}

func TestBuildInfoDanHandlerMelayaniMetrik(t *testing.T) {
	m := metrikBaru(t, nil)
	if got := nilaiMencari(t, m, "grahita_build_info", map[string]string{"version": "v1.2.3", "env": "staging"}); got != 1 {
		t.Fatalf("build_info = %v, ingin 1", got)
	}

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status /metrics = %d, ingin 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	for _, wajib := range []string{"grahita_build_info", "go_goroutines"} {
		if !strings.Contains(string(body), wajib) {
			t.Errorf("isi /metrics tidak memuat %q", wajib)
		}
	}
}

func TestCatatPermintaanMencatatStatusDanDurasi(t *testing.T) {
	m := metrikBaru(t, nil)
	m.CatatPermintaan("/api/v1/orders", http.MethodGet, http.StatusOK, 50*time.Millisecond)
	m.CatatPermintaan("/api/v1/orders", http.MethodGet, http.StatusInternalServerError, 120*time.Millisecond)

	if got := nilaiMencari(t, m, "grahita_http_requests_total", map[string]string{"route": "/api/v1/orders", "method": "GET", "status": "200"}); got != 1 {
		t.Errorf("permintaan 200 = %v, ingin 1", got)
	}
	if got := nilaiMencari(t, m, "grahita_http_requests_total", map[string]string{"route": "/api/v1/orders", "method": "GET", "status": "500"}); got != 1 {
		t.Errorf("permintaan 500 = %v, ingin 1", got)
	}
	if got := nilaiMencari(t, m, "grahita_http_request_duration_seconds", map[string]string{"route": "/api/v1/orders", "method": "GET"}); got != 2 {
		t.Errorf("jumlah observasi durasi = %v, ingin 2", got)
	}
}

// TestMiddlewareMemakaiPolaRute membuktikan label rute memakai pola chi (bukan
// path mentah) sehingga id pesanan tidak meledakkan deret waktu.
func TestMiddlewareMemakaiPolaRute(t *testing.T) {
	m := metrikBaru(t, nil)
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/api/v1/orders/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/orders/0199-abc", nil))

	if got := nilaiMencari(t, m, "grahita_http_requests_total", map[string]string{"route": "/api/v1/orders/{id}", "method": "GET", "status": "200"}); got != 1 {
		t.Fatalf("permintaan dengan pola rute = %v, ingin 1", got)
	}
	// Tidak boleh ada deret waktu dengan path mentah.
	keluarga, _ := m.reg.Gather()
	for _, k := range keluarga {
		if k.GetName() != "grahita_http_requests_total" {
			continue
		}
		for _, metrik := range k.GetMetric() {
			for _, lp := range metrik.GetLabel() {
				if lp.GetName() == "route" && strings.Contains(lp.GetValue(), "0199-abc") {
					t.Fatalf("label rute memakai path mentah: %s", lp.GetValue())
				}
			}
		}
	}
}

func TestCatatJobMenghitungHasil(t *testing.T) {
	m := metrikBaru(t, nil)
	m.CatatJob("email_outbox_pindai", 2*time.Second, nil)
	m.CatatJob("render_dokumen_pesanan", 500*time.Millisecond, errors.New("gotenberg belum siap"))
	m.CatatJob("render_dokumen_pesanan", time.Second, errors.New("gagal lagi"))

	if got := nilaiMencari(t, m, "grahita_jobs_completed_total", map[string]string{"kind": "email_outbox_pindai", "outcome": "sukses"}); got != 1 {
		t.Errorf("job sukses = %v, ingin 1", got)
	}
	if got := nilaiMencari(t, m, "grahita_jobs_completed_total", map[string]string{"kind": "render_dokumen_pesanan", "outcome": "gagal"}); got != 2 {
		t.Errorf("job gagal = %v, ingin 2", got)
	}
}

func TestPerbaruiStatistikDariSumberDanDisk(t *testing.T) {
	jam := time.Date(2026, 10, 9, 5, 0, 0, 0, time.UTC)
	sumber := sumberPalsu{
		outbox: OutboxStat{
			PerStatus:      map[string]int64{"queued": 4, "sending": 1, "sent": 90, "failed": 2},
			TertuaMenunggu: jam.Add(-45 * time.Minute),
		},
		orders: map[string]int64{"draft": 3, "submitted": 7, "done": 12},
		jobs:   map[string]int64{"available": 2, "discarded": 1, "completed": 50},
		backup: map[string]BackupStat{
			"postgres": {Ada: true, Sukses: true, SelesaiPada: jam.Add(-3 * time.Hour)},
			"wal":      {Ada: true, Sukses: false, SelesaiPada: jam.Add(-1 * time.Hour)},
		},
	}
	m := metrikBaru(t, sumber)
	m.PerbaruiStatistik(context.Background())

	if got := nilaiMencari(t, m, "grahita_outbox_messages", map[string]string{"status": "queued"}); got != 4 {
		t.Errorf("outbox queued = %v, ingin 4", got)
	}
	if got := nilaiMencari(t, m, "grahita_outbox_oldest_pending_seconds", nil); got != (45 * 60) {
		t.Errorf("outbox tertua = %v detik, ingin 2700", got)
	}
	if got := nilaiMencari(t, m, "grahita_orders_current", map[string]string{"status": "done"}); got != 12 {
		t.Errorf("pesanan done = %v, ingin 12", got)
	}
	if got := nilaiMencari(t, m, "grahita_jobs_current", map[string]string{"state": "discarded"}); got != 1 {
		t.Errorf("job discarded = %v, ingin 1", got)
	}
	if got := nilaiMencari(t, m, "grahita_backup_last_run_success", map[string]string{"jenis": "postgres"}); got != 1 {
		t.Errorf("backup postgres sukses = %v, ingin 1", got)
	}
	if got := nilaiMencari(t, m, "grahita_backup_last_run_success", map[string]string{"jenis": "wal"}); got != 0 {
		t.Errorf("backup wal sukses = %v, ingin 0", got)
	}
	if got := nilaiMencari(t, m, "grahita_disk_used_ratio", map[string]string{"mount": "/"}); got != 0.8 {
		t.Errorf("rasio disk = %v, ingin 0.8", got)
	}
}

// TestPerbaruiStatistikTetapJalanSaatSumberGagal membuktikan kegagalan satu
// sumber database tidak menghilangkan metrik lain (khususnya disk).
func TestPerbaruiStatistikTetapJalanSaatSumberGagal(t *testing.T) {
	m := metrikBaru(t, sumberPalsu{errLuar: errors.New("postgres mati")})
	m.PerbaruiStatistik(context.Background())

	if got := nilaiMencari(t, m, "grahita_disk_used_ratio", map[string]string{"mount": "/"}); got != 0.8 {
		t.Fatalf("rasio disk tetap harus terisi walau database gagal, dapat %v", got)
	}
	// Gauge outbox tidak boleh terisi angka palsu.
	keluarga, _ := m.reg.Gather()
	for _, k := range keluarga {
		if k.GetName() == "grahita_outbox_messages" && len(k.GetMetric()) > 0 {
			t.Fatalf("gauge outbox terisi padahal sumber gagal: %v", k.GetMetric())
		}
	}
}

func TestJalankanPemantauanBerhentiSaatContextDibatalkan(t *testing.T) {
	m := metrikBaru(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	selesai := make(chan struct{})
	go func() {
		m.JalankanPemantauan(ctx, 5*time.Millisecond)
		close(selesai)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-selesai:
	case <-time.After(2 * time.Second):
		t.Fatal("JalankanPemantauan tidak berhenti setelah context dibatalkan")
	}
}

func TestInitSentryNonaktifTanpaDSN(t *testing.T) {
	tutup, err := InitSentry(SentryOptions{})
	if err != nil {
		t.Fatalf("InitSentry tanpa DSN: %v", err)
	}
	if tutup == nil {
		t.Fatal("fungsi penutup tidak boleh nil")
	}
	if SentryAktif() {
		t.Fatal("Sentry tidak boleh aktif tanpa DSN")
	}
	tutup()

	// Semua pelaporan harus aman tanpa client.
	TangkapError(context.Background(), errors.New("boom"), map[string]string{"route": "/healthz"})
	TangkapPanic(context.Background(), "boom", nil)
}

func TestMountsDariEnv(t *testing.T) {
	got := MountsDariEnv(" /, /var/lib/postgresql ,, ")
	if len(got) != 2 || got[0] != "/" || got[1] != "/var/lib/postgresql" {
		t.Fatalf("MountsDariEnv = %v", got)
	}
	if MountsDariEnv("") != nil {
		t.Fatal("daftar kosong harus nil")
	}
}
