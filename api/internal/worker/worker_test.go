package worker_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/worker"
)

// TestWorkerMenjalankanJob menyalakan River di atas PostgreSQL nyata,
// memasukkan satu job, lalu memastikan job itu selesai dikerjakan.
//
// Job queue berada di database yang sama dengan data bisnis sehingga enqueue
// bisa ikut dalam transaksi bisnis (SystemDesign.md §4.2). Uji ini dijalankan
// bila TEST_DATABASE_URL diisi.
func TestWorkerMenjalankanJob(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi worker dilewati")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("koneksi database: %v", err)
	}
	defer pool.Close()

	// Tabel River dibuat oleh mode --mode=migrate; uji ini memastikannya ada
	// supaya kegagalan datang dari job, bukan dari skema yang belum siap.
	var ada bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                 WHERE table_schema = 'public' AND table_name = 'river_job')`).Scan(&ada); err != nil {
		t.Fatalf("cek tabel river_job: %v", err)
	}
	if !ada {
		t.Skip("tabel river_job belum ada — jalankan --mode=migrate lebih dulu")
	}

	cfg := config.Config{
		WorkerQueues:    map[string]int{"default": 1},
		JobPollInterval: 100 * time.Millisecond,
		WorkerShutdown:  5 * time.Second,
	}

	client, err := worker.New(pool, cfg, nil, nil)
	if err != nil {
		t.Fatalf("buat worker: %v", err)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()

	selesai := make(chan error, 1)
	go func() { selesai <- client.Run(runCtx) }()

	id, err := client.EnqueuePing(ctx, "uji fondasi F1-01")
	if err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if id == 0 {
		t.Fatal("id job tidak boleh nol")
	}

	tenggat := time.Now().Add(30 * time.Second)
	var state string
	for time.Now().Before(tenggat) {
		if err := pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatalf("baca state job: %v", err)
		}
		if state == "completed" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if state != "completed" {
		t.Fatalf("job %d tidak selesai, state terakhir %q", id, state)
	}

	stopRun()
	select {
	case err := <-selesai:
		if err != nil {
			t.Fatalf("worker berhenti dengan error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker tidak berhenti setelah context dibatalkan")
	}
}
