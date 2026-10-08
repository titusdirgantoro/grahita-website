package document

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Memory adalah repository dokumen in-memory untuk pengujian dan pemakaian
// lokal tanpa PostgreSQL. Perilakunya meniru kontrak PostgreSQL: SaveAll
// tidak pernah menimpa baris yang sudah ada (unique order_id+jenis+versi) dan
// mengembalikan baris finalnya.
type Memory struct {
	mu     sync.Mutex
	orders map[string]Order
	docs   []Document
	seq    int
	now    func() time.Time

	// SaveErr memaksa SaveAll gagal (dipakai menguji pembersihan objek).
	SaveErr error
	// OrderErr memaksa OrderForDocument gagal selain tidak ditemukan.
	OrderErr error
	// GagalUntuk memaksa SaveAll gagal hanya untuk pesanan tertentu.
	GagalUntuk string
}

// NewMemory membuat repository dokumen in-memory yang kosong.
func NewMemory() *Memory {
	return &Memory{orders: map[string]Order{}, now: time.Now}
}

// SetOrder menaruh satu snapshot pesanan supaya bisa dirender.
func (m *Memory) SetOrder(o Order) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orders[o.ID] = o
}

// Order mengembalikan snapshot pesanan yang tersimpan.
func (m *Memory) Order(orderID string) (Order, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[orderID]
	return o, ok
}

// OrderForDocument mengembalikan snapshot pesanan beserta baris dan
// rekeningnya.
func (m *Memory) OrderForDocument(_ context.Context, orderID string) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.OrderErr != nil {
		return Order{}, m.OrderErr
	}
	o, ok := m.orders[orderID]
	if !ok {
		return Order{}, ErrNotFound
	}
	return o, nil
}

// ListByOrder mengembalikan dokumen satu pesanan, urut versi lalu jenis.
func (m *Memory) ListByOrder(_ context.Context, orderID string) ([]Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Document, 0, len(m.docs))
	for _, d := range m.docs {
		if d.OrderID == orderID {
			out = append(out, d)
		}
	}
	return out, nil
}

// GetByID mengembalikan satu dokumen beserta pemiliknya.
func (m *Memory) GetByID(_ context.Context, id string) (Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.docs {
		if d.ID == id {
			return d, nil
		}
	}
	return Document{}, ErrNotFound
}

// SaveAll menyimpan seluruh baris satu permintaan dalam satu langkah: bila ada
// yang gagal, tidak ada baris yang tersimpan (meniru transaksi PostgreSQL).
func (m *Memory) SaveAll(_ context.Context, req SaveRequest) ([]Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SaveErr != nil {
		return nil, m.SaveErr
	}
	if m.GagalUntuk != "" && m.GagalUntuk == req.OrderID {
		return nil, fmt.Errorf("gagal menyimpan dokumen (uji): pesanan %s", req.OrderID)
	}

	out := make([]Document, 0, len(req.Docs))
	for _, d := range req.Docs {
		ada := false
		for _, lama := range m.docs {
			if lama.OrderID == d.OrderID && lama.Jenis == d.Jenis && lama.Versi == d.Versi {
				out = append(out, lama)
				ada = true
				break
			}
		}
		if ada {
			continue
		}
		m.seq++
		d.ID = idMemory(m.seq)
		d.CreatedAt = m.now().UTC()
		m.docs = append(m.docs, d)
		out = append(out, d)
	}
	return out, nil
}

// TambahDokumen menaruh satu baris dokumen secara langsung (dipakai pengujian
// untuk meniru data lama).
func (m *Memory) TambahDokumen(d Document) Document {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	d.ID = idMemory(m.seq)
	d.CreatedAt = m.now().UTC()
	m.docs = append(m.docs, d)
	return d
}

// idMemory menyusun id berbentuk UUID (seperti uuidv7 di database) supaya
// pengujian menempuh jalur pemeriksaan format id yang sama dengan produksi.
func idMemory(n int) string {
	return fmt.Sprintf("11111111-2222-3333-4444-%012d", n)
}

// JumlahDokumen mengembalikan banyaknya baris dokumen yang tersimpan.
func (m *Memory) JumlahDokumen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.docs)
}
