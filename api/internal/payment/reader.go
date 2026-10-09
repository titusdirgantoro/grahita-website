package payment

// Berkas ini memuat ABSTRAKSI PEMBACA BUKTI TRANSFER: provider OCR/AI ada di
// balik interface `PaymentReader` (Architecture.md §1 dan §11) sehingga
// provider bisa ditukar — Claude vision sekarang, mock deterministik untuk
// lingkungan lokal/pengujian, atau penyedia lain kelak — tanpa mengubah domain
// verifikasi (kartu F2-26; PRD §5; SystemDesign.md §4.3).
//
// Batas tanggung jawab:
//
//   - pembaca HANYA mengubah isi satu berkas bukti menjadi bacaan terstruktur
//     (nominal, tanggal, bank, digit akhir rekening, keyakinan) beserta
//     jawaban mentah provider untuk audit;
//   - pembaca TIDAK pernah memutuskan apa pun: keputusan verifikasi adalah
//     pekerjaan `Nilai` + `VerificationService.MarkVerified` (satu-satunya
//     pintu transisi Done, PRD §5 "AI tidak pernah menolak secara final");
//   - kunci/kredensial provider TIDAK pernah masuk ke hasil bacaan maupun log
//     (lihat `SanitizeRaw`).

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Sentinel error pembaca bukti. Pemetaan ke bentuk HTTP tidak ada: pembaca
// hanya dipanggil pekerjaan latar (worker), bukan permintaan HTTP.
var (
	// ErrReaderUnavailable dikembalikan bila provider belum bisa dipakai
	// (kunci API kosong, endpoint tidak terjangkau). Pekerjaan dikembalikan
	// sebagai galat supaya River mencobanya lagi; TIDAK ada keputusan
	// verifikasi yang ditulis dari kegagalan ini.
	ErrReaderUnavailable = errors.New("pembaca bukti (AI) belum tersedia")
	// ErrReaderGagal dikembalikan bila provider menjawab galat atau
	// jawabannya tidak dapat dipahami.
	ErrReaderGagal = errors.New("pembacaan bukti gagal")
	// ErrReaderJenis dikembalikan bila jenis berkas tidak dapat dibaca
	// provider (bukan JPG/PNG/PDF).
	ErrReaderJenis = errors.New("jenis bukti tidak dapat dibaca AI")
)

// ReadInput adalah satu bukti yang akan dibaca. Isi berkasnya sudah dibaca
// dari object storage oleh pemanggil (`storage.Store.Get`).
type ReadInput struct {
	// OrderID/ProofID hanya untuk konteks dan pencatatan; tidak dipakai
	// provider.
	OrderID  string
	ProofID  string
	NamaFile string
	// MimeType adalah jenis berkas yang sudah ditentukan server dari magic
	// bytes (image/jpeg, image/png, application/pdf) — bukan dari klien.
	MimeType string
	// Content adalah isi berkas apa adanya.
	Content []byte
}

// ReadResult adalah hasil pembacaan SATU bukti.
//
// Field yang tidak terbaca dibiarkan kosong (Nominal/Tanggal nil) — pembaca
// TIDAK boleh mengarang angka: kekosongan itulah yang membuat keputusan jatuh
// ke "perlu review (tidak terbaca)".
type ReadResult struct {
	// Nominal adalah nominal transfer yang terbaca (Rupiah bulat, tanpa sen).
	Nominal *int64
	// Tanggal adalah tanggal transfer yang terbaca.
	Tanggal *time.Time
	// Bank adalah kode bank tujuan (mis. "BCA"/"BRI") bila terbaca.
	Bank string
	// RekAkhir adalah nomor rekening tujuan yang terbaca (apa adanya; yang
	// dibandingkan hanya digit akhirnya).
	RekAkhir string
	// Confidence adalah keyakinan provider 0..1 (0 = tidak yakin/tidak
	// terbaca).
	Confidence float64
	// Raw adalah jawaban mentah provider (JSON) untuk audit. Nilai yang
	// tampak rahasia dibuang oleh SanitizeRaw sebelum disimpan.
	Raw json.RawMessage
}

// PaymentReader membaca satu bukti transfer menjadi bacaan terstruktur.
//
// Implementasi WAJIB gagal terang-terangan (ErrReaderUnavailable/ErrReaderGagal)
// alih-alih mengembalikan bacaan kosong yang "sukses": kegagalan provider
// harus terlihat sebagai pekerjaan yang diulang, bukan sebagai bukti yang
// kebetulan tidak terbaca.
type PaymentReader interface {
	Read(ctx context.Context, in ReadInput) (ReadResult, error)
}
