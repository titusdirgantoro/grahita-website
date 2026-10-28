// Package ocrsecret menyimpan kunci Gemini terenkripsi untuk OCR. Kunci hanya
// dapat ditulis Superadmin dan tidak pernah dikirim kembali ke browser.
package ocrsecret

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
)

var (
	ErrNotConfigured = errors.New("kunci Gemini belum dikonfigurasi")
	ErrNotVerified   = errors.New("kunci Gemini belum lulus uji koneksi")
	ErrAIActive      = errors.New("matikan AI sebelum mengganti atau menghapus kunci")
	ErrStale         = errors.New("kunci Gemini berubah selama uji koneksi")
	ErrForbidden     = errors.New("hanya Superadmin boleh mengelola kunci Gemini")
	ErrInvalidKey    = errors.New("format API key Gemini tidak valid")
)

const aad = "grahita/gemini-ocr-key/v1"

type Record struct {
	Ciphertext []byte
	Nonce      []byte
	Version    int64
	VerifiedAt *time.Time
	UpdatedAt  time.Time
}

type Repository interface {
	Load(context.Context) (Record, error)
	Save(context.Context, auth.Actor, Record) error
	MarkVerified(context.Context, auth.Actor, int64) error
	Delete(context.Context, auth.Actor) error
}

type Status struct {
	Configured bool       `json:"key_configured"`
	Verified   bool       `json:"key_verified"`
	UpdatedAt  *time.Time `json:"key_updated_at,omitempty"`
}

type Service struct {
	repo Repository
	aead cipher.AEAD
}

func New(repo Repository, encodedMasterKey string) (*Service, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedMasterKey))
	if err != nil || len(key) != 32 {
		return nil, errors.New("GEMINI_KEY_ENCRYPTION_KEY harus base64 32 byte")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, aead: aead}, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	r, err := s.repo.Load(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	if _, err := s.decrypt(r); err != nil {
		return Status{}, err
	}
	return Status{Configured: true, Verified: r.VerifiedAt != nil, UpdatedAt: &r.UpdatedAt}, nil
}

func (s *Service) Save(ctx context.Context, actor auth.Actor, key string) error {
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminSettings) {
		return ErrForbidden
	}
	key = strings.TrimSpace(key)
	if len(key) < 16 || len(key) > 512 || strings.ContainsAny(key, " \t\r\n") {
		return ErrInvalidKey
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("buat nonce kunci OCR: %w", err)
	}
	return s.repo.Save(ctx, actor, Record{
		Ciphertext: s.aead.Seal(nil, nonce, []byte(key), []byte(aad)),
		Nonce:      nonce,
	})
}

func (s *Service) Delete(ctx context.Context, actor auth.Actor) error {
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminSettings) {
		return ErrForbidden
	}
	return s.repo.Delete(ctx, actor)
}

// KeyForRead hanya membuka kunci yang telah diuji. Worker membacanya per job,
// sehingga rotasi kunci dari portal berlaku tanpa restart dan tidak di-cache.
func (s *Service) KeyForRead(ctx context.Context) (string, error) {
	r, err := s.repo.Load(ctx)
	if err != nil {
		return "", err
	}
	if r.VerifiedAt == nil {
		return "", ErrNotVerified
	}
	return s.decrypt(r)
}

// KeyForTest membolehkan API menguji kunci baru sebelum worker memakainya.
func (s *Service) KeyForTest(ctx context.Context) (string, int64, error) {
	r, err := s.repo.Load(ctx)
	if err != nil {
		return "", 0, err
	}
	key, err := s.decrypt(r)
	return key, r.Version, err
}

func (s *Service) MarkVerified(ctx context.Context, actor auth.Actor, version int64) error {
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminSettings) {
		return ErrForbidden
	}
	return s.repo.MarkVerified(ctx, actor, version)
}

func (s *Service) decrypt(r Record) (string, error) {
	if len(r.Nonce) != s.aead.NonceSize() {
		return "", errors.New("nonce kunci OCR tidak valid")
	}
	plain, err := s.aead.Open(nil, r.Nonce, r.Ciphertext, []byte(aad))
	if err != nil {
		return "", errors.New("kunci OCR tidak dapat didekripsi")
	}
	return string(plain), nil
}
