// Package catalog menyediakan katalog produk (harga khusus cabang) dan
// master cabang untuk kedua portal.
package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/grahita/portal/api/internal/auth"
)

// ErrNotFound dikembalikan bila data tidak ada.
var ErrNotFound = errors.New("data tidak ditemukan")

// ErrForbidden dikembalikan bila aktor tidak berhak melihat data tersebut.
var ErrForbidden = errors.New("tidak berhak mengakses data ini")

// ErrNoBranch dikembalikan bila akun cabang tidak terhubung ke cabang mana pun.
var ErrNoBranch = errors.New("akun tidak terhubung ke cabang")

// Product adalah produk pada katalog admin (harga default).
type Product struct {
	ID           string `json:"id"`
	Kode         string `json:"kode,omitempty"`
	Nama         string `json:"nama"`
	Jenis        string `json:"jenis"`
	Satuan       string `json:"satuan"`
	Deskripsi    string `json:"deskripsi,omitempty"`
	HargaDefault int64  `json:"harga_default"`
	Urutan       int    `json:"urutan"`
	IsActive     bool   `json:"is_active"`
}

// BranchProduct adalah produk yang benar-benar dijual ke satu cabang beserta
// harga khususnya. Produk tanpa baris harga tidak pernah muncul di sini
// (PRD CP-KAT-02).
type BranchProduct struct {
	ID        string `json:"id"`
	Nama      string `json:"nama"`
	Jenis     string `json:"jenis"`
	Satuan    string `json:"satuan"`
	Deskripsi string `json:"deskripsi,omitempty"`
	Harga     int64  `json:"harga"`
	Urutan    int    `json:"urutan"`
}

// Branch adalah data cabang (master data).
type Branch struct {
	ID         string `json:"id"`
	Nama       string `json:"nama"`
	NamaKepala string `json:"nama_kepala"`
	Email      string `json:"email"`
	Telepon    string `json:"telepon,omitempty"`
	IsCNC      bool   `json:"is_cnc"`
	IsSpecial  bool   `json:"is_special"`
	PajakIn    bool   `json:"pajak_in"`
	IsActive   bool   `json:"is_active"`
}

// Repository memuat data katalog.
type Repository interface {
	ListProducts(ctx context.Context) ([]Product, error)
	ListActiveProducts(ctx context.Context) ([]Product, error)
	ListProductsForBranch(ctx context.Context, branchID string) ([]BranchProduct, error)
	ListBranches(ctx context.Context) ([]Branch, error)
	GetBranch(ctx context.Context, id string) (Branch, error)
}

// Service adalah logika bisnis katalog.
type Service struct {
	repo Repository
}

// NewService membuat service katalog.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Products mengembalikan katalog sesuai sudut pandang aktor:
// cabang hanya melihat produk yang dijual ke dirinya, admin melihat semua
// produk aktif dengan harga default.
func (s *Service) Products(ctx context.Context, actor auth.Actor) (any, error) {
	switch {
	case actor.IsBranch():
		if actor.BranchID == "" {
			return nil, ErrNoBranch
		}
		rows, err := s.repo.ListProductsForBranch(ctx, actor.BranchID)
		if err != nil {
			return nil, fmt.Errorf("katalog cabang: %w", err)
		}
		return rows, nil
	case actor.IsAdmin():
		rows, err := s.repo.ListActiveProducts(ctx)
		if err != nil {
			return nil, fmt.Errorf("katalog admin: %w", err)
		}
		return rows, nil
	default:
		return nil, ErrForbidden
	}
}

// Branches mengembalikan seluruh cabang — hanya untuk staf Pusat.
func (s *Service) Branches(ctx context.Context, actor auth.Actor) ([]Branch, error) {
	if !actor.Can(auth.CapAdminBranches) {
		return nil, ErrForbidden
	}
	rows, err := s.repo.ListBranches(ctx)
	if err != nil {
		return nil, fmt.Errorf("daftar cabang: %w", err)
	}
	return rows, nil
}

// Branch mengembalikan satu cabang. Kepemilikan cabang diperiksa di sini
// lewat Actor (bukan dari parameter klien): akun cabang hanya boleh membaca
// cabangnya sendiri (isolasi data cabang, Architecture.md §6).
func (s *Service) Branch(ctx context.Context, actor auth.Actor, id string) (Branch, error) {
	if err := actor.RequireBranch(id); err != nil {
		return Branch{}, err
	}
	row, err := s.repo.GetBranch(ctx, id)
	if err != nil {
		return Branch{}, err
	}
	return row, nil
}
