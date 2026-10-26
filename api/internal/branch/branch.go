// Package branch mengelola master cabang: CRUD, filter, dan token registrasi
// sekali pakai (PRD §4.4, SystemDesign.md §4.1 dan §6).
//
// Aturan yang ditegakkan di sini — bukan hanya disembunyikan di UI:
//   - email cabang unik; email cabang = email login, jadi mengubahnya ikut
//     menyelaraskan email akun cabang yang sudah ada (Database.md §4.1).
//   - cabang yang sudah punya pesanan TIDAK boleh dihapus, hanya
//     dinonaktifkan (PRD AP-CAB-04).
//   - cabang nonaktif dilarang login lewat aturan `boleh_login` di SQL —
//     paket ini TIDAK menduplikasi aturan itu (kewajiban turunan F1-04).
//   - token registrasi hanya untuk cabang yang BELUM punya akun (1 cabang =
//     1 akun, PRD CP-REG-06); generate menggantikan token lama yang belum
//     terpakai (PRD AP-CAB-02) dan nilai aslinya hanya tampil sekali.
//   - setiap mutasi dicatat ke audit_logs DI DALAM transaksi yang sama.
//
// PI adalah atribut laporan dan tidak mengubah harga maupun invoice.
package branch

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrForbidden: aktor tidak punya kapabilitas master cabang.
	ErrForbidden = errors.New("tidak berhak mengelola master cabang")
	// ErrNotFound: cabang tidak ditemukan.
	ErrNotFound = errors.New("cabang tidak ditemukan")
	// ErrEmailTaken: email sudah dipakai cabang (atau akun admin) lain.
	ErrEmailTaken = errors.New("email sudah dipakai")
	// ErrInvalidData: data yang dikirim tidak lolos validasi domain.
	ErrInvalidData = errors.New("data cabang tidak valid")
	// ErrHasOrders: cabang sudah punya pesanan atau riwayat ekonomi lain (bukti
	// bayar, deposit, tarif impor) sehingga tidak boleh dihapus.
	ErrHasOrders = errors.New("cabang sudah punya pesanan atau riwayat transaksi")
	// ErrHasAccount: cabang sudah punya akun sehingga tidak butuh token.
	ErrHasAccount = errors.New("cabang sudah punya akun")
	// ErrInactive: cabang nonaktif tidak boleh memegang token registrasi baru.
	ErrInactive = errors.New("cabang nonaktif")
	// ErrIdentityIncomplete: identitas wajib belum lengkap sehingga seluruh
	// jalur autentikasi harus fail-closed.
	ErrIdentityIncomplete = errors.New("identitas cabang belum lengkap")
)

const (
	IdentityIncomplete = "identity_incomplete"
	IdentityReady      = "ready"
	IdentityRegistered = "registered"
)

// emailPattern adalah pemeriksaan bentuk email tingkat domain. Validasi
// lengkap ada di lapisan transport (tag `validate:"email"`); yang ini menjaga
// service tetap benar bila dipanggil dari jalur lain (mis. seed/uji).
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Branch adalah satu cabang pada master data (PRD §4.4).
//
// TokenPrefix/TokenExpiresAt hanya terisi bila cabang masih punya token
// registrasi yang berlaku — nilai asli tokennya sendiri tidak pernah
// dikembalikan dari sini (hanya sekali saat generate).
type Branch struct {
	ID                        string     `json:"id"`
	Nama                      string     `json:"nama"`
	NamaKepala                string     `json:"nama_kepala"`
	Email                     string     `json:"email"`
	JabatanKepala             string     `json:"jabatan_kepala,omitempty"`
	IdentityStatus            string     `json:"identity_status"`
	Telepon                   string     `json:"telepon,omitempty"`
	IsCNC                     bool       `json:"is_cnc"`
	IsTempo                   bool       `json:"is_tempo"`
	AllowTransfer             bool       `json:"allow_transfer"`
	TanggalBayar              int        `json:"tanggal_bayar,omitempty"`
	TahanBilaTelat            bool       `json:"tahan_bila_telat"`
	PajakIn                   bool       `json:"pajak_in"`
	IsActive                  bool       `json:"is_active"`
	BankAllowlistVersion      int64      `json:"bank_allowlist_version"`
	EffectiveBankAccountCount int64      `json:"effective_bank_account_count"`
	TokenPrefix               string     `json:"token_prefix,omitempty"`
	TokenExpiresAt            *time.Time `json:"token_expires_at,omitempty"`
}

