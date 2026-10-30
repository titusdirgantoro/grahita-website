package document

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/pdf"
)

// TestOPS12PDF29BarisCatatP95 memakai Chromium/Gotenberg nyata. Hasilnya hanya
// baseline QA mesin saat uji, bukan SLA produksi.
func TestOPS12PDF29BarisCatatP95(t *testing.T) {
	url := os.Getenv("GRAHITA_QA_GOTENBERG_URL")
	if url == "" {
		t.Skip("GRAHITA_QA_GOTENBERG_URL tidak diisi")
	}
	renderer := pdf.NewGotenberg(url)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if err := renderer.Health(ctx); err != nil {
		t.Fatalf("Gotenberg QA tidak siap: %v", err)
	}

	order := fixtureOrder("ops12-pdf-29")
	order.Lines = make([]Line, 0, 29)
	order.Total = 29 * 27_500
	for i := 1; i <= 29; i++ {
		order.Lines = append(order.Lines, Line{
			LineNo: i, Jenis: JenisBarisProduk, Nama: "Bio Magnetik",
			NamaInstansi: fmt.Sprintf("Sekolah Uji Kinerja %02d", i), Qty: 1,
			HargaSatuan: 27_500, Subtotal: 27_500,
			SerialNumber: fmt.Sprintf("20261029%03d", i),
		})
	}

	for _, doc := range []struct {
		name string
		html func(Order) ([]byte, error)
	}{{"invoice", invoiceHTML}, {"surat-jalan", suratJalanHTML}} {
		t.Run(doc.name, func(t *testing.T) {
			html, err := doc.html(order)
			if err != nil {
				t.Fatal(err)
			}
			const runs = 20
			durations := make([]time.Duration, 0, runs)
			sizes := make([]int, 0, runs)
			for i := 0; i < runs; i++ {
				start := time.Now()
				content, err := renderer.HTMLToPDF(ctx, html)
				durations = append(durations, time.Since(start))
				if err != nil {
					t.Fatalf("render %d: %v", i, err)
				}
				if len(content) < 5_000 {
					t.Fatalf("render %d terlalu kecil: %d bytes", i, len(content))
				}
				sizes = append(sizes, len(content))
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			sort.Ints(sizes)
			p95 := durations[(runs*95+99)/100-1]
			t.Logf("OPS-12 PDF %s 29 baris: n=%d p50=%s p95=%s min=%s max=%s ukuran=%d..%d bytes",
				doc.name, runs, durations[runs/2], p95, durations[0], durations[runs-1], sizes[0], sizes[runs-1])
		})
	}
}
