package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestKontrakOpenAPIIkutRouter menjaga dua arah antara router dan kontrak:
// setiap rute+metode yang benar-benar didaftarkan harus ada di
// openapi/openapi.yaml, dan setiap path+metode di kontrak harus benar-benar
// dilayani router. Tanpa uji ini, endpoint baru (atau yang dihapus) bisa
// membuat dokumentasi diam-diam menyimpang.
func TestKontrakOpenAPIIkutRouter(t *testing.T) {
	as := newAuthServer(t, nil)

	chiRouter, ok := as.router.(chi.Routes)
	if !ok {
		t.Fatal("router yang dipasang bukan chi.Routes")
	}

	routerKontrak := map[string]map[string]bool{}
	err := chi.Walk(chiRouter, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if route == "" {
			route = "/"
		}
		for _, m := range strings.Split(method, ",") {
			m = strings.ToUpper(strings.TrimSpace(m))
			if m == "" || m == "*" {
				continue
			}
			if routerKontrak[route] == nil {
				routerKontrak[route] = map[string]bool{}
			}
			routerKontrak[route][m] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("menelusuri rute router: %v", err)
	}
	if len(routerKontrak) < 10 {
		t.Fatalf("hanya menemukan %d rute — penelusuran tidak berjalan", len(routerKontrak))
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "openapi", "openapi.yaml"))
	if err != nil {
		t.Fatalf("membaca kontrak OpenAPI: %v", err)
	}
	specKontrak := parsePathMetode(string(raw))
	if len(specKontrak) < 10 {
		t.Fatalf("hanya menemukan %d path di kontrak — penguraian tidak berjalan", len(specKontrak))
	}

	for route, metode := range routerKontrak {
		mau, ada := specKontrak[route]
		if !ada {
			t.Errorf("rute %s ada di router tetapi tidak ada di openapi/openapi.yaml", route)
			continue
		}
		for m := range metode {
			if !mau[m] {
				t.Errorf("rute %s %s ada di router tetapi tidak didokumentasikan", m, route)
			}
		}
	}
	for route, metode := range specKontrak {
		mau, ada := routerKontrak[route]
		if !ada {
			t.Errorf("path %s didokumentasikan tetapi tidak dilayani router", route)
			continue
		}
		for m := range metode {
			if !mau[m] {
				t.Errorf("kontrak menjanjikan %s %s tetapi router tidak melayaninya", m, route)
			}
		}
	}
}

// parsePathMetode mengurai openapi.yaml tanpa pustaka YAML: cukup mengenali
// kunci path pada indentasi 2 spasi dan metode HTTP pada indentasi 4 spasi.
// Berkas ini ditulis dengan gaya tetap sehingga heuristiknya cukup, dan
// hitungan minimal di pemanggil menjaga agar penguraian tidak diam-diam kosong.
func parsePathMetode(isi string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	path := ""
	for _, line := range strings.Split(isi, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch indent {
		case 2:
			if strings.HasSuffix(trimmed, ":") {
				kandidat := strings.TrimSuffix(trimmed, ":")
				if strings.HasPrefix(kandidat, "/") {
					path = kandidat
				} else {
					path = ""
				}
			}
		case 4:
			if path == "" || !strings.HasSuffix(trimmed, ":") {
				continue
			}
			metode := strings.ToUpper(strings.TrimSuffix(trimmed, ":"))
			switch metode {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
				if out[path] == nil {
					out[path] = map[string]bool{}
				}
				out[path][metode] = true
			}
		}
	}
	return out
}

// TestKontrakOpenAPIPathAuthLengkap memastikan seluruh endpoint auth yang
// diwajibkan kartu F1-04 benar-benar tercatat di kontrak.
func TestKontrakOpenAPIPathAuthLengkap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "openapi", "openapi.yaml"))
	if err != nil {
		t.Fatalf("membaca kontrak OpenAPI: %v", err)
	}
	spec := parsePathMetode(string(raw))

	wajib := map[string]string{
		"/api/v1/auth/login":           "POST",
		"/api/v1/auth/logout":          "POST",
		"/api/v1/auth/me":              "GET",
		"/api/v1/auth/password/forgot": "POST",
		"/api/v1/auth/password/reset":  "POST",
		"/api/v1/auth/password/change": "POST",
	}
	for path, metode := range wajib {
		if !spec[path][metode] {
			t.Errorf("%s %s wajib ada di kontrak OpenAPI", metode, path)
		}
	}

	// Kode error yang dipakai handler juga harus dikenal kontrak.
	if !strings.Contains(string(raw), "RESET_INVALID") {
		t.Error("kode RESET_INVALID belum tercantum di enum error kontrak")
	}

	paths := make([]string, 0, len(spec))
	for p := range spec {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	t.Logf("kontrak OpenAPI memuat %d path: %s", len(paths), strings.Join(paths, " "))
}
