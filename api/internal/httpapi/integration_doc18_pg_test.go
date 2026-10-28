package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/document"
)

// rendererVersiTemplate menandai keluaran template agar versi sebelum dan
// sesudah perubahan dapat dibedakan tanpa mengubah template produksi.
type rendererVersiTemplate struct{ versi string }

func (r *rendererVersiTemplate) HTMLToPDF(ctx context.Context, html []byte) ([]byte, error) {
	bertanda := append(bytes.Clone(html), []byte("\n<!--template-"+r.versi+"-->\n")...)
	return (rendererUji{}).HTMLToPDF(ctx, bertanda)
}

func (*rendererVersiTemplate) Health(context.Context) error { return nil }

// TestPGDOC18VersiPDFHistorisSesudahTemplateBerubah memeriksa URL unduh
// kedua versi sampai byte objeknya. Gateway HTTP kecil menyajikan storage QA
// in-memory; ia tidak meniru validasi tanda tangan S3 produksi.
func TestPGDOC18VersiPDFHistorisSesudahTemplateBerubah(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	order := f.seedPesananDone(t, f.branchA, "2026-10-0188", false)
	renderer := &rendererVersiTemplate{versi: "lama"}
	docs := document.NewService(f.docRepo, f.objek, renderer)
	if _, err := docs.Ensure(ctx, order.ID); err != nil {
		t.Fatalf("render template lama: %v", err)
	}
	keyLama := document.StorageKey(order.ID, document.JenisInvoicePDF, 0)
	byteLama := f.isiBerkas(t, keyLama)
	if !bytes.Contains(byteLama, []byte("template-lama")) {
		t.Fatal("invoice awal tidak memakai output template lama")
	}

	var idLama string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM documents WHERE order_id=$1 AND jenis='invoice_pdf' AND versi=0`, order.ID).Scan(&idLama); err != nil {
		t.Fatalf("baca id invoice lama: %v", err)
	}

	var verifikasiBaru string
	if err := f.pool.QueryRow(ctx, `INSERT INTO verification_logs
 (order_id,metode,total_pesanan,total_terbaca,hasil,admin_id,order_version,deposit_amount,transfer_amount,
 destination_results,branch_allowlist_version)
 SELECT o.id,'manual',350000,350000,'cocok',$2,1,0,350000,
 v.destination_results,v.branch_allowlist_version
 FROM orders o JOIN verification_logs v ON v.id=o.done_verification_log_id
 WHERE o.id=$1 RETURNING id`, order.ID, f.adminSA).Scan(&verifikasiBaru); err != nil {
		t.Fatalf("verifikasi revisi: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET versi=1,total=350000,done_order_version=1,
 done_verification_log_id=$2,done_transfer_amount=350000 WHERE id=$1`, order.ID, verifikasiBaru); err != nil {
		t.Fatalf("revisi pesanan: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE order_lines SET qty=3 WHERE order_id=$1 AND line_no=1`, order.ID); err != nil {
		t.Fatalf("revisi qty: %v", err)
	}
	renderer.versi = "baru"
	docBaru, err := docs.EnsureInvoice(ctx, order.ID)
	if err != nil {
		t.Fatalf("render template baru: %v", err)
	}
	if docBaru.Versi != 1 || docBaru.StorageKey != document.StorageKey(order.ID, document.JenisInvoicePDF, 1) {
		t.Fatalf("metadata invoice revisi salah: %+v", docBaru)
	}
	if !bytes.Equal(f.isiBerkas(t, keyLama), byteLama) {
		t.Fatal("byte invoice historis berubah setelah template dan revisi")
	}
	byteBaru := f.isiBerkas(t, docBaru.StorageKey)
	if !bytes.Contains(byteBaru, []byte("template-baru")) || bytes.Contains(byteBaru, []byte("template-lama")) {
		t.Fatal("invoice revisi tidak memakai output template baru")
	}

	// API memberi tautan untuk masing-masing ID. GET berikut menempuh URL
	// tersebut pada gateway storage QA dan membandingkan seluruh byte berkas.
	storageHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/files/") {
			http.NotFound(w, r)
			return
		}
		objek, err := f.objek.Get(r.Context(), strings.TrimPrefix(r.URL.Path, "/files/"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer objek.Close()
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.Copy(w, objek)
	}))
	defer storageHTTP.Close()
	admin, _ := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	for _, tc := range []struct {
		nama string
		id   string
		mau  []byte
		key  string
	}{
		{"historis", idLama, byteLama, keyLama},
		{"revisi", docBaru.ID, byteBaru, docBaru.StorageKey},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
				f.srv.URL+"/api/v1/documents/"+tc.id+"/download", "", admin, "")
			if status != http.StatusOK {
				t.Fatalf("tautan unduh: HTTP %d (%v)", status, body)
			}
			path, _ := dataMap(t, body)["url"].(string)
			downloadURL, err := url.Parse(path)
			if err != nil {
				t.Fatalf("tautan unduh tidak valid: %v", err)
			}
			if downloadURL.Path != "/files/"+tc.key {
				t.Fatalf("tautan menunjuk objek salah: %q", path)
			}
			resp, err := http.Get(storageHTTP.URL + path)
			if err != nil {
				t.Fatalf("GET berkas: %v", err)
			}
			defer resp.Body.Close()
			terunduh, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(terunduh, tc.mau) {
				t.Fatalf("berkas unduh berubah: status=%d byte=%d mau=%d err=%v", resp.StatusCode, len(terunduh), len(tc.mau), err)
			}
			if !bytes.HasPrefix(terunduh, []byte("%PDF-")) {
				t.Fatal("berkas unduh bukan output PDF")
			}
		})
	}
}
