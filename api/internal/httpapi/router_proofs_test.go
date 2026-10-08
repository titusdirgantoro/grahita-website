package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/settings"
)

// proofsServer adalah router lengkap dengan layanan bukti in-memory: cukup
// untuk menguji seluruh perilaku HTTP tanpa PostgreSQL.
type proofsServer struct {
	srv   *httptest.Server
	store *storage.Memory
	pay   *payment.Service
	saldo map[string]string
}

const (
	proofOrderA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // milik cabang A, submitted
	proofOrderB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" // milik cabang B, submitted
	proofOrderC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc" // milik cabang A, done
)

// berkasGambar menyusun pseudo-JPEG/PNG/PDF yang lolos pemeriksaan magic bytes.
func berkasJPEGUji(isi string) []byte { return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(isi)...) }
func berkasPDFUji(isi string) []byte  { return append([]byte("%PDF-1.7\n"), []byte(isi)...) }

// newProofsServer menyiapkan router dengan setelan batas unggah yang bisa
// diatur pengujian.
func newProofsServer(t *testing.T, setelan map[string]string, limiter *httpx.RateLimiter) *proofsServer {
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
	store.PutBranch(auth.Credentials{
		UserID: "usr-b", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-b",
		Nama: "Grahita Cabang B", Email: "b@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})

	nilai := map[string]string{"biaya_packaging": "3000"}
	for k, v := range setelan {
		nilai[k] = v
	}
	settingsSvc := settings.NewService(&fakeSettingsRepo{values: nilai})

	objek := storage.NewMemory("/files")
	src := payment.NewMemoryOrderSource()
	src.Put(payment.OrderRef{ID: proofOrderA, BranchID: "br-a", Status: payment.StatusSubmitted, Nomor: "2026-10-01"})
	src.Put(payment.OrderRef{ID: proofOrderB, BranchID: "br-b", Status: payment.StatusSubmitted, Nomor: "2026-10-02"})
	src.Put(payment.OrderRef{ID: proofOrderC, BranchID: "br-a", Status: payment.StatusDone, Nomor: "2026-10-03"})
	paySvc := payment.NewService(payment.NewMemory(), src, objek, settingsSvc)

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	deps := Deps{
		Config:       testConfig(),
		Version:      "test-proofs",
		Health:       fakeHealth{migrasi: 12},
		Auth:         svc,
		Settings:     settingsSvc,
		Catalog:      nil,
		Payments:     paySvc,
		Storage:      objek,
		ProofLimiter: limiter,
	}
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &proofsServer{srv: srv, store: objek, pay: paySvc, saldo: nilai}
}

// multipartUnggah mengirim berkas bukti sebagai multipart/form-data.
func multipartUnggah(t *testing.T, url string, cookies []*http.Cookie, csrf string, berkas map[string][]byte) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for nama, isi := range berkas {
		fw, err := w.CreateFormFile(payment.FieldBerkas, nama)
		if err != nil {
			t.Fatalf("buat bagian multipart: %v", err)
		}
		if _, err := fw.Write(isi); err != nil {
			t.Fatalf("tulis isi: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tutup multipart: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("buat request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if csrf != "" {
		req.Header.Set(httpx.CSRFHeader, csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	if resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("body bukan JSON (%d): %v", resp.StatusCode, err)
		}
	}
	return resp, decoded
}

func loginUji(t *testing.T, srv *httptest.Server, email string) ([]*http.Cookie, string) {
	t.Helper()
	resp, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		`{"email":"`+email+`","password":"rahasia123"}`, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s: %d", email, resp.StatusCode)
	}
	// Cookie csrf tersedia dari respons login (double-submit).
	var cookies []*http.Cookie
	for _, c := range resp.Cookies() {
		cookies = append(cookies, c)
	}
	return cookies, csrfFromCookies(cookies)
}

func TestProofsUnggahMultiBerkasDanUnduh(t *testing.T) {
	s := newProofsServer(t, nil, nil)
	cookies, csrf := loginUji(t, s.srv, "a@example.test")

	resp, body := multipartUnggah(t, s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs", cookies, csrf,
		map[string][]byte{"transfer-1.jpg": berkasJPEGUji("foto-satu"), "transfer-2.pdf": berkasPDFUji("pdf-dua")})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("unggah harus 201, dapat %d (%v)", resp.StatusCode, body)
	}
	data, ok := body["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("harus ada 2 bukti, dapat %v", body["data"])
	}
	for _, d := range data {
		p := d.(map[string]any)
		if p["uploaded_by_kind"] != "branch" {
			t.Errorf("pengunggah cabang harus kind branch: %v", p)
		}
		if _, ada := p["storage_key"]; ada {
			t.Errorf("storage_key tidak boleh dikirim ke klien: %v", p)
		}
		if p["mime_type"] == nil || p["sha256"] == nil {
			t.Errorf("metadata bukti tidak lengkap: %v", p)
		}
	}

	// Daftar bukti.
	resp, body = doJSON(t, http.MethodGet, s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs", "", cookies, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("daftar bukti harus 200, dapat %d", resp.StatusCode)
	}
	daftar := dataList(t, body)
	if len(daftar) != 2 {
		t.Fatalf("daftar bukti harus 2, dapat %d", len(daftar))
	}
	pertama := daftar[0].(map[string]any)
	id := pertama["id"].(string)

	// Tautan unduh berumur pendek.
	resp, body = doJSON(t, http.MethodGet,
		s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs/"+id+"/download", "", cookies, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unduh harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	dl := dataMap(t, body)
	if dl["expires_in"] != float64(300) {
		t.Errorf("expires_in harus 300, dapat %v", dl["expires_in"])
	}
	if u, _ := dl["url"].(string); !strings.Contains(u, "download=") {
		t.Errorf("tautan unduh harus memaksa nama berkas: %v", dl["url"])
	}

	// Isi berkas benar-benar masuk object storage pada kunci ACAK (tanpa nama
	// kiriman klien) — diperiksa dari sisi domain, bukan dari respons HTTP.
	actorA := auth.Actor{UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a"}
	bukti, err := s.pay.List(context.Background(), actorA, proofOrderA)
	if err != nil {
		t.Fatalf("daftar bukti domain: %v", err)
	}
	if len(bukti) != 2 {
		t.Fatalf("bukti domain harus 2, dapat %d", len(bukti))
	}
	for _, p := range bukti {
		if !strings.HasPrefix(p.StorageKey, "proofs/"+proofOrderA+"/") {
			t.Errorf("kunci objek salah: %q", p.StorageKey)
		}
		if strings.Contains(p.StorageKey, "transfer-1") || strings.Contains(p.StorageKey, "transfer-2") {
			t.Errorf("kunci objek tidak boleh memakai nama kiriman: %q", p.StorageKey)
		}
	}
}

func TestProofsIsolasiCabangDanStatus(t *testing.T) {
	s := newProofsServer(t, nil, nil)
	cookiesB, csrfB := loginUji(t, s.srv, "b@example.test")

	// Cabang B tidak bisa mengunggah/melihat/mengunduh bukti pesanan cabang A.
	resp, body := multipartUnggah(t, s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs", cookiesB, csrfB,
		map[string][]byte{"x.jpg": berkasJPEGUji("x")})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unggah lintas cabang harus 404, dapat %d (%v)", resp.StatusCode, body)
	}
	resp, body = doJSON(t, http.MethodGet, s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs", "", cookiesB, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("daftar lintas cabang harus 404, dapat %d", resp.StatusCode)
	}
	resp, body = doJSON(t, http.MethodGet,
		s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs/aaaaaaaa-1111-4111-8111-111111111111/download", "", cookiesB, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unduh lintas cabang harus 404, dapat %d", resp.StatusCode)
	}

	// Id pesanan bukan UUID → 404, bukan 500.
	resp, _ = multipartUnggah(t, s.srv.URL+"/api/v1/orders/bukan-uuid/proofs", cookiesB, csrfB,
		map[string][]byte{"x.jpg": berkasJPEGUji("x")})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("id bukan UUID harus 404, dapat %d", resp.StatusCode)
	}

	// Cabang A pada pesanan Done → 409 ORDER_LOCKED.
	cookiesA, csrfA := loginUji(t, s.srv, "a@example.test")
	resp, body = multipartUnggah(t, s.srv.URL+"/api/v1/orders/"+proofOrderC+"/proofs", cookiesA, csrfA,
		map[string][]byte{"x.jpg": berkasJPEGUji("x")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pesanan Done untuk cabang harus 409, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "ORDER_LOCKED")

	// Tanpa sesi: middleware CSRF berjalan lebih dulu untuk permintaan non-GET
	// (403 CSRF_INVALID — perilaku yang didokumentasikan di router.go),
	// sedangkan GET tanpa sesi dijawab 401.
	resp, body = multipartUnggah(t, s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs", nil, "",
		map[string][]byte{"x.jpg": berkasJPEGUji("x")})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tanpa sesi & CSRF harus 403 CSRF_INVALID, dapat %d", resp.StatusCode)
	}
	assertErrorShape(t, body, "CSRF_INVALID")
	resp, body = doJSON(t, http.MethodGet, s.srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs", "", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET tanpa sesi harus 401, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")
}

func TestProofsValidasiJenisBerkas(t *testing.T) {
	s := newProofsServer(t, nil, nil)
	cookies, csrf := loginUji(t, s.srv, "a@example.test")
	url := s.srv.URL + "/api/v1/orders/" + proofOrderA + "/proofs"

	// PDF berkedok .jpg → 400 dengan field `file`.
	resp, body := multipartUnggah(t, url, cookies, csrf,
		map[string][]byte{"palsu.jpg": berkasPDFUji("sebenarnya pdf")})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ekstensi palsu harus 400, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	if fields, _ := body["error"].(map[string]any)["fields"].(map[string]any); fields[payment.FieldBerkas] == nil {
		t.Errorf("error harus menunjuk field %q: %v", payment.FieldBerkas, body)
	}

	// Skrip berkedok .jpg → 400.
	resp, body = multipartUnggah(t, url, cookies, csrf,
		map[string][]byte{"jahat.jpg": []byte("#!/bin/sh\nrm -rf /")})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("skrip harus 400, dapat %d (%v)", resp.StatusCode, body)
	}

	// Tanpa berkas → 400.
	var kosong bytes.Buffer
	w := multipart.NewWriter(&kosong)
	_ = w.WriteField("catatan", "tidak ada berkas")
	_ = w.Close()
	req, _ := http.NewRequest(http.MethodPost, url, &kosong)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set(httpx.CSRFHeader, csrf)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	hasil, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim: %v", err)
	}
	defer func() { _ = hasil.Body.Close() }()
	if hasil.StatusCode != http.StatusBadRequest {
		t.Fatalf("tanpa berkas harus 400, dapat %d", hasil.StatusCode)
	}

	// Bukan multipart sama sekali → 400.
	resp, body = doJSON(t, http.MethodPost, url, `{"file":"bukan berkas"}`, cookies, csrf)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bukan multipart harus 400, dapat %d (%v)", resp.StatusCode, body)
	}
}

