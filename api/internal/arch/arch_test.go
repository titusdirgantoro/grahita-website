// Package arch menegakkan aturan ketergantungan antar-lapisan.
//
// Aturan (Architecture.md, SystemDesign.md §2):
//
//	httpapi -> service -> repository -> database
//
// Pengujian ini membaca import graph kode sumber, bukan dokumentasi, sehingga
// pelanggaran lapisan langsung gagal di CI.
package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/grahita/portal/api"

type rule struct {
	nama      string
	matches   func(pkgRel string) bool
	terlarang func(imp string) bool
	alasan    string
}

func internal(prefix string) func(string) bool {
	return func(pkgRel string) bool {
		return pkgRel == prefix || strings.HasPrefix(pkgRel, prefix+"/")
	}
}

func exact(pkgs ...string) func(string) bool {
	return func(pkgRel string) bool {
		for _, p := range pkgs {
			if pkgRel == p {
				return true
			}
		}
		return false
	}
}

func anyOf(preds ...func(string) bool) func(string) bool {
	return func(imp string) bool {
		for _, p := range preds {
			if p(imp) {
				return true
			}
		}
		return false
	}
}

func has(subs ...string) func(string) bool {
	return func(imp string) bool {
		for _, s := range subs {
			if strings.Contains(imp, s) {
				return true
			}
		}
		return false
	}
}

func isPostgresOrSqlc(imp string) bool {
	return strings.HasSuffix(imp, "/postgres") || strings.Contains(imp, modulePath+"/db/sqlc")
}

func TestAturanLapisan(t *testing.T) {
	rules := []rule{
		{
			nama:      "transport tidak menyentuh repository",
			matches:   internal("internal/httpapi"),
			terlarang: isPostgresOrSqlc,
			alasan:    "httpapi hanya boleh berbicara lewat interface service",
		},
		{
			nama:    "platform tidak bergantung pada domain/aplikasi",
			matches: internal("internal/platform"),
			terlarang: anyOf(
				exact(modulePath+"/internal/auth"),
				internal(modulePath+"/internal/catalog"),
				internal(modulePath+"/internal/settings"),
				internal(modulePath+"/internal/worker"),
				internal(modulePath+"/internal/httpapi"),
				internal(modulePath+"/internal/seed"),
			),
			alasan: "platform adalah fondasi; arah import tidak boleh dibalik",
		},
		{
			nama:    "service tidak mengimpor transport maupun net/http",
			matches: exact(modulePath+"/internal/auth", modulePath+"/internal/audit", modulePath+"/internal/bank", modulePath+"/internal/branch", modulePath+"/internal/catalog", modulePath+"/internal/order", modulePath+"/internal/registration", modulePath+"/internal/settings"),
			terlarang: anyOf(
				has("net/http"),
				exact(modulePath+"/internal/httpapi"),
				isPostgresOrSqlc,
			),
			alasan: "service harus bebas dari detail HTTP dan penyimpanan",
		},
		{
			nama:    "hanya repository & composition root yang memakai sqlc",
			matches: func(pkgRel string) bool { return true },
			terlarang: func(imp string) bool {
				return strings.Contains(imp, modulePath+"/db/sqlc")
			},
			alasan: "query hasil generate tidak boleh bocor ke lapisan lain",
		},
	}

	kenaAturanKhusus := func(pkgRel string) bool {
		return strings.Contains(pkgRel, "/postgres") || strings.HasPrefix(pkgRel, "cmd/")
	}

	packages := kumpulkanPaket(t)
	if len(packages) < 10 {
		t.Fatalf("hanya menemukan %d paket — struktur proyek tidak terbaca, periksa jalur", len(packages))
	}

	for _, r := range rules {
		for pkgRel, imports := range packages {
			if !r.matches(pkgRel) {
				continue
			}
			if r.nama == "hanya repository & composition root yang memakai sqlc" && kenaAturanKhusus(pkgRel) {
				continue
			}
			for _, imp := range imports {
				if r.terlarang(imp) {
					t.Errorf("%s: paket %q mengimpor %q yang terlarang — %s", r.nama, pkgRel, imp, r.alasan)
				}
			}
		}
	}
}

// kumpulkanPaket memetakan setiap direktori paket (relatif terhadap akar
// modul) ke daftar import yang dipakai.
func kumpulkanPaket(t *testing.T) map[string][]string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("tidak bisa menentukan lokasi berkas pengujian")
	}
	apiRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))

	fset := token.NewFileSet()
	out := map[string][]string{}
	seen := map[string]bool{}

	err := filepath.WalkDir(apiRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") && name != "." {
				if path != apiRoot {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(apiRoot, filepath.Dir(path))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			return nil
		}
		seen[rel] = true

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		imports := make([]string, 0, len(file.Imports))
		for _, spec := range file.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			imports = append(imports, p)
		}
		out[rel] = imports
		return nil
	})
	if err != nil {
		t.Fatalf("menelusuri kode sumber: %v", err)
	}
	return out
}
