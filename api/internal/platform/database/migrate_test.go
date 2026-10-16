package database_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/database"
)

// TestMigrasiBersih menguji "migrasi bersih" yang diwajibkan CI:
// database kosong -> seluruh migrasi naik -> seluruh migrasi turun -> naik lagi.
//
// Uji ini dijalankan bila TEST_DATABASE_URL diisi. Di CI nilainya menunjuk
// service container postgres:18.
func TestMigrasiBersih(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi migrasi dilewati")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Bersihkan sisa run sebelumnya (boleh gagal bila memang masih kosong).
	if err := database.DownTo(ctx, url, 0); err != nil {
		t.Logf("pembersihan awal (diabaikan): %v", err)
	}

	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi up pada database bersih gagal: %v", err)
	}

	versi, err := database.Version(ctx, url)
	if err != nil {
		t.Fatalf("baca versi migrasi: %v", err)
	}
	if versi < 2 {
		t.Fatalf("versi migrasi harus minimal 2, dapat %d", versi)
	}

	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("koneksi pool: %v", err)
	}
	defer pool.Close()

	// Tabel inti harus ada.
	for _, tabel := range []string{
		"branches", "branch_users", "admin_users", "sessions", "products",
		"branch_prices", "orders", "order_lines", "settings", "numbering_counters",
	} {
		var ada bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                 WHERE table_schema = 'public' AND table_name = $1)`, tabel).Scan(&ada)
		if err != nil {
			t.Fatalf("cek tabel %s: %v", tabel, err)
		}
		if !ada {
			t.Errorf("tabel %s tidak ada setelah migrasi", tabel)
		}
	}
	// Fase expand 0008 mempertahankan alias satuan dan index token lama supaya
	// binary pra-0008 masih dapat berjalan selama rollout campuran.
	for tabel, kolom := range map[string]string{"products": "satuan", "order_lines": "satuan_snapshot"} {
		var ada bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM information_schema.columns
 WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, tabel, kolom).Scan(&ada); err != nil {
			t.Fatalf("cek alias transisi %s.%s: %v", tabel, kolom, err)
		}
		if !ada {
			t.Errorf("alias transisi %s.%s harus tetap ada sampai migrasi contract", tabel, kolom)
		}
	}
	var definisiIndex string
	if err := pool.QueryRow(ctx, `SELECT pg_get_indexdef(indexrelid)
  FROM pg_index WHERE indexrelid='registration_tokens_one_active'::regclass`).Scan(&definisiIndex); err != nil {
		t.Fatalf("baca index token kompatibel: %v", err)
	}
	if definisiIndex == "" {
		t.Error("index registration_tokens_one_active harus tetap ada")
	}

	// Pengaturan default dari PRD §4.9 harus terisi.
	var jumlah int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM settings`).Scan(&jumlah); err != nil {
		t.Fatalf("hitung settings: %v", err)
	}
	if jumlah < 9 {
		t.Errorf("pengaturan default harus minimal 9 kunci, dapat %d", jumlah)
	}

	// Fungsi penomoran harus ada dan tidak bolong saat rollback.
	if err := ujiPenomoran(ctx, t, pool); err != nil {
		t.Fatalf("penomoran: %v", err)
	}

	// Turunkan seluruh migrasi.
	if err := database.DownTo(ctx, url, 0); err != nil {
		t.Fatalf("migrasi down gagal: %v", err)
	}

	var sisa int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = 'public' AND table_name = 'orders'`).Scan(&sisa); err != nil {
		t.Fatalf("cek tabel setelah down: %v", err)
	}
	if sisa != 0 {
		t.Errorf("tabel orders harus hilang setelah migrasi down")
	}

	// Naikkan kembali: membuktikan migrasi idempoten dan bisa dijalankan ulang.
	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi up kedua gagal: %v", err)
	}
}

// TestRollbackIdentitasTidakMenghidupkanToken memastikan binary pra-0008 tidak
// akan melihat kembali token yang sudah dicabut setelah kolom revoked_at hilang.
func TestRollbackIdentitasTidakMenghidupkanToken(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi migrasi dilewati")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := database.DownTo(ctx, url, 0); err != nil {
		t.Logf("pembersihan awal (diabaikan): %v", err)
	}
	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi up gagal: %v", err)
	}
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("koneksi pool: %v", err)
	}
	defer pool.Close()

	var branchID, userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO branches (nama, nama_kepala, email, identity_status)
		 VALUES ('Cabang rollback', 'Kepala', 'rollback@example.test', 'registered') RETURNING id`).Scan(&branchID); err != nil {
		t.Fatalf("seed cabang: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO branch_users (branch_id, email, password_hash)
		 VALUES ($1, 'rollback@example.test', 'hash') RETURNING id`, branchID).Scan(&userID); err != nil {
		t.Fatalf("seed akun cabang: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO registration_tokens (branch_id, token_hash, token_prefix, expires_at, used_at, revoked_at)
		VALUES ($1, decode('01','hex'), 'REVOKED1', now() + interval '1 day', now(), now()),
		       ($1, decode('02','hex'), 'ACTIVE02', now() + interval '1 day', NULL, NULL)`, branchID); err != nil {
		t.Fatalf("seed token registrasi: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO password_reset_tokens (actor_kind, user_id, token_hash, expires_at, revoked_at)
		VALUES ('branch', $1, decode('03','hex'), now() + interval '1 day', now()),
		       ('branch', $1, decode('04','hex'), now() + interval '1 day', NULL)`, userID); err != nil {
		t.Fatalf("seed token reset: %v", err)
	}

	if err := database.DownTo(ctx, url, 7); err != nil {
		t.Fatalf("rollback 0008 gagal: %v", err)
	}
	for tabel, mau := range map[string]int{"registration_tokens": 1, "password_reset_tokens": 1} {
		var dapat int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+tabel).Scan(&dapat); err != nil {
			t.Fatalf("hitung %s: %v", tabel, err)
		}
		if dapat != mau {
			t.Errorf("%s setelah rollback = %d, ingin %d token aktif saja", tabel, dapat, mau)
		}
	}
	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi up ulang gagal: %v", err)
	}
}

// ujiPenomoran memastikan counter tidak membocorkan nomor saat rollback
// (Database.md §6, SystemDesign.md §5.2).
func ujiPenomoran(ctx context.Context, t *testing.T, pool *database.Pool) error {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	var pertama, kedua int
	if err := tx.QueryRow(ctx, `SELECT next_numbers('invoice', '2026-10', 2)`).Scan(&pertama); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT next_numbers('invoice', '2026-10', 2)`).Scan(&kedua); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if kedua != pertama+2 {
		t.Errorf("nomor harus berurutan: %d lalu %d", pertama, kedua)
	}
	if err := tx.Rollback(ctx); err != nil {
		return err
	}

	// Setelah rollback, counter kembali seperti semula sehingga nomor tidak
	// pernah terbuang percuma.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	var lagi int
	if err := tx2.QueryRow(ctx, `SELECT next_numbers('invoice', '2026-10', 1)`).Scan(&lagi); err != nil {
		return err
	}
	if lagi != pertama {
		t.Errorf("setelah rollback nomor harus mulai dari %d, dapat %d", pertama, lagi)
	}
	return tx2.Rollback(ctx)
}
