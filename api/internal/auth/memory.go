package auth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Memory adalah implementasi UserStore + SessionStore + ResetStore untuk
// pengujian dan mode demo. Bukan untuk produksi: state hilang saat proses
// berhenti.
type Memory struct {
	mu       sync.RWMutex
	admins   map[string]Credentials // key: id
	branches map[string]Credentials // key: id
	sessions map[string]*memSession // key: hash heksadesimal
	resets   map[string]*memReset   // key: hash heksadesimal
	// audits adalah entri audit yang ditulis bersama token atur ulang
	// (F2-28); implementasi PostgreSQL menyimpannya di tabel audit_logs.
	audits []ResetAudit
}

type memSession struct {
	session Session
	hash    []byte
	revoked bool
}

type memReset struct {
	token ResetToken
	used  bool
}

// NewMemory membuat penyimpanan in-memory kosong.
func NewMemory() *Memory {
	return &Memory{
		admins:   map[string]Credentials{},
		branches: map[string]Credentials{},
		sessions: map[string]*memSession{},
		resets:   map[string]*memReset{},
	}
}

// PutAdmin menambahkan akun admin (kata sandi sudah dalam bentuk hash).
func (m *Memory) PutAdmin(c Credentials) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.Kind == "" {
		c.Kind = KindAdmin
	}
	// Default yang aman untuk pengujian lama: akun yang boleh login pasti
	// aktif. Varian undangan (Active=false, IsActive=true) diisi eksplisit.
	if !c.IsActive && c.Active {
		c.IsActive = true
	}
	m.admins[c.UserID] = c
}

// PutBranch menambahkan akun cabang (kata sandi sudah dalam bentuk hash).
func (m *Memory) PutBranch(c Credentials) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.Kind == "" {
		c.Kind = KindBranch
	}
	if c.Role == "" {
		c.Role = RoleBranch
	}
	m.branches[c.UserID] = c
}

// AdminByEmail mencari akun admin berdasarkan email.
func (m *Memory) AdminByEmail(_ context.Context, email string) (Credentials, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.admins {
		if NormalizeEmail(c.Email) == NormalizeEmail(email) {
			return c, nil
		}
	}
	return Credentials{}, ErrNoUser
}

// AdminByID mencari akun admin berdasarkan id.
func (m *Memory) AdminByID(_ context.Context, id string) (Credentials, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if c, ok := m.admins[id]; ok {
		return c, nil
	}
	return Credentials{}, ErrNoUser
}

// BranchByEmail mencari akun cabang berdasarkan email.
func (m *Memory) BranchByEmail(_ context.Context, email string) (Credentials, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.branches {
		if NormalizeEmail(c.Email) == NormalizeEmail(email) {
			return c, nil
		}
	}
	return Credentials{}, ErrNoUser
}

// BranchByID mencari akun cabang berdasarkan id.
func (m *Memory) BranchByID(_ context.Context, id string) (Credentials, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if c, ok := m.branches[id]; ok {
		return c, nil
	}
	return Credentials{}, ErrNoUser
}

// TouchLogin tidak melakukan apa pun di memori.
func (m *Memory) TouchLogin(context.Context, Kind, string) error { return nil }

// UpdatePassword mengganti hash kata sandi satu akun.
func (m *Memory) UpdatePassword(_ context.Context, kind Kind, id, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind == KindAdmin {
		c, ok := m.admins[id]
		if !ok {
			return ErrNoUser
		}
		c.PasswordHash = passwordHash
		m.admins[id] = c
		return nil
	}
	c, ok := m.branches[id]
	if !ok {
		return ErrNoUser
	}
	c.PasswordHash = passwordHash
	m.branches[id] = c
	return nil
}

