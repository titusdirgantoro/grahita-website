package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/tutorial"
)

// fakeTutorialRepo adalah repository konten tutorial in-memory untuk uji
// transport (uji integrasi PostgreSQL nyata ada di
// integration_tutorial_pg_test.go).
type fakeTutorialRepo struct {
	topik    []tutorial.Topic
	panggil  int
	recs     []audit.Entry
	urutBaru int
}

func (r *fakeTutorialRepo) List(context.Context) ([]tutorial.Topic, error) {
	return append([]tutorial.Topic(nil), r.topik...), nil
}

func (r *fakeTutorialRepo) Apply(_ context.Context, actorID string, tambah, ubah []tutorial.Topic, hapus []string, recs []audit.Entry) error {
	r.panggil++
	r.recs = append(r.recs, recs...)
	buang := map[string]bool{}
	for _, id := range hapus {
		buang[id] = true
	}
	ganti := map[string]tutorial.Topic{}
	for _, t := range ubah {
		ganti[t.ID] = t
	}
	akhir := make([]tutorial.Topic, 0, len(r.topik)+len(tambah))
	for _, t := range r.topik {
		if buang[t.ID] {
			continue
		}
		if g, ok := ganti[t.ID]; ok {
			akhir = append(akhir, g)
			continue
		}
		akhir = append(akhir, t)
	}
	for _, t := range tambah {
		r.urutBaru++
		t.ID = fmt.Sprintf("topik-baru-%d", r.urutBaru)
		akhir = append(akhir, t)
	}
	sort.SliceStable(akhir, func(i, j int) bool { return akhir[i].Urutan < akhir[j].Urutan })
	r.topik = akhir
	return nil
}

// topikAwalUji adalah empat topik awal yang juga diisi migrasi 0006.
func topikAwalUji() []tutorial.Topic {
	bahan := []struct {
		id, ikon, judul, ringkas string
		langkah                  []string
	}{
		{"11111111-1111-1111-1111-111111111111", tutorial.IkonMemesan, "Cara memesan", "Dari memilih produk sampai pesanan terkirim ke Pusat.", []string{"Buka menu Pesanan lalu pilih “Buat pesanan”."}},
		{"22222222-2222-2222-2222-222222222222", tutorial.IkonBukti, "Cara unggah bukti transfer", "Bukti yang jelas mempercepat verifikasi Pusat.", []string{"Gunakan foto atau tangkapan layar yang jelas: JPG, PNG, atau PDF."}},
		{"33333333-3333-3333-3333-333333333333", tutorial.IkonSertifikat, "Aturan produk Sertifikat", "Sertifikat selalu dipesan terpisah dari produk reguler.", []string{"Buat pesanan baru khusus untuk produk Sertifikat."}},
		{"44444444-4444-4444-4444-444444444444", tutorial.IkonCNC, "Pesanan Cash & Carry", "Bayar sebelum jatuh tempo yang dihitung dari tanggal pesanan.", []string{"Opsi Cash & Carry hanya muncul untuk cabang yang diizinkan Pusat."}},
	}
	out := make([]tutorial.Topic, 0, len(bahan))
	for i, b := range bahan {
		out = append(out, tutorial.Topic{ID: b.id, Ikon: b.ikon, Judul: b.judul, Ringkas: b.ringkas, Langkah: b.langkah, Urutan: (i + 1) * 10})
	}
	return out
}

type tutorialServer struct {
	srv  *httptest.Server
	repo *fakeTutorialRepo
}

