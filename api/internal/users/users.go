// Package users mengelola akun pengguna Admin Portal: akun cabang (PRD §4.8
// tab "User cabang") dan akun admin Pusat (tab "User admin").
//
// Aturan yang ditegakkan di sini, bukan hanya disembunyikan di UI:
//   - Manager hanya boleh mengelola akun CABANG; akun admin khusus Superadmin
//     (PRD §2.1).
//   - Admin yang masih berstatus undangan (belum diaktivasi) tidak bisa login
//     dan tidak menerima email dokumen (PRD AP-USR-02); penerima dokumen
//     ditentukan flag per admin (AP-USR-01).
//   - Superadmin aktif TERAKHIR tidak boleh dinonaktifkan atau diturunkan
//     role-nya, termasuk bila dua permintaan datang bersamaan (AP-USR-03).
//     "Aktif" di sini berarti akun benar-benar BISA LOGIN (is_active DAN sudah
//     diaktivasi): superadmin yang masih berstatus undangan tidak dihitung
//     sebagai pelindung, karena ia sendiri tidak bisa masuk.
//
// Email undangan TIDAK dikirim langsung dari paket ini: ia diantre lewat
// interface EmailQueue yang implementasi produksinya menulis email_outbox
// (SystemDesign.md §5.1, kartu F1-16).
package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Status akun pada daftar User management (PRD §4.8).
const (
	// StatusAktif: akun ada dan boleh login.
	StatusAktif = "aktif"
	// StatusBelumRegistrasi: cabang belum punya akun sama sekali.
	StatusBelumRegistrasi = "belum_registrasi"
	// StatusNonaktif: akun ada tetapi tidak boleh login (akun atau cabangnya
	// dinonaktifkan).
	StatusNonaktif = "nonaktif"
	// StatusUndangan: admin sudah diundang tetapi belum mengaktivasi akunnya.
	StatusUndangan = "undangan"
)

// DocKind adalah jenis dokumen yang dikirim ke admin ber-flag (PRD §6.1).
type DocKind string

// Jenis dokumen yang punya daftar penerima admin.
const (
	DocInvoice    DocKind = "invoice"
	DocSuratJalan DocKind = "surat_jalan"
)

// EmailKind adalah nilai kolom email_outbox.jenis untuk email yang diantre
// paket ini (Database.md §4.7).
type EmailKind string

// JenisUndanganAdmin adalah email undangan admin baru (PRD §6.2).
const JenisUndanganAdmin EmailKind = "undangan_admin"

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrForbidden: aktor tidak punya kapabilitas yang dibutuhkan.
	ErrForbidden = errors.New("tidak berhak mengelola akun pengguna")
	// ErrNotFound: akun tidak ditemukan.
	ErrNotFound = errors.New("akun tidak ditemukan")
	// ErrEmailTaken: email sudah dipakai akun lain.
	ErrEmailTaken = errors.New("email sudah dipakai akun lain")
	// ErrLastSuperadmin: superadmin aktif terakhir dilindungi (PRD AP-USR-03).
	ErrLastSuperadmin = errors.New("superadmin aktif terakhir tidak boleh dinonaktifkan atau diturunkan")
	// ErrNoAccount: cabang belum punya akun (belum registrasi).
	ErrNoAccount = errors.New("cabang belum punya akun")
	// ErrNotInvited: akun admin bukan undangan yang menunggu aktivasi (sudah
	// aktif atau nonaktif) sehingga tautan undangan tidak berlaku untuknya.
	ErrNotInvited = errors.New("akun admin ini bukan undangan yang menunggu aktivasi")
	// ErrInvalidData: data yang dikirim tidak lolos validasi domain.
	ErrInvalidData = errors.New("data pengguna tidak valid")
)

