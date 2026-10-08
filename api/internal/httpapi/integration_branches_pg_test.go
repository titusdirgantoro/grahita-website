package httpapi

// Uji integrasi master cabang (F1-06) pada PostgreSQL nyata.
//
// Berbeda dari router_branches_test.go yang memakai penyimpanan in-memory,
// berkas ini menggerakkan router lengkap dengan repository asli
// (branch/postgres, auth/postgres, settings/postgres) sehingga perilaku SQL
// benar-benar dieksekusi: konflik email citext, penyelarasan email login akun
// cabang, token registrasi yang disimpan sebagai SHA-256, penggantian token
// lama, larangan hapus cabang berp Pesanan, serta baris audit_logs yang
// tercatat di dalam transaksi mutasi.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi (lihat integration_pg_test.go).

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
)

// auditTerakhir memuat satu entri audit terbaru untuk entity/action tertentu.
func (f *pgFixture) hitungAudit(t *testing.T, entity, action, actorID string) int {
	t.Helper()
	var n int
	err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE entity = $1 AND action = $2 AND actor_id = $3`,
		entity, action, actorID).Scan(&n)
	if err != nil {
		t.Fatalf("hitung audit %s/%s: %v", entity, action, err)
	}
	return n
}

func TestPGCrudCabangKonflikEmailDanAudit(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")

	// ---- create ----
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches",
		`{"nama":"Grahita Cabang F","nama_kepala":"Kepala F","email":"Cabang-F@Example.Test","telepon":"0812","is_cnc":true,"is_special":true,"pajak_in":true}`,
		cookiesMgr, csrfMgr)
	if status != http.StatusCreated {
		t.Fatalf("buat cabang harus 201, dapat %d (%v)", status, body)
	}
	dibuat := body["data"].(map[string]any)
	id, _ := dibuat["id"].(string)
	if dibuat["email"] != "cabang-f@example.test" {
		t.Errorf("email tersimpan harus huruf kecil (citext), dapat %v", dibuat["email"])
	}

	// Baris benar-benar ada di database, flag PI/Special tersimpan sebagai
	// atribut (tanpa efek harga sesuai keputusan user).
	var nama, email string
	var cnc, special, pi, aktif bool
	if err := f.pool.QueryRow(ctx,
		`SELECT nama, email::text, is_cnc, is_special, pajak_in, is_active FROM branches WHERE id = $1`, id).
		Scan(&nama, &email, &cnc, &special, &pi, &aktif); err != nil {
		t.Fatalf("baca cabang baru: %v", err)
	}
	if nama != "Grahita Cabang F" || email != "cabang-f@example.test" || !cnc || !special || !pi || !aktif {
		t.Errorf("baris cabang tidak sesuai: %s %s %v %v %v %v", nama, email, cnc, special, pi, aktif)
	}

	// Audit create tercatat dengan aktor, IP, dan snapshot "after" lengkap
	// (termasuk id cabang yang baru dibuat).
	var actorNama, entitas, afterID string
	var ipAda bool
	var afterNama string
	if err := f.pool.QueryRow(ctx,
		`SELECT actor_nama, entity, ip IS NOT NULL, after->>'nama', after->>'id'
		   FROM audit_logs
		  WHERE entity = 'branch' AND action = 'create' AND entity_id = $1`, id).
		Scan(&actorNama, &entitas, &ipAda, &afterNama, &afterID); err != nil {
		t.Fatalf("baca audit create: %v", err)
	}
	if actorNama != "Budi" || entitas != "branch" || !ipAda || afterNama != "Grahita Cabang F" || afterID != id {
		t.Errorf("audit create tidak sesuai: %s %s %v %s %s", actorNama, entitas, ipAda, afterNama, afterID)
	}

	// ---- konflik email (beda huruf besar/kecil, citext) ----
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches",
		`{"nama":"Duplikat","nama_kepala":"Kepala","email":"CABANG-F@EXAMPLE.TEST"}`, cookiesMgr, csrfMgr)
	if status != http.StatusConflict {
		t.Fatalf("email ganda harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "EMAIL_TAKEN")
	if f.hitungAudit(t, "branch", "create", f.adminMgr) != 1 {
		t.Error("permintaan yang ditolak tidak boleh menambah audit")
	}

	// Email milik akun admin juga ditolak (login mencari admin lebih dulu).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches",
		`{"nama":"Milik Admin","nama_kepala":"Kepala","email":"rina@example.test"}`, cookiesMgr, csrfMgr)
	if status != http.StatusConflict {
		t.Fatalf("email admin harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "EMAIL_TAKEN")

	// ---- filter daftar di database nyata ----
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/branches?aktif=true&q=Cabang+F", "", cookiesMgr, "")
	if status != http.StatusOK {
		t.Fatalf("daftar berfilter harus 200, dapat %d (%v)", status, body)
	}
	if got := len(body["data"].([]any)); got != 1 {
		t.Errorf("filter harus menyisakan 1 cabang, dapat %d", got)
	}

	// ---- ubah email cabang menyelaraskan email login akun cabang ----
	// Cabang A sudah punya akun (f.userA) dengan alamat a@example.test.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
		`{"email":"cabang-a-baru@example.test","telepon":"0221"}`, cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("ubah email cabang harus 200, dapat %d (%v)", status, body)
	}
	var emailLogin string
	if err := f.pool.QueryRow(ctx, `SELECT email::text FROM branch_users WHERE id = $1`, f.userA).Scan(&emailLogin); err != nil {
		t.Fatalf("baca email login akun cabang: %v", err)
	}
	if emailLogin != "cabang-a-baru@example.test" {
		t.Fatalf("email login akun cabang harus ikut berubah, dapat %q", emailLogin)
	}
	// Akun bisa login dengan email baru, dan email lama sudah tidak berlaku.
	pgLogin(t, f.srv, "cabang-a-baru@example.test", "rahasia123")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"a@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("email lama harus ditolak, dapat %d", status)
	}
	// Audit update memuat snapshot sebelum dan sesudah.
	var beforeEmail, afterEmail string
	if err := f.pool.QueryRow(ctx,
		`SELECT before->>'email', after->>'email' FROM audit_logs
		  WHERE entity = 'branch' AND action = 'update' AND entity_id = $1
		  ORDER BY at DESC LIMIT 1`, f.branchA).Scan(&beforeEmail, &afterEmail); err != nil {
		t.Fatalf("baca audit update: %v", err)
	}
	if beforeEmail != "cabang-a@example.test" || afterEmail != "cabang-a-baru@example.test" {
		t.Errorf("audit update salah: %q -> %q", beforeEmail, afterEmail)
	}
}

func TestPGCabangNonaktifTidakBisaLoginDanSesiGugur(t *testing.T) {
	f := newPGFixture(t)

	// Sesi cabang B hidup lebih dulu.
	cookiesB, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	status, _, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesB, "")
	if status != http.StatusOK {
		t.Fatalf("prasyarat: sesi cabang B harus hidup, dapat %d", status)
	}

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/branches/"+f.branchB,
		`{"is_active":false}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan cabang harus 200, dapat %d (%v)", status, body)
	}
	if body["data"].(map[string]any)["is_active"] != false {
		t.Errorf("respons harus menunjukkan cabang nonaktif: %v", body["data"])
	}

	// Sesi yang sudah berjalan langsung gugur, dan login barunya ditolak.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesB, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi cabang nonaktif harus 401, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"b@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Errorf("login cabang nonaktif harus 403, dapat %d", status)
	}

	// Diaktifkan lagi: login kembali berhasil.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/branches/"+f.branchB,
		`{"is_active":true}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("aktifkan kembali harus 200, dapat %d", status)
	}
	pgLogin(t, f.srv, "b@example.test", "rahasia123")
}

func TestPGTokenRegistrasiSHA256SekaliPakai(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")

	// Cabang tanpa akun, siap menerima token.
	id := insertBranch(t, ctx, f.pool, "Grahita Cabang G", "Kepala G", "cabang-g@example.test", true)

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches/"+id+"/token",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("generate token harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	token, _ := data["token"].(string)
	prefix, _ := data["prefix"].(string)
	if !strings.HasPrefix(token, "GRH-") || len(token) != len("GRH-XXXX-XXXX-XXXX-XXXX") {
		t.Fatalf("format token tidak sesuai: %q", token)
	}
	if prefix != token[:8] {
		t.Errorf("prefix harus 8 karakter pertama token, dapat %q", prefix)
	}

	// Database hanya memuat SHA-256 token + prefix + masa berlaku 14 hari
	// (nilai `token_hari` bawaan PRD §4.9).
	var hashDB []byte
	var prefixDB string
	var expiresAt time.Time
	var usedAt *time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT token_hash, token_prefix, expires_at, used_at FROM registration_tokens WHERE branch_id = $1`, id).
		Scan(&hashDB, &prefixDB, &expiresAt, &usedAt); err != nil {
		t.Fatalf("baca token registrasi: %v", err)
	}
	if !bytes.Equal(hashDB, auth.HashRegistrationToken(token)) {
		t.Error("token_hash harus SHA-256 dari token (bentuk ternormalisasi)")
	}
	if bytes.Contains(hashDB, []byte(token)) {
		t.Error("nilai token mentah tidak boleh tersimpan")
	}
	if prefixDB != prefix || usedAt != nil {
		t.Errorf("prefix/used_at tidak sesuai: %q %v", prefixDB, usedAt)
	}
	if d := time.Until(expiresAt); d < 13*24*time.Hour || d > 15*24*time.Hour {
		t.Errorf("masa berlaku harus ~14 hari, dapat %v", d)
	}
	// Token tersimpan juga hanya SATU baris aktif untuk cabang itu.
	var jumlah int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM registration_tokens WHERE branch_id = $1 AND used_at IS NULL`, id).Scan(&jumlah); err != nil {
		t.Fatalf("hitung token aktif: %v", err)
	}
	if jumlah != 1 {
		t.Fatalf("harus ada tepat 1 token aktif, dapat %d", jumlah)
	}
	// Daftar cabang menampilkan prefix-nya (bukan token aslinya).
	_, list, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/branches?q=Cabang+G", "", cookiesMgr, "")
	baris := list["data"].([]any)[0].(map[string]any)
	if baris["token_prefix"] != prefix {
		t.Errorf("daftar cabang harus memuat prefix token, dapat %v", baris["token_prefix"])
	}
	if _, ada := baris["token"]; ada {
		t.Error("daftar cabang tidak boleh memuat token asli")
	}

	// Generate ulang: token lama batal (hash-nya tidak ada lagi di database),
	// token baru berlaku, tetap satu baris aktif.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches/"+id+"/token",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("generate ulang harus 200, dapat %d (%v)", status, body)
	}
	tokenBaru := body["data"].(map[string]any)["token"].(string)
	if tokenBaru == token {
		t.Error("generate ulang harus menghasilkan token berbeda")
	}
	var nLama, nBaru, nAktif int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE token_hash = sha256($2::bytea)),
		        count(*) FILTER (WHERE token_hash = sha256($3::bytea)),
		        count(*) FILTER (WHERE used_at IS NULL)
		   FROM registration_tokens WHERE branch_id = $1`,
		id, strings.ReplaceAll(token, "-", ""), strings.ReplaceAll(tokenBaru, "-", "")).Scan(&nLama, &nBaru, &nAktif); err != nil {
		t.Fatalf("periksa token lama/baru: %v", err)
	}
	if nLama != 0 {
		t.Error("token lama harus sudah tidak ada di database")
	}
	if nBaru != 1 || nAktif != 1 {
		t.Errorf("harus ada tepat 1 token aktif (baru), dapat baru=%d aktif=%d", nBaru, nAktif)
	}

	// Dua mutasi token tercatat audit, yang kedua memuat token lama di before.
	if n := f.hitungAudit(t, "branch", "token_generate", f.adminMgr); n != 2 {
		t.Errorf("harus ada 2 audit token_generate, dapat %d", n)
	}
	var beforePrefix string
	if err := f.pool.QueryRow(ctx,
		`SELECT before->>'token_prefix' FROM audit_logs
		  WHERE entity = 'branch' AND action = 'token_generate' AND entity_id = $1 AND before IS NOT NULL`, id).
		Scan(&beforePrefix); err != nil {
		t.Fatalf("baca audit token kedua: %v", err)
	}
	if beforePrefix != prefix {
		t.Errorf("before harus memuat prefix token lama %q, dapat %q", prefix, beforePrefix)
	}

	// Cabang yang sudah punya akun tidak butuh token; cabang nonaktif juga.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches/"+f.branchA+"/token",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusConflict {
		t.Fatalf("cabang berakun harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_HAS_ACCOUNT")

	nonaktif := insertBranch(t, ctx, f.pool, "Grahita Cabang H", "Kepala H", "cabang-h@example.test", false)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches/"+nonaktif+"/token",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusConflict {
		t.Fatalf("cabang nonaktif harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_INACTIVE")
}