// ChangePassword menerapkan CAS credential + validasi sesi secara atomik.
func (m *Memory) ChangePassword(_ context.Context, kind Kind, id, sessionID, expectedPasswordHash, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var creds Credentials
	var ok bool
	if kind == KindAdmin {
		creds, ok = m.admins[id]
	} else {
		creds, ok = m.branches[id]
	}
	if !ok || !creds.Active || creds.PasswordHash != expectedPasswordHash {
		return ErrSessionInvalid
	}
	var current *memSession
	for _, ms := range m.sessions {
		if ms.session.ID == sessionID && ms.session.ActorID == id && ms.session.Kind == kind && !ms.revoked {
			current = ms
			break
		}
	}
	if current == nil {
		return ErrSessionInvalid
	}
	creds.PasswordHash = passwordHash
	if kind == KindAdmin {
		m.admins[id] = creds
	} else {
		m.branches[id] = creds
	}
	for _, ms := range m.sessions {
		if ms.session.ActorID == id && ms.session.ID != sessionID {
			ms.revoked = true
		}
	}
	return nil
}

// ActivateAdmin menyimpan kata sandi DAN menandai admin sudah diaktivasi —
// padanan memory dari password_hash + activated_at (kewajiban F1-04).
func (m *Memory) ActivateAdmin(_ context.Context, id, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.admins[id]
	if !ok {
		return ErrNoUser
	}
	c.PasswordHash = passwordHash
	if c.IsActive {
		c.Active = true
	}
	m.admins[id] = c
	return nil
}

// UpdateName menyimpan nama tampilan akun ADMIN. Akun cabang ditolak karena
// namanya berasal dari master cabang (PRD §3.8).
func (m *Memory) UpdateName(_ context.Context, kind Kind, id, nama string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind != KindAdmin {
		return ErrProfileNotEditable
	}
	c, ok := m.admins[id]
	if !ok {
		return ErrNoUser
	}
	c.Nama = nama
	m.admins[id] = c
	return nil
}

// CreateResetToken menyimpan token atur ulang dan membatalkan token aktif
// sebelumnya milik pengguna yang sama (hanya satu tautan berlaku). Entri audit
// yang menyertai token (F2-28) disimpan bersama tokennya — di memori keduanya
// selalu "satu transaksi".
func (m *Memory) CreateResetToken(_ context.Context, t ResetToken, tokenHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var creds Credentials
	var ok bool
	if t.Kind == KindAdmin {
		creds, ok = m.admins[t.UserID]
		if !ok {
			return ErrNoUser
		}
		if !creds.IsActive {
			return ErrInactiveAccount
		}
	} else {
		creds, ok = m.branches[t.UserID]
		if !ok {
			return ErrNoUser
		}
		if !creds.Active {
			return ErrInactiveAccount
		}
	}
	if NormalizeEmail(creds.Email) != NormalizeEmail(t.ExpectedEmail) ||
		creds.PasswordHash != t.ExpectedPasswordHash {
		return ErrInactiveAccount
	}
	for _, r := range m.resets {
		if r.token.UserID == t.UserID && !r.used {
			r.used = true
		}
	}
	if t.ID == "" {
		t.ID = fmt.Sprintf("reset-%x", tokenHash[:8])
	}
	if t.Audit != nil {
		m.audits = append(m.audits, *t.Audit)
	}
	t.Audit = nil               // token tersimpan tanpa pointer ke fakta auditnya
	t.ExpectedEmail = ""        // snapshot validasi tidak ikut dipersistenkan
	t.ExpectedPasswordHash = "" // snapshot credential juga hanya untuk validasi
	m.resets[string(tokenHash)] = &memReset{token: t}
	return nil
}

// ResetAudits mengembalikan salinan entri audit yang ditulis bersama token
// atur ulang (hanya untuk pengujian).
func (m *Memory) ResetAudits() []ResetAudit {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ResetAudit, len(m.audits))
	copy(out, m.audits)
	return out
}

// ConsumeResetToken memakai token sekali pakai secara atomik.
func (m *Memory) ConsumeResetToken(_ context.Context, tokenHash []byte, now time.Time) (ResetToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resets[string(tokenHash)]
	if !ok || r.used || !now.Before(r.token.ExpiresAt) {
		return ResetToken{}, ErrResetInvalid
	}
	r.used = true
	return r.token, nil
}

