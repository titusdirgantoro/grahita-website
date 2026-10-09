package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// ---------------------------------------------------------------------------
// Redaksi snapshot (kartu F2-28)
// ---------------------------------------------------------------------------

func TestRedactMenyembunyikanKunciSensitif(t *testing.T) {
	in := map[string]any{
		"nama":          "Budi",
		"password":      "rahasia-banget",
		"password_hash": "$argon2id$v=19$…",
		"kata_sandi":    "12345678",
		"token_hash":    "9f8a…",
		"token_prefix":  "GRH-7K4Q",
		"secret":        "s3cr3t",
		"api_key":       "key-123",
		"authorization": "Bearer abc",
		"url_bukti":     "https://storage.example/bukti?signature=abc",
		"tautan":        "https://admin.example/lupa?token=xyz",
		"storage_key":   "orders/1/proof.png",
		"is_active":     true,
		"nested": map[string]any{
			"csrf":  "tok",
			"benar": "nilai",
		},
		"daftar": []any{
			map[string]any{"cookie": "sid=1", "nomor": "INV-1"},
		},
		"total": json.Number("9007199254740993"),
	}

	out, ok := audit.Redact(in).(map[string]any)
	if !ok {
		t.Fatalf("Redact tidak mengembalikan map: %T", audit.Redact(in))
	}
	// Kunci sensitif → diganti penanda.
	for _, k := range []string{"password", "password_hash", "kata_sandi", "token_hash",
		"secret", "api_key", "authorization", "url_bukti", "tautan", "storage_key"} {
		if out[k] != audit.NilaiRedaksi {
			t.Errorf("kunci %q harus diredaksi, dapat %v", k, out[k])
		}
	}
	// Pengecualian yang disengaja + nilai biasa tetap utuh.
	if out["token_prefix"] != "GRH-7K4Q" {
		t.Errorf("token_prefix tidak boleh diredaksi, dapat %v", out["token_prefix"])
	}
	if out["nama"] != "Budi" || out["is_active"] != true {
		t.Errorf("nilai biasa ikut berubah: %v", out)
	}
	nested := out["nested"].(map[string]any)
	if nested["csrf"] != audit.NilaiRedaksi || nested["benar"] != "nilai" {
		t.Errorf("redaksi tidak menelusuri map bersarang: %v", nested)
	}
	daftar := out["daftar"].([]any)[0].(map[string]any)
	if daftar["cookie"] != audit.NilaiRedaksi || daftar["nomor"] != "INV-1" {
		t.Errorf("redaksi tidak menelusuri slice: %v", daftar)
	}
	// Presisi angka besar dipertahankan (json.Number, bukan float64).
	if out["total"] != json.Number("9007199254740993") {
		t.Errorf("angka besar kehilangan presisi: %v (%T)", out["total"], out["total"])
	}
	// Idempoten.
	lagi := audit.Redact(out).(map[string]any)
	if lagi["password"] != audit.NilaiRedaksi || lagi["token_prefix"] != "GRH-7K4Q" {
		t.Errorf("redaksi tidak idempoten: %v", lagi)
	}
}

func TestRedactNilDanSnapshotKosong(t *testing.T) {
	if got := audit.Redact(nil); got != nil {
		t.Errorf("Redact(nil) harus nil, dapat %v", got)
	}
	v, err := audit.Snapshot(nil)
	if err != nil || v != nil {
		t.Errorf("Snapshot(nil) harus (nil, nil), dapat (%v, %v)", v, err)
	}
	v, err = audit.Snapshot([]byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("Snapshot JSON valid: %v", err)
	}
	if v.(map[string]any)["a"] != json.Number("1") {
		t.Errorf("angka tidak dipertahankan sebagai json.Number: %v", v)
	}
	if _, err := audit.Snapshot([]byte(`{bukan json`)); err == nil {
		t.Error("Snapshot JSON rusak harus gagal")
	}
}

func TestRedactMempertahankanTipeBilaTidakAdaKunciSensitif(t *testing.T) {
	// Struct domain yang bersih harus dikembalikan apa adanya, supaya snapshot
	// produsen lama (mis. master cabang) tidak berubah bentuk.
	type cabang struct {
		Nama     string `json:"nama"`
		IsActive bool   `json:"is_active"`
	}
	in := cabang{Nama: "Grahita Cabang A", IsActive: true}
	out := audit.Redact(in)
	kembali, ok := out.(cabang)
	if !ok || kembali != in {
		t.Errorf("snapshot bersih harus utuh, dapat %#v (%T)", out, out)
	}

	// Ada kunci sensitif → bentuk generik dengan nilai diredaksi.
	out = audit.Redact(map[string]any{"nama": "x", "token": "GRH-RAHASIA"})
	m, ok := out.(map[string]any)
	if !ok || m["token"] != audit.NilaiRedaksi || m["nama"] != "x" {
		t.Errorf("snapshot sensitif tidak diredaksi: %#v", out)
	}
}

