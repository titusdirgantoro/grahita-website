package payment

// Berkas ini memuat VERIFIKASI PEMBAYARAN MANUAL dan transisi Submitted -> Done
// (kartu F1-18; PRD §4.2 AP-ORD-07, §5, §7; SystemDesign.md §4.3–4.4;
// Database.md §4.4–4.6).
//
// Satu jalur untuk semua cara verifikasi. Permintaan HTTP admin memanggil
// `VerificationService.MarkVerified` dengan metode `manual`; verifikasi OCR/AI
// (Fase 2) dan callback Virtual Account (Fase 3) nanti memanggil method YANG
// SAMA dengan metode `ai`/`virtual_account`. Karena itu efek sampingnya —
// status + done_at, log verifikasi, timeline, audit, dan penjadwalan
// dokumen/email — selalu identik, tidak peduli siapa yang memutuskan.
//
// Keputusan yang diambil di lapisan ini:
//
//   - hanya staf Pusat ber-kapabilitas `admin.verify` (Manager maupun
//     Superadmin, PRD §2.1) yang boleh memverifikasi; akun cabang ditolak;
//   - `cocok` berarti MENYETUJUI: pesanan berpindah ke Done. `perlu_review`
//     berarti menandai sub-status verifikasi dan alasan WAJIB diisi dari daftar
//     tertutup (kurang bayar / lebih bayar / tidak terbaca / rekening berbeda /
//     lainnya) — pesanan TETAP Submitted (PRD §7.1);
//   - `total_terbaca` adalah nominal yang dibaca admin dari bukti (atau diisi
//     manual untuk bukti non-OCR); selisih dihitung kolom generated di database
//     sebagai total_terbaca − total_pesanan, jadi angkanya tidak pernah datang
//     dari perhitungan klien;
//   - keputusan approve TIDAK dipaksakan selisih nol: keputusan akhir selalu
//     milik manusia (PRD §5). Angka selisih tetap tercatat untuk audit;
//   - tepat-sekali: transisi (dan seluruh efek sampingnya) hanya terjadi bila
//     pesanan masih Submitted. Dua verifikasi bersamaan diserialkan kunci baris
//     pesanan; yang kedua dijawab ErrAlreadyDone tanpa menulis apa pun.
//
// Penulisan sesungguhnya (status, log, timeline, audit, penjadwalan pekerjaan
// dokumen/email) dilakukan repository DI DALAM SATU transaksi, karena hanya di
// sana baris pesanan bisa dikunci dan keputusan diperiksa ulang.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Hasil verifikasi (mirror enum verif_status, Database.md §3).
const (
	// VerifCocok berarti pembayaran disetujui (pesanan berpindah ke Done).
	VerifCocok = "cocok"
	// VerifPerluReview berarti pesanan tetap Submitted dengan sub-status
	// verifikasi perlu_review (PRD §7.1).
	VerifPerluReview = "perlu_review"
)

// Metode verifikasi (mirror enum verify_method, Database.md §3).
const (
	MetodeManual         = "manual"
	MetodeAI             = "ai"
	MetodeVirtualAccount = "virtual_account"
)

// Alasan review (mirror enum review_reason, PRD AP-ORD-07).
const (
	AlasanKurangBayar  = "kurang_bayar"
	AlasanLebihBayar   = "lebih_bayar"
	AlasanTidakTerbaca = "tidak_terbaca"
	AlasanRekeningBeda = "rekening_beda"
	AlasanLainnya      = "lainnya"
)

// MaxCatatanVerifikasi membatasi panjang catatan verifikasi (kolom text).
const MaxCatatanVerifikasi = 2000

