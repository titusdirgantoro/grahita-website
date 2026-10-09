package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/observability"
)

// sumberStatistik membaca keadaan operasional dari PostgreSQL untuk metrik
// Prometheus (Architecture.md §8).
//
// Ia hidup di composition root supaya paket platform/observability tetap bebas
// dari pgx. Setiap kueri diberi batas waktu sendiri: pengumpulan metrik tidak
// boleh menggantung (dan menahan /metrics) saat database sedang sibuk.
type sumberStatistik struct {
	pool *pgxpool.Pool
}

const batasKueriMetrik = 5 * time.Second

// EmailOutbox menghitung baris email_outbox per status dan umur antrean
// tertua yang belum terkirim (F1-16). Alert "email tertahan" bersandar pada
// metrik ini.
func (s sumberStatistik) EmailOutbox(ctx context.Context) (observability.OutboxStat, error) {
	ctx, cancel := context.WithTimeout(ctx, batasKueriMetrik)
	defer cancel()

	stat := observability.OutboxStat{PerStatus: map[string]int64{}}
	rows, err := s.pool.Query(ctx, `SELECT status::text, count(*) FROM email_outbox GROUP BY 1`)
	if err != nil {
		return stat, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var jumlah int64
		if err := rows.Scan(&status, &jumlah); err != nil {
			return stat, err
		}
		stat.PerStatus[status] = jumlah
	}
	if err := rows.Err(); err != nil {
		return stat, err
	}

	// Baris 'sending' yang lama tertinggal juga dihitung menunggu: itulah
	// keadaan yang justru paling perlu alarm (worker mati di tengah kirim).
	var tertua *time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT min(created_at) FROM email_outbox WHERE status IN ('queued','sending')`).Scan(&tertua); err != nil {
		return stat, err
	}
	if tertua != nil {
		stat.TertuaMenunggu = *tertua
	}
	return stat, nil
}

// Orders menghitung pesanan per status.
func (s sumberStatistik) Orders(ctx context.Context) (map[string]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, batasKueriMetrik)
	defer cancel()

	hasil := map[string]int64{}
	rows, err := s.pool.Query(ctx, `SELECT status::text, count(*) FROM orders GROUP BY 1`)
	if err != nil {
		return hasil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var jumlah int64
		if err := rows.Scan(&status, &jumlah); err != nil {
			return hasil, err
		}
		hasil[status] = jumlah
	}
	return hasil, rows.Err()
}

// RiverJobs menghitung pekerjaan River per state. State discarded/retryable
// adalah dasar alarm "pekerjaan gagal permanen atau tertinggal" (F1-18).
func (s sumberStatistik) RiverJobs(ctx context.Context) (map[string]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, batasKueriMetrik)
	defer cancel()

	hasil := map[string]int64{}
	rows, err := s.pool.Query(ctx, `SELECT state::text, count(*) FROM river_job GROUP BY 1`)
	if err != nil {
		return hasil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var jumlah int64
		if err := rows.Scan(&state, &jumlah); err != nil {
			return hasil, err
		}
		hasil[state] = jumlah
	}
	return hasil, rows.Err()
}

// BackupRuns mengambil hasil cadangan TERAKHIR per jenis (F1-21): sukses dan
// waktunya. jenis tanpa baris tidak dilaporkan (belum pernah berjalan).
func (s sumberStatistik) BackupRuns(ctx context.Context) (map[string]observability.BackupStat, error) {
	ctx, cancel := context.WithTimeout(ctx, batasKueriMetrik)
	defer cancel()

	hasil := map[string]observability.BackupStat{}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (jenis) jenis, sukses, coalesce(selesai, mulai)
		FROM backup_runs
		ORDER BY jenis, mulai DESC`)
	if err != nil {
		return hasil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			jenis   string
			sukses  *bool
			selesai time.Time
		)
		if err := rows.Scan(&jenis, &sukses, &selesai); err != nil {
			return hasil, err
		}
		stat := observability.BackupStat{Ada: true, SelesaiPada: selesai}
		if sukses != nil {
			stat.Sukses = *sukses
		}
		hasil[jenis] = stat
	}
	return hasil, rows.Err()
}

// jalankanMetrikWorker menyalakan listener HTTP kecil berisi /metrics dan
// /healthz untuk proses worker (Architecture.md §8).
//
// Worker tidak melayani HTTP lain, jadi tanpa listener ini metrik pekerjaan
// River (durasi, kegagalan, panjang antrean) tidak akan pernah dipanen
// Prometheus. Alamatnya default ":9091" dan tidak dipublikasikan ke host.
func jalankanMetrikWorker(ctx context.Context, cfg config.Config, logger *slog.Logger, metrik *observability.Metrics) (func(), error) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrik.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	ln, err := net.Listen("tcp", cfg.WorkerMetricsAddr)
	if err != nil {
		return nil, fmt.Errorf("dengarkan metrik worker di %s: %w", cfg.WorkerMetricsAddr, err)
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("metrik worker listening", "addr", cfg.WorkerMetricsAddr)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server metrik worker berhenti", "err", err)
		}
	}()

	// Context dari sinyal juga menutup listener supaya tidak menahan port saat
	// proses dimatikan dengan rapi.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}, nil
}