func TestPGHapusCabangTerjagaOlehPesanan(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Cabang A punya pesanan (draft minimal).
	if _, err := f.pool.Exec(ctx, `INSERT INTO orders (branch_id, status) VALUES ($1, 'draft')`, f.branchA); err != nil {
		t.Fatalf("seed pesanan: %v", err)
	}
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodDelete, f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
		"", cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("hapus cabang berp Pesanan harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_HAS_ORDERS")
	var masihAda bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM branches WHERE id = $1)`, f.branchA).Scan(&masihAda); err != nil {
		t.Fatalf("periksa cabang: %v", err)
	}
	if !masihAda {
		t.Error("cabang berp Pesanan harus tetap ada (hanya boleh dinonaktifkan)")
	}
	if f.hitungAudit(t, "branch", "delete", f.adminSA) != 0 {
		t.Error("hapus yang ditolak tidak boleh mencatat audit")
	}

	// Cabang B punya akun + sesi, tetapi tidak punya pesanan → boleh dihapus.
	pgLogin(t, f.srv, "b@example.test", "rahasia123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodDelete, f.srv.URL+"/api/v1/admin/branches/"+f.branchB,
		"", cookiesSA, csrfSA)
	if status != http.StatusNoContent {
		t.Fatalf("hapus cabang tanpa pesanan harus 204, dapat %d (%v)", status, body)
	}
	var sisaCabang, sisaAkun, sisaSesi int
	if err := f.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM branches WHERE id = $1),
		        (SELECT count(*) FROM branch_users WHERE branch_id = $1),
		        (SELECT count(*) FROM sessions WHERE branch_id = $1)`, f.branchB).
		Scan(&sisaCabang, &sisaAkun, &sisaSesi); err != nil {
		t.Fatalf("periksa sisa data cabang: %v", err)
	}
	if sisaCabang != 0 || sisaAkun != 0 || sisaSesi != 0 {
		t.Errorf("cabang, akun, dan sesi harus terhapus: %d %d %d", sisaCabang, sisaAkun, sisaSesi)
	}
	// Akunnya benar-benar tidak bisa login lagi.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"b@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("akun cabang terhapus harus 401, dapat %d", status)
	}

	// Penghapusan tercatat audit dengan snapshot sebelum.
	var beforeNama, action string
	if err := f.pool.QueryRow(ctx,
		`SELECT before->>'nama', action FROM audit_logs
		  WHERE entity = 'branch' AND entity_id = $1 AND action = 'delete'`, f.branchB).Scan(&beforeNama, &action); err != nil {
		t.Fatalf("baca audit delete: %v", err)
	}
	if beforeNama != "Grahita Cabang B" || action != "delete" {
		t.Errorf("audit delete tidak sesuai: %q %q", beforeNama, action)
	}
}
