package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestPGInputPaginasiEkstremTidakMenjadi500 memastikan angka halaman yang
// valid secara sintaks tidak meluap ketika service mengubahnya menjadi offset
// int32 untuk query PostgreSQL. Ini jalur HTTP nyata, bukan hanya parser.
func TestPGInputPaginasiEkstremTidakMenjadi500(t *testing.T) {
	t.Run("pesanan", func(t *testing.T) {
		f := newPGFixture(t)
		admin, _ := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			f.srv.URL+"/api/v1/admin/orders?halaman=2147483648&per_halaman=100", "", admin, "")
		if status != http.StatusBadRequest {
			t.Fatalf("halaman ekstrem harus 400, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "VALIDATION_FAILED")
	})
	t.Run("laporan", func(t *testing.T) {
		f := newLaporanPG(t)
		admin, _ := f.saLogin(t)
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			f.srv.URL+"/api/v1/admin/reports/order-lines?halaman=2147483648&per_halaman=100", "", admin, "")
		if status != http.StatusBadRequest {
			t.Fatalf("halaman ekstrem harus 400, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "VALIDATION_FAILED")
	})
}

// TestPGInputSalahBentukDanTeksBebasTerisolasi memeriksa batas API yang
// dituju langsung oleh kedua portal: filter salah ditolak terstruktur,
// sedangkan teks mirip SQL/HTML diperlakukan sebagai data biasa.
func TestPGInputSalahBentukDanTeksBebasTerisolasi(t *testing.T) {
	f := newPGFixture(t)
	admin, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	base := f.srv.URL + "/api/v1"
	for _, tc := range []struct {
		nama string
		path string
	}{
		{"tanggal", "/admin/orders?dari=31-10-2026"},
		{"uuid cabang", "/admin/orders?cabang=not-a-uuid"},
		{"angka halaman", "/admin/orders?halaman=abc"},
		{"angka limit", "/admin/orders?per_halaman=abc"},
		{"pencarian panjang", "/admin/orders?cari=" + url.QueryEscape(strings.Repeat("🙂", 201))},
		{"status html", "/admin/orders?status=" + url.QueryEscape("<script>alert(1)</script>")},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+tc.path, "", admin, "")
			if status != http.StatusBadRequest {
				t.Fatalf("filter invalid harus 400, dapat %d (%v)", status, body)
			}
			assertErrorShape(t, body, "VALIDATION_FAILED")
		})
	}

	p := f.seedPesananDone(t, f.branchA, "2026-10-0099", false)
	teks := `'; DROP TABLE orders; -- <script>alert(1)</script>`
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		base+"/admin/orders?cari="+url.QueryEscape(teks), "", admin, "")
	if status != http.StatusOK {
		t.Fatalf("teks mirip SQL pada pencarian adalah data, dapat %d (%v)", status, body)
	}

	isi, err := json.Marshal(map[string]string{"catatan": teks})
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		base+"/admin/orders/"+p.ID+"/notes", string(isi), admin, csrf)
	if status != http.StatusOK {
		t.Fatalf("catatan berisi SQL/HTML harus tersimpan sebagai teks, dapat %d (%v)", status, body)
	}
	if got := dataMap(t, body)["note_admin"]; got != teks {
		t.Fatalf("catatan berubah atau diproses sebagai kode: %v", got)
	}
	var jumlah int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE id=$1`, p.ID).Scan(&jumlah); err != nil || jumlah != 1 {
		t.Fatalf("injeksi teks mengubah tabel orders: jumlah=%d err=%v", jumlah, err)
	}

	isi, err = json.Marshal(map[string]string{"catatan": strings.Repeat("x", 2001)})
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		base+"/admin/orders/"+p.ID+"/notes", string(isi), admin, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("catatan melampaui 2000 harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		base+"/admin/orders/not-a-uuid", "", admin, "")
	if status != http.StatusNotFound {
		t.Fatalf("id bukan UUID harus 404, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "NOT_FOUND")
}
