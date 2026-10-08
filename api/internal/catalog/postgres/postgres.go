// Package postgres adalah implementasi repository katalog di PostgreSQL.
//
// Setiap metode yang mengubah data menuliskan entri audit di dalam transaksi
// yang sama, sehingga perubahan dan catatannya selalu sejalan
// (SystemDesign.md §5.4).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository katalog berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository katalog.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// ListProducts memuat seluruh produk (termasuk nonaktif) untuk admin.
func (r *Repo) ListProducts(ctx context.Context) ([]catalog.Product, error) {
	rows, err := r.q.ListProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListProducts: %w", err)
	}
	out := make([]catalog.Product, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.Product{
			ID: row.ID, Kode: pgtypes.TextOr(row.Kode), Nama: row.Nama, Jenis: row.Jenis,
			Satuan: row.Satuan, Deskripsi: row.Deskripsi, HargaDefault: row.HargaDefault,
			Urutan: int(row.Urutan), IsActive: row.IsActive,
		})
	}
	return out, nil
}

// ListActiveProducts memuat produk aktif untuk katalog admin.
func (r *Repo) ListActiveProducts(ctx context.Context) ([]catalog.Product, error) {
	rows, err := r.q.ListActiveProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListActiveProducts: %w", err)
	}
	out := make([]catalog.Product, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.Product{
			ID: row.ID, Kode: pgtypes.TextOr(row.Kode), Nama: row.Nama, Jenis: row.Jenis,
			Satuan: row.Satuan, Deskripsi: row.Deskripsi, HargaDefault: row.HargaDefault,
			Urutan: int(row.Urutan), IsActive: row.IsActive,
		})
	}
	return out, nil
}

// ListProductsForBranch memuat produk yang dijual ke satu cabang.
func (r *Repo) ListProductsForBranch(ctx context.Context, branchID string) ([]catalog.BranchProduct, error) {
	rows, err := r.q.ListProductsForBranch(ctx, branchID)
	if err != nil {
		return nil, fmt.Errorf("query ListProductsForBranch: %w", err)
	}
	out := make([]catalog.BranchProduct, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.BranchProduct{
			ID: row.ID, Nama: row.Nama, Jenis: row.Jenis, Satuan: row.Satuan,
			Deskripsi: row.Deskripsi, Harga: row.Harga, Urutan: int(row.Urutan),
		})
	}
	return out, nil
}

// GetBranch memuat satu cabang.
func (r *Repo) GetBranch(ctx context.Context, id string) (catalog.Branch, error) {
	row, err := r.q.GetBranch(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Branch{}, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Branch{}, fmt.Errorf("query GetBranch: %w", err)
	}
	return catalog.Branch{
		ID: row.ID, Nama: row.Nama, NamaKepala: row.NamaKepala, Email: row.Email,
		Telepon: pgtypes.TextOr(row.Telepon), IsCNC: row.IsCnc, IsSpecial: row.IsSpecial,
		PajakIn: row.PajakIn, IsActive: row.IsActive,
	}, nil
}

// ListProductsForAdmin memuat produk untuk halaman katalog admin.
func (r *Repo) ListProductsForAdmin(ctx context.Context, f catalog.ProductFilter) ([]catalog.Product, error) {
	arg := sqlc.ListProductsForAdminParams{Aktif: pgtypes.BoolPtr(f.Active)}
	if f.Q != "" {
		arg.Q = pgtypes.Text("%" + f.Q + "%")
	}
	if f.Jenis != "" {
		arg.Jenis = sqlc.NullProductKind{ProductKind: sqlc.ProductKind(f.Jenis), Valid: true}
	}
	rows, err := r.q.ListProductsForAdmin(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("query ListProductsForAdmin: %w", err)
	}
	out := make([]catalog.Product, 0, len(rows))
	for _, row := range rows {
		out = append(out, productFromAdmin(
			row.ID, pgtypes.TextOr(row.Kode), row.Nama, row.Jenis, row.Satuan,
			row.Deskripsi, row.HargaDefault, int(row.Urutan), row.IsActive))
	}
	return out, nil
}

