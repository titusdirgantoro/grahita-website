package httpapi

// Uji integrasi beranda cabang (kartu F1-19) pada PostgreSQL nyata: pengingat
// C&C lewat jatuh tempo HARUS benar-benar berasal dari respons server
// `GET /api/v1/orders` (bukan data tiruan di UI), dan membacanya TIDAK boleh
// mengubah status pesanan (tanpa auto-Done, PRD CP-CNC-06/07).
//
// Uji ini lahir dari temuan QA putaran 1: beranda.tsx hanya menampilkan
// pengingat bila `o.jatuh_tempo` ada, sementara `GET /orders` belum pernah
// mengirimkannya (`ListOrdersForBranch` tidak memilih kolom itu dan `Repo.List`
// tidak mengisinya) padahal OpenAPI `Order.jatuh_tempo` sudah didokumentasikan.

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// pesananDariList mencari satu baris `GET /orders` berdasarkan id.
func pesananDariList(t *testing.T, body map[string]any, id string) map[string]any {
	t.Helper()
	for _, item := range dataList(t, body) {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("baris pesanan bukan objek: %v", item)
		}
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("pesanan %s tidak ada di GET /orders: %v", id, body["data"])
	return nil
}

// sisaHariCNC meniru perhitungan beranda.tsx (`pengingatCNC` + `selisihHari`):
// selisih hari kalender dari `jatuhTempo` ke tanggal bisnis WIB. Nilai negatif
// berarti C&C sudah lewat jatuh tempo sehingga pengingat tampil.
func sisaHariCNC(jatuhTempo, hariIniWIB string) int {
	a, errA := time.Parse("2006-01-02", jatuhTempo)
	b, errB := time.Parse("2006-01-02", hariIniWIB)
	if errA != nil || errB != nil {
		return 0
	}
	return int(a.Sub(b).Hours() / 24)
}

// judulPengingatCNC adalah teks pengingat yang dihasilkan beranda.tsx untuk
// `sisaHari` tertentu (dipakai sebagai oracle kriteria "C&C overdue tampil").
func judulPengingatCNC(sisa int) string {
	switch {
	case sisa == 0:
		return "C&C jatuh tempo hari ini"
	case sisa < 0:
		return "C&C lewat jatuh tempo " + strconv.Itoa(-sisa) + " hari"
	default:
		return "C&C jatuh tempo " + strconv.Itoa(sisa) + " hari lagi"
	}
}