// Filter adalah penyaring daftar cabang. Field nil berarti tidak disaring.
type Filter struct {
	// Q mencari pada nama dan email (ILIKE).
	Q      string
	Active *bool
	CNC    *bool
	Tempo  *bool
}

// NewBranch adalah permintaan pembuatan cabang.
type NewBranch struct {
	Nama           string
	NamaKepala     string
	Email          string
	JabatanKepala  string
	Telepon        string
	IsCNC          bool
	IsTempo        bool
	AllowTransfer  *bool
	TanggalBayar   int
	TahanBilaTelat bool
	PajakIn        bool
	// IsActive nil berarti cabang baru aktif (default kolom).
	IsActive *bool
}

// Patch adalah perubahan sebagian cabang. Field nil berarti tidak diubah.
type Patch struct {
	Nama           *string
	NamaKepala     *string
	Email          *string
	JabatanKepala  *string
	Telepon        *string
	IsCNC          *bool
	IsTempo        *bool
	AllowTransfer  *bool
	TanggalBayar   *int
	TahanBilaTelat *bool
	PajakIn        *bool
	IsActive       *bool
}

// IssuedToken adalah token registrasi yang baru dibuat. Token hanya ada di
// nilai ini dan tidak pernah bisa dibaca ulang dari mana pun.
type IssuedToken struct {
	Token     string    `json:"token"`
	Prefix    string    `json:"prefix"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Repository menyimpan master cabang beserta token registrasinya.
//
// Setiap metode yang mengubah data menerima audit.Entry dan WAJIB
// menyimpannya di dalam transaksi yang sama dengan perubahannya
// (SystemDesign.md §5.4).
type Repository interface {
	List(ctx context.Context, f Filter) ([]Branch, error)
	Get(ctx context.Context, id string) (Branch, error)
	// Create menyimpan cabang baru dan mengembalikan id-nya.
	Create(ctx context.Context, in Branch, rec audit.Entry) (string, error)
	// Update menerapkan patch terhadap state terkini setelah repository
	// mengunci cabang dan akun terkait. Hasil dan audit harus memakai snapshot
	// aktual di dalam transaksi, bukan hasil pembacaan service sebelum lock.
	Update(ctx context.Context, id string, p Patch, actor auth.Actor) (Branch, error)
	Delete(ctx context.Context, id string, rec audit.Entry) error
	// IssueToken menggantikan token lama yang belum terpakai dengan token baru.
	IssueToken(ctx context.Context, branchID string, tokenHash []byte, prefix string, expiresAt time.Time, rec audit.Entry) error
	HasOrders(ctx context.Context, id string) (bool, error)
	HasAccount(ctx context.Context, id string) (bool, error)
	// EmailTaken memeriksa email cabang lain; exceptID dikecualikan.
	EmailTaken(ctx context.Context, email, exceptID string) (bool, error)
	// AdminEmailTaken memeriksa email yang sudah dipakai akun admin.
	AdminEmailTaken(ctx context.Context, email string) (bool, error)
}

// Service adalah logika bisnis master cabang.
type Service struct {
	repo  Repository
	clock clock.Clock
}

// NewService membuat service master cabang.
func NewService(repo Repository, c clock.Clock) *Service {
	if c == nil {
		c = clock.System()
	}
	return &Service{repo: repo, clock: c}
}

// List mengembalikan cabang sesuai filter (PRD §4.4, tombol filter aktif).
func (s *Service) List(ctx context.Context, actor auth.Actor, f Filter) ([]Branch, error) {
	if !actor.Can(auth.CapAdminBranches) {
		return nil, ErrForbidden
	}
	f.Q = strings.TrimSpace(f.Q)
	rows, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("daftar cabang: %w", err)
	}
	return rows, nil
}

// Get mengembalikan satu cabang.
func (s *Service) Get(ctx context.Context, actor auth.Actor, id string) (Branch, error) {
	if !actor.Can(auth.CapAdminBranches) {
		return Branch{}, ErrForbidden
	}
	if id == "" {
		return Branch{}, ErrNotFound
	}
	return s.repo.Get(ctx, id)
}

// IsCNC melaporkan apakah satu cabang berflag Cash & Carry (PRD CP-CNC-01).
//
// Metode ini SENGAJA tidak menuntut aktor: ia bukan permukaan HTTP, melainkan
// pembacaan lintas-domain (SystemDesign.md §2) yang dipakai domain pesanan
// untuk menentukan aturan C&C dari flag cabang SESI — bukan dari masukan
// klien. Aturan C&C yang bergantung pada nilai ini (penolakan permintaan C&C
// oleh cabang tanpa flag) ditegakkan di domain pesanan.
func (s *Service) IsCNC(ctx context.Context, branchID string) (bool, error) {
	if branchID == "" {
		return false, ErrNotFound
	}
	b, err := s.repo.Get(ctx, branchID)
	if err != nil {
		return false, err
	}
	return b.IsCNC, nil
}

// PaymentMode returns the server-owned payment settings used at submit.
func (s *Service) PaymentMode(ctx context.Context, branchID string) (Branch, error) {
	if branchID == "" {
		return Branch{}, ErrNotFound
	}
	return s.repo.Get(ctx, branchID)
}

// Create menambahkan cabang baru.
func (s *Service) Create(ctx context.Context, actor auth.Actor, in NewBranch) (Branch, error) {
	if !actor.Can(auth.CapAdminBranches) {
		return Branch{}, ErrForbidden
	}
	next := Branch{
		Nama:           strings.TrimSpace(in.Nama),
		NamaKepala:     strings.TrimSpace(in.NamaKepala),
		Email:          NormalizeEmail(in.Email),
		JabatanKepala:  strings.TrimSpace(in.JabatanKepala),
		Telepon:        strings.TrimSpace(in.Telepon),
		IsCNC:          in.IsCNC,
		IsTempo:        in.IsTempo,
		AllowTransfer:  !in.IsCNC && !in.IsTempo,
		TanggalBayar:   in.TanggalBayar,
		TahanBilaTelat: in.TahanBilaTelat,
		PajakIn:        in.PajakIn,
		IsActive:       true,
	}
	if in.AllowTransfer != nil {
		next.AllowTransfer = *in.AllowTransfer
	}
	next.IdentityStatus = identityStatus(next, false)
	if in.IsActive != nil {
		next.IsActive = *in.IsActive
	}
	if err := validate(next); err != nil {
		return Branch{}, err
	}
	if err := s.ensureEmailFree(ctx, next.Email, ""); err != nil {
		return Branch{}, err
	}

	rec := audit.NewEntry(ctx, actor, audit.EntityBranch, "", audit.ActionCreate, nil, next)
	id, err := s.repo.Create(ctx, next, rec)
	if err != nil {
		return Branch{}, err
	}
	next.ID = id
	return next, nil
}

// Update mengubah cabang. Email baru harus bebas; bila cabang sudah punya
// akun, email akunnya ikut berubah karena email cabang = email login.
func (s *Service) Update(ctx context.Context, actor auth.Actor, id string, p Patch) (Branch, error) {
	if !actor.Can(auth.CapAdminBranches) {
		return Branch{}, ErrForbidden
	}
	if id == "" {
		return Branch{}, ErrNotFound
	}
	p = normalizePatch(p)
	return s.repo.Update(ctx, id, p, actor)
}

// ApplyPatch menerapkan perubahan sebagian terhadap snapshot yang sudah
// dikunci repository. Fungsi ini diekspor agar implementasi repository dapat
// menjaga logika domain yang sama tanpa membaca state sebelum transaksi.
func ApplyPatch(cur Branch, p Patch, hasAccount bool) (Branch, error) {
	next := cur
	identityChanged := (p.NamaKepala != nil && *p.NamaKepala != cur.NamaKepala) ||
		(p.Email != nil && *p.Email != cur.Email)
	if p.Nama != nil {
		next.Nama = *p.Nama
	}
	if p.NamaKepala != nil {
		next.NamaKepala = *p.NamaKepala
	}
	if p.Email != nil {
		next.Email = *p.Email
	}
	if p.JabatanKepala != nil {
		next.JabatanKepala = *p.JabatanKepala
	}
	if p.Telepon != nil {
		next.Telepon = *p.Telepon
	}
	if p.IsCNC != nil {
		next.IsCNC = *p.IsCNC
	}
	if p.IsTempo != nil {
		next.IsTempo = *p.IsTempo
	}
	if p.AllowTransfer != nil {
		next.AllowTransfer = *p.AllowTransfer
	} else if !next.IsCNC && !next.IsTempo {
		// Legacy clients represent transfer by clearing both old flags.
		next.AllowTransfer = true
	}
	if p.TanggalBayar != nil {
		next.TanggalBayar = *p.TanggalBayar
	}
	if p.TahanBilaTelat != nil {
		next.TahanBilaTelat = *p.TahanBilaTelat
	}
	if !next.IsTempo {
		next.TanggalBayar = 0
		next.TahanBilaTelat = false
	}
	if p.PajakIn != nil {
		next.PajakIn = *p.PajakIn
	}
	if p.IsActive != nil {
		next.IsActive = *p.IsActive
	}
	// Status identity_incomplete dapat menandai akses yang sengaja dicabut
	// walaupun nama dan email masih tersimpan. PUT master cabang hanya mengubah
	// atribut; ia bukan workflow pemulihan identitas/credential terverifikasi.
	// Karena itu state incomplete hanya boleh tetap incomplete (termasuk saat
	// nilai identitas benar-benar berubah). State ready/registered tetap boleh
	// turun menjadi incomplete saat atribut wajib dikosongkan.
	if identityChanged && cur.IdentityStatus != IdentityIncomplete {
		next.IdentityStatus = identityStatus(next, hasAccount)
	}
	if err := validate(next); err != nil {
		return Branch{}, err
	}
	return next, nil
}

func normalizePatch(p Patch) Patch {
	trim := func(v *string) *string {
		if v == nil {
			return nil
		}
		n := strings.TrimSpace(*v)
		return &n
	}
	p.Nama = trim(p.Nama)
	p.NamaKepala = trim(p.NamaKepala)
	p.Email = trim(p.Email)
	if p.Email != nil {
		n := NormalizeEmail(*p.Email)
		p.Email = &n
	}
	p.JabatanKepala = trim(p.JabatanKepala)
	p.Telepon = trim(p.Telepon)
	return p
}

// Delete menghapus cabang yang belum punya pesanan. Cabang yang sudah punya
// pesanan hanya boleh dinonaktifkan (PRD AP-CAB-04).
func (s *Service) Delete(ctx context.Context, actor auth.Actor, id string) error {
	if !actor.Can(auth.CapAdminBranches) {
		return ErrForbidden
	}
	if id == "" {
		return ErrNotFound
	}
	cur, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	adaPesanan, err := s.repo.HasOrders(ctx, id)
	if err != nil {
		return err
	}
	if adaPesanan {
		return ErrHasOrders
	}

	rec := audit.NewEntry(ctx, actor, audit.EntityBranch, id, audit.ActionDelete, cur, nil)
	return s.repo.Delete(ctx, id, rec)
}

// GenerateToken membuat token registrasi baru untuk cabang yang BELUM punya
// akun. Token lama yang belum terpakai otomatis batal (PRD AP-CAB-02), dan
// nilai asli token hanya dikembalikan sekali ini.
func (s *Service) GenerateToken(ctx context.Context, actor auth.Actor, id string, ttl time.Duration) (IssuedToken, error) {
	if !actor.Can(auth.CapAdminBranches) {
		return IssuedToken{}, ErrForbidden
	}
	if id == "" {
		return IssuedToken{}, ErrNotFound
	}
	if ttl <= 0 {
		return IssuedToken{}, ErrInvalidData
	}
	cur, err := s.repo.Get(ctx, id)
	if err != nil {
		return IssuedToken{}, err
	}
	// Cabang yang sudah punya akun diperiksa lebih dulu: pesan "sudah punya
	// akun" lebih tepat daripada "nonaktif" untuk cabang yang kedua-duanya.
	punyaAkun, err := s.repo.HasAccount(ctx, id)
	if err != nil {
		return IssuedToken{}, err
	}
	if punyaAkun {
		return IssuedToken{}, ErrHasAccount
	}
	if !cur.IsActive {
		return IssuedToken{}, ErrInactive
	}
	if cur.IdentityStatus != IdentityReady || cur.Email == "" || cur.NamaKepala == "" {
		return IssuedToken{}, ErrIdentityIncomplete
	}

	token, prefix, err := auth.NewRegistrationToken()
	if err != nil {
		return IssuedToken{}, err
	}
	expiresAt := s.clock.Now().Add(ttl)

	var before any
	if cur.TokenPrefix != "" {
		before = map[string]any{"token_prefix": cur.TokenPrefix, "berlaku_sampai": cur.TokenExpiresAt}
	}
	after := map[string]any{"token_prefix": prefix, "berlaku_sampai": expiresAt}
	rec := audit.NewEntry(ctx, actor, audit.EntityBranch, id, audit.ActionTokenGenerate, before, after)

	if err := s.repo.IssueToken(ctx, id, auth.HashRegistrationToken(token), prefix, expiresAt, rec); err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Token: token, Prefix: prefix, ExpiresAt: expiresAt}, nil
}

// ensureEmailFree menolak email yang sudah dipakai cabang lain maupun akun
// admin. Akun admin diperiksa karena login mencari admin lebih dulu: bila
// emailnya dipegang admin, akun cabang dengan email itu tidak akan pernah
// bisa masuk.
func (s *Service) ensureEmailFree(ctx context.Context, email, exceptID string) error {
	if email == "" {
		return nil
	}
	taken, err := s.repo.EmailTaken(ctx, email, exceptID)
	if err != nil {
		return err
	}
	if taken {
		return ErrEmailTaken
	}
	takenAdmin, err := s.repo.AdminEmailTaken(ctx, email)
	if err != nil {
		return err
	}
	if takenAdmin {
		return ErrEmailTaken
	}
	return nil
}

// validate memeriksa invarian data cabang yang tidak dijaga database.
func validate(b Branch) error {
	switch {
	case len([]rune(b.Nama)) < 2:
		return fmt.Errorf("%w: nama cabang minimal 2 karakter", ErrInvalidData)
	case len([]rune(b.Nama)) > 120:
		return fmt.Errorf("%w: nama cabang maksimal 120 karakter", ErrInvalidData)
	case b.NamaKepala != "" && len([]rune(b.NamaKepala)) < 2:
		return fmt.Errorf("%w: nama kepala cabang minimal 2 karakter", ErrInvalidData)
	case len([]rune(b.NamaKepala)) > 120:
		return fmt.Errorf("%w: nama kepala cabang maksimal 120 karakter", ErrInvalidData)
	case b.Email != "" && !emailPattern.MatchString(b.Email):
		return fmt.Errorf("%w: format email cabang tidak valid", ErrInvalidData)
	case len(b.Email) > 254:
		return fmt.Errorf("%w: email cabang terlalu panjang", ErrInvalidData)
	case len([]rune(b.Telepon)) > 40:
		return fmt.Errorf("%w: telepon terlalu panjang", ErrInvalidData)
	case len([]rune(b.JabatanKepala)) > 120:
		return fmt.Errorf("%w: jabatan kepala terlalu panjang", ErrInvalidData)
	case !b.AllowTransfer && !b.IsCNC && !b.IsTempo:
		return fmt.Errorf("%w: pilih minimal satu cara pembayaran", ErrInvalidData)
	case b.IsTempo && (b.TanggalBayar < 1 || b.TanggalBayar > 28):
		return fmt.Errorf("%w: tanggal bayar Tempo harus 1–28", ErrInvalidData)
	case !b.IsTempo && (b.TanggalBayar != 0 || b.TahanBilaTelat):
		return fmt.Errorf("%w: tanggal bayar dan penahanan hanya untuk Tempo", ErrInvalidData)
	}
	return nil
}

func identityStatus(b Branch, registered bool) string {
	if b.NamaKepala == "" || b.Email == "" {
		return IdentityIncomplete
	}
	if registered {
		return IdentityRegistered
	}
	return IdentityReady
}

// NormalizeEmail menyeragamkan email cabang (huruf kecil, tanpa spasi tepi).
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
