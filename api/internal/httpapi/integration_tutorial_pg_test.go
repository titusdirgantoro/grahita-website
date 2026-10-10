package httpapi

// Uji integrasi konten tutorial pada PostgreSQL nyata (kartu F2-27).
//
// Berbeda dari router_tutorial_test.go (repository in-memory), berkas ini
// menjalankan migrasi + repository sqlc sungguhan sehingga perilaku SQL-nya
// benar-benar dieksekusi: urutan tampil, transaksi all-or-nothing, dan audit
// per topik (before/after) di tabel audit_logs.
//
// Dunia uji bersama (pgFixture) dipakai apa adanya: layanan tutorial dipasang
// pada salinan deps milik uji ini sehingga berkas fixture bersama tidak perlu
// disentuh. Fixture mengosongkan tabel domain (termasuk tutorial_topics lewat
// CASCADE dari admin_users), jadi konten dibuat lewat API — persis seperti
// Superadmin memakainya di produksi. Ketersediaan empat topik awal SETELAH
// MIGRASI diuji terpisah di internal/platform/database/schema_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/tutorial"
	tutorialpg "github.com/grahita/portal/api/internal/tutorial/postgres"
)

// routerTutorialPG menyusun router dengan layanan tutorial di atas PostgreSQL
// nyata pada pool fixture bersama.
func (f *pgFixture) routerTutorialPG(t *testing.T) *httptest.Server {
	t.Helper()
	f.deps.Tutorial = tutorial.NewService(tutorialpg.New(f.pool))
	srv := httptest.NewServer(NewRouter(f.deps))
	t.Cleanup(srv.Close)
	return srv
}

// topikTutorial menyusun body {"topik":[...]} untuk PUT konten tutorial.
func topikTutorial(t *testing.T, topik []map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"topik": topik})
	if err != nil {
		t.Fatalf("susun body tutorial: %v", err)
	}
	return string(b)
}

// empatTopikAwal menyalin isi empat topik awal (migrasi 0006) dalam bentuk
// body API — kontennya sama dengan yang tampil di portal cabang sebelum
// konten dinamis diperkenalkan.
func empatTopikAwal() []map[string]any {
	return []map[string]any{
		{"ikon": "memesan", "judul": "Cara memesan",
			"ringkas": "Dari memilih produk sampai pesanan terkirim ke Pusat.",
			"langkah": []string{"Buka menu Pesanan lalu pilih “Buat pesanan”.",
				"Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”."}},
		{"ikon": "bukti", "judul": "Cara unggah bukti transfer",
			"ringkas": "Bukti yang jelas mempercepat verifikasi Pusat.",
			"langkah": []string{"Gunakan foto atau tangkapan layar yang jelas: JPG, PNG, atau PDF.",
				"Nominal seluruh bukti dijumlahkan saat verifikasi Pusat."}},
		{"ikon": "sertifikat", "judul": "Aturan produk Sertifikat",
			"ringkas": "Sertifikat selalu dipesan terpisah dari produk reguler.",
			"langkah": []string{"Harga satuan Sertifikat selalu Rp0 berapa pun jumlah lembarnya."}},
		{"ikon": "cnc", "judul": "Pesanan Cash & Carry",
			"ringkas": "Bayar sebelum jatuh tempo yang dihitung dari tanggal pesanan.",
			"langkah": []string{"Jatuh tempo = tanggal pesanan + jumlah hari C&C yang berlaku (bawaan 7 hari)."}},
	}
}

// langkahDari mengubah field langkah respons menjadi []string.
func langkahDari(t *testing.T, topik map[string]any) []string {
	t.Helper()
	raw, ok := topik["langkah"].([]any)
	if !ok {
		t.Fatalf("langkah bukan array: %v", topik)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("langkah bukan teks: %v", v)
		}
		out = append(out, s)
	}
	return out
}

