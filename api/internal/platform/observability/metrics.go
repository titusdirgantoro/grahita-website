// Package observability menyediakan metrik Prometheus dan pelaporan error
// Sentry (Architecture.md §8).
//
// Paket ini adalah fondasi (platform): ia tidak boleh bergantung pada domain
// aplikasi. Seluruh data bisnis yang dipublikasikan sebagai metrik diambil
// lewat interface StatsSource yang diisi composition root, sehingga paket ini
// tetap bebas dari pgx/sqlc.
//
// Nama metrik memakai namespace "grahita" dan dipakai apa adanya oleh berkas
// aturan alert di infra/observability/prometheus/rules/grahita-alerts.yml —
// mengubah nama metrik berarti mengubah alert juga.
package observability

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace adalah awalan seluruh metrik aplikasi.
const Namespace = "grahita"

// StatsSource menyediakan angka keadaan yang hanya diketahui database.
//
// Implementasinya ada di composition root (cmd/server) supaya paket platform
// ini tidak menyentuh pgx. Boleh nil: metrik kesehatan proses dan HTTP tetap
// berjalan tanpa database.
type StatsSource interface {
	// EmailOutbox mengembalikan jumlah baris email_outbox per status
	// (queued/sending/sent/failed) beserta waktu pembuatan baris yang paling
	// lama masih menunggu (queued/sending). Waktu nol berarti tidak ada yang
	// menunggu.
	EmailOutbox(ctx context.Context) (OutboxStat, error)

	// Orders mengembalikan jumlah pesanan per status (draft/submitted/done).
	Orders(ctx context.Context) (map[string]int64, error)

	// RiverJobs mengembalikan jumlah pekerjaan River per state
	// (available/running/retryable/discarded/completed/...).
	RiverJobs(ctx context.Context) (map[string]int64, error)

	// BackupRuns mengembalikan hasil cadangan TERAKHIR per jenis
	// (postgres/storage/wal).
	BackupRuns(ctx context.Context) (map[string]BackupStat, error)
}

// OutboxStat adalah keadaan antrean email (F1-16).
type OutboxStat struct {
	PerStatus      map[string]int64
	TertuaMenunggu time.Time
}

// BackupStat adalah hasil satu jenis cadangan pada baris terakhir (F1-21).
type BackupStat struct {
	Ada         bool
	Sukses      bool
	SelesaiPada time.Time
}

// Options adalah parameter pembuatan Metrics.
type Options struct {
	// Version adalah versi build (dipakai label grahita_build_info).
	Version string
	// Env adalah lingkungan (local/staging/production).
	Env string
	// Sumber menyediakan angka database. Boleh nil.
	Sumber StatsSource
	// Mounts adalah titik mount yang rasionya dipantau. Kosong berarti "/".
	Mounts []string
	// Logger untuk kegagalan pengumpulan metrik. Boleh nil.
	Logger *slog.Logger

	// jam dan statfs dapat diganti pengujian.
	jam    func() time.Time
	statfs func(path string) (uint64, uint64, uint64, error)
}

// Metrics adalah registri metrik aplikasi beserta middleware HTTP-nya.
type Metrics struct {
	reg    *prometheus.Registry
	logger *slog.Logger
	sumber StatsSource
	mounts []string
	jam    func() time.Time
	statfs func(path string) (uint64, uint64, uint64, error)

	buildInfo      *prometheus.GaugeVec
	permintaan     *prometheus.CounterVec
	latensi        *prometheus.HistogramVec
	sedangDiproses prometheus.Gauge
	jobTotal       *prometheus.CounterVec
	jobDurasi      *prometheus.HistogramVec
	jobState       *prometheus.GaugeVec
	outboxStatus   *prometheus.GaugeVec
	outboxTertua   prometheus.Gauge
	pesananStatus  *prometheus.GaugeVec
	backupSukses   *prometheus.GaugeVec
	backupWaktu    *prometheus.GaugeVec
	diskRasio      *prometheus.GaugeVec
}

