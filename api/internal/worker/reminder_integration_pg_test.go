package worker_test

// Uji integrasi pengingat C&C lewat worker River nyata (F2-23): job
// "reminder_cnc" benar-benar terdaftar, dijalankan River, dan menghasilkan
// tepat satu baris cnc_reminders walau dijalankan lebih dari sekali.
//
// Uji dijalankan bila TEST_DATABASE_URL diisi.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/grahita/portal/api/internal/notification"
	notificationpg "github.com/grahita/portal/api/internal/notification/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/worker"
)

func TestWorkerMenjalankanPengingatCNC(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi worker dilewati")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("koneksi database: %v", err)
	}
	defer pool.Close()

	// Shim uuidv7 untuk PostgreSQL < 18 (target resmi proyek), lalu migrasi.
	var ada bool
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('uuidv7()') IS NOT NULL`).Scan(&ada); err != nil {
		t.Fatalf("cek uuidv7: %v", err)
	}
	if !ada {
		shim, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "pg-dev-shim.sql"))
		if err != nil {
			t.Fatalf("baca shim uuidv7: %v", err)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("ambil koneksi shim: %v", err)
		}
		_, err = conn.Conn().PgConn().Exec(ctx, string(shim)).ReadAll()
		conn.Release()
		if err != nil {
			t.Fatalf("pasang shim uuidv7: %v", err)
		}
	}
	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi skema: %v", err)
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatalf("siapkan migrasi River: %v", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("migrasi tabel River: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE branches, orders, cnc_reminders, email_outbox, audit_logs,
		river_job, river_queue RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("kosongkan tabel: %v", err)
	}

	// Pesanan C&C jatuh tempo 2026-10-09; pekerjaan disuntik clock 2026-10-07
	// sehingga tahap h-2 yang harus lahir.
	var branchID string
	if err := pool.QueryRow(ctx, `INSERT INTO branches (nama, nama_kepala, email, is_cnc)
		VALUES ('Cabang CNC Worker','Kepala','cabang-worker@example.test',true) RETURNING id`).
		Scan(&branchID); err != nil {
		t.Fatalf("seed cabang: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (branch_id, nomor, versi, status, tanggal, is_cnc, jatuh_tempo, total, submitted_at)
		VALUES ($1,'2026-10-0001',0,'submitted','2026-10-01',true,'2026-10-09',350000,now())`, branchID); err != nil {
		t.Fatalf("seed pesanan C&C: %v", err)
	}

	clk := clock.Fixed(time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()))
	repo := notificationpg.New(pool)
	notifSvc := notification.New(repo, nil,
		notification.WithClock(clk), notification.WithReminderStore(repo))

	cfg := config.Config{
		WorkerQueues:    map[string]int{"default": 1},
		JobPollInterval: 100 * time.Millisecond,
		WorkerShutdown:  5 * time.Second,
	}
	client, err := worker.New(pool, cfg, notifSvc, nil, nil, nil)
	if err != nil {
		t.Fatalf("buat worker: %v", err)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	selesai := make(chan error, 1)
	go func() { selesai <- client.Run(runCtx) }()

	// Job kedua disisipkan manual; job jadwal (RunOnStart) sudah memicu satu
	// kali. Meski dua job berjalan, tahap h-2 hanya boleh lahir SEKALI.
	if _, err := client.River().Insert(ctx, jobs.ReminderCNCArgs{}, nil); err != nil {
		t.Fatalf("sisipkan job pengingat: %v", err)
	}

	tenggat := time.Now().Add(30 * time.Second)
	var n int
	for time.Now().Before(tenggat) {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cnc_reminders`).Scan(&n); err != nil {
			t.Fatalf("hitung cnc_reminders: %v", err)
		}
		if n > 0 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if n == 0 {
		t.Fatal("job pengingat C&C tidak menghasilkan baris cnc_reminders")
	}
	// Beri kesempatan job kedua dijalankan sebelum memastikan tidak ganda.
	time.Sleep(time.Second)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cnc_reminders`).Scan(&n); err != nil {
		t.Fatalf("hitung cnc_reminders akhir: %v", err)
	}
	if n != 1 {
		t.Fatalf("pengingat tahap h-2 harus tepat satu baris, dapat %d", n)
	}
	var jenis string
	if err := pool.QueryRow(ctx, `SELECT jenis::text FROM email_outbox`).Scan(&jenis); err != nil {
		t.Fatalf("baca email_outbox: %v", err)
	}
	if jenis != notification.JenisReminderCNC {
		t.Fatalf("email harus %s, dapat %s", notification.JenisReminderCNC, jenis)
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
