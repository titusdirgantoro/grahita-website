package httpapi

// Uji integrasi pengelolaan akun pengguna (F1-05) pada PostgreSQL nyata.
//
// Berbeda dari router_users_test.go yang memakai penyimpanan in-memory, berkas
// ini menggerakkan router lengkap dengan repository asli (users/postgres,
// auth/postgres) sehingga perilaku SQL benar-benar dieksekusi: status akun,
// kolom login terakhir, pencabutan sesi, aktivasi admin (password_hash +
// activated_at), penerima dokumen ber-flag, dan proteksi superadmin aktif
// terakhir pada dua permintaan bersamaan.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi (lihat integration_pg_test.go).

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/users"
	userspg "github.com/grahita/portal/api/internal/users/postgres"
)

// hashUji menghasilkan hash Argon2id kata sandi uji standar.
func hashUji(t *testing.T) string {
	t.Helper()
	h, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	return h
}

// putJSON mengirim PUT tanpa memanggil t.Fatal sehingga aman dipakai dari
// beberapa goroutine sekaligus.
func putJSON(base, path, body string, cookies []*http.Cookie, csrf string) (int, error) {
	req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set(httpx.CSRFHeader, csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func TestPGAkunCabangStatusDanLoginTerakhir(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Cabang keempat yang belum punya akun + token registrasi aktif.
	belum := insertBranch(t, ctx, f.pool, "Grahita Belum Registrasi", "Kepala D", "cabang-d@example.test", true)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO registration_tokens (branch_id, token_hash, token_prefix, expires_at)
		 VALUES ($1, sha256('token-uji'::bytea), 'GRH-D123', now() + interval '10 days')`, belum); err != nil {
		t.Fatalf("seed token registrasi: %v", err)
	}
	// Cabang nonaktif tanpa akun: statusnya harus nonaktif.
	nonaktif := insertBranch(t, ctx, f.pool, "Grahita Nonaktif", "Kepala E", "cabang-e@example.test", false)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_users (branch_id, email, password_hash, is_active)
		 VALUES ($1, 'cabang-e@example.test', 'x', true)`, nonaktif); err != nil {
		t.Fatalf("seed akun cabang nonaktif: %v", err)
	}

	// Login cabang A supaya login terakhirnya terisi.
	pgLogin(t, f.srv, "a@example.test", "rahasia123")

	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/users/branch", "", cookiesMgr, "")
	if status != http.StatusOK {
		t.Fatalf("daftar akun cabang harus 200, dapat %d (%v)", status, body)
	}
	perBranch := map[string]map[string]any{}
	for _, item := range body["data"].([]any) {
		row := item.(map[string]any)
		perBranch[row["branch_id"].(string)] = row
	}

	barisA := perBranch[f.branchA]
	if barisA["status"] != "aktif" {
		t.Errorf("cabang A harus aktif, dapat %v", barisA)
	}
	if barisA["last_login_at"] == nil {
		t.Errorf("login terakhir cabang A harus terisi setelah login, dapat %v", barisA)
	}
	if perBranch[f.branchB]["status"] != "aktif" || perBranch[f.branchB]["last_login_at"] != nil {
		t.Errorf("cabang B belum pernah login, login terakhir harus null: %v", perBranch[f.branchB])
	}
	if perBranch[f.branchC]["status"] != "nonaktif" {
		t.Errorf("cabang C (induk nonaktif) harus nonaktif, dapat %v", perBranch[f.branchC])
	}
	if perBranch[belum]["status"] != "belum_registrasi" || perBranch[belum]["has_account"] != false {
		t.Errorf("cabang tanpa akun harus belum_registrasi, dapat %v", perBranch[belum])
	}
	if perBranch[belum]["token_prefix"] != "GRH-D123" {
		t.Errorf("token prefix harus tampil untuk cabang belum registrasi, dapat %v", perBranch[belum]["token_prefix"])
	}
	if perBranch[nonaktif]["status"] != "nonaktif" {
		t.Errorf("cabang nonaktif harus berstatus nonaktif, dapat %v", perBranch[nonaktif])
	}

	// Sesi cabang A hidup, lalu dinonaktifkan lewat API.
	cookiesA, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userA, `{"is_active":false}`, cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan akun cabang harus 200, dapat %d (%v)", status, body)
	}
	if body["data"].(map[string]any)["status"] != "nonaktif" {
		t.Errorf("respons harus berstatus nonaktif, dapat %v", body["data"])
	}
	// Sesi akun yang dinonaktifkan langsung gugur, dan login barunya ditolak.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesA, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi akun nonaktif harus 401, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"a@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Errorf("login akun nonaktif harus 403, dapat %d", status)
	}
}

