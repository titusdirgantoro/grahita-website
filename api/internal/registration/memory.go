package registration

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Memory adalah implementasi Repository in-memory untuk pengujian dan mode
// demo. Bukan untuk produksi: state hilang saat proses berhenti.
//
// Tidak ada kunci baris sungguhan di sini; keadaan token dan keberadaan akun
// dihitung ULANG pada setiap pemanggilan sehingga perilaku sekali pakai dan
// "1 cabang = 1 akun" tetap bisa diuji. Pengujian konkurensi yang sesungguhnya
// ada di uji integrasi PostgreSQL.
type Memory struct {
	mu       sync.Mutex
	tokens   map[string]*memToken // key: hash heksadesimal
	accounts map[string]memAkun   // key: branch id (1 cabang = 1 akun)
	taken    map[string]bool      // email yang sudah dipakai akun lain
	audit    []audit.Entry
	seq      int
}

type memToken struct {
	branchID       string
	expiresAt      time.Time
	used           bool
	usedBy         string
	branchActive   bool
	identityStatus string
	nama           string
	namaKepala     string
	email          string
}

type memAkun struct {
	userID       string
	email        string
	passwordHash string
	telepon      string
}

// NewMemory membuat penyimpanan pendaftaran in-memory kosong.
func NewMemory() *Memory {
	return &Memory{
		tokens:   map[string]*memToken{},
		accounts: map[string]memAkun{},
		taken:    map[string]bool{},
	}
}

// PutToken menambahkan token registrasi berdasarkan NILAI MENTAHNYA (bentuk
// tampilan, mis. GRH-XXXX-XXXX-XXXX-XXXX). snap dipakai untuk data cabang dan
// masa berlaku; bidang UsedAt/BranchHasAccount diabaikan karena keduanya
// dihitung ulang dari state Memory.
func (m *Memory) PutToken(rawToken string, snap Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[hex.EncodeToString(auth.HashRegistrationToken(rawToken))] = &memToken{
		branchID:       snap.BranchID,
		expiresAt:      snap.ExpiresAt,
		used:           snap.UsedAt != nil,
		branchActive:   snap.BranchActive,
		identityStatus: snap.IdentityStatus,
		nama:           snap.Nama,
		namaKepala:     snap.NamaKepala,
		email:          snap.Email,
	}
}

// PutUsedToken menambahkan token yang sudah terpakai (untuk menguji
// TOKEN_USED tanpa harus mendaftar lebih dulu).
func (m *Memory) PutUsedToken(rawToken string, snap Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := hex.EncodeToString(auth.HashRegistrationToken(rawToken))
	m.tokens[key] = &memToken{
		branchID:       snap.BranchID,
		expiresAt:      snap.ExpiresAt,
		used:           true,
		usedBy:         "usr-lama",
		branchActive:   snap.BranchActive,
		identityStatus: snap.IdentityStatus,
		nama:           snap.Nama,
		namaKepala:     snap.NamaKepala,
		email:          snap.Email,
	}
}

// PutAccount menandai cabang sudah punya akun (menguji BRANCH_HAS_ACCOUNT).
func (m *Memory) PutAccount(branchID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[branchID] = memAkun{userID: "usr-" + branchID, email: "seed-" + branchID + "@example.test"}
}

// PutEmailTaken menandai email sudah dipakai akun lain.
func (m *Memory) PutEmailTaken(email string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taken[auth.NormalizeEmail(email)] = true
}

// AuditEntries mengembalikan salinan entri audit yang tercatat.
func (m *Memory) AuditEntries() []audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.Entry, len(m.audit))
	copy(out, m.audit)
	return out
}

// Akun mengembalikan akun cabang yang tersimpan (untuk pengujian).
func (m *Memory) Akun(branchID string) (MemAkun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[branchID]
	if !ok {
		return MemAkun{}, false
	}
	return MemAkun{UserID: a.userID, Email: a.email, PasswordHash: a.passwordHash, Telepon: a.telepon}, true
}

// MemAkun adalah salinan akun in-memory yang bisa diperiksa pengujian.
type MemAkun struct {
	UserID       string
	Email        string
	PasswordHash string
	Telepon      string
}

// Lookup memuat keadaan token berdasarkan hash-nya.
func (m *Memory) Lookup(_ context.Context, tokenHash []byte) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[hex.EncodeToString(tokenHash)]
	if !ok {
		return Snapshot{}, ErrTokenInvalid
	}
	return m.snapshotLocked(t), nil
}

// Register memakai token lalu membuat akun cabang — padanan transaksi
// PostgreSQL, termasuk pemeriksaan ULANG kelayakan token.
func (m *Memory) Register(_ context.Context, req RegisterRequest, rec audit.Entry) (Akun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, ok := m.tokens[hex.EncodeToString(req.TokenHash)]
	if !ok {
		return Akun{}, ErrTokenInvalid
	}
	snap := m.snapshotLocked(t)
	if err := snap.Cek(req.Now); err != nil {
		return Akun{}, err
	}

	email := auth.NormalizeEmail(snap.Email)
	if m.taken[email] {
		return Akun{}, ErrEmailTaken
	}
	m.seq++
	userID := fmt.Sprintf("usr-baru-%d", m.seq)
	m.accounts[snap.BranchID] = memAkun{
		userID:       userID,
		email:        email,
		passwordHash: req.PasswordHash,
		telepon:      req.Telepon,
	}
	t.used = true
	t.usedBy = userID

	rec.ActorID = userID
	rec.EntityID = userID
	rec.After = map[string]any{
		"user_id": userID, "branch_id": snap.BranchID, "nama": snap.Nama, "email": email,
	}
	m.audit = append(m.audit, rec)

	return Akun{
		UserID: userID, BranchID: snap.BranchID, Nama: snap.Nama,
		Email: email, Telepon: req.Telepon,
	}, nil
}

// snapshotLocked menyusun keadaan token apa adanya saat ini.
func (m *Memory) snapshotLocked(t *memToken) Snapshot {
	s := Snapshot{
		TokenID:        "tok-" + t.branchID,
		BranchID:       t.branchID,
		ExpiresAt:      t.expiresAt,
		Nama:           t.nama,
		NamaKepala:     t.namaKepala,
		Email:          t.email,
		IdentityStatus: t.identityStatus,
		BranchActive:   t.branchActive,
	}
	if t.used {
		used := t.expiresAt
		s.UsedAt = &used
	}
	if _, ok := m.accounts[t.branchID]; ok {
		s.BranchHasAccount = true
	}
	return s
}
