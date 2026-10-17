package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/grahita/portal/api/internal/audit"
)

// Memory adalah implementasi Repository in-memory untuk pengujian dan mode
// demo. Ia juga menyimpan entri audit yang diterimanya sehingga pengujian
// dapat memeriksa bahwa setiap mutasi benar-benar dicatat.
type Memory struct {
	mu       sync.Mutex
	products []Product
	prices   map[[2]string]PriceCell
	branches map[string]Branch
	used     map[string]bool
	audit    []audit.Entry
	seq      int
}

// NewMemory membuat penyimpanan katalog in-memory kosong.
func NewMemory() *Memory {
	return &Memory{
		prices:   map[[2]string]PriceCell{},
		branches: map[string]Branch{},
		used:     map[string]bool{},
	}
}

// PutProduct menambahkan satu produk (untuk pengujian).
func (m *Memory) PutProduct(p Product) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.ID == "" {
		m.seq++
		p.ID = fmt.Sprintf("pr-%d", m.seq)
	}
	if p.Jenis == "" {
		p.Jenis = JenisReguler
	}

	m.products = append(m.products, p)
}

// PutPrice menetapkan harga satu sel matriks tanpa rincian komponen (seperti
// baris harga lama sebelum tarif berversi).
func (m *Memory) PutPrice(branchID, productID string, harga int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prices[[2]string{branchID, productID}] = PriceCell{BranchID: branchID, ProductID: productID, Harga: harga, Sumber: SumberLama}
}

// PutBranch menambahkan data cabang untuk /me/branch.
func (m *Memory) PutBranch(b Branch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.branches[b.ID] = b
}

// PutUsed menandai produk sudah dipakai order line.
func (m *Memory) PutUsed(productID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.used[productID] = true
}

// AuditEntries mengembalikan salinan entri audit yang tercatat.
func (m *Memory) AuditEntries() []audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.Entry, len(m.audit))
	copy(out, m.audit)
	return out
}

// PriceOf membaca harga satu sel matriks (ok=false bila sel kosong).
func (m *Memory) PriceOf(branchID, productID string) (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.prices[[2]string{branchID, productID}]
	return v.Harga, ok
}

// CellOf membaca satu sel matriks beserta komponennya (ok=false bila kosong).
func (m *Memory) CellOf(branchID, productID string) (PriceCell, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.prices[[2]string{branchID, productID}]
	return v, ok
}

// ListProducts mengembalikan seluruh produk.
func (m *Memory) ListProducts(_ context.Context) ([]Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortProducts(m.products), nil
}

// ListActiveProducts mengembalikan produk aktif saja.
func (m *Memory) ListActiveProducts(_ context.Context) ([]Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Product, 0, len(m.products))
	for _, p := range m.products {
		if p.IsActive {
			out = append(out, p)
		}
	}
	return sortProducts(out), nil
}

// ListProductsForBranch mengembalikan produk yang dijual ke satu cabang.
func (m *Memory) ListProductsForBranch(_ context.Context, branchID string) ([]BranchProduct, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BranchProduct, 0)
	for _, p := range m.products {
		if !p.IsActive {
			continue
		}
		sel, ok := m.prices[[2]string{branchID, p.ID}]
		if !ok {
			continue
		}
		out = append(out, BranchProduct{
			ID: p.ID, Nama: p.Nama, Jenis: p.Jenis,
			Deskripsi: p.Deskripsi, Harga: sel.Harga, Urutan: p.Urutan,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Urutan != out[j].Urutan {
			return out[i].Urutan < out[j].Urutan
		}
		return out[i].Nama < out[j].Nama
	})
	return out, nil
}

// GetBranch mengembalikan satu cabang.
func (m *Memory) GetBranch(_ context.Context, id string) (Branch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.branches[id]
	if !ok {
		return Branch{}, ErrNotFound
	}
	return b, nil
}

// ListProductsForAdmin mengembalikan produk sesuai filter admin.
func (m *Memory) ListProductsForAdmin(_ context.Context, f ProductFilter) ([]Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(f.Q)
	out := make([]Product, 0, len(m.products))
	for _, p := range m.products {
		switch {
		case q != "" && !strings.Contains(strings.ToLower(p.Nama), q) && !strings.Contains(strings.ToLower(p.Kode), q):
			continue
		case f.Jenis != "" && p.Jenis != f.Jenis:
			continue
		case f.Active != nil && p.IsActive != *f.Active:
			continue
		}
		out = append(out, p)
	}
	return sortProducts(out), nil
}

// GetProduct mengembalikan satu produk.
func (m *Memory) GetProduct(_ context.Context, id string) (Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.products {
		if p.ID == id {
			return p, nil
		}
	}
	return Product{}, ErrNotFound
}