// BranchUser adalah satu baris tab "User cabang": data cabang beserta akunnya.
// Sebuah cabang tetap tampil walau belum punya akun (status belum_registrasi),
// karena 1 cabang = 1 akun dan akun dibuat lewat token registrasi (PRD BR-01).
type BranchUser struct {
	BranchID     string `json:"branch_id"`
	UserID       string `json:"user_id,omitempty"`
	Nama         string `json:"nama"`
	NamaKepala   string `json:"nama_kepala"`
	Email        string `json:"email"`
	Telepon      string `json:"telepon,omitempty"`
	HasAccount   bool   `json:"has_account"`
	IsActive     bool   `json:"is_active"`
	BranchActive bool   `json:"branch_active"`
	Status       string `json:"status"`
	// LastLoginAt sengaja tanpa omitempty: kolom "login terakhir" harus selalu
	// ada di respons (null bila belum pernah login) agar tabel UI konsisten.
	LastLoginAt *time.Time `json:"last_login_at"`
	// TokenPrefix adalah 8 karakter pertama token registrasi yang masih
	// berlaku (bila ada); nilai token aslinya hanya tampil sekali saat dibuat
	// (PRD §4.4).
	TokenPrefix    string     `json:"token_prefix,omitempty"`
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty"`
}

// AdminUser adalah satu baris tab "User admin".
type AdminUser struct {
	ID              string     `json:"id"`
	Nama            string     `json:"nama"`
	Email           string     `json:"email"`
	Role            auth.Role  `json:"role"`
	NotifInvoice    bool       `json:"notif_invoice"`
	NotifSuratJalan bool       `json:"notif_surat_jalan"`
	IsActive        bool       `json:"is_active"`
	Status          string     `json:"status"`
	InvitedAt       *time.Time `json:"invited_at,omitempty"`
	ActivatedAt     *time.Time `json:"activated_at,omitempty"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty"`
}

// NewAdmin adalah permintaan undangan admin baru.
type NewAdmin struct {
	Nama            string
	Email           string
	Role            auth.Role
	NotifInvoice    bool
	NotifSuratJalan bool
}

// AdminPatch adalah perubahan admin. Field nil berarti tidak diubah.
type AdminPatch struct {
	Nama            *string
	Role            *auth.Role
	NotifInvoice    *bool
	NotifSuratJalan *bool
	IsActive        *bool
}

// BranchRepository menyimpan dan memutasi akun cabang.
//
// Setiap mutasi menerima entri audit yang WAJIB disimpan implementasinya di
// dalam transaksi yang sama dengan perubahannya (F2-28); daftar kosong berarti
// tidak ada catatan yang perlu ditulis (mis. permintaan no-op).
type BranchRepository interface {
	ListBranchUsers(ctx context.Context) ([]BranchUser, error)
	BranchUserByID(ctx context.Context, id string) (BranchUser, error)
	SetBranchUserActive(ctx context.Context, id string, active bool, recs []audit.Entry) (BranchUser, error)
	// EmailTakenByBranch melaporkan apakah email sudah dipakai cabang, baik
	// sebagai email login akun cabang (branch_users) maupun email cabang itu
	// sendiri (branches, termasuk cabang yang belum registrasi).
	EmailTakenByBranch(ctx context.Context, email string) (bool, error)
}

// AdminRepository menyimpan dan memutasi akun admin.
//
// UpdateAdmin WAJIB atomik dan menegakkan larangan menurunkan/menonaktifkan
// superadmin aktif terakhir, termasuk untuk permintaan yang datang bersamaan;
// pelanggaran mengembalikan ErrLastSuperadmin. "Aktif" berarti akun bisa
// login (is_active DAN sudah diaktivasi) — superadmin berstatus undangan
// bukan pelindung.
type AdminRepository interface {
	ListAdmins(ctx context.Context) ([]AdminUser, error)
	AdminByID(ctx context.Context, id string) (AdminUser, error)
	AdminByEmail(ctx context.Context, email string) (AdminUser, error)
	CreateAdmin(ctx context.Context, in NewAdmin, recs []audit.Entry) (AdminUser, error)
	UpdateAdmin(ctx context.Context, next AdminUser, recs []audit.Entry) (AdminUser, error)
	// DocumentRecipients mengembalikan email admin yang berhak menerima
	// dokumen jenis tertentu: aktif, sudah diaktivasi, dan flag-nya menyala.
	DocumentRecipients(ctx context.Context, kind DocKind) ([]string, error)
}