func newTutorialServer(t *testing.T) *tutorialServer {
	t.Helper()

	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store := auth.NewMemory()
	store.PutBranch(auth.Credentials{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a",
		Nama: "Grahita Cabang A", Email: "a@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})

	repo := &fakeTutorialRepo{topik: topikAwalUji()}
	deps := Deps{
		Config:       testConfig(),
		Version:      "test-tutorial",
		Health:       fakeHealth{migrasi: 9},
		Auth:         auth.NewService(store, store, store, clock.System(), testConfig().SessionIdle, testConfig().SessionAbsolute),
		Tutorial:     tutorial.NewService(repo),
		LoginLimiter: httpx.NewRateLimiter(5, 15*time.Minute),
	}
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &tutorialServer{srv: srv, repo: repo}
}

// topikRespons mengambil daftar topik dari body respons {data:{topik:[...]}}.
func topikRespons(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("body tanpa data: %v", body)
	}
	raw, ok := data["topik"].([]any)
	if !ok {
		t.Fatalf("data tanpa topik: %v", data)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("topik bukan objek: %v", r)
		}
		out = append(out, m)
	}
	return out
}

// TestApiTutorialBacaUntukSesiCabang memastikan konten tutorial cabang dibaca
// dari API (bukan ditanam di bundel portal) dan hanya untuk sesi yang sah.
func TestApiTutorialBacaUntukSesiCabang(t *testing.T) {
	ts := newTutorialServer(t)
	base := ts.srv.URL + "/api/v1"

	// Tanpa sesi → 401 (tidak ada permukaan publik untuk konten ini).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("tanpa sesi harus 401, dapat %d", status)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")

	// Akun cabang membaca empat topik awal beserta kunci ilustrasinya.
	cabang, _ := login(t, ts.srv, "a@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca tutorial harus 200, dapat %d (%v)", status, body)
	}
	topik := topikRespons(t, body)
	if len(topik) != 4 {
		t.Fatalf("harus 4 topik, dapat %d", len(topik))
	}
	ingin := []string{"Cara memesan", "Cara unggah bukti transfer", "Aturan produk Sertifikat", "Pesanan Cash & Carry"}
	for i, j := range ingin {
		if topik[i]["judul"] != j {
			t.Errorf("topik[%d].judul = %v, ingin %q", i, topik[i]["judul"], j)
		}
		if topik[i]["id"] == "" || topik[i]["ikon"] == "" {
			t.Errorf("topik[%d] tanpa id/ikon: %v", i, topik[i])
		}
		if langkah, ok := topik[i]["langkah"].([]any); !ok || len(langkah) == 0 {
			t.Errorf("topik[%d] tanpa langkah: %v", i, topik[i])
		}
	}

	// Manager boleh membaca (halaman informasi), tetapi TIDAK boleh mengubah.
	mgr, _ := login(t, ts.srv, "budi@example.test")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", mgr, "")
	if status != http.StatusOK {
		t.Errorf("manager baca tutorial harus 200, dapat %d", status)
	}
}