func TestNewEntryMeredaksiSnapshotSebelumDisimpan(t *testing.T) {
	ctx := audit.WithRequestMeta(context.Background(), audit.RequestMeta{IP: "203.0.113.7"})
	actor := auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
	e := audit.NewEntry(ctx, actor, audit.EntityBranch, "br-1", audit.ActionTokenGenerate,
		map[string]any{"token_prefix": "GRH-AAAA", "token": "GRH-AAAA-BBBB-CCCC-DDDD"},
		map[string]any{"bukti_url": "https://storage.example/x?sig=1", "ok": true})

	if e.ActorNama != "Rina" || e.IP != "203.0.113.7" || e.EntityID != "br-1" {
		t.Fatalf("metadata entri salah: %+v", e)
	}
	before := e.Before.(map[string]any)
	if before["token"] != audit.NilaiRedaksi || before["token_prefix"] != "GRH-AAAA" {
		t.Errorf("before tidak diredaksi dengan benar: %v", before)
	}
	after := e.After.(map[string]any)
	if after["bukti_url"] != audit.NilaiRedaksi || after["ok"] != true {
		t.Errorf("after tidak diredaksi dengan benar: %v", after)
	}
}

// ---------------------------------------------------------------------------
// Pembacaan + filter (kartu F2-28)
// ---------------------------------------------------------------------------

// fakeReader merekam Query yang diterima dan mengembalikan baris yang
// disiapkan pengujian.
type fakeReader struct {
	query    audit.Query
	queryDua audit.Query
	items    []audit.Log
	total    int64
	err      error
}

func (f *fakeReader) List(_ context.Context, q audit.Query) ([]audit.Log, error) {
	f.query = q
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f *fakeReader) Count(_ context.Context, q audit.Query) (int64, error) {
	f.queryDua = q
	if f.err != nil {
		return 0, f.err
	}
	return f.total, nil
}

