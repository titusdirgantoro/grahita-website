// Package worker menjalankan pekerjaan latar belakang dengan River.
//
// River menyimpan job di PostgreSQL yang sama dengan data bisnis, sehingga
// service bisa men-enqueue job di dalam transaksi yang sama (SystemDesign.md
// §4.2 dan §5.1) — tidak mungkin terjadi "pesanan tersimpan tapi job hilang".
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
)

// PingArgs adalah job contoh yang dipakai untuk memverifikasi rantai
// enqueue → work berjalan. Job bisnis (email, PDF, OCR, reminder) menyusul di
// domain masing-masing dengan pola yang sama.
type PingArgs struct {
	Pesan string `json:"pesan"`
}

// Kind mengidentifikasi jenis job di tabel River.
func (PingArgs) Kind() string { return "ping" }

// PingWorker mencatat job ping ke log.
type PingWorker struct {
	river.WorkerDefaults[PingArgs]
}

// Work dijalankan River untuk setiap job ping.
func (PingWorker) Work(ctx context.Context, job *river.Job[PingArgs]) error {
	slog.InfoContext(ctx, "job ping diterima",
		"id", job.ID, "pesan", job.Args.Pesan, "attempt", job.Attempt)
	return nil
}

// Client membungkus River client beserta pool-nya.
type Client struct {
	river *river.Client[pgx.Tx]
}

// New membuat River client dengan queue sesuai konfigurasi.
func New(pool *database.Pool, cfg config.Config) (*Client, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &PingWorker{})

	queues := map[string]river.QueueConfig{}
	for name, max := range cfg.WorkerQueues {
		if max <= 0 {
			max = 1
		}
		queues[name] = river.QueueConfig{MaxWorkers: max}
	}
	if len(queues) == 0 {
		queues["default"] = river.QueueConfig{MaxWorkers: 4}
	}

	// River mensyaratkan FetchCooldown tidak lebih besar dari FetchPollInterval.
	cooldown := 200 * time.Millisecond
	if cfg.JobPollInterval > 0 && cfg.JobPollInterval < cooldown {
		cooldown = cfg.JobPollInterval / 2
	}

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:            queues,
		Workers:           workers,
		JobTimeout:        2 * time.Minute,
		FetchCooldown:     cooldown,
		FetchPollInterval: cfg.JobPollInterval,
		SoftStopTimeout:   cfg.WorkerShutdown,
	})
	if err != nil {
		return nil, fmt.Errorf("buat river client: %w", err)
	}
	return &Client{river: client}, nil
}

// River mengembalikan client River yang mendasarinya (dipakai untuk enqueue).
func (c *Client) River() *river.Client[pgx.Tx] { return c.river }

// Run menjalankan worker sampai context dibatalkan.
func (c *Client) Run(ctx context.Context) error {
	if err := c.river.Start(ctx); err != nil {
		return fmt.Errorf("jalankan river: %w", err)
	}
	slog.Info("worker river berjalan")

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.river.Stop(stopCtx); err != nil {
		return fmt.Errorf("hentikan river: %w", err)
	}
	return nil
}

// EnqueuePing memasukkan job ping — dipakai /healthz manual dan pengujian.
func (c *Client) EnqueuePing(ctx context.Context, pesan string) (int64, error) {
	res, err := c.river.Insert(ctx, PingArgs{Pesan: pesan}, nil)
	if err != nil {
		return 0, err
	}
	return res.Job.ID, nil
}

// JobCount mengembalikan jumlah job pada satu state (untuk /readyz dan uji).
func JobCount(ctx context.Context, pool *database.Pool, state string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE state = $1`, state).Scan(&n)
	return n, err
}
