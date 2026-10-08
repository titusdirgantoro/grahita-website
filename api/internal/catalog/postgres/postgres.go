// Package postgres adalah implementasi repository katalog di PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository katalog berbasis sqlc.
type Repo struct {
	q *sqlc.Queries
}

// New membuat repository katalog.
func New(pool *database.Pool) *Repo { return &Repo{q: sqlc.New(pool)} }

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
