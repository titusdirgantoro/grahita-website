package httpapi

// Uji integrasi pendaftaran akun cabang lewat token registrasi (F1-07) pada
// PostgreSQL nyata.
//
// Berbeda dari router_registration_test.go yang memakai penyimpanan in-memory,
// berkas ini menggerakkan router lengkap dengan repository asli sehingga
// perilaku SQL benar-benar dieksekusi: hash token SHA-256, kunci baris
// FOR UPDATE (dua pendaftaran bersamaan hanya satu yang berhasil), penandaan
// used_at/used_by, email akun yang diambil dari data cabang, serta baris
// audit_logs di dalam transaksi yang sama.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi (lihat integration_pg_test.go).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
)

// seedToken menyisipkan satu token registrasi langsung ke database dan
// mengembalikan nilai mentahnya (bentuk tampilan yang dipakai klien).
func seedToken(t *testing.T, ctx context.Context, f *pgFixture, branchID string, expiresAt time.Time) string {
	t.Helper()
	raw, prefix, err := auth.NewRegistrationToken()
	if err != nil {
		t.Fatalf("buat token uji: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO registration_tokens (branch_id, token_hash, token_prefix, expires_at) VALUES ($1,$2,$3,$4)`,
		branchID, auth.HashRegistrationToken(raw), prefix, expiresAt); err != nil {
		t.Fatalf("seed token registrasi: %v", err)
	}
	return raw
}

func TestPGPendaftaranAkunCabangLengkapDanBisaLogin(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Cabang baru tanpa akun; token dibuat lewat API admin (F1-06) sehingga
	// kedua kartu benar-benar bertemu di satu alur.
	branch := insertBranch(t, ctx, f.pool, "Grahita Cabang Registrasi", "Kepala Registrasi", "cabang-reg@example.test", true)
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/branches/"+branch+"/token", "", cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("generate token harus 200, dapat %d (%v)", status, body)
	}
	token, _ := body["data"].(map[string]any)["token"].(string)
	if token == "" {
		t.Fatal("generate token tidak mengembalikan token")
	}

	// Langkah 1: data cabang tampil terisi (read-only) — diambil dari DB.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+token+`"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("validasi token harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	if data["nama_cabang"] != "Grahita Cabang Registrasi" || data["nama_kepala"] != "Kepala Registrasi" ||
		data["email"] != "cabang-reg@example.test" {
		t.Errorf("data cabang tidak sesuai: %v", data)
	}

	// Langkah 2: daftar. Token dikirim tanpa tanda hubung + huruf kecil untuk
	// memastikan normalisasi bekerja pada jalur nyata juga.
	tanpaHubung := strings.ToLower(strings.ReplaceAll(token, "-", ""))
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/register",
		`{"token":"`+tanpaHubung+`","telepon":"0821-1111-2222","password":"rahasia123","password_konfirmasi":"rahasia123"}`, nil, "")
	if status != http.StatusCreated {
		t.Fatalf("daftar harus 201, dapat %d (%v)", status, body)
	}
	user := body["data"].(map[string]any)["user"].(map[string]any)
	userID, _ := user["id"].(string)
	if userID == "" || user["email"] != "cabang-reg@example.test" || user["branch_id"] != branch {
		t.Fatalf("akun yang dikembalikan tidak sesuai: %v", user)
	}

	// Baris akun: email = email cabang (bukan dari payload), telepon tersimpan,
	// kata sandi Argon2id, akun aktif.
	var emailLogin, telepon, hashAkun string
	var akunAktif bool
	if err := f.pool.QueryRow(ctx,
		`SELECT email::text, COALESCE(telepon,''), password_hash, is_active FROM branch_users WHERE branch_id = $1`, branch).
		Scan(&emailLogin, &telepon, &hashAkun, &akunAktif); err != nil {
		t.Fatalf("baca akun cabang baru: %v", err)
	}
	if emailLogin != "cabang-reg@example.test" || telepon != "0821-1111-2222" || !akunAktif {
		t.Errorf("baris akun tidak sesuai: %q %q %v", emailLogin, telepon, akunAktif)
	}
	if !auth.VerifyPassword(hashAkun, "rahasia123") {
		t.Error("kata sandi tersimpan harus hash Argon2id dari kata sandi pendaftaran")
	}

	// Token ditandai terpakai oleh akun itu.
	var used bool
	var usedBy string
	if err := f.pool.QueryRow(ctx,
		`SELECT used_at IS NOT NULL, COALESCE(used_by::text,'') FROM registration_tokens WHERE branch_id = $1`, branch).
		Scan(&used, &usedBy); err != nil {
		t.Fatalf("baca token registrasi: %v", err)
	}
	if !used || usedBy != userID {
		t.Errorf("token harus terpakai oleh akun baru: used=%v used_by=%q", used, usedBy)
	}

	// Audit: entity branch_user, action register, aktor akun itu sendiri.
	var actorID, actorNama, afterEmail string
	var ipAda bool
	if err := f.pool.QueryRow(ctx,
		`SELECT actor_id::text, actor_nama, ip IS NOT NULL, after->>'email' FROM audit_logs
		  WHERE entity = 'branch_user' AND action = 'register' AND entity_id = $1`, userID).
		Scan(&actorID, &actorNama, &ipAda, &afterEmail); err != nil {
		t.Fatalf("baca audit register: %v", err)
	}
	if actorID != userID || actorNama != "Grahita Cabang Registrasi" || !ipAda || afterEmail != "cabang-reg@example.test" {
		t.Errorf("audit register tidak sesuai: %q %q %v %q", actorID, actorNama, ipAda, afterEmail)
	}

	// Akun baru bisa login (kriteria kartu), dan tokennya tidak bisa dipakai lagi.
	pgLogin(t, f.srv, "cabang-reg@example.test", "rahasia123")

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/register",
		`{"token":"`+token+`","telepon":"0821","password":"rahasia123","password_konfirmasi":"rahasia123"}`, nil, "")
	if status != http.StatusConflict {
		t.Fatalf("pendaftaran kedua harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "TOKEN_USED")

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+token+`"}`, nil, "")
	if status != http.StatusConflict {
		t.Fatalf("validasi token terpakai harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "TOKEN_USED")

	// Tetap tepat satu akun untuk cabang itu (1 cabang = 1 akun).
	var jumlahAkun int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM branch_users WHERE branch_id = $1`, branch).Scan(&jumlahAkun); err != nil {
		t.Fatalf("hitung akun: %v", err)
	}
	if jumlahAkun != 1 {
		t.Errorf("harus tepat 1 akun cabang, dapat %d", jumlahAkun)
	}
}

func TestPGPendaftaranTidakBisaMemalsukanDataReadOnly(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	branch := insertBranch(t, ctx, f.pool, "Grahita Cabang Palsu", "Kepala Palsu", "cabang-palsu@example.test", true)
	token := seedToken(t, ctx, f, branch, time.Now().Add(24*time.Hour))

	payloads := []string{
		`{"token":"` + token + `","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia123","nama_cabang":"Cabang Diubah"}`,
		`{"token":"` + token + `","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia123","email":"palsu@example.test"}`,
		`{"token":"` + token + `","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia123","branch_id":"` + f.branchA + `"}`,
	}
	for _, body := range payloads {
		status, resp, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/register", body, nil, "")
		if status != http.StatusBadRequest {
			t.Fatalf("payload berisi field read-only harus 400, dapat %d (%v)", status, resp)
		}
		assertErrorShape(t, resp, "VALIDATION_FAILED")
	}

	// Tidak ada akun yang lahir dari percobaan itu, dan tokennya belum terpakai.
	var jumlah int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM branch_users WHERE branch_id = $1`, branch).Scan(&jumlah); err != nil {
		t.Fatalf("hitung akun: %v", err)
	}
	if jumlah != 0 {
		t.Errorf("permintaan palsu tidak boleh membuat akun, dapat %d", jumlah)
	}
	var used bool
	if err := f.pool.QueryRow(ctx, `SELECT used_at IS NOT NULL FROM registration_tokens WHERE branch_id = $1`, branch).Scan(&used); err != nil {
		t.Fatalf("baca token: %v", err)
	}
	if used {
		t.Error("token tidak boleh terbakar oleh payload yang ditolak")
	}
}

func TestPGPendaftaranMenolakTokenKedaluwarsaNonaktifDanBerakun(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Kedaluwarsa.
	kadaluwarsa := insertBranch(t, ctx, f.pool, "Grahita Cabang Kadaluwarsa", "Kepala", "kadaluwarsa@example.test", true)
	tokenKadaluwarsa := seedToken(t, ctx, f, kadaluwarsa, time.Now().Add(-time.Hour))
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+tokenKadaluwarsa+`"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("token kedaluwarsa harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "TOKEN_EXPIRED")

	// Cabang nonaktif.
	nonaktif := insertBranch(t, ctx, f.pool, "Grahita Cabang Mati", "Kepala", "mati@example.test", false)
	tokenNonaktif := seedToken(t, ctx, f, nonaktif, time.Now().Add(time.Hour))
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+tokenNonaktif+`"}`, nil, "")
	if status != http.StatusConflict {
		t.Fatalf("cabang nonaktif harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_INACTIVE")

	// Cabang yang sudah punya akun (token warisan/kebocoran).
	tokenBerakun := seedToken(t, ctx, f, f.branchB, time.Now().Add(time.Hour))
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+tokenBerakun+`"}`, nil, "")
	if status != http.StatusConflict {
		t.Fatalf("cabang berakun harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_HAS_ACCOUNT")

	// Token yang sudah digantikan token baru tidak dikenal lagi (F1-06).
	pengganti := insertBranch(t, ctx, f.pool, "Grahita Cabang Diganti", "Kepala", "diganti@example.test", true)
	generasi1 := seedToken(t, ctx, f, pengganti, time.Now().Add(time.Hour))
	if _, err := f.pool.Exec(ctx, `DELETE FROM registration_tokens WHERE branch_id = $1`, pengganti); err != nil {
		t.Fatalf("batalkan token lama: %v", err)
	}
	seedToken(t, ctx, f, pengganti, time.Now().Add(time.Hour))
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+generasi1+`"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("token lama harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "TOKEN_INVALID")

	// Satu pun tidak boleh menghasilkan akun.
	var jumlah int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM branch_users WHERE branch_id = ANY($1::uuid[])`,
		[]string{kadaluwarsa, nonaktif, pengganti}).Scan(&jumlah); err != nil {
		t.Fatalf("hitung akun: %v", err)
	}
	if jumlah != 0 {
		t.Errorf("tidak boleh ada akun baru dari token yang ditolak, dapat %d", jumlah)
	}
}

func TestPGDuaPendaftaranBersamaanHanyaSatuBerhasil(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	branch := insertBranch(t, ctx, f.pool, "Grahita Cabang Balapan", "Kepala", "balapan@example.test", true)
	token := seedToken(t, ctx, f, branch, time.Now().Add(time.Hour))

	type hasil struct {
		status int
		kode   string
	}
	hasilCh := make(chan hasil, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"token":"` + token + `","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia123"}`
			resp, err := http.Post(f.srv.URL+"/api/v1/auth/register", "application/json", strings.NewReader(body))
			if err != nil {
				hasilCh <- hasil{status: -1, kode: err.Error()}
				return
			}
			defer func() { _ = resp.Body.Close() }()
			var decoded map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&decoded)
			kode := ""
			if e, ok := decoded["error"].(map[string]any); ok {
				kode, _ = e["code"].(string)
			}
			hasilCh <- hasil{status: resp.StatusCode, kode: kode}
		}()
	}
	wg.Wait()
	close(hasilCh)

	var sukses, ditolak, lain int
	for h := range hasilCh {
		switch {
		case h.status == http.StatusCreated:
			sukses++
		case h.status == http.StatusConflict && h.kode == "TOKEN_USED":
			ditolak++
		default:
			lain++
			t.Errorf("hasil tak terduga: status=%d kode=%q", h.status, h.kode)
		}
	}
	if sukses != 1 || ditolak != 1 || lain != 0 {
		t.Fatalf("harus tepat 1 sukses dan 1 TOKEN_USED, dapat sukses=%d ditolak=%d lain=%d", sukses, ditolak, lain)
	}

	// Database tetap konsisten: satu akun, token terpakai sekali.
	var jumlahAkun int
	var used bool
	var usedBy string
	if err := f.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM branch_users WHERE branch_id = $1),
		        (SELECT used_at IS NOT NULL FROM registration_tokens WHERE branch_id = $1),
		        (SELECT COALESCE(used_by::text,'') FROM registration_tokens WHERE branch_id = $1)`, branch).
		Scan(&jumlahAkun, &used, &usedBy); err != nil {
		t.Fatalf("periksa keadaan akhir: %v", err)
	}
	if jumlahAkun != 1 || !used || usedBy == "" {
		t.Errorf("keadaan akhir tidak konsisten: akun=%d used=%v used_by=%q", jumlahAkun, used, usedBy)
	}

	// Akun yang menang benar-benar bisa login.
	pgLogin(t, f.srv, "balapan@example.test", "rahasia123")
}
