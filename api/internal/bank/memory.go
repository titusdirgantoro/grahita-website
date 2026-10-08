package bank

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/grahita/portal/api/internal/audit"
)

// Memory adalah implementasi Repository in-memory untuk pengujian. Ia juga
// menyimpan entri audit yang diterimanya sehingga pengujian dapat memeriksa
// bahwa setiap mutasi benar-benar dicatat.
type Memory struct {
	mu    sync.Mutex
	rows  []Account
	audit []audit.Entry
	seq   int
}

// NewMemory membuat penyimpanan rekening in-memory kosong.
func NewMemory() *Memory { return &Memory{} }

// Put menambahkan satu rekening (untuk pengujian).
func (m *Memory) Put(a Account) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.ID == "" {
		m.seq++
		a.ID = fmt.Sprintf("bk-%d", m.seq)
	}
	m.rows = append(m.rows, a)
}

// AuditEntries mengembalikan salinan entri audit yang tercatat.
func (m *Memory) AuditEntries() []audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.Entry, len(m.audit))
	copy(out, m.audit)
	return out
}

// List mengembalikan seluruh rekening, diurutkan per bank lalu nomor.
func (m *Memory) List(context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Account, len(m.rows))
	copy(out, m.rows)
	sortAccounts(out)
	return out, nil
}

// ListActive mengembalikan hanya rekening aktif.
func (m *Memory) ListActive(context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Account, 0, len(m.rows))
	for _, a := range m.rows {
		if a.IsActive {
			out = append(out, a)
		}
	}
	sortAccounts(out)
	return out, nil
}

// Get mengembalikan satu rekening.
func (m *Memory) Get(_ context.Context, id string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.rows {
		if a.ID == id {
			return a, nil
		}
	}
	return Account{}, ErrNotFound
}

// Create menyimpan rekening baru beserta entri auditnya.
func (m *Memory) Create(_ context.Context, in Account, rec audit.Entry) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.duplicate(in, "") {
		return Account{}, ErrDuplicate
	}
	m.seq++
	in.ID = fmt.Sprintf("bk-%d", m.seq)
	m.rows = append(m.rows, in)
	rec.EntityID = in.ID
	rec.After = in
	m.audit = append(m.audit, rec)
	return in, nil
}

// Update menyimpan perubahan penuh satu rekening beserta entri auditnya.
func (m *Memory) Update(_ context.Context, next Account, rec audit.Entry) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.rows {
		if a.ID != next.ID {
			continue
		}
		if m.duplicate(next, next.ID) {
			return Account{}, ErrDuplicate
		}
		m.rows[i] = next
		m.audit = append(m.audit, rec)
		return next, nil
	}
	return Account{}, ErrNotFound
}

// duplicate melaporkan apakah (bank, nomor) sudah dipakai rekening lain.
func (m *Memory) duplicate(a Account, exceptID string) bool {
	for _, lain := range m.rows {
		if lain.ID != exceptID && lain.Bank == a.Bank && lain.Nomor == a.Nomor {
			return true
		}
	}
	return false
}

func sortAccounts(rows []Account) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Bank != rows[j].Bank {
			return rows[i].Bank < rows[j].Bank
		}
		return rows[i].Nomor < rows[j].Nomor
	})
}
