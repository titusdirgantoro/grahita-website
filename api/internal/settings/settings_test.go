package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// fakeRepo adalah repository pengaturan in-memory yang merekam audit.
type fakeRepo struct {
	mu      sync.Mutex
	values  map[string]string
	audit   []audit.Entry
	saveErr error
}

func newFakeRepo(values map[string]string) *fakeRepo {
	if values == nil {
		values = map[string]string{}
	}
	return &fakeRepo{values: values}
}

func (r *fakeRepo) List(context.Context) ([]Value, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Value, 0, len(r.values))
	for k, v := range r.values {
		out = append(out, Value{Key: k, Value: []byte(v)})
	}
	return out, nil
}

func (r *fakeRepo) Get(_ context.Context, key string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(v), nil
}

func (r *fakeRepo) Save(_ context.Context, _ string, changes []Change, recs []audit.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	for _, c := range changes {
		r.values[c.Key] = string(c.Value)
	}
	r.audit = append(r.audit, recs...)
	return nil
}

func superadmin() auth.Actor {
	return auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
}

func manager() auth.Actor {
	return auth.Actor{UserID: "adm-2", Kind: auth.KindAdmin, Role: auth.RoleManager}
}

func TestDefaultsSesuaiPRD(t *testing.T) {
	want := map[string]string{
		BiayaPackaging:     "3000",
		CNCHari:            "7",
		TokenHari:          "14",
		ResetMenit:         "60",
		MaxMB:              "5",
		MaxFile:            "5",
		DigitInvoice:       "2",
		ManagerBolehRevisi: "true",
	}
	for key, raw := range want {
		got, err := Default(key)
		if err != nil {
			t.Fatalf("Default(%s): %v", key, err)
		}
		if string(got) != raw {
			t.Errorf("default %s = %s, mau %s", key, got, raw)
		}
	}
	ai, err := Default(AI)
	if err != nil {
		t.Fatalf("Default(ai): %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(ai, &obj); err != nil {
		t.Fatalf("default ai bukan objek JSON: %v", err)
	}
	for _, k := range []string{"aktif", "min_keyakinan", "tol_kurang", "tol_lebih", "digit_rek", "mundur"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("default ai kehilangan field %s", k)
		}
	}
	if _, err := Default("kunci_karangan"); !errors.Is(err, ErrNotFound) {
		t.Errorf("kunci tak dikenal harus ErrNotFound, dapat %v", err)
	}
}

func TestCheckValidasiTipeDanRentang(t *testing.T) {
	kasus := []struct {
		nama string
		key  string
		raw  string
		mau  string // substring pesan; "" = valid
		norm string // bentuk kanonik bila valid
	}{
		{"packaging bulat", BiayaPackaging, "3000", "", "3000"},
		{"packaging nol boleh", BiayaPackaging, "0", "", "0"},
		{"packaging negatif ditolak", BiayaPackaging, "-1", "antara", ""},
		{"packaging pecahan ditolak", BiayaPackaging, "3000.5", "bilangan bulat", ""},
		{"packaging terlalu besar ditolak", BiayaPackaging, "100000001", "antara", ""},
		{"cnc nol ditolak", CNCHari, "0", "antara", ""},
		{"cnc teks ditolak", CNCHari, `"tujuh"`, "angka", ""},
		{"token hari oke", TokenHari, "30", "", "30"},
		{"reset menit oke", ResetMenit, "120", "", "120"},
		{"max mb nol ditolak", MaxMB, "0", "antara", ""},
		{"max file oke", MaxFile, "3", "", "3"},
		{"digit invoice nol ditolak", DigitInvoice, "0", "antara", ""},
		{"manager revisi bool", ManagerBolehRevisi, "false", "", "false"},
		{"manager revisi teks ditolak", ManagerBolehRevisi, `"ya"`, "true atau false", ""},
		{"kunci tak dikenal", "kunci_karangan", "1", "dikenal", ""},
		{"kunci kosong", "", "1", "dikenal", ""},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			got, msg := Check(k.key, json.RawMessage(k.raw))
			if k.mau == "" {
				if msg != "" {
					t.Fatalf("harus valid, dapat pesan %q", msg)
				}
				if string(got) != k.norm {
					t.Fatalf("normalisasi = %s, mau %s", got, k.norm)
				}
				return
			}
			if msg == "" {
				t.Fatalf("harus ditolak, ternyata valid (%s)", got)
			}
			if !strings.Contains(msg, k.mau) {
				t.Fatalf("pesan %q tidak memuat %q", msg, k.mau)
			}
		})
	}
}

