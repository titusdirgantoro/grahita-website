// Package settings mengelola pengaturan sistem (PRD §4.9).
//
// Nilai disimpan sebagai jsonb sehingga bisa ditambah tanpa migrasi; tipe
// yang diharapkan divalidasi di layer ini, bukan dipercaya dari klien.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/grahita/portal/api/internal/auth"
)

// ErrNotFound dikembalikan bila kunci pengaturan tidak dikenal.
var ErrNotFound = errors.New("pengaturan tidak ditemukan")

// ErrForbidden dikembalikan bila aktor tidak berhak mengubah pengaturan.
var ErrForbidden = errors.New("tidak berhak mengubah pengaturan")

// Value adalah satu baris pengaturan.
type Value struct {
	Key       string
	Value     []byte
	UpdatedAt time.Time
}

// Repository memuat dan menyimpan pengaturan.
type Repository interface {
	List(ctx context.Context) ([]Value, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Upsert(ctx context.Context, key string, value []byte, updatedBy string) error
}

// Service adalah logika bisnis pengaturan.
type Service struct {
	repo Repository
}

// NewService membuat service pengaturan.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// All mengembalikan seluruh pengaturan sebagai peta JSON.
func (s *Service) All(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("baca pengaturan: %w", err)
	}
	out := make(map[string]json.RawMessage, len(rows))
	for _, r := range rows {
		out[r.Key] = json.RawMessage(r.Value)
	}
	return out, nil
}

// Get mengembalikan satu nilai pengaturan.
func (s *Service) Get(ctx context.Context, key string) (json.RawMessage, error) {
	raw, err := s.repo.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// Update menyimpan satu pengaturan. Hanya Superadmin yang boleh mengubah
// (PRD §2.1) — pemeriksaan dilakukan di sini, bukan hanya di router.
func (s *Service) Update(ctx context.Context, actor auth.Actor, key string, value json.RawMessage) error {
	if !actor.Can(auth.CapAdminSettings) {
		return ErrForbidden
	}
	if key == "" {
		return errors.New("kunci pengaturan wajib diisi")
	}
	if len(value) == 0 || !json.Valid(value) {
		return errors.New("nilai pengaturan harus JSON yang valid")
	}
	if err := s.repo.Upsert(ctx, key, []byte(value), actor.UserID); err != nil {
		return fmt.Errorf("simpan pengaturan %s: %w", key, err)
	}
	return nil
}

// KnownKeys adalah kunci pengaturan yang dipakai sistem saat ini.
var KnownKeys = []string{
	"biaya_packaging",
	"cnc_hari",
	"token_hari",
	"reset_menit",
	"max_mb",
	"max_file",
	"digit_invoice",
	"manager_boleh_revisi",
	"ai",
}
