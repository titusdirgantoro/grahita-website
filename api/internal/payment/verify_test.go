package payment

// Uji unit verifikasi pembayaran (kartu F1-18): aturan bentuk yang diputuskan
// tanpa database — hasil/alasan, nominal, catatan, aktor, dan bentuk id —
// beserta permintaan yang benar-benar diserahkan ke penyimpanan (termasuk entri
// audit yang menyertainya).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// verifierPalsu menangkap permintaan yang lolos validasi.
type verifierPalsu struct {
	dipanggil int
	req       MarkVerifiedRequest
	hasil     VerifyOutcome
	err       error
}

func (v *verifierPalsu) MarkVerified(_ context.Context, req MarkVerifiedRequest) (VerifyOutcome, error) {
	v.dipanggil++
	v.req = req
	return v.hasil, v.err
}

func adminSah() auth.Actor {
	return auth.Actor{UserID: "11111111-1111-4111-8111-111111111111", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
}

const orderUUID = "0199a1b2-0000-7000-8000-000000000001"

func TestMarkVerifiedMenolakAktorYangTidakBerhak(t *testing.T) {
	repo := &verifierPalsu{}
	svc := NewVerificationService(repo)

	kasus := map[string]auth.Actor{
		"cabang":    {UserID: "22222222-2222-4222-8222-222222222222", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "b"},
		"anonim":    auth.Anonymous(),
		"role lain": {UserID: "33333333-3333-4333-8333-333333333333", Kind: auth.KindAdmin, Role: auth.Role("bendahara"), Nama: "X"},
	}
	for nama, actor := range kasus {
		_, err := svc.MarkVerified(context.Background(), actor, orderUUID, VerifyInput{Hasil: VerifCocok})
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: harus ErrForbidden, dapat %v", nama, err)
		}
	}
	if repo.dipanggil != 0 {
		t.Errorf("penyimpanan tidak boleh disentuh saat otorisasi gagal, dipanggil %d kali", repo.dipanggil)
	}
}

func TestMarkVerifiedMenolakIdBukanUUIDSebagaiNotFound(t *testing.T) {
	repo := &verifierPalsu{}
	svc := NewVerificationService(repo)
	for _, id := range []string{"", "  ", "abc", "1", orderUUID + "-x"} {
		if _, err := svc.MarkVerified(context.Background(), adminSah(), id, VerifyInput{Hasil: VerifCocok}); !errors.Is(err, ErrNotFound) {
			t.Errorf("id %q: harus ErrNotFound, dapat %v", id, err)
		}
	}
	if repo.dipanggil != 0 {
		t.Errorf("penyimpanan tidak boleh disentuh untuk id bukan UUID")
	}
}

func TestMarkVerifiedAturanHasilDanAlasan(t *testing.T) {
	kasus := []struct {
		nama   string
		in     VerifyInput
		mauErr error
	}{
		{"hasil kosong", VerifyInput{}, ErrHasilInvalid},
		{"hasil tak dikenal", VerifyInput{Hasil: "selesai"}, ErrHasilInvalid},
		{"review tanpa alasan", VerifyInput{Hasil: VerifPerluReview}, ErrAlasanInvalid},
		{"review alasan tak dikenal", VerifyInput{Hasil: VerifPerluReview, Alasan: "entahlah"}, ErrAlasanInvalid},
		{"setuju membawa alasan", VerifyInput{Hasil: VerifCocok, Alasan: AlasanKurangBayar}, ErrAlasanInvalid},
		{"nominal negatif", VerifyInput{Hasil: VerifCocok, TotalTerbaca: ptrInt64(-1)}, ErrNominalInvalid},
		{"catatan terlalu panjang", VerifyInput{Hasil: VerifCocok, Catatan: strings.Repeat("a", MaxCatatanVerifikasi+1)}, ErrCatatanInvalid},
		{"metode tak dikenal", VerifyInput{Hasil: VerifCocok, Metode: "kira-kira"}, ErrMetodeInvalid},
	}
	for _, k := range kasus {
		repo := &verifierPalsu{}
		svc := NewVerificationService(repo)
		_, err := svc.MarkVerified(context.Background(), adminSah(), orderUUID, k.in)
		if !errors.Is(err, k.mauErr) {
			t.Errorf("%s: harus %v, dapat %v", k.nama, k.mauErr, err)
		}
		if repo.dipanggil != 0 {
			t.Errorf("%s: penyimpanan tidak boleh disentuh saat data tidak sah", k.nama)
		}
	}
}

