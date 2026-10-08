package order

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Memory adalah implementasi Repository in-memory untuk pengujian.
//
// Ia meniru perilaku penting repository PostgreSQL: pesanan difilter dengan
// branch_id, hanya draft yang bisa diubah/dihapus, total dan jumlah item
// dihitung dari barisnya (bukan dari klien), dan draft selalu tanpa nomor,
// tanggal, atau serial number.
type Memory struct {
	mu     sync.Mutex
	orders []Order
	seq    int
}

// NewMemory membuat penyimpanan pesanan in-memory kosong.
func NewMemory() *Memory { return &Memory{} }

// Put menyisipkan pesanan yang sudah ada (untuk menguji isolasi cabang,
// pesanan yang terkunci, dan pesanan yang sudah dikirim). ID dibuat otomatis
// bila kosong dan total jumlah item dihitung dari barisnya.
func (m *Memory) Put(o Order) Order {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.putLocked(o)
}

func (m *Memory) putLocked(o Order) Order {
	if o.ID == "" {
		o.ID = m.newID()
	}
	if o.Status == "" {
		o.Status = StatusDraft
	}
	if o.VerifStatus == "" {
		o.VerifStatus = "belum"
	}
	if o.CreatedAt.IsZero() {
		now := time.Now().UTC()
		o.CreatedAt, o.UpdatedAt = now, now
	}
	o.Total, o.JumlahItem = hitung(o.Lines)
	// is_sertifikat diturunkan dari isi baris, sama seperti trigger
	// order_lines_recalc di PostgreSQL — bukan disetel dari luar.
	o.IsSertifikat = adaPackaging(o.Lines)
	m.orders = append(m.orders, o)
	return o
}

// newID menerbitkan id berbentuk UUID (seperti uuidv7 di PostgreSQL) supaya
// pengujian in-memory melewati pemeriksaan format id di service.
func (m *Memory) newID() string {
	m.seq++
	return fmt.Sprintf("11111111-1111-4111-8111-%012x", m.seq)
}

// Count mengembalikan jumlah pesanan tersimpan (dipakai pengujian).
func (m *Memory) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.orders)
}

// Lines mengembalikan baris satu pesanan (dipakai pengujian).
func (m *Memory) Lines(id string) []Line {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.orders {
		if o.ID == id {
			out := make([]Line, len(o.Lines))
			copy(out, o.Lines)
			return out
		}
	}
	return nil
}

// List mengembalikan pesanan satu cabang, terbaru lebih dahulu.
func (m *Memory) List(_ context.Context, branchID, status string) ([]Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Order, 0, len(m.orders))
	for _, o := range m.orders {
		if o.BranchID != branchID {
			continue
		}
		if status != "" && o.Status != status {
			continue
		}
		out = append(out, m.publik(o))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return kunciUrut(out[i]).After(kunciUrut(out[j]))
	})
	return out, nil
}

// Get mengembalikan satu pesanan milik cabang tersebut.
func (m *Memory) Get(_ context.Context, id, branchID string) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.cari(id, branchID)
	if idx < 0 {
		return Order{}, ErrNotFound
	}
	return m.publik(m.orders[idx]), nil
}

// CreateDraft menyimpan draft baru.
func (m *Memory) CreateDraft(_ context.Context, branchID, createdBy, catatan string, lines []Line) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.putLocked(Order{
		BranchID:  branchID,
		Status:    StatusDraft,
		Catatan:   catatan,
		CreatedBy: createdBy,
		Lines:     lines,
	})
	return m.publik(o), nil
}

// UpdateDraft mengganti catatan dan seluruh baris draft.
func (m *Memory) UpdateDraft(_ context.Context, id, branchID, catatan string, lines []Line) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.cari(id, branchID)
	if idx < 0 || m.orders[idx].Status != StatusDraft {
		return Order{}, ErrNotFound
	}
	m.orders[idx].Catatan = catatan
	m.orders[idx].Lines = lines
	m.orders[idx].Total, m.orders[idx].JumlahItem = hitung(lines)
	m.orders[idx].IsSertifikat = adaPackaging(lines)
	m.orders[idx].UpdatedAt = time.Now().UTC()
	return m.publik(m.orders[idx]), nil
}

// DeleteDraft menghapus draft milik cabang tersebut.
func (m *Memory) DeleteDraft(_ context.Context, id, branchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.cari(id, branchID)
	if idx < 0 || m.orders[idx].Status != StatusDraft {
		return ErrNotFound
	}
	m.orders = append(m.orders[:idx], m.orders[idx+1:]...)
	return nil
}

// cari mengembalikan indeks pesanan id milik branchID (-1 bila tidak ada).
func (m *Memory) cari(id, branchID string) int {
	for i, o := range m.orders {
		if o.ID == id && o.BranchID == branchID {
			return i
		}
	}
	return -1
}

// publik mengembalikan salinan dengan barisnya agar pemanggil tidak bisa
// mengubah isi penyimpanan.
func (m *Memory) publik(o Order) Order {
	out := o
	out.Lines = make([]Line, len(o.Lines))
	copy(out.Lines, o.Lines)
	return out
}

// hitung mengembalikan total dan jumlah item dari baris pesanan.
func hitung(lines []Line) (int64, int64) {
	var total, item int64
	for _, l := range lines {
		total += int64(l.Qty) * l.HargaSatuan
		if l.Jenis == JenisProduct {
			item += int64(l.Qty)
		}
	}
	return total, item
}

// adaPackaging melaporkan apakah baris pesanan memuat baris biaya packaging.
// Baris packaging hanya pernah lahir untuk pesanan Sertifikat (CP-SER-02),
// jadi keberadaannya menjadi penanda is_sertifikat yang sama dengan yang
// dihitung trigger order_lines_recalc dari products.jenis di PostgreSQL.
func adaPackaging(lines []Line) bool {
	for _, l := range lines {
		if l.Jenis == JenisPackaging {
			return true
		}
	}
	return false
}

// kunciUrut adalah waktu pengurutan daftar: tanggal submit bila ada (draft
// tidak punya tanggal), selain itu created_at — sama seperti query PostgreSQL.
func kunciUrut(o Order) time.Time {
	if o.Tanggal != "" {
		if t, err := time.Parse(TanggalLayout, o.Tanggal); err == nil {
			return t
		}
	}
	return o.CreatedAt
}
