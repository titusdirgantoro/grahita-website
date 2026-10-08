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