// GetProduct memuat satu produk untuk admin.
func (r *Repo) GetProduct(ctx context.Context, id string) (catalog.Product, error) {
	row, err := r.q.GetProductForAdmin(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Product{}, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Product{}, fmt.Errorf("query GetProductForAdmin: %w", err)
	}
	return productFromAdmin(
		row.ID, pgtypes.TextOr(row.Kode), row.Nama, row.Jenis, row.Satuan,
		row.Deskripsi, row.HargaDefault, int(row.Urutan), row.IsActive), nil
}

// CreateProduct menyimpan produk baru beserta catatan auditnya.
func (r *Repo) CreateProduct(ctx context.Context, in catalog.Product, rec audit.Entry) (catalog.Product, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return catalog.Product{}, fmt.Errorf("mulai transaksi buat produk: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := r.q.WithTx(tx).CreateProduct(ctx, sqlc.CreateProductParams{
		Kode:         pgtypes.Text(in.Kode),
		Nama:         in.Nama,
		Jenis:        sqlc.ProductKind(in.Jenis),
		Satuan:       in.Satuan,
		Deskripsi:    pgtypes.Text(in.Deskripsi),
		HargaDefault: in.HargaDefault,
		Urutan:       int32(in.Urutan),
		IsActive:     in.IsActive,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return catalog.Product{}, catalog.ErrKodeTaken
		}
		return catalog.Product{}, fmt.Errorf("query CreateProduct: %w", err)
	}
	saved := productFromAdmin(
		row.ID, pgtypes.TextOr(row.Kode), row.Nama, row.Jenis, row.Satuan,
		row.Deskripsi, row.HargaDefault, int(row.Urutan), row.IsActive)
	rec.EntityID = saved.ID
	rec.After = saved
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return catalog.Product{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Product{}, fmt.Errorf("commit buat produk: %w", err)
	}
	return saved, nil
}

// UpdateProduct menyimpan perubahan penuh satu produk beserta catatan auditnya.
func (r *Repo) UpdateProduct(ctx context.Context, in catalog.Product, rec audit.Entry) (catalog.Product, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return catalog.Product{}, fmt.Errorf("mulai transaksi ubah produk: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := r.q.WithTx(tx).UpdateProduct(ctx, sqlc.UpdateProductParams{
		Kode:         pgtypes.Text(in.Kode),
		Nama:         in.Nama,
		Jenis:        sqlc.ProductKind(in.Jenis),
		Satuan:       in.Satuan,
		Deskripsi:    pgtypes.Text(in.Deskripsi),
		HargaDefault: in.HargaDefault,
		Urutan:       int32(in.Urutan),
		IsActive:     in.IsActive,
		ID:           in.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Product{}, catalog.ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return catalog.Product{}, catalog.ErrKodeTaken
		}
		return catalog.Product{}, fmt.Errorf("query UpdateProduct: %w", err)
	}
	saved := productFromAdmin(
		row.ID, pgtypes.TextOr(row.Kode), row.Nama, row.Jenis, row.Satuan,
		row.Deskripsi, row.HargaDefault, int(row.Urutan), row.IsActive)
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return catalog.Product{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Product{}, fmt.Errorf("commit ubah produk: %w", err)
	}
	return saved, nil
}

// DeleteProduct menghapus produk beserta catatan auditnya. Pemanggil sudah
// memastikan produk ini belum dipakai order line.
func (r *Repo) DeleteProduct(ctx context.Context, id string, rec audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi hapus produk: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("hapus produk: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return catalog.ErrNotFound
	}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit hapus produk: %w", err)
	}
	return nil
}

// ProductKodeTaken melaporkan apakah kode sudah dipakai produk lain.
func (r *Repo) ProductKodeTaken(ctx context.Context, kode, exceptID string) (bool, error) {
	ada, err := r.q.ProductKodeTaken(ctx, sqlc.ProductKodeTakenParams{
		Kode:     pgtypes.Text(kode),
		ExceptID: pgtypes.UUID(exceptID),
	})
	if err != nil {
		return false, fmt.Errorf("query ProductKodeTaken: %w", err)
	}
	return ada, nil
}

