package users

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Memory adalah implementasi Repository in-memory untuk pengujian dan mode
// demo. Aturan "superadmin aktif terakhir" ditegakkan di sini dengan mutex
// tunggal sehingga perilaku permintaan bersamaan bisa diuji tanpa database.
type Memory struct {
	mu       sync.Mutex
	branches []BranchUser
	admins   map[string]AdminUser
	urut     []string // urutan id admin untuk daftar yang stabil
	seq      int

	// OnAdminCreated dipanggil setiap admin baru dibuat. Dipakai pengujian
	// untuk mencerminkan akun yang sama ke auth.Memory (alur aktivasi).
	OnAdminCreated func(AdminUser)

	// recs adalah entri audit yang tercatat (F2-28). Implementasi produksi
	// menulisnya ke tabel audit_logs; penyimpanan in-memory menyimpannya di
	// sini supaya pengujian domain bisa memeriksa "tepat satu event per
	// mutasi" tanpa database.
	recs []audit.Entry
}

// AuditEntries mengembalikan salinan entri audit yang tercatat.
func (m *Memory) AuditEntries() []audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.Entry, len(m.recs))
	copy(out, m.recs)
	return out
}

// catatAudit menambahkan entri audit yang perlu ditulis. Pemanggil sudah
// memegang mutex.
func (m *Memory) catatAudit(recs []audit.Entry) {
	m.recs = append(m.recs, recs...)
}

// lengkapiAudit mengisi EntityID/Before/After entri audit dari baris yang
// benar-benar berubah. Service hanya menyusun kerangkanya (aktor, IP, entitas,
// aksi) supaya snapshot selalu berasal dari keadaan data — bukan dari nilai
// yang dikirim klien.
func lengkapiAudit(recs []audit.Entry, entityID string, before, after any) {
	for i := range recs {
		recs[i].EntityID = entityID
		recs[i].Before = audit.Redact(before)
		recs[i].After = audit.Redact(after)
	}
}

// NewMemory membuat penyimpanan pengguna in-memory kosong.
func NewMemory() *Memory {
	return &Memory{admins: map[string]AdminUser{}}
}

// PutBranch menambahkan satu baris akun cabang (untuk pengujian).
func (m *Memory) PutBranch(b BranchUser) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b.HasAccount = b.UserID != ""
	b.Status = BranchStatus(b.HasAccount, b.IsActive, b.BranchActive)
	m.branches = append(m.branches, b)
}

// PutAdmin menambahkan satu akun admin (untuk pengujian).
func (m *Memory) PutAdmin(a AdminUser) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.ID == "" {
		m.seq++
		a.ID = fmt.Sprintf("adm-%d", m.seq)
	}
	a.Status = AdminStatus(a.IsActive, a.ActivatedAt != nil)
	m.admins[a.ID] = a
	m.urut = append(m.urut, a.ID)
}

// ListBranchUsers mengembalikan seluruh cabang beserta status akunnya.
func (m *Memory) ListBranchUsers(context.Context) ([]BranchUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BranchUser, 0, len(m.branches))
	for _, b := range m.branches {
		b.HasAccount = b.UserID != ""
		b.Status = BranchStatus(b.HasAccount, b.IsActive, b.BranchActive)
		out = append(out, b)
	}
	return out, nil
}

// BranchUserByID mencari akun cabang berdasarkan id akun.
func (m *Memory) BranchUserByID(_ context.Context, id string) (BranchUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.branches {
		if b.UserID != "" && b.UserID == id {
			b.HasAccount = true
			b.Status = BranchStatus(true, b.IsActive, b.BranchActive)
			return b, nil
		}
	}
	return BranchUser{}, ErrNotFound
}

// SetBranchUserActive mengubah status aktif satu akun cabang. Entri audit yang
// diberikan dilengkapi before/after dari baris yang benar-benar berubah.
func (m *Memory) SetBranchUserActive(_ context.Context, id string, active bool, recs []audit.Entry) (BranchUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, b := range m.branches {
		if b.UserID != "" && b.UserID == id {
			lama := b
			lama.HasAccount = true
			lama.Status = BranchStatus(true, lama.IsActive, lama.BranchActive)
			m.branches[i].IsActive = active
			b = m.branches[i]
			b.HasAccount = true
			b.Status = BranchStatus(true, active, b.BranchActive)
			lengkapiAudit(recs, b.UserID, BranchUserSnapshot(lama), BranchUserSnapshot(b))
			m.catatAudit(recs)
			return b, nil
		}
	}
	return BranchUser{}, ErrNotFound
}

// ListAdmins mengembalikan seluruh admin.
func (m *Memory) ListAdmins(context.Context) ([]AdminUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AdminUser, 0, len(m.admins))
	for _, id := range m.urut {
		a, ok := m.admins[id]
		if !ok {
			continue
		}
		a.Status = AdminStatus(a.IsActive, a.ActivatedAt != nil)
		out = append(out, a)
	}
	return out, nil
}

