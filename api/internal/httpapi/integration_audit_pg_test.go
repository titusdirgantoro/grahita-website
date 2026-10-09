package httpapi

// Uji integrasi antarmuka audit log (F2-28) pada PostgreSQL nyata.
//
// Berbeda dari router_audit_test.go (pembaca palsu), berkas ini menggerakkan
// router lengkap dengan repository asli sehingga yang diuji adalah perilaku
// sebenarnya: entri audit ditulis di dalam transaksi mutasinya, rollback tidak
// meninggalkan event, filter/paginasi stabil pada data nyata, dan data
// sensitif tidak pernah bocor lewat respons.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi (lihat integration_pg_test.go).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
)

// auditJSON mengambil before/after satu entri sebagai teks JSON ("" bila NULL).
func auditJSON(t *testing.T, ctx context.Context, f *pgFixture, id string) (string, string) {
	t.Helper()
	var before, after *string
	if err := f.pool.QueryRow(ctx,
		`SELECT before::text, after::text FROM audit_logs WHERE id = $1`, id).Scan(&before, &after); err != nil {
		t.Fatalf("baca entri audit %s: %v", id, err)
	}
	teks := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	return teks(before), teks(after)
}

// hitungAudit menghitung baris audit yang cocok dengan WHERE yang diberikan.
func hitungAudit(t *testing.T, ctx context.Context, f *pgFixture, where string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatalf("hitung audit (%s): %v", where, err)
	}
	return n
}

// idAudit terbaru untuk satu (entity, action) — dipakai agar pengujian bisa
// memeriksa isi entri yang baru saja ditulis.
func idAudit(t *testing.T, ctx context.Context, f *pgFixture, entity, action string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(ctx,
		`SELECT id::text FROM audit_logs WHERE entity = $1 AND action = $2 ORDER BY at DESC, id DESC LIMIT 1`,
		entity, action).Scan(&id); err != nil {
		t.Fatalf("entri audit %s/%s tidak ada: %v", entity, action, err)
	}
	return id
}

// halamanAudit memanggil GET /api/v1/admin/audit-logs dan mengembalikan data.
func halamanAudit(t *testing.T, f *pgFixture, cookies []*http.Cookie, query string) map[string]any {
	t.Helper()
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/audit-logs"+query, "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("GET audit-logs%s harus 200, dapat %d (%v)", query, status, body)
	}
	return body["data"].(map[string]any)
}