// TestApiTutorialTulisHanyaSuperadmin memastikan hak akses API: hanya
// Superadmin yang boleh mengubah konten, dan perubahan langsung terbaca di
// endpoint baca cabang (tanpa build/deploy).
func TestApiTutorialTulisHanyaSuperadmin(t *testing.T) {
	ts := newTutorialServer(t)
	base := ts.srv.URL + "/api/v1"

	cabang, cabangCsrf := login(t, ts.srv, "a@example.test")
	mgr, mgrCsrf := login(t, ts.srv, "budi@example.test")

	// Baca admin: cabang & Manager ditolak, Superadmin boleh.
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
	}{{"cabang", cabang}, {"manager", mgr}} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/tutorial", "", aktor.cookies, "")
		if status != http.StatusForbidden {
			t.Errorf("%s baca /admin/tutorial harus 403, dapat %d", aktor.nama, status)
		}
		assertErrorShape(t, body, "FORBIDDEN")
	}

	// Tulis: cabang & Manager 403 tanpa jejak apa pun.
	ubah := `{"topik":[{"id":"11111111-1111-1111-1111-111111111111","ikon":"memesan","judul":"Cara memesan (baru)","ringkas":"Ringkas baru.","langkah":["Langkah baru."]}]}`
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
		csrf    string
	}{{"cabang", cabang, cabangCsrf}, {"manager", mgr, mgrCsrf}} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial", ubah, aktor.cookies, aktor.csrf)
		if status != http.StatusForbidden {
			t.Errorf("%s ubah tutorial harus 403, dapat %d (%v)", aktor.nama, status, body)
		}
		assertErrorShape(t, body, "FORBIDDEN")
	}
	if ts.repo.panggil != 0 {
		t.Fatalf("tidak boleh ada penulisan sebelum Superadmin mengubah (panggil=%d)", ts.repo.panggil)
	}

	// Superadmin mengubah judul, ringkasan, dan jumlah langkah sebuah topik.
	sa, saCsrf := login(t, ts.srv, "rina@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial", ubah, sa, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("superadmin ubah tutorial harus 200, dapat %d (%v)", status, body)
	}
	simpan := topikRespons(t, body)
	if len(simpan) != 1 || simpan[0]["judul"] != "Cara memesan (baru)" {
		t.Fatalf("respons simpan salah: %v", simpan)
	}
	if ts.repo.panggil != 1 {
		t.Fatalf("harus tepat satu penulisan, dapat %d", ts.repo.panggil)
	}
	if len(ts.repo.recs) != 4 {
		t.Fatalf("audit harus 4 entri (1 ubah + 3 hapus), dapat %d", len(ts.repo.recs))
	}
	for _, r := range ts.repo.recs {
		if r.Entity != audit.EntityTutorial || r.ActorID != "adm-sa" {
			t.Errorf("entri audit salah: %+v", r)
		}
		if r.Before != nil {
			if m, ok := r.Before.(map[string]any); ok {
				if _, ada := m["judul"]; !ada {
					t.Errorf("snapshot before tanpa judul: %v", m)
				}
			}
		}
	}

	// Portal cabang yang di-refresh langsung melihat versi baru — tanpa build.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca ulang harus 200, dapat %d", status)
	}
	baru := topikRespons(t, body)
	if len(baru) != 1 || baru[0]["judul"] != "Cara memesan (baru)" {
		t.Fatalf("portal cabang belum mencerminkan perubahan: %v", baru)
	}
	if langkah, _ := baru[0]["langkah"].([]any); len(langkah) != 1 || langkah[0] != "Langkah baru." {
		t.Fatalf("langkah portal cabang salah: %v", baru[0]["langkah"])
	}

	// Mengirim ulang konten yang sama tidak menulis apa pun (no-op).
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial", ubah, sa, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("ulang simpan harus 200, dapat %d", status)
	}
	if ts.repo.panggil != 1 {
		t.Fatalf("no-op tidak boleh menulis (panggil=%d)", ts.repo.panggil)
	}
}

// TestApiTutorialSanitasiKonten memastikan HTML berbahaya tidak pernah
// tersimpan maupun terkirim ke portal: jawabannya sudah bersih dari markup.
func TestApiTutorialSanitasiKonten(t *testing.T) {
	ts := newTutorialServer(t)
	base := ts.srv.URL + "/api/v1"
	sa, saCsrf := login(t, ts.srv, "rina@example.test")

	body := `{"topik":[{"id":"11111111-1111-1111-1111-111111111111","ikon":"memesan",` +
		`"judul":"Cara <b>memesan</b><script>alert(1)</script>",` +
		`"ringkas":"<img src=x onerror=alert(1)>Ringkas aman.",` +
		`"langkah":["Klik <a href=\"javascript:alert(1)\">di sini</a>","<style>p{}</style>Selesai"]}]}`
	status, resp, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial", body, sa, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("simpan konten harus 200, dapat %d (%v)", status, resp)
	}
	topik := topikRespons(t, resp)
	if len(topik) != 1 {
		t.Fatalf("harus 1 topik, dapat %d", len(topik))
	}
	if topik[0]["judul"] != "Cara memesan" {
		t.Errorf("judul tersanitasi = %v", topik[0]["judul"])
	}
	if topik[0]["ringkas"] != "Ringkas aman." {
		t.Errorf("ringkasan tersanitasi = %v", topik[0]["ringkas"])
	}
	if langkah, _ := topik[0]["langkah"].([]any); len(langkah) != 2 || langkah[0] != "Klik di sini" || langkah[1] != "Selesai" {
		t.Errorf("langkah tersanitasi = %v", topik[0]["langkah"])
	}

	// Yang tersimpan di repository pun sudah bersih.
	if len(ts.repo.topik) != 1 {
		t.Fatalf("repository harus memuat 1 topik, dapat %d", len(ts.repo.topik))
	}
	if teks := strings.Join(ts.repo.topik[0].Langkah, " ") + ts.repo.topik[0].Judul + ts.repo.topik[0].Ringkas; strings.ContainsAny(teks, "<>") {
		t.Errorf("konten tersimpan masih memuat markup: %q", teks)
	}

	// Respons portal cabang juga bebas markup.
	cabang, _ := login(t, ts.srv, "a@example.test")
	status, resp, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca harus 200, dapat %d", status)
	}
	mentah := fmt.Sprintf("%v", resp)
	if strings.Contains(mentah, "<") || strings.Contains(mentah, "script") {
		t.Errorf("respons portal cabang masih memuat markup: %s", mentah)
	}
}