// Identitas AKTOR SISTEM untuk keputusan verifikasi otomatis (kartu F2-26).
//
// Nilainya sengaja konstan dan bukan akun nyata: verifikasi AI tidak punya
// sesi, tidak bisa login, dan TIDAK boleh muncul sebagai pemeriksa manusia —
// `verification_logs.admin_id` karena itu tetap NULL untuk metode `ai`
// (Database.md §4.5). Pola yang sama dipakai pekerjaan latar lain
// (backup/notifikasi: id 00000000-0000-0000-0000-000000000000).
const (
	// AktorSistemAI adalah id mesin untuk keputusan verifikasi otomatis.
	AktorSistemAI = "00000000-0000-0000-0000-000000000000"
	// NamaAktorSistemAI adalah penanda mesin pada audit/timeline.
	NamaAktorSistemAI = "verifikasi-ai"
)

// SystemActor mengembalikan aktor mesin untuk keputusan verifikasi otomatis.
// Aktor ini HANYA diterima untuk metode `ai` (lihat penjaga di MarkVerified).
func SystemActor() auth.Actor {
	return auth.Actor{UserID: AktorSistemAI, Kind: auth.KindSystem, Nama: NamaAktorSistemAI}
}

// Sentinel error domain verifikasi. Pemetaan ke bentuk HTTP ada di transport.
var (
	// ErrInvalidData adalah kesalahan bentuk data verifikasi (dipakai sebagai
	// pembungkus agar pesannya tetap spesifik).
	ErrInvalidData = errors.New("data verifikasi tidak valid")
	// ErrNotSubmitted dikembalikan bila verifikasi diminta untuk pesanan yang
	// belum dikirim (masih Draft). Draft belum punya nomor/tanggal sehingga
	// tidak pernah bisa berstatus Done (constraint draft_tanpa_nomor).
	ErrNotSubmitted = errors.New("verifikasi hanya untuk pesanan yang sudah dikirim")
	// ErrAlreadyDone dikembalikan bila pesanan sudah berstatus Done: verifikasi
	// kedua TIDAK menghasilkan dokumen, email, log, atau audit kedua.
	ErrAlreadyDone = errors.New("pesanan sudah selesai diverifikasi")
	// ErrHasilInvalid dikembalikan bila hasil verifikasi bukan cocok/perlu_review.
	ErrHasilInvalid = fmt.Errorf("%w: hasil verifikasi harus %q atau %q", ErrInvalidData, VerifCocok, VerifPerluReview)
	// ErrAlasanInvalid dikembalikan bila alasan review tidak diisi padahal
	// diperlukan, diisi padahal tidak perlu, atau bukan salah satu pilihan.
	ErrAlasanInvalid = errors.New("alasan review tidak valid")
	// ErrNominalInvalid dikembalikan bila nominal terbaca negatif.
	ErrNominalInvalid = fmt.Errorf("%w: nominal terbaca tidak boleh negatif", ErrInvalidData)
	// ErrCatatanInvalid dikembalikan bila catatan melebihi batas panjang.
	ErrCatatanInvalid = fmt.Errorf("%w: catatan verifikasi maksimal %d karakter", ErrInvalidData, MaxCatatanVerifikasi)
	// ErrMetodeInvalid dikembalikan bila metode verifikasi tidak dikenal.
	ErrMetodeInvalid = errors.New("metode verifikasi tidak dikenal")
	// ErrQueueUnavailable dikembalikan bila penjadwal pekerjaan dokumen/email
	// belum terpasang: pesanan TIDAK diselesaikan (fail closed) supaya tidak
	// pernah ada pesanan Done tanpa dokumen dan email.
	ErrQueueUnavailable = errors.New("penjadwal pekerjaan dokumen belum tersedia")
	// ErrVerifierUnavailable dikembalikan bila penyimpan verifikasi belum
	// terpasang pada service (kesalahan wiring, bukan kesalahan pemanggil).
	ErrVerifierUnavailable   = errors.New("layanan penyimpanan verifikasi belum terpasang")
	ErrDestinationRequired   = errors.New("setiap bukti harus dipetakan ke rekening tujuan")
	ErrDestinationNotAllowed = errors.New("rekening tujuan bukti tidak efektif untuk cabang")
)

