package branch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/grahita/portal/api/internal/audit"
)

// Memory adalah implementasi Repository in-memory untuk pengujian dan mode
// demo. Ia juga menyimpan entri audit yang diterimanya sehingga pengujian
// dapat memeriksa bahwa setiap mutasi benar-benar dicatat.
type Memory struct {
	mu       sync.Mutex
	rows     []Branch
	accounts map[string]bool
	orders   map[string]bool
	admins   map[string]bool
	tokens   map[string]memoryToken
	audit    []audit.Entry
	seq      int
}

type memoryToken struct {
	hash      []byte
	prefix    string
	expiresAt time.Time
}

// NewMemory membuat penyimpanan cabang in-memory kosong.
func NewMemory() *Memory {
	return &Memory{
		accounts: map[string]bool{},
		orders:   map[string]bool{},
		admins:   map[string]bool{},
		tokens:   map[string]memoryToken{},
	}
}

// Put menambahkan satu cabang (untuk pengujian).
func (m *Memory) Put(b Branch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.ID == "" {
		m.seq++
		b.ID = fmt.Sprintf("br-%d", m.seq)
	}
	m.rows = append(m.rows, b)
}

// PutAccount menandai cabang sudah punya akun.
func (m *Memory) PutAccount(branchID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[branchID] = true
}

// PutOrder menandai cabang sudah punya pesanan.
func (m *Memory) PutOrder(branchID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orders[branchID] = true
}

// PutAdminEmail menandai email sudah dipakai akun admin.
func (m *Memory) PutAdminEmail(email string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.admins[NormalizeEmail(email)] = true
}

// AuditEntries mengembalikan salinan entri audit yang tercatat.
func (m *Memory) AuditEntries() []audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.Entry, len(m.audit))
	copy(out, m.audit)
	return out
}

// ActiveToken mengembalikan hash token registrasi yang sedang berlaku untuk
// satu cabang (untuk memeriksa bahwa yang tersimpan hanya hash-nya).
func (m *Memory) ActiveToken(branchID string) (hash []byte, prefix string, expiresAt time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[branchID]
	return t.hash, t.prefix, t.expiresAt, ok
}

// List mengembalikan cabang sesuai filter, diurutkan per nama.
func (m *Memory) List(_ context.Context, f Filter) ([]Branch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(f.Q)
	out := make([]Branch, 0, len(m.rows))
	for _, b := range m.rows {
		switch {
		case q != "" && !strings.Contains(strings.ToLower(b.Nama), q) && !strings.Contains(strings.ToLower(b.Email), q):
			continue
		case f.Active != nil && b.IsActive != *f.Active:
			continue
		case f.CNC != nil && b.IsCNC != *f.CNC:
			continue
		case f.Special != nil && b.IsSpecial != *f.Special:
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nama < out[j].Nama })
	return out, nil
}

// Get mengembalikan satu cabang.
func (m *Memory) Get(_ context.Context, id string) (Branch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.rows {
		if b.ID == id {
			return b, nil
		}
	}
	return Branch{}, ErrNotFound
}

// Create menyimpan cabang baru beserta entri auditnya.
func (m *Memory) Create(_ context.Context, in Branch, rec audit.Entry) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.rows {
		if NormalizeEmail(b.Email) == NormalizeEmail(in.Email) {
			return "", ErrEmailTaken
		}
	}
	m.seq++
	in.ID = fmt.Sprintf("br-%d", m.seq)
	m.rows = append(m.rows, in)
	rec.EntityID = in.ID
	// Snapshot "after" memakai cabang yang baru disimpan (lengkap dengan id).
	rec.After = in
	m.audit = append(m.audit, rec)
	return in.ID, nil
}

// Update menyimpan perubahan penuh satu cabang beserta entri auditnya.
func (m *Memory) Update(_ context.Context, next Branch, rec audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, b := range m.rows {
		if b.ID != next.ID {
			continue
		}
		for _, lain := range m.rows {
			if lain.ID != next.ID && NormalizeEmail(lain.Email) == NormalizeEmail(next.Email) {
				return ErrEmailTaken
			}
		}
		next.TokenPrefix = b.TokenPrefix
		next.TokenExpiresAt = b.TokenExpiresAt
		m.rows[i] = next
		m.audit = append(m.audit, rec)
		return nil
	}
	return ErrNotFound
}

// Delete menghapus cabang beserta token/akunnya dan mencatat auditnya.
func (m *Memory) Delete(_ context.Context, id string, rec audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, b := range m.rows {
		if b.ID != id {
			continue
		}
		m.rows = append(m.rows[:i], m.rows[i+1:]...)
		delete(m.tokens, id)
		delete(m.accounts, id)
		m.audit = append(m.audit, rec)
		return nil
	}
	return ErrNotFound
}

// IssueToken menggantikan token aktif cabang dengan token baru.
func (m *Memory) IssueToken(_ context.Context, branchID string, tokenHash []byte, prefix string, expiresAt time.Time, rec audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[branchID] = memoryToken{hash: tokenHash, prefix: prefix, expiresAt: expiresAt}
	for i, b := range m.rows {
		if b.ID == branchID {
			m.rows[i].TokenPrefix = prefix
			m.rows[i].TokenExpiresAt = &expiresAt
		}
	}
	m.audit = append(m.audit, rec)
	return nil
}

// HasOrders melaporkan apakah cabang sudah punya pesanan.
func (m *Memory) HasOrders(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orders[id], nil
}

// HasAccount melaporkan apakah cabang sudah punya akun.
func (m *Memory) HasAccount(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accounts[id], nil
}

// EmailTaken melaporkan apakah email sudah dipakai cabang lain.
func (m *Memory) EmailTaken(_ context.Context, email, exceptID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.rows {
		if b.ID != exceptID && NormalizeEmail(b.Email) == NormalizeEmail(email) {
			return true, nil
		}
	}
	return false, nil
}

// AdminEmailTaken melaporkan apakah email sudah dipakai akun admin.
func (m *Memory) AdminEmailTaken(_ context.Context, email string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.admins[NormalizeEmail(email)], nil
}
