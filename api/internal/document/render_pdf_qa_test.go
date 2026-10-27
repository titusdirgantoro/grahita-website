package document

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/pdf"
)

// TestPDFBanyakBarisDenganGotenberg adalah pemeriksaan renderer nyata yang
// dijalankan pada fasilitas QA dengan GRAHITA_QA_GOTENBERG_URL. Berkas yang
// dihasilkan dapat diperiksa per halaman bersama contoh invoice/surat jalan.
func TestPDFBanyakBarisDenganGotenberg(t *testing.T) {
	url := os.Getenv("GRAHITA_QA_GOTENBERG_URL")
	if url == "" {
		t.Skip("GRAHITA_QA_GOTENBERG_URL tidak diisi")
	}
	renderer := pdf.NewGotenberg(url)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if err := renderer.Health(ctx); err != nil {
		t.Fatalf("Gotenberg QA tidak siap: %v", err)
	}
	outputDir := os.Getenv("GRAHITA_QA_PDF_DIR")
	if outputDir == "" {
		outputDir = t.TempDir()
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{1, 11, 22, 23, 29, 100} {
		o := fixtureOrder(fmt.Sprintf("qa-pdf-%d", count))
		o.Lines = make([]Line, 0, count)
		o.Total = int64(count) * 27500
		o.Catatan = strings.Repeat("Pastikan nama instansi, serial, dan jumlah sesuai sebelum tanda tangan. ", 7)
		for i := 1; i <= count; i++ {
			instansi := fmt.Sprintf("Sekolah Islam Harapan Bangsa %02d", i)
			product := "Bio Magnetik"
			if count == 1 {
				instansi = "TK Élite & Mitra <script>alert('uji')</script>"
				product = "Sertifikat"
			}
			if i == count/2 {
				instansi = "Yayasan Pendidikan Anak dan Remaja Grahita – Sekolah Menengah Pertama Bina Kasih Élite & Mitra Cabang Bandung"
			}
			o.Lines = append(o.Lines, Line{LineNo: i, Jenis: JenisBarisProduk,
				Nama: product, NamaInstansi: instansi, Qty: 1, HargaSatuan: 27500,
				Subtotal: 27500, SerialNumber: fmt.Sprintf("20261008%03d", i)})
		}
		if count == 1 {
			o.Total += 4500
			o.Lines = append(o.Lines, Line{LineNo: 2, Jenis: "packaging", Nama: "Biaya packaging",
				Qty: 1, HargaSatuan: 4500, Subtotal: 4500})
		}
		for _, doc := range []struct {
			name string
			html func(Order) ([]byte, error)
		}{{"invoice", invoiceHTML}, {"surat-jalan", suratJalanHTML}} {
			t.Run(fmt.Sprintf("%s-%d", doc.name, count), func(t *testing.T) {
				html, err := doc.html(o)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(html, []byte("<script>")) {
					t.Fatal("teks instansi tidak boleh membentuk elemen script")
				}
				for _, line := range o.Lines {
					if !bytes.Contains(html, []byte(line.SerialNumber)) {
						t.Fatalf("serial %s hilang dari HTML", line.SerialNumber)
					}
				}
				content, err := renderer.HTMLToPDF(ctx, html)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasPrefix(content, []byte("%PDF-")) || len(content) < 5000 {
					t.Fatalf("PDF %s %d baris tidak valid: %d byte", doc.name, count, len(content))
				}
				path := filepath.Join(outputDir, fmt.Sprintf("qa-%s-%02d-baris.pdf", doc.name, count))
				if err := os.WriteFile(path, content, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Logf("PDF QA tersimpan: %s", path)
			})
		}
	}
}
