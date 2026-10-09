// Package httpapi adalah satu-satunya lapisan transport HTTP.
//
// Aturan ketergantungan (Architecture.md, SystemDesign.md §2):
//
//	httpapi -> service -> repository -> database
//
// Paket ini boleh mengimpor paket service (auth, branch, settings, catalog,
// order) dan platform, tetapi TIDAK boleh mengimpor repository (*/postgres)
// maupun paket query hasil sqlc. Aturan ini ditegakkan otomatis oleh pengujian
// di internal/arch.
package httpapi

import (
	"context"
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/backup"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/dashboard"
	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/notification"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/registration"
	"github.com/grahita/portal/api/internal/report"
	"github.com/grahita/portal/api/internal/settings"
	"github.com/grahita/portal/api/internal/users"
)

// HealthChecker adalah dependensi yang diperiksa /readyz. Implementasinya
// ada di cmd/server supaya paket ini tidak menyentuh pgx.
type HealthChecker interface {
	Ping(ctx context.Context) error
	MigrationVersion(ctx context.Context) (int64, error)
}

// Deps adalah seluruh dependensi router.
type Deps struct {
	Config  config.Config
	Version string

	Health   HealthChecker
	Auth     *auth.Service
	Settings *settings.Service
	Catalog  *catalog.Service
	Users    *users.Service
	Branches *branch.Service
	Bank     *bank.Service

	// Emails melayani outbox email transaksional: riwayat email pesanan dan
	// kirim ulang oleh admin (F1-16).
	Emails *notification.Service

	// Backups menyediakan status cadangan terenkripsi untuk halaman
	// Pengaturan admin (F1-21, Architecture.md §7).
	Backups *backup.Service

	// Orders melayani draft, editor order line, daftar dan detail pesanan
	// cabang (F1-11).
	Orders *order.Service

	// AdminOrders melayani riwayat & detail pesanan seluruh cabang untuk admin
	// Pusat: filter, grouping, paginasi server, dan catatan internal (F1-15).
	AdminOrders *order.AdminService

	// Dashboard melayani metrik dashboard admin Pusat + chip "perlu tindakan"
	// (F1-20, PRD §4.1). Nilai rupiah hanya keluar untuk pemegang
	// `admin.dashboard.value`; Manager menerima jumlah pesanan saja.
	Dashboard *dashboard.Service

	// Verify melayani verifikasi pembayaran manual: persetujuan (Submitted →
	// Done, lengkap dengan dokumen + email) atau penandaan perlu review
	// (F1-18, PRD AP-ORD-07).
	Verify *payment.VerificationService

	// Payments melayani bukti transfer: unggah multi-berkas, daftar, dan
	// tautan unduh berumur pendek (F1-13).
	Payments *payment.Service

	// Documents melayani dokumen pesanan: render Invoice PDF + Surat Jalan
	// XLSX/PDF, daftar, dan tautan unduh berumur pendek (F1-17).
	Documents *document.Service

	// Reports melayani laporan order line (paginasi) dan ekspor XLSX
	// asinkron: permintaan job, status, dan tautan unduh hasilnya (F2-24,
	// PRD §4.7). Khusus Superadmin.
	Reports *report.Service

	// Registration melayani pendaftaran akun cabang dua langkah lewat token
	// registrasi sekali pakai (F1-07).
	Registration *registration.Service

	Storage   storage.Store
	Gotenberg pdf.Renderer
	Mailer    mailer.Mailer

	// LoginLimiter membatasi percobaan login GAGAL per akun + per IP
	// (PRD CP-AUT-05: 5 percobaan gagal / 15 menit).
	LoginLimiter *httpx.RateLimiter

	// ForgotLimiter membatasi permintaan tautan atur ulang per IP, baik untuk
	// forgot maupun reset (Architecture.md §6: lupa password dibatasi).
	ForgotLimiter *httpx.RateLimiter

	// RegistrationLimiter membatasi permintaan validasi token registrasi dan
	// pendaftaran akun per IP; keduanya endpoint publik (Architecture.md §6).
	RegistrationLimiter *httpx.RateLimiter

	// ProofLimiter membatasi laju unggahan bukti transfer per akun
	// (Architecture.md §6: unggahan dibatasi).
	ProofLimiter *httpx.RateLimiter
}

// API memegang dependensi untuk handler.
type API struct {
	d Deps
}

// New membuat handler API.
func New(d Deps) *API {
	if d.Storage == nil {
		d.Storage = storage.Disabled{}
	}
	if d.Gotenberg == nil {
		d.Gotenberg = pdf.Disabled{}
	}
	if d.Mailer == nil {
		d.Mailer = mailer.Log{}
	}
	return &API{d: d}
}

// handler adalah bentuk fungsi handler tanpa pembungkus tambahan.
type handler func(http.ResponseWriter, *http.Request) error

// adapt mengubah handler yang mengembalikan error menjadi http.HandlerFunc
// sekaligus menerjemahkan error ke bentuk seragam.
func (a *API) adapt(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			httpx.Fail(w, r, err)
		}
	}
}
