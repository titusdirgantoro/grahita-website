// Package registration mengelola pendaftaran akun cabang dua langkah lewat
// token registrasi sekali pakai (PRD §3.1, SystemDesign.md §4.1).
//
// Alur: klien memvalidasi token (langkah 1 — data cabang tampil read-only),
// lalu mengirim token + telepon + kata sandi (langkah 2). Data cabang TIDAK
// pernah diambil dari payload klien: nama cabang, nama kepala, dan email
// selalu dibaca ulang dari database lewat token, sehingga tidak bisa
// dipalsukan dari body permintaan.
//
// Aturan yang ditegakkan di sini — bukan hanya disembunyikan di UI:
//   - token hanya sah bila belum dipakai, belum kedaluwarsa, cabangnya
//     aktif, dan cabangnya belum punya akun (PRD CP-REG-02/06/07).
//   - token dipakai TEPAT sekali: langkah 2 mengunci baris token di dalam
//     transaksi, memeriksa ULANG keadaannya, barulah menandainya terpakai
//     (SystemDesign.md §4.1). Dua permintaan bersamaan hanya satu yang lolos.
//   - 1 cabang = 1 akun; dijaga di sini DAN oleh indeks unik
//     branch_users.branch_id di database.
package registration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrTokenInvalid: token tidak dikenal — salah ketik, bentuknya tidak sah,
	// atau token lama yang sudah digantikan token baru.
	ErrTokenInvalid = errors.New("token registrasi tidak valid")
	// ErrTokenExpired: token ada tetapi sudah lewat masa berlakunya.
	ErrTokenExpired = errors.New("token registrasi sudah kedaluwarsa")
	// ErrTokenUsed: token sudah pernah dipakai mendaftar.
	ErrTokenUsed = errors.New("token registrasi sudah dipakai")
	// ErrBranchInactive: cabang pemilik token sedang nonaktif.
	ErrBranchInactive = errors.New("cabang nonaktif")
	// ErrBranchHasAccount: cabang sudah punya akun (1 cabang = 1 akun).
	ErrBranchHasAccount = errors.New("cabang sudah punya akun")
	// ErrIdentityIncomplete: identitas wajib belum lengkap.
	ErrIdentityIncomplete = errors.New("identitas cabang belum lengkap")
	// ErrEmailTaken: email cabang sudah dipakai akun lain (admin atau cabang).
	ErrEmailTaken = errors.New("email cabang sudah dipakai akun lain")
	// ErrTeleponInvalid: nomor telepon kosong atau melebihi batas panjang.
	ErrTeleponInvalid = errors.New("nomor telepon tidak valid")
	// ErrPasswordMismatch: konfirmasi kata sandi tidak sama dengan kata sandi.
	ErrPasswordMismatch = errors.New("konfirmasi kata sandi tidak sama")
)

// TeleponMaxLen adalah batas panjang nomor telepon akun cabang.
const TeleponMaxLen = 40

// BranchData adalah data cabang yang ditampilkan terisi dan READ-ONLY pada
// langkah 2 (PRD CP-REG-03). Nilainya selalu berasal dari database.
type BranchData struct {
	NamaCabang string `json:"nama_cabang"`
	NamaKepala string `json:"nama_kepala"`
	Email      string `json:"email"`
}

// Snapshot adalah keadaan satu token registrasi beserta cabangnya pada saat
// dibaca. Repository mengisinya; kelayakannya dinilai oleh Cek supaya aturan
// pesan tetap satu tempat dan sama untuk langkah 1 maupun langkah 2.
type Snapshot struct {
	TokenID   string
	BranchID  string
	ExpiresAt time.Time
	// UsedAt terisi bila token sudah dipakai.
	UsedAt *time.Time
	// Data cabang (read-only bagi pengguna).
	Nama           string
	NamaKepala     string
	Email          string
	IdentityStatus string
	// BranchActive false berarti cabangnya nonaktif.
	BranchActive bool
	// BranchHasAccount true berarti cabang sudah punya akun.
	BranchHasAccount bool
}

// Data mengembalikan data cabang yang boleh dilihat pengguna pada langkah 2.
func (s Snapshot) Data() BranchData {
	return BranchData{NamaCabang: s.Nama, NamaKepala: s.NamaKepala, Email: s.Email}
}

// Cek menilai kelayakan token pada waktu now. Urutan pemeriksaannya sengaja
// tetap: token terpakai lebih dulu (paling spesifik), lalu kedaluwarsa, lalu
// kondisi cabangnya.
func (s Snapshot) Cek(now time.Time) error {
	switch {
	case s.UsedAt != nil:
		return ErrTokenUsed
	case !now.Before(s.ExpiresAt):
		return ErrTokenExpired
	case !s.BranchActive:
		return ErrBranchInactive
	case s.BranchHasAccount:
		return ErrBranchHasAccount
	case s.IdentityStatus != "ready" || s.NamaKepala == "" || s.Email == "":
		return ErrIdentityIncomplete
	}
	return nil
}