// ProductUsed melaporkan apakah produk sudah dipakai order line.
func (r *Repo) ProductUsed(ctx context.Context, id string) (bool, error) {
	ada, err := r.q.ProductHasOrderLines(ctx, pgtypes.UUID(id))
	if err != nil {
		return false, fmt.Errorf("query ProductHasOrderLines: %w", err)
	}
	return ada, nil
}

// ListPrices memuat sel matriks harga yang berisi.
func (r *Repo) ListPrices(ctx context.Context, f catalog.PriceFilter) ([]catalog.PriceCell, error) {
	rows, err := r.q.ListBranchPrices(ctx, sqlc.ListBranchPricesParams{
		BranchID:  pgtypes.UUID(f.BranchID),
		ProductID: pgtypes.UUID(f.ProductID),
	})
	if err != nil {
		return nil, fmt.Errorf("query ListBranchPrices: %w", err)
	}
	out := make([]catalog.PriceCell, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.PriceCell{BranchID: row.BranchID, ProductID: row.ProductID, Harga: row.Harga})
	}
	return out, nil
}

// ApplyPriceChanges menerapkan seluruh perubahan sel dan entri auditnya dalam
// satu transaksi.
func (r *Repo) ApplyPriceChanges(ctx context.Context, changes []catalog.PriceChange, actorID string, recs []audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi harga: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	for _, c := range changes {
		if c.Harga == nil {
			if err := q.DeleteBranchPrice(ctx, sqlc.DeleteBranchPriceParams{
				BranchID: c.BranchID, ProductID: c.ProductID,
			}); err != nil {
				return fmt.Errorf("query DeleteBranchPrice: %w", err)
			}
			continue
		}
		if err := q.UpsertBranchPrice(ctx, sqlc.UpsertBranchPriceParams{
			BranchID:  c.BranchID,
			ProductID: c.ProductID,
			Harga:     *c.Harga,
			UpdatedBy: pgtypes.UUID(actorID),
		}); err != nil {
			return fmt.Errorf("query UpsertBranchPrice: %w", err)
		}
	}
	for _, rec := range recs {
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit harga: %w", err)
	}
	return nil
}

// CopyPrices menggantikan seluruh matriks harga setiap cabang tujuan dengan
// matriks cabang sumber, beserta satu entri audit per cabang tujuan.
//
// Sumber dibaca di dalam transaksi yang sama supaya hasil salinnya konsisten
// walau ada perubahan harga bersamaan.
func (r *Repo) CopyPrices(ctx context.Context, fromBranchID string, toBranchIDs []string, actorID string, recs []audit.Entry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi salin harga: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	sumber, err := q.ListBranchPrices(ctx, sqlc.ListBranchPricesParams{
		BranchID: pgtypes.UUID(fromBranchID),
	})
	if err != nil {
		return fmt.Errorf("query ListBranchPrices sumber: %w", err)
	}
	for _, target := range toBranchIDs {
		if err := q.DeleteBranchPricesOfBranch(ctx, target); err != nil {
			return fmt.Errorf("query DeleteBranchPricesOfBranch: %w", err)
		}
		for _, row := range sumber {
			if err := q.UpsertBranchPrice(ctx, sqlc.UpsertBranchPriceParams{
				BranchID:  target,
				ProductID: row.ProductID,
				Harga:     row.Harga,
				UpdatedBy: pgtypes.UUID(actorID),
			}); err != nil {
				return fmt.Errorf("query UpsertBranchPrice salin: %w", err)
			}
		}
	}
	for _, rec := range recs {
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit salin harga: %w", err)
	}
	return nil
}

// productFromAdmin menyusun Product dari kolom hasil query admin.
func productFromAdmin(id, kode, nama, jenis, satuan, deskripsi string, harga int64, urutan int, aktif bool) catalog.Product {
	return catalog.Product{
		ID: id, Kode: kode, Nama: nama, Jenis: jenis, Satuan: satuan,
		Deskripsi: deskripsi, HargaDefault: harga, Urutan: urutan, IsActive: aktif,
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
