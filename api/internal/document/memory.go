package document

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

// ListInvoices meniru query ListInvoicesForBranch: pesanan ber-nomor milik
// satu cabang, satu baris per dokumen invoice (versi apa pun) dan satu baris
// tanpa dokumen bila belum ada, terurut tanggal/nomor/versi menurun.
func (m *Memory) ListInvoices(_ context.Context, branchID string, dari, sampai *time.Time) ([]InvoiceRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]InvoiceRow, 0)
	for _, o := range m.orders {
		if o.BranchID != branchID || strings.TrimSpace(o.Nomor) == "" {
			continue
		}
		if !dalamPeriode(o.Tanggal, dari, sampai) {
			continue
		}
		dasar := InvoiceRow{
			OrderID:    o.ID,
			Nomor:      o.Nomor,
			Status:     o.Status,
			Versi:      o.Versi,
			IsCNC:      o.IsCNC,
			Total:      o.Total,
			JumlahItem: jumlahItemProduk(o.Lines),
		}
		if !o.Tanggal.IsZero() {
			dasar.Tanggal = o.Tanggal.Format(TanggalLayout)
		}
		if o.JatuhTempo != nil && !o.JatuhTempo.IsZero() {
			dasar.JatuhTempo = o.JatuhTempo.Format(TanggalLayout)
		}
		ada := false
		for _, d := range m.docs {
			if d.OrderID != o.ID || d.Jenis != JenisInvoicePDF {
				continue
			}
			ada = true
			versi := d.Versi
			row := dasar
			row.DokumenID = d.ID
			row.VersiDokumen = &versi
			row.UkuranByte = d.UkuranByte
			out = append(out, row)
		}
		if !ada {
			out = append(out, dasar)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Tanggal != out[j].Tanggal {
			return out[i].Tanggal > out[j].Tanggal
		}
		if out[i].Nomor != out[j].Nomor {
			return out[i].Nomor > out[j].Nomor
		}
		return versiDokumen(out[i]) > versiDokumen(out[j])
	})
	return out, nil
}

// dalamPeriode melaporkan apakah tanggal pesanan masuk rentang inklusif.
func dalamPeriode(t time.Time, dari, sampai *time.Time) bool {
	if t.IsZero() {
		return dari == nil && sampai == nil
	}
	if dari != nil && t.Before(*dari) {
		return false
	}
	if sampai != nil && t.After(*sampai) {
		return false
	}
	return true
}

// versiDokumen mengembalikan versi dokumen baris (-1 bila belum ada dokumen).
func versiDokumen(r InvoiceRow) int {
	if r.VersiDokumen == nil {
		return -1
	}
	return *r.VersiDokumen
}

// jumlahItemProduk menjumlahkan qty baris produk (baris packaging tidak
// dihitung) — sama dengan kolom `jumlah_item` daftar pesanan cabang.
func jumlahItemProduk(lines []Line) int64 {
	var total int64
	for _, l := range lines {
		if l.Jenis == JenisBarisProduk {
			total += int64(l.Qty)
		}
	}
	return total
}

// JumlahDokumen mengembalikan banyaknya baris dokumen yang tersimpan.
func (m *Memory) JumlahDokumen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.docs)
}
