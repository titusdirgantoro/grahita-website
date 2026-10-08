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
	r.Use(httpx.RequestID)
	r.Use(httpx.Logger)
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

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(a.authenticate)

		r.Get("/meta", a.adapt(a.meta))
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
			r.Post("/auth/password/change", a.adapt(a.changePassword))
			r.Get("/settings", a.adapt(a.settings))
			r.Get("/catalog", a.adapt(a.catalog))
			r.Get("/me/branch", a.adapt(a.myBranch))
			// Rekening AKTIF untuk instruksi pembayaran & referensi OCR
			// (PRD §4.6, §5); nonaktif tidak pernah muncul di sini.
			r.Get("/bank-accounts", a.adapt(a.bankAccountsPublic))

			r.Route("/admin", func(r chi.Router) {
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
				})
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminCatalog))
					r.Get("/products", a.adapt(a.adminProducts))
				})
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminSettings))
					r.Get("/settings", a.adapt(a.adminSettings))
					r.Put("/settings", a.adapt(a.adminSettingsUpdate))
				})
				// Rekening bank: khusus Superadmin (PRD §2.1, §4.6).
				r.Group(func(r chi.Router) {
					r.Use(a.requireCap(auth.CapAdminBank))
					r.Get("/bank-accounts", a.adapt(a.adminBankAccounts))
					r.Post("/bank-accounts", a.adapt(a.adminBankAccountCreate))
					r.Put("/bank-accounts/{id}", a.adapt(a.adminBankAccountUpdate))
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
