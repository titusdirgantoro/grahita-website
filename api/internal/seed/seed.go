// Package seed mengisi data contoh untuk pengembangan lokal.
//
// Aman dijalankan berulang (ON CONFLICT DO NOTHING). Akun pengguna hanya
// dibuat bila SEED_PASSWORD diisi, sehingga tidak ada kredensial bawaan yang
// ikut masuk ke repo (Architecture.md §6).
package seed

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/database"
)

type produk struct {
	kode, nama, jenis, deskripsi string
	harga                        int64
	urutan                       int
}

type cabang struct {
	nama, kepala, email string
	cnc, special, pi    bool
}

// Run mengisi data contoh.
func Run(ctx context.Context, pool *database.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	produks := []produk{
		{"P-01", "Bio Magnetik", "reguler", "Alat terapi bio magnetik untuk klien cabang.", 350000, 1},
		{"P-02", "Terapi Musik Pentatonic (USB)", "reguler", "Rekaman musik terapi aktivasi otak.", 175000, 2},
		{"P-03", "Buku Mengenal Potensi Anak", "reguler", "Buku pendamping hasil psikotes anak untuk orang tua.", 85000, 3},
		{"P-04", "Buku Karier & Minat Remaja", "reguler", "Panduan minat dan bakat untuk siswa SMP-SMA.", 95000, 4},
		{"P-05", "Rimpang Daun", "reguler", "Produk herbal Rimpang Daun.", 65000, 5},
		{"P-06", "Map Laporan Psikotes", "reguler", "Map untuk laporan hasil psikotes.", 12500, 6},
		{"P-07", "Sertifikat Psikotes", "sertifikat", "Dipesan terpisah. Biaya packaging ditambahkan otomatis.", 0, 7},
	}
	for _, p := range produks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO products (kode, nama, jenis, deskripsi, harga_default, urutan)
			VALUES ($1,$2,$3::product_kind,$4,$5,$6)
			ON CONFLICT (kode) DO NOTHING`,
			p.kode, p.nama, p.jenis, p.deskripsi, p.harga, p.urutan); err != nil {
			return fmt.Errorf("seed produk %s: %w", p.kode, err)
		}
	}

	cabangs := []cabang{
		{"Grahita Bandung", "Dra. Sri Wahyuni", "bandung@grahitaindonesia.com", true, false, false},
		{"Grahita Bekasi", "Agus Pratama, S.Psi.", "bekasi@grahitaindonesia.com", false, false, true},
		{"Grahita Yogyakarta", "Bambang Sutrisno", "yogyakarta@grahitaindonesia.com", true, true, false},
		{"Grahita Surabaya", "Indah Permatasari", "surabaya@grahitaindonesia.com", false, false, false},
	}
	for _, c := range cabangs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO branches (nama, nama_kepala, email, is_cnc, is_special, pajak_in)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (email) DO NOTHING`,
			c.nama, c.kepala, c.email, c.cnc, c.special, c.pi); err != nil {
			return fmt.Errorf("seed cabang %s: %w", c.nama, err)
		}
	}

	// Matriks harga contoh: semua produk dijual ke semua cabang dengan harga
	// default. Produk Sertifikat ikut disertakan dengan harga 0 — tanpanya
	// produk Sertifikat tidak punya baris harga cabang, sehingga katalog tidak
	// menampilkannya dan aturan Sertifikat (F1-12) tidak bisa dipakai sama
	// sekali di lingkungan contoh. `harga_default` Sertifikat dijamin 0 oleh
	// constraint `sertifikat_gratis` (PRD CP-SER-01).
	if _, err := tx.Exec(ctx, `
		INSERT INTO branch_prices (branch_id, product_id, harga)
		SELECT b.id, p.id, p.harga_default
		  FROM branches b CROSS JOIN products p
		ON CONFLICT (branch_id, product_id) DO NOTHING`); err != nil {
		return fmt.Errorf("seed harga cabang: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO bank_accounts (bank, nama_pemilik, nomor)
		VALUES ('BCA','PT Grahita Indonesia','1234567890'),
		       ('BRI','PT Grahita Indonesia','0987654321')
		ON CONFLICT (bank, nomor) DO NOTHING`); err != nil {
		return fmt.Errorf("seed rekening: %w", err)
	}

	if err := seedUsers(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	slog.Info("seed selesai")
	return nil
}

// seedUsers membuat akun cabang & admin dari SEED_PASSWORD.
func seedUsers(ctx context.Context, tx pgx.Tx) error {
	password := os.Getenv("SEED_PASSWORD")
	if password == "" {
		slog.Warn("SEED_PASSWORD tidak diisi — akun contoh dilewati")
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash SEED_PASSWORD: %w", err)
	}

	// Admin contoh langsung dianggap AKTIF: mereka sudah punya kata sandi,
	// jadi activated_at terisi. Admin undangan (F1-05) justru kebalikannya —
	// password_hash dan activated_at NULL sampai benar-benar diaktivasi.
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_users (nama, email, password_hash, role, is_active, invited_at, activated_at)
		VALUES ($1,$2,$3,'superadmin',true,now(),now())
		ON CONFLICT (email) DO NOTHING`,
		"Rina Kartika", "rina@grahitaindonesia.com", hash); err != nil {
		return fmt.Errorf("seed admin superadmin: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_users (nama, email, password_hash, role, is_active, invited_at, activated_at)
		VALUES ($1,$2,$3,'manager',true,now(),now())
		ON CONFLICT (email) DO NOTHING`,
		"Budi Santoso", "budi@grahitaindonesia.com", hash); err != nil {
		return fmt.Errorf("seed admin manager: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO branch_users (branch_id, email, password_hash, is_active)
		SELECT id, email, $1, true FROM branches
		ON CONFLICT (email) DO NOTHING`, hash); err != nil {
		return fmt.Errorf("seed akun cabang: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE branches
		SET identity_status='registered', updated_at=now()
		WHERE nama_kepala IS NOT NULL AND email IS NOT NULL
		  AND EXISTS (SELECT 1 FROM branch_users bu WHERE bu.branch_id=branches.id)`); err != nil {
		return fmt.Errorf("seed status identitas cabang: %w", err)
	}
	slog.Info("akun contoh dibuat", "jumlah_admin", 2, "sumber_password", "SEED_PASSWORD")
	return nil
}