type ProofDestination struct {
	PaymentProofID string `json:"payment_proof_id"`
	BankAccountID  string `json:"bank_account_id"`
}

type DestinationResult struct {
	PaymentProofID            string `json:"payment_proof_id"`
	Source                    string `json:"source"`
	Status                    string `json:"status"`
	BankAccountID             string `json:"bank_account_id,omitempty"`
	BankSnapshot              string `json:"bank_snapshot,omitempty"`
	AccountOwnerSnapshot      string `json:"account_owner_snapshot,omitempty"`
	AccountLastDigitsSnapshot string `json:"account_last_digits_snapshot,omitempty"`
}

// VerifyInput adalah keputusan verifikasi yang datang dari pemanggil
// (permintaan admin atau, kelak, keputusan AI).
//
// Metode sengaja TIDAK datang dari klien pada endpoint admin: handler memakai
// MetodeManual supaya permintaan HTTP tidak bisa mengaku sebagai AI.
type VerifyInput struct {
	// Hasil adalah VerifCocok (setujui) atau VerifPerluReview (tandai review).
	Hasil string
	// Metode adalah salah satu MetodeManual/MetodeAI/MetodeVirtualAccount.
	Metode string
	// TotalTerbaca adalah nominal yang dibaca/diisi manual. nil berarti tidak
	// ada nominal terbaca (tersimpan NULL, selisih dihitung 0 − total).
	TotalTerbaca *int64
	// Alasan wajib diisi (dan hanya diisi) ketika Hasil = VerifPerluReview.
	Alasan string
	// Catatan bebas opsional (maksimal MaxCatatanVerifikasi karakter).
	Catatan           string
	ProofDestinations []ProofDestination
	// Expected* hanya diisi worker reevaluate agar transaksi final menolak job
	// bila policy atau himpunan bukti berubah setelah pembacaan awal.
	ExpectedAllowlistVersion int64
	ExpectedProofFingerprint string
}

// VerificationLog adalah satu baris `verification_logs` (Database.md §4.5).
type VerificationLog struct {
	ID string `json:"id"`
	// OrderID adalah id pesanan yang diverifikasi.
	OrderID string `json:"order_id"`
	// Metode adalah manual/ai/virtual_account.
	Metode string `json:"metode"`
	// TotalPesanan adalah total pesanan saat verifikasi (dibaca dari pesanan).
	TotalPesanan int64 `json:"total_pesanan"`
	// TotalTerbaca adalah nominal yang dibaca/diisi (0 bila belum terbaca).
	TotalTerbaca int64 `json:"total_terbaca"`
	// Selisih = TotalTerbaca − TotalPesanan (kolom generated di database).
	Selisih int64 `json:"selisih"`
	// Hasil adalah cocok/perlu_review.
	Hasil string `json:"hasil"`
	// Alasan terisi hanya bila Hasil = perlu_review.
	Alasan string `json:"alasan,omitempty"`
	// AdminNama adalah nama pemeriksa; kosong bila diverifikasi AI.
	AdminNama string `json:"admin_nama,omitempty"`
	// Catatan bebas dari pemeriksa.
	Catatan                string              `json:"catatan,omitempty"`
	CreatedAt              time.Time           `json:"created_at"`
	BranchAllowlistVersion int64               `json:"branch_allowlist_version"`
	DestinationResults     []DestinationResult `json:"destination_results"`
}

// VerifiedOrder adalah ringkasan pesanan setelah verifikasi.
type VerifiedOrder struct {
	ID          string `json:"id"`
	Nomor       string `json:"nomor"`
	Versi       int    `json:"versi"`
	BranchID    string `json:"branch_id"`
	Status      string `json:"status"`
	VerifStatus string `json:"verif_status"`
	// AlasanReview terisi hanya bila verif_status = perlu_review.
	AlasanReview string `json:"alasan_review,omitempty"`
	Total        int64  `json:"total"`
}

