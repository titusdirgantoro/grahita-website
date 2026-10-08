package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Memory adalah repository bukti in-memory untuk pengujian dan mode lokal
// tanpa PostgreSQL. Perilaku yang penting tetap ditiru: id berbentuk UUID,
// unggahan ganda (order_id + sha256) ditolak ErrDuplicate, dan pencarian
// selalu dibatasi order_id.
type Memory struct {
	mu     sync.Mutex
	proofs map[string]Proof
	urutan []string
}

// NewMemory membuat repository bukti in-memory.
func NewMemory() *Memory {
	return &Memory{proofs: map[string]Proof{}}
}

// CountByOrder menghitung bukti tersimpan pada satu pesanan.
func (m *Memory) CountByOrder(_ context.Context, orderID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, p := range m.proofs {
		if p.OrderID == orderID {
			n++
		}
	}
	return n, nil
}

// Add menyimpan metadata satu bukti dan meniru indeks unik (order_id, sha256).
func (m *Memory) Add(_ context.Context, p Proof) (Proof, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ada := range m.proofs {
		if ada.OrderID == p.OrderID && ada.Sha256 == p.Sha256 {
			return Proof{}, ErrDuplicate
		}
	}
	if p.ID == "" {
		p.ID = uuidAcak()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	m.proofs[p.ID] = p
	m.urutan = append(m.urutan, p.ID)
	return p, nil
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
