package worker_test

// Uji integrasi pengingat C&C lewat worker River nyata (F2-23): job
// "reminder_cnc" benar-benar terdaftar, dijalankan River, dan menghasilkan
// tepat satu baris cnc_reminders walau dijalankan lebih dari sekali.
//
// Uji dijalankan bila TEST_DATABASE_URL diisi.

import (
	"context"
	"fmt"
	"net/url"
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
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi worker dilewati")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	adminPool, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi database: %v", err)
	}
	defer adminPool.Close()

	// Setiap package integrasi memakai skema unik agar migrasi/TRUNCATE worker
	// tidak berdeadlock dengan suite HTTP atau migrasi yang berjalan paralel.
	skema := fmt.Sprintf("worker_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+skema); err != nil {
		t.Fatalf("buat skema worker: %v", err)
	}
	defer func() {
		bersihCtx, batal := context.WithTimeout(context.Background(), 30*time.Second)
		defer batal()
		if _, err := adminPool.Exec(bersihCtx, `DROP SCHEMA IF EXISTS `+skema+` CASCADE`); err != nil {
			t.Logf("hapus skema worker: %v", err)
		}
	}()
	if _, err := adminPool.Exec(ctx, `CREATE TABLE `+skema+`.goose_db_version (
		id serial NOT NULL, version_id bigint NOT NULL, is_applied boolean NOT NULL,
		tstamp timestamp NULL DEFAULT now(), PRIMARY KEY (id));
		INSERT INTO `+skema+`.goose_db_version (version_id, is_applied) VALUES (0, true)`); err != nil {
		t.Fatalf("siapkan versi migrasi worker: %v", err)
	}
	parsedURL, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN worker tidak valid: %v", err)
	}
	query := parsedURL.Query()
	query.Set("search_path", skema+",public")
	parsedURL.RawQuery = query.Encode()
	scopedDSN := parsedURL.String()

	pool, err := database.Connect(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("koneksi skema worker: %v", err)
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
	if err := database.Up(ctx, scopedDSN); err != nil {
		t.Fatalf("migrasi skema: %v", err)
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatalf("siapkan migrasi River: %v", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("migrasi tabel River: %v", err)
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