// VerifyOutcome adalah hasil satu permintaan verifikasi: keadaan pesanan
// sesudahnya beserta log yang tersimpan.
type VerifyOutcome struct {
	Order VerifiedOrder   `json:"order"`
	Log   VerificationLog `json:"verifikasi"`
}

// MarkVerifiedRequest adalah permintaan yang sudah divalidasi service dan
// diserahkan ke penyimpanan untuk dijalankan dalam SATU transaksi.
//
// Actor ikut supaya audit dan timeline mencatat pelakunya apa adanya. Audit
// dibangun service (Before/After dasar) lalu DISEMPURNAKAN repository dari
// baris pesanan yang dikunci — snapshot sebelum/sesudah tidak boleh basi.
type MarkVerifiedRequest struct {
	OrderID                  string
	Hasil                    string
	Metode                   string
	TotalTerbaca             *int64
	Alasan                   string
	Catatan                  string
	Actor                    auth.Actor
	Audit                    audit.Entry
	ProofDestinations        []ProofDestination
	ExpectedAllowlistVersion int64
	ExpectedProofFingerprint string
}

// Verifier adalah sisi penyimpanan verifikasi. Implementasinya menegakkan
// transisi di dalam satu transaksi: kunci baris pesanan, periksa status,
// tulis status/verif/done_at, log verifikasi, timeline, audit, dan titipkan
// pekerjaan lanjutan (render dokumen + email) pada transaksi yang sama.
type Verifier interface {
	MarkVerified(ctx context.Context, req MarkVerifiedRequest) (VerifyOutcome, error)
}

// VerificationService adalah logika bisnis verifikasi pembayaran.
type VerificationService struct {
	repo Verifier
}

// NewVerificationService membuat service verifikasi. repo tidak boleh nil.
func NewVerificationService(repo Verifier) *VerificationService {
	return &VerificationService{repo: repo}
}

// MarkVerified menjalankan verifikasi pembayaran satu pesanan: menyetujui
// (Submitted -> Done) atau menandainya perlu review.
//
// Ini satu-satunya pintu transisi Done, dipakai verifikasi manual sekarang dan
// AI/OCR maupun Virtual Account nanti.
func (s *VerificationService) MarkVerified(ctx context.Context, actor auth.Actor, orderID string, in VerifyInput) (VerifyOutcome, error) {
	// Metode ditentukan lebih dulu: ia menentukan siapa yang boleh memanggil
	// (aktor sistem hanya untuk `ai`, lihat verifikatorSah).
	metode := strings.TrimSpace(in.Metode)
	if metode == "" {
		// Bawaan: verifikasi manual. Handler admin selalu mengirim yang
		// eksplisit supaya jejaknya jelas.
		metode = MetodeManual
	}
	if !MetodeValid(metode) {
		return VerifyOutcome{}, fmt.Errorf("%w: %q", ErrMetodeInvalid, in.Metode)
	}
	if err := verifikatorSah(actor, metode); err != nil {
		return VerifyOutcome{}, err
	}
	id := strings.TrimSpace(orderID)
	if !uuidRe.MatchString(id) {
		// Bentuk id yang bukan UUID dijawab 404 (bukan 500 SQLSTATE 22P02) —
		// sama seperti jalur admin lain.
		return VerifyOutcome{}, ErrNotFound
	}
	req, err := s.siapkan(ctx, actor, id, in)
	if err != nil {
		return VerifyOutcome{}, err
	}
	if s.repo == nil {
		// Kesalahan wiring: lebih baik gagal terang-terangan daripada
		// melaporkan verifikasi yang tidak pernah tersimpan.
		return VerifyOutcome{}, ErrVerifierUnavailable
	}
	return s.repo.MarkVerified(ctx, req)
}