// Repository adalah seluruh penyimpanan yang dibutuhkan service ini.
type Repository interface {
	BranchRepository
	AdminRepository
}

// PasswordResetter membuat tautan atur ulang / aktivasi akun. Implementasinya
// adalah auth.Service; pemindahan email ke outbox (F1-16) terjadi di sana.
//
// audit, bila terisi, diserahkan ke jalur token supaya tautan baru dan entri
// auditnya commit bersama di dalam satu transaksi (F2-28): kegagalan menulis
// audit membatalkan tautannya, jadi tidak ada tautan terbit tanpa catatan dan
// permintaan bisa diulang dengan aman. Nil dipakai saat auditnya sudah ditulis
// pemanggil di transaksinya sendiri (undangan admin baru).
type PasswordResetter interface {
	SendPasswordResetFor(ctx context.Context, kind auth.Kind, userID string, ttl time.Duration, audit *auth.ResetAudit) error
	IssuePasswordResetFor(ctx context.Context, kind auth.Kind, userID string, ttl time.Duration, audit *auth.ResetAudit) (auth.IssuedReset, error)
}

// SessionRevoker mencabut seluruh sesi satu akun, dipakai saat akun
// dinonaktifkan supaya sesi yang sedang berjalan ikut gugur.
type SessionRevoker interface {
	RevokeAllForUser(ctx context.Context, userID string) error
}

// QueuedEmail adalah satu email transaksional yang diantre. Payload menjadi
// data template saat F1-16 merender dan mengirimnya.
type QueuedEmail struct {
	Jenis   EmailKind      `json:"jenis"`
	To      []string       `json:"to_addr"`
	Subject string         `json:"subject"`
	Payload map[string]any `json:"payload"`
}

// EmailQueue mengantre email transaksional (email_outbox, SystemDesign §5.1).
// Paket ini tidak pernah mengirim email sendiri.
type EmailQueue interface {
	Enqueue(ctx context.Context, m QueuedEmail) error
}

// LogQueue hanya mencatat antrean email. Dipakai sampai penulis email_outbox
// (F1-16) terpasang; email TIDAK dikirim dari sini.
type LogQueue struct{}

// Enqueue mencatat antrean ke log.
func (LogQueue) Enqueue(_ context.Context, m QueuedEmail) error {
	slog.Info("email diantre (outbox F1-16 belum terpasang)",
		"jenis", string(m.Jenis), "ke", strings.Join(m.To, ","), "subjek", m.Subject)
	return nil
}

// Service adalah logika bisnis pengelolaan akun.
type Service struct {
	repo     Repository
	resets   PasswordResetter
	sessions SessionRevoker
	queue    EmailQueue
	resetURL auth.ResetURLBuilder
}

// Option menyesuaikan Service.
type Option func(*Service)

// WithEmailQueue memasang antrean email undangan (wajib diisi di produksi).
func WithEmailQueue(q EmailQueue) Option {
	return func(s *Service) {
		if q != nil {
			s.queue = q
		}
	}
}

// WithResetURLBuilder memasang penyusun tautan atur ulang/aktivasi.
func WithResetURLBuilder(b auth.ResetURLBuilder) Option {
	return func(s *Service) {
		if b != nil {
			s.resetURL = b
		}
	}
}