func TestPGResetCabangBerhasilSampaiKataSandiBaru(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userB+"/reset", "", cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("kirim ulang tautan reset harus 200, dapat %d (%v)", status, body)
	}
	if f.mail.jumlah() != 1 {
		t.Fatalf("harus ada 1 email tautan reset, dapat %d", f.mail.jumlah())
	}
	token := f.mail.tokenTerakhir(t)

	// Token benar-benar tersimpan (hash) untuk akun cabang B.
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM password_reset_tokens WHERE user_id = $1 AND used_at IS NULL`, f.userB).Scan(&n); err != nil {
		t.Fatalf("hitung token reset: %v", err)
	}
	if n != 1 {
		t.Fatalf("harus ada 1 tautan aktif untuk cabang B, dapat %d", n)
	}

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("pakai tautan reset harus 204, dapat %d (%v)", status, body)
	}
	pgLogin(t, f.srv, "b@example.test", "sandibaru123")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"b@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("kata sandi lama harus ditolak, dapat %d", status)
	}

	// Mengirim ulang tautan untuk akun yang tidak ada → 404.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/branch/00000000-0000-0000-0000-000000000000/reset", "", cookiesMgr, csrfMgr)
	if status != http.StatusNotFound {
		t.Errorf("akun tak dikenal harus 404, dapat %d", status)
	}
}

func TestPGUndangAdminAktivasiDanPenerimaDokumen(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Admin nonaktif ber-flag: tidak boleh menerima dokumen.
	if _, err := f.pool.Exec(ctx,
		`UPDATE admin_users SET notif_invoice = true WHERE email = 'mantan@example.test'`); err != nil {
		t.Fatalf("nyalakan flag admin nonaktif: %v", err)
	}

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Dewi Lestari","email":"dewi@example.test","role":"manager","notif_invoice":true}`,
		cookiesSA, csrfSA)
	if status != http.StatusCreated {
		t.Fatalf("undang admin harus 201, dapat %d (%v)", status, body)
	}
	admin := body["data"].(map[string]any)
	if admin["status"] != "undangan" {
		t.Errorf("admin baru harus berstatus undangan, dapat %v", admin)
	}
	id, _ := admin["id"].(string)

	// Di database: password_hash dan activated_at masih NULL, invited_at terisi.
	var hashNull, activatedNull bool
	var invited bool
	if err := f.pool.QueryRow(ctx,
		`SELECT password_hash IS NULL, activated_at IS NULL, invited_at IS NOT NULL FROM admin_users WHERE id = $1`, id).
		Scan(&hashNull, &activatedNull, &invited); err != nil {
		t.Fatalf("baca admin undangan: %v", err)
	}
	if !hashNull || !activatedNull || !invited {
		t.Errorf("undangan harus password_hash NULL, activated_at NULL, invited_at terisi: %v/%v/%v", hashNull, activatedNull, invited)
	}

	// Admin undangan tidak bisa login.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"dewi@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("admin undangan harus 401, dapat %d", status)
	}

	// Penerima dokumen: hanya admin aktif ber-flag DAN sudah diaktivasi.
	invoice, err := f.users.DocumentRecipients(ctx, users.DocInvoice)
	if err != nil {
		t.Fatalf("penerima invoice: %v", err)
	}
	if len(invoice) != 0 {
		t.Errorf("belum ada admin aktif ber-flag invoice yang diaktivasi, dapat %v", invoice)
	}

	// Email undangan diantre dan memuat tautan aktivasi.
	antre := f.queue.terakhir(t)
	if antre.Jenis != users.JenisUndanganAdmin || len(antre.To) != 1 || antre.To[0] != "dewi@example.test" {
		t.Fatalf("antrean undangan tidak sesuai: %+v", antre)
	}
	tautan, _ := antre.Payload["tautan"].(string)
	token := tokenDariTautan(t, tautan)

	// Aktivasi lewat tautan: password_hash DAN activated_at terisi bersamaan.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("aktivasi harus 204, dapat %d (%v)", status, body)
	}
	var hashTerisi, activatedTerisi bool
	if err := f.pool.QueryRow(ctx,
		`SELECT password_hash IS NOT NULL, activated_at IS NOT NULL FROM admin_users WHERE id = $1`, id).
		Scan(&hashTerisi, &activatedTerisi); err != nil {
		t.Fatalf("baca admin teraktivasi: %v", err)
	}
	if !hashTerisi || !activatedTerisi {
		t.Fatalf("aktivasi WAJIB mengisi password_hash dan activated_at: %v/%v", hashTerisi, activatedTerisi)
	}

	// Login berhasil, dan daftar admin kini menampilkan status aktif.
	pgLogin(t, f.srv, "dewi@example.test", "sandibaru123")
	_, list, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/users/admin", "", cookiesSA, "")
	var statusDewi string
	for _, item := range list["data"].([]any) {
		row := item.(map[string]any)
		if row["email"] == "dewi@example.test" {
			statusDewi, _ = row["status"].(string)
		}
	}
	if statusDewi != "aktif" {
		t.Errorf("admin teraktivasi harus berstatus aktif, dapat %q", statusDewi)
	}

	// Sesudah aktivasi ia menerima invoice; admin nonaktif tidak.
	invoice, err = f.users.DocumentRecipients(ctx, users.DocInvoice)
	if err != nil {
		t.Fatalf("penerima invoice sesudah aktivasi: %v", err)
	}
	if len(invoice) != 1 || invoice[0] != "dewi@example.test" {
		t.Errorf("hanya admin aktif teraktivasi ber-flag yang menerima invoice, dapat %v", invoice)
	}

	// Email ganda ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Lain","email":"dewi@example.test","role":"manager"}`, cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("email ganda harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "EMAIL_TAKEN")
}

