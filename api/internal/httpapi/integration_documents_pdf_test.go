package httpapi

// Uji dokumen dengan PDF SUNGGUHAN: alih-alih Gotenberg di Docker (tidak
// tersedia di lingkungan ini), pengujian menjalankan tiruan protokol Gotenberg
// yang memakai Chromium lokal — mesin render yang sama dengan yang dipakai
// Gotenberg. Tujuannya membuktikan template HTML kita benar-benar menjadi
// berkas PDF A4 yang sah, bukan sekadar "renderer palsu".
//
// Uji dilewati bila google-chrome tidak terpasang (mis. di CI).

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/platform/pdf"
)

// serverGotenbergChromium menjalankan tiruan endpoint Gotenberg
// `/forms/chromium/convert/html` di atas Chromium headless lokal.
func serverGotenbergChromium(t *testing.T) *httptest.Server {
	t.Helper()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		t.Skip("google-chrome tidak tersedia — uji PDF nyata dilewati")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/forms/chromium/convert/html" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, "multipart tidak valid: "+err.Error(), http.StatusBadRequest)
			return
		}
		var html []byte
		for _, fh := range r.MultipartForm.File["files"] {
			f, err := fh.Open()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			html, err = io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			break
		}
		if len(html) == 0 {
			http.Error(w, "berkas index.html tidak dikirim", http.StatusBadRequest)
			return
		}

		dir, err := os.MkdirTemp("", "pdf-uji-")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		sumber := filepath.Join(dir, "index.html")
		keluaran := filepath.Join(dir, "out.pdf")
		if err := os.WriteFile(sumber, html, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ctx, batal := context.WithTimeout(r.Context(), 60*time.Second)
		defer batal()
		cmd := exec.CommandContext(ctx, chrome,
			"--headless=new", "--no-sandbox", "--disable-gpu",
			"--no-pdf-header-footer",
			"--user-data-dir="+filepath.Join(dir, "profil"),
			"--print-to-pdf="+keluaran,
			"file://"+sumber,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			http.Error(w, "chromium gagal: "+err.Error()+" — "+string(out), http.StatusInternalServerError)
			return
		}
		pdfBytes, err := os.ReadFile(keluaran)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfBytes)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPGDokumenPDFNyataMelaluiChromium menempuh jalur lengkap: HTTP ->
// service -> klien Gotenberg -> Chromium -> PDF A4 nyata -> object storage ->
// metadata di PostgreSQL. Berkas XLSX-nya juga diperiksa dengan excelize.
func TestPGDokumenPDFNyataMelaluiChromium(t *testing.T) {
	f := newPGFixture(t)
	shim := serverGotenbergChromium(t)

	// Router kedua memakai klien Gotenberg sungguhan yang menunjuk Chromium
	// lokal, dengan repository + storage yang sama seperti fixture.
	deps := f.deps
	deps.Documents = document.NewService(f.docRepo, f.objek, pdf.NewGotenberg(shim.URL))
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	pesanan := f.seedPesananDone(t, f.branchA, "2026-10-0006", true)
	sa, csrfSA := pgLogin(t, srv, "rina@example.test", "rahasia123")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		srv.URL+"/api/v1/admin/orders/"+pesanan.ID+"/documents", "", sa, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("generate harus 200, dapat %d (%v)", status, body)
	}
	if got := len(dataList(t, body)); got != 3 {
		t.Fatalf("harus 3 dokumen, dapat %d (%v)", got, body["data"])
	}

	// Invoice & surat jalan PDF: berkas PDF sungguhan dari Chromium.
	for _, jenis := range []string{"invoice_pdf", "surat_jalan_pdf"} {
		isi := f.isiBerkas(t, document.StorageKey(pesanan.ID, jenis, 0))
		if !bytes.HasPrefix(isi, []byte("%PDF-")) {
			t.Fatalf("%s bukan PDF (awalan %q)", jenis, string(isi[:12]))
		}
		if !bytes.Contains(isi, []byte("%%EOF")) {
			t.Errorf("%s tidak lengkap (tanpa %%EOF)", jenis)
		}
		if !bytes.Contains(isi, []byte("/Type /Page")) {
			t.Errorf("%s tidak memuat halaman", jenis)
		}
		if len(isi) < 3000 {
			t.Errorf("%s terlalu kecil untuk A4 berisi tabel: %d byte", jenis, len(isi))
		}
	}

	// Surat jalan XLSX: satu baris per order line dengan serial snapshotted.
	xlsx := f.isiBerkas(t, document.StorageKey(pesanan.ID, "surat_jalan_xlsx", 0))
	wb, err := excelize.OpenReader(bytes.NewReader(xlsx))
	if err != nil {
		t.Fatalf("surat jalan XLSX tidak dapat dibuka: %v", err)
	}
	defer func() { _ = wb.Close() }()
	if v, _ := wb.GetCellValue(document.SheetSuratJalan, "B3"); v != pesanan.Nomor {
		t.Errorf("nomor surat jalan = %q, mau %q", v, pesanan.Nomor)
	}
	for i, serial := range pesanan.Serials {
		sel, err := excelize.CoordinatesToCellName(5, 9+i)
		if err != nil {
			t.Fatalf("nama sel: %v", err)
		}
		if v, _ := wb.GetCellValue(document.SheetSuratJalan, sel); v != serial {
			t.Errorf("serial baris %d (%s) = %q, mau %q", i+1, sel, v, serial)
		}
	}
	if strings.Contains(string(xlsx), "storage_key") {
		t.Error("berkas XLSX tidak boleh memuat kunci storage")
	}
}

// TestChromeTersediaUntukUjiPDF mencatat ketersediaan Chromium supaya galat
// "uji dilewati" tidak diam-diam menyembunyikan hilangnya bukti PDF nyata
// (dijalankan sebagai catatan, bukan kegagalan).
func TestChromeTersediaUntukUjiPDF(t *testing.T) {
	if _, err := exec.LookPath("google-chrome"); err != nil {
		t.Skip("google-chrome tidak tersedia — uji PDF nyata dilewati")
	}
	t.Log("google-chrome tersedia: uji PDF nyata dijalankan")
}
