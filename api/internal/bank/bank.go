// Package bank mengelola rekening bank tujuan transfer (PRD §4.6,
// Database.md §4.3).
//
// Aturan yang ditegakkan di sini — bukan hanya disembunyikan di UI:
//   - bank hanya BCA atau BRI (enum bank_code pada database).
//   - nomor rekening hanya digit; pasangan (bank, nomor) unik.
//   - rekening HANYA boleh dibaca/diubah Superadmin (PRD §2.1). Pembacaan
//     rekening AKTIF untuk instruksi pembayaran Customer Portal dan referensi
//     pencocokan rekening tujuan pada verifikasi OCR tidak membutuhkan
//     kapabilitas admin: yang boleh dilihat cabang hanyalah rekening aktif.
//   - setiap mutasi dicatat ke audit_logs DI DALAM transaksi yang sama.
package bank

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrForbidden: aktor tidak punya kapabilitas rekening bank (bukan Superadmin).
	ErrForbidden = errors.New("tidak berhak mengelola rekening bank")
	// ErrNotFound: rekening tidak ditemukan.
	ErrNotFound = errors.New("rekening tidak ditemukan")
	// ErrInvalidData: data rekening tidak lolos validasi domain.
	ErrInvalidData = errors.New("data rekening tidak valid")
	// ErrDuplicate: pasangan bank + nomor sudah terdaftar.
	ErrDuplicate = errors.New("rekening sudah terdaftar")
)

// Kode bank yang dikenal (Database.md §3 tipe enum bank_code).
const (
	BankBCA = "BCA"
	BankBRI = "BRI"
)

// nomorPattern memastikan nomor rekening hanya berisi digit. Panjang yang
// wajar untuk rekening bank Indonesia adalah 6–34 digit.
var nomorPattern = regexp.MustCompile(`^[0-9]{6,34}$`)

// Account adalah satu rekening bank pada master data (PRD §4.6).
type Account struct {
	ID          string `json:"id"`
	Bank        string `json:"bank"`
	NamaPemilik string `json:"nama_pemilik"`
	Nomor       string `json:"nomor"`
	IsActive    bool   `json:"is_active"`
}

// NewAccount adalah permintaan pembuatan rekening.
type NewAccount struct {
	Bank        string
	NamaPemilik string
	Nomor       string
	// IsActive nil berarti rekening baru aktif (default kolom).
	IsActive *bool
}

// Patch adalah perubahan sebagian rekening. Field nil berarti tidak diubah.
type Patch struct {
	Bank        *string
	NamaPemilik *string
	Nomor       *string
	IsActive    *bool
}

// Repository menyimpan master rekening bank.
//
// Setiap metode yang mengubah data menerima audit.Entry dan WAJIB
// menyimpannya di dalam transaksi yang sama dengan perubahannya
// (SystemDesign.md §5.4).
type Repository interface {
	List(ctx context.Context) ([]Account, error)
	ListActive(ctx context.Context) ([]Account, error)
	Get(ctx context.Context, id string) (Account, error)
	Create(ctx context.Context, in Account, rec audit.Entry) (Account, error)
	Update(ctx context.Context, next Account, rec audit.Entry) (Account, error)
}

// Service adalah logika bisnis rekening bank.
type Service struct {
	repo Repository
}

// NewService membuat service rekening bank.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// List mengembalikan seluruh rekening (aktif maupun nonaktif) untuk
// pengelolaan Superadmin. Manager dan akun cabang ditolak (PRD §2.1).
func (s *Service) List(ctx context.Context, actor auth.Actor) ([]Account, error) {
	if !actor.Can(auth.CapAdminBank) {
		return nil, ErrForbidden
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("daftar rekening: %w", err)
	}
	return rows, nil
}

// Active mengembalikan HANYA rekening aktif. Inilah sumber instruksi
// pembayaran Customer Portal dan referensi pencocokan rekening tujuan pada
// verifikasi OCR (PRD §4.6, §5): rekening nonaktif tidak pernah ditampilkan.
func (s *Service) Active(ctx context.Context) ([]Account, error) {
	rows, err := s.repo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("daftar rekening aktif: %w", err)
	}
	return rows, nil
}

// Create menambahkan rekening baru (Superadmin).
func (s *Service) Create(ctx context.Context, actor auth.Actor, in NewAccount) (Account, error) {
	if !actor.Can(auth.CapAdminBank) {
		return Account{}, ErrForbidden
	}
	next := Account{
		Bank:        NormalizeBank(in.Bank),
		NamaPemilik: strings.TrimSpace(in.NamaPemilik),
		Nomor:       NormalizeNomor(in.Nomor),
		IsActive:    true,
	}
	if in.IsActive != nil {
		next.IsActive = *in.IsActive
	}
	if err := validate(next); err != nil {
		return Account{}, err
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityBankAccount, "", audit.ActionCreate, nil, next)
	saved, err := s.repo.Create(ctx, next, rec)
	if err != nil {
		return Account{}, err
	}
	return saved, nil
}

// Update mengubah rekening, termasuk mengaktifkan atau menonaktifkannya
// (Superadmin). Rekening nonaktif tidak lagi muncul pada instruksi pembayaran.
func (s *Service) Update(ctx context.Context, actor auth.Actor, id string, p Patch) (Account, error) {
	if !actor.Can(auth.CapAdminBank) {
		return Account{}, ErrForbidden
	}
	if id == "" {
		return Account{}, ErrNotFound
	}
	cur, err := s.repo.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	next := cur
	if p.Bank != nil {
		next.Bank = NormalizeBank(*p.Bank)
	}
	if p.NamaPemilik != nil {
		next.NamaPemilik = strings.TrimSpace(*p.NamaPemilik)
	}
	if p.Nomor != nil {
		next.Nomor = NormalizeNomor(*p.Nomor)
	}
	if p.IsActive != nil {
		next.IsActive = *p.IsActive
	}
	if err := validate(next); err != nil {
		return Account{}, err
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityBankAccount, id, audit.ActionUpdate, cur, next)
	saved, err := s.repo.Update(ctx, next, rec)
	if err != nil {
		return Account{}, err
	}
	return saved, nil
}

// validate memeriksa invarian data rekening.
func validate(a Account) error {
	switch {
	case !knownBank(a.Bank):
		return fmt.Errorf("%w: bank harus BCA atau BRI", ErrInvalidData)
	case len([]rune(a.NamaPemilik)) < 2:
		return fmt.Errorf("%w: nama pemilik minimal 2 karakter", ErrInvalidData)
	case len([]rune(a.NamaPemilik)) > 120:
		return fmt.Errorf("%w: nama pemilik maksimal 120 karakter", ErrInvalidData)
	case !nomorPattern.MatchString(a.Nomor):
		return fmt.Errorf("%w: nomor rekening harus 6–34 digit angka", ErrInvalidData)
	}
	return nil
}

// knownBank melaporkan apakah kode bank dikenal sistem.
func knownBank(b string) bool {
	return b == BankBCA || b == BankBRI
}

// NormalizeBank menyeragamkan kode bank (huruf besar, tanpa spasi tepi).
func NormalizeBank(b string) string {
	return strings.ToUpper(strings.TrimSpace(b))
}

// NormalizeNomor menyeragamkan nomor rekening: hanya digit, spasi dan tanda
// hubung pemisah dibuang supaya pengetikan "1234 5678" tidak ditolak.
func NormalizeNomor(n string) string {
	n = strings.TrimSpace(n)
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.':
			// pemisah diabaikan
		default:
			// karakter lain dipertahankan agar validasi menolaknya
			b.WriteRune(r)
		}
	}
	return b.String()
}
