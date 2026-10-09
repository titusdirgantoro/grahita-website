package order

// Uji unit revisi invoice (kartu F2-25): aturan yang bisa diputuskan tanpa
// database — otorisasi (capability + setelan manager_boleh_revisi), alasan
// wajib, bentuk daftar baris, dan susunan kalimat timeline/audit.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/settings"
)

// revConfig adalah sudut pandang pengaturan untuk pengujian revisi.
type revConfig struct {
	bools map[string]bool
	ints  map[string]int64
	errB  error
	errI  error
}

func (c *revConfig) Bool(_ context.Context, key string) (bool, error) {
	if c.errB != nil {
		return false, c.errB
	}
	return c.bools[key], nil
}

func (c *revConfig) Int(_ context.Context, key string) (int64, error) {
	if c.errI != nil {
		return 0, c.errI
	}
	return c.ints[key], nil
}

// revRepo menangkap permintaan yang diteruskan service ke repository.
type revRepo struct {
	dipanggil int
	req       ReviseRequest
	out       ReviseOutcome
	err       error
}

func (r *revRepo) Revise(_ context.Context, req ReviseRequest) (ReviseOutcome, error) {
	r.dipanggil++
	r.req = req
	return r.out, r.err
}

func cfgRevisi(boleh bool) *revConfig {
	return &revConfig{
		bools: map[string]bool{settings.ManagerBolehRevisi: boleh},
		ints:  map[string]int64{settings.BiayaPackaging: 3000},
	}
}

func aktorSA() auth.Actor {
	return auth.Actor{UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
}

func aktorMgr() auth.Actor {
	return auth.Actor{UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
}

func aktorRevCabang() auth.Actor {
	return aktorCabang("br-1")
}

const idPesanan = "018f0000-0000-7000-8000-000000000001"

func barisFinal(qty int, harga int64) []ReviseLineInput {
	return []ReviseLineInput{{LineNo: 1, Qty: qty, HargaSatuan: harga}}
}

func TestReviseAlasanWajib(t *testing.T) {
	repo := &revRepo{}
	svc := NewRevisionService(repo, cfgRevisi(true))
	_, err := svc.Revise(context.Background(), aktorSA(), idPesanan, ReviseInput{Lines: barisFinal(1, 1000)})
	if !errors.Is(err, ErrAlasanWajib) {
		t.Fatalf("alasan kosong harus ditolak ErrAlasanWajib, dapat %v", err)
	}
	if repo.dipanggil != 0 {
		t.Error("repository tidak boleh dipanggil saat alasan kosong")
	}
}

func TestReviseAlasanTerlaluPanjang(t *testing.T) {
	repo := &revRepo{}
	svc := NewRevisionService(repo, cfgRevisi(true))
	_, err := svc.Revise(context.Background(), aktorSA(), idPesanan,
		ReviseInput{Alasan: strings.Repeat("a", MaxAlasanRevisi+1), Lines: barisFinal(1, 1000)})
	if !errors.Is(err, ErrInvalidData) {
		t.Fatalf("alasan terlalu panjang harus ErrInvalidData, dapat %v", err)
	}
}

func TestReviseMenolakAktorBukanAdmin(t *testing.T) {
	repo := &revRepo{}
	svc := NewRevisionService(repo, cfgRevisi(true))
	_, err := svc.Revise(context.Background(), aktorRevCabang(), idPesanan,
		ReviseInput{Alasan: "salah harga", Lines: barisFinal(1, 1000)})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("akun cabang harus 403, dapat %v", err)
	}
}

func TestReviseManagerDitolakSaatToggleMati(t *testing.T) {
	repo := &revRepo{}
	svc := NewRevisionService(repo, cfgRevisi(false))
	_, err := svc.Revise(context.Background(), aktorMgr(), idPesanan,
		ReviseInput{Alasan: "salah harga", Lines: barisFinal(1, 1000)})
	if !errors.Is(err, ErrRevisiTidakDiizinkan) {
		t.Fatalf("Manager saat toggle mati harus ditolak, dapat %v", err)
	}
	if repo.dipanggil != 0 {
		t.Error("repository tidak boleh dipanggil saat Manager ditolak")
	}
}

func TestReviseManagerBolehSaatToggleHidup(t *testing.T) {
	repo := &revRepo{out: ReviseOutcome{OrderID: idPesanan, Versi: 1}}
	svc := NewRevisionService(repo, cfgRevisi(true))
	out, err := svc.Revise(context.Background(), aktorMgr(), idPesanan,
		ReviseInput{Alasan: "salah harga", Lines: barisFinal(2, 5000)})
	if err != nil {
		t.Fatalf("Manager saat toggle hidup harus boleh: %v", err)
	}
	if repo.dipanggil != 1 || out.Versi != 1 {
		t.Fatalf("revisi harus diteruskan ke repository: %+v", out)
	}
	if repo.req.Alasan != "salah harga" || repo.req.BiayaPackaging != 3000 {
		t.Errorf("nilai setelan/alasan harus diteruskan apa adanya: %+v", repo.req)
	}
}

func TestReviseSuperadminMengabaikanToggle(t *testing.T) {
	repo := &revRepo{out: ReviseOutcome{OrderID: idPesanan, Versi: 1}}
	cfg := cfgRevisi(false)
	cfg.errB = errors.New("setelan tidak terbaca")
	svc := NewRevisionService(repo, cfg)
	if _, err := svc.Revise(context.Background(), aktorSA(), idPesanan,
		ReviseInput{Alasan: "koreksi alamat", Lines: barisFinal(1, 100)}); err != nil {
		t.Fatalf("Superadmin tidak boleh bergantung setelan: %v", err)
	}
	if repo.dipanggil != 1 {
		t.Error("Superadmin harus diteruskan ke repository")
	}
}

func TestReviseIdBukanUUID(t *testing.T) {
	repo := &revRepo{}
	svc := NewRevisionService(repo, cfgRevisi(true))
	_, err := svc.Revise(context.Background(), aktorSA(), "bukan-uuid",
		ReviseInput{Alasan: "x", Lines: barisFinal(1, 100)})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("id bukan UUID harus ErrNotFound, dapat %v", err)
	}
}