// siapkan menegakkan aturan yang bisa diputuskan tanpa database dan menyusun
// permintaan final untuk repository.
func (s *VerificationService) siapkan(ctx context.Context, actor auth.Actor, orderID string, in VerifyInput) (MarkVerifiedRequest, error) {
	hasil := strings.TrimSpace(in.Hasil)
	if !HasilValid(hasil) {
		return MarkVerifiedRequest{}, ErrHasilInvalid
	}
	metode := strings.TrimSpace(in.Metode)
	if metode == "" {
		// Bawaan: verifikasi manual. Handler admin selalu mengirim yang
		// eksplisit supaya jejaknya jelas.
		metode = MetodeManual
	}
	if !MetodeValid(metode) {
		return MarkVerifiedRequest{}, fmt.Errorf("%w: %q", ErrMetodeInvalid, in.Metode)
	}
	if in.TotalTerbaca != nil && *in.TotalTerbaca < 0 {
		return MarkVerifiedRequest{}, ErrNominalInvalid
	}
	alasan := strings.TrimSpace(in.Alasan)
	if err := ValidasiAlasan(hasil, alasan); err != nil {
		return MarkVerifiedRequest{}, err
	}
	catatan := strings.TrimSpace(in.Catatan)
	if len([]rune(catatan)) > MaxCatatanVerifikasi {
		return MarkVerifiedRequest{}, ErrCatatanInvalid
	}

	req := MarkVerifiedRequest{
		OrderID:                  orderID,
		Hasil:                    hasil,
		Metode:                   metode,
		TotalTerbaca:             in.TotalTerbaca,
		Alasan:                   alasan,
		Catatan:                  catatan,
		Actor:                    actor,
		ProofDestinations:        append([]ProofDestination(nil), in.ProofDestinations...),
		ExpectedAllowlistVersion: in.ExpectedAllowlistVersion,
		ExpectedProofFingerprint: strings.ToLower(strings.TrimSpace(in.ExpectedProofFingerprint)),
	}
	if hasil == VerifCocok {
		seen := map[string]bool{}
		for _, d := range req.ProofDestinations {
			proofID := strings.TrimSpace(d.PaymentProofID)
			accountID := strings.TrimSpace(d.BankAccountID)
			if !uuidRe.MatchString(proofID) || !uuidRe.MatchString(accountID) || seen[proofID] {
				return MarkVerifiedRequest{}, ErrDestinationRequired
			}
			seen[proofID] = true
		}
	}
	// Audit disusun di sini supaya aktor + IP request tercatat; Before/After
	// diisi repository dari baris yang dikunci (lihat MarkVerifiedRequest).
	req.Audit = audit.NewEntry(ctx, actor, audit.EntityOrder, orderID, audit.ActionVerify, nil, nil)
	return req, nil
}

// verifikatorSah menegakkan PRD §2.1: hanya staf Pusat ber-kapabilitas
// `admin.verify` (Manager maupun Superadmin) yang boleh memverifikasi
// pembayaran. Akun cabang ditolak — cabang tidak pernah memutuskan pembayaran
// pesanannya sendiri.
//
// Satu pengecualian yang disengaja (kartu F2-26): aktor SISTEM
// (`auth.KindSystem`) — mesin pembaca bukti AI — boleh memverifikasi HANYA
// dengan metode `ai`. Aktor sistem tidak pernah diterima untuk metode
// `manual`/`virtual_account`, sehingga tidak ada jalan bagi pekerjaan latar
// menulis verifikasi yang mengaku dibuat manusia.
func verifikatorSah(actor auth.Actor, metode string) error {
	if actor.Kind == auth.KindSystem {
		if metode == MetodeAI {
			return nil
		}
		return fmt.Errorf("%w: aktor sistem hanya boleh memakai metode %q", ErrForbidden, MetodeAI)
	}
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminVerify) {
		return ErrForbidden
	}
	return nil
}

// HasilValid melaporkan apakah hasil verifikasi dikenal.
func HasilValid(hasil string) bool {
	return hasil == VerifCocok || hasil == VerifPerluReview
}

