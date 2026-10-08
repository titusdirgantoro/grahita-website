package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"time"
)

// ErrInvalidCredentials dikembalikan untuk email tidak dikenal maupun
// password salah — pesannya identik supaya tidak membocorkan keberadaan akun
// (PRD CP-AUT-02).
var ErrInvalidCredentials = errors.New("email atau kata sandi salah")

// ErrInactiveAccount dikembalikan bila akun ada tetapi dinonaktifkan.
var ErrInactiveAccount = errors.New("akun tidak aktif")

// ErrSessionInvalid dikembalikan bila cookie sesi tidak dikenali, sudah
// dicabut, atau kedaluwarsa.
var ErrSessionInvalid = errors.New("sesi tidak valid atau sudah berakhir")

// TokenBytes adalah panjang token sesi mentah (ditampilkan hanya di cookie).
const TokenBytes = 32

// Session adalah baris sesi yang sudah dimuat dari penyimpanan.
type Session struct {
	ID         string
	ActorID    string
	Kind       Kind
	BranchID   string
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// Expired memeriksa batas absolut maupun batas diam (idle timeout).
func (s Session) Expired(now time.Time, idle time.Duration) bool {
	if now.After(s.ExpiresAt) {
		return true
	}
	if idle > 0 && now.Sub(s.LastSeenAt) > idle {
		return true
	}
	return false
}

// SessionStore menyimpan sesi di database. Token TIDAK pernah disimpan apa
// adanya; yang disimpan hanya SHA-256-nya.
type SessionStore interface {
	Create(ctx context.Context, s Session, tokenHash []byte, meta RequestMeta) (Session, error)
	ByTokenHash(ctx context.Context, tokenHash []byte) (Session, error)
	Touch(ctx context.Context, id string, at time.Time) error
	Revoke(ctx context.Context, id string) error
	RevokeForUser(ctx context.Context, userID string) error
}

// RequestMeta adalah konteks request yang dicatat untuk audit sesi.
type RequestMeta struct {
	IP        string
	UserAgent string
}

// NewToken membuat token sesi acak beserta hash SHA-256-nya.
func NewToken() (token string, hash []byte, err error) {
	buf := make([]byte, TokenBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken menghitung hash yang disimpan di kolom sessions.token_hash.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