// TestPGAuditMutasiAkunTercatatTepatSekali membuktikan setiap mutasi akun
// menghasilkan TEPAT satu entri audit yang aman, dan no-op tidak menghasilkan
// apa pun.
func TestPGAuditMutasiAkunTercatatTepatSekali(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	sa, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// 1. Undang admin baru: satu entri create dengan snapshot akun baru.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Dewi Lestari","email":"dewi.audit@example.test","role":"manager","notif_invoice":true}`,
		sa, csrf)
	if status != http.StatusCreated {
		t.Fatalf("undang admin harus 201, dapat %d (%v)", status, body)
	}
	adminBaru := body["data"].(map[string]any)["id"].(string)
	if n := hitungAudit(t, ctx, f, `entity = 'admin_user' AND action = 'create'`); n != 1 {
		t.Fatalf("undangan admin harus menghasilkan 1 entri, dapat %d", n)
	}
	before, after := auditJSON(t, ctx, f, idAudit(t, ctx, f, "admin_user", "create"))
	if before != "" {
		t.Errorf("create tidak boleh punya before: %s", before)
	}
	for _, mau := range []string{"Dewi Lestari", "manager"} {
		if !strings.Contains(after, mau) {
			t.Errorf("snapshot after tidak memuat %q: %s", mau, after)
		}
	}
	// Email akun dan kata sandi tidak boleh masuk snapshot.
	if strings.Contains(strings.ToLower(after), "dewi.audit@example.test") || strings.Contains(after, "password") {
		t.Errorf("snapshot audit memuat data sensitif: %s", after)
	}
	var actorID, actorNama, ip, entityID string
	if err := f.pool.QueryRow(ctx,
		`SELECT COALESCE(actor_id::text,''), COALESCE(actor_nama,''), COALESCE(host(ip),''), COALESCE(entity_id::text,'')
		   FROM audit_logs WHERE entity='admin_user' AND action='create'`).Scan(&actorID, &actorNama, &ip, &entityID); err != nil {
		t.Fatalf("baca metadata aktor: %v", err)
	}
	if actorID != f.adminSA || actorNama != "Rina" || ip == "" {
		t.Errorf("aktor/IP audit salah: id=%q nama=%q ip=%q", actorID, actorNama, ip)
	}
	if entityID != adminBaru {
		t.Errorf("entity_id harus id akun baru (%s), dapat %q", adminBaru, entityID)
	}

	// 2. Ubah role: satu entri update with delta yang terlihat.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/admin/"+adminBaru, `{"role":"superadmin"}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah role harus 200, dapat %d (%v)", status, body)
	}
	if n := hitungAudit(t, ctx, f, `entity='admin_user' AND action='update'`); n != 1 {
		t.Fatalf("ubah role harus menghasilkan 1 entri, dapat %d", n)
	}
	before, after = auditJSON(t, ctx, f, idAudit(t, ctx, f, "admin_user", "update"))
	if !strings.Contains(before, `"role": "manager"`) || !strings.Contains(after, `"role": "superadmin"`) {
		t.Errorf("delta role tidak tercatat: %s → %s", before, after)
	}

	// 3. Kirim ulang nilai yang sama: no-op, tanpa entri baru.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/admin/"+adminBaru, `{"role":"superadmin"}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("no-op harus 200, dapat %d", status)
	}
	if n := hitungAudit(t, ctx, f, `entity='admin_user' AND action='update'`); n != 1 {
		t.Errorf("no-op tidak boleh menambah entri: %d", n)
	}

	// 4. Nonaktifkan akun cabang: satu entri branch_user/update.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userA, `{"is_active":false}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan akun cabang harus 200, dapat %d (%v)", status, body)
	}
	if n := hitungAudit(t, ctx, f, `entity='branch_user' AND action='update'`); n != 1 {
		t.Fatalf("nonaktifkan akun cabang harus 1 entri, dapat %d", n)
	}
	before, after = auditJSON(t, ctx, f, idAudit(t, ctx, f, "branch_user", "update"))
	if !strings.Contains(before, `"is_active": true`) || !strings.Contains(after, `"is_active": false`) {
		t.Errorf("delta status akun cabang tidak tercatat: %s → %s", before, after)
	}
	// Mengirim status yang sama: no-op.
	if status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userA, `{"is_active":false}`, sa, csrf); status != http.StatusOK {
		t.Fatalf("no-op status akun cabang harus 200, dapat %d", status)
	}
	if n := hitungAudit(t, ctx, f, `entity='branch_user' AND action='update'`); n != 1 {
		t.Errorf("no-op status akun cabang tidak boleh menambah entri: %d", n)
	}

	// 5. Kirim tautan atur ulang: satu entri reset tanpa nilai tautannya.
	//    Memakai userB yang masih aktif (usr-A baru saja dinonaktifkan di atas).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/branch/"+f.userB+"/reset", "", sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("kirim tautan atur ulang harus 200, dapat %d (%v)", status, body)
	}
	if n := hitungAudit(t, ctx, f, `entity='branch_user' AND action='reset'`); n != 1 {
		t.Fatalf("tautan atur ulang harus 1 entri audit, dapat %d", n)
	}
	_, after = auditJSON(t, ctx, f, idAudit(t, ctx, f, "branch_user", "reset"))
	if strings.Contains(strings.ToLower(after), "token") || strings.Contains(after, "lupa?") {
		t.Errorf("snapshot audit memuat tautan/token: %s", after)
	}

	// 6. Ganti nama sendiri (PUT /auth/me): satu entri, no-op tanpa entri.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/auth/me", `{"nama":"Rina Kartika"}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah nama sendiri harus 200, dapat %d (%v)", status, body)
	}
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE entity='admin_user' AND action='update' AND entity_id=$1`,
		f.adminSA).Scan(&n); err != nil {
		t.Fatalf("hitung audit ubah nama: %v", err)
	}
	if n != 1 {
		t.Fatalf("ubah nama sendiri harus 1 entri, dapat %d", n)
	}
	before, after = auditJSON(t, ctx, f, idAudit(t, ctx, f, "admin_user", "update"))
	if !strings.Contains(before, "Rina") || !strings.Contains(after, "Rina Kartika") {
		t.Errorf("delta nama tidak tercatat: %s → %s", before, after)
	}
	// Nama sama lagi = no-op.
	if status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/auth/me", `{"nama":"Rina Kartika"}`, sa, csrf); status != http.StatusOK {
		t.Fatalf("no-op nama harus 200, dapat %d", status)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE entity='admin_user' AND action='update' AND entity_id=$1`,
		f.adminSA).Scan(&n); err != nil {
		t.Fatalf("hitung ulang audit ubah nama: %v", err)
	}
	if n != 1 {
		t.Errorf("no-op nama tidak boleh menambah entri: %d", n)
	}
}

