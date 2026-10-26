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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	authpg "github.com/grahita/portal/api/internal/auth/postgres"
	"github.com/grahita/portal/api/internal/branch"
	branchpg "github.com/grahita/portal/api/internal/branch/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/users"
)

type sessionCreateBarrier struct {
	auth.SessionStore
	entered chan struct{}
	proceed <-chan struct{}
}

func (b sessionCreateBarrier) Create(ctx context.Context, s auth.Session, hash []byte, meta auth.RequestMeta) (auth.Session, error) {
	b.entered <- struct{}{}
	if b.proceed != nil {
		select {
		case <-b.proceed:
		case <-ctx.Done():
			return auth.Session{}, ctx.Err()
		}
	}
	return b.SessionStore.Create(ctx, s, hash, meta)
}

type resetCreateBarrier struct {
	auth.ResetStore
	entered chan struct{}
	proceed <-chan struct{}
}

func (b resetCreateBarrier) CreateResetToken(ctx context.Context, token auth.ResetToken, hash []byte) error {
	b.entered <- struct{}{}
	if b.proceed != nil {
		select {
		case <-b.proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.ResetStore.CreateResetToken(ctx, token, hash)
}

type resetPasswordBarrier struct {
	auth.ResetStore
	entered chan struct{}
	proceed <-chan struct{}
}

func (b resetPasswordBarrier) ResetPassword(ctx context.Context, tokenHash []byte, now time.Time, passwordHash string) error {
	b.entered <- struct{}{}
	select {
	case <-b.proceed:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.ResetStore.ResetPassword(ctx, tokenHash, now, passwordHash)
}

type changePasswordBarrier struct {
	auth.UserStore
	entered chan struct{}
	proceed <-chan struct{}
}

func (b changePasswordBarrier) ChangePassword(ctx context.Context, kind auth.Kind, id, sessionID, expected, next string) error {
	b.entered <- struct{}{}
	select {
	case <-b.proceed:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.UserStore.ChangePassword(ctx, kind, id, sessionID, expected, next)
}

func pulihkanIdentitasDenganKredensialBaru(t *testing.T, f *pgFixture, email, passwordHash string) {
	t.Helper()
	ctx := context.Background()
	revoke, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai transaksi revokasi: %v", err)
	}
	defer func() { _ = revoke.Rollback(ctx) }()
	if _, err := revoke.Exec(ctx, `UPDATE branches SET nama_kepala=NULL, email=NULL, identity_status='identity_incomplete' WHERE id=$1`, f.branchA); err != nil {
		t.Fatalf("cabut identitas: %v", err)
	}
	if _, err := revoke.Exec(ctx, `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA); err != nil {
		t.Fatalf("cabut credential: %v", err)
	}
	if _, err := revoke.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, f.userA); err != nil {
		t.Fatalf("cabut sesi: %v", err)
	}
	if _, err := revoke.Exec(ctx, `UPDATE password_reset_tokens SET revoked_at=now() WHERE actor_kind='branch' AND user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, f.userA); err != nil {
		t.Fatalf("cabut token reset: %v", err)
	}
	if err := revoke.Commit(ctx); err != nil {
		t.Fatalf("commit revokasi: %v", err)
	}

	recoverTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai transaksi pemulihan: %v", err)
	}
	defer func() { _ = recoverTx.Rollback(ctx) }()
	if _, err := recoverTx.Exec(ctx, `UPDATE branches SET nama_kepala='Kepala Baru', email=$2, identity_status='registered' WHERE id=$1`, f.branchA, email); err != nil {
		t.Fatalf("pulihkan identitas: %v", err)
	}
	if _, err := recoverTx.Exec(ctx, `UPDATE branch_users SET email=$2, is_active=true, password_hash=$3 WHERE id=$1`, f.userA, email, passwordHash); err != nil {
		t.Fatalf("pasang credential baru: %v", err)
	}
	if err := recoverTx.Commit(ctx); err != nil {
		t.Fatalf("commit pemulihan: %v", err)
	}
}

type hasilPutRevokasi struct {
	status int
	err    error
}

type branchUpdateBarrier struct {
	branch.Repository
	entered chan<- struct{}
	proceed <-chan struct{}
}

func (b branchUpdateBarrier) Update(ctx context.Context, id string, p branch.Patch, actor auth.Actor) (branch.Branch, error) {
	select {
	case b.entered <- struct{}{}:
	case <-ctx.Done():
		return branch.Branch{}, ctx.Err()
	}
	select {
	case <-b.proceed:
	case <-ctx.Done():
		return branch.Branch{}, ctx.Err()
	}
	return b.Repository.Update(ctx, id, p, actor)
}

func serverDenganBarrierUpdateCabang(t *testing.T, f *pgFixture, entered chan<- struct{}, proceed <-chan struct{}) *httptest.Server {
	t.Helper()
	deps := f.deps
	deps.Branches = branch.NewService(branchUpdateBarrier{
		Repository: branchpg.New(f.pool), entered: entered, proceed: proceed,
	}, clock.System())
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return srv
}

func mulaiPutCabang(t *testing.T, baseURL, branchID, body string, cookies []*http.Cookie, csrf string) <-chan hasilPutRevokasi {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/admin/branches/"+branchID, strings.NewReader(body))
	if err != nil {
		t.Fatalf("buat PUT cabang: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(httpx.CSRFHeader, csrf)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	hasil := make(chan hasilPutRevokasi, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			hasil <- hasilPutRevokasi{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		hasil <- hasilPutRevokasi{status: resp.StatusCode}
	}()
	return hasil
}

func mulaiPutRevokasiIdentitas(t *testing.T, f *pgFixture, cookies []*http.Cookie, csrf string) <-chan hasilPutRevokasi {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
		strings.NewReader(`{"nama_kepala":"","email":""}`))
	if err != nil {
		t.Fatalf("buat PUT revokasi identitas: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(httpx.CSRFHeader, csrf)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	hasil := make(chan hasilPutRevokasi, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			hasil <- hasilPutRevokasi{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		hasil <- hasilPutRevokasi{status: resp.StatusCode}
	}()
	return hasil
}

func pulihkanIdentitasSetelahRevokasi(t *testing.T, f *pgFixture, email, passwordHash string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai transaksi pemulihan: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE branches SET nama_kepala='Kepala Baru', email=$2, identity_status='registered' WHERE id=$1`, f.branchA, email); err != nil {
		t.Fatalf("pulihkan identitas: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE branch_users SET email=$2, is_active=true, password_hash=$3 WHERE id=$1`, f.userA, email, passwordHash); err != nil {
		t.Fatalf("pasang credential baru: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit pemulihan: %v", err)
	}
}

func tungguUpdatePasswordTertahan(t *testing.T, f *pgFixture) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_stat_activity
 WHERE pid <> pg_backend_pid() AND wait_event_type='Lock'
   AND query LIKE '%UPDATE branch_users%password_hash%')`).Scan(&blocked); err != nil {
			t.Fatalf("periksa barrier update credential: %v", err)
		}
		if blocked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("mutasi credential tidak mencapai UPDATE setelah memegang lock identitas")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func tungguPutRevokasiTertahan(t *testing.T, f *pgFixture) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_stat_activity
 WHERE pid <> pg_backend_pid() AND wait_event_type='Lock'
   AND (query LIKE '%SELECT id, nama, nama_kepala%FROM branches WHERE id = $1 FOR UPDATE%'
     OR query LIKE '%pg_advisory_xact_lock(22102, 2)%'))`).Scan(&blocked); err != nil {
			t.Fatalf("periksa PUT revokasi tertahan: %v", err)
		}
		if blocked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("PUT revokasi tidak tertahan pada lock serialisasi cabang")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ujiMutasiMenangLaluRevokasi(t *testing.T, mode, emailPemulihan string) {
	t.Helper()
	f := newPGFixture(t)
	ctx := context.Background()
	repo := authpg.New(f.pool)
	svc := auth.NewService(repo, repo, repo, clock.System(), 8*time.Hour, 30*24*time.Hour)
	svc.SetResetMailer(f.mail)
	cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	issued, err := svc.Login(ctx, "a@example.test", "rahasia123", auth.RequestMeta{})
	if err != nil {
		t.Fatalf("buat sesi pra-mutasi: %v", err)
	}
	if err := svc.ForgotPassword(ctx, "a@example.test", time.Hour, auth.RequestMeta{}); err != nil {
		t.Fatalf("terbitkan token pra-mutasi: %v", err)
	}
	token := f.mail.tokenTerakhir(t)

	// SHARE kompatibel dengan SELECT FOR UPDATE (ROW SHARE), tetapi menahan
	// UPDATE branch_users (ROW EXCLUSIVE). Pada reset, wait_event ini hanya
	// mungkin dicapai setelah UPDATE used_at. Pada ganti sandi, ini dicapai
	// setelah hash lama dan sesi peminta sudah dikunci serta diverifikasi.
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai blocker mutasi: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx, `LOCK TABLE branch_users IN SHARE MODE`); err != nil {
		t.Fatalf("kunci tabel akun cabang: %v", err)
	}
	mutationDone := make(chan error, 1)
	go func() {
		if mode == "reset" {
			mutationDone <- svc.ResetPassword(context.Background(), token, "pilihanlama123", auth.RequestMeta{})
			return
		}
		mutationDone <- svc.ChangePassword(context.Background(), issued.Actor, "rahasia123", "pilihanlama123", auth.RequestMeta{})
	}()
	tungguUpdatePasswordTertahan(t, f)

	revoked := mulaiPutRevokasiIdentitas(t, f, cookiesAdmin, csrfAdmin)
	tungguPutRevokasiTertahan(t, f)
	select {
	case hasil := <-revoked:
		t.Fatalf("PUT revokasi harus menunggu transaksi %s, selesai dini: status=%d err=%v", mode, hasil.status, hasil.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("lepas blocker %s: %v", mode, err)
	}
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatalf("mutasi %s yang menang lock harus commit: %v", mode, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("mutasi %s tidak selesai setelah blocker dilepas", mode)
	}
	select {
	case hasil := <-revoked:
		if hasil.err != nil || hasil.status != http.StatusOK {
			t.Fatalf("PUT revokasi setelah %s gagal: status=%d err=%v", mode, hasil.status, hasil.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("revokasi tidak selesai setelah %s commit", mode)
	}

	var hashAkhir string
	var tokenTerpakai, tokenDicabut bool
	var sesiAktif, tokenAktif int
	if err := f.pool.QueryRow(ctx, `SELECT password_hash FROM branch_users WHERE id=$1`, f.userA).Scan(&hashAkhir); err != nil {
		t.Fatalf("baca hash pascarevokasi: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT used_at IS NOT NULL, revoked_at IS NOT NULL
 FROM password_reset_tokens WHERE token_hash=sha256($1::bytea)`, token).Scan(&tokenTerpakai, &tokenDicabut); err != nil {
		t.Fatalf("baca status token pascarevokasi: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, f.userA).Scan(&sesiAktif); err != nil {
		t.Fatalf("hitung sesi aktif: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, f.userA).Scan(&tokenAktif); err != nil {
		t.Fatalf("hitung token aktif: %v", err)
	}
	statusTokenBenar := mode == "reset" && tokenTerpakai && !tokenDicabut || mode == "change" && !tokenTerpakai && tokenDicabut
	if hashAkhir != "!identity-revoked!" || !statusTokenBenar || sesiAktif != 0 || tokenAktif != 0 {
		t.Fatalf("revokasi akhir %s tidak lengkap: hash=%q used=%v revoked=%v sesi=%d token=%d",
			mode, hashAkhir, tokenTerpakai, tokenDicabut, sesiAktif, tokenAktif)
	}
	if _, err := svc.Authenticate(ctx, issued.Token); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("sesi pra-mutasi harus dicabut, dapat %v", err)
	}

	hashPemulihan, err := auth.HashPassword("credentialbaru123")
	if err != nil {
		t.Fatalf("hash credential pemulihan: %v", err)
	}
	pulihkanIdentitasSetelahRevokasi(t, f, emailPemulihan, hashPemulihan)
	if _, err := svc.Login(ctx, emailPemulihan, "credentialbaru123", auth.RequestMeta{}); err != nil {
		t.Fatalf("credential pemulihan harus berlaku: %v", err)
	}
	if _, err := svc.Login(ctx, emailPemulihan, "pilihanlama123", auth.RequestMeta{}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("credential %s sebelum revokasi harus mati, dapat %v", mode, err)
	}
}

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
		`{"nama":"Grahita Cabang F","nama_kepala":"Kepala F","email":"Cabang-F@Example.Test","telepon":"0812","is_cnc":true,"pajak_in":true}`,
		cookiesMgr, csrfMgr)
	if status != http.StatusCreated {
		t.Fatalf("buat cabang harus 201, dapat %d (%v)", status, body)
	}
	dibuat := body["data"].(map[string]any)
	id, _ := dibuat["id"].(string)
	if dibuat["email"] != "cabang-f@example.test" {
		t.Errorf("email tersimpan harus huruf kecil (citext), dapat %v", dibuat["email"])
	}

	// Baris benar-benar ada di database, flag PI tersimpan sebagai
	// atribut (tanpa efek harga sesuai keputusan user).
	var nama, email string
	var cnc, pi, aktif bool
	if err := f.pool.QueryRow(ctx,
		`SELECT nama, email::text, is_cnc, pajak_in, is_active FROM branches WHERE id = $1`, id).
		Scan(&nama, &email, &cnc, &pi, &aktif); err != nil {
		t.Fatalf("baca cabang baru: %v", err)
	}
	if nama != "Grahita Cabang F" || email != "cabang-f@example.test" || !cnc || !pi || !aktif {
		t.Errorf("baris cabang tidak sesuai: %s %s %v %v %v", nama, email, cnc, pi, aktif)
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

func TestPGIdentitasIncompleteMencabutSeluruhAksesLama(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesCabang, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO registration_tokens (branch_id, token_hash, token_prefix, expires_at)
		VALUES ($1, decode('a1','hex'), 'TOKEN-A1', now() + interval '1 day')`, f.branchA); err != nil {
		t.Fatalf("seed token registrasi: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO password_reset_tokens (actor_kind, user_id, token_hash, expires_at)
		VALUES ('branch', $1, decode('a2','hex'), now() + interval '1 day')`, f.userA); err != nil {
		t.Fatalf("seed token reset: %v", err)
	}

	cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
		`{"nama_kepala":"","email":""}`, cookiesAdmin, csrfAdmin)
	if status != http.StatusOK {
		t.Fatalf("kosongkan identitas harus 200, dapat %d (%v)", status, body)
	}
	if got := body["data"].(map[string]any)["identity_status"]; got != "identity_incomplete" {
		t.Fatalf("status identitas harus incomplete, dapat %v", got)
	}

	var userAktif, tokenRegistrasiDicabut, tokenResetDicabut, sesiDicabut bool
	if err := f.pool.QueryRow(ctx, `SELECT is_active FROM branch_users WHERE id=$1`, f.userA).Scan(&userAktif); err != nil {
		t.Fatalf("baca status akun: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM registration_tokens WHERE branch_id=$1`, f.branchA).Scan(&tokenRegistrasiDicabut); err != nil {
		t.Fatalf("baca token registrasi: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM password_reset_tokens WHERE user_id=$1`, f.userA).Scan(&tokenResetDicabut); err != nil {
		t.Fatalf("baca token reset: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT bool_and(revoked_at IS NOT NULL) FROM sessions WHERE user_id=$1`, f.userA).Scan(&sesiDicabut); err != nil {
		t.Fatalf("baca sesi: %v", err)
	}
	if userAktif || !tokenRegistrasiDicabut || !tokenResetDicabut || !sesiDicabut {
		t.Fatalf("akses lama belum seluruhnya dicabut: user=%v registration=%v reset=%v session=%v",
			userAktif, tokenRegistrasiDicabut, tokenResetDicabut, sesiDicabut)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesCabang, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("sesi lama harus ditolak 401, dapat %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userA,
		`{"is_active":true}`, cookiesAdmin, csrfAdmin)
	if status != http.StatusConflict {
		t.Fatalf("aktivasi identitas incomplete harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userA+"/reset",
		"", cookiesAdmin, csrfAdmin)
	if status != http.StatusConflict {
		t.Fatalf("reset identitas incomplete harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")

	var hashSetelahRevokasi string
	if err := f.pool.QueryRow(ctx, `SELECT password_hash FROM branch_users WHERE id=$1`, f.userA).Scan(&hashSetelahRevokasi); err != nil {
		t.Fatalf("baca hash setelah revokasi: %v", err)
	}
	if hashSetelahRevokasi != "!identity-revoked!" {
		t.Fatalf("credential lama harus dihancurkan, dapat marker %q", hashSetelahRevokasi)
	}

	// PUT atribut identitas biasa bukan workflow pemulihan terverifikasi. Nilai
	// boleh disimpan sebagai atribut dorman, tetapi status dan akses tetap
	// fail-closed sampai workflow terpisah memasang credential baru.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
		`{"nama_kepala":"Kepala A","email":"a@example.test"}`, cookiesAdmin, csrfAdmin)
	if status != http.StatusOK {
		t.Fatalf("ubah atribut identitas dorman harus 200, dapat %d (%v)", status, body)
	}
	if got := body["data"].(map[string]any)["identity_status"]; got != branch.IdentityIncomplete {
		t.Fatalf("PUT atribut tidak boleh memulihkan status, dapat %v", got)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userA,
		`{"is_active":true}`, cookiesAdmin, csrfAdmin)
	if status != http.StatusConflict {
		t.Fatalf("aktivasi tanpa pemulihan terverifikasi harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"a@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("credential lama harus tetap ditolak, dapat %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userA+"/reset", "", cookiesAdmin, csrfAdmin)
	if status != http.StatusConflict {
		t.Fatalf("reset tanpa pemulihan terverifikasi harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")

	// Fixture workflow terpisah mensimulasikan hasil verifikasi identitas dan
	// pemasangan credential baru dalam satu transaksi. Jalur sah ini tetap
	// bekerja tanpa menghidupkan hash/token/sesi lama.
	hashBaru, err := auth.HashPassword("credentialbaru123")
	if err != nil {
		t.Fatalf("hash credential pemulihan: %v", err)
	}
	pulihkanIdentitasDenganKredensialBaru(t, f, "a@example.test", hashBaru)
	pgLogin(t, f.srv, "a@example.test", "credentialbaru123")
}

func TestPGPutNonIdentitasMempertahankanStatusIncompleteDenganAtributLengkap(t *testing.T) {
	t.Run("dengan akun nonaktif", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		if _, err := f.pool.Exec(ctx, `UPDATE branches SET identity_status='identity_incomplete' WHERE id=$1`, f.branchA); err != nil {
			t.Fatalf("siapkan identitas incomplete dengan akun nonaktif: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA); err != nil {
			t.Fatalf("cabut credential fixture incomplete: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, f.userA); err != nil {
			t.Fatalf("cabut sesi fixture incomplete: %v", err)
		}

		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
			`{"pajak_in":true}`, cookiesAdmin, csrfAdmin)
		if status != http.StatusOK {
			t.Fatalf("PUT non-identitas harus 200, dapat %d (%v)", status, body)
		}
		data := body["data"].(map[string]any)
		if data["identity_status"] != branch.IdentityIncomplete || data["email"] != "cabang-a@example.test" || data["nama_kepala"] != "Kepala A" {
			t.Fatalf("PUT non-identitas memulihkan status tanpa workflow eksplisit: %v", data)
		}

		var dbStatus, beforeStatus, afterStatus, beforeEmail, afterEmail string
		var active bool
		if err := f.pool.QueryRow(ctx, `SELECT b.identity_status::text, u.is_active
 FROM branches b JOIN branch_users u ON u.branch_id=b.id WHERE b.id=$1`, f.branchA).
			Scan(&dbStatus, &active); err != nil {
			t.Fatalf("baca state akun incomplete: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', after->>'identity_status',
 before->>'email', after->>'email' FROM audit_logs
 WHERE entity='branch' AND action='update' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, f.branchA).
			Scan(&beforeStatus, &afterStatus, &beforeEmail, &afterEmail); err != nil {
			t.Fatalf("baca audit PUT non-identitas: %v", err)
		}
		if dbStatus != branch.IdentityIncomplete || active || beforeStatus != branch.IdentityIncomplete ||
			afterStatus != branch.IdentityIncomplete || beforeEmail != "cabang-a@example.test" || afterEmail != "cabang-a@example.test" {
			t.Fatalf("state/audit incomplete tidak dipertahankan: db=%s active=%v audit=%s/%q->%s/%q",
				dbStatus, active, beforeStatus, beforeEmail, afterStatus, afterEmail)
		}
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
			`{"email":"a@example.test","password":"rahasia123"}`, nil, "")
		if status != http.StatusUnauthorized {
			t.Fatalf("akun incomplete tidak boleh login, dapat %d", status)
		}
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/users/branch/"+f.userA+"/reset", "", cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("reset akun incomplete harus 409, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
	})

	t.Run("tanpa akun", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		branchID := insertBranch(t, ctx, f.pool, "Cabang Incomplete", "Kepala Incomplete", "incomplete@example.test", true)
		if _, err := f.pool.Exec(ctx, `UPDATE branches SET identity_status='identity_incomplete' WHERE id=$1`, branchID); err != nil {
			t.Fatalf("siapkan identitas incomplete tanpa akun: %v", err)
		}
		cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/admin/branches/"+branchID,
			`{"pajak_in":true}`, cookiesAdmin, csrfAdmin)
		if status != http.StatusOK {
			t.Fatalf("PUT non-identitas harus 200, dapat %d (%v)", status, body)
		}
		data := body["data"].(map[string]any)
		if data["identity_status"] != branch.IdentityIncomplete || data["email"] != "incomplete@example.test" {
			t.Fatalf("PUT non-identitas menaikkan incomplete menjadi ready: %v", data)
		}
		var accounts, activeTokens int
		var beforeStatus, afterStatus string
		if err := f.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM branch_users WHERE branch_id=$1),
 (SELECT count(*) FROM registration_tokens WHERE branch_id=$1 AND used_at IS NULL AND revoked_at IS NULL)`, branchID).
			Scan(&accounts, &activeTokens); err != nil {
			t.Fatalf("baca artefak cabang tanpa akun: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', after->>'identity_status'
 FROM audit_logs WHERE entity='branch' AND action='update' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, branchID).
			Scan(&beforeStatus, &afterStatus); err != nil {
			t.Fatalf("baca audit cabang tanpa akun: %v", err)
		}
		if accounts != 0 || activeTokens != 0 || beforeStatus != branch.IdentityIncomplete || afterStatus != branch.IdentityIncomplete {
			t.Fatalf("cabang tanpa akun tidak fail-closed: accounts=%d tokens=%d audit=%s->%s",
				accounts, activeTokens, beforeStatus, afterStatus)
		}
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/branches/"+branchID+"/token", "", cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("token registrasi incomplete harus 409, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
	})
}

func TestPGPutIdentitasBiasaTidakMemulihkanStatusIncomplete(t *testing.T) {
	t.Run("email sama dengan akun nonaktif", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		cookiesCabang, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
		cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		if _, err := f.pool.Exec(ctx, `UPDATE branches SET identity_status='identity_incomplete' WHERE id=$1`, f.branchA); err != nil {
			t.Fatalf("siapkan identitas incomplete: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA); err != nil {
			t.Fatalf("cabut credential: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, f.userA); err != nil {
			t.Fatalf("cabut sesi: %v", err)
		}

		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
			`{"email":" CABANG-A@EXAMPLE.TEST "}`, cookiesAdmin, csrfAdmin)
		if status != http.StatusOK {
			t.Fatalf("PUT email sama harus 200, dapat %d (%v)", status, body)
		}
		data := body["data"].(map[string]any)
		if data["identity_status"] != branch.IdentityIncomplete || data["email"] != "cabang-a@example.test" {
			t.Fatalf("email no-op memulihkan identitas: %v", data)
		}

		var dbStatus, beforeStatus, afterStatus, beforeEmail, afterEmail string
		var activeRegistration, activeReset, activeSessions int
		if err := f.pool.QueryRow(ctx, `SELECT identity_status::text FROM branches WHERE id=$1`, f.branchA).Scan(&dbStatus); err != nil {
			t.Fatalf("baca status akhir: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', after->>'identity_status',
 before->>'email', after->>'email' FROM audit_logs
 WHERE entity='branch' AND action='update' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, f.branchA).
			Scan(&beforeStatus, &afterStatus, &beforeEmail, &afterEmail); err != nil {
			t.Fatalf("baca audit email no-op: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM registration_tokens WHERE branch_id=$1 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM password_reset_tokens WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM sessions WHERE user_id=$2 AND revoked_at IS NULL)`, f.branchA, f.userA).
			Scan(&activeRegistration, &activeReset, &activeSessions); err != nil {
			t.Fatalf("hitung artefak aktif: %v", err)
		}
		if dbStatus != branch.IdentityIncomplete || beforeStatus != branch.IdentityIncomplete || afterStatus != branch.IdentityIncomplete ||
			beforeEmail != "cabang-a@example.test" || afterEmail != "cabang-a@example.test" ||
			activeRegistration != 0 || activeReset != 0 || activeSessions != 0 {
			t.Fatalf("email no-op tidak fail-closed: db=%s audit=%s/%q->%s/%q artefak=%d/%d/%d",
				dbStatus, beforeStatus, beforeEmail, afterStatus, afterEmail,
				activeRegistration, activeReset, activeSessions)
		}
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesCabang, "")
		if status != http.StatusUnauthorized {
			t.Fatalf("sesi lama harus tetap dicabut, dapat %d", status)
		}
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/users/branch/"+f.userA+"/reset", "", cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("reset baru harus ditolak, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
	})

	t.Run("nama kepala sama tanpa akun", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		branchID := insertBranch(t, ctx, f.pool, "Cabang No-op", "Kepala No-op", "noop@example.test", true)
		if _, err := f.pool.Exec(ctx, `UPDATE branches SET identity_status='identity_incomplete' WHERE id=$1`, branchID); err != nil {
			t.Fatalf("siapkan identitas incomplete tanpa akun: %v", err)
		}
		cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/admin/branches/"+branchID,
			`{"nama_kepala":" Kepala No-op "}`, cookiesAdmin, csrfAdmin)
		if status != http.StatusOK {
			t.Fatalf("PUT nama sama harus 200, dapat %d (%v)", status, body)
		}
		data := body["data"].(map[string]any)
		if data["identity_status"] != branch.IdentityIncomplete || data["nama_kepala"] != "Kepala No-op" {
			t.Fatalf("nama no-op memulihkan identitas: %v", data)
		}
		var accounts, activeTokens int
		var beforeStatus, afterStatus, beforeName, afterName string
		if err := f.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM branch_users WHERE branch_id=$1),
 (SELECT count(*) FROM registration_tokens WHERE branch_id=$1 AND used_at IS NULL AND revoked_at IS NULL)`, branchID).
			Scan(&accounts, &activeTokens); err != nil {
			t.Fatalf("baca artefak cabang tanpa akun: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', after->>'identity_status',
 before->>'nama_kepala', after->>'nama_kepala' FROM audit_logs
 WHERE entity='branch' AND action='update' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, branchID).
			Scan(&beforeStatus, &afterStatus, &beforeName, &afterName); err != nil {
			t.Fatalf("baca audit nama no-op: %v", err)
		}
		if accounts != 0 || activeTokens != 0 || beforeStatus != branch.IdentityIncomplete || afterStatus != branch.IdentityIncomplete ||
			beforeName != "Kepala No-op" || afterName != "Kepala No-op" {
			t.Fatalf("nama no-op tanpa akun tidak fail-closed: accounts=%d tokens=%d audit=%s/%q->%s/%q",
				accounts, activeTokens, beforeStatus, beforeName, afterStatus, afterName)
		}
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/branches/"+branchID+"/token", "", cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("token baru harus ditolak, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
	})

	t.Run("nilai berbeda dengan akun revoked", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		cookiesCabang, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
		cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		if _, err := f.pool.Exec(ctx, `UPDATE branches SET identity_status='identity_incomplete' WHERE id=$1`, f.branchA); err != nil {
			t.Fatalf("siapkan identitas incomplete: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA); err != nil {
			t.Fatalf("cabut credential: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, f.userA); err != nil {
			t.Fatalf("cabut sesi: %v", err)
		}

		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
			`{"nama_kepala":"Kepala Dormant Baru","email":"dorman-baru@example.test"}`,
			cookiesAdmin, csrfAdmin)
		if status != http.StatusOK {
			t.Fatalf("PUT identitas berbeda harus 200, dapat %d (%v)", status, body)
		}
		data := body["data"].(map[string]any)
		if data["identity_status"] != branch.IdentityIncomplete ||
			data["nama_kepala"] != "Kepala Dormant Baru" || data["email"] != "dorman-baru@example.test" {
			t.Fatalf("atribut dorman harus berubah tanpa memulihkan status: %v", data)
		}

		var dbStatus, passwordHash, beforeStatus, afterStatus, beforeName, afterName, beforeEmail, afterEmail string
		var active bool
		var activeRegistration, activeReset, activeSessions int
		if err := f.pool.QueryRow(ctx, `SELECT b.identity_status::text, u.is_active, u.password_hash
 FROM branches b JOIN branch_users u ON u.branch_id=b.id WHERE b.id=$1`, f.branchA).
			Scan(&dbStatus, &active, &passwordHash); err != nil {
			t.Fatalf("baca state akhir: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', after->>'identity_status',
 before->>'nama_kepala', after->>'nama_kepala', before->>'email', after->>'email'
 FROM audit_logs WHERE entity='branch' AND action='update' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, f.branchA).
			Scan(&beforeStatus, &afterStatus, &beforeName, &afterName, &beforeEmail, &afterEmail); err != nil {
			t.Fatalf("baca audit PUT identitas berbeda: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM registration_tokens WHERE branch_id=$1 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM password_reset_tokens WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM sessions WHERE user_id=$2 AND revoked_at IS NULL)`, f.branchA, f.userA).
			Scan(&activeRegistration, &activeReset, &activeSessions); err != nil {
			t.Fatalf("hitung artefak aktif: %v", err)
		}
		if dbStatus != branch.IdentityIncomplete || active || passwordHash != "!identity-revoked!" ||
			beforeStatus != branch.IdentityIncomplete || afterStatus != branch.IdentityIncomplete ||
			beforeName != "Kepala A" || afterName != "Kepala Dormant Baru" ||
			beforeEmail != "cabang-a@example.test" || afterEmail != "dorman-baru@example.test" ||
			activeRegistration != 0 || activeReset != 0 || activeSessions != 0 {
			t.Fatalf("PUT identitas berbeda tidak fail-closed: status=%s active=%v hash=%q audit=%s/%q/%q->%s/%q/%q artefak=%d/%d/%d",
				dbStatus, active, passwordHash, beforeStatus, beforeName, beforeEmail,
				afterStatus, afterName, afterEmail, activeRegistration, activeReset, activeSessions)
		}
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesCabang, "")
		if status != http.StatusUnauthorized {
			t.Fatalf("sesi lama harus tetap dicabut, dapat %d", status)
		}
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/admin/users/branch/"+f.userA,
			`{"is_active":true}`, cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("aktivasi akun revoked harus 409, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/users/branch/"+f.userA+"/reset", "", cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("reset baru harus ditolak, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")
	})
}

func TestPGPutParsialMemakaiStateTerkiniSetelahRevokasiIdentitas(t *testing.T) {
	t.Run("PUT parsial tertahan lalu revokasi commit", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		cookiesCabang, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
		if _, err := f.pool.Exec(ctx, `INSERT INTO registration_tokens
 (branch_id, token_hash, token_prefix, expires_at)
 VALUES ($1, decode('b1','hex'), 'TOKEN-B1', now()+interval '1 day')`, f.branchA); err != nil {
			t.Fatalf("seed token registrasi: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO password_reset_tokens
 (actor_kind, user_id, token_hash, expires_at)
 VALUES ('branch',$1,decode('b2','hex'),now()+interval '1 day')`, f.userA); err != nil {
			t.Fatalf("seed token reset: %v", err)
		}

		cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		entered := make(chan struct{}, 1)
		proceed := make(chan struct{})
		barrierServer := serverDenganBarrierUpdateCabang(t, f, entered, proceed)
		parsial := mulaiPutCabang(t, barrierServer.URL, f.branchA, `{"pajak_in":true}`, cookiesAdmin, csrfAdmin)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("PUT parsial tidak mencapai barrier sebelum pembacaan transaksional")
		}

		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
			`{"nama_kepala":"","email":""}`, cookiesAdmin, csrfAdmin)
		if status != http.StatusOK {
			t.Fatalf("revokasi identitas harus 200, dapat %d (%v)", status, body)
		}
		close(proceed)
		select {
		case hasil := <-parsial:
			if hasil.err != nil || hasil.status != http.StatusOK {
				t.Fatalf("PUT parsial setelah revokasi gagal: status=%d err=%v", hasil.status, hasil.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PUT parsial tidak selesai setelah barrier dilepas")
		}

		var namaKepala, email *string
		var identityStatus, hash string
		var pajakFlag, userAktif bool
		var tokenRegistrasiAktif, tokenResetAktif, sesiAktif int
		if err := f.pool.QueryRow(ctx, `SELECT nama_kepala, email::text, identity_status::text, pajak_in
 FROM branches WHERE id=$1`, f.branchA).Scan(&namaKepala, &email, &identityStatus, &pajakFlag); err != nil {
			t.Fatalf("baca state cabang akhir: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT password_hash, is_active FROM branch_users WHERE id=$1`, f.userA).Scan(&hash, &userAktif); err != nil {
			t.Fatalf("baca state akun akhir: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM registration_tokens WHERE branch_id=$1 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM password_reset_tokens WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM sessions WHERE user_id=$2 AND revoked_at IS NULL)`, f.branchA, f.userA).
			Scan(&tokenRegistrasiAktif, &tokenResetAktif, &sesiAktif); err != nil {
			t.Fatalf("hitung artefak auth aktif: %v", err)
		}
		if namaKepala != nil || email != nil || identityStatus != branch.IdentityIncomplete || !pajakFlag ||
			userAktif || hash != "!identity-revoked!" || tokenRegistrasiAktif != 0 || tokenResetAktif != 0 || sesiAktif != 0 {
			t.Fatalf("PUT parsial menghidupkan kembali state pra-revokasi: kepala=%v email=%v status=%s pajakFlag=%v user=%v hash=%q reg=%d reset=%d sesi=%d",
				namaKepala, email, identityStatus, pajakFlag, userAktif, hash, tokenRegistrasiAktif, tokenResetAktif, sesiAktif)
		}
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesCabang, "")
		if status != http.StatusUnauthorized {
			t.Fatalf("sesi pra-revokasi harus tetap mati, dapat %d", status)
		}
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/users/branch/"+f.userA+"/reset", "", cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("reset baru pada identitas incomplete harus 409, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")

		var auditBeforeStatus, auditBeforeEmail, auditAfterStatus string
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', COALESCE(before->>'email',''), after->>'identity_status'
 FROM audit_logs WHERE entity='branch' AND action='update' AND entity_id=$1
   AND after->>'pajak_in'='true' ORDER BY id DESC LIMIT 1`, f.branchA).
			Scan(&auditBeforeStatus, &auditBeforeEmail, &auditAfterStatus); err != nil {
			t.Fatalf("baca audit PUT parsial: %v", err)
		}
		if auditBeforeStatus != branch.IdentityIncomplete || auditBeforeEmail != "" || auditAfterStatus != branch.IdentityIncomplete {
			t.Fatalf("audit parsial harus memakai snapshot pascarevokasi: before=%s/%q after=%s",
				auditBeforeStatus, auditBeforeEmail, auditAfterStatus)
		}
		var revokeBeforeStatus, revokeBeforeEmail, revokeAfterStatus, revokeAfterEmail string
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', COALESCE(before->>'email',''),
 after->>'identity_status', COALESCE(after->>'email','') FROM audit_logs
 WHERE entity='branch' AND action='update' AND entity_id=$1
 ORDER BY id DESC LIMIT 1 OFFSET 1`, f.branchA).
			Scan(&revokeBeforeStatus, &revokeBeforeEmail, &revokeAfterStatus, &revokeAfterEmail); err != nil {
			t.Fatalf("baca audit revokasi sebelum PUT parsial: %v", err)
		}
		if revokeBeforeStatus != branch.IdentityRegistered || revokeBeforeEmail != "cabang-a@example.test" ||
			revokeAfterStatus != branch.IdentityIncomplete || revokeAfterEmail != "" {
			t.Fatalf("audit revokasi harus mencerminkan commit aktual: before=%s/%q after=%s/%q",
				revokeBeforeStatus, revokeBeforeEmail, revokeAfterStatus, revokeAfterEmail)
		}
	})

	t.Run("PUT parsial commit lalu revokasi", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		cookiesCabang, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
		if _, err := f.pool.Exec(ctx, `INSERT INTO registration_tokens
 (branch_id, token_hash, token_prefix, expires_at)
 VALUES ($1, decode('c1','hex'), 'TOKEN-C1', now()+interval '1 day')`, f.branchA); err != nil {
			t.Fatalf("seed token registrasi: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO password_reset_tokens
 (actor_kind, user_id, token_hash, expires_at)
 VALUES ('branch',$1,decode('c2','hex'),now()+interval '1 day')`, f.userA); err != nil {
			t.Fatalf("seed token reset: %v", err)
		}
		cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

		// Tahan INSERT audit. Pada titik tunggu ini PUT parsial sudah memegang
		// row lock cabang dari jalur repository produksi, sehingga PUT revokasi
		// yang dimulai sesudahnya wajib menunggu lock yang sama.
		blocker, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("mulai barrier audit: %v", err)
		}
		defer func() { _ = blocker.Rollback(ctx) }()
		if _, err := blocker.Exec(ctx, `LOCK TABLE audit_logs IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatalf("kunci tabel audit: %v", err)
		}
		parsial := mulaiPutCabang(t, f.srv.URL, f.branchA, `{"pajak_in":true}`, cookiesAdmin, csrfAdmin)
		deadline := time.Now().Add(5 * time.Second)
		for {
			var blocked bool
			if err := f.pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock'
   AND query LIKE '%INSERT INTO audit_logs%')`).Scan(&blocked); err != nil {
				t.Fatalf("periksa PUT parsial pada barrier audit: %v", err)
			}
			if blocked {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("PUT parsial tidak mencapai INSERT audit sambil memegang lock cabang")
			}
			time.Sleep(10 * time.Millisecond)
		}

		revoked := mulaiPutRevokasiIdentitas(t, f, cookiesAdmin, csrfAdmin)
		tungguPutRevokasiTertahan(t, f)
		select {
		case hasil := <-revoked:
			t.Fatalf("revokasi harus menunggu PUT parsial, selesai dini: status=%d err=%v", hasil.status, hasil.err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatalf("lepas barrier audit: %v", err)
		}
		select {
		case hasil := <-parsial:
			if hasil.err != nil || hasil.status != http.StatusOK {
				t.Fatalf("PUT parsial yang menang lock gagal: status=%d err=%v", hasil.status, hasil.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PUT parsial tidak selesai setelah barrier dilepas")
		}
		select {
		case hasil := <-revoked:
			if hasil.err != nil || hasil.status != http.StatusOK {
				t.Fatalf("revokasi setelah PUT parsial gagal: status=%d err=%v", hasil.status, hasil.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("revokasi tidak selesai setelah PUT parsial commit")
		}

		var namaKepala, email *string
		var identityStatus, hash string
		var pajakFlag, userAktif bool
		var tokenRegistrasiAktif, tokenResetAktif, sesiAktif int
		if err := f.pool.QueryRow(ctx, `SELECT b.nama_kepala, b.email::text, b.identity_status::text,
 b.pajak_in, u.password_hash, u.is_active FROM branches b JOIN branch_users u ON u.branch_id=b.id WHERE b.id=$1`, f.branchA).
			Scan(&namaKepala, &email, &identityStatus, &pajakFlag, &hash, &userAktif); err != nil {
			t.Fatalf("baca state akhir urutan kedua: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM registration_tokens WHERE branch_id=$1 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM password_reset_tokens WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL),
 (SELECT count(*) FROM sessions WHERE user_id=$2 AND revoked_at IS NULL)`, f.branchA, f.userA).
			Scan(&tokenRegistrasiAktif, &tokenResetAktif, &sesiAktif); err != nil {
			t.Fatalf("hitung artefak auth akhir urutan kedua: %v", err)
		}
		if namaKepala != nil || email != nil || identityStatus != branch.IdentityIncomplete || !pajakFlag ||
			userAktif || hash != "!identity-revoked!" || tokenRegistrasiAktif != 0 || tokenResetAktif != 0 || sesiAktif != 0 {
			t.Fatalf("revokasi akhir tidak fail-closed: kepala=%v email=%v status=%s pajakFlag=%v user=%v hash=%q reg=%d reset=%d sesi=%d",
				namaKepala, email, identityStatus, pajakFlag, userAktif, hash,
				tokenRegistrasiAktif, tokenResetAktif, sesiAktif)
		}
		status, _, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookiesCabang, "")
		if status != http.StatusUnauthorized {
			t.Fatalf("sesi pra-revokasi harus dicabut, dapat %d", status)
		}
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/users/branch/"+f.userA+"/reset", "", cookiesAdmin, csrfAdmin)
		if status != http.StatusConflict {
			t.Fatalf("reset baru pada identitas incomplete harus 409, dapat %d (%v)", status, body)
		}
		assertErrorShape(t, body, "BRANCH_IDENTITY_INCOMPLETE")

		var latestBefore, latestAfter, previousBefore, previousAfter string
		var latestPajakFlag, previousPajakFlag bool
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', after->>'identity_status',
 (after->>'pajak_in')::boolean FROM audit_logs
 WHERE entity='branch' AND action='update' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, f.branchA).
			Scan(&latestBefore, &latestAfter, &latestPajakFlag); err != nil {
			t.Fatalf("baca audit revokasi: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT before->>'identity_status', after->>'identity_status',
 (after->>'pajak_in')::boolean FROM audit_logs
 WHERE entity='branch' AND action='update' AND entity_id=$1 ORDER BY id DESC LIMIT 1 OFFSET 1`, f.branchA).
			Scan(&previousBefore, &previousAfter, &previousPajakFlag); err != nil {
			t.Fatalf("baca audit PUT parsial: %v", err)
		}
		if previousBefore != branch.IdentityRegistered || previousAfter != branch.IdentityRegistered || !previousPajakFlag ||
			latestBefore != branch.IdentityRegistered || latestAfter != branch.IdentityIncomplete || !latestPajakFlag {
			t.Fatalf("urutan audit tidak mencerminkan commit parsial lalu revokasi: partial=%s->%s/%v revoke=%s->%s/%v",
				previousBefore, previousAfter, previousPajakFlag, latestBefore, latestAfter, latestPajakFlag)
		}
	})
}

func TestPGRegistrasiDanPutParsialTidakMenurunkanIdentityStatus(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	branchID := insertBranch(t, ctx, f.pool, "Cabang Race", "Kepala Race", "race@example.test", true)
	issued, err := f.branches.GenerateToken(ctx,
		auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}, branchID, time.Hour)
	if err != nil {
		t.Fatalf("terbitkan token registrasi: %v", err)
	}
	cookiesAdmin, csrfAdmin := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai blocker cabang: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, branchID); err != nil {
		t.Fatalf("kunci cabang untuk barrier: %v", err)
	}

	registrationResult := make(chan hasilPutRevokasi, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/api/v1/auth/register", strings.NewReader(
			`{"token":"`+issued.Token+`","telepon":"0812","password":"credentialbaru123","password_konfirmasi":"credentialbaru123"}`))
		if err != nil {
			registrationResult <- hasilPutRevokasi{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			registrationResult <- hasilPutRevokasi{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		registrationResult <- hasilPutRevokasi{status: resp.StatusCode}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock'
   AND query LIKE '%SELECT id FROM branches WHERE id=$1 FOR UPDATE%')`).Scan(&blocked); err != nil {
			t.Fatalf("periksa barrier registrasi: %v", err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("registrasi tidak tertahan pada lock cabang")
		}
		time.Sleep(10 * time.Millisecond)
	}

	partialResult := mulaiPutCabang(t, f.srv.URL, branchID, `{"pajak_in":true}`, cookiesAdmin, csrfAdmin)
	deadline = time.Now().Add(5 * time.Second)
	for {
		var blocked int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
 WHERE pid<>pg_backend_pid() AND wait_event_type='Lock'
   AND query LIKE '%branches%FOR UPDATE%'`).Scan(&blocked); err != nil {
			t.Fatalf("periksa kedua request pada barrier: %v", err)
		}
		if blocked >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registrasi dan PUT tidak sama-sama mencapai barrier, blocked=%d", blocked)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("lepas barrier cabang: %v", err)
	}

	select {
	case hasil := <-registrationResult:
		if hasil.err != nil || hasil.status != http.StatusCreated {
			t.Fatalf("registrasi paralel gagal: status=%d err=%v", hasil.status, hasil.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("registrasi paralel tidak selesai")
	}
	select {
	case hasil := <-partialResult:
		if hasil.err != nil || hasil.status != http.StatusOK {
			t.Fatalf("PUT parsial paralel gagal: status=%d err=%v", hasil.status, hasil.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PUT parsial paralel tidak selesai")
	}

	var status string
	var pajakFlag bool
	var accounts int
	if err := f.pool.QueryRow(ctx, `SELECT identity_status::text, pajak_in,
 (SELECT count(*) FROM branch_users WHERE branch_id=$1) FROM branches WHERE id=$1`, branchID).
		Scan(&status, &pajakFlag, &accounts); err != nil {
		t.Fatalf("baca state akhir registrasi-vs-PUT: %v", err)
	}
	if status != branch.IdentityRegistered || !pajakFlag || accounts != 1 {
		t.Fatalf("state akhir harus registered + patch parsial: status=%s pajakFlag=%v accounts=%d", status, pajakFlag, accounts)
	}
}

func TestPGAktivasiBersamaanDenganPengosonganIdentitasTetapFailClosed(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai transaksi penurun identitas: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, f.branchA); err != nil {
		t.Fatalf("kunci cabang: %v", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE branches
   SET nama_kepala=NULL, email=NULL, identity_status='identity_incomplete'
 WHERE id=$1`, f.branchA); err != nil {
		t.Fatalf("turunkan identitas dalam transaksi: %v", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE branch_users
   SET is_active=false, password_hash='!identity-revoked!'
 WHERE id=$1`, f.userA); err != nil {
		t.Fatalf("cabut credential dalam transaksi: %v", err)
	}

	hasil := make(chan error, 1)
	go func() {
		_, err := f.users.SetBranchUserActive(context.Background(),
			auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}, f.userA, true)
		hasil <- err
	}()
	select {
	case err := <-hasil:
		t.Fatalf("aktivasi tidak boleh melewati transaksi identitas yang belum commit: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Aktivasi tertahan pada lock cabang, sesuai urutan serialisasi.
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit penurun identitas: %v", err)
	}
	select {
	case err := <-hasil:
		if !errors.Is(err, users.ErrIdentityIncomplete) {
			t.Fatalf("aktivasi sesudah interleaving harus ErrIdentityIncomplete, dapat %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("aktivasi tidak selesai setelah lock dilepas")
	}
	var aktif bool
	if err := f.pool.QueryRow(ctx, `SELECT is_active FROM branch_users WHERE id=$1`, f.userA).Scan(&aktif); err != nil {
		t.Fatalf("baca status akun akhir: %v", err)
	}
	if aktif {
		t.Fatal("akun identity_incomplete tidak boleh kembali aktif")
	}
}

func TestPGMutasiKredensialTerserialisasiDenganRevokasiIdentitas(t *testing.T) {
	for _, pemulihan := range []struct {
		nama  string
		email string
	}{
		{nama: "email tetap", email: "a@example.test"},
		{nama: "email berubah", email: "baru-a@example.test"},
	} {
		pemulihan := pemulihan
		t.Run("reset lama ditolak setelah pemulihan "+pemulihan.nama, func(t *testing.T) {
			f := newPGFixture(t)
			repo := authpg.New(f.pool)
			issuer := auth.NewService(repo, repo, repo, clock.System(), 8*time.Hour, 30*24*time.Hour)
			issuer.SetResetMailer(f.mail)
			if err := issuer.ForgotPassword(context.Background(), "a@example.test", time.Hour, auth.RequestMeta{}); err != nil {
				t.Fatalf("terbitkan reset lama: %v", err)
			}
			token := f.mail.tokenTerakhir(t)
			entered := make(chan struct{}, 1)
			proceed := make(chan struct{})
			resets := resetPasswordBarrier{ResetStore: repo, entered: entered, proceed: proceed}
			svc := auth.NewService(repo, repo, resets, clock.System(), 8*time.Hour, 30*24*time.Hour)
			hasil := make(chan error, 1)
			go func() { hasil <- svc.ResetPassword(context.Background(), token, "pilihanlama123", auth.RequestMeta{}) }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("reset lama tidak mencapai barrier mutasi")
			}
			hashBaru, err := auth.HashPassword("credentialbaru123")
			if err != nil {
				t.Fatalf("hash credential pemulihan: %v", err)
			}
			pulihkanIdentitasDenganKredensialBaru(t, f, pemulihan.email, hashBaru)
			close(proceed)
			select {
			case err := <-hasil:
				if !errors.Is(err, auth.ErrResetInvalid) {
					t.Fatalf("reset pra-revokasi harus ditolak, dapat %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reset lama tidak selesai")
			}
			if _, err := issuer.Login(context.Background(), pemulihan.email, "credentialbaru123", auth.RequestMeta{}); err != nil {
				t.Fatalf("credential pemulihan harus tetap berlaku: %v", err)
			}
			if _, err := issuer.Login(context.Background(), pemulihan.email, "pilihanlama123", auth.RequestMeta{}); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("password request lama harus ditolak, dapat %v", err)
			}
		})

		t.Run("ganti sandi lama ditolak setelah pemulihan "+pemulihan.nama, func(t *testing.T) {
			f := newPGFixture(t)
			repo := authpg.New(f.pool)
			loginSvc := auth.NewService(repo, repo, repo, clock.System(), 8*time.Hour, 30*24*time.Hour)
			issued, err := loginSvc.Login(context.Background(), "a@example.test", "rahasia123", auth.RequestMeta{})
			if err != nil {
				t.Fatalf("login sesi lama: %v", err)
			}
			entered := make(chan struct{}, 1)
			proceed := make(chan struct{})
			users := changePasswordBarrier{UserStore: repo, entered: entered, proceed: proceed}
			svc := auth.NewService(users, repo, repo, clock.System(), 8*time.Hour, 30*24*time.Hour)
			hasil := make(chan error, 1)
			go func() {
				hasil <- svc.ChangePassword(context.Background(), issued.Actor, "rahasia123", "pilihanlama123", auth.RequestMeta{})
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("ganti sandi lama tidak mencapai barrier setelah verifikasi")
			}
			hashBaru, err := auth.HashPassword("credentialbaru123")
			if err != nil {
				t.Fatalf("hash credential pemulihan: %v", err)
			}
			pulihkanIdentitasDenganKredensialBaru(t, f, pemulihan.email, hashBaru)
			close(proceed)
			select {
			case err := <-hasil:
				if !errors.Is(err, auth.ErrSessionInvalid) {
					t.Fatalf("ganti sandi sesi pra-revokasi harus ditolak, dapat %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ganti sandi lama tidak selesai")
			}
			if _, err := loginSvc.Authenticate(context.Background(), issued.Token); !errors.Is(err, auth.ErrSessionInvalid) {
				t.Fatalf("sesi lama harus tetap dicabut, dapat %v", err)
			}
			if _, err := loginSvc.Login(context.Background(), pemulihan.email, "credentialbaru123", auth.RequestMeta{}); err != nil {
				t.Fatalf("credential pemulihan harus tetap berlaku: %v", err)
			}
			if _, err := loginSvc.Login(context.Background(), pemulihan.email, "pilihanlama123", auth.RequestMeta{}); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("password request lama harus ditolak, dapat %v", err)
			}
		})
	}

	for _, pemulihan := range []struct {
		nama  string
		email string
	}{
		{nama: "email tetap", email: "a@example.test"},
		{nama: "email berubah", email: "baru-a@example.test"},
	} {
		pemulihan := pemulihan
		t.Run("reset menang lock lalu revokasi mencabut "+pemulihan.nama, func(t *testing.T) {
			ujiMutasiMenangLaluRevokasi(t, "reset", pemulihan.email)
		})
		t.Run("ganti sandi menang lock lalu revokasi mencabut "+pemulihan.nama, func(t *testing.T) {
			ujiMutasiMenangLaluRevokasi(t, "change", pemulihan.email)
		})
	}

	t.Run("happy path reset ganda dan ganti sandi", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		repo := authpg.New(f.pool)
		svc := auth.NewService(repo, repo, repo, clock.System(), 8*time.Hour, 30*24*time.Hour)
		svc.SetResetMailer(f.mail)
		if err := svc.ForgotPassword(ctx, "a@example.test", time.Hour, auth.RequestMeta{}); err != nil {
			t.Fatalf("terbitkan reset happy path: %v", err)
		}
		token := f.mail.tokenTerakhir(t)
		if err := svc.ResetPassword(ctx, token, "sandireset123", auth.RequestMeta{}); err != nil {
			t.Fatalf("reset pertama harus berhasil: %v", err)
		}
		if err := svc.ResetPassword(ctx, token, "sandikedua123", auth.RequestMeta{}); !errors.Is(err, auth.ErrResetInvalid) {
			t.Fatalf("reset ganda harus ditolak, dapat %v", err)
		}
		issued, err := svc.Login(ctx, "a@example.test", "sandireset123", auth.RequestMeta{})
		if err != nil {
			t.Fatalf("password hasil reset harus dapat login: %v", err)
		}
		if err := svc.ChangePassword(ctx, issued.Actor, "sandireset123", "sandiganti123", auth.RequestMeta{}); err != nil {
			t.Fatalf("ganti sandi happy path: %v", err)
		}
		if _, err := svc.Authenticate(ctx, issued.Token); err != nil {
			t.Fatalf("sesi peminta harus tetap hidup: %v", err)
		}
		if _, err := svc.Login(ctx, "a@example.test", "sandiganti123", auth.RequestMeta{}); err != nil {
			t.Fatalf("password hasil ganti harus dapat login: %v", err)
		}
		if _, err := svc.Login(ctx, "a@example.test", "sandireset123", auth.RequestMeta{}); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("password sebelum ganti harus ditolak, dapat %v", err)
		}
	})
}

func TestPGPenerbitanArtefakAuthTerserialisasiDenganRevokasiIdentitas(t *testing.T) {
	t.Run("sesi", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		repo := authpg.New(f.pool)
		entered := make(chan struct{}, 1)
		svc := auth.NewService(repo, sessionCreateBarrier{SessionStore: repo, entered: entered}, repo,
			clock.System(), 8*time.Hour, 30*24*time.Hour)

		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("mulai transaksi revokasi: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, f.branchA); err != nil {
			t.Fatalf("kunci cabang: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE branches SET nama_kepala=NULL, identity_status='identity_incomplete' WHERE id=$1`, f.branchA); err != nil {
			t.Fatalf("turunkan identitas: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA); err != nil {
			t.Fatalf("cabut credential: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, f.userA); err != nil {
			t.Fatalf("cabut sesi: %v", err)
		}

		hasil := make(chan error, 1)
		go func() {
			_, err := svc.Login(context.Background(), "a@example.test", "rahasia123", auth.RequestMeta{})
			hasil <- err
		}()
		select {
		case <-entered:
			// Login sudah membaca credential lama dan tepat akan memasuki boundary transaksi.
		case <-time.After(10 * time.Second):
			t.Fatal("login tidak mencapai penerbitan sesi")
		}
		select {
		case err := <-hasil:
			t.Fatalf("penerbitan sesi melewati lock revokasi: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit revokasi: %v", err)
		}
		select {
		case err := <-hasil:
			if !errors.Is(err, auth.ErrInactiveAccount) {
				t.Fatalf("login setelah revokasi harus fail-closed, dapat %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("login tidak selesai setelah lock dilepas")
		}
		var aktif int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, f.userA).Scan(&aktif); err != nil {
			t.Fatalf("hitung sesi aktif: %v", err)
		}
		if aktif != 0 {
			t.Fatalf("revokasi tidak boleh disusul sesi aktif, dapat %d", aktif)
		}
	})

	t.Run("reset publik dan admin", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		repo := authpg.New(f.pool)
		entered := make(chan struct{}, 2)
		svc := auth.NewService(repo, repo, resetCreateBarrier{ResetStore: repo, entered: entered},
			clock.System(), 8*time.Hour, 30*24*time.Hour)

		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("mulai transaksi revokasi: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, f.branchA); err != nil {
			t.Fatalf("kunci cabang: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE branches SET nama_kepala=NULL, identity_status='identity_incomplete' WHERE id=$1`, f.branchA); err != nil {
			t.Fatalf("turunkan identitas: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA); err != nil {
			t.Fatalf("cabut credential: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE password_reset_tokens SET revoked_at=now()
 WHERE actor_kind='branch' AND user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, f.userA); err != nil {
			t.Fatalf("cabut token reset: %v", err)
		}

		publik := make(chan error, 1)
		admin := make(chan error, 1)
		go func() {
			publik <- svc.ForgotPassword(context.Background(), "a@example.test", time.Hour, auth.RequestMeta{})
		}()
		go func() {
			admin <- svc.SendPasswordResetFor(context.Background(), auth.KindBranch, f.userA, time.Hour, nil)
		}()
		for i := 0; i < 2; i++ {
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("reset tidak mencapai penerbitan token")
			}
		}
		select {
		case err := <-publik:
			t.Fatalf("reset publik melewati lock revokasi: %v", err)
		case err := <-admin:
			t.Fatalf("reset admin melewati lock revokasi: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit revokasi: %v", err)
		}
		select {
		case err := <-publik:
			if err != nil {
				t.Fatalf("reset publik tetap non-enumeratif, dapat %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reset publik tidak selesai setelah lock dilepas")
		}
		select {
		case err := <-admin:
			if !errors.Is(err, auth.ErrInactiveAccount) {
				t.Fatalf("reset admin setelah revokasi harus fail-closed, dapat %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reset admin tidak selesai setelah lock dilepas")
		}
		var aktif int
		if err := f.pool.QueryRow(ctx, `
SELECT count(*) FROM password_reset_tokens
 WHERE actor_kind='branch' AND user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, f.userA).Scan(&aktif); err != nil {
			t.Fatalf("hitung token reset aktif: %v", err)
		}
		if aktif != 0 {
			t.Fatalf("revokasi tidak boleh disusul token reset aktif, dapat %d", aktif)
		}
	})

	t.Run("login snapshot password lama ditolak setelah pemulihan", func(t *testing.T) {
		f := newPGFixture(t)
		repo := authpg.New(f.pool)
		entered := make(chan struct{}, 1)
		proceed := make(chan struct{})
		svc := auth.NewService(repo, sessionCreateBarrier{SessionStore: repo, entered: entered, proceed: proceed}, repo,
			clock.System(), 8*time.Hour, 30*24*time.Hour)

		hasil := make(chan error, 1)
		go func() {
			_, err := svc.Login(context.Background(), "a@example.test", "rahasia123", auth.RequestMeta{})
			hasil <- err
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("login lama tidak mencapai barrier sesi")
		}
		hashBaru, err := auth.HashPassword("credentialbaru123")
		if err != nil {
			t.Fatalf("hash credential pemulihan: %v", err)
		}
		pulihkanIdentitasDenganKredensialBaru(t, f, "baru-a@example.test", hashBaru)
		close(proceed)
		select {
		case err := <-hasil:
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("password lama harus ditolak sesudah pemulihan, dapat %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("login lama tidak selesai setelah barrier dilepas")
		}
		var aktif int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, f.userA).Scan(&aktif); err != nil {
			t.Fatalf("hitung sesi usang: %v", err)
		}
		if aktif != 0 {
			t.Fatalf("password lama menghasilkan %d sesi aktif", aktif)
		}
		if _, err := svc.Login(context.Background(), "baru-a@example.test", "credentialbaru123", auth.RequestMeta{}); err != nil {
			t.Fatalf("credential baru harus dapat login: %v", err)
		}
	})

	for _, jalur := range []string{"publik", "admin"} {
		jalur := jalur
		for _, pemulihan := range []struct {
			nama  string
			email string
		}{
			{nama: "email berubah", email: "baru-a@example.test"},
			{nama: "email tetap", email: "a@example.test"},
		} {
			pemulihan := pemulihan
			t.Run("reset snapshot lama ditolak setelah pemulihan "+jalur+" "+pemulihan.nama, func(t *testing.T) {
				f := newPGFixture(t)
				repo := authpg.New(f.pool)
				entered := make(chan struct{}, 1)
				proceed := make(chan struct{})
				svc := auth.NewService(repo, repo, resetCreateBarrier{ResetStore: repo, entered: entered, proceed: proceed},
					clock.System(), 8*time.Hour, 30*24*time.Hour)
				svc.SetResetMailer(f.mail)

				hasil := make(chan error, 1)
				go func() {
					if jalur == "publik" {
						hasil <- svc.ForgotPassword(context.Background(), "a@example.test", time.Hour, auth.RequestMeta{})
						return
					}
					hasil <- svc.SendPasswordResetFor(context.Background(), auth.KindBranch, f.userA, time.Hour, nil)
				}()
				select {
				case <-entered:
				case <-time.After(10 * time.Second):
					t.Fatal("reset lama tidak mencapai barrier token")
				}
				hashBaru, err := auth.HashPassword("credentialbaru123")
				if err != nil {
					t.Fatalf("hash credential pemulihan: %v", err)
				}
				pulihkanIdentitasDenganKredensialBaru(t, f, pemulihan.email, hashBaru)
				close(proceed)
				select {
				case err := <-hasil:
					if jalur == "publik" {
						if err != nil {
							t.Fatalf("reset publik harus tetap non-enumeratif, dapat %v", err)
						}
					} else if !errors.Is(err, auth.ErrInactiveAccount) {
						t.Fatalf("reset admin dengan snapshot usang harus fail-closed, dapat %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("reset lama tidak selesai setelah barrier dilepas")
				}
				if f.mail.jumlah() != 0 {
					t.Fatal("request pra-revokasi tidak boleh mengirim tautan setelah pemulihan")
				}
				var aktif int
				if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM password_reset_tokens WHERE actor_kind='branch' AND user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, f.userA).Scan(&aktif); err != nil {
					t.Fatalf("hitung token reset usang: %v", err)
				}
				if aktif != 0 {
					t.Fatalf("snapshot lama menghasilkan %d token aktif", aktif)
				}
				if err := svc.ForgotPassword(context.Background(), pemulihan.email, time.Hour, auth.RequestMeta{}); err != nil {
					t.Fatalf("reset sah setelah pemulihan harus bekerja: %v", err)
				}
				if f.mail.jumlah() != 1 {
					t.Fatalf("reset sah harus mengirim tepat satu email, dapat %d", f.mail.jumlah())
				}
			})
		}
	}

	t.Run("sesi terbit lebih dahulu lalu revokasi menunggu dan mencabut", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		repo := authpg.New(f.pool)
		svc := auth.NewService(repo, repo, repo, clock.System(), 8*time.Hour, 30*24*time.Hour)
		blocker, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("mulai blocker sesi: %v", err)
		}
		defer func() { _ = blocker.Rollback(ctx) }()
		if _, err := blocker.Exec(ctx, `LOCK TABLE sessions IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatalf("kunci tabel sesi: %v", err)
		}

		type loginResult struct {
			issued auth.IssuedSession
			err    error
		}
		login := make(chan loginResult, 1)
		go func() {
			issued, err := svc.Login(context.Background(), "a@example.test", "rahasia123", auth.RequestMeta{})
			login <- loginResult{issued: issued, err: err}
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var blocked bool
			if err := f.pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_stat_activity
 WHERE pid <> pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%INSERT INTO sessions%')`).Scan(&blocked); err != nil {
				t.Fatalf("periksa barrier sesi: %v", err)
			}
			if blocked {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("penerbitan sesi tidak mencapai INSERT setelah memegang lock identitas")
			}
			time.Sleep(10 * time.Millisecond)
		}

		revoked := make(chan error, 1)
		go func() {
			tx, err := f.pool.Begin(context.Background())
			if err == nil {
				_, err = tx.Exec(context.Background(), `UPDATE branches SET nama_kepala=NULL, email=NULL, identity_status='identity_incomplete' WHERE id=$1`, f.branchA)
			}
			if err == nil {
				_, err = tx.Exec(context.Background(), `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA)
			}
			if err == nil {
				_, err = tx.Exec(context.Background(), `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, f.userA)
			}
			if err == nil {
				err = tx.Commit(context.Background())
			} else if tx != nil {
				_ = tx.Rollback(context.Background())
			}
			revoked <- err
		}()
		select {
		case err := <-revoked:
			t.Fatalf("revokasi seharusnya menunggu lock penerbitan, selesai dini: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatalf("lepas blocker sesi: %v", err)
		}
		var hasil loginResult
		select {
		case hasil = <-login:
			if hasil.err != nil {
				t.Fatalf("login yang menang lock harus sempat menerbitkan sesi: %v", hasil.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("login tidak selesai setelah blocker dilepas")
		}
		select {
		case err := <-revoked:
			if err != nil {
				t.Fatalf("revokasi setelah menunggu gagal: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("revokasi tidak selesai setelah sesi commit")
		}
		if _, err := svc.Authenticate(ctx, hasil.issued.Token); err == nil {
			t.Fatal("sesi yang terbit sebelum revokasi harus sudah ditolak")
		}
	})

	t.Run("reset terbit lebih dahulu lalu revokasi menunggu dan mencabut", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		repo := authpg.New(f.pool)
		svc := auth.NewService(repo, repo, repo, clock.System(), 8*time.Hour, 30*24*time.Hour)
		svc.SetResetMailer(f.mail)
		blocker, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("mulai blocker reset: %v", err)
		}
		defer func() { _ = blocker.Rollback(ctx) }()
		if _, err := blocker.Exec(ctx, `LOCK TABLE password_reset_tokens IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatalf("kunci tabel reset: %v", err)
		}
		resetDone := make(chan error, 1)
		go func() {
			resetDone <- svc.ForgotPassword(context.Background(), "a@example.test", time.Hour, auth.RequestMeta{})
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var blocked bool
			if err := f.pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_stat_activity
 WHERE pid <> pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%password_reset_tokens%')`).Scan(&blocked); err != nil {
				t.Fatalf("periksa barrier reset: %v", err)
			}
			if blocked {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("penerbitan reset tidak mencapai INSERT setelah memegang lock identitas")
			}
			time.Sleep(10 * time.Millisecond)
		}
		revoked := make(chan error, 1)
		go func() {
			tx, err := f.pool.Begin(context.Background())
			if err == nil {
				_, err = tx.Exec(context.Background(), `UPDATE branches SET nama_kepala=NULL, email=NULL, identity_status='identity_incomplete' WHERE id=$1`, f.branchA)
			}
			if err == nil {
				_, err = tx.Exec(context.Background(), `UPDATE branch_users SET is_active=false, password_hash='!identity-revoked!' WHERE id=$1`, f.userA)
			}
			if err == nil {
				_, err = tx.Exec(context.Background(), `UPDATE password_reset_tokens SET revoked_at=now() WHERE actor_kind='branch' AND user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, f.userA)
			}
			if err == nil {
				err = tx.Commit(context.Background())
			} else if tx != nil {
				_ = tx.Rollback(context.Background())
			}
			revoked <- err
		}()
		select {
		case err := <-revoked:
			t.Fatalf("revokasi seharusnya menunggu lock reset, selesai dini: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatalf("lepas blocker reset: %v", err)
		}
		select {
		case err := <-resetDone:
			if err != nil {
				t.Fatalf("reset yang menang lock harus sempat terbit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reset tidak selesai setelah blocker dilepas")
		}
		select {
		case err := <-revoked:
			if err != nil {
				t.Fatalf("revokasi reset setelah menunggu gagal: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("revokasi reset tidak selesai setelah token commit")
		}
		var aktif int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE actor_kind='branch' AND user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, f.userA).Scan(&aktif); err != nil {
			t.Fatalf("hitung token setelah revokasi: %v", err)
		}
		if aktif != 0 {
			t.Fatalf("token yang terbit sebelum revokasi harus dicabut, aktif=%d", aktif)
		}
	})
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

	// Generate ulang: token lama dicabut, token baru berlaku, tetap satu baris aktif.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/branches/"+id+"/token",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("generate ulang harus 200, dapat %d (%v)", status, body)
	}
	tokenBaru := body["data"].(map[string]any)["token"].(string)
	if tokenBaru == token {
		t.Error("generate ulang harus menghasilkan token berbeda")
	}
	var nLamaDicabut, nBaru, nAktif int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE token_hash = sha256($2::bytea) AND revoked_at IS NOT NULL),
		        count(*) FILTER (WHERE token_hash = sha256($3::bytea)),
		        count(*) FILTER (WHERE used_at IS NULL AND revoked_at IS NULL)
		   FROM registration_tokens WHERE branch_id = $1`,
		id, strings.ReplaceAll(token, "-", ""), strings.ReplaceAll(tokenBaru, "-", "")).Scan(&nLamaDicabut, &nBaru, &nAktif); err != nil {
		t.Fatalf("periksa token lama/baru: %v", err)
	}
	if nLamaDicabut != 1 {
		t.Error("token lama harus tetap tercatat sebagai revoked")
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
