package payment

// Uji unit pembacaan bukti AI/OCR (kartu F2-26): aturan penilaian (PRD §5),
// penyensoran jawaban mentah, pembaca tiruan deterministik, serta orkestrasi
// layanan OCR mulai dari setelan `ai` sampai keputusan yang diserahkan ke
// `MarkVerified`. Penyimpanan diuji di berkas *_pg_test.go pada PostgreSQL
// nyata; di sini seluruh aturan diputuskan tanpa database.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/settings"
)

// ------------------------------------------------------------- tolok bantu --

const (
	proofUUID1 = "11111111-1111-4111-8111-111111111111"
	proofUUID2 = "22222222-2222-4222-8222-222222222222"
	bankUUID   = "33333333-3333-4333-8333-333333333333"
)

func aiAktif() settings.AISettings {
	return settings.AISettings{Aktif: true, MinKeyakinan: 0.90, TolKurang: 0, TolLebih: 0, DigitRek: 4, Mundur: 1}
}

func bankAktif() []bank.Account {
	return []bank.Account{{ID: bankUUID, Bank: "BCA", NamaPemilik: "PT Grahita", Nomor: "1234567890", IsActive: true}}
}

func tgl(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &t
}

// tv adalah tanggal sebagai nilai (bukan pointer) untuk argumen Nilai.
func tv(s string) time.Time { return *tgl(s) }

// rekeningPalsu adalah RekeningSource in-memory.
type rekeningPalsu struct{ akun []bank.Account }

func (r rekeningPalsu) Active(context.Context) ([]bank.Account, error) { return r.akun, nil }

func (r rekeningPalsu) EffectiveForBranch(context.Context, string) (bank.EffectiveSet, error) {
	return bank.EffectiveSet{Accounts: r.akun}, nil
}

// aiPalsu adalah AISettingsSource in-memory (dengan penghitung panggilan).
type aiPalsu struct {
	setelan settings.AISettings
	err     error
}

func (a aiPalsu) AISettings(context.Context) (settings.AISettings, error) {
	if a.err != nil {
		return settings.AISettings{}, a.err
	}
	return a.setelan, nil
}

// ------------------------------------------------------------- penilaian ----

func TestNilaiMenutupSeluruhAturanPRD(t *testing.T) {
	total := int64(300000)
	tanggalOrder := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	rules := aiAktif()

	bukti := func(nominal *int64, tanggal *time.Time, rek string, keyakinan float64) PembacaanBukti {
		return PembacaanBukti{ProofID: "p", Nominal: nominal, Tanggal: tanggal, Bank: "BCA", RekAkhir: rek, Confidence: keyakinan}
	}

	kasus := []struct {
		nama      string
		bukti     []PembacaanBukti
		mauHasil  string
		mauAlasan string
		mauTotal  int64
	}{
		{
			nama:     "cocok tepat",
			bukti:    []PembacaanBukti{bukti(ptrInt64(300000), tgl("2026-10-09"), "1234567890", 0.98)},
			mauHasil: VerifCocok,
			mauTotal: 300000,
		},
		{
			nama: "multi-bukti dijumlahkan",
			bukti: []PembacaanBukti{
				bukti(ptrInt64(100000), tgl("2026-10-09"), "1234567890", 0.97),
				bukti(ptrInt64(200000), tgl("2026-10-08"), "1234567890", 0.95),
			},
			mauHasil: VerifCocok,
			mauTotal: 300000,
		},
		{
			nama:      "bukti kabur (keyakinan di bawah ambang)",
			bukti:     []PembacaanBukti{bukti(ptrInt64(300000), tgl("2026-10-09"), "1234567890", 0.40)},
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanTidakTerbaca,
			mauTotal:  300000,
		},
		{
			nama:      "nominal tidak terbaca",
			bukti:     []PembacaanBukti{bukti(nil, tgl("2026-10-09"), "1234567890", 0.99)},
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanTidakTerbaca,
		},
		{
			nama:      "rekening tujuan tidak cocok",
			bukti:     []PembacaanBukti{bukti(ptrInt64(300000), tgl("2026-10-09"), "1234560000", 0.99)},
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanRekeningBeda,
			mauTotal:  300000,
		},
		{
			nama:      "rekening tujuan tidak terbaca",
			bukti:     []PembacaanBukti{bukti(ptrInt64(300000), tgl("2026-10-09"), "", 0.99)},
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanTidakTerbaca,
			mauTotal:  300000,
		},
		{
			nama:      "tanggal lebih tua dari pesanan - mundur",
			bukti:     []PembacaanBukti{bukti(ptrInt64(300000), tgl("2026-10-07"), "1234567890", 0.99)},
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanLainnya,
			mauTotal:  300000,
		},
		{
			nama:     "tanggal tepat di batas mundur diterima",
			bukti:    []PembacaanBukti{bukti(ptrInt64(300000), tgl("2026-10-08"), "1234567890", 0.99)},
			mauHasil: VerifCocok,
			mauTotal: 300000,
		},
		{
			nama:      "kurang bayar",
			bukti:     []PembacaanBukti{bukti(ptrInt64(299999), tgl("2026-10-09"), "1234567890", 0.99)},
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanKurangBayar,
			mauTotal:  299999,
		},
		{
			nama:      "lebih bayar",
			bukti:     []PembacaanBukti{bukti(ptrInt64(300001), tgl("2026-10-09"), "1234567890", 0.99)},
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanLebihBayar,
			mauTotal:  300001,
		},
		{
			nama:      "tanpa bukti",
			bukti:     nil,
			mauHasil:  VerifPerluReview,
			mauAlasan: AlasanTidakTerbaca,
		},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			d := Nilai(total, tanggalOrder, k.bukti, bankAktif(), rules)
			if d.Hasil != k.mauHasil {
				t.Fatalf("hasil harus %q, dapat %q (catatan %q)", k.mauHasil, d.Hasil, d.Catatan)
			}
			if d.Alasan != k.mauAlasan {
				t.Fatalf("alasan harus %q, dapat %q", k.mauAlasan, d.Alasan)
			}
			if d.TotalTerbaca != k.mauTotal {
				t.Errorf("total terbaca harus %d, dapat %d", k.mauTotal, d.TotalTerbaca)
			}
			if d.Hasil == VerifPerluReview && d.Catatan == "" {
				t.Error("perlu review harus punya catatan yang bisa dibaca admin")
			}
			if len(d.Catatan) > MaxCatatanVerifikasi {
				t.Errorf("catatan melampaui batas kolom: %d karakter", len(d.Catatan))
			}
		})
	}
}

