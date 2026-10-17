// Package postgres adalah implementasi repository dashboard admin di
// PostgreSQL (F1-20).
//
// Paket ini READ-ONLY: tidak ada satu pun query di sini yang menulis. Sumber
// angkanya adalah tabel `orders`/`branches` milik domain pesanan dan daftar id
// pesanan ber-email gagal yang DISERAHKAN domain notification lewat parameter
// `email_gagal_ids` (domain dashboard tidak pernah membaca `email_outbox`).
//
// Ekspresi tanggal bisnis
//
//	COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date)
//
// ditulis persis sama dengan db/queries/admin_order.sql supaya angka dashboard
// dan daftar terfilter yang dibuka dari chip tidak pernah berbeda.
package postgres

import (
	"context"
	"fmt"

	"github.com/grahita/portal/api/internal/dashboard"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository dashboard berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository dashboard.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

var _ dashboard.Repository = (*Repo)(nil)

// Ringkasan mengembalikan jumlah + nilai pesanan periode terpilih beserta
// ringkasan status pembayarannya (AP-DSH-05).
func (r *Repo) Ringkasan(ctx context.Context, q dashboard.Query) (dashboard.HitunganPeriode, error) {
	row, err := r.q.DashboardRingkasan(ctx, sqlc.DashboardRingkasanParams{
		BranchID: pgtypes.UUID(q.BranchID),
		Dari:     q.Dari,
		Sampai:   q.Sampai,
	})
	if err != nil {
		return dashboard.HitunganPeriode{}, fmt.Errorf("query DashboardRingkasan: %w", err)
	}
	return dashboard.HitunganPeriode{
		Jumlah:              row.Jumlah,
		Nilai:               row.Nilai,
		TerverifikasiJumlah: row.TerverifikasiJumlah,
		TerverifikasiNilai:  row.TerverifikasiNilai,
		MenungguJumlah:      row.MenungguJumlah,
		MenungguNilai:       row.MenungguNilai,
		PerluReviewJumlah:   row.PerluReviewJumlah,
		PerluReviewNilai:    row.PerluReviewNilai,
	}, nil
}

// Chip menghitung empat chip "perlu tindakan" (AP-DSH-03) dengan klausa yang
// sama dengan filter daftar riwayat pemesanan (F1-15).
func (r *Repo) Chip(ctx context.Context, q dashboard.Query) (dashboard.HitunganChip, error) {
	row, err := r.q.DashboardChip(ctx, sqlc.DashboardChipParams{
		HariIni:       q.HariIni,
		EmailGagalIds: q.EmailGagalIDs,
		BranchID:      pgtypes.UUID(q.BranchID),
	})
	if err != nil {
		return dashboard.HitunganChip{}, fmt.Errorf("query DashboardChip: %w", err)
	}
	return dashboard.HitunganChip{
		Menunggu:        row.Menunggu,
		PerluReview:     row.PerluReview,
		LewatJatuhTempo: row.LewatJatuhTempo,
		EmailGagal:      row.EmailGagal,
	}, nil
}

// TrenBulanan membaca agregat per bulan pada tahun bisnis berjalan; bulan tanpa
// pesanan tidak dikembalikan (service mengisinya dengan nol).
func (r *Repo) TrenBulanan(ctx context.Context, q dashboard.Query) ([]dashboard.Titik, error) {
	rows, err := r.q.DashboardTrenBulanan(ctx, sqlc.DashboardTrenBulananParams{
		BranchID: pgtypes.UUID(q.BranchID),
		Dari:     q.TahunAwal,
		Sampai:   q.TahunAkhir,
	})
	if err != nil {
		return nil, fmt.Errorf("query DashboardTrenBulanan: %w", err)
	}
	out := make([]dashboard.Titik, 0, len(rows))
	for _, row := range rows {
		out = append(out, dashboard.Titik{Kunci: row.Kunci, Jumlah: row.Jumlah, Nilai: row.Nilai, Terverifikasi: row.Terverifikasi})
	}
	return out, nil
}

// Harian membaca agregat per hari pada bulan bisnis berjalan; hari tanpa
// pesanan tidak dikembalikan (service mengisinya dengan nol).
func (r *Repo) Harian(ctx context.Context, q dashboard.Query) ([]dashboard.Titik, error) {
	rows, err := r.q.DashboardHarian(ctx, sqlc.DashboardHarianParams{
		BranchID: pgtypes.UUID(q.BranchID),
		Dari:     q.BulanAwal,
		Sampai:   q.BulanAkhir,
	})
	if err != nil {
		return nil, fmt.Errorf("query DashboardHarian: %w", err)
	}
	out := make([]dashboard.Titik, 0, len(rows))
	for _, row := range rows {
		out = append(out, dashboard.Titik{Kunci: row.Kunci, Jumlah: row.Jumlah, Nilai: row.Nilai, Terverifikasi: row.Terverifikasi})
	}
	return out, nil
}

// PeringkatCabang membaca peringkat cabang periode terpilih (AP-DSH-06),
// dibatasi MaxPeringkatCabang baris di database.
func (r *Repo) PeringkatCabang(ctx context.Context, q dashboard.Query) ([]dashboard.BarisPeringkat, error) {
	rows, err := r.q.DashboardPeringkatCabang(ctx, sqlc.DashboardPeringkatCabangParams{
		BranchID: pgtypes.UUID(q.BranchID),
		Dari:     q.Dari,
		Sampai:   q.Sampai,
		Batas:    int32(dashboard.MaxPeringkatCabang),
	})
	if err != nil {
		return nil, fmt.Errorf("query DashboardPeringkatCabang: %w", err)
	}
	out := make([]dashboard.BarisPeringkat, 0, len(rows))
	for _, row := range rows {
		out = append(out, dashboard.BarisPeringkat{
			BranchID: row.BranchID,
			Nama:     row.Nama,
			Jumlah:   row.Jumlah,
			Nilai:    row.Nilai,
		})
	}
	return out, nil
}

// Item menjumlahkan qty baris produk pesanan pada rentang query.
func (r *Repo) Item(ctx context.Context, q dashboard.Query) (int64, error) {
	item, err := r.q.DashboardItem(ctx, sqlc.DashboardItemParams{
		BranchID: pgtypes.UUID(q.BranchID),
		Dari:     q.Dari,
		Sampai:   q.Sampai,
	})
	if err != nil {
		return 0, fmt.Errorf("query DashboardItem: %w", err)
	}
	return item, nil
}

// Komposisi membaca komposisi pesanan periode terpilih.
func (r *Repo) Komposisi(ctx context.Context, q dashboard.Query) (dashboard.HitunganKomposisi, error) {
	row, err := r.q.DashboardKomposisi(ctx, sqlc.DashboardKomposisiParams{
		HariIni:  q.HariIni,
		BranchID: pgtypes.UUID(q.BranchID),
		Dari:     q.Dari,
		Sampai:   q.Sampai,
	})
	if err != nil {
		return dashboard.HitunganKomposisi{}, fmt.Errorf("query DashboardKomposisi: %w", err)
	}
	return dashboard.HitunganKomposisi{
		Reguler: row.Reguler, CNC: row.Cnc, Sertifikat: row.Sertifikat, CNCLewatTempo: row.CncLewatTempo,
	}, nil
}

// ProdukTerlaris membaca product terlaris periode terpilih, dibatasi
// MaxProdukTerlaris baris di database.
func (r *Repo) ProdukTerlaris(ctx context.Context, q dashboard.Query) ([]dashboard.BarisProduk, error) {
	rows, err := r.q.DashboardProdukTerlaris(ctx, sqlc.DashboardProdukTerlarisParams{
		BranchID: pgtypes.UUID(q.BranchID),
		Dari:     q.Dari,
		Sampai:   q.Sampai,
		Batas:    int32(dashboard.MaxProdukTerlaris),
	})
	if err != nil {
		return nil, fmt.Errorf("query DashboardProdukTerlaris: %w", err)
	}
	out := make([]dashboard.BarisProduk, 0, len(rows))
	for _, row := range rows {
		out = append(out, dashboard.BarisProduk{ProductID: row.ProductID, Nama: row.Nama, Qty: row.Qty})
	}
	return out, nil
}
