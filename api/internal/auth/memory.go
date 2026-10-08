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

// CreateResetToken menyimpan token atur ulang dan membatalkan token aktif
// sebelumnya milik pengguna yang sama (hanya satu tautan berlaku).
func (m *Memory) CreateResetToken(_ context.Context, t ResetToken, tokenHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.resets {
		if r.token.UserID == t.UserID && !r.used {
			r.used = true
		}
	}
	if t.ID == "" {
		t.ID = fmt.Sprintf("reset-%x", tokenHash[:8])
	}
	m.resets[string(tokenHash)] = &memReset{token: t}
	return nil
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
	if s.LastSeenAt.IsZero() {
		s.LastSeenAt = time.Now().UTC()
	}
	if s.ID == "" {
		s.ID = "mem-" + time.Now().UTC().Format("150405.000000")
	}
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