// NewService membuat service pengelolaan akun pengguna.
func NewService(repo Repository, resets PasswordResetter, sessions SessionRevoker, opts ...Option) *Service {
	s := &Service{repo: repo, resets: resets, sessions: sessions, queue: LogQueue{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// AdminSnapshot menyusun snapshot audit satu akun admin.
//
// Sengaja TIDAK memuat email: audit log mencatat APA yang berubah pada akun,
// bukan identitas loginnya, sehingga tidak ada PII yang tersimpan di diff
// (kartu F2-28).
func AdminSnapshot(a AdminUser) map[string]any {
	return map[string]any{
		"nama":              a.Nama,
		"role":              string(a.Role),
		"notif_invoice":     a.NotifInvoice,
		"notif_surat_jalan": a.NotifSuratJalan,
		"is_active":         a.IsActive,
		"status":            a.Status,
	}
}

// BranchUserSnapshot menyusun snapshot audit satu akun cabang. Sama seperti
// AuditSnapshot admin, email sengaja tidak ikut dicatat.
func BranchUserSnapshot(b BranchUser) map[string]any {
	return map[string]any{
		"branch_id":     b.BranchID,
		"user_id":       b.UserID,
		"nama":          b.Nama,
		"is_active":     b.IsActive,
		"branch_active": b.BranchActive,
		"status":        b.Status,
	}
}

// BranchStatus menentukan status akun cabang yang ditampilkan di daftar.
//
// "nonaktif" dipakai bila akun ATAU cabangnya nonaktif, karena kedua keadaan
// itu sama-sama membuat akun tidak bisa login (aturan boleh_login).
func BranchStatus(hasAccount, userActive, branchActive bool) string {
	switch {
	case !hasAccount:
		return StatusBelumRegistrasi
	case userActive && branchActive:
		return StatusAktif
	default:
		return StatusNonaktif
	}
}

// AdminStatus menentukan status akun admin yang ditampilkan di daftar:
// undangan (belum diaktivasi), aktif, atau nonaktif.
func AdminStatus(isActive, activated bool) string {
	switch {
	case !isActive:
		return StatusNonaktif
	case !activated:
		return StatusUndangan
	default:
		return StatusAktif
	}
}

// BranchUsers mengembalikan daftar akun cabang (PRD §4.8). Manager maupun
// Superadmin boleh melihatnya (PRD §2.1).
func (s *Service) BranchUsers(ctx context.Context, actor auth.Actor) ([]BranchUser, error) {
	if !actor.Can(auth.CapAdminUsersBranch) {
		return nil, ErrForbidden
	}
	rows, err := s.repo.ListBranchUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("daftar akun cabang: %w", err)
	}
	return rows, nil
}

// SetBranchUserActive mengaktifkan atau menonaktifkan akun cabang. Menonaktifkan
// akun langsung mencabut seluruh sesinya supaya akses berhenti saat itu juga
// (PRD AP-CAB-03).
//
// Mengirim status yang sama dengan yang berlaku adalah no-op: tidak ada
// perubahan, tidak ada entri audit (F2-28). Satu perubahan nyata menghasilkan
// tepat satu entri `branch_user/update` di dalam transaksi yang sama.
func (s *Service) SetBranchUserActive(ctx context.Context, actor auth.Actor, id string, active bool) (BranchUser, error) {
	if !actor.Can(auth.CapAdminUsersBranch) {
		return BranchUser{}, ErrForbidden
	}
	if id == "" {
		return BranchUser{}, ErrNotFound
	}
	cur, err := s.repo.BranchUserByID(ctx, id)
	if err != nil {
		return BranchUser{}, err
	}
	if cur.IsActive == active {
		return cur, nil
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityBranchUser, cur.UserID, audit.ActionUpdate, nil, nil)
	u, err := s.repo.SetBranchUserActive(ctx, id, active, []audit.Entry{rec})
	if err != nil {
		return BranchUser{}, err
	}
	if !active && u.UserID != "" {
		if err := s.sessions.RevokeAllForUser(ctx, u.UserID); err != nil {
			return BranchUser{}, fmt.Errorf("cabut sesi akun cabang: %w", err)
		}
	}
	return u, nil
}

// ResendBranchReset mengirim ulang tautan atur ulang kata sandi akun cabang
// (PRD §4.8). Email dikirim lewat jalur yang sama dengan lupa kata sandi
// sehingga F1-16 cukup mengalihkannya ke email_outbox di satu tempat.
//
// Tautan lahir di repositori auth (domain lain), jadi entri auditnya
// diserahkan ke jalur token itu dan ditulis DI DALAM transaksi yang sama
// dengan token barunya (F2-28): kegagalan menulis audit membatalkan tokennya,
// permintaan gagal tanpa efek samping, dan pengulangan aman. Snapshot-nya
// hanya memuat id akun/cabang — tidak pernah nilai tautannya.
func (s *Service) ResendBranchReset(ctx context.Context, actor auth.Actor, id string, ttl time.Duration) error {
	if !actor.Can(auth.CapAdminUsersBranch) {
		return ErrForbidden
	}
	u, err := s.repo.BranchUserByID(ctx, id)
	if err != nil {
		return err
	}
	if !u.HasAccount || u.UserID == "" {
		return ErrNoAccount
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityBranchUser, u.UserID, audit.ActionReset, nil,
		map[string]any{"branch_id": u.BranchID, "user_id": u.UserID})
	if err := s.resets.SendPasswordResetFor(ctx, auth.KindBranch, u.UserID, ttl, resetAudit(rec)); err != nil {
		return fmt.Errorf("kirim tautan atur ulang akun cabang: %w", err)
	}
	return nil
}

// Admins mengembalikan daftar akun admin — hanya Superadmin (PRD §2.1).
func (s *Service) Admins(ctx context.Context, actor auth.Actor) ([]AdminUser, error) {
	if !actor.Can(auth.CapAdminUsersAdmin) {
		return nil, ErrForbidden
	}
	rows, err := s.repo.ListAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("daftar akun admin: %w", err)
	}
	return rows, nil
}

// InviteAdmin mengundang admin baru: akun dibuat TANPA kata sandi (belum bisa
// login dan belum menerima email dokumen), lalu tautan aktivasi sekali pakai
// diantre lewat outbox undangan (PRD §4.8, §6.2). ttl adalah masa berlaku
// tautan aktivasi. Email yang sudah dipakai akun admin MAUPUN cabang ditolak
// dengan ErrEmailTaken.
func (s *Service) InviteAdmin(ctx context.Context, actor auth.Actor, in NewAdmin, ttl time.Duration) (AdminUser, error) {
	if !actor.Can(auth.CapAdminUsersAdmin) {
		return AdminUser{}, ErrForbidden
	}
	in.Nama = strings.TrimSpace(in.Nama)
	in.Email = NormalizeEmail(in.Email)
	if in.Nama == "" || in.Email == "" {
		return AdminUser{}, ErrInvalidData
	}
	if !ValidRole(in.Role) {
		return AdminUser{}, ErrInvalidData
	}
	if _, err := s.repo.AdminByEmail(ctx, in.Email); err == nil {
		return AdminUser{}, ErrEmailTaken
	} else if !errors.Is(err, ErrNotFound) {
		return AdminUser{}, err
	}
	// Email yang sudah dipakai cabang juga ditolak: login mencari akun admin
	// lebih dulu, jadi email milik cabang tidak boleh dipegang admin — kalau
	// tidak, akun cabang itu tidak akan pernah bisa masuk (kebalikan dari
	// pemeriksaan branch.AdminEmailTaken pada F1-06).
	takenBranch, err := s.repo.EmailTakenByBranch(ctx, in.Email)
	if err != nil {
		return AdminUser{}, err
	}
	if takenBranch {
		return AdminUser{}, ErrEmailTaken
	}

	admin, err := s.repo.CreateAdmin(ctx, in, []audit.Entry{
		audit.NewEntry(ctx, actor, audit.EntityAdminUser, "", audit.ActionCreate, nil, nil),
	})
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return AdminUser{}, ErrEmailTaken
		}
		return AdminUser{}, fmt.Errorf("buat undangan admin: %w", err)
	}

	// Catatan: CreateAdmin, tautan aktivasi, dan antrean email bukan satu
	// transaksi database (repository pengguna dan repository token terpisah).
	// Kegagalan di sini TIDAK boleh membiarkan undangan tanpa jalan pemulihan:
	// admin yang sudah terlanjur dibuat selalu bisa dikirim ulang tautannya
	// lewat ResendInviteAdmin, dan belum bisa login sampai mengaktivasi
	// (password_hash/activated_at NULL).
	if err := s.sendInvite(ctx, admin, ttl, nil); err != nil {
		return AdminUser{}, err
	}
	return admin, nil
}