func TestPGProteksiSuperadminAktifTerakhir(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Berurutan: satu-satunya superadmin aktif tidak boleh diturunkan.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/admin/"+f.adminSA, `{"role":"manager"}`, cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("menurunkan superadmin terakhir harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "LAST_SUPERADMIN")

	// Tambah superadmin aktif kedua. Dua permintaan BERSAMAAN akan mencoba
	// menurunkan keduanya; agar benar-benar bersamaan, kunci penasihat
	// dipegang lebih dulu dari koneksi lain sehingga kedua permintaan pasti
	// menunggu di titik serialisasi yang sama.
	kedua := insertAdmin(t, ctx, f.pool, "Andi Wijaya", "andi@example.test", hashUji(t), "superadmin", true, true)

	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("ambil koneksi untuk memegang kunci: %v", err)
	}
	penahan, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatalf("mulai transaksi penahan: %v", err)
	}
	if _, err := penahan.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, userspg.AdminMutationLockKey); err != nil {
		t.Fatalf("pegang kunci mutasi admin: %v", err)
	}
	lepas := func() {
		_ = penahan.Commit(ctx)
		conn.Release()
	}
	t.Cleanup(func() { lepas() })

	targets := []string{f.adminSA, kedua}
	kode := make([]int, len(targets))
	galat := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, id := range targets {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			kode[i], galat[i] = putJSON(f.srv.URL, "/api/v1/admin/users/admin/"+id,
				`{"role":"manager"}`, cookiesSA, csrfSA)
		}(i, id)
	}

	// Selama kunci masih dipegang, TIDAK boleh ada satu pun yang selesai:
	// bila ada yang selesai di sini, mutasi admin tidak menunggu kunci.
	selesai := make(chan struct{})
	go func() { wg.Wait(); close(selesai) }()
	select {
	case <-selesai:
		t.Fatalf("mutasi admin selesai padahal kunci masih dipegang (status %v | %v)", kode, galat)
	case <-time.After(500 * time.Millisecond):
	}

	// Lepaskan kunci: kedua permintaan berjalan berurutan dan tepat satu
	// boleh berhasil.
	lepas()
	select {
	case <-selesai:
	case <-time.After(10 * time.Second):
		t.Fatalf("mutasi admin tidak selesai setelah kunci dilepas")
	}

	sukses, ditolak := 0, 0
	for i, k := range kode {
		if galat[i] != nil {
			t.Fatalf("permintaan bersamaan gagal: %v", galat[i])
		}
		switch k {
		case http.StatusOK:
			sukses++
		case http.StatusConflict:
			ditolak++
		default:
			t.Fatalf("status tak terduga dari mutasi bersamaan: %d", k)
		}
	}
	if sukses != 1 || ditolak != 1 {
		t.Fatalf("harus tepat satu berhasil dan satu ditolak, dapat sukses=%d ditolak=%d", sukses, ditolak)
	}

	var sisa int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_users WHERE role = 'superadmin' AND is_active`).Scan(&sisa); err != nil {
		t.Fatalf("hitung superadmin aktif: %v", err)
	}
	if sisa != 1 {
		t.Fatalf("harus tersisa tepat 1 superadmin aktif di database, dapat %d", sisa)
	}
}

// TestPGAdminNonaktifTidakBisaLoginDanSesiDicabut memastikan admin yang
// dinonaktifkan lewat API langsung kehilangan sesinya di database.
func TestPGAdminNonaktifTidakBisaLoginDanSesiDicabut(t *testing.T) {
	f := newPGFixture(t)

	cookiesMgr, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/admin/"+f.adminMgr, `{"is_active":false}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan admin harus 200, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesMgr, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi admin nonaktif harus 401, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"budi@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Errorf("login admin nonaktif harus 403, dapat %d", status)
	}
	// Flag notifikasi boleh diubah pada admin nonaktif tanpa menghidupkannya.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/admin/"+f.adminMgr, `{"notif_surat_jalan":true}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Errorf("mengubah flag admin nonaktif harus 200, dapat %d", status)
	}
	var aktif bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT is_active FROM admin_users WHERE id = $1`, f.adminMgr).Scan(&aktif); err != nil {
		t.Fatalf("baca status admin: %v", err)
	}
	if aktif {
		t.Error("mengubah flag tidak boleh mengaktifkan kembali admin")
	}
}
