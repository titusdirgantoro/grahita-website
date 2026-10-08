// Package seed mengisi data contoh untuk development.
// Datanya mengikuti prototype (7 produk, beberapa cabang, 2 rekening).
// Aman dijalankan berulang: memakai ON CONFLICT DO NOTHING.
package seed

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/grahita/portal/api/internal/platform/db"
)

type produk struct {
	kode, nama, jenis, satuan, deskripsi string
	harga                                int64
	urutan                               int
}

type cabang struct {
	nama, kepala, email         string
	cnc, special, pi            bool
}

func Run(ctx context.Context, pool *db.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	produks := []produk{
		{"P-01", "Bio Magnetik", "reguler", "pcs", "Alat terapi bio magnetik untuk klien cabang.", 350000, 1},
		{"P-02", "Terapi Musik Pentatonic (USB)", "reguler", "pcs", "Rekaman musik terapi aktivasi otak.", 175000, 2},
		{"P-03", "Buku Mengenal Potensi Anak", "reguler", "eks", "Buku pendamping hasil psikotes anak untuk orang tua.", 85000, 3},
		{"P-04", "Buku Karier & Minat Remaja", "reguler", "eks", "Panduan minat dan bakat untuk siswa SMP-SMA.", 95000, 4},
		{"P-05", "Rimpang Daun", "reguler", "pak", "Produk herbal Rimpang Daun.", 65000, 5},
		{"P-06", "Map Laporan Psikotes", "reguler", "pcs", "Map untuk laporan hasil psikotes.", 12500, 6},
		{"P-07", "Sertifikat Psikotes", "sertifikat", "lembar", "Dipesan terpisah. Biaya packaging ditambahkan otomatis.", 0, 7},
	}
	for _, p := range produks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO products (kode, nama, jenis, satuan, deskripsi, harga_default, urutan)
			VALUES ($1,$2,$3::product_kind,$4,$5,$6,$7)
			ON CONFLICT (kode) DO NOTHING`,
			p.kode, p.nama, p.jenis, p.satuan, p.deskripsi, p.harga, p.urutan); err != nil {
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

	// Harga per cabang: mulai dari harga default, cabang "special" dapat diskon 5%.
	if _, err := tx.Exec(ctx, `
		INSERT INTO branch_prices (branch_id, product_id, harga)
		SELECT b.id, p.id,
		       CASE WHEN b.is_special THEN (p.harga_default * 95) / 100 ELSE p.harga_default END
		  FROM branches b CROSS JOIN products p
		 WHERE p.is_active
		ON CONFLICT (branch_id, product_id) DO NOTHING`); err != nil {
		return fmt.Errorf("seed harga: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO bank_accounts (bank, nama_pemilik, nomor) VALUES
			('BCA','PT Grahita Indonesia','5271039988'),
			('BRI','PT Grahita Indonesia','038201000456307')
		ON CONFLICT (bank, nomor) DO NOTHING`); err != nil {
		return fmt.Errorf("seed rekening: %w", err)
	}

	// Admin awal tanpa password: diaktifkan lewat alur undangan nanti.
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_users (nama, email, role, notif_invoice, notif_surat_jalan, invited_at)
		VALUES ('Superadmin Grahita','admin@grahitaindonesia.com','superadmin',true,true,now())
		ON CONFLICT (email) DO NOTHING`); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	slog.Info("data contoh siap",
		"produk", len(produks), "cabang", len(cabangs), "rekening", 2, "admin", 1)
	return nil
}
