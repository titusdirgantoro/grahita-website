package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// NewRouter menyusun seluruh permukaan /api/v1.
//
// Kedua portal memakai router yang sama; yang membedakan hanya host tempat
// SPA disajikan. Identitas dan hak akses ditentukan role akun di server.
func NewRouter(d Deps) http.Handler {
	a := New(d)

	r := chi.NewRouter()
	// Metrik dipasang PALING LUAR (setelah RequestID) supaya defer-nya berjalan
	// paling akhir: status akhir — termasuk 500 hasil Recoverer saat panic —
	// yang tercatat, bukan 200 sementara. Menaruhnya setelah Recoverer membuat
	// panic tercatat sebagai 200 dan mengecilkan rasio error pada alarm
	// (Architecture.md §8).
	if d.Metrics != nil {
		r.Use(d.Metrics.Middleware)
	}
	r.Use(httpx.RequestID)
	r.Use(httpx.Logger)
	// Panic diubah menjadi 500 oleh Recoverer; metrik di atas mencatat hasil
	// akhirnya.
	r.Use(httpx.Recoverer)
	r.Use(httpx.SecurityHeaders)
	// Metadata request (IP klien) dititipkan lebih dulu supaya entri audit
	// mutasi admin mencatat dari mana permintaan datang.
	r.Use(a.requestMeta)
	if len(d.Config.AllowedOrigins) > 0 {
		r.Use(httpx.CORS(d.Config.AllowedOrigins))
	}
	// CSRF dipasang pada tingkat router (sebelum routing), jadi permintaan
	// non-GET ke rute yang hanya menerima GET — mis. POST /healthz — akan
	// dijawab 403 CSRF_INVALID alih-alih 405. Bentuk error tetap konsisten
	// {error:{code,message,fields}}; kesengajaan ini didokumentasikan agar
	// tidak dianggap bug.
	r.Use(httpx.CSRF(httpx.CSRFConfig{
		Enabled: d.Config.CSRFEnabled,
		Secure:  d.Config.SessionSecure,
		Exempt:  csrfExempt(),
	}))

	// Endpoint kesehatan: tersedia tanpa autentikasi dan tanpa CSRF.
	r.Get("/healthz", a.adapt(a.healthz))
	r.Get("/readyz", a.adapt(a.readyz))
	// Metrik Prometheus (Architecture.md §8). Tanpa autentikasi seperti
	// /healthz, tetapi TIDAK dijangkau publik: Caddy hanya mem-proxy /api/*
	// dan port API tidak dipublikasikan di produksi (docker-compose.prod.yml),
	// jadi hanya Prometheus di jaringan internal yang bisa memanjatnya.
	if d.Metrics != nil {
		r.Get("/metrics", d.Metrics.Handler().ServeHTTP)
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(a.authenticate)

		r.Get("/meta", a.adapt(a.meta))
		// Fixture browser hanya tersedia pada runner sintetis lokal. Password
		// seed tidak pernah dikirim browser: handler memakainya dari environment
		// proses untuk menempuh Login normal dan menerbitkan cookie sesi+CSRF.
		// Guard ganda (IsLocal + flag) memastikan route tidak pernah terdaftar
		// pada build/runtime production, bahkan bila Config dibentuk manual.
		if d.Config.IsLocal() && d.Config.BrowserFixture.Enabled {
			r.Get("/test/browser-session/{profile}", a.adapt(a.browserFixtureSession))
		}
		r.Post("/auth/login", a.adapt(a.login))
		r.Post("/auth/logout", a.adapt(a.logout))
		// Lupa/atur ulang kata sandi tersedia tanpa sesi: pengguna yang tidak
		// bisa masuk justru yang memakainya (PRD §3.2).
		r.Post("/auth/password/forgot", a.adapt(a.forgotPassword))
		r.Post("/auth/password/reset", a.adapt(a.resetPassword))
		// Pendaftaran akun cabang lewat token registrasi: dua langkah, juga
		// tanpa sesi karena pemakainya belum punya akun (PRD §3.1).
		r.Post("/auth/registration/validate", a.adapt(a.registrationValidate))
		r.Post("/auth/register", a.adapt(a.registrationRegister))

		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth)

			r.Get("/auth/me", a.adapt(a.me))
			// Profil sendiri (PRD §4.10): hanya nama tampilan admin yang bisa
			// diubah pemiliknya; email/role read-only.
			r.Put("/auth/me", a.adapt(a.meUpdate))
			r.Post("/auth/password/change", a.adapt(a.changePassword))
			r.Get("/catalog", a.adapt(a.catalog))
			r.Get("/me/branch", a.adapt(a.myBranch))
			// Pesanan cabang (F1-11): draft + editor order line + daftar/detail.
			// Semua endpoint dibatasi branch_id SESI di layer service, dan
			// harga selalu diambil server dari branch_prices cabang itu.
			r.Get("/orders", a.adapt(a.ordersList))
			r.Post("/orders", a.adapt(a.orderCreate))
			r.Get("/orders/{id}", a.adapt(a.orderGet))
			r.Put("/orders/{id}", a.adapt(a.orderUpdate))
			r.Delete("/orders/{id}", a.adapt(a.orderDelete))
			// Submit pesanan (F1-14): multipart (bukti opsional + flag C&C).
			// Nomor invoice/serial, snapshot harga, tanggal WIB, dan timeline
			// ditulis dalam satu transaksi oleh domain pesanan.
			r.Post("/orders/{id}/submit", a.adapt(a.orderSubmit))
			// Bukti transfer pesanan (F1-13): multi-berkas, dibatasi cabang
			// sesi di layer service. Unduhan selalu lewat presigned URL
			// berumur pendek — kunci storage tidak pernah dikirim ke klien.
			r.Get("/orders/{id}/proofs", a.adapt(a.orderProofs))
			r.Post("/orders/{id}/proofs", a.adapt(a.orderProofUpload))
			r.Get("/orders/{id}/proofs/{proofId}/download", a.adapt(a.orderProofDownload))
			// Dokumen pesanan (F1-17): daftar dokumen satu pesanan milik
			// cabang sesi (yang tampil hanya Invoice-nya) dan tautan unduh
			// berumur pendek per dokumen — kunci storage tidak pernah
			// dikirim ke klien (SystemDesign.md §8).
			r.Get("/orders/{id}/documents", a.adapt(a.orderDocuments))
			r.Get("/documents/{id}/download", a.adapt(a.documentDownload))
			// Riwayat invoice cabang (F1-19, PRD §3.8): nomor, tanggal,
			// versi (versi lama tetap tampil setelah revisi), dan
			// ketersediaan PDF-nya — selalu dibatasi branch_id SESI dan
			// dapat difilter periode.
			r.Get("/invoices", a.adapt(a.invoices))
			// Rekening AKTIF untuk instruksi pembayaran & referensi OCR
			// (PRD §4.6, §5); nonaktif tidak pernah muncul di sini.
			r.Get("/bank-accounts", a.adapt(a.bankAccountsPublic))
			// Deposit cabang (D3 C2, §6.1): saldo dan journal cabang sesi,
			// top-up pending (tidak pernah menambah saldo sebelum diverifikasi
			// Superadmin), dan alokasi deposit ke order Submitted miliknya.
			r.Get("/deposit", a.adapt(a.depositSummary))
			r.Get("/deposit/transactions", a.adapt(a.depositTransactions))
			r.Get("/deposit/topups", a.adapt(a.depositTopups))
			r.Post("/deposit/topups", a.adapt(a.depositTopupCreate))
			r.Get("/deposit/topups/{id}/proof", a.adapt(a.depositTopupProof))
			r.Get("/orders/{id}/deposit-allocations", a.adapt(a.depositOrderAllocations))
			r.Post("/orders/{id}/deposit-allocations", a.adapt(a.depositAllocate))
			r.Get("/orders/{id}/payment", a.adapt(a.depositOrderPayment))
			// Konten halaman Tutorial cabang (F2-27, PRD §3.8): dibaca dari
			// database sehingga pembaruan Superadmin langsung terlihat di
			// portal cabang tanpa build/deploy. Pembacaan cukup sesi yang
			// sah; TIDAK ada jalur tulis di sini (lihat grup admin di bawah).
			r.Get("/tutorial", a.adapt(a.tutorialRead))

			r.Route("/admin", func(r chi.Router) {
				// Dashboard admin Pusat (F1-20, PRD §4.1): metrik + chip "perlu
				// tindakan". Manager maupun Superadmin boleh membukanya
				// (`admin.dashboard`); yang dibatasi role adalah NILAI RUPIAH —
				// service menghilangkan field `nilai` untuk Manager
				// (`admin.dashboard.value`, AP-DSH-02).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminDashboard))
					r.Get("/dashboard", a.adapt(a.adminDashboard))
				})
				// Riwayat email pesanan + kirim ulang (F1-16). Manager maupun
				// Superadmin boleh memakainya (PRD §2.1).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminOrders))
					// Riwayat pemesanan seluruh cabang (F1-15): pencarian, filter,
					// grouping, dan paginasi server. Catatan internal hanya hidup
					// di jalur ini — portal cabang tidak pernah menerimanya.
					r.Get("/orders", a.adapt(a.adminOrdersList))
					r.Get("/orders/{id}", a.adapt(a.adminOrderDetail))
					r.Post("/orders/{id}/notes", a.adapt(a.adminOrderNote))
					r.Get("/orders/{id}/emails", a.adapt(a.adminOrderEmails))
					r.Post("/orders/{id}/emails/{kind}/resend", a.adapt(a.adminOrderEmailResend))
					// Bukti transfer: admin boleh melihat semua cabang dan
					// mengunggah atas nama cabang — pengunggahnya ditandai
					// nama admin (PRD AP-ORD-06).
					r.Get("/orders/{id}/proofs", a.adapt(a.adminOrderProofs))
					r.Post("/orders/{id}/proofs", a.adapt(a.adminOrderProofUpload))
					r.Get("/orders/{id}/proofs/{proofId}/download", a.adapt(a.adminOrderProofDownload))
					// Dokumen pesanan (F1-17): daftar lengkap dokumen satu
					// pesanan dan pembuatan/penyegaran dokumen versi berjalan.
					// Idempoten — aman dipanggil berulang.
					r.Get("/orders/{id}/documents", a.adapt(a.orderDocuments))
					r.Post("/orders/{id}/documents", a.adapt(a.adminOrderDocuments))
				})
				// Verifikasi pembayaran manual (F1-18): kapabilitas sendiri
				// `admin.verify` (Manager maupun Superadmin, PRD §2.1) — sama
				// dengan yang ditegakkan layer service, supaya penjaga rute dan
				// penjaga domain tidak pernah berbeda. Setujui (→ Done beserta
				// dokumen + email) atau tandai perlu review dengan alasan;
				// idempoten terhadap klik ganda: yang kedua 409.
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminVerify))
					r.Post("/orders/{id}/verify", a.adapt(a.adminOrderVerify))
					r.Get("/orders/{id}/bank-accounts", a.adapt(a.adminOrderBankAccounts))
					r.Post("/orders/{id}/verification/reevaluate-destination", a.adapt(a.adminOrderReevaluateDestination))
					// Deposit cabang (D3 C2, §6.2): antrean dan saldo, keputusan
					// top-up (verify kredit khusus Superadmin `admin.deposit_credit`,
					// ditegakkan service), alokasi atas permintaan cabang, reversal.
					r.Get("/deposits", a.adapt(a.adminDeposits))
					r.Get("/deposits/{branch_id}", a.adapt(a.adminDepositBranch))
					r.Get("/deposit-topups", a.adapt(a.adminDepositTopups))
					r.Get("/deposit-topups/{id}/proof", a.adapt(a.depositTopupProof))
					r.Post("/deposit-topups/{id}/decision", a.adapt(a.adminDepositDecision))
					r.Get("/orders/{id}/deposit-allocations", a.adapt(a.depositOrderAllocations))
					r.Get("/orders/{id}/payment", a.adapt(a.depositOrderPayment))
					r.Post("/orders/{id}/deposit-allocations", a.adapt(a.depositAllocate))
					r.Post("/orders/{id}/deposit-allocations/{allocation_id}/reverse", a.adapt(a.depositReverse))
				})
				// Revisi invoice berversi (F2-25): kapabilitas sendiri
				// `admin.revise` (Superadmin selalu; Manager hanya bila setelan
				// `manager_boleh_revisi` aktif — ditegakkan lagi di layer service).
				// Nomor invoice tetap, versi naik, snapshot versi lama tersimpan,
				// dan invoice versi baru dirender + dikirim ulang ke cabang/admin
				// ber-flag Invoice (Surat Jalan tidak dikirim ulang).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminRevise))
					r.Post("/orders/{id}/revise", a.adapt(a.adminOrderRevise))
				})
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminBranches))
					r.Get("/branches", a.adapt(a.adminBranches))
					r.Post("/branches", a.adapt(a.adminBranchCreate))
					r.Get("/branches/{id}", a.adapt(a.adminBranch))
					r.Put("/branches/{id}", a.adapt(a.adminBranchUpdate))
					r.Delete("/branches/{id}", a.adapt(a.adminBranchDelete))
					// Token registrasi sekali pakai (PRD §4.4 AP-CAB-02).
					r.Post("/branches/{id}/token", a.adapt(a.adminBranchToken))
				})
				// User management akun CABANG: Manager & Superadmin (PRD §2.1).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminUsersBranch))
					r.Get("/users/branch", a.adapt(a.adminBranchUsers))
					r.Put("/users/branch/{id}", a.adapt(a.adminBranchUserUpdate))
					r.Post("/users/branch/{id}/reset", a.adapt(a.adminBranchUserReset))
				})
				// User management akun ADMIN: khusus Superadmin (PRD §2.1).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminUsersAdmin))
					r.Get("/users/admin", a.adapt(a.adminUsers))
					r.Post("/users/admin", a.adapt(a.adminUserCreate))
					r.Put("/users/admin/{id}", a.adapt(a.adminUserUpdate))
					// Kirim ulang tautan aktivasi admin yang masih undangan
					// (pemulihan bila email undangan pertama gagal).
					r.Post("/users/admin/{id}/resend", a.adapt(a.adminUserResendInvite))
				})
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminCatalog))
					// Master produk (PRD §4.5 AP-KAT-01).
					r.Get("/products", a.adapt(a.adminProducts))
					r.Post("/products", a.adapt(a.adminProductCreate))
					r.Get("/products/{id}", a.adapt(a.adminProduct))
					r.Put("/products/{id}", a.adapt(a.adminProductUpdate))
					r.Delete("/products/{id}", a.adapt(a.adminProductDelete))
					// Matriks harga Produk × Cabang: baca + batch edit + salin
					// antar cabang (PRD AP-KAT-02..04).
					r.Get("/prices", a.adapt(a.adminPrices))
					r.Put("/prices", a.adapt(a.adminPricesUpdate))
					r.Post("/prices/copy", a.adapt(a.adminPricesCopy))
				})
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminSettings))
					r.Get("/settings", a.adapt(a.adminSettings))
					r.Put("/settings", a.adapt(a.adminSettingsUpdate))
					// Status cadangan terenkripsi (F1-21): hasil setiap pekerjaan
					// (jenis/status/ukuran/tujuan) + ringkasan "perlu tindakan".
					// Tampil di halaman Pengaturan (Architecture.md §7).
					r.Get("/backup-runs", a.adapt(a.adminBackupRuns))
				})
				// Konten tutorial portal cabang (F2-27, PRD §3.8): baca & ubah
				// hanya Superadmin (`admin.tutorial`); Manager dan akun cabang
				// ditolak 403 di sini DAN di layer service. Isi yang dikirim
				// divalidasi/disanitasi domain (tanpa HTML arbitrer) dan
				// perubahannya dicatat per topik ke audit log.
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminTutorial))
					r.Get("/tutorial", a.adapt(a.adminTutorial))
					r.Put("/tutorial", a.adapt(a.adminTutorialUpdate))
				})
				// Audit log (F2-28, SystemDesign.md §5.4 & §6): hanya
				// Superadmin (`admin.audit`). Manager ditolak 403 di sini dan
				// lagi di layer service; isi diff yang dikirim sudah bersih
				// dari kata sandi, token, dan URL bukti (diredaksi produsennya
				// dan diperiksa ulang saat ditampilkan).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminAudit))
					r.Get("/audit-logs", a.adapt(a.adminAuditLogs))
				})
				// Preview, apply idempoten, dan rollback staging master Impact:
				// Superadmin saja dan seluruh POST tetap dilindungi CSRF.
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminMasterImport))
					r.Post("/master-imports/impact/preview", a.adapt(a.adminMasterImpactPreview))
					r.Get("/master-imports/impact/{id}/evidence", a.adapt(a.adminMasterImpactEvidence))
					r.Post("/master-imports/impact/{id}/apply", a.adapt(a.adminMasterImpactApply))
					r.Post("/master-imports/impact/apply-runs/{id}/rollback", a.adapt(a.adminMasterImpactRollback))
				})
				// Rekening bank: khusus Superadmin (PRD §2.1, §4.6).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminBank))
					r.Get("/bank-accounts", a.adapt(a.adminBankAccounts))
					r.Post("/bank-accounts", a.adapt(a.adminBankAccountCreate))
					r.Put("/bank-accounts/{id}", a.adapt(a.adminBankAccountUpdate))
					r.Get("/branches/{id}/bank-accounts", a.adapt(a.adminBranchBankAccounts))
					r.Put("/branches/{id}/bank-accounts", a.adapt(a.adminBranchBankAccountsReplace))
				})
				// Reporting & ekspor Excel: khusus Superadmin (PRD §2.1, §4.7).
				// Manager ditolak 403 di sini (dan lagi di layer service), bukan
				// hanya disembunyikan menunya di UI.
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminReports))
					r.Get("/reports/order-lines", a.adapt(a.adminReportOrderLines))
					r.Post("/reports/export", a.adapt(a.adminReportExport))
					r.Get("/reports/export/{id}", a.adapt(a.adminReportExportStatus))
					r.Get("/reports/export/{id}/download", a.adapt(a.adminReportExportDownload))
				})
			})
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpErr(w, r, notFoundErr())
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpErr(w, r, methodNotAllowedErr())
	})

	return r
}

// csrfExempt adalah endpoint yang memang belum punya sesi/CSRF cookie:
// login, lupa/atur ulang kata sandi, serta pendaftaran akun cabang. Semuanya
// dipanggil dari halaman publik, dan permintaannya tidak mengubah data apa pun
// atas nama sesi yang ada.
func csrfExempt() map[string]bool {
	return map[string]bool{
		"/api/v1/auth/login":                 true,
		"/api/v1/auth/password/forgot":       true,
		"/api/v1/auth/password/reset":        true,
		"/api/v1/auth/registration/validate": true,
		"/api/v1/auth/register":              true,
	}
}