func TestProofsDuplikatDanBatasJumlah(t *testing.T) {
	s := newProofsServer(t, map[string]string{"max_file": "2"}, nil)
	cookies, csrf := loginUji(t, s.srv, "a@example.test")
	url := s.srv.URL + "/api/v1/orders/" + proofOrderA + "/proofs"

	if resp, body := multipartUnggah(t, url, cookies, csrf,
		map[string][]byte{"a.jpg": berkasJPEGUji("isi-sama")}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("unggah pertama: %d (%v)", resp.StatusCode, body)
	}
	// Isi identik, nama berbeda → 409 PROOF_DUPLICATE.
	resp, body := multipartUnggah(t, url, cookies, csrf,
		map[string][]byte{"b.jpg": berkasJPEGUji("isi-sama")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplikat harus 409, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "PROOF_DUPLICATE")

	// max_file=2 → unggahan kedua yang berbeda boleh, ketiga ditolak 409 PROOF_LIMIT.
	if resp, body := multipartUnggah(t, url, cookies, csrf,
		map[string][]byte{"c.jpg": berkasJPEGUji("isi-lain")}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("unggah kedua: %d (%v)", resp.StatusCode, body)
	}
	resp, body = multipartUnggah(t, url, cookies, csrf, map[string][]byte{"d.jpg": berkasJPEGUji("isi-ketiga")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("melebihi max_file harus 409, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "PROOF_LIMIT")
}

func TestProofsBatasUkuran(t *testing.T) {
	// max_mb=1: berkas 2 MB ditolak 413; permintaan yang lebih besar dari
	// max_file × max_mb juga 413.
	s := newProofsServer(t, map[string]string{"max_mb": "1", "max_file": "1"}, nil)
	cookies, csrf := loginUji(t, s.srv, "a@example.test")
	url := s.srv.URL + "/api/v1/orders/" + proofOrderA + "/proofs"

	besar := berkasJPEGUji(strings.Repeat("x", 2*1024*1024))
	resp, body := multipartUnggah(t, url, cookies, csrf, map[string][]byte{"besar.jpg": besar})
	if resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("berkas melebihi max_mb harus 413/400, dapat %d (%v)", resp.StatusCode, body)
	}
}

func TestProofsLajuDibatasi(t *testing.T) {
	// Limiter 1 permintaan: unggahan kedua dalam jendela yang sama → 429.
	s := newProofsServer(t, nil, httpx.NewRateLimiter(1, time.Minute))
	cookies, csrf := loginUji(t, s.srv, "a@example.test")
	url := s.srv.URL + "/api/v1/orders/" + proofOrderA + "/proofs"

	if resp, _ := multipartUnggah(t, url, cookies, csrf, map[string][]byte{"a.jpg": berkasJPEGUji("a")}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("unggah pertama harus 201, dapat %d", resp.StatusCode)
	}
	resp, body := multipartUnggah(t, url, cookies, csrf, map[string][]byte{"b.jpg": berkasJPEGUji("b")})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unggahan kedua harus 429, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "RATE_LIMITED")
}

func TestProofsAdminUnggahAtasNamaCabang(t *testing.T) {
	s := newProofsServer(t, nil, nil)
	cookies, csrf := loginUji(t, s.srv, "budi@example.test")

	resp, body := multipartUnggah(t, s.srv.URL+"/api/v1/admin/orders/"+proofOrderA+"/proofs", cookies, csrf,
		map[string][]byte{"cicilan.pdf": berkasPDFUji("cicilan")})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin unggah harus 201, dapat %d (%v)", resp.StatusCode, body)
	}
	data := dataList(t, body)
	if len(data) != 1 {
		t.Fatalf("harus 1 bukti, dapat %d", len(data))
	}
	p := data[0].(map[string]any)
	if p["uploaded_by_kind"] != "admin" || p["uploaded_by_nama"] != "Budi" {
		t.Errorf("pengunggah admin harus ditandai nama: %v", p)
	}

	// admin melihat bukti pesanan cabang mana pun.
	resp, body = doJSON(t, http.MethodGet, s.srv.URL+"/api/v1/admin/orders/"+proofOrderB+"/proofs", "", cookies, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin daftar bukti cabang lain harus 200, dapat %d", resp.StatusCode)
	}

	// Admin boleh menambah bukti pada pesanan Done (koreksi).
	resp, body = multipartUnggah(t, s.srv.URL+"/api/v1/admin/orders/"+proofOrderC+"/proofs", cookies, csrf,
		map[string][]byte{"koreksi.jpg": berkasJPEGUji("koreksi")})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin pada pesanan Done harus 201, dapat %d (%v)", resp.StatusCode, body)
	}
}