// nilaiPtr menampilkan pointer string sebagai teks untuk pesan galat.
func nilaiPtr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestPGBerandaPengingatCNCOverdueDariResponsNyata membuktikan janji kartu
// F1-19 "C&C overdue tampil tanpa auto-Done" pada alur server nyata: pesanan
// C&C dibuat lewat submit, tanggalnya digeser ke masa lalu (jatuh tempo
// terlewat), lalu `GET /api/v1/orders` harus membawa `jatuh_tempo` yang sama
// dengan kolom DB dan membiarkan statusnya `submitted`.
func TestPGBerandaPengingatCNCOverdueDariResponsNyata(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_cnc = true WHERE id = $1`, f.branchA); err != nil {
		t.Fatalf("aktifkan flag C&C cabang A: %v", err)
	}
	setSetting(t, f, "cnc_hari", "7")

	cabangA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	cabangB, csrfB := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")

	// 1) Alur nyata: draft -> submit C&C. Server yang mengisi jatuh_tempo.
	draftCNC := buatDraftHTTP(t, f, cabangA, csrfA, produk, 1)
	idCNC, _ := draftCNC["id"].(string)
	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+idCNC+"/submit", cabangA, csrfA, nil, "true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit C&C harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	if hasil := dataMap(t, body); hasil["is_cnc"] != true || hasil["jatuh_tempo"] == nil || hasil["jatuh_tempo"] == "" {
		t.Fatalf("submit C&C harus mengisi jatuh_tempo: %v", hasil)
	}

	// 2) Pesanan non-C&C pembanding: jatuh_tempo-nya harus tetap kosong.
	draftBiasa := buatDraftHTTP(t, f, cabangA, csrfA, produk, 1)
	idBiasa, _ := draftBiasa["id"].(string)
	respBiasa, bodyBiasa := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+idBiasa+"/submit", cabangA, csrfA,
		map[string][]byte{"bukti.jpg": berkasJPEGUji("bukti-beranda-f119")}, "")
	if respBiasa.StatusCode != http.StatusOK {
		t.Fatalf("submit non-C&C harus 200, dapat %d (%v)", respBiasa.StatusCode, bodyBiasa)
	}

	// 3) Geser pesanan C&C ke masa lalu (tanggal 10 hari lalu, jatuh tempo 3
	//    hari lalu) memakai tanggal bisnis DATABASE (WIB) supaya sama dengan
	//    yang dibandingkan beranda. Ini keadaan nyata "C&C sudah lewat tempo".
	hariIniWIB, _, _ := wibHariIni(t, f)
	var tanggalDB, jatuhTempoDB string
	if err := f.pool.QueryRow(ctx,
		`UPDATE orders SET tanggal = wib_today() - 10, jatuh_tempo = wib_today() - 3
		  WHERE id = $1 RETURNING tanggal::text, jatuh_tempo::text`, idCNC).
		Scan(&tanggalDB, &jatuhTempoDB); err != nil {
		t.Fatalf("geser tanggal pesanan C&C: %v", err)
	}
	if jatuhTempoDB >= hariIniWIB {
		t.Fatalf("prasyarat uji: jatuh tempo %s harus sebelum hari ini WIB %s", jatuhTempoDB, hariIniWIB)
	}

	// Keadaan pra-baca: status submitted, belum Done.
	bacaKeadaan := func(id string) (status, verif string, doneAt *string, events int) {
		t.Helper()
		if err := f.pool.QueryRow(ctx,
			`SELECT status::text, verif_status::text, done_at::text,
			        (SELECT count(*) FROM order_events e WHERE e.order_id = $1)
			   FROM orders WHERE id = $1`, id).Scan(&status, &verif, &doneAt, &events); err != nil {
			t.Fatalf("baca keadaan %s: %v", id, err)
		}
		return
	}
	statusPra, verifPra, donePra, eventsPra := bacaKeadaan(idCNC)

	// 4) Beranda membaca daftar pesanan cabang sesi lewat HTTP nyata.
	statusHTTP, listBody, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/orders", "", cabangA, csrfA)
	if statusHTTP != http.StatusOK {
		t.Fatalf("GET /orders harus 200, dapat %d (%v)", statusHTTP, listBody)
	}
	barisCNC := pesananDariList(t, listBody, idCNC)

	// [KIAT PEMBLOKIR QA] jatuh_tempo kini benar-benar dibawa GET /orders dan
	// nilainya sama dengan kolom DB.
	if barisCNC["jatuh_tempo"] != jatuhTempoDB {
		t.Errorf("GET /orders harus membawa jatuh_tempo %q, dapat %v", jatuhTempoDB, barisCNC["jatuh_tempo"])
	}
	if barisCNC["is_cnc"] != true {
		t.Errorf("pesanan C&C harus is_cnc=true: %v", barisCNC)
	}
	if barisCNC["tanggal"] != tanggalDB {
		t.Errorf("tanggal pesanan = %v, mau %q", barisCNC["tanggal"], tanggalDB)
	}
	// Tanpa auto-Done: status tetap submitted meski tenggat terlewat.
	if barisCNC["status"] != "submitted" {
		t.Errorf("C&C lewat jatuh tempo harus tetap submitted (tanpa auto-Done), dapat %v", barisCNC["status"])
	}
	if donePra != nil {
		t.Errorf("done_at seharusnya NULL: %v", nilaiPtr(donePra))
	}

	// 5) Prasyarat pengingat persis seperti pengingatCNC di beranda.tsx:
	//    is_cnc && status=="submitted" && jatuh_tempo terisi && sisa hari < 0.
	jt, _ := barisCNC["jatuh_tempo"].(string)
	if jt == "" {
		t.Fatal("pengingat C&C tidak akan tampil: server mengirim jatuh_tempo kosong")
	}
	sisa := sisaHariCNC(jt, hariIniWIB)
	if sisa >= 0 {
		t.Errorf("pengingat C&C tidak akan tampil: sisa %d hari (harus negatif)", sisa)
	}
	judul := judulPengingatCNC(sisa)
	if judul != "C&C lewat jatuh tempo 3 hari" {
		t.Errorf("judul pengingat = %q, mau %q", judul, "C&C lewat jatuh tempo 3 hari")
	}
	// Bukti untuk QA: baris nyata dari GET /orders + pengingat yang dihasilkan.
	t.Logf("GET /orders baris C&C: is_cnc=%v status=%v tanggal=%v jatuh_tempo=%v", barisCNC["is_cnc"], barisCNC["status"], barisCNC["tanggal"], barisCNC["jatuh_tempo"])
	t.Logf("pengingat beranda dari data server: judul=%q sisa_hari=%d hari_ini_WIB=%s", judul, sisa, hariIniWIB)

	// Pesanan non-C&C tidak membocorkan jatuh_tempo.
	barisBiasa := pesananDariList(t, listBody, idBiasa)
	if v, ada := barisBiasa["jatuh_tempo"]; ada && v != nil && v != "" {
		t.Errorf("non-C&C tidak boleh membawa jatuh_tempo: %v", v)
	}

	// 6) Isolasi cabang: pesanan C&C cabang A tidak muncul di daftar cabang B.
	statusHTTP, listB, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/orders", "", cabangB, csrfB)
	if statusHTTP != http.StatusOK {
		t.Fatalf("GET /orders cabang B harus 200, dapat %d (%v)", statusHTTP, listB)
	}
	for _, item := range dataList(t, listB) {
		if item.(map[string]any)["id"] == idCNC {
			t.Error("pesanan C&C cabang A bocor ke daftar cabang B")
		}
	}

	// 7) Membaca beranda tidak mengubah apa pun (tanpa auto-Done / tanpa efek
	//    samping): status, verif, done_at, dan jumlah timeline tetap sama.
	statusPasca, verifPasca, donePasca, eventsPasca := bacaKeadaan(idCNC)
	if statusPasca != statusPra || verifPasca != verifPra ||
		nilaiPtr(donePasca) != nilaiPtr(donePra) || eventsPasca != eventsPra {
		t.Errorf("membaca beranda mengubah pesanan: status %s->%s verif %s->%s done %s->%s events %d->%d",
			statusPra, statusPasca, verifPra, verifPasca, nilaiPtr(donePra), nilaiPtr(donePasca), eventsPra, eventsPasca)
	}

	// 8) Idempoten: GET ulang tetap membawa jatuh tempo yang sama dan status
	//    submission yang sama.
	statusHTTP, listBody, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/orders", "", cabangA, csrfA)
	if statusHTTP != http.StatusOK {
		t.Fatalf("GET /orders ulang harus 200, dapat %d (%v)", statusHTTP, listBody)
	}
	ulang := pesananDariList(t, listBody, idCNC)
	if ulang["jatuh_tempo"] != jatuhTempoDB || ulang["status"] != "submitted" {
		t.Errorf("GET /orders ulang tidak stabil: %v", ulang)
	}
}