// CreateProduct menyimpan produk baru beserta auditnya.
func (m *Memory) CreateProduct(_ context.Context, in Product, rec audit.Entry) (Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.cekKode(in.Kode, ""); err != nil {
		return Product{}, err
	}
	m.seq++
	in.ID = fmt.Sprintf("pr-%d", m.seq)
	m.products = append(m.products, in)
	rec.EntityID = in.ID
	rec.After = in
	m.audit = append(m.audit, rec)
	return in, nil
}

// UpdateProduct menyimpan perubahan penuh satu produk beserta auditnya.
func (m *Memory) UpdateProduct(_ context.Context, in Product, rec audit.Entry) (Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.cekKode(in.Kode, in.ID); err != nil {
		return Product{}, err
	}
	for i, p := range m.products {
		if p.ID == in.ID {
			m.products[i] = in
			m.audit = append(m.audit, rec)
			return in, nil
		}
	}
	return Product{}, ErrNotFound
}

// DeleteProduct menghapus produk beserta auditnya.
func (m *Memory) DeleteProduct(_ context.Context, id string, rec audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.products {
		if p.ID != id {
			continue
		}
		m.products = append(m.products[:i], m.products[i+1:]...)
		for key := range m.prices {
			if key[1] == id {
				delete(m.prices, key)
			}
		}
		m.audit = append(m.audit, rec)
		return nil
	}
	return ErrNotFound
}

// ProductKodeTaken melaporkan apakah kode sudah dipakai produk lain.
func (m *Memory) ProductKodeTaken(_ context.Context, kode, exceptID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.products {
		if p.ID != exceptID && p.Kode != "" && p.Kode == kode {
			return true, nil
		}
	}
	return false, nil
}

// ProductUsed melaporkan apakah produk sudah dipakai order line.
func (m *Memory) ProductUsed(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used[id], nil
}

// ListPrices mengembalikan sel matriks yang berisi.
func (m *Memory) ListPrices(_ context.Context, f PriceFilter) ([]PriceCell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PriceCell, 0, len(m.prices))
	for key, sel := range m.prices {
		if f.BranchID != "" && key[0] != f.BranchID {
			continue
		}
		if f.ProductID != "" && key[1] != f.ProductID {
			continue
		}
		out = append(out, sel)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BranchID != out[j].BranchID {
			return out[i].BranchID < out[j].BranchID
		}
		return out[i].ProductID < out[j].ProductID
	})
	return out, nil
}

// ApplyPriceChanges menerapkan perubahan sel dan auditnya. Seperti di
// PostgreSQL, produk yang pernah bertarif berversi menjadi punya riwayat
// sehingga tidak bisa dihapus lagi.
func (m *Memory) ApplyPriceChanges(_ context.Context, changes []PriceChange, _ string, recs []audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range changes {
		m.terapkanSel(c.BranchID, c.ProductID, c.Tarif)
	}
	m.audit = append(m.audit, recs...)
	return nil
}

// CopyPrices menggantikan matriks tiap cabang tujuan dengan tarif sumber.
func (m *Memory) CopyPrices(_ context.Context, fromBranchID string, toBranchIDs []string, _ string, recs []audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sumber := make(map[string]TarifSel)
	for key, sel := range m.prices {
		if key[0] != fromBranchID {
			continue
		}
		t, err := TarifDariSel(sel)
		if err != nil {
			return err
		}
		sumber[key[1]] = t
	}
	for _, target := range toBranchIDs {
		for key := range m.prices {
			if _, ada := sumber[key[1]]; key[0] == target && !ada {
				m.terapkanSel(target, key[1], nil)
			}
		}
		for pid, t := range sumber {
			if sel, ok := m.prices[[2]string{target, pid}]; ok && sel.SamaDengan(t) {
				continue
			}
			m.terapkanSel(target, pid, &t)
		}
	}
	m.audit = append(m.audit, recs...)
	return nil
}

// terapkanSel menulis atau mengosongkan satu sel; pemanggil memegang kunci.
func (m *Memory) terapkanSel(branchID, productID string, t *TarifSel) {
	key := [2]string{branchID, productID}
	if t == nil {
		delete(m.prices, key)
		return
	}
	hjm, p1, spv := t.HJM, t.P1, t.SPV
	m.prices[key] = PriceCell{BranchID: branchID, ProductID: productID, Harga: t.Final, HJM: &hjm, P1: &p1, SPV: &spv, Sumber: SumberManual}
	m.used[productID] = true
}

// cekKode menolak kode duplikat.
func (m *Memory) cekKode(kode, exceptID string) error {
	if kode == "" {
		return nil
	}
	for _, p := range m.products {
		if p.ID != exceptID && p.Kode == kode {
			return ErrKodeTaken
		}
	}
	return nil
}

func sortProducts(in []Product) []Product {
	out := make([]Product, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Urutan != out[j].Urutan {
			return out[i].Urutan < out[j].Urutan
		}
		return out[i].Nama < out[j].Nama
	})
	return out
}
