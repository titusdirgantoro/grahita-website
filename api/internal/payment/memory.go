package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// EventTercatat adalah satu peristiwa timeline yang dicatat Memory; dipakai
// pengujian untuk memeriksa apa yang akan ditulis ke order_events.
type EventTercatat struct {
	OrderID string
	Kind    string
	Nama    string
	ActorID string
}

// Memory adalah repository bukti in-memory untuk pengujian dan mode lokal
// tanpa PostgreSQL. Perilaku yang penting tetap ditiru: id berbentuk UUID,
// unggahan ganda (order_id + sha256) ditolak ErrDuplicate, batas jumlah
// ditegakkan, dan pencarian selalu dibatasi order_id.
//
// CommitProofs menahan mutex selama satu permintaan penuh sehingga sifat
// atomik dan aman-balapan repository nyata (transaksi + kunci baris pesanan)
// tetap terwakili: unggahan paralel tidak bisa melampaui batas.
type Memory struct {
	mu     sync.Mutex
	proofs map[string]Proof
	urutan []string
	events []EventTercatat
}

// NewMemory membuat repository bukti in-memory.
func NewMemory() *Memory {
	return &Memory{proofs: map[string]Proof{}}
}

// Sha256ByOrder mengembalikan sidik jari seluruh bukti satu pesanan.
func (m *Memory) Sha256ByOrder(_ context.Context, orderID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.urutan))
	for _, id := range m.urutan {
		if p := m.proofs[id]; p.OrderID == orderID {
			out = append(out, p.Sha256)
		}
	}
	return out, nil
}

// CommitProofs menyimpan seluruh metadata + peristiwa timeline satu permintaan
// sebagai satu unit: bila ada baris yang ditolak (duplikat atau melebihi
// MaxFile), tidak satu pun tersimpan.
func (m *Memory) CommitProofs(_ context.Context, req CommitRequest) ([]Proof, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var ada int64
	for _, p := range m.proofs {
		if p.OrderID == req.OrderID {
			ada++
		}
	}
	if ada+int64(len(req.Proofs)) > req.MaxFile {
		return nil, fmt.Errorf("%w: maksimal %d berkas per pesanan (tersimpan %d)",
			ErrTooMany, req.MaxFile, ada)
	}

	// Validasi seluruh baris lebih dulu, baru diterapkan — meniru transaksi.
	siap := make([]Proof, 0, len(req.Proofs))
	terlihat := map[string]bool{}
	for i, p := range req.Proofs {
		if terlihat[p.Sha256] {
			return nil, fmt.Errorf("berkas ke-%d: %w", i+1, ErrDuplicate)
		}
		terlihat[p.Sha256] = true
		for _, l := range m.proofs {
			if l.OrderID == p.OrderID && l.Sha256 == p.Sha256 {
				return nil, fmt.Errorf("berkas ke-%d: %w", i+1, ErrDuplicate)
			}
		}
		if p.ID == "" {
			p.ID = uuidAcak()
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		siap = append(siap, p)
	}

	out := make([]Proof, 0, len(siap))
	for _, p := range siap {
		m.proofs[p.ID] = p
		m.urutan = append(m.urutan, p.ID)
		out = append(out, p)
	}
	for _, e := range req.Events {
		m.events = append(m.events, EventTercatat{OrderID: req.OrderID, Kind: e.Kind, Nama: e.Nama, ActorID: e.ActorID})
	}
	return out, nil
}

// Events mengembalikan peristiwa timeline yang tercatat (urut penulisan).
func (m *Memory) Events() []EventTercatat {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]EventTercatat(nil), m.events...)
}

// ListByOrder mengembalikan bukti satu pesanan, terlama dulu.
func (m *Memory) ListByOrder(_ context.Context, orderID string) ([]Proof, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Proof, 0, len(m.urutan))
	for _, id := range m.urutan {
		if p := m.proofs[id]; p.OrderID == orderID {
			out = append(out, p)
		}
	}
	return out, nil
}

// Get mengembalikan satu bukti MILIK pesanan tersebut.
func (m *Memory) Get(_ context.Context, id, orderID string) (Proof, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.proofs[id]
	if !ok || p.OrderID != orderID {
		return Proof{}, ErrNotFound
	}
	return p, nil
}

// MemoryOrderSource adalah OrderSource in-memory untuk pengujian.
type MemoryOrderSource struct {
	mu     sync.Mutex
	orders map[string]OrderRef
}

// NewMemoryOrderSource membuat OrderSource in-memory.
func NewMemoryOrderSource() *MemoryOrderSource {
	return &MemoryOrderSource{orders: map[string]OrderRef{}}
}

// Put menambahkan/mengganti konteks satu pesanan.
func (s *MemoryOrderSource) Put(ref OrderRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[ref.ID] = ref
}

// OrderForProof mengembalikan konteks pesanan; id tak dikenal = ErrNotFound.
func (s *MemoryOrderSource) OrderForProof(_ context.Context, orderID string) (OrderRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.orders[orderID]
	if !ok {
		return OrderRef{}, ErrNotFound
	}
	return ref, nil
}

// uuidAcak menghasilkan id berbentuk UUID v4 untuk repository in-memory.
func uuidAcak() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Tidak mungkin terjadi pada sistem yang punya /dev/urandom; kalau
		// terjadi, id waktu tetap lebih baik daripada id kosong.
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1e12)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