func TestNilaiMenghormatiToleransiSetelan(t *testing.T) {
	total := int64(300000)
	tanggalOrder := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)

	rules := aiAktif()
	rules.TolKurang = 5000
	rules.TolLebih = 1000
	b := []PembacaanBukti{{ProofID: "p", Nominal: ptrInt64(295000), Tanggal: tgl("2026-10-09"), RekAkhir: "1234567890", Confidence: 0.99}}

	if d := Nilai(total, tanggalOrder, b, bankAktif(), rules); d.Hasil != VerifCocok {
		t.Errorf("selisih di dalam toleransi kurang harus cocok, dapat %q/%q", d.Hasil, d.Alasan)
	}
	b[0].Nominal = ptrInt64(294999)
	if d := Nilai(total, tanggalOrder, b, bankAktif(), rules); d.Alasan != AlasanKurangBayar {
		t.Errorf("selisih di luar toleransi kurang harus kurang_bayar, dapat %q", d.Alasan)
	}
	b[0].Nominal = ptrInt64(301000)
	if d := Nilai(total, tanggalOrder, b, bankAktif(), rules); d.Hasil != VerifCocok {
		t.Errorf("kelebihan di dalam toleransi harus cocok, dapat %q/%q", d.Hasil, d.Alasan)
	}
	b[0].Nominal = ptrInt64(301001)
	if d := Nilai(total, tanggalOrder, b, bankAktif(), rules); d.Alasan != AlasanLebihBayar {
		t.Errorf("kelebihan di luar toleransi harus lebih_bayar, dapat %q", d.Alasan)
	}
}

func TestNilaiMenghormatiDigitRekeningSetelan(t *testing.T) {
	total := int64(100)
	akun := []bank.Account{{ID: "b1", Bank: "BCA", Nomor: "998877665544", IsActive: true}}
	b := []PembacaanBukti{{ProofID: "p", Nominal: ptrInt64(100), Tanggal: tgl("2026-10-09"), RekAkhir: "5544", Confidence: 0.99}}

	rules := aiAktif()
	rules.DigitRek = 4
	if d := Nilai(total, tv("2026-10-09"), b, akun, rules); d.Hasil != VerifCocok {
		t.Errorf("4 digit terakhir harus cocok, dapat %q/%q", d.Hasil, d.Alasan)
	}
	rules.DigitRek = 6
	if d := Nilai(total, tv("2026-10-09"), b, akun, rules); d.Alasan != AlasanRekeningBeda {
		t.Errorf("6 digit terakhir tidak cocok (baca hanya 4 digit), dapat %q", d.Alasan)
	}
	// Pemisah pada nomor hasil baca diabaikan.
	b[0].RekAkhir = "55 44"
	if d := Nilai(total, tv("2026-10-09"), b, akun, rules); d.Alasan != AlasanRekeningBeda {
		t.Errorf("hanya digit yang dibandingkan; 6 digit terakhir tetap tidak cocok, dapat %q", d.Alasan)
	}
	rules.DigitRek = 4
	if d := Nilai(total, tv("2026-10-09"), b, akun, rules); d.Hasil != VerifCocok {
		t.Errorf("pemisah diabaikan sehingga 4 digit terakhir cocok, dapat %q/%q", d.Hasil, d.Alasan)
	}
}

func TestRekeningCocokDanDigitSaja(t *testing.T) {
	akun := []bank.Account{{Bank: "BCA", Nomor: "1234-5678.90"}}
	if !rekeningCocok("987890", 4, akun) {
		t.Error("empat digit terakhir harus cocok walau nomor rekening memakai pemisah")
	}
	if rekeningCocok("", 4, akun) || rekeningCocok("abc", 4, akun) {
		t.Error("nomor kosong/bukan digit tidak boleh dianggap cocok")
	}
	if rekeningCocok("987890", 4, nil) {
		t.Error("tanpa rekening aktif tidak ada yang cocok")
	}
	if got := digitSaja("12-34 56"); got != "123456" {
		t.Errorf("digitSaja harus membuang non-digit, dapat %q", got)
	}
}

// ---------------------------------------------------------- sensor rahasia --

func TestSanitizeRawMembuangNilaiRahasia(t *testing.T) {
	raw := json.RawMessage(`{
		"nominal": 250000,
		"api_key": "sk-rahasia-jangan-bocor",
		"nested": {"Authorization": "Bearer tok123", "bank": "BCA", "items":[{"password":"p"},{"rek":"99"}]},
		"token": "xyz"
	}`)
	bersih := SanitizeRaw(raw)
	if bersih == nil {
		t.Fatal("JSON valid harus tetap menghasilkan JSON")
	}
	s := string(bersih)
	for _, rahasia := range []string{"sk-rahasia-jangan-bocor", "tok123", `"p"`, "xyz"} {
		if strings.Contains(s, rahasia) {
			t.Errorf("nilai rahasia %q masih tersimpan: %s", rahasia, s)
		}
	}
	if !strings.Contains(s, "250000") || !strings.Contains(s, "BCA") {
		t.Errorf("nilai non-rahasia harus dipertahankan: %s", s)
	}
	if !strings.Contains(s, "[disensor]") {
		t.Errorf("nilai rahasia harus diganti penanda: %s", s)
	}
	// JSON yang tidak valid tidak disimpan sama sekali.
	if got := SanitizeRaw(json.RawMessage(`{bukan json`)); got != nil {
		t.Errorf("JSON tidak valid harus menjadi nil, dapat %q", got)
	}
	if got := SanitizeRaw(nil); got != nil {
		t.Errorf("raw kosong harus nil, dapat %q", got)
	}
}

// ------------------------------------------------------ pembaca tiruan -----

func TestMockReaderDeterministikDanBawaanTidakTerbaca(t *testing.T) {
	peta := map[string]ReadResult{
		MockKey([]byte("isi-a")): {Nominal: ptrInt64(100000), Confidence: 0.97},
	}
	m := NewMockReader(peta)
	ctx := context.Background()

	pertama, err := m.Read(ctx, ReadInput{Content: []byte("isi-a")})
	if err != nil {
		t.Fatalf("baca harus berhasil: %v", err)
	}
	kedua, _ := m.Read(ctx, ReadInput{Content: []byte("isi-a")})
	if pertama.Nominal == nil || kedua.Nominal == nil || *pertama.Nominal != *kedua.Nominal || pertama.Confidence != kedua.Confidence {
		t.Error("pembaca tiruan harus deterministik untuk isi yang sama")
	}
	if m.JumlahPanggilan() != 2 {
		t.Errorf("penghitung panggilan harus 2, dapat %d", m.JumlahPanggilan())
	}

	// Berkas yang tidak dikenal memakai bawaan: TIDAK terbaca, bukan angka
	// karangan (mode lokal tidak boleh menyetujui pembayaran).
	lain, _ := m.Read(ctx, ReadInput{Content: []byte("isi-lain")})
	if lain.Nominal != nil || lain.Confidence != 0 {
		t.Errorf("bawaan harus 'tidak terbaca', dapat %+v", lain)
	}

	m.Galat = errors.New("provider tumbang")
	if _, err := m.Read(ctx, ReadInput{Content: []byte("isi-a")}); err == nil {
		t.Error("galat provider harus diteruskan")
	}
}

// ------------------------------------------------- orkestrasi layanan OCR ---

// ocrRepoPalsu adalah OCRRepository in-memory yang meniru efek samping
// penyimpanan yang penting: bukti yang sudah disimpan hasilnya ditandai
// terbaca (ocr_at) sehingga pengulangan tidak membaca ulang.
type ocrRepoPalsu struct {
	ord          OrderOCR
	proofs       []ProofOCR
	simpan       []SimpanBacaanRequest
	sudahDinilai bool
	errOrder     error
}

func (r *ocrRepoPalsu) OrderForOCR(context.Context, string) (OrderOCR, error) {
	if r.errOrder != nil {
		return OrderOCR{}, r.errOrder
	}
	return r.ord, nil
}

func (r *ocrRepoPalsu) ListProofsForOCR(context.Context, string) ([]ProofOCR, error) {
	return append([]ProofOCR(nil), r.proofs...), nil
}

func (r *ocrRepoPalsu) SimpanBacaan(_ context.Context, req SimpanBacaanRequest) error {
	r.simpan = append(r.simpan, req)
	for i := range r.proofs {
		if r.proofs[i].ID != req.ProofID {
			continue
		}
		r.proofs[i].Nominal = req.Hasil.Nominal
		r.proofs[i].Tanggal = req.Hasil.Tanggal
		r.proofs[i].Bank = req.Hasil.Bank
		r.proofs[i].RekAkhir = req.Hasil.RekAkhir
		r.proofs[i].Confidence = req.Hasil.Confidence
		at := time.Now().UTC()
		r.proofs[i].At = &at
	}
	return nil
}

func (r *ocrRepoPalsu) SudahDinilaiAI(context.Context, string) (bool, error) {
	return r.sudahDinilai, nil
}

// siapkanOCR menyusun layanan OCR beserta perkakas ujinya.
func siapkanOCR(t *testing.T, repo *ocrRepoPalsu, reader PaymentReader, ai settings.AISettings, verifier Verifier) (*OCRService, *storage.Memory) {
	t.Helper()
	mem := storage.NewMemory("")
	svc := NewOCRService(repo, reader, mem, rekeningPalsu{bankAktif()}, aiPalsu{setelan: ai}, NewVerificationService(verifier))
	return svc, mem
}

// simpanBukti menaruh isi bukti di object storage in-memory.
func simpanBukti(t *testing.T, mem *storage.Memory, key string, isi []byte) {
	t.Helper()
	if err := mem.Put(context.Background(), key, strings.NewReader(string(isi)), int64(len(isi)), MimePNG); err != nil {
		t.Fatalf("simpan objek uji: %v", err)
	}
}

func pesananOCR(total int64) OrderOCR {
	return OrderOCR{ID: orderUUID, Status: StatusSubmitted, Total: total, Tanggal: tgl("2026-10-09"), VerifStatus: "belum"}
}

func TestOCSKipTidakMemanggilProviderDanTidakMenulis(t *testing.T) {
	repo := &ocrRepoPalsu{ord: pesananOCR(300000), proofs: []ProofOCR{{ID: proofUUID1, StorageKey: "proofs/p1", MimeType: MimePNG}}}
	reader := NewMockReader(nil)
	verifier := &verifierPalsu{}
	svc, _ := siapkanOCR(t, repo, reader, settings.AISettings{Aktif: false}, verifier)

	hasil, err := svc.Baca(context.Background(), orderUUID)
	if err != nil {
		t.Fatalf("Baca dengan AI mati tidak boleh gagal: %v", err)
	}
	if !hasil.Dilewati {
		t.Error("AI mati harus melewati pekerjaan")
	}
	if reader.JumlahPanggilan() != 0 {
		t.Errorf("AI mati TIDAK boleh memanggil provider, dipanggil %d kali", reader.JumlahPanggilan())
	}
	if len(repo.simpan) != 0 || verifier.dipanggil != 0 {
		t.Error("AI mati tidak boleh menyentuh penyimpanan maupun verifikasi")
	}
}

func TestOCRCocokMemverifikasiDenganMetodeAI(t *testing.T) {
	isi := []byte("bukti-cocok")
	repo := &ocrRepoPalsu{
		ord:    pesananOCR(300000),
		proofs: []ProofOCR{{ID: proofUUID1, OrderID: orderUUID, StorageKey: "proofs/p1", MimeType: MimePNG, NamaFile: "a.png"}},
	}
	reader := NewMockReader(map[string]ReadResult{
		MockKey(isi): {
			Nominal: ptrInt64(300000), Tanggal: tgl("2026-10-09"), Bank: "BCA", RekAkhir: "1234567890",
			Confidence: 0.97,
			Raw:        json.RawMessage(`{"nominal":300000,"_debug":{"api_key":"sk-bocor"}}`),
		},
	})
	verifier := &verifierPalsu{}
	svc, mem := siapkanOCR(t, repo, reader, aiAktif(), verifier)
	simpanBukti(t, mem, "proofs/p1", isi)

	hasil, err := svc.Baca(context.Background(), orderUUID)
	if err != nil {
		t.Fatalf("Baca gagal: %v", err)
	}
	if hasil.Dilewati || hasil.Keputusan == nil || hasil.Keputusan.Hasil != VerifCocok {
		t.Fatalf("harus memutuskan cocok, dapat %+v", hasil)
	}
	if verifier.dipanggil != 1 {
		t.Fatalf("MarkVerified harus dipanggil sekali, dapat %d", verifier.dipanggil)
	}
	req := verifier.req
	if req.Metode != MetodeAI {
		t.Errorf("metode harus %q, dapat %q", MetodeAI, req.Metode)
	}
	if req.Actor.Kind != "system" {
		t.Errorf("pelaku harus aktor sistem, dapat %q", req.Actor.Kind)
	}
	if req.TotalTerbaca == nil || *req.TotalTerbaca != 300000 {
		t.Errorf("nominal terbaca harus 300000, dapat %v", req.TotalTerbaca)
	}
	if req.Hasil != VerifCocok || req.Alasan != "" {
		t.Errorf("persetujuan tidak boleh membawa alasan, dapat %q/%q", req.Hasil, req.Alasan)
	}
	if len(repo.simpan) != 1 {
		t.Fatalf("hasil baca harus disimpan sekali, dapat %d", len(repo.simpan))
	}
	simpan := repo.simpan[0]
	if simpan.Hasil.Nominal == nil || *simpan.Hasil.Nominal != 300000 {
		t.Error("nominal hasil baca harus tersimpan")
	}
	if strings.Contains(string(simpan.Raw), "sk-bocor") {
		t.Errorf("jawaban mentah yang disimpan masih memuat rahasia: %s", simpan.Raw)
	}
	if simpan.Audit.Action != audit.ActionBacaBukti || simpan.Audit.Entity != audit.EntityOrder {
		t.Errorf("audit pembacaan harus entity=%q action=%q, dapat %q/%q",
			audit.EntityOrder, audit.ActionBacaBukti, simpan.Audit.Entity, simpan.Audit.Action)
	}
	if simpan.Audit.ActorKind != "system" {
		t.Errorf("audit pembacaan harus oleh aktor sistem, dapat %q", simpan.Audit.ActorKind)
	}
}

func TestOCRPerluReviewKarenaBuktiKaburTidakMenyetujui(t *testing.T) {
	isi := []byte("bukti-buram")
	repo := &ocrRepoPalsu{
		ord:    pesananOCR(300000),
		proofs: []ProofOCR{{ID: proofUUID1, OrderID: orderUUID, StorageKey: "proofs/p1", MimeType: MimePNG}},
	}
	reader := NewMockReader(map[string]ReadResult{
		MockKey(isi): {Nominal: ptrInt64(300000), Tanggal: tgl("2026-10-09"), RekAkhir: "1234567890", Confidence: 0.35},
	})
	verifier := &verifierPalsu{}
	svc, mem := siapkanOCR(t, repo, reader, aiAktif(), verifier)
	simpanBukti(t, mem, "proofs/p1", isi)

	hasil, err := svc.Baca(context.Background(), orderUUID)
	if err != nil {
		t.Fatalf("Baca gagal: %v", err)
	}
	if hasil.Keputusan == nil || hasil.Keputusan.Alasan != AlasanTidakTerbaca {
		t.Fatalf("bukti kabur harus perlu review (tidak_terbaca), dapat %+v", hasil.Keputusan)
	}
	req := verifier.req
	if req.Hasil != VerifPerluReview || req.Alasan != AlasanTidakTerbaca {
		t.Errorf("keputusan harus perlu_review/tidak_terbaca, dapat %q/%q", req.Hasil, req.Alasan)
	}
	if req.TotalTerbaca != nil {
		t.Errorf("nominal tidak terbaca harus disimpan NULL, dapat %v", *req.TotalTerbaca)
	}
	if req.Catatan == "" {
		t.Error("perlu review harus membawa catatan/alasan yang bisa dibaca admin")
	}
}

func TestOCRIdempotenSaatDiulang(t *testing.T) {
	isi := []byte("bukti-idempoten")
	repo := &ocrRepoPalsu{
		ord:    pesananOCR(300000),
		proofs: []ProofOCR{{ID: proofUUID1, OrderID: orderUUID, StorageKey: "proofs/p1", MimeType: MimePNG}},
	}
	reader := NewMockReader(map[string]ReadResult{
		MockKey(isi): {Nominal: ptrInt64(300000), Tanggal: tgl("2026-10-09"), RekAkhir: "1234567890", Confidence: 0.99},
	})
	verifier := &verifierPalsu{}
	svc, mem := siapkanOCR(t, repo, reader, aiAktif(), verifier)
	simpanBukti(t, mem, "proofs/p1", isi)

	if _, err := svc.Baca(context.Background(), orderUUID); err != nil {
		t.Fatalf("Baca pertama gagal: %v", err)
	}
	if reader.JumlahPanggilan() != 1 || verifier.dipanggil != 1 {
		t.Fatalf("jalan pertama harus membaca sekali + memutuskan sekali, dapat %d/%d",
			reader.JumlahPanggilan(), verifier.dipanggil)
	}

	// Pengulangan job River setelah keputusan tersimpan: penyimpanan sudah
	// menandai bukti terbaca + keputusan AI sudah ada.
	repo.sudahDinilai = true
	hasil, err := svc.Baca(context.Background(), orderUUID)
	if err != nil {
		t.Fatalf("Baca ulang gagal: %v", err)
	}
	if reader.JumlahPanggilan() != 1 {
		t.Errorf("bukti yang sudah dibaca tidak boleh dipanggil ulang, dipanggil %d kali", reader.JumlahPanggilan())
	}
	if verifier.dipanggil != 1 {
		t.Errorf("keputusan tidak boleh ditulis dua kali, MarkVerified dipanggil %d kali", verifier.dipanggil)
	}
	if !hasil.Dilewati {
		t.Error("pengulangan tanpa bukti baru harus dilewati")
	}
}

func TestOCRBuktiBaruMemicuPenilaianUlang(t *testing.T) {
	// Dua bukti; satu sudah dibaca sebelumnya, satu baru: pekerjaan harus
	// membaca HANYA yang baru lalu menilai ulang seluruh himpunan.
	isiLama := []byte("bukti-lama")
	isiBaru := []byte("bukti-baru")
	at := time.Now().UTC().Add(-time.Hour)
	repo := &ocrRepoPalsu{
		ord: pesananOCR(300000),
		proofs: []ProofOCR{
			{ID: proofUUID1, OrderID: orderUUID, StorageKey: "proofs/p1", MimeType: MimePNG,
				Nominal: ptrInt64(100000), Tanggal: tgl("2026-10-09"), RekAkhir: "1234567890", Confidence: 0.95, At: &at},
			{ID: proofUUID2, OrderID: orderUUID, StorageKey: "proofs/p2", MimeType: MimePNG},
		},
	}
	reader := NewMockReader(map[string]ReadResult{
		MockKey(isiLama): {Nominal: ptrInt64(100000), Tanggal: tgl("2026-10-09"), RekAkhir: "1234567890", Confidence: 0.95},
		MockKey(isiBaru): {Nominal: ptrInt64(200000), Tanggal: tgl("2026-10-09"), RekAkhir: "1234567890", Confidence: 0.95},
	})
	verifier := &verifierPalsu{}
	svc, mem := siapkanOCR(t, repo, reader, aiAktif(), verifier)
	simpanBukti(t, mem, "proofs/p1", isiLama)
	simpanBukti(t, mem, "proofs/p2", isiBaru)

	hasil, err := svc.Baca(context.Background(), orderUUID)
	if err != nil {
		t.Fatalf("Baca gagal: %v", err)
	}
	if reader.JumlahPanggilan() != 1 {
		t.Errorf("hanya bukti baru yang boleh dipanggil provider, dipanggil %d kali", reader.JumlahPanggilan())
	}
	if hasil.Dibaca != 1 || hasil.JumlahBukti != 2 {
		t.Errorf("harus membaca 1 dari 2 bukti, dapat %d/%d", hasil.Dibaca, hasil.JumlahBukti)
	}
	if verifier.req.TotalTerbaca == nil || *verifier.req.TotalTerbaca != 300000 {
		t.Errorf("nominal harus menjumlahkan bukti lama + baru (300000), dapat %v", verifier.req.TotalTerbaca)
	}
	if verifier.req.Hasil != VerifCocok {
		t.Errorf("himpunan lengkap harus cocok, dapat %q/%q", verifier.req.Hasil, verifier.req.Alasan)
	}
}

func TestOCRGagalProviderTidakMenulisApaPun(t *testing.T) {
	repo := &ocrRepoPalsu{
		ord:    pesananOCR(300000),
		proofs: []ProofOCR{{ID: proofUUID1, OrderID: orderUUID, StorageKey: "proofs/p1", MimeType: MimePNG}},
	}
	reader := NewMockReader(nil)
	reader.Galat = errors.New("provider tumbang")
	verifier := &verifierPalsu{}
	svc, mem := siapkanOCR(t, repo, reader, aiAktif(), verifier)
	simpanBukti(t, mem, "proofs/p1", []byte("apa saja"))

	_, err := svc.Baca(context.Background(), orderUUID)
	if err == nil {
		t.Fatal("kegagalan provider harus dikembalikan supaya pekerjaan diulang")
	}
	if len(repo.simpan) != 0 || verifier.dipanggil != 0 {
		t.Error("kegagalan provider tidak boleh menyimpan bacaan maupun keputusan apa pun")
	}
	if repo.proofs[0].At != nil {
		t.Error("bukti yang gagal dibaca tidak boleh ditandai sudah dibaca")
	}
}

func TestOCRLewatiPesananYangTidakBisaDinilai(t *testing.T) {
	kasus := []struct {
		nama  string
		ord   OrderOCR
		bukti []ProofOCR
	}{
		{"pesanan masih draft", OrderOCR{ID: orderUUID, Status: StatusDraft, Total: 100}, []ProofOCR{{ID: proofUUID1}}},
		{"pesanan sudah selesai", OrderOCR{ID: orderUUID, Status: StatusDone, Total: 100}, []ProofOCR{{ID: proofUUID1}}},
		{"tidak ada bukti", pesananOCR(100), nil},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			repo := &ocrRepoPalsu{ord: k.ord, proofs: k.bukti}
			reader := NewMockReader(nil)
			verifier := &verifierPalsu{}
			svc, _ := siapkanOCR(t, repo, reader, aiAktif(), verifier)

			hasil, err := svc.Baca(context.Background(), orderUUID)
			if err != nil {
				t.Fatalf("harus dilewati tanpa galat: %v", err)
			}
			if !hasil.Dilewati || hasil.Sebab == "" {
				t.Errorf("harus dilewati dengan sebab, dapat %+v", hasil)
			}
			if reader.JumlahPanggilan() != 0 || verifier.dipanggil != 0 {
				t.Error("tidak boleh memanggil provider/verifikasi")
			}
		})
	}
}

func TestOCRMenghormatiPesananSudahSelesaiSaatKeputusan(t *testing.T) {
	// Balapan: pesanan selesai diverifikasi admin di antara pembacaan dan
	// keputusan. MarkVerified menjawab ErrAlreadyDone — itu bukan kegagalan.
	isi := []byte("bukti-balapan")
	repo := &ocrRepoPalsu{
		ord:    pesananOCR(300000),
		proofs: []ProofOCR{{ID: proofUUID1, OrderID: orderUUID, StorageKey: "proofs/p1", MimeType: MimePNG}},
	}
	reader := NewMockReader(map[string]ReadResult{
		MockKey(isi): {Nominal: ptrInt64(300000), Tanggal: tgl("2026-10-09"), RekAkhir: "1234567890", Confidence: 0.99},
	})
	verifier := &verifierPalsu{err: ErrAlreadyDone}
	svc, mem := siapkanOCR(t, repo, reader, aiAktif(), verifier)
	simpanBukti(t, mem, "proofs/p1", isi)

	hasil, err := svc.Baca(context.Background(), orderUUID)
	if err != nil {
		t.Fatalf("pesanan yang sudah selesai harus dilewati, bukan gagal: %v", err)
	}
	if !hasil.Dilewati {
		t.Error("harus ditandai dilewati")
	}
}

func TestOCRBelumTerpasangGagalTerangTerangan(t *testing.T) {
	var svc *OCRService
	if _, err := svc.Baca(context.Background(), orderUUID); !errors.Is(err, ErrOCRBelumTerpasang) {
		t.Errorf("layanan kosong harus gagal jelas, dapat %v", err)
	}
	svc = NewOCRService(&ocrRepoPalsu{}, nil, nil, nil, nil, nil)
	if _, err := svc.Baca(context.Background(), orderUUID); !errors.Is(err, ErrOCRBelumTerpasang) {
		t.Errorf("layanan setengah terpasang harus gagal jelas, dapat %v", err)
	}
}

// ------------------------------------------------- aktor sistem & otorisasi -

func TestAktorSistemHanyaUntukMetodeAI(t *testing.T) {
	repo := &verifierPalsu{}
	svc := NewVerificationService(repo)
	ctx := context.Background()

	if _, err := svc.MarkVerified(ctx, SystemActor(), orderUUID, VerifyInput{
		Hasil: VerifCocok, Metode: MetodeAI, TotalTerbaca: ptrInt64(1000),
	}); err != nil {
		t.Fatalf("aktor sistem dengan metode ai harus diterima: %v", err)
	}
	if repo.req.Actor.Kind != "system" {
		t.Errorf("pelaku harus dicatat sebagai sistem, dapat %q", repo.req.Actor.Kind)
	}

	for _, m := range []string{MetodeManual, MetodeVirtualAccount, ""} {
		repo := &verifierPalsu{}
		svc := NewVerificationService(repo)
		in := VerifyInput{Hasil: VerifCocok, Metode: m}
		if _, err := svc.MarkVerified(ctx, SystemActor(), orderUUID, in); !errors.Is(err, ErrForbidden) {
			t.Errorf("aktor sistem dengan metode %q harus ditolak, dapat %v", m, err)
		}
		if repo.dipanggil != 0 {
			t.Errorf("metode %q: penyimpanan tidak boleh disentuh", m)
		}
	}
}

func TestTeksTimelineSistemTidakMengakuAdmin(t *testing.T) {
	setuju := TeksEventSetujuSistem()
	if strings.Contains(setuju, "admin Pusat") || !strings.Contains(setuju, "sistem") {
		t.Errorf("teks persetujuan otomatis tidak boleh mengaku admin: %q", setuju)
	}
	review := TeksEventPerluReviewSistem(AlasanKurangBayar)
	if !strings.Contains(review, "kurang bayar") || strings.Contains(review, "admin Pusat") {
		t.Errorf("teks review otomatis harus memuat alasan tanpa mengaku admin: %q", review)
	}
}

func TestAktorSistemKonstannyaBukanAkunManusia(t *testing.T) {
	a := SystemActor()
	if a.UserID != AktorSistemAI || a.Kind != "system" {
		t.Errorf("aktor sistem harus memakai identitas mesin tetap, dapat %+v", a)
	}
	if a.IsAdmin() || a.IsBranch() || a.IsSuperadmin() {
		t.Error("aktor sistem tidak boleh dianggap admin, cabang, maupun superadmin")
	}
	if a.Can(auth.CapAdminVerify) || a.Can(auth.CapAdminSettings) {
		t.Error("aktor sistem tidak boleh punya kapabilitas manusia; jalur AI diizinkan khusus oleh verifikatorSah")
	}
	// Nama berbeda dari "admin Pusat" supaya log/timeline tidak menyesatkan.
	if a.Nama == "" || strings.Contains(a.Nama, "admin") {
		t.Errorf("nama aktor sistem harus penanda mesin, dapat %q", a.Nama)
	}
}

func TestNilaiCatatanPendek(t *testing.T) {
	// Catatan keputusan tidak boleh melampaui kolom verifikasi walau pesan
	// sebabnya panjang.
	panjang := fmt.Sprintf("rekening tujuan tidak cocok %s", strings.Repeat("x", 5000))
	k := Keputusan{KeyakinanTerendah: 0.5, TotalTerbaca: 1}.tolak(AlasanRekeningBeda, panjang)
	if len([]rune(k.Catatan)) > MaxCatatanVerifikasi {
		t.Errorf("catatan %d rune melampaui batas %d", len([]rune(k.Catatan)), MaxCatatanVerifikasi)
	}
}