// MetodeValid melaporkan apakah metode verifikasi dikenal.
func MetodeValid(metode string) bool {
	switch metode {
	case MetodeManual, MetodeAI, MetodeVirtualAccount:
		return true
	}
	return false
}

// ValidasiAlasan menegakkan hubungan hasil ↔ alasan:
//
//   - perlu review WAJIB punya alasan dari daftar tertutup (kurang bayar,
//     lebih bayar, tidak terbaca, rekening berbeda, lainnya);
//   - persetujuan TIDAK boleh membawa alasan review — alasan yang tertinggal
//     dari pilihan sebelumnya tidak boleh tersimpan diam-diam.
func ValidasiAlasan(hasil, alasan string) error {
	switch hasil {
	case VerifPerluReview:
		if alasan == "" {
			return fmt.Errorf("%w: alasan wajib diisi saat menandai perlu review", ErrAlasanInvalid)
		}
		if !AlasanValid(alasan) {
			return fmt.Errorf("%w: %q bukan pilihan alasan", ErrAlasanInvalid, alasan)
		}
	case VerifCocok:
		if alasan != "" {
			return fmt.Errorf("%w: persetujuan tidak memakai alasan review", ErrAlasanInvalid)
		}
	}
	return nil
}

// AlasanValid melaporkan apakah alasan review dikenal (PRD AP-ORD-07).
func AlasanValid(alasan string) bool {
	switch alasan {
	case AlasanKurangBayar, AlasanLebihBayar, AlasanTidakTerbaca, AlasanRekeningBeda, AlasanLainnya:
		return true
	}
	return false
}

// LabelAlasan mengubah kode alasan menjadi kalimat yang bisa dibaca manusia
// di timeline (mis. "kurang bayar").
func LabelAlasan(alasan string) string {
	switch alasan {
	case AlasanKurangBayar:
		return "kurang bayar"
	case AlasanLebihBayar:
		return "lebih bayar"
	case AlasanTidakTerbaca:
		return "bukti tidak terbaca"
	case AlasanRekeningBeda:
		return "rekening tidak sesuai"
	case AlasanLainnya:
		return "lainnya"
	}
	return alasan
}

// TeksEventSetuju menyusun kalimat timeline saat admin menyetujui pembayaran
// (PRD CP-ORD-08). Nama pelaku ditulis apa adanya.
func TeksEventSetuju(nama string) string {
	return "Pembayaran diverifikasi dan pesanan selesai oleh " + namaPelaku(nama) + " (admin Pusat)"
}

// TeksEventPerluReview menyusun kalimat timeline saat admin menandai pesanan
// perlu review, lengkap dengan alasannya.
func TeksEventPerluReview(nama, alasan string) string {
	return "Pembayaran ditandai perlu review (" + LabelAlasan(alasan) + ") oleh " + namaPelaku(nama) + " (admin Pusat)"
}

// TeksEventSetujuSistem menyusun kalimat timeline saat pembayaran disetujui
// OTOMATIS oleh sistem (verifikasi AI/OCR, kartu F2-26). Dipisahkan dari
// kalimat verifikasi manusia supaya timeline tidak pernah mengaku dikerjakan
// admin padahal tidak ada admin yang memutuskan (PRD §5).
func TeksEventSetujuSistem() string {
	return "Pembayaran diverifikasi otomatis oleh sistem (AI) dan pesanan selesai"
}

// TeksEventPerluReviewSistem menyusun kalimat timeline saat sistem menandai
// pesanan perlu review; keputusan akhir tetap milik manusia (PRD §5).
func TeksEventPerluReviewSistem(alasan string) string {
	return "Pembayaran ditandai perlu review (" + LabelAlasan(alasan) + ") oleh sistem (AI)"
}

// namaPelaku memakai nama admin apa adanya; akun tanpa nama memakai sebutan
// netral supaya timeline tidak pernah kosong.
func namaPelaku(nama string) string {
	nama = strings.TrimSpace(nama)
	if nama == "" {
		return "admin Pusat"
	}
	return nama
}
