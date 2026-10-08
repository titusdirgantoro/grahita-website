package httpx

import (
	"net/http"
	"strings"
)

// SecurityHeaders memasang header keamanan (Architecture.md §6).
//
// CSP ketat: tidak ada CDN pihak ketiga di produksi; font dan ikon
// di-self-host di dalam bundle SPA.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self'; "+
				"font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

// HSTS dipasang hanya bila trafik memang lewat HTTPS.
func HSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// CORS dipakai hanya saat pengembangan (Vite dev server berjalan di origin
// berbeda). Di produksi kedua SPA satu origin dengan API sehingga middleware
// ini tidak diaktifkan.
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimRight(r.Header.Get("Origin"), "/")
			if origin != "" && allowed[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Vary", "Origin")
				if r.Method == http.MethodOptions {
					h.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token, X-Request-Id")
					h.Set("Access-Control-Max-Age", "600")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