func TestCheckAI(t *testing.T) {
	valid := `{"aktif":true,"min_keyakinan":0.75,"tol_kurang":500,"tol_lebih":100,"digit_rek":5,"mundur":2}`
	got, msg := Check(AI, json.RawMessage(valid))
	if msg != "" {
		t.Fatalf("ai valid ditolak: %s", msg)
	}
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("hasil ai bukan JSON: %v", err)
	}
	if obj["aktif"] != true || obj["min_keyakinan"] != 0.75 || obj["digit_rek"] != float64(5) {
		t.Errorf("nilai ai tidak sesuai: %v", obj)
	}

	// Objek parsial dilengkapi nilai bawaan sehingga tersimpan selalu lengkap.
	got, msg = Check(AI, json.RawMessage(`{"aktif":true}`))
	if msg != "" {
		t.Fatalf("ai parsial ditolak: %s", msg)
	}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("hasil ai parsial bukan JSON: %v", err)
	}
	if obj["aktif"] != true || obj["min_keyakinan"] != 0.9 || obj["digit_rek"] != float64(4) {
		t.Errorf("ai parsial tidak dilengkapi default: %v", obj)
	}

	buruk := []struct{ nama, raw, mau string }{
		{"bukan objek", `"ai"`, "objek"},
		{"field tak dikenal", `{"aktif":true,"karangan":1}`, "tidak dikenal"},
		{"keyakinan di luar 0..1", `{"min_keyakinan":1.5}`, "antara"},
		{"keyakinan negatif", `{"min_keyakinan":-0.1}`, "antara"},
		{"digit rek nol", `{"digit_rek":0}`, "antara"},
		{"mundur negatif", `{"mundur":-1}`, "antara"},
		{"aktif bukan bool", `{"aktif":"ya"}`, "true atau false"},
	}
	for _, k := range buruk {
		t.Run(k.nama, func(t *testing.T) {
			if _, msg := Check(AI, json.RawMessage(k.raw)); !strings.Contains(msg, k.mau) {
				t.Fatalf("pesan %q tidak memuat %q", msg, k.mau)
			}
		})
	}
}

func TestUpdateHanyaSuperadmin(t *testing.T) {
	svc := NewService(newFakeRepo(nil))
	err := svc.Update(context.Background(), manager(),
		map[string]json.RawMessage{BiayaPackaging: json.RawMessage("4000")})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("manager harus ditolak, dapat %v", err)
	}
	if _, err := svc.All(context.Background(), manager()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manager tidak boleh membaca All, dapat %v", err)
	}
}

func TestUpdateAllOrNothingDanAudit(t *testing.T) {
	repo := newFakeRepo(map[string]string{BiayaPackaging: "3000"})
	svc := NewService(repo)
	ctx := context.Background()

	// Satu nilai tidak valid -> tidak ada yang tersimpan, tidak ada audit.
	err := svc.Update(ctx, superadmin(), map[string]json.RawMessage{
		CNCHari:        json.RawMessage("10"),
		BiayaPackaging: json.RawMessage("-5"),
	})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("harus ValidationError, dapat %v", err)
	}
	if ve.Fields[BiayaPackaging] == "" {
		t.Errorf("pesan field biaya_packaging hilang: %v", ve.Fields)
	}
	if _, ok := repo.values[CNCHari]; ok {
		t.Error("nilai valid tidak boleh tersimpan saat ada yang tidak valid")
	}
	if len(repo.audit) != 0 {
		t.Errorf("tidak boleh ada audit saat validasi gagal, dapat %d", len(repo.audit))
	}

	// Semua valid -> tersimpan sekaligus, satu audit per kunci.
	err = svc.Update(ctx, superadmin(), map[string]json.RawMessage{
		BiayaPackaging: json.RawMessage("4500"),
		CNCHari:        json.RawMessage("10"),
	})
	if err != nil {
		t.Fatalf("update valid: %v", err)
	}
	if repo.values[BiayaPackaging] != "4500" || repo.values[CNCHari] != "10" {
		t.Errorf("nilai tidak tersimpan: %v", repo.values)
	}
	if len(repo.audit) != 2 {
		t.Fatalf("harus 2 entri audit, dapat %d", len(repo.audit))
	}
	for _, rec := range repo.audit {
		if rec.Entity != audit.EntitySetting || rec.Action != audit.ActionUpdate {
			t.Errorf("entri audit tidak sesuai: %+v", rec)
		}
		if rec.ActorID != "adm-1" {
			t.Errorf("aktor audit salah: %s", rec.ActorID)
		}
	}
	// Before mencatat nilai lama, after mencatat nilai baru.
	for _, rec := range repo.audit {
		b, _ := json.Marshal(rec.Before)
		a, _ := json.Marshal(rec.After)
		if !strings.Contains(string(b), "3000") && !strings.Contains(string(b), "7") {
			t.Errorf("before audit tidak memuat nilai lama: %s", b)
		}
		if !strings.Contains(string(a), "4500") && !strings.Contains(string(a), "10") {
			t.Errorf("after audit tidak memuat nilai baru: %s", a)
		}
	}
}

func TestUpdateKosongDitolak(t *testing.T) {
	svc := NewService(newFakeRepo(nil))
	err := svc.Update(context.Background(), superadmin(), nil)
	if !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("update kosong harus ErrInvalidValue, dapat %v", err)
	}
}