// ResetPassword mengubah credential dan mencabut sesi pada critical section
// yang sama dengan konsumsi token.
func (m *Memory) ResetPassword(_ context.Context, tokenHash []byte, now time.Time, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.resets[string(tokenHash)]
	if !ok || r.used || !now.Before(r.token.ExpiresAt) {
		return ErrResetInvalid
	}
	var creds Credentials
	if r.token.Kind == KindAdmin {
		creds, ok = m.admins[r.token.UserID]
		if !ok || !creds.IsActive {
			return ErrResetInvalid
		}
		creds.PasswordHash = passwordHash
		creds.Active = true
		m.admins[r.token.UserID] = creds
	} else {
		creds, ok = m.branches[r.token.UserID]
		if !ok || !creds.Active {
			return ErrResetInvalid
		}
		creds.PasswordHash = passwordHash
		m.branches[r.token.UserID] = creds
	}
	r.used = true
	for _, ms := range m.sessions {
		if ms.session.ActorID == r.token.UserID {
			ms.revoked = true
		}
	}
	return nil
}

// ResetTokenCount mengembalikan jumlah token atur ulang yang masih berlaku
// (hanya untuk pengujian).
func (m *Memory) ResetTokenCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, r := range m.resets {
		if !r.used {
			n++
		}
	}
	return n
}

// Create menyimpan sesi baru.
func (m *Memory) Create(_ context.Context, s Session, tokenHash []byte, _ RequestMeta) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var creds Credentials
	var ok bool
	if s.Kind == KindAdmin {
		creds, ok = m.admins[s.ActorID]
	} else {
		creds, ok = m.branches[s.ActorID]
	}
	if !ok {
		return Session{}, ErrNoUser
	}
	if !creds.Active {
		return Session{}, ErrInactiveAccount
	}
	if creds.PasswordHash != s.ExpectedPasswordHash {
		return Session{}, ErrInvalidCredentials
	}
	if s.LastSeenAt.IsZero() {
		s.LastSeenAt = time.Now().UTC()
	}
	if s.ID == "" {
		s.ID = "mem-" + time.Now().UTC().Format("150405.000000")
	}
	s.ExpectedPasswordHash = ""
	cp := s
	m.sessions[string(tokenHash)] = &memSession{session: cp, hash: tokenHash}
	return s, nil
}

// ByTokenHash memuat sesi dari hash token.
func (m *Memory) ByTokenHash(_ context.Context, tokenHash []byte) (Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ms, ok := m.sessions[string(tokenHash)]
	if !ok || ms.revoked {
		return Session{}, ErrSessionInvalid
	}
	return ms.session, nil
}

// Touch memperbarui waktu akses terakhir.
func (m *Memory) Touch(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ms := range m.sessions {
		if ms.session.ID == id {
			ms.session.LastSeenAt = at
		}
	}
	return nil
}

// Revoke mencabut satu sesi.
func (m *Memory) Revoke(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ms := range m.sessions {
		if ms.session.ID == id {
			ms.revoked = true
		}
	}
	return nil
}

// RevokeForUser mencabut seluruh sesi satu pengguna.
func (m *Memory) RevokeForUser(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ms := range m.sessions {
		if ms.session.ActorID == userID {
			ms.revoked = true
		}
	}
	return nil
}

// RevokeOthers mencabut seluruh sesi satu pengguna kecuali keepID.
func (m *Memory) RevokeOthers(_ context.Context, userID, keepID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ms := range m.sessions {
		if ms.session.ActorID == userID && ms.session.ID != keepID {
			ms.revoked = true
		}
	}
	return nil
}

// SessionCount mengembalikan jumlah sesi aktif (hanya untuk pengujian).
func (m *Memory) SessionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, ms := range m.sessions {
		if !ms.revoked {
			n++
		}
	}
	return n
}