// sendInvite membuat tautan aktivasi baru untuk seorang admin undangan lalu
// mengantre email undangannya lewat outbox. Tautan lama otomatis batal
// (satu tautan aktif per akun).
//
// audit, bila terisi, ditulis bersama tautan barunya (F2-28); email undangan
// diantre setelah token + audit commit.
func (s *Service) sendInvite(ctx context.Context, admin AdminUser, ttl time.Duration, audit *auth.ResetAudit) error {
	issued, err := s.resets.IssuePasswordResetFor(ctx, auth.KindAdmin, admin.ID, ttl, audit)
	if err != nil {
		return fmt.Errorf("buat tautan aktivasi admin: %w", err)
	}
	tautan := ""
	if s.resetURL != nil {
		tautan = s.resetURL(auth.KindAdmin, issued.Token)
	}
	if err := s.queue.Enqueue(ctx, QueuedEmail{
		Jenis:   JenisUndanganAdmin,
		To:      []string{admin.Email},
		Subject: "Undangan akses Admin Portal Grahita Indonesia",
		Payload: map[string]any{
			"template":       "undangan_admin",
			"nama":           admin.Nama,
			"email":          admin.Email,
			"role":           string(admin.Role),
			"tautan":         tautan,
			"berlaku_sampai": issued.ExpiresAt,
			"catatan":        "Buat kata sandi lewat tautan ini; akun baru aktif setelah kata sandi dibuat.",
		},
	}); err != nil {
		return fmt.Errorf("antre email undangan admin: %w", err)
	}
	return nil
}