func TestProofsStorageBelumSiap(t *testing.T) {
	hash, _ := auth.HashPassword("rahasia123")
	store := auth.NewMemory()
	store.PutBranch(auth.Credentials{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a",
		Nama: "Grahita Cabang A", Email: "a@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	settingsSvc := settings.NewService(&fakeSettingsRepo{values: map[string]string{}})
	src := payment.NewMemoryOrderSource()
	src.Put(payment.OrderRef{ID: proofOrderA, BranchID: "br-a", Status: payment.StatusSubmitted})
	paySvc := payment.NewService(payment.NewMemory(), src, storage.Disabled{}, settingsSvc)

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	srv := httptest.NewServer(NewRouter(Deps{
		Config:   testConfig(),
		Version:  "test-proofs-disabled",
		Health:   fakeHealth{migrasi: 12},
		Auth:     svc,
		Settings: settingsSvc,
		Payments: paySvc,
	}))
	t.Cleanup(srv.Close)

	cookies, csrf := loginUji(t, srv, "a@example.test")
	resp, body := multipartUnggah(t, srv.URL+"/api/v1/orders/"+proofOrderA+"/proofs", cookies, csrf,
		map[string][]byte{"a.jpg": berkasJPEGUji("a")})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("storage belum siap harus 503, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "UNAVAILABLE")
}