// AdminByID mencari admin berdasarkan id.
func (m *Memory) AdminByID(_ context.Context, id string) (AdminUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.admins[id]
	if !ok {
		return AdminUser{}, ErrNotFound
	}
	a.Status = AdminStatus(a.IsActive, a.ActivatedAt != nil)
	return a, nil
}

// AdminByEmail mencari admin berdasarkan email.
func (m *Memory) AdminByEmail(_ context.Context, email string) (AdminUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.admins {
		if NormalizeEmail(a.Email) == NormalizeEmail(email) {
			a.Status = AdminStatus(a.IsActive, a.ActivatedAt != nil)
			return a, nil
		}
	}
	return AdminUser{}, ErrNotFound
}

// CreateAdmin membuat undangan admin (belum diaktivasi). Entri audit yang
// diberikan dilengkapi id akun + snapshotnya.
func (m *Memory) CreateAdmin(_ context.Context, in NewAdmin, recs []audit.Entry) (AdminUser, error) {
	m.mu.Lock()
	for _, a := range m.admins {
		if NormalizeEmail(a.Email) == in.Email {
			m.mu.Unlock()
			return AdminUser{}, ErrEmailTaken
		}
	}
	m.seq++
	now := time.Now().UTC()
	a := AdminUser{
		ID:              fmt.Sprintf("adm-%d", m.seq),
		Nama:            in.Nama,
		Email:           in.Email,
		Role:            in.Role,
		NotifInvoice:    in.NotifInvoice,
		NotifSuratJalan: in.NotifSuratJalan,
		IsActive:        true,
		InvitedAt:       &now,
	}
	a.Status = AdminStatus(a.IsActive, false)
	m.admins[a.ID] = a
	m.urut = append(m.urut, a.ID)
	lengkapiAudit(recs, a.ID, nil, AdminSnapshot(a))
	m.catatAudit(recs)
	hook := m.OnAdminCreated
	m.mu.Unlock()

	if hook != nil {
		hook(a)
	}
	return a, nil
}

// UpdateAdmin menyimpan perubahan admin sambil menegakkan larangan menurunkan
// atau menonaktifkan superadmin aktif terakhir. Penolakan (ErrLastSuperadmin)
// tidak menulis apa pun dan tidak meninggalkan entri audit.
func (m *Memory) UpdateAdmin(_ context.Context, next AdminUser, recs []audit.Entry) (AdminUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.admins[next.ID]
	if !ok {
		return AdminUser{}, ErrNotFound
	}
	turun := cur.Role == auth.RoleSuperadmin && cur.IsActive &&
		(next.Role != auth.RoleSuperadmin || !next.IsActive)
	if turun && !m.adaSuperadminAktifLain(cur.ID) {
		return AdminUser{}, ErrLastSuperadmin
	}
	next.Email = cur.Email
	next.InvitedAt = cur.InvitedAt
	next.ActivatedAt = cur.ActivatedAt
	next.LastLoginAt = cur.LastLoginAt
	next.Status = AdminStatus(next.IsActive, next.ActivatedAt != nil)
	m.admins[next.ID] = next
	lengkapiAudit(recs, next.ID, AdminSnapshot(cur), AdminSnapshot(next))
	m.catatAudit(recs)
	return next, nil
}

// adaSuperadminAktifLain melaporkan apakah masih ada superadmin yang BENAR-BENAR
// bisa login (aktif DAN sudah diaktivasi) selain id. Superadmin yang masih
// berstatus undangan TIDAK dihitung: ia sendiri tidak bisa masuk, jadi ia tidak
// bisa menjadi penjaga agar sistem tidak ditinggalkan tanpa superadmin.
// Pemanggil harus sudah memegang mutex.
func (m *Memory) adaSuperadminAktifLain(id string) bool {
	for _, a := range m.admins {
		if a.ID != id && a.Role == auth.RoleSuperadmin && a.IsActive && a.ActivatedAt != nil {
			return true
		}
	}
	return false
}

// EmailTakenByBranch melaporkan apakah email sudah dipakai cabang, baik sebagai
// email login akun cabang maupun email cabang yang belum registrasi.
func (m *Memory) EmailTakenByBranch(_ context.Context, email string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.branches {
		if NormalizeEmail(b.Email) == NormalizeEmail(email) {
			return true, nil
		}
	}
	return false, nil
}

// DocumentRecipients mengembalikan email admin ber-flag yang sudah diaktivasi.
func (m *Memory) DocumentRecipients(_ context.Context, kind DocKind) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, a := range m.admins {
		if !a.IsActive || a.ActivatedAt == nil {
			continue
		}
		switch kind {
		case DocInvoice:
			if a.NotifInvoice {
				out = append(out, a.Email)
			}
		case DocSuratJalan:
			if a.NotifSuratJalan {
				out = append(out, a.Email)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