// ResendInviteAdmin mengirim ulang (membuat ulang) tautan aktivasi untuk admin
// yang masih berstatus UNDANGAN — jalan pemulihan resmi bila antrean email
// undangan pertama gagal, tanpa harus menunggu admin itu tidak bisa apa-apa.
//
// Sifatnya idempoten dari sisi keadaan: pemanggilan berulang tidak membuat
// akun baru, dan selalu menyisakan tepat satu tautan aktif (tautan lama batal).
// Akun yang sudah aktif (atau nonaktif) ditolak dengan ErrNotInvited: mereka
// memakai alur lupa kata sandi biasa.
func (s *Service) ResendInviteAdmin(ctx context.Context, actor auth.Actor, id string, ttl time.Duration) (AdminUser, error) {
	if !actor.Can(auth.CapAdminUsersAdmin) {
		return AdminUser{}, ErrForbidden
	}
	if id == "" {
		return AdminUser{}, ErrNotFound
	}
	admin, err := s.repo.AdminByID(ctx, id)
	if err != nil {
		return AdminUser{}, err
	}
	if !admin.IsActive || admin.ActivatedAt != nil {
		return AdminUser{}, ErrNotInvited
	}
	// Undangan yang dikirim ulang adalah mutasi yang layak diaudit (F2-28):
	// aktor memicu ulang akses orang lain. Snapshot-nya hanya nama/role/status
	// — tidak ada email, kata sandi, maupun tautan aktivasi. Entri itu ditulis
	// bersama token barunya di jalur auth (satu transaksi), sehingga audit yang
	// gagal membatalkan tautannya.
	rec := audit.NewEntry(ctx, actor, audit.EntityAdminUser, admin.ID, audit.ActionResend, nil,
		map[string]any{"nama": admin.Nama, "role": string(admin.Role), "status": admin.Status})
	if err := s.sendInvite(ctx, admin, ttl, resetAudit(rec)); err != nil {
		return AdminUser{}, err
	}
	return admin, nil
}

// resetAudit menerjemahkan entri audit domain menjadi fakta audit paket auth
// yang ditulis bersama token atur ulang (F2-28). Paket auth tidak boleh
// mengimpor paket audit (audit mengimpor auth), jadi fakta itu diteruskan
// lewat tipe milik auth; snapshot sudah diredaksi NewEntry dan penulis audit
// (audit/postgres.Insert) mengulanginya sebagai jaring terakhir.
func resetAudit(e audit.Entry) *auth.ResetAudit {
	return &auth.ResetAudit{
		ActorKind: e.ActorKind,
		ActorID:   e.ActorID,
		ActorNama: e.ActorNama,
		IP:        e.IP,
		Entity:    e.Entity,
		EntityID:  e.EntityID,
		Action:    e.Action,
		Before:    e.Before,
		After:     e.After,
	}
}

