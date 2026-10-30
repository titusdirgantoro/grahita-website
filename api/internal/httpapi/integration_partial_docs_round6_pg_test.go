package httpapi

// Pengujian ini menutup bukti yang sebelumnya belum lengkap pada matriks QA:
// DOC-04 untuk pesanan Deposit sampai dokumennya benar-benar dikerjakan worker,
// dan REP-05 untuk laporan versi berjalan setelah satu revisi nyata.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/report"
	reportpg "github.com/grahita/portal/api/internal/report/postgres"
	"github.com/grahita/portal/api/internal/worker"
)

// TestPGDOC04DepositMenghasilkanDokumenSekali menempuh transaksi Deposit
// sungguhan: saldo dikreditkan, dialokasikan, diverifikasi sampai Done, lalu
// pekerjaan River dijalankan. Ketiga jenis dokumen harus punya baris DB dan
// objek storage versi 0; pengulangan worker tidak boleh menggandakannya.
func TestPGDOC04DepositMenghasilkanDokumenSekali(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	jadikanCNC(t, f, f.branchA)

	orderID, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-604")
	cabang, csrfCabang := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	versiSaldo := kreditCabang(t, f, cabang, csrfCabang, total, "doc04-deposit")
	alokasiID := alokasiHTTP(t, f, cabang, csrfCabang, orderID, total, versiSaldo, 0, "doc04-alokasi")

	admin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body := verifyKey(t, f, admin, csrfAdmin, orderID,
		settlementBody(f, 0, []string{alokasiID}, nil), "doc04-verifikasi")
	if status != http.StatusOK {
		t.Fatalf("verifikasi Deposit harus 200, dapat %d (%v)", status, body)
	}

	var statusOrder string
	var jobCount int
	if err := f.pool.QueryRow(ctx, `SELECT status::text FROM orders WHERE id=$1`, orderID).Scan(&statusOrder); err != nil {
		t.Fatal(err)
	}
	if statusOrder != "done" {
		t.Fatalf("pesanan Deposit harus Done sebelum worker dokumen, dapat %s", statusOrder)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job
		WHERE kind=$1 AND args->>'order_id'=$2`, jobs.KindRenderDokumen, orderID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("transaksi harus menitipkan tepat satu job render, dapat %d", jobCount)
	}

	w := worker.NewRenderDokumenWorker(f.selesai)
	job := &river.Job[jobs.RenderDokumenArgs]{
		JobRow: &rivertype.JobRow{ID: 604},
		Args:   jobs.RenderDokumenArgs{OrderID: orderID},
	}
	emailSesudahPertama := 0
	for percobaan := 1; percobaan <= 2; percobaan++ {
		if err := w.Work(ctx, job); err != nil {
			t.Fatalf("worker dokumen percobaan %d: %v", percobaan, err)
		}
		rows := f.barisDokumen(t, orderID)
		if len(rows) != 3 {
			t.Fatalf("percobaan %d harus tetap menghasilkan tiga dokumen, dapat %d", percobaan, len(rows))
		}
		jenis := map[string]bool{}
		for _, row := range rows {
			jenis[row.Jenis] = true
			if row.Versi != 0 {
				t.Errorf("versi dokumen %s = %d, mau 0", row.Jenis, row.Versi)
			}
			objek, err := f.objek.Get(ctx, row.StorageKey)
			if err != nil {
				t.Errorf("objek %s tidak tersedia: %v", row.StorageKey, err)
			} else {
				_ = objek.Close()
			}
		}
		for _, wajib := range []string{document.JenisInvoicePDF, document.JenisSuratJalanPDF, document.JenisSuratJalanXLSX} {
			if !jenis[wajib] {
				t.Errorf("dokumen %s tidak terbit: %v", wajib, jenis)
			}
		}
		emailSekarang := hitungBaris(t, f, "email_outbox", "order_id", orderID)
		if percobaan == 1 {
			emailSesudahPertama = emailSekarang
		} else if emailSekarang != emailSesudahPertama {
			t.Fatalf("worker ulang menggandakan email dokumen: %d menjadi %d", emailSesudahPertama, emailSekarang)
		}
	}
	if emailSesudahPertama < 1 {
		t.Fatal("worker harus mengantre minimal email invoice cabang")
	}
}

// TestPGREP05LaporanMemakaiTigaBarisVersiRevisi menyiapkan pesanan Done dua
// baris, menerbitkan dokumen prasyarat, lalu merevisinya menjadi tiga baris
// melalui API produksi. Laporan harus membaca hanya versi berjalan, menjaga
// instansi/serial tiap baris, dan menerapkan filter tanggal/cabang/status yang
// sama pada daftar maupun total subtotal.
func TestPGREP05LaporanMemakaiTigaBarisVersiRevisi(t *testing.T) {
	f, orderID, _, admin, csrf := jalurUtamaRevisi(t)
	ctx := context.Background()
	tanggal, periode := tanggalDanPeriode(3)

	if err := jalankanPekerjaanDone(t, f, orderID); err != nil {
		t.Fatalf("render dokumen versi 0 sebagai prasyarat revisi: %v", err)
	}
	produkBaru := insertProduct(t, ctx, f.pool, "P-REP05", "Produk Laporan", 25000)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id,product_id,harga) VALUES ($1,$2,25000)`,
		f.branchA, produkBaru); err != nil {
		t.Fatalf("seed harga produk baru: %v", err)
	}

	request := reviseBody("REP-05 koreksi tiga instansi",
		`{"line_no":1,"nama_instansi":"TK Mawar","qty":1,"harga_satuan":100000},`+
			`{"line_no":2,"nama_instansi":"SD Melati","qty":2,"harga_satuan":50000},`+
			`{"product_id":"`+produkBaru+`","nama_instansi":"SMP Anggrek","qty":2,"harga_satuan":25000}`)
	status, revised := reviseHTTP(t, f, admin, csrf, orderID, request)
	if status != http.StatusOK {
		t.Fatalf("revisi REP-05 harus 200, dapat %d (%v)", status, revised)
	}
	if dataMap(t, revised)["versi"] != float64(1) {
		t.Fatalf("pesanan harus masuk versi 1: %v", revised)
	}

	repo := reportpg.New(f.pool, reportpg.WithJobQueue(f.jobs))
	f.deps.Reports = report.NewService(repo, f.objek)
	srv := httptest.NewServer(NewRouter(f.deps))
	t.Cleanup(srv.Close)
	admin, _ = pgLogin(t, srv, "rina@example.test", "rahasia123")

	ambil := func(query string) map[string]any {
		t.Helper()
		st, response, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			srv.URL+"/api/v1/admin/reports/order-lines"+query, "", admin, "")
		if st != http.StatusOK {
			t.Fatalf("laporan %s harus 200, dapat %d (%v)", query, st, response)
		}
		return dataMap(t, response)
	}

	filter := "?dari=" + tanggal + "&sampai=" + tanggal + "&cabang=" + f.branchA + "&status=done"
	page := ambil(filter)
	items := page["items"].([]any)
	if page["total"] != float64(3) || len(items) != 3 {
		t.Fatalf("laporan harus hanya memuat tiga baris versi berjalan: %+v", page)
	}
	if page["total_subtotal"] != float64(250000) {
		t.Fatalf("total subtotal laporan harus 250000, dapat %v", page["total_subtotal"])
	}

	instansi := map[string]bool{}
	serial := map[string]bool{}
	var jumlahSubtotal float64
	for _, raw := range items {
		row := raw.(map[string]any)
		if row["nomor"] != "2026-10-900" || row["versi"] != float64(1) || row["status"] != "done" {
			t.Errorf("baris bukan versi/status berjalan: %v", row)
		}
		nama := row["nama_instansi"].(string)
		instansi[nama] = true
		sn := row["serial_number"].(string)
		if !strings.HasPrefix(sn, periode) || serial[sn] {
			t.Errorf("serial kosong/salah periode/duplikat: %q pada %v", sn, row)
		}
		serial[sn] = true
		jumlahSubtotal += row["subtotal"].(float64)
	}
	for _, nama := range []string{"TK Mawar", "SD Melati", "SMP Anggrek"} {
		if !instansi[nama] {
			t.Errorf("instansi %q hilang dari laporan: %v", nama, instansi)
		}
	}
	if jumlahSubtotal != page["total_subtotal"].(float64) {
		t.Fatalf("jumlah subtotal baris %.0f tidak sama dengan total %.0f", jumlahSubtotal, page["total_subtotal"])
	}

	for nama, query := range map[string]string{
		"tanggal lain": fmt.Sprintf("?dari=%s&sampai=%s", "2000-01-01", "2000-01-01"),
		"cabang lain":  "?cabang=" + f.branchB,
		"status lain":  "?status=submitted",
	} {
		if got := ambil(query); got["total"] != float64(0) {
			t.Errorf("filter %s harus menyingkirkan order revisi, dapat %+v", nama, got)
		}
	}
}