func TestPGKontenTutorialTerbacaTanpaDeployDanTeraudit(t *testing.T) {
	f := newPGFixture(t)
	srv := f.routerTutorialPG(t)
	base := srv.URL + "/api/v1"
	ctx := context.Background()

	cabang, _ := pgLogin(t, srv, "a@example.test", "rahasia123")
	sa, saCsrf := pgLogin(t, srv, "rina@example.test", "rahasia123")

	// Titik awal (tabel dibersihkan fixture): portal cabang membaca konten
	// dari API, bukan dari bundel portal.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca tutorial harus 200, dapat %d (%v)", status, body)
	}
	if n := len(topikRespons(t, body)); n != 0 {
		t.Fatalf("konten awal harus kosong, dapat %d topik", n)
	}

	// 1) Superadmin membuat empat topik (isi awal) lewat API.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial",
		topikTutorial(t, empatTopikAwal()), sa, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("simpan konten awal harus 200, dapat %d (%v)", status, body)
	}
	awal := topikRespons(t, body)
	if len(awal) != 4 {
		t.Fatalf("harus 4 topik tersimpan, dapat %d", len(awal))
	}
	ids := make([]string, 0, 4)
	for i, tp := range awal {
		id, _ := tp["id"].(string)
		if id == "" {
			t.Fatalf("topik[%d] tanpa id", i)
		}
		ids = append(ids, id)
	}
	// Urutan tampil = posisi array yang dikirim.
	var urutan []int
	rows, err := f.pool.Query(ctx, `SELECT urutan FROM tutorial_topics ORDER BY urutan`)
	if err != nil {
		t.Fatalf("baca urutan: %v", err)
	}
	for rows.Next() {
		var u int
		if err := rows.Scan(&u); err != nil {
			t.Fatalf("scan urutan: %v", err)
		}
		urutan = append(urutan, u)
	}
	rows.Close()
	if len(urutan) != 4 || urutan[0] != 10 || urutan[3] != 40 {
		t.Fatalf("urutan tersimpan = %v, ingin 10/20/30/40", urutan)
	}

	// 2) Portal cabang — permintaan GET baru — langsung melihat konten itu.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca ulang harus 200, dapat %d", status)
	}
	baca := topikRespons(t, body)
	judulAwal := []string{"Cara memesan", "Cara unggah bukti transfer", "Aturan produk Sertifikat", "Pesanan Cash & Carry"}
	if len(baca) != 4 {
		t.Fatalf("portal cabang harus melihat 4 topik, dapat %d", len(baca))
	}
	for i, j := range judulAwal {
		if baca[i]["judul"] != j {
			t.Errorf("topik[%d].judul = %v, ingin %q", i, baca[i]["judul"], j)
		}
	}

	// 3) Audit: empat entri create dengan aktor Superadmin, tanpa snapshot
	//    "before" (belum ada nilai sebelumnya).
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE entity = 'tutorial' AND action = 'create'`).Scan(&n); err != nil {
		t.Fatalf("hitung audit create: %v", err)
	}
	if n != 4 {
		t.Errorf("audit create = %d, ingin 4", n)
	}
	var aktor string
	var before *string
	var afterJudul string
	if err := f.pool.QueryRow(ctx,
		`SELECT coalesce(actor_id::text,''), before::text, after->>'judul'
		   FROM audit_logs WHERE entity = 'tutorial' AND action = 'create' ORDER BY at LIMIT 1`).
		Scan(&aktor, &before, &afterJudul); err != nil {
		t.Fatalf("baca audit create: %v", err)
	}
	if aktor != f.adminSA {
		t.Errorf("aktor audit = %q, ingin %q", aktor, f.adminSA)
	}
	if before != nil {
		t.Errorf("audit create tidak boleh punya snapshot before: %v", *before)
	}
	if afterJudul != "Cara memesan" {
		t.Errorf("snapshot after tidak memuat judul: %q", afterJudul)
	}

	// 4) Ubah satu topik (judul, ringkasan, langkah) dan buang satu topik:
	//    perubahan langsung terlihat pada pembacaan berikutnya, sedangkan
	//    topik yang tidak berubah TIDAK menghasilkan audit baru.
	ubah := []map[string]any{
		{"id": ids[0], "ikon": "memesan", "judul": "Cara memesan",
			"ringkas": "Dari memilih produk sampai pesanan terkirim ke Pusat.",
			"langkah": []string{"Buka menu Pesanan lalu pilih “Buat pesanan”.",
				"Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”."}},
		{"id": ids[1], "ikon": "bukti", "judul": "Cara unggah bukti transfer",
			"ringkas": "Bukti yang jelas mempercepat verifikasi Pusat.",
			"langkah": []string{"Gunakan foto atau tangkapan layar yang jelas: JPG, PNG, atau PDF.",
				"Nominal seluruh bukti dijumlahkan saat verifikasi Pusat."}},
		{"id": ids[3], "ikon": "cnc", "judul": "Pesanan Cash & Carry (diperbarui)",
			"ringkas": "Bayar sebelum jatuh tempo.",
			"langkah": []string{"Jatuh tempo = tanggal pesanan + jumlah hari C&C (bawaan 7 hari).", "Pengingat dikirim ke email cabang."}},
		// ids[2] (Sertifikat) sengaja tidak dikirim → dihapus.
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial",
		topikTutorial(t, ubah), sa, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("ubah konten harus 200, dapat %d (%v)", status, body)
	}
	baru := topikRespons(t, body)
	if len(baru) != 3 {
		t.Fatalf("harus 3 topik setelah perubahan, dapat %d", len(baru))
	}
	if baru[2]["judul"] != "Pesanan Cash & Carry (diperbarui)" || baru[2]["ikon"] != "cnc" {
		t.Errorf("urutan tampil tidak mengikuti array: %v", baru[2])
	}
	if l := langkahDari(t, baru[2]); len(l) != 2 {
		t.Errorf("langkah topik ketiga = %v", l)
	}

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca setelah ubah harus 200, dapat %d", status)
	}
	baca = topikRespons(t, body)
	if len(baca) != 3 || baca[2]["judul"] != "Pesanan Cash & Carry (diperbarui)" {
		t.Fatalf("portal cabang tidak mencerminkan perubahan: %v", baca)
	}
	for _, tp := range baca {
		if tp["judul"] == "Aturan produk Sertifikat" {
			t.Errorf("topik yang tidak dikirim harus terhapus: %v", tp)
		}
	}
	// Urutan tersimpan dinomori ulang sesuai posisi array (10, 20, 30).
	var urutan2 []int
	rows2, err := f.pool.Query(ctx, `SELECT urutan FROM tutorial_topics ORDER BY urutan`)
	if err != nil {
		t.Fatalf("baca urutan setelah ubah: %v", err)
	}
	for rows2.Next() {
		var u int
		if err := rows2.Scan(&u); err != nil {
			t.Fatalf("scan urutan: %v", err)
		}
		urutan2 = append(urutan2, u)
	}
	rows2.Close()
	if len(urutan2) != 3 || urutan2[0] != 10 || urutan2[2] != 30 {
		t.Fatalf("urutan setelah ubah = %v, ingin 10/20/30", urutan2)
	}

	// 5) Audit per topik: satu update + satu delete, lengkap dengan snapshot
	//    before/after.
	var nUpdate, nDelete int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE entity = 'tutorial' AND action = 'update'`).Scan(&nUpdate); err != nil {
		t.Fatalf("hitung audit update: %v", err)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE entity = 'tutorial' AND action = 'delete'`).Scan(&nDelete); err != nil {
		t.Fatalf("hitung audit delete: %v", err)
	}
	if nUpdate != 1 {
		t.Errorf("audit update = %d, ingin 1 (hanya topik yang berubah)", nUpdate)
	}
	if nDelete != 1 {
		t.Errorf("audit delete = %d, ingin 1", nDelete)
	}
	var entityID string
	var beforeJudul, afterJudul2, afterRingkas string
	if err := f.pool.QueryRow(ctx,
		`SELECT entity_id::text, before->>'judul', after->>'judul', after->>'ringkas'
		   FROM audit_logs WHERE entity = 'tutorial' AND action = 'update' ORDER BY at DESC LIMIT 1`).
		Scan(&entityID, &beforeJudul, &afterJudul2, &afterRingkas); err != nil {
		t.Fatalf("baca audit update: %v", err)
	}
	if entityID != ids[3] {
		t.Errorf("entity_id audit = %q, ingin %q", entityID, ids[3])
	}
	if beforeJudul != "Pesanan Cash & Carry" || afterJudul2 != "Pesanan Cash & Carry (diperbarui)" {
		t.Errorf("snapshot before/after salah: %q -> %q", beforeJudul, afterJudul2)
	}
	if afterRingkas != "Bayar sebelum jatuh tempo." {
		t.Errorf("snapshot after.ringkas = %q", afterRingkas)
	}
	// Snapshot audit tidak pernah memuat rahasia: isinya konten saja.
	var jumlahKunci int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT jsonb_object_keys(after) AS k FROM audit_logs
		   WHERE entity = 'tutorial' AND action = 'update') kunci`).Scan(&jumlahKunci); err != nil {
		t.Fatalf("periksa kunci snapshot: %v", err)
	}
	if jumlahKunci != 5 {
		t.Errorf("snapshot audit harus 5 kunci konten, dapat %d", jumlahKunci)
	}

	// 6) Mengirim ulang konten yang sama tidak menambah audit (no-op).
	sebelum := jumlahAuditTutorial(t, f)
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial",
		topikTutorial(t, ubah), sa, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("ulang simpan harus 200, dapat %d", status)
	}
	if sesudah := jumlahAuditTutorial(t, f); sesudah != sebelum {
		t.Errorf("no-op menambah audit: %d -> %d", sebelum, sesudah)
	}
}