// TestPGAuditRollbackTidakMeninggalkanEvent membuktikan entri audit benar-benar
// satu transaksi dengan perubahannya: bila penulisan audit gagal, perubahannya
// ikut batal.
func TestPGAuditRollbackTidakMeninggalkanEvent(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	sa, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Trigger penggagal: setiap INSERT audit admin_user ditolak.
	if _, err := f.pool.Exec(ctx, `
		CREATE FUNCTION uji_tolak_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.entity = 'admin_user' THEN
				RAISE EXCEPTION 'uji: audit admin_user ditolak';
			END IF;
			RETURN NEW;
		END $$ LANGUAGE plpgsql;
		CREATE TRIGGER uji_tolak_audit BEFORE INSERT ON audit_logs
			FOR EACH ROW EXECUTE FUNCTION uji_tolak_audit();`); err != nil {
		t.Fatalf("pasang trigger penggagal: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS uji_tolak_audit ON audit_logs`)
		_, _ = f.pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS uji_tolak_audit()`)
	})

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/auth/me", `{"nama":"Nama Tidak Boleh Tersimpan"}`, sa, csrf)
	if status != http.StatusInternalServerError {
		t.Fatalf("penulisan audit yang gagal harus menjadi galat server, dapat %d (%v)", status, body)
	}
	var nama string
	if err := f.pool.QueryRow(ctx, `SELECT nama FROM admin_users WHERE id = $1`, f.adminSA).Scan(&nama); err != nil {
		t.Fatalf("baca nama admin: %v", err)
	}
	if nama != "Rina" {
		t.Errorf("perubahan nama harus ikut batal, nama sekarang %q", nama)
	}
	if n := hitungAudit(t, ctx, f, `entity='admin_user'`); n != 0 {
		t.Errorf("rollback tidak boleh meninggalkan entri audit: %d", n)
	}

	// Setelah trigger dilepas, permintaan yang sama berhasil dan tercatat.
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER uji_tolak_audit ON audit_logs`); err != nil {
		t.Fatalf("lepas trigger: %v", err)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/auth/me", `{"nama":"Rina Kartika"}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah nama setelah trigger dilepas harus 200, dapat %d (%v)", status, body)
	}
	if n := hitungAudit(t, ctx, f, `entity='admin_user' AND action='update'`); n != 1 {
		t.Errorf("ubah nama yang berhasil harus menghasilkan 1 entri, dapat %d", n)
	}

	// Permintaan yang ditolak domain (email sudah dipakai) juga tanpa entri.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Duplikat","email":"budi@example.test","role":"manager"}`, sa, csrf)
	if status != http.StatusConflict {
		t.Fatalf("email terpakai harus 409, dapat %d (%v)", status, body)
	}
	if n := hitungAudit(t, ctx, f, `entity='admin_user' AND action='create'`); n != 0 {
		t.Errorf("undangan yang ditolak tidak boleh meninggalkan entri: %d", n)
	}
}

// TestPGAuditInsertMeredaksiSebelumMenyimpan membuktikan penulis audit
// (audit/postgres) menyaring nilai sensitif SEBELUM masuk kolom jsonb — bukan
// hanya saat ditampilkan.
func TestPGAuditInsertMeredaksiSebelumMenyimpan(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	writer := auditpg.New(f.pool)

	rec := audit.Entry{
		ActorKind: auth.KindAdmin,
		ActorID:   f.adminSA,
		ActorNama: "Rina",
		IP:        "203.0.113.9",
		Entity:    audit.EntityBranch,
		EntityID:  f.branchA,
		Action:    audit.ActionTokenGenerate,
		Before: map[string]any{
			"token":        "GRH-RAHASIA-BBB",
			"password":     "rahasia123",
			"token_prefix": "GRH-1234",
		},
		After: map[string]any{
			"bukti_url": "https://storage.example/x?sig=rahasia",
			"is_active": true,
		},
	}
	if err := writer.Record(ctx, rec); err != nil {
		t.Fatalf("catat audit: %v", err)
	}

	var before, after string
	if err := f.pool.QueryRow(ctx,
		`SELECT before::text, after::text FROM audit_logs WHERE entity='branch' AND action='token_generate'`).
		Scan(&before, &after); err != nil {
		t.Fatalf("baca kolom audit: %v", err)
	}
	for _, terlarang := range []string{"GRH-RAHASIA-BBB", "rahasia123", "storage.example"} {
		if strings.Contains(before+after, terlarang) {
			t.Errorf("nilai sensitif %q tersimpan di kolom audit: %s / %s", terlarang, before, after)
		}
	}
	if !strings.Contains(before, audit.NilaiRedaksi) || !strings.Contains(after, audit.NilaiRedaksi) {
		t.Errorf("nilai sensitif tidak ditandai redaksi: %s / %s", before, after)
	}
	if !strings.Contains(before, "GRH-1234") {
		t.Errorf("token_prefix (bukan rahasia) tidak boleh hilang: %s", before)
	}

	// Lewat API pun isinya tetap teredaksi.
	sa, _ := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	data := halamanAudit(t, f, sa, "?entitas=branch&aksi=token_generate")
	item := data["items"].([]any)[0].(map[string]any)
	snap := item["before"].(map[string]any)
	if snap["token"] != audit.NilaiRedaksi || snap["password"] != audit.NilaiRedaksi {
		t.Errorf("API membocorkan nilai sensitif: %v", snap)
	}
}