// RegisterInput adalah permintaan pemakaian token (langkah 2). Field data
// cabang tidak ada di sini dengan sengaja: nilainya tidak boleh datang dari
// klien (PRD CP-REG-03).
type RegisterInput struct {
	Token              string
	Telepon            string
	Password           string
	PasswordKonfirmasi string
}

// RegisterRequest adalah data yang SUDAH tervalidasi dan siap disimpan
// repository di dalam satu transaksi.
type RegisterRequest struct {
	TokenHash    []byte
	Telepon      string
	PasswordHash string
	Now          time.Time
}

// Akun adalah akun cabang yang baru dibuat.
type Akun struct {
	UserID   string
	BranchID string
	Nama     string
	Email    string
	Telepon  string
}

// Repository menyimpan pembacaan token registrasi dan pembuatan akun cabang.
type Repository interface {
	// Lookup memuat keadaan token berdasarkan hash-nya tanpa mengunci baris.
	// Token yang tidak ada dikembalikan sebagai ErrTokenInvalid.
	Lookup(ctx context.Context, tokenHash []byte) (Snapshot, error)
	// Register mengunci baris token (FOR UPDATE), memeriksa ULANG
	// kelayakannya, membuat akun cabang, menandai token terpakai, dan
	// menulis entri audit — semuanya dalam SATU transaksi. rec diisi
	// pemanggil; repository melengkapinya dengan id akun yang baru dibuat.
	Register(ctx context.Context, req RegisterRequest, rec audit.Entry) (Akun, error)
}

// Service adalah logika bisnis pendaftaran akun cabang.
type Service struct {
	repo  Repository
	clock clock.Clock
}

// NewService membuat service pendaftaran.
func NewService(repo Repository, c clock.Clock) *Service {
	if c == nil {
		c = clock.System()
	}
	return &Service{repo: repo, clock: c}
}

// Validate menangani langkah 1: memeriksa token dan mengembalikan data cabang
// yang akan tampil read-only di langkah 2.
func (s *Service) Validate(ctx context.Context, rawToken string) (BranchData, error) {
	if !auth.ValidRegistrationTokenFormat(auth.NormalizeRegistrationToken(rawToken)) {
		return BranchData{}, ErrTokenInvalid
	}
	snap, err := s.repo.Lookup(ctx, auth.HashRegistrationToken(rawToken))
	if err != nil {
		return BranchData{}, err
	}
	if err := snap.Cek(s.clock.Now()); err != nil {
		return BranchData{}, err
	}
	return snap.Data(), nil
}

// Register menangani langkah 2: memakai token lalu membuat akun cabang.
//
// Kata sandi dan konfirmasinya divalidasi SEBELUM token disentuh, sehingga
// percobaan dengan konfirmasi salah atau kata sandi lemah tidak membakar
// token yang masih berlaku (perilaku yang sama dengan tautan atur ulang).
func (s *Service) Register(ctx context.Context, in RegisterInput, meta auth.RequestMeta) (Akun, error) {
	telepon := strings.TrimSpace(in.Telepon)
	if telepon == "" || len([]rune(telepon)) > TeleponMaxLen {
		return Akun{}, fmt.Errorf("%w: wajib diisi, maksimal %d karakter", ErrTeleponInvalid, TeleponMaxLen)
	}
	if in.Password != in.PasswordKonfirmasi {
		return Akun{}, ErrPasswordMismatch
	}
	if err := auth.CheckPasswordStrength(in.Password); err != nil {
		return Akun{}, err
	}
	normalized := auth.NormalizeRegistrationToken(in.Token)
	if !auth.ValidRegistrationTokenFormat(normalized) {
		return Akun{}, ErrTokenInvalid
	}

	now := s.clock.Now()
	tokenHash := auth.HashRegistrationToken(in.Token)

	// Pemeriksaan awal TANPA kunci: memberi pesan yang spesifik dan menolak
	// token sampah tanpa membuka transaksi. Pemeriksaan ini tidak menggantikan
	// pemeriksaan ulang di dalam transaksi — dua permintaan bersamaan bisa
	// sama-sama lolos di sini, dan yang kedua baru tertahan oleh kunci baris.
	snap, err := s.repo.Lookup(ctx, tokenHash)
	if err != nil {
		return Akun{}, err
	}
	if err := snap.Cek(now); err != nil {
		return Akun{}, err
	}

	passwordHash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Akun{}, err
	}

	rec := audit.Entry{
		ActorKind: auth.KindBranch,
		ActorNama: snap.Nama,
		IP:        meta.IP,
		Entity:    audit.EntityBranchUser,
		EntityID:  snap.BranchID,
		Action:    audit.ActionRegister,
		After: map[string]any{
			"branch_id": snap.BranchID,
			"nama":      snap.Nama,
			"email":     snap.Email,
		},
	}
	return s.repo.Register(ctx, RegisterRequest{
		TokenHash:    tokenHash,
		Telepon:      telepon,
		PasswordHash: passwordHash,
		Now:          now,
	}, rec)
}