// jumlahAuditTutorial menghitung seluruh entri audit konten tutorial.
func jumlahAuditTutorial(t *testing.T, f *pgFixture) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE entity = 'tutorial'`).Scan(&n); err != nil {
		t.Fatalf("hitung audit tutorial: %v", err)
	}
	return n
}

func TestPGTutorialSanitasiDanHakAkses(t *testing.T) {
	f := newPGFixture(t)
	srv := f.routerTutorialPG(t)
	base := srv.URL + "/api/v1"
	ctx := context.Background()

	cabang, cabangCsrf := pgLogin(t, srv, "a@example.test", "rahasia123")
	mgr, mgrCsrf := pgLogin(t, srv, "budi@example.test", "rahasia123")
	sa, saCsrf := pgLogin(t, srv, "rina@example.test", "rahasia123")

	// Konten berbahaya lewat Superadmin → tersimpan dalam bentuk tersanitasi.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial",
		topikTutorial(t, []map[string]any{{
			"ikon":    "umum",
			"judul":   "Panduan <b>aman</b><script>alert('xss')</script>",
			"ringkas": "<img src=x onerror=alert(1)>Ringkasan bersih.",
			"langkah": []string{"Klik <a href='javascript:alert(1)'>di sini</a>",
				"<style>body{display:none}</style>Selesai"},
		}}), sa, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("simpan konten harus 200, dapat %d (%v)", status, body)
	}
	simpan := topikRespons(t, body)
	if len(simpan) != 1 || simpan[0]["judul"] != "Panduan aman" || simpan[0]["ringkas"] != "Ringkasan bersih." {
		t.Fatalf("konten tersimpan belum tersanitasi: %v", simpan)
	}
	if l := langkahDari(t, simpan[0]); len(l) != 2 || l[0] != "Klik di sini" || l[1] != "Selesai" {
		t.Fatalf("langkah tersanitasi = %v", l)
	}

	// Nilai di DATABASE juga bersih (bukan hanya respons API).
	var judul, ringkas, langkahJSON string
	if err := f.pool.QueryRow(ctx,
		`SELECT judul, ringkas, langkah::text FROM tutorial_topics`).Scan(&judul, &ringkas, &langkahJSON); err != nil {
		t.Fatalf("baca konten tutorial: %v", err)
	}
	gabung := judul + " " + ringkas + " " + langkahJSON
	for _, kotor := range []string{"<", ">", "script", "onerror", "javascript"} {
		if strings.Contains(gabung, kotor) {
			t.Errorf("konten database masih memuat %q: %s", kotor, gabung)
		}
	}

	// Manager dan akun cabang tidak punya jalur tulis; isinya tidak berubah.
	ubahJudul := topikTutorial(t, []map[string]any{{
		"ikon": "umum", "judul": "Diubah Manager", "ringkas": "Ringkas.",
		"langkah": []string{"Satu"},
	}})
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
		csrf    string
	}{{"cabang", cabang, cabangCsrf}, {"manager", mgr, mgrCsrf}} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial", ubahJudul, aktor.cookies, aktor.csrf)
		if status != http.StatusForbidden {
			t.Errorf("%s ubah tutorial harus 403, dapat %d (%v)", aktor.nama, status, body)
		}
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/tutorial", "", aktor.cookies, "")
		if status != http.StatusForbidden {
			t.Errorf("%s baca /admin/tutorial harus 403, dapat %d", aktor.nama, status)
		}
	}
	var jumlah, jumlahAudit int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tutorial_topics`).Scan(&jumlah); err != nil {
		t.Fatalf("hitung topik: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE entity = 'tutorial'`).Scan(&jumlahAudit); err != nil {
		t.Fatalf("hitung audit: %v", err)
	}
	if jumlah != 1 || jumlahAudit != 1 {
		t.Errorf("penolakan role tidak boleh mengubah data: topik=%d audit=%d", jumlah, jumlahAudit)
	}

	// Konten tidak valid (ikon di luar daftar) → 400 dan tidak ada penulisan.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial",
		topikTutorial(t, []map[string]any{{
			"ikon": "inline-svg-jahat", "judul": "Judul", "ringkas": "Ringkas.",
			"langkah": []string{"Satu"},
		}}), sa, saCsrf)
	if status != http.StatusBadRequest {
		t.Fatalf("ikon tidak dikenal harus 400, dapat %d (%v)", status, body)
	}
	errObj, _ := body["error"].(map[string]any)
	if fields, ok := errObj["fields"].(map[string]any); !ok || fields["topik[0].ikon"] == nil {
		t.Errorf("pesan validasi per bagian tidak ada: %v", body)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tutorial_topics`).Scan(&jumlah); err != nil {
		t.Fatalf("hitung topik: %v", err)
	}
	if jumlah != 1 {
		t.Errorf("konten tidak valid tidak boleh tersimpan: %d topik", jumlah)
	}
	if n := jumlahAuditTutorial(t, f); n != 1 {
		t.Errorf("konten tidak valid tidak boleh menambah audit: %d", n)
	}

	// Portal cabang memakai konten dari database.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca harus 200, dapat %d", status)
	}
	baca := topikRespons(t, body)
	if len(baca) != 1 || baca[0]["judul"] != "Panduan aman" {
		t.Fatalf("portal cabang tidak memakai konten database: %v", baca)
	}

	// Kunci ilustrasi yang dikenal portal tetap kunci bawaan (bukan berkas).
	if !tutorial.IkonSah("umum") || tutorial.IkonSah("inline-svg-jahat") {
		t.Errorf("whitelist ikon tidak konsisten")
	}
}