// New membuat Metrics beserta seluruh kolektornya.
func New(o Options) *Metrics {
	if o.jam == nil {
		o.jam = time.Now
	}
	if o.statfs == nil {
		o.statfs = statfsRasio
	}
	if len(o.Mounts) == 0 {
		o.Mounts = []string{"/"}
	}

	m := &Metrics{
		reg:    prometheus.NewRegistry(),
		logger: o.Logger,
		sumber: o.Sumber,
		mounts: o.Mounts,
		jam:    o.jam,
		statfs: o.statfs,
	}

	m.buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "build_info",
		Help:      "Informasi build yang sedang berjalan (nilai selalu 1).",
	}, []string{"version", "env"})

	m.permintaan = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "http_requests_total",
		Help:      "Jumlah permintaan HTTP yang selesai, per rute, metode, dan kode status.",
	}, []string{"route", "method", "status"})

	m.latensi = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: Namespace,
		Name:      "http_request_duration_seconds",
		Help:      "Durasi permintaan HTTP dalam detik, per rute dan metode.",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"route", "method"})

	m.sedangDiproses = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "http_in_flight_requests",
		Help:      "Jumlah permintaan HTTP yang sedang diproses.",
	})

	m.jobTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "jobs_completed_total",
		Help:      "Jumlah pekerjaan River yang selesai dikerjakan, per jenis dan hasil.",
	}, []string{"kind", "outcome"})

	m.jobDurasi = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: Namespace,
		Name:      "job_duration_seconds",
		Help:      "Durasi pengerjaan satu pekerjaan River dalam detik.",
		Buckets:   []float64{0.05, 0.25, 1, 5, 15, 60, 300, 900},
	}, []string{"kind"})

	m.jobState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "jobs_current",
		Help:      "Jumlah pekerjaan River pada setiap state (available/running/retryable/discarded/completed).",
	}, []string{"state"})

	m.outboxStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "outbox_messages",
		Help:      "Jumlah baris email_outbox per status (queued/sending/sent/failed).",
	}, []string{"status"})

	m.outboxTertua = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "outbox_oldest_pending_seconds",
		Help:      "Umur (detik) email tertua yang masih menunggu dikirim; 0 bila tidak ada.",
	})

	m.pesananStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "orders_current",
		Help:      "Jumlah pesanan per status (draft/submitted/done).",
	}, []string{"status"})

	m.backupSukses = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "backup_last_run_success",
		Help:      "1 bila cadangan terakhir untuk jenis ini sukses, 0 bila gagal.",
	}, []string{"jenis"})

	m.backupWaktu = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "backup_last_run_timestamp_seconds",
		Help:      "Waktu (epoch detik) cadangan terakhir per jenis.",
	}, []string{"jenis"})

	m.diskRasio = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "disk_used_ratio",
		Help:      "Rasio pemakaian disk (0..1) per titik mount.",
	}, []string{"mount"})

	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.buildInfo, m.permintaan, m.latensi, m.sedangDiproses,
		m.jobTotal, m.jobDurasi, m.jobState,
		m.outboxStatus, m.outboxTertua, m.pesananStatus,
		m.backupSukses, m.backupWaktu, m.diskRasio,
	)
	m.buildInfo.WithLabelValues(o.Version, o.Env).Set(1)
	return m
}

// Registry mengembalikan registri metrik (dipakai pengujian dan Gathering
// manual).
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// Handler melayani /metrics dalam format teks Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		// Satu kolektor yang gagal tidak boleh menghilangkan seluruh metrik.
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      nil,
	})
}

// Middleware mencatat jumlah dan durasi setiap permintaan HTTP.
//
// Label rute memakai pola rute chi (bukan path mentah) supaya id di URL tidak
// meledakkan jumlah deret waktu.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mulai := m.jam()
		m.sedangDiproses.Inc()
		sc := &statusCatcher{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			m.sedangDiproses.Dec()
			m.CatatPermintaan(routePattern(r), r.Method, sc.status, m.jam().Sub(mulai))
		}()
		next.ServeHTTP(sc, r)
	})
}

// CatatPermintaan menambah satu titik data permintaan HTTP. Dipisah dari
// middleware supaya dapat diuji tanpa server.
func (m *Metrics) CatatPermintaan(route, method string, status int, durasi time.Duration) {
	if route == "" {
		route = "tidak-dikenal"
	}
	kode := strconv.Itoa(status)
	m.permintaan.WithLabelValues(route, method, kode).Inc()
	m.latensi.WithLabelValues(route, method).Observe(durasi.Seconds())
}