func TestReviseValidasiBaris(t *testing.T) {
	kasus := []struct {
		nama  string
		lines []ReviseLineInput
		mau   error
	}{
		{"tanpa baris", nil, ErrEmptyLines},
		{"qty nol", []ReviseLineInput{{LineNo: 1, Qty: 0, HargaSatuan: 1}}, ErrInvalidData},
		{"harga negatif", []ReviseLineInput{{LineNo: 1, Qty: 1, HargaSatuan: -1}}, ErrInvalidData},
		{"baris lama ganda", []ReviseLineInput{{LineNo: 1, Qty: 1, HargaSatuan: 1}, {LineNo: 1, Qty: 2, HargaSatuan: 1}}, ErrInvalidData},
		{"baris baru tanpa produk", []ReviseLineInput{{Qty: 1, HargaSatuan: 1}}, ErrInvalidData},
		{"baris baru product_id bukan UUID", []ReviseLineInput{{ProductID: "x", Qty: 1, HargaSatuan: 1}}, ErrInvalidData},
		{"line_no negatif", []ReviseLineInput{{LineNo: -1, Qty: 1, HargaSatuan: 1}}, ErrInvalidData},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			repo := &revRepo{}
			svc := NewRevisionService(repo, cfgRevisi(true))
			_, err := svc.Revise(context.Background(), aktorSA(), idPesanan,
				ReviseInput{Alasan: "sebab", Lines: k.lines})
			if !errors.Is(err, k.mau) {
				t.Fatalf("harus %v, dapat %v", k.mau, err)
			}
			if repo.dipanggil != 0 {
				t.Error("baris tidak valid tidak boleh sampai ke repository")
			}
		})
	}
}

func TestReviseBarisBaruWajibProductID(t *testing.T) {
	repo := &revRepo{out: ReviseOutcome{Versi: 1}}
	svc := NewRevisionService(repo, cfgRevisi(true))
	// Dua baris baru ber-product_id valid + satu baris lama.
	lines := []ReviseLineInput{
		{LineNo: 3, Qty: 1, HargaSatuan: 1000},
		{ProductID: "018f0000-0000-7000-8000-0000000000aa", Qty: 2, HargaSatuan: 500},
	}
	if _, err := svc.Revise(context.Background(), aktorSA(), idPesanan,
		ReviseInput{Alasan: "tambah baris", Lines: lines}); err != nil {
		t.Fatalf("baris final sah harus diterima: %v", err)
	}
	if len(repo.req.Lines) != 2 || repo.req.Lines[1].ProductID == "" {
		t.Errorf("baris final harus diteruskan apa adanya: %+v", repo.req.Lines)
	}
}

func TestReviseSetelanTidakTerbacaGagalTerang(t *testing.T) {
	repo := &revRepo{}
	cfg := cfgRevisi(true)
	cfg.errI = errors.New("database mati")
	svc := NewRevisionService(repo, cfg)
	if _, err := svc.Revise(context.Background(), aktorMgr(), idPesanan,
		ReviseInput{Alasan: "x", Lines: barisFinal(1, 1)}); err == nil {
		t.Fatal("kegagalan membaca setelan harus terlihat, bukan diam")
	}
}

func TestTeksEventRevisi(t *testing.T) {
	teks := TeksEventRevisi("  Rina ", "  salah harga  ")
	if !strings.Contains(teks, "Rina") || !strings.Contains(teks, "salah harga") {
		t.Errorf("kalimat timeline harus memuat aktor + alasan: %q", teks)
	}
	if empty := TeksEventRevisi("", "x"); !strings.Contains(empty, "admin Pusat") {
		t.Errorf("aktor tanpa nama harus memakai sebutan netral: %q", empty)
	}
}