func TestAllMengembalikanSemuaKunci(t *testing.T) {
	repo := newFakeRepo(map[string]string{BiayaPackaging: "5000"})
	svc := NewService(repo)
	ctx := context.Background()

	all, err := svc.All(ctx, superadmin())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != len(KnownKeys()) {
		t.Errorf("All harus memuat seluruh kunci (default+lengkap), dapat %d dari %d", len(all), len(KnownKeys()))
	}
	if string(all[BiayaPackaging]) != "5000" {
		t.Errorf("nilai tersimpan harus menang atas default: %s", all[BiayaPackaging])
	}
	if _, ok := all[AI]; !ok {
		t.Error("All harus memuat ai")
	}
	// Tidak ada permukaan pembacaan lintas-role: hanya All yang boleh membaca.
	if _, err := svc.All(ctx, manager()); !errors.Is(err, ErrForbidden) {
		t.Errorf("manager tidak boleh membaca All, dapat %v", err)
	}
}

// TestUpdateHanyaMencatatKunciYangBerubah mengunci janji "satu entri audit per
// kunci yang BERUBAH": nilai yang sama (kanonik) tidak menulis baris maupun
// event audit, batch campuran hanya mencatat kunci yang berubah, dan kegagalan
// simpan tidak meninggalkan audit.
func TestUpdateHanyaMencatatKunciYangBerubah(t *testing.T) {
	repo := newFakeRepo(map[string]string{BiayaPackaging: "3000"})
	svc := NewService(repo)
	ctx := context.Background()

	// Nilai sama dengan yang tersimpan → no-op.
	if err := svc.Update(ctx, superadmin(), map[string]json.RawMessage{
		BiayaPackaging: json.RawMessage("3000"),
	}); err != nil {
		t.Fatalf("update nilai sama: %v", err)
	}
	if len(repo.audit) != 0 {
		t.Errorf("nilai sama tidak boleh menambah audit, dapat %d", len(repo.audit))
	}

	// Nilai bawaan yang belum pernah tersimpan juga no-op walau dikirim
	// dengan penulisan berbeda (objek ai: urutan kunci tidak sama).
	bawaan, err := Default(AI)
	if err != nil {
		t.Fatalf("Default(ai): %v", err)
	}
	if err := svc.Update(ctx, superadmin(), map[string]json.RawMessage{AI: bawaan}); err != nil {
		t.Fatalf("update nilai bawaan: %v", err)
	}
	if len(repo.audit) != 0 {
		t.Errorf("nilai bawaan yang sama tidak boleh menambah audit, dapat %d", len(repo.audit))
	}
	if _, ok := repo.values[AI]; ok {
		t.Error("kunci yang tidak berubah tidak perlu ditulis ke database")
	}

	// Batch campuran: hanya kunci yang berubah yang ditulis & dicatat.
	if err := svc.Update(ctx, superadmin(), map[string]json.RawMessage{
		BiayaPackaging: json.RawMessage("4500"), // berubah dari 3000
		CNCHari:        json.RawMessage("7"),    // sama dengan default
	}); err != nil {
		t.Fatalf("batch campuran: %v", err)
	}
	if len(repo.audit) != 1 {
		t.Fatalf("hanya kunci yang berubah dicatat, dapat %d", len(repo.audit))
	}
	if repo.values[BiayaPackaging] != "4500" {
		t.Errorf("nilai berubah harus tersimpan: %v", repo.values)
	}
	if _, ok := repo.values[CNCHari]; ok {
		t.Error("kunci yang tidak berubah tidak boleh ditulis")
	}
	b, _ := json.Marshal(repo.audit[0].Before)
	a, _ := json.Marshal(repo.audit[0].After)
	if !strings.Contains(string(b), "3000") || !strings.Contains(string(a), "4500") {
		t.Errorf("snapshot audit harus 3000 → 4500: %s → %s", b, a)
	}

	// Kegagalan simpan → tidak ada perubahan nilai maupun audit tambahan.
	repo.saveErr = errors.New("gagal simpan")
	if err := svc.Update(ctx, superadmin(), map[string]json.RawMessage{
		BiayaPackaging: json.RawMessage("9000"),
	}); err == nil {
		t.Fatal("kegagalan simpan harus dikembalikan")
	}
	if len(repo.audit) != 1 {
		t.Errorf("rollback tidak boleh menambah audit, dapat %d", len(repo.audit))
	}
	if repo.values[BiayaPackaging] != "4500" {
		t.Errorf("nilai tidak boleh berubah saat simpan gagal: %q", repo.values[BiayaPackaging])
	}
}

func TestGetKunciTidakDikenal(t *testing.T) {
	svc := NewService(newFakeRepo(nil))
	if _, err := svc.Get(context.Background(), "karangan"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get kunci tak dikenal harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.Get(context.Background(), BiayaPackaging); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get belum tersimpan harus ErrNotFound, dapat %v", err)
	}
}