// CatatJob mencatat hasil satu pekerjaan River. PencatatJob dari paket worker.
func (m *Metrics) CatatJob(kind string, durasi time.Duration, err error) {
	if kind == "" {
		kind = "tidak-dikenal"
	}
	outcome := "sukses"
	if err != nil {
		outcome = "gagal"
	}
	m.jobTotal.WithLabelValues(kind, outcome).Inc()
	if durasi > 0 {
		m.jobDurasi.WithLabelValues(kind).Observe(durasi.Seconds())
	}
}

// PerbaruiStatistik menyegarkan gauge yang berasal dari database dan disk.
//
// Kegagalan setiap sumber diperlakukan sebagai kegagalan lunak: metrik lain
// tetap diperbarui dan kesalahan dicatat ke log (bukan menjatuhkan proses
// pelapor).
func (m *Metrics) PerbaruiStatistik(ctx context.Context) {
	if m.sumber != nil {
		if outbox, err := m.sumber.EmailOutbox(ctx); err != nil {
			m.catatGagal("email_outbox", err)
		} else {
			for status, jumlah := range outbox.PerStatus {
				m.outboxStatus.WithLabelValues(status).Set(float64(jumlah))
			}
			tertua := 0.0
			if !outbox.TertuaMenunggu.IsZero() {
				umur := m.jam().Sub(outbox.TertuaMenunggu)
				if umur > 0 {
					tertua = umur.Seconds()
				}
			}
			m.outboxTertua.Set(tertua)
		}

		if pesanan, err := m.sumber.Orders(ctx); err != nil {
			m.catatGagal("orders", err)
		} else {
			for status, jumlah := range pesanan {
				m.pesananStatus.WithLabelValues(status).Set(float64(jumlah))
			}
		}

		if jobs, err := m.sumber.RiverJobs(ctx); err != nil {
			m.catatGagal("river_job", err)
		} else {
			for state, jumlah := range jobs {
				m.jobState.WithLabelValues(state).Set(float64(jumlah))
			}
		}

		if cadangan, err := m.sumber.BackupRuns(ctx); err != nil {
			m.catatGagal("backup_runs", err)
		} else {
			for jenis, stat := range cadangan {
				if !stat.Ada {
					continue
				}
				sukses := 0.0
				if stat.Sukses {
					sukses = 1
				}
				m.backupSukses.WithLabelValues(jenis).Set(sukses)
				if !stat.SelesaiPada.IsZero() {
					m.backupWaktu.WithLabelValues(jenis).Set(float64(stat.SelesaiPada.Unix()))
				}
			}
		}
	}

	for _, mount := range m.mounts {
		blok, bebas, _, err := m.statfs(mount)
		if err != nil || blok == 0 {
			if err != nil {
				m.catatGagal("disk "+mount, err)
			}
			continue
		}
		m.diskRasio.WithLabelValues(mount).Set(1 - float64(bebas)/float64(blok))
	}
}

// JalankanPemantauan memperbarui statistik secara berkala sampai context
// dibatalkan. Satu pembaruan langsung dilakukan sebelum menunggu tick pertama
// supaya /metrics tidak kosong pada detik-detik awal.
func (m *Metrics) JalankanPemantauan(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	m.PerbaruiStatistik(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.PerbaruiStatistik(ctx)
		}
	}
}

func (m *Metrics) catatGagal(sumber string, err error) {
	if m.logger == nil {
		return
	}
	m.logger.Warn("gagal mengumpulkan metrik", "sumber", sumber, "err", err)
}

// routePattern mengambil pola rute chi bila middleware dipasang di dalam
// router chi; selain itu jatuh ke Pattern (Go 1.22+) lalu path mentah.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}

// statusCatcher merekam kode status tanpa menelan interface opsional
// (http.Flusher, http.Hijacker) yang dipakai net/http.
type statusCatcher struct {
	http.ResponseWriter
	status int
}

func (w *statusCatcher) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCatcher) Write(b []byte) (int, error) { return w.ResponseWriter.Write(b) }

// Unwrap memungkinkan http.ResponseController menjangkau ResponseWriter asli.
func (w *statusCatcher) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// MountsDariEnv memecah daftar titik mount dipisah koma.
func MountsDariEnv(nilai string) []string {
	var out []string
	for _, bagian := range strings.Split(nilai, ",") {
		if bagian = strings.TrimSpace(bagian); bagian != "" {
			out = append(out, bagian)
		}
	}
	return out
}
