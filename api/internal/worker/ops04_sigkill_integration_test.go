package worker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/grahita/portal/api/internal/platform/jobs"
	grahitaworker "github.com/grahita/portal/api/internal/worker"
)

// TestOPS04SIGKILLRiverReclaim adalah harness operasional opt-in. Berbeda dari
// uji context cancellation, harness ini menyalakan proses worker OS terpisah,
// menunggu empat jenis pekerjaan benar-benar berstatus running, mengirim
// SIGKILL, lalu menyalakan proses worker baru. River sendiri yang mengubah job
// yatim menjadi retryable dan mengklaimnya lagi.
//
// TEST_DATABASE_URL wajib menunjuk PostgreSQL QA kosong. OPS04_SIGKILL=1
// sengaja diwajibkan karena test membunuh subprocess dan menunggu siklus
// leader/rescuer River sungguhan.
func TestOPS04SIGKILLRiverReclaim(t *testing.T) {
	if os.Getenv("OPS04_SIGKILL_CHILD") != "" {
		t.Skip("parent-only test")
	}
	if os.Getenv("OPS04_SIGKILL") != "1" {
		t.Skip("set OPS04_SIGKILL=1 untuk menjalankan harness SIGKILL")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL wajib diisi")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := ops04Pool(t, ctx, dsn)
	defer pool.Close()
	ops04Migrate(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		CREATE TABLE ops04_attempts (
			kind text NOT NULL,
			attempt integer NOT NULL,
			worker_pid integer NOT NULL,
			started_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (kind, attempt)
		);
		CREATE TABLE ops04_results (
			kind text PRIMARY KEY,
			business_key text NOT NULL,
			payload_sha256 text NOT NULL,
			completed_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("buat tabel bukti OPS-04: %v", err)
	}

	insertClient := ops04Client(t, pool, false)
	ids := make(map[string]int64, 4)
	ops04Insert := func(kind string, args river.JobArgs) {
		res, err := insertClient.Insert(ctx, args, nil)
		if err != nil {
			t.Fatalf("enqueue %s: %v", kind, err)
		}
		ids[kind] = res.Job.ID
	}
	ops04Insert(jobs.KindRenderDokumen, jobs.RenderDokumenArgs{OrderID: "019a31f0-7c00-7000-8000-000000000004"})
	ops04Insert("tempo_daily", grahitaworker.TempoDailyArgs{})
	ops04Insert(jobs.KindReportXLSX, jobs.ReportXLSXArgs{JobID: "ops04-export-01"})
	ops04Insert("email_outbox_pindai", grahitaworker.EmailScanArgs{})

	crashLog := t.TempDir() + "/worker-crash.log"
	crashCmd, crashFile := ops04StartChild(t, dsn, "crash", crashLog)

	// Semua worker harus sudah masuk Work. Ini membedakan SIGKILL saat job
	// aktif dari sekadar membunuh proses yang belum mengklaim apa pun.
	ops04WaitCount(t, ctx, pool, `SELECT count(*) FROM ops04_attempts WHERE attempt=1`, 4, 45*time.Second)
	ops04WaitCount(t, ctx, pool, `SELECT count(*) FROM river_job WHERE state='running'`, 4, 15*time.Second)

	if err := crashCmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL worker pertama: %v", err)
	}
	err := crashCmd.Wait()
	_ = crashFile.Close()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != -1 {
		t.Fatalf("worker pertama harus benar-benar mati oleh signal, dapat %v", err)
	}
	t.Logf("worker pertama PID %d dihentikan SIGKILL saat 4 job running", crashCmd.Process.Pid)

	// Konfigurasi produksi membiarkan job running selama satu jam sebelum
	// rescue. Backdate hanya mempercepat jam uji; state tetap running dan
	// perpindahan state berikutnya tetap dilakukan maintenance rescuer River.
	if tag, err := pool.Exec(ctx, `
		UPDATE river_job
		SET attempted_at = now() - interval '2 hours'
		WHERE state = 'running'`); err != nil || tag.RowsAffected() != 4 {
		t.Fatalf("backdate empat job yatim: rows=%d err=%v", tag.RowsAffected(), err)
	}

	recoverLog := t.TempDir() + "/worker-recovery.log"
	recoverCmd, recoverFile := ops04StartChild(t, dsn, "recover", recoverLog)
	if err := recoverCmd.Wait(); err != nil {
		_ = recoverFile.Close()
		b, _ := os.ReadFile(recoverLog)
		t.Fatalf("worker pemulihan gagal: %v\n%s", err, b)
	}
	_ = recoverFile.Close()

	rows, err := pool.Query(ctx, `
		SELECT j.id, j.kind, j.state, j.attempt,
		       COALESCE((j.metadata->>'river:rescue_count')::int, 0),
		       count(DISTINCT a.attempt), count(DISTINCT r.kind),
		       COALESCE(max(r.business_key),''), COALESCE(max(r.payload_sha256),'')
		FROM river_job j
		LEFT JOIN ops04_attempts a ON a.kind=j.kind
		LEFT JOIN ops04_results r ON r.kind=j.kind
		WHERE j.id = ANY($1::bigint[])
		GROUP BY j.id, j.kind, j.state, j.attempt, j.metadata
		ORDER BY j.kind`, ops04IDs(ids))
	if err != nil {
		t.Fatalf("baca bukti akhir: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id int64
		var kind, state, key, gotSHA string
		var attempt, rescued, attemptRows, resultRows int
		if err := rows.Scan(&id, &kind, &state, &attempt, &rescued, &attemptRows, &resultRows, &key, &gotSHA); err != nil {
			t.Fatal(err)
		}
		wantKey := ops04BusinessKey(kind)
		wantSHA := ops04SHA(kind, wantKey)
		if state != "completed" || attempt != 2 || rescued != 1 || attemptRows != 2 || resultRows != 1 || key != wantKey || gotSHA != wantSHA {
			t.Errorf("hasil %s tidak konsisten: id=%d state=%s attempt=%d rescue=%d attempt_rows=%d result_rows=%d key=%q sha=%q", kind, id, state, attempt, rescued, attemptRows, resultRows, key, gotSHA)
		} else {
			t.Logf("PASS kind=%s id=%d state=%s attempt=%d rescue_count=%d hasil=%d sha256=%s", kind, id, state, attempt, rescued, resultRows, gotSHA)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 4 {
		t.Fatalf("harus memverifikasi 4 jenis job, dapat %d", seen)
	}

	var totalResults, duplicateResults int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*)-count(DISTINCT kind) FROM ops04_results`).Scan(&totalResults, &duplicateResults); err != nil {
		t.Fatal(err)
	}
	if totalResults != 4 || duplicateResults != 0 {
		t.Fatalf("hasil bisnis harus tepat satu per kind: total=%d duplicate=%d", totalResults, duplicateResults)
	}
}

// TestOPS04SIGKILLWorkerProcess hanya dipanggil oleh parent di subprocess.
// Mode crash tidak punya shutdown path; parent wajib menghentikannya SIGKILL.
func TestOPS04SIGKILLWorkerProcess(t *testing.T) {
	mode := os.Getenv("OPS04_SIGKILL_CHILD")
	if mode == "" {
		t.Skip("helper subprocess")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := ops04Pool(t, ctx, dsn)
	defer pool.Close()
	client := ops04Client(t, pool, mode == "crash")
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start River: %v", err)
	}

	if mode == "crash" {
		select {} // hanya SIGKILL dari parent yang boleh mengakhiri proses ini
	}
	if mode != "recover" {
		t.Fatalf("mode helper tidak dikenal: %q", mode)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var completed, results int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE state='completed'`).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops04_results`).Scan(&results); err != nil {
			t.Fatal(err)
		}
		if completed == 4 && results == 4 {
			stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if err := client.Stop(stopCtx); err != nil {
				t.Fatalf("stop River pemulihan: %v", err)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("River tidak menyelesaikan empat job hasil rescue dalam 90 detik")
}

type ops04Runner struct {
	pool  *pgxpool.Pool
	crash bool
}

func (r *ops04Runner) run(ctx context.Context, kind string, attempt int) error {
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO ops04_attempts(kind,attempt,worker_pid)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, kind, attempt, os.Getpid()); err != nil {
		return err
	}
	if r.crash {
		select {} // subprocess dihentikan SIGKILL, bukan context cancellation
	}
	key := ops04BusinessKey(kind)
	sha := ops04SHA(kind, key)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO ops04_results(kind,business_key,payload_sha256)
		VALUES ($1,$2,$3)
		ON CONFLICT (kind) DO UPDATE SET
		  business_key=EXCLUDED.business_key,
		  payload_sha256=EXCLUDED.payload_sha256
		WHERE ops04_results.business_key=EXCLUDED.business_key
		  AND ops04_results.payload_sha256=EXCLUDED.payload_sha256`, kind, key, sha); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type ops04DocsWorker struct {
	river.WorkerDefaults[jobs.RenderDokumenArgs]
	r *ops04Runner
}

func (w *ops04DocsWorker) Work(ctx context.Context, j *river.Job[jobs.RenderDokumenArgs]) error {
	return w.r.run(ctx, j.Kind, j.Attempt)
}

type ops04StatementWorker struct {
	river.WorkerDefaults[grahitaworker.TempoDailyArgs]
	r *ops04Runner
}

func (w *ops04StatementWorker) Work(ctx context.Context, j *river.Job[grahitaworker.TempoDailyArgs]) error {
	return w.r.run(ctx, j.Kind, j.Attempt)
}

type ops04ReportWorker struct {
	river.WorkerDefaults[jobs.ReportXLSXArgs]
	r *ops04Runner
}

func (w *ops04ReportWorker) Work(ctx context.Context, j *river.Job[jobs.ReportXLSXArgs]) error {
	return w.r.run(ctx, j.Kind, j.Attempt)
}

type ops04EmailWorker struct {
	river.WorkerDefaults[grahitaworker.EmailScanArgs]
	r *ops04Runner
}

func (w *ops04EmailWorker) Work(ctx context.Context, j *river.Job[grahitaworker.EmailScanArgs]) error {
	return w.r.run(ctx, j.Kind, j.Attempt)
}

func ops04Client(t *testing.T, pool *pgxpool.Pool, crash bool) *river.Client[pgx.Tx] {
	t.Helper()
	runner := &ops04Runner{pool: pool, crash: crash}
	workers := river.NewWorkers()
	river.AddWorker(workers, &ops04DocsWorker{r: runner})
	river.AddWorker(workers, &ops04StatementWorker{r: runner})
	river.AddWorker(workers, &ops04ReportWorker{r: runner})
	river.AddWorker(workers, &ops04EmailWorker{r: runner})
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:            map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		Workers:           workers,
		JobTimeout:        2 * time.Minute,
		FetchCooldown:     50 * time.Millisecond,
		FetchPollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("buat River client: %v", err)
	}
	return client
}

func ops04Pool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi PostgreSQL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	return pool
}

func ops04Migrate(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("migrasi River: %v", err)
	}
}

func ops04StartChild(t *testing.T, dsn, mode, logPath string) (*exec.Cmd, *os.File) {
	t.Helper()
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOPS04SIGKILLWorkerProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "OPS04_SIGKILL_CHILD="+mode, "TEST_DATABASE_URL="+dsn)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start worker %s: %v", mode, err)
	}
	return cmd, logFile
}

func ops04WaitCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got int
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout menunggu count=%d, terakhir %d; query=%s", want, got, strings.TrimSpace(query))
}

func ops04BusinessKey(kind string) string {
	switch kind {
	case jobs.KindRenderDokumen:
		return "order:019a31f0-7c00-7000-8000-000000000004:documents:v1"
	case "tempo_daily":
		return "statement:2026-10:branch-qa"
	case jobs.KindReportXLSX:
		return "export:ops04-export-01"
	case "email_outbox_pindai":
		return "held-email:ops04-email-01"
	default:
		return "unknown:" + kind
	}
}

func ops04SHA(kind, key string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|ops04-v1", kind, key)))
	return hex.EncodeToString(sum[:])
}

func ops04IDs(ids map[string]int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}