// UpdateAdmin mengubah nama, role, flag notifikasi, dan/atau status aktif
// seorang admin (PRD §4.8). Menonaktifkan akun mencabut sesinya. Perlindungan
// superadmin aktif terakhir ditegakkan repository secara atomik.
//
// Permintaan yang tidak mengubah apa pun adalah no-op: tidak ada tulisan dan
// tidak ada entri audit. Satu perubahan nyata menghasilkan tepat satu entri
// `admin_user/update` (before/after tanpa email) di dalam transaksi yang sama
// (F2-28).
func (s *Service) UpdateAdmin(ctx context.Context, actor auth.Actor, id string, p AdminPatch) (AdminUser, error) {
	if !actor.Can(auth.CapAdminUsersAdmin) {
		return AdminUser{}, ErrForbidden
	}
	if id == "" {
		return AdminUser{}, ErrNotFound
	}
	cur, err := s.repo.AdminByID(ctx, id)
	if err != nil {
		return AdminUser{}, err
	}

	next := cur
	if p.Nama != nil {
		next.Nama = strings.TrimSpace(*p.Nama)
	}
	if p.Role != nil {
		if !ValidRole(*p.Role) {
			return AdminUser{}, ErrInvalidData
		}
		next.Role = *p.Role
	}
	if p.NotifInvoice != nil {
		next.NotifInvoice = *p.NotifInvoice
	}
	if p.NotifSuratJalan != nil {
		next.NotifSuratJalan = *p.NotifSuratJalan
	}
	if p.IsActive != nil {
		next.IsActive = *p.IsActive
	}
	if next.Nama == "" {
		return AdminUser{}, ErrInvalidData
	}
	if samaAdmin(cur, next) {
		return cur, nil
	}

	rec := audit.NewEntry(ctx, actor, audit.EntityAdminUser, next.ID, audit.ActionUpdate, nil, nil)
	updated, err := s.repo.UpdateAdmin(ctx, next, []audit.Entry{rec})
	if err != nil {
		return AdminUser{}, err
	}
	if cur.IsActive && !updated.IsActive && updated.ID != "" {
		if err := s.sessions.RevokeAllForUser(ctx, updated.ID); err != nil {
			return AdminUser{}, fmt.Errorf("cabut sesi admin nonaktif: %w", err)
		}
	}
	return updated, nil
}

// samaAdmin melaporkan apakah perubahan yang diminta tidak mengubah apa pun
// pada akun (dipakai untuk melewatkan tulisan + entri audit yang sia-sia).
func samaAdmin(a, b AdminUser) bool {
	return a.Nama == b.Nama && a.Role == b.Role && a.NotifInvoice == b.NotifInvoice &&
		a.NotifSuratJalan == b.NotifSuratJalan && a.IsActive == b.IsActive
}

// DocumentRecipients mengembalikan email penerima dokumen jenis tertentu.
// Admin yang masih undangan atau nonaktif TIDAK pernah masuk daftar ini
// (PRD AP-USR-01/AP-USR-02). Dipakai F1-16 saat mengirim dokumen.
func (s *Service) DocumentRecipients(ctx context.Context, kind DocKind) ([]string, error) {
	if kind != DocInvoice && kind != DocSuratJalan {
		return nil, fmt.Errorf("%w: jenis dokumen %q", ErrInvalidData, kind)
	}
	emails, err := s.repo.DocumentRecipients(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("penerima dokumen %s: %w", kind, err)
	}
	return emails, nil
}

// ValidRole melaporkan apakah role admin dikenal.
func ValidRole(r auth.Role) bool {
	return r == auth.RoleManager || r == auth.RoleSuperadmin
}

// NormalizeEmail menyeragamkan email akun (huruf kecil, tanpa spasi tepi).
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