func adminSA() auth.Actor {
	return auth.Actor{UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
}

func TestListDitolakUntukSelainSuperadmin(t *testing.T) {
	svc := audit.NewService(&fakeReader{})
	for _, a := range []auth.Actor{
		{UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"},
		{UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a", Nama: "Cabang A"},
		auth.Anonymous(),
	} {
		if _, err := svc.List(context.Background(), a, audit.Filter{}); !errors.Is(err, audit.ErrForbidden) {
			t.Errorf("aktor %s %s harus ErrForbidden, dapat %v", a.Kind, a.Role, err)
		}
	}
}

func TestListMenormalkanFilterDanPaginasi(t *testing.T) {
	repo := &fakeReader{
		total: 42,
		items: []audit.Log{{
			ID: "1", At: time.Now(), Entity: "branch", Action: "create",
			Before: map[string]any{"password": "x"},
			After:  map[string]any{"token": "t", "nama": "n"},
		}},
	}
	svc := audit.NewService(repo)

	page, err := svc.List(context.Background(), adminSA(), audit.Filter{
		Entitas:    []string{" Branch ", "order,branch"},
		Aksi:       " CREATE ",
		Aktor:      " 3F0C8F3E-1234-4A5B-8C9D-ABCDEFABCDEF ",
		Dari:       "2026-10-01",
		Sampai:     "2026-10-09",
		Halaman:    3,
		PerHalaman: 10,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := strings.Join(repo.query.Entitas, ","); got != "branch,order" {
		t.Errorf("entitas dinormalkan/diurut salah: %q", got)
	}
	if repo.query.Aksi != "create" {
		t.Errorf("aksi harus huruf kecil: %q", repo.query.Aksi)
	}
	if repo.query.ActorID != "3f0c8f3e-1234-4a5b-8c9d-abcdefabcdef" || repo.query.ActorNama != "" {
		t.Errorf("aktor UUID harus jadi ActorID: %+v", repo.query)
	}
	if repo.query.Offset != 20 || repo.query.Batch != 10 {
		t.Errorf("paginasi salah: offset=%d batch=%d", repo.query.Offset, repo.query.Batch)
	}
	if repo.query.Dari != "2026-10-01" || repo.query.Sampai != "2026-10-09" {
		t.Errorf("rentang tanggal salah: %q..%q", repo.query.Dari, repo.query.Sampai)
	}
	if page.Total != 42 || page.Halaman != 3 || page.PerHalaman != 10 || page.TotalHalaman != 5 {
		t.Errorf("halaman salah: %+v", page)
	}
	// Filter yang dipakai dikembalikan apa adanya supaya UI bisa menampilkannya.
	if page.Filter.Aktor != "3F0C8F3E-1234-4A5B-8C9D-ABCDEFABCDEF" || page.Filter.Dari != "2026-10-01" {
		t.Errorf("filter kembalian salah: %+v", page.Filter)
	}
	// Baris yang dibaca diredaksi ulang (pertahanan untuk data lama).
	before := page.Items[0].Before.(map[string]any)
	after := page.Items[0].After.(map[string]any)
	if before["password"] != audit.NilaiRedaksi || after["token"] != audit.NilaiRedaksi {
		t.Errorf("isi halaman tidak diredaksi: %v", page.Items[0])
	}
	if after["nama"] != "n" {
		t.Errorf("nilai biasa ikut hilang: %v", after)
	}
}

func TestListAktorNamaDanBatasBawaan(t *testing.T) {
	repo := &fakeReader{}
	svc := audit.NewService(repo)
	if _, err := svc.List(context.Background(), adminSA(), audit.Filter{Aktor: "Budi"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if repo.query.ActorNama != "Budi" || repo.query.ActorID != "" {
		t.Errorf("aktor non-UUID harus jadi pencarian nama: %+v", repo.query)
	}
	if repo.query.Batch != audit.DefaultPerHalaman || repo.query.Halaman != 1 || repo.query.Offset != 0 {
		t.Errorf("bawaan paginasi salah: %+v", repo.query)
	}
	// per_halaman dipagari; halaman < 1 menjadi 1.
	if _, err := svc.List(context.Background(), adminSA(), audit.Filter{Halaman: -4, PerHalaman: 100000}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if repo.query.Batch != audit.MaxPerHalaman || repo.query.Halaman != 1 {
		t.Errorf("pagar paginasi gagal: %+v", repo.query)
	}
}

func TestListMenolakFilterTidakValid(t *testing.T) {
	svc := audit.NewService(&fakeReader{})
	kasus := []struct {
		nama string
		f    audit.Filter
	}{
		{"entitas tak dikenal", audit.Filter{Entitas: []string{"pesanan"}}},
		{"aksi tak dikenal", audit.Filter{Aksi: "hapus_semua"}},
		{"tanggal salah format", audit.Filter{Dari: "01-10-2026"}},
		{"tanggal sampai salah format", audit.Filter{Sampai: "kemarin"}},
		{"rentang terbalik", audit.Filter{Dari: "2026-10-09", Sampai: "2026-10-01"}},
	}
	for _, k := range kasus {
		if _, err := svc.List(context.Background(), adminSA(), k.f); !errors.Is(err, audit.ErrFilterInvalid) {
			t.Errorf("%s: harus ErrFilterInvalid, dapat %v", k.nama, err)
		}
	}
}

func TestListMeneruskanGalatRepository(t *testing.T) {
	repo := &fakeReader{err: errors.New("db mati")}
	svc := audit.NewService(repo)
	if _, err := svc.List(context.Background(), adminSA(), audit.Filter{}); err == nil {
		t.Fatal("galat repository harus diteruskan")
	}
}

func TestDaftarEntitasDanAksiKonsisten(t *testing.T) {
	// Entitas/aksi yang benar-benar ditulis produsen harus bisa disaring.
	for _, e := range []string{audit.EntityAdminUser, audit.EntityBranchUser, audit.EntityTutorial} {
		ada := false
		for _, s := range audit.EntitasSah() {
			if s == e {
				ada = true
			}
		}
		if !ada {
			t.Errorf("entitas %q tidak ada di EntitasSah()", e)
		}
	}
	for _, a := range []string{audit.ActionCreate, audit.ActionUpdate, audit.ActionResend, audit.ActionReset} {
		ada := false
		for _, s := range audit.AksiSah() {
			if s == a {
				ada = true
			}
		}
		if !ada {
			t.Errorf("aksi %q tidak ada di AksiSah()", a)
		}
	}
}