// TestPGAuditFilterPaginasiDanRedaksi membuktikan filter/paginasi stabil pada
// data nyata (termasuk batas hari WIB dan baris ber-`at` identik) sekaligus
// bahwa baris lama yang memuat data sensitif tetap tampil teredaksi.
func TestPGAuditFilterPaginasiDanRedaksi(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	sa, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	if csrf == "" {
		t.Fatal("cookie CSRF tidak ada")
	}

	// 12 baris dengan `at` IDENTIK (uji tie-break id) + 1 baris sensitif + 2
	// baris batas hari WIB.
	entityID := f.branchA
	for i := 0; i < 12; i++ {
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO audit_logs (at, actor_kind, actor_id, actor_nama, ip, entity, entity_id, action, after)
			 VALUES ('2026-10-07 10:00:00+00', 'admin', $1, 'Rina', '203.0.113.5', 'order', $2, 'update',
			         jsonb_build_object('i', $3::int))`, f.adminSA, entityID, i); err != nil {
			t.Fatalf("seed audit identik %d: %v", i, err)
		}
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO audit_logs (at, actor_kind, actor_id, actor_nama, ip, entity, entity_id, action, before, after)
		 VALUES ('2026-10-08 03:00:00+00', 'admin', $1, 'Budi', '203.0.113.6', 'branch', $2, 'token_generate',
		         jsonb_build_object('token', 'GRH-RAHASIA', 'password', 'x', 'token_prefix', 'GRH-1234',
		                            'bukti_url', 'https://storage.example/x?sig=1'),
		         jsonb_build_object('nama', 'Grahita Cabang A'))`, f.adminMgr, entityID); err != nil {
		t.Fatalf("seed audit sensitif: %v", err)
	}
	// Baris mirip revisi invoice (F2-25): alasan + delta nilai harus tetap
	// terbaca utuh (bukan bagian yang diredaksi).
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO audit_logs (at, actor_kind, actor_id, actor_nama, ip, entity, entity_id, action, before, after)
		 VALUES ('2026-10-06 08:00:00+00', 'admin', $1, 'Rina', '203.0.113.9', 'order', $2, 'revise',
		         jsonb_build_object('versi', 1, 'total', 100000),
		         jsonb_build_object('versi', 2, 'total', 125000, 'delta_total', 25000, 'alasan', 'Harga naik dari pusat'))`,
		f.adminSA, entityID); err != nil {
		t.Fatalf("seed audit revisi: %v", err)
	}
	// 16:59:59Z = 23:59:59 WIB 8 Okt; 17:00:00Z = 00:00:00 WIB 9 Okt.
	for _, at := range []string{"2026-10-08 16:59:59+00", "2026-10-08 17:00:00+00"} {
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO audit_logs (at, actor_kind, actor_id, actor_nama, entity, entity_id, action)
			 VALUES ($1, 'admin', $2, 'Rina', 'order', $3, 'verify')`, at, f.adminSA, entityID); err != nil {
			t.Fatalf("seed audit batas hari: %v", err)
		}
	}

	var totalDB int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&totalDB); err != nil {
		t.Fatalf("hitung audit: %v", err)
	}
	if totalDB != 16 {
		t.Fatalf("perkiraan jumlah baris uji salah: %d", totalDB)
	}

	// Paginasi: seluruh halaman menutup semua baris tanpa duplikat.
	dilihat := map[string]bool{}
	pertama := halamanAudit(t, f, sa, "?per_halaman=4&halaman=1")
	if int(pertama["total"].(float64)) != totalDB {
		t.Fatalf("total halaman pertama salah: %v (DB %d)", pertama["total"], totalDB)
	}
	if int(pertama["total_halaman"].(float64)) != 4 {
		t.Errorf("total_halaman salah: %v", pertama["total_halaman"])
	}
	for halaman := 1; halaman <= 4; halaman++ {
		data := halamanAudit(t, f, sa, fmt.Sprintf("?per_halaman=4&halaman=%d", halaman))
		for _, item := range data["items"].([]any) {
			id := item.(map[string]any)["id"].(string)
			if dilihat[id] {
				t.Errorf("baris %s tampil dua kali antar halaman", id)
			}
			dilihat[id] = true
		}
	}
	if len(dilihat) != totalDB {
		t.Errorf("paginasi melewatkan baris: %d dari %d", len(dilihat), totalDB)
	}

	// Filter entitas (boleh dipisah koma) + aksi.
	data := halamanAudit(t, f, sa, "?entitas=order,branch&per_halaman=100")
	items := data["items"].([]any)
	if len(items) != totalDB {
		t.Errorf("filter entitas salah: %d baris (mau %d)", len(items), totalDB)
	}
	for _, item := range items {
		e := item.(map[string]any)["entity"].(string)
		if e != "order" && e != "branch" {
			t.Errorf("filter entitas meloloskan %q", e)
		}
	}
	data = halamanAudit(t, f, sa, "?aksi=token_generate")
	if len(data["items"].([]any)) != 1 {
		t.Errorf("filter aksi salah: %v", data["items"])
	}

	// Filter aktor: UUID persis dan potongan nama.
	data = halamanAudit(t, f, sa, "?aktor="+f.adminMgr)
	if len(data["items"].([]any)) != 1 {
		t.Errorf("filter aktor UUID salah: %d baris", len(data["items"].([]any)))
	}
	data = halamanAudit(t, f, sa, "?aktor=budi")
	if len(data["items"].([]any)) != 1 {
		t.Errorf("filter aktor nama salah: %d baris", len(data["items"].([]any)))
	}

	// Batas hari WIB: 8 Okt WIB mencakup 16:59:59Z tetapi tidak 17:00:00Z.
	data = halamanAudit(t, f, sa, "?dari=2026-10-08&sampai=2026-10-08&per_halaman=100")
	if n := len(data["items"].([]any)); n != 2 {
		t.Errorf("filter tanggal WIB salah: %d baris (mau 2: token_generate + batas 8 Okt)", n)
	}

	// Baris sensitif tetap tampil teredaksi.
	data = halamanAudit(t, f, sa, "?entitas=branch&aksi=token_generate")
	item := data["items"].([]any)[0].(map[string]any)
	before := item["before"].(map[string]any)
	for _, k := range []string{"token", "password", "bukti_url"} {
		if before[k] != audit.NilaiRedaksi {
			t.Errorf("kunci %q tidak diredaksi saat dibaca: %v", k, before[k])
		}
	}
	if before["token_prefix"] != "GRH-1234" {
		t.Errorf("token_prefix tidak boleh hilang: %v", before)
	}

	// Baris revisi invoice: aktor, alasan, dan delta nilai tampil utuh.
	data = halamanAudit(t, f, sa, "?entitas=order&aksi=revise")
	if len(data["items"].([]any)) != 1 {
		t.Fatalf("filter revisi salah: %v", data["items"])
	}
	item = data["items"].([]any)[0].(map[string]any)
	if item["actor_nama"] != "Rina" {
		t.Errorf("aktor revisi tidak terbaca: %v", item)
	}
	afterRevisi := item["after"].(map[string]any)
	if afterRevisi["alasan"] != "Harga naik dari pusat" || afterRevisi["delta_total"] != float64(25000) {
		t.Errorf("alasan/delta revisi tidak utuh: %v", afterRevisi)
	}
	beforeRevisi := item["before"].(map[string]any)
	if beforeRevisi["versi"] != float64(1) {
		t.Errorf("snapshot versi lama revisi hilang: %v", beforeRevisi)
	}

	// Filter tak dikenal ditolak 400, bukan diam-diam mengosongkan hasil.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/audit-logs?entitas=pesanan", "", sa, "")
	if status != http.StatusBadRequest {
		t.Errorf("entitas tak dikenal harus 400, dapat %d (%v)", status, body)
	}

	// Manager ditolak pada data nyata (bukan hanya di uji in-memory).
	mgr, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/audit-logs", "", mgr, "")
	if status != http.StatusForbidden {
		t.Errorf("Manager harus 403, dapat %d (%v)", status, body)
	}
}
