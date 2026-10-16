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
	"sort"
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
	ErrDuplicate         = errors.New("rekening sudah terdaftar")
	ErrIdentityImmutable = errors.New("identitas rekening tidak dapat diubah")
	ErrBranchNotFound    = errors.New("cabang tidak ditemukan")
	ErrVersionConflict   = errors.New("versi allowlist sudah berubah")
	ErrAccountInactive   = errors.New("rekening nonaktif tidak dapat ditambahkan")
	ErrUnavailable       = errors.New("cabang tidak mempunyai rekening pembayaran efektif")
	ErrNotAllowed        = errors.New("rekening tidak diizinkan untuk cabang")
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

func (a Account) DisplayNomor() string {
	n := NormalizeNomor(a.Nomor)
	if len(n) <= 4 {
		return "••••" + n
	}
	return "••••" + n[len(n)-4:]
}

type EffectiveSet struct {
	Accounts []Account `json:"data"`
	Version  int64     `json:"allowlist_version"`
}

type AssignmentItem struct {
	ID           string `json:"id"`
	Bank         string `json:"bank"`
	NamaPemilik  string `json:"nama_pemilik"`
	DisplayNomor string `json:"display_nomor"`
	IsActive     bool   `json:"is_active"`
	IsAllowed    bool   `json:"is_allowed"`
	Dormant      bool   `json:"dormant"`
	Effective    bool   `json:"effective"`
}

type AssignmentList struct {
	BranchID string           `json:"branch_id"`
	Version  int64            `json:"version"`
	Items    []AssignmentItem `json:"items"`
}

type ReplaceInput struct {
	ExpectedVersion int64
	BankAccountIDs  []string
}

type ReplaceResult struct {
	BranchID         string   `json:"branch_id"`
	Version          int64    `json:"version"`
	BankAccountIDs   []string `json:"bank_account_ids"`
	PaymentAvailable bool     `json:"payment_available"`
	Changed          bool     `json:"-"`
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
	EffectiveForBranch(ctx context.Context, branchID string) (EffectiveSet, error)
	ListAssignments(ctx context.Context, branchID string) (AssignmentList, error)
	ReplaceAssignments(ctx context.Context, branchID string, in ReplaceInput, actor auth.Actor) (ReplaceResult, error)
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

// EffectiveForBranch adalah satu-satunya sumber rekening instruksi/verifikasi
// cabang. Hasil kosong tidak pernah diganti daftar rekening global.
func (s *Service) EffectiveForBranch(ctx context.Context, branchID string) (EffectiveSet, error) {
	if strings.TrimSpace(branchID) == "" {
		return EffectiveSet{}, ErrBranchNotFound
	}
	set, err := s.repo.EffectiveForBranch(ctx, branchID)
	if err != nil {
		return EffectiveSet{}, err
	}
	if set.Accounts == nil {
		set.Accounts = []Account{}
	}
	return set, nil
}

func (s *Service) EffectiveForActor(ctx context.Context, actor auth.Actor) (EffectiveSet, error) {
	if !actor.IsBranch() || actor.BranchID == "" {
		return EffectiveSet{}, ErrForbidden
	}
	return s.EffectiveForBranch(ctx, actor.BranchID)
}

func (s *Service) PaymentAvailable(ctx context.Context, branchID string) (bool, error) {
	set, err := s.EffectiveForBranch(ctx, branchID)
	if err != nil {
		return false, err
	}
	return len(set.Accounts) > 0, nil
}

func (s *Service) ListAssignments(ctx context.Context, actor auth.Actor, branchID string) (AssignmentList, error) {
	if !actor.Can(auth.CapAdminBank) {
		return AssignmentList{}, ErrForbidden
	}
	return s.repo.ListAssignments(ctx, strings.TrimSpace(branchID))
}

func (s *Service) ReplaceAssignments(ctx context.Context, actor auth.Actor, branchID string, in ReplaceInput) (ReplaceResult, error) {
	if !actor.Can(auth.CapAdminBank) {
		return ReplaceResult{}, ErrForbidden
	}
	branchID = strings.TrimSpace(branchID)
	if branchID == "" || in.ExpectedVersion < 0 {
		return ReplaceResult{}, ErrInvalidData
	}
	seen := make(map[string]bool, len(in.BankAccountIDs))
	ids := make([]string, 0, len(in.BankAccountIDs))
	for _, raw := range in.BankAccountIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			return ReplaceResult{}, fmt.Errorf("%w: bank_account_ids harus unik dan tidak kosong", ErrInvalidData)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	in.BankAccountIDs = ids
	return s.repo.ReplaceAssignments(ctx, branchID, in, actor)
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
	rec := audit.NewEntry(ctx, actor, audit.EntityBankAccount, "", audit.ActionCreate, nil, accountAuditSnapshot(next))
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
		if NormalizeBank(*p.Bank) != cur.Bank {
			return Account{}, ErrIdentityImmutable
		}
	}
	if p.NamaPemilik != nil {
		next.NamaPemilik = strings.TrimSpace(*p.NamaPemilik)
	}
	if p.Nomor != nil {
		if NormalizeNomor(*p.Nomor) != cur.Nomor {
			return Account{}, ErrIdentityImmutable
		}
	}
	if p.IsActive != nil {
		next.IsActive = *p.IsActive
	}
	if err := validate(next); err != nil {
		return Account{}, err
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityBankAccount, id, audit.ActionUpdate,
		accountAuditSnapshot(cur), accountAuditSnapshot(next))
	saved, err := s.repo.Update(ctx, next, rec)
	if err != nil {
		return Account{}, err
	}
	return saved, nil
}

func accountAuditSnapshot(a Account) map[string]any {
	return map[string]any{
		"id": a.ID, "bank": a.Bank, "nama_pemilik": a.NamaPemilik,
		"display_nomor": a.DisplayNomor(), "is_active": a.IsActive,
	}
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