// TestApiTutorialValidasiAllOrNothing memastikan konten tidak valid dijawab
// 400 dengan pesan per bagian dan TIDAK menyimpan apa pun.
func TestApiTutorialValidasiAllOrNothing(t *testing.T) {
	ts := newTutorialServer(t)
	base := ts.srv.URL + "/api/v1"
	sa, saCsrf := login(t, ts.srv, "rina@example.test")

	body := `{"topik":[{"ikon":"bukan-ilustrasi","judul":"","ringkas":"","langkah":[]}]}`
	status, resp, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/tutorial", body, sa, saCsrf)
	if status != http.StatusBadRequest {
		t.Fatalf("konten tidak valid harus 400, dapat %d (%v)", status, resp)
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("body tanpa error: %v", resp)
	}
	if errObj["code"] != "VALIDATION_FAILED" {
		t.Errorf("kode error = %v", errObj["code"])
	}
	fields, ok := errObj["fields"].(map[string]any)
	if !ok {
		t.Fatalf("validasi tanpa fields: %v", errObj)
	}
	for _, f := range []string{"topik[0].ikon", "topik[0].judul", "topik[0].ringkas", "topik[0].langkah"} {
		if _, ada := fields[f]; !ada {
			t.Errorf("field %q tidak dilaporkan: %v", f, fields)
		}
	}
	if ts.repo.panggil != 0 {
		t.Fatalf("konten tidak valid tidak boleh disimpan (panggil=%d)", ts.repo.panggil)
	}
	cabang, _ := login(t, ts.srv, "a@example.test")
	status, resp, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/tutorial", "", cabang, "")
	if status != http.StatusOK || len(topikRespons(t, resp)) != 4 {
		t.Fatalf("konten lama harus tetap utuh, dapat %d %v", status, resp)
	}
}

// TestApiTutorialTidakAdaJalurTulisCabang memastikan endpoint baca cabang
// memang hanya bisa dibaca (tidak ada permukaan tulis lintas-role).
func TestApiTutorialTidakAdaJalurTulisCabang(t *testing.T) {
	ts := newTutorialServer(t)
	base := ts.srv.URL + "/api/v1"
	cabang, cabangCsrf := login(t, ts.srv, "a@example.test")

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		status, _, _ := apiReq(t, http.DefaultClient, method, base+"/tutorial", `{}`, cabang, cabangCsrf)
		if status != http.StatusMethodNotAllowed {
			t.Errorf("%s /tutorial harus 405, dapat %d", method, status)
		}
	}
	if ts.repo.panggil != 0 {
		t.Fatalf("tidak boleh ada penulisan dari portal cabang (panggil=%d)", ts.repo.panggil)
	}
}