func TestMarkVerifiedMenyusunPermintaanDanAudit(t *testing.T) {
	repo := &verifierPalsu{}
	svc := NewVerificationService(repo)
	nominal := int64(250000)

	ctx := audit.WithRequestMeta(context.Background(), audit.RequestMeta{IP: "10.1.2.3"})
	_, err := svc.MarkVerified(ctx, adminSah(), "  "+orderUUID+"  ", VerifyInput{
		Hasil:        VerifCocok,
		TotalTerbaca: &nominal,
		Catatan:      "  sudah dicek  ",
	})
	if err != nil {
		t.Fatalf("verifikasi sah harus berhasil: %v", err)
	}
	if repo.dipanggil != 1 {
		t.Fatalf("penyimpanan harus dipanggil sekali, dapat %d", repo.dipanggil)
	}
	req := repo.req
	if req.OrderID != orderUUID {
		t.Errorf("id pesanan harus dirapikan, dapat %q", req.OrderID)
	}
	if req.Metode != MetodeManual {
		t.Errorf("metode bawaan harus %q, dapat %q", MetodeManual, req.Metode)
	}
	if req.Catatan != "sudah dicek" {
		t.Errorf("catatan harus dirapikan, dapat %q", req.Catatan)
	}
	if req.TotalTerbaca == nil || *req.TotalTerbaca != nominal {
		t.Errorf("nominal terbaca harus diteruskan apa adanya, dapat %v", req.TotalTerbaca)
	}
	if req.Audit.Entity != audit.EntityOrder || req.Audit.Action != audit.ActionVerify {
		t.Errorf("audit harus entity=%q action=%q, dapat %q/%q",
			audit.EntityOrder, audit.ActionVerify, req.Audit.Entity, req.Audit.Action)
	}
	if req.Audit.EntityID != orderUUID {
		t.Errorf("audit harus menunjuk pesanannya, dapat %q", req.Audit.EntityID)
	}
	if req.Audit.ActorID != adminSah().UserID || req.Audit.IP != "10.1.2.3" {
		t.Errorf("audit harus mencatat pelaku + IP: %+v", req.Audit)
	}
}

func TestMarkVerifiedReviewDanMetodeEksplisit(t *testing.T) {
	repo := &verifierPalsu{}
	svc := NewVerificationService(repo)
	if _, err := svc.MarkVerified(context.Background(), adminSah(), orderUUID, VerifyInput{
		Hasil:  VerifPerluReview,
		Alasan: AlasanRekeningBeda,
		Metode: MetodeAI,
	}); err != nil {
		t.Fatalf("review dengan alasan sah harus berhasil: %v", err)
	}
	if repo.req.Alasan != AlasanRekeningBeda || repo.req.Metode != MetodeAI {
		t.Errorf("alasan/metode harus diteruskan, dapat %q/%q", repo.req.Alasan, repo.req.Metode)
	}
}

func TestMarkVerifiedMeneruskanErrorPenyimpanan(t *testing.T) {
	repo := &verifierPalsu{err: ErrAlreadyDone}
	svc := NewVerificationService(repo)
	if _, err := svc.MarkVerified(context.Background(), adminSah(), orderUUID, VerifyInput{Hasil: VerifCocok}); !errors.Is(err, ErrAlreadyDone) {
		t.Errorf("error penyimpanan harus diteruskan apa adanya, dapat %v", err)
	}
}

func TestMarkVerifiedTanpaPenyimpananGagalTerangTerangan(t *testing.T) {
	svc := NewVerificationService(nil)
	if _, err := svc.MarkVerified(context.Background(), adminSah(), orderUUID, VerifyInput{Hasil: VerifCocok}); !errors.Is(err, ErrVerifierUnavailable) {
		t.Errorf("penyimpan yang belum terpasang harus gagal jelas, dapat %v", err)
	}
}

func TestValidasiAlasanMenutupSeluruhPilihan(t *testing.T) {
	for _, a := range []string{AlasanKurangBayar, AlasanLebihBayar, AlasanTidakTerbaca, AlasanRekeningBeda, AlasanLainnya} {
		if err := ValidasiAlasan(VerifPerluReview, a); err != nil {
			t.Errorf("alasan %q harus sah untuk perlu review: %v", a, err)
		}
		if !AlasanValid(a) {
			t.Errorf("alasan %q harus dikenal", a)
		}
	}
	if AlasanValid("") || AlasanValid("KURANG_BAYAR") {
		t.Error("alasan kosong/huruf besar tidak boleh dianggap sah")
	}
}

func TestLabelAlasanDanTeksTimeline(t *testing.T) {
	if got := LabelAlasan(AlasanTidakTerbaca); got != "bukti tidak terbaca" {
		t.Errorf("label tidak terbaca: %q", got)
	}
	if got := LabelAlasan(AlasanKurangBayar); got != "kurang bayar" {
		t.Errorf("label kurang bayar: %q", got)
	}
	setuju := TeksEventSetuju("Budi")
	if !strings.Contains(setuju, "Budi") || !strings.Contains(setuju, "admin Pusat") {
		t.Errorf("teks persetujuan harus memuat pelaku: %q", setuju)
	}
	review := TeksEventPerluReview("Budi", AlasanLebihBayar)
	if !strings.Contains(review, "lebih bayar") || !strings.Contains(review, "Budi") {
		t.Errorf("teks review harus memuat alasan + pelaku: %q", review)
	}
	if got := TeksEventSetuju("  "); !strings.Contains(got, "admin Pusat") {
		t.Errorf("nama kosong harus memakai sebutan netral: %q", got)
	}
}

func TestMetodeValid(t *testing.T) {
	for _, m := range []string{MetodeManual, MetodeAI, MetodeVirtualAccount} {
		if !MetodeValid(m) {
			t.Errorf("metode %q harus sah", m)
		}
	}
	for _, m := range []string{"", "MANUAL", "otomatis"} {
		if MetodeValid(m) {
			t.Errorf("metode %q tidak boleh sah", m)
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }
