package payment

// Berkas ini memuat PEMBACAAN BUKTI AI/OCR DAN ANTREAN REVIEW (kartu F2-26):
// membaca setiap bukti transfer lewat `PaymentReader`, menyimpan hasilnya ke
// kolom `payment_proofs.ocr_*`, menjumlahkan nominal multi-bukti, dan
// membandingkannya dengan pesanan + rekening aktif + setelan `ai` (PRD §5,
// §4.9; SystemDesign.md §4.3, §9; Database.md §4.5, §7.4).
//
// Aturan yang ditegakkan (PRD §5 "Aturan AI"):
//
//   - `ai.aktif = false` berarti pekerjaan ini TIDAK memanggil provider sama
//     sekali dan tidak menyentuh keadaan pesanan (antre verifikasi manual);
//   - multi-bukti → nominal DIJUMLAHKAN sebelum dibandingkan;
//   - keyakinan < `ai.min_keyakinan`, nominal/tanggal/rekening tidak terbaca →
//     perlu review (alasan "tidak terbaca");
//   - rekening tujuan tidak cocok dengan REKENING AKTIF (digit akhir
//     `ai.digit_rek`) → perlu review ("rekening_beda");
//   - tanggal transfer lebih tua dari `tanggal pesanan − ai.mundur` hari →
//     perlu review ("lainnya");
//   - selisih di luar `ai.tol_kurang`/`ai.tol_lebih` → perlu review
//     ("kurang_bayar"/"lebih_bayar");
//   - cocok → `VerificationService.MarkVerified` dengan metode `ai` (satu
//     pintu transisi Done; dokumen + email + timeline + audit mengikuti jalur
//     yang sama dengan verifikasi manual);
//   - AI TIDAK PERNAH menolak final: keputusan "perlu review" hanya menandai
//     sub-status verifikasi, dan admin selalu bisa menimpanya lewat
//     `POST /admin/orders/{id}/verify` (F1-18).
//
// Idempotensi (River at-least-once; job diulang/tergandakan):
//
//  1. Bukti yang SUDAH dibaca (kolom `ocr_at` terisi) tidak dipanggil ulang ke
//     provider — hasil bacanya dipakai kembali dari database.
//  2. Kegagalan provider TIDAK menyimpan apa pun: pekerjaan dikembalikan
//     sebagai galat dan River mengulanginya dari keadaan bersih.
//  3. Keputusan untuk satu himpunan bukti yang sama tidak ditulis dua kali:
//     bila sudah ada log verifikasi metode `ai` yang dibuat SESUDAH bukti
//     terakhir dibaca, keputusan dilewati. Karena itu pengulangan job tidak
//     pernah menambah baris `verification_logs`, peristiwa timeline, atau
//     entri audit — sementara bukti BARU (yang ocr_at-nya lebih muda) selalu
//     memicu penilaian ulang.
//  4. Pesanan yang sudah Done dijawab ErrAlreadyDone dan diperlakukan sebagai
//     "tidak ada yang perlu dilakukan".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/settings"
)

// Sentinel error domain pembacaan bukti.
var (
	// ErrOCRBelumTerpasang dikembalikan bila layanan pembaca belum dirakit
	// (kesalahan wiring, bukan kesalahan pemanggil).
	ErrOCRBelumTerpasang = errors.New("layanan pembacaan bukti belum terpasang")
)

// OrderOCR adalah konteks pesanan yang dibutuhkan penilaian otomatis.
type OrderOCR struct {
	ID string
	// Status adalah status pesanan (hanya "submitted" yang dinilai).
	Status string
	// Total adalah total pesanan (Rupiah bulat).
	Total int64
	// Tanggal adalah tanggal bisnis pesanan (dipakai toleransi mundur).
	Tanggal *time.Time
	// VerifStatus adalah sub-status verifikasi pesanan saat dibaca.
	VerifStatus string
	// AlasanReview adalah alasan review yang sedang tercatat (bila ada).
	AlasanReview string
}

// ProofOCR adalah satu bukti transfer beserta hasil baca yang sudah ada.
type ProofOCR struct {
	ID         string
	OrderID    string
	StorageKey string
	MimeType   string
	NamaFile   string
	// Nominal nil berarti bukti ini belum/tidak menghasilkan nominal.
	Nominal    *int64
	Tanggal    *time.Time
	Bank       string
	RekAkhir   string
	Confidence float64
	// At adalah waktu baca terakhir; nil berarti belum pernah dibaca.
	At *time.Time
}

// SimpanBacaanRequest adalah satu hasil baca bukti yang harus disimpan bersama
// entri auditnya dalam SATU transaksi.
type SimpanBacaanRequest struct {
	ProofID string
	OrderID string
	Hasil   ReadResult
	// Raw adalah jawaban mentah provider yang SUDAH dibersihkan dari nilai
	// yang tampak rahasia (SanitizeRaw).
	Raw   json.RawMessage
	Audit audit.Entry
}

// OCRRepository adalah sisi penyimpanan pembacaan bukti.
type OCRRepository interface {
	// OrderForOCR membaca konteks pesanan; id tak dikenal = ErrNotFound.
	OrderForOCR(ctx context.Context, orderID string) (OrderOCR, error)
	// ListProofsForOCR mengembalikan seluruh bukti pesanan, terlama dulu.
	ListProofsForOCR(ctx context.Context, orderID string) ([]ProofOCR, error)
	// SimpanBacaan menyimpan hasil baca satu bukti + entri auditnya.
	SimpanBacaan(ctx context.Context, req SimpanBacaanRequest) error
	// SudahDinilaiAI melaporkan apakah sudah ada keputusan AI sesudah bukti
	// terakhir dibaca (dipakai penjaga idempotensi).
	SudahDinilaiAI(ctx context.Context, orderID string) (bool, error)
}

// RekeningSource menyediakan rekening bank AKTIF sebagai acuan pencocokan
// rekening tujuan (PRD §5, §4.6). Implementasinya adalah bank.Service.
type RekeningSource interface {
	Active(ctx context.Context) ([]bank.Account, error)
}

// AISettingsSource menyediakan setelan `ai` yang sedang berlaku (PRD §4.9).
// Implementasinya adalah settings.Service.
type AISettingsSource interface {
	AISettings(ctx context.Context) (settings.AISettings, error)
}

// PembacaanBukti adalah hasil baca SATU bukti yang dipakai penilaian.
type PembacaanBukti struct {
	ProofID    string
	Nominal    *int64
	Tanggal    *time.Time
	Bank       string
	RekAkhir   string
	Confidence float64
}

// Keputusan adalah hasil penilaian seluruh bukti satu pesanan.
type Keputusan struct {
	// Hasil adalah VerifCocok atau VerifPerluReview.
	Hasil string
	// Alasan terisi hanya ketika Hasil = VerifPerluReview.
	Alasan string
	// TotalTerbaca adalah Σ nominal bukti yang terbaca.
	TotalTerbaca int64
	// KeyakinanTerendah adalah keyakinan provider terendah pada himpunan ini.
	KeyakinanTerendah float64
	// JumlahBukti adalah banyaknya bukti yang dinilai.
	JumlahBukti int
	// Catatan adalah ringkasan yang bisa dibaca manusia untuk log verifikasi.
	Catatan string
}

// OCRHasil adalah ringkasan satu kali pembacaan seluruh bukti satu pesanan.
type OCRHasil struct {
	OrderID string
	// Dilewati menandakan tidak ada keputusan yang ditulis, beserta sebabnya.
	Dilewati bool
	Sebab    string
	// JumlahBukti adalah banyaknya bukti pesanan yang dinilai.
	JumlahBukti int
	// Dibaca adalah banyaknya bukti yang benar-benar dipanggil provider pada
	// jalannya ini (0 pada pengulangan job).
	Dibaca int
	// Keputusan terisi bila penilaian sampai dijalankan.
	Keputusan *Keputusan
	// Outcome terisi bila keputusan diteruskan ke MarkVerified.
	Outcome *VerifyOutcome
}

// OCRService adalah logika bisnis pembacaan bukti + penilaian otomatis.
type OCRService struct {
	repo   OCRRepository
	baca   PaymentReader
	store  storage.Store
	rek    RekeningSource
	ai     AISettingsSource
	verify *VerificationService
	// sekarang adalah sumber waktu (bisa diganti pengujian).
	sekarang func() time.Time
}

// NewOCRService membuat layanan pembacaan bukti. Semua dependensi WAJIB ada:
// layanan yang setengah terpasang akan diam-diam melewati pekerjaannya, dan
// itu justru menyembunyikan masalah konfigurasi.
func NewOCRService(repo OCRRepository, baca PaymentReader, store storage.Store, rek RekeningSource, ai AISettingsSource, verify *VerificationService) *OCRService {
	return &OCRService{repo: repo, baca: baca, store: store, rek: rek, ai: ai, verify: verify, sekarang: func() time.Time { return time.Now().UTC() }}
}

// Tersedia melaporkan apakah layanan siap dipakai.
func (s *OCRService) Tersedia() bool {
	return s != nil && s.repo != nil && s.baca != nil && s.store != nil && s.rek != nil && s.ai != nil && s.verify != nil
}

// Baca menjalankan satu siklus pembacaan + penilaian untuk satu pesanan.
func (s *OCRService) Baca(ctx context.Context, orderID string) (OCRHasil, error) {
	if s == nil || !s.Tersedia() {
		return OCRHasil{}, ErrOCRBelumTerpasang
	}
	out := OCRHasil{OrderID: orderID}

	// 1. Setelan: AI mati berarti pekerjaan ini tidak memanggil provider dan
	//    tidak menyentuh keadaan pesanan sama sekali (PRD §5, SystemDesign
	//    §4.3: "ai.aktif = false → antre verifikasi manual").
	rules, err := s.ai.AISettings(ctx)
	if err != nil {
		return out, fmt.Errorf("baca setelan ai: %w", err)
	}
	if !rules.Aktif {
		out.Dilewati, out.Sebab = true, "setelan ai.aktif=false"
		return out, nil
	}

	// 2. Hanya pesanan yang sudah dikirim yang bisa diverifikasi.
	ord, err := s.repo.OrderForOCR(ctx, orderID)
	if err != nil {
		return out, err
	}
	if ord.Status != StatusSubmitted {
		out.Dilewati, out.Sebab = true, "pesanan tidak berstatus submitted"
		return out, nil
	}

	proofs, err := s.repo.ListProofsForOCR(ctx, orderID)
	if err != nil {
		return out, fmt.Errorf("baca bukti pesanan: %w", err)
	}
	if len(proofs) == 0 {
		out.Dilewati, out.Sebab = true, "tidak ada bukti untuk dibaca"
		return out, nil
	}
	out.JumlahBukti = len(proofs)

	// 3. Baca bukti yang BELUM pernah dibaca. Sebuah kegagalan provider di
	//    tengah jalan menghentikan pekerjaan tanpa menyimpan apa pun untuk
	//    bukti yang gagal (River akan mengulanginya).
	for i := range proofs {
		if proofs[i].At != nil {
			continue // sudah dibaca: tidak memanggil provider lagi
		}
		hasil, err := s.bacaSatu(ctx, ord, proofs[i], rules)
		if err != nil {
			return out, err
		}
		proofs[i].Nominal = hasil.Nominal
		proofs[i].Tanggal = hasil.Tanggal
		proofs[i].Bank = hasil.Bank
		proofs[i].RekAkhir = hasil.RekAkhir
		proofs[i].Confidence = hasil.Confidence
		at := s.sekarang()
		proofs[i].At = &at
		out.Dibaca++
	}

	// 4. Penilaian: jumlahkan nominal, bandingkan rekening + tanggal +
	//    toleransi (PRD §5).
	rekening, err := s.rek.Active(ctx)
	if err != nil {
		return out, fmt.Errorf("baca rekening aktif: %w", err)
	}
	pembacaan := make([]PembacaanBukti, 0, len(proofs))
	for _, p := range proofs {
		pembacaan = append(pembacaan, PembacaanBukti{
			ProofID:    p.ID,
			Nominal:    p.Nominal,
			Tanggal:    p.Tanggal,
			Bank:       p.Bank,
			RekAkhir:   p.RekAkhir,
			Confidence: p.Confidence,
		})
	}
	keputusan := Nilai(ord.Total, tanggalPesanan(ord), pembacaan, rekening, rules)
	out.Keputusan = &keputusan

	// 5. Penjaga idempotensi: keputusan untuk himpunan bukti yang sama tidak
	//    ditulis dua kali.
	sudah, err := s.repo.SudahDinilaiAI(ctx, orderID)
	if err != nil {
		return out, fmt.Errorf("periksa keputusan sebelumnya: %w", err)
	}
	if sudah {
		out.Dilewati, out.Sebab = true, "sudah dinilai untuk himpunan bukti ini"
		return out, nil
	}

	// 6. Keputusan diteruskan ke SATU pintu transisi Done (metode `ai`).
	total := keputusan.TotalTerbaca
	in := VerifyInput{
		Hasil:        keputusan.Hasil,
		Metode:       MetodeAI,
		Alasan:       keputusan.Alasan,
		Catatan:      keputusan.Catatan,
		TotalTerbaca: &total,
	}
	if keputusan.Hasil == VerifPerluReview && !keputusan.nominalDapatDipercaya() {
		// Nominal belum bisa dipercaya sama sekali (ada bukti yang tidak
		// terbaca): jangan mengaku "nominal terbaca 0" — simpan NULL supaya
		// selisih tidak membohongi pembaca log (Database.md §4.5).
		in.TotalTerbaca = nil
	}
	hasilVerif, err := s.verify.MarkVerified(ctx, SystemActor(), orderID, in)
	if err != nil {
		if errors.Is(err, ErrAlreadyDone) || errors.Is(err, ErrNotFound) {
			// Pesanan sudah selesai/dihapus di antara pembacaan dan
			// keputusan: tidak ada yang perlu dilakukan, bukan kegagalan.
			out.Dilewati, out.Sebab = true, "pesanan sudah selesai diverifikasi"
			return out, nil
		}
		return out, err
	}
	out.Outcome = &hasilVerif
	return out, nil
}

// bacaSatu membaca satu bukti dari object storage lalu menyerahkannya ke
// provider, dan menyimpan hasilnya bersama entri audit raw yang sudah bersih.
func (s *OCRService) bacaSatu(ctx context.Context, ord OrderOCR, p ProofOCR, rules settings.AISettings) (ReadResult, error) {
	objek, err := s.store.Get(ctx, p.StorageKey)
	if err != nil {
		return ReadResult{}, fmt.Errorf("%w: ambil bukti dari storage: %v", ErrReaderUnavailable, err)
	}
	isi, err := io.ReadAll(objek)
	_ = objek.Close()
	if err != nil {
		return ReadResult{}, fmt.Errorf("%w: baca isi bukti: %v", ErrReaderGagal, err)
	}
	hasil, err := s.baca.Read(ctx, ReadInput{
		OrderID:  ord.ID,
		ProofID:  p.ID,
		NamaFile: p.NamaFile,
		MimeType: p.MimeType,
		Content:  isi,
	})
	if err != nil {
		// Tanpa penyimpanan: percobaan berikutnya membaca ulang bukti ini.
		return ReadResult{}, err
	}
	hasil.Raw = SanitizeRaw(hasil.Raw)
	hasil.Confidence = batasiKeyakinan(hasil.Confidence)
	rec := audit.NewEntry(ctx, SystemActor(), audit.EntityOrder, ord.ID, audit.ActionBacaBukti,
		nil, ringkasanBacaan(p.ID, hasil, rules))
	if err := s.repo.SimpanBacaan(ctx, SimpanBacaanRequest{
		ProofID: p.ID,
		OrderID: ord.ID,
		Hasil:   hasil,
		Raw:     hasil.Raw,
		Audit:   rec,
	}); err != nil {
		return ReadResult{}, fmt.Errorf("simpan hasil baca bukti %s: %w", p.ID, err)
	}
	return hasil, nil
}

// tanggalPesanan mengembalikan tanggal bisnis pesanan (tengah malam) atau
// waktu nol bila pesanan belum bertanggal (tidak terjadi untuk pesanan yang
// sudah dikirim, tetapi tetap dijaga).
func tanggalPesanan(ord OrderOCR) time.Time {
	if ord.Tanggal == nil {
		return time.Time{}
	}
	return *ord.Tanggal
}

// nominalDapatDipercaya melaporkan apakah Σ nominal pada keputusan ini layak
// dipercaya. Keputusan "tidak terbaca" berarti nominalnya (sebagian atau
// seluruhnya) tidak diketahui sehingga tidak boleh dicatat sebagai angka.
func (k Keputusan) nominalDapatDipercaya() bool {
	return k.Alasan != AlasanTidakTerbaca
}

// Nilai memutuskan hasil verifikasi otomatis dari bacaan seluruh bukti.
//
// Urutan pemeriksaan disengaja: keterbacaan lebih dulu (nominal/tanggal/
// rekening yang tidak terbaca membuat pemeriksaan lain tak bermakna), lalu
// rekening, lalu tanggal, terakhir kecocokan nominal. Semua nilai diambil dari
// parameter; fungsi ini murni sehingga bisa diuji tanpa database.
func Nilai(totalPesanan int64, tanggalOrder time.Time, bukti []PembacaanBukti, rekeningAktif []bank.Account, rules settings.AISettings) Keputusan {
	k := Keputusan{Hasil: VerifCocok, JumlahBukti: len(bukti), KeyakinanTerendah: 1}
	if len(bukti) == 0 {
		return k.tolak(AlasanTidakTerbaca, "tidak ada bukti yang dapat dibaca")
	}

	// 1. Keterbacaan: nominal ada, dan keyakinan provider memadai.
	var jumlah int64
	for _, b := range bukti {
		if b.Confidence < k.KeyakinanTerendah {
			k.KeyakinanTerendah = b.Confidence
		}
		if b.Nominal == nil {
			return k.tolak(AlasanTidakTerbaca, "nominal tidak terbaca pada salah satu bukti")
		}
		jumlah += *b.Nominal
	}
	k.TotalTerbaca = jumlah
	for _, b := range bukti {
		if b.Confidence < rules.MinKeyakinan {
			return k.tolak(AlasanTidakTerbaca, fmt.Sprintf(
				"keyakinan %.3f di bawah ambang %.3f", b.Confidence, rules.MinKeyakinan))
		}
	}

	// 2. Rekening tujuan harus salah satu rekening AKTIF (digit akhir).
	if len(rekeningAktif) == 0 {
		return k.tolak(AlasanRekeningBeda, "tidak ada rekening aktif sebagai acuan")
	}
	for _, b := range bukti {
		if strings.TrimSpace(b.RekAkhir) == "" {
			return k.tolak(AlasanTidakTerbaca, "nomor rekening tujuan tidak terbaca")
		}
		if !rekeningCocok(b.RekAkhir, rules.DigitRek, rekeningAktif) {
			return k.tolak(AlasanRekeningBeda, "rekening tujuan tidak cocok dengan rekening aktif")
		}
	}

	// 3. Tanggal transfer tidak boleh lebih tua dari tanggal pesanan − mundur.
	batas := tanggalOrder.AddDate(0, 0, -int(rules.Mundur))
	for _, b := range bukti {
		if b.Tanggal == nil {
			return k.tolak(AlasanTidakTerbaca, "tanggal transfer tidak terbaca")
		}
		if !tanggalOrder.IsZero() && b.Tanggal.Before(batas) {
			return k.tolak(AlasanLainnya, fmt.Sprintf(
				"tanggal transfer %s lebih tua dari batas %s (pesanan %s − %d hari)",
				b.Tanggal.Format("2006-01-02"), batas.Format("2006-01-02"), tanggalOrder.Format("2006-01-02"), rules.Mundur))
		}
	}

	// 4. Kecocokan nominal: Σ bukti terhadap total pesanan + toleransi.
	if k.TotalTerbaca < totalPesanan-rules.TolKurang {
		return k.tolak(AlasanKurangBayar, fmt.Sprintf(
			"nominal terbaca %d kurang dari total %d (toleransi %d)", k.TotalTerbaca, totalPesanan, rules.TolKurang))
	}
	if k.TotalTerbaca > totalPesanan+rules.TolLebih {
		return k.tolak(AlasanLebihBayar, fmt.Sprintf(
			"nominal terbaca %d lebih dari total %d (toleransi %d)", k.TotalTerbaca, totalPesanan, rules.TolLebih))
	}
	k.Catatan = fmt.Sprintf("AI/OCR: %d bukti, keyakinan terendah %.3f, total terbaca %d",
		k.JumlahBukti, k.KeyakinanTerendah, k.TotalTerbaca)
	return k
}

// tolak mengubah keputusan menjadi perlu review dengan alasan + catatan.
func (k Keputusan) tolak(alasan, sebab string) Keputusan {
	k.Hasil = VerifPerluReview
	k.Alasan = alasan
	k.Catatan = ringkasCatatan("AI/OCR: " + sebab + fmt.Sprintf(
		" (keyakinan terendah %.3f, total terbaca %d)", k.KeyakinanTerendah, k.TotalTerbaca))
	return k
}

// ringkasCatatan membatasi panjang catatan agar tidak melampaui kolom
// verifikasi (MaxCatatanVerifikasi).
func ringkasCatatan(s string) string {
	if len([]rune(s)) <= MaxCatatanVerifikasi {
		return s
	}
	r := []rune(s)
	return string(r[:MaxCatatanVerifikasi])
}

// rekeningCocok melaporkan apakah nomor rekening hasil bacaan cocok dengan
// salah satu rekening aktif berdasarkan `digit` digit terakhirnya. Pemisah
// (spasi/tanda hubung) diabaikan.
//
// Hasil baca yang digitnya LEBIH PENDEK dari `digit_rek` tidak pernah dianggap
// cocok: jumlah digit yang diminta setelan adalah bagian dari janji
// pemeriksaan, dan mencocokkan lebih sedikit digit daripada yang disetel akan
// meloloskan rekening yang salah (fail closed → perlu review).
func rekeningCocok(baca string, digit int64, rekening []bank.Account) bool {
	b := digitSaja(baca)
	n := int(digit)
	if n <= 0 || len(b) < n {
		return false
	}
	ekorBaca := b[len(b)-n:]
	for _, r := range rekening {
		a := digitSaja(r.Nomor)
		if len(a) < n {
			continue
		}
		if a[len(a)-n:] == ekorBaca {
			return true
		}
	}
	return false
}

// digitSaja membuang semua karakter selain angka.
func digitSaja(s string) string {
	var out strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// kunciRahasia mengenali nama kunci JSON yang nilainya tidak boleh tercatat
// (kunci API, token, kredensial) — dipakai SanitizeRaw.
var kunciRahasia = regexp.MustCompile(`(?i)(api[-_ ]?key|secret|token|password|passwd|authorization|bearer|credential|private[-_ ]?key|access[-_ ]?key)`)

// SanitizeRaw membersihkan jawaban mentah provider sebelum disimpan ke
// `ocr_raw` dan entri audit: setiap nilai dengan nama kunci yang tampak
// rahasia diganti penanda, secara rekursif. Jawaban yang bukan JSON valid
// dikembalikan nil supaya tidak ada sampah/tak terduga yang tersimpan.
func SanitizeRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	bersih, err := json.Marshal(sensor(v))
	if err != nil {
		return nil
	}
	return json.RawMessage(bersih)
}

// sensor menelusuri nilai JSON dan menyensor nilai berkunci rahasia.
func sensor(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if kunciRahasia.MatchString(k) {
				out[k] = "[disensor]"
				continue
			}
			out[k] = sensor(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, val := range t {
			out = append(out, sensor(val))
		}
		return out
	default:
		return v
	}
}

// ringkasanBacaan menyusun snapshot audit satu hasil baca. Nilainya TIDAK
// pernah memuat kredensial: `hasil.Raw` sudah dibersihkan SanitizeRaw.
func ringkasanBacaan(proofID string, hasil ReadResult, rules settings.AISettings) map[string]any {
	out := map[string]any{
		"bukti_id":  proofID,
		"keyakinan": hasil.Confidence,
		"ambang":    rules.MinKeyakinan,
		"bank":      hasil.Bank,
		"rek_akhir": hasil.RekAkhir,
		"raw":       hasil.Raw,
	}
	if hasil.Nominal != nil {
		out["nominal"] = *hasil.Nominal
	}
	if hasil.Tanggal != nil {
		out["tanggal"] = hasil.Tanggal.Format("2006-01-02")
	}
	return out
}
