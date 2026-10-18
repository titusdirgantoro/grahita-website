// Package jobs memuat muatan pekerjaan latar belakang (River) yang dipakai
// bersama oleh PRODUSEN dan KONSUMEN-nya.
//
// Muatan job sengaja hidup di paket kecil tersendiri supaya repository domain
// yang menitipkan pekerjaan (mis. verifikasi pembayaran) dan worker yang
// menjalankannya tidak perlu saling mengimpor. Paket ini tidak bergantung pada
// paket internal lain — hanya pada River — sehingga aman dipakai di kedua sisi.
package jobs

// KindRenderDokumen adalah Kind pekerjaan "render dokumen pesanan lalu
// antre email dokumen".
const KindRenderDokumen = "render_dokumen_pesanan"

// RenderDokumenArgs meminta pekerjaan lanjutan setelah pesanan berstatus Done:
// render Invoice PDF + Surat Jalan XLSX/PDF, lalu antre email dokumen SETELAH
// dokumennya ada (SystemDesign.md §4.4, kartu F1-18).
//
// Job ini dititipkan DI DALAM transaksi verifikasi, jadi tidak mungkin terjadi
// "pesanan selesai tapi pekerjaan dokumennya hilang". Sifatnya idempoten:
// render dokumen memakai kunci unik (order_id, jenis, versi) dan pengantrean
// email hanya membuat baris yang belum ada, sehingga job yang dijalankan ulang
// (River bersifat at-least-once) tidak menggandakan dokumen maupun email.
type RenderDokumenArgs struct {
	// OrderID adalah id pesanan yang baru selesai.
	OrderID string `json:"order_id"`
}

// Kind mengidentifikasi jenis job di tabel River.
func (RenderDokumenArgs) Kind() string { return KindRenderDokumen }

// KindRevisiInvoice adalah Kind pekerjaan "render invoice versi baru lalu
// antre email invoice" setelah revisi invoice (F2-25, SystemDesign.md §4.5).
const KindRevisiInvoice = "revisi_invoice"

// RevisiInvoiceArgs meminta pekerjaan lanjutan setelah pesanan direvisi:
// render Invoice PDF VERSI BARU (tanpa menyentuh berkas versi lama), lalu
// mengantre email invoice ke cabang dan admin ber-flag Invoice. Surat Jalan
// TIDAK ikut dikirim ulang (PRD AP-REV-04, §6.2) — revisi hanya menyentuh
// invoice.
//
// Job ini dititipkan DI DALAM transaksi revisi, jadi tidak mungkin ada versi
// pesanan yang naik tanpa invoice versi barunya disiapkan. Sifatnya idempoten:
// render memakai kunci (order_id, jenis, versi) dan pengantrean email dijaga
// per (pesanan, jenis, versi), sehingga job yang dijalankan ulang tidak
// menggandakan dokumen maupun email.
type RevisiInvoiceArgs struct {
	// OrderID adalah id pesanan yang baru direvisi.
	OrderID string `json:"order_id"`
	// Versi adalah versi pesanan/invoice yang MENJADI HAK pekerjaan ini
	// (versi sesudah revisi). Pekerjaan dipatok ke versinya supaya revisi
	// yang sudah dijawab 200 tidak pernah kehilangan invoice versinya: worker
	// memastikan invoice VERSI INI ada, bukan invoice versi berjalan saat
	// worker kebetulan berjalan (temuan QA putaran 1, F2-25).
	//
	// Nilai <= 0 berarti "versi berjalan saat pekerjaan dijalankan" — sikap
	// kompatibel untuk job lama yang dititipkan sebelum kolom ini ada.
	Versi int `json:"versi"`
}

// Kind mengidentifikasi jenis job di tabel River.
func (RevisiInvoiceArgs) Kind() string { return KindRevisiInvoice }

// KindReportXLSX adalah Kind pekerjaan "tulis laporan XLSX lalu simpan ke
// object storage" (kartu F2-24, PRD §4.7 AP-REP-04).
const KindReportXLSX = "report_xlsx"

// ReportXLSXArgs meminta ekspor satu laporan XLSX asinkron.
//
// Muatannya SENGAJA hanya id pekerjaan: seluruh keadaan (filter yang diminta,
// status, lokasi berkas) hidup di tabel `report_jobs`, sehingga job yang
// terlewat/tergandakan tidak pernah mengekspor dua kali dengan filter berbeda
// — pekerjaan yang sudah selesai dilewati begitu saja (River at-least-once).
type ReportXLSXArgs struct {
	// JobID adalah id baris report_jobs yang harus dikerjakan.
	JobID string `json:"job_id"`
	// Jenis adalah jenis laporan ('order_lines'; F3-32 menambah 'payments').
	Jenis string `json:"jenis"`
}

// Kind mengidentifikasi jenis job ekspor laporan di tabel River.
func (ReportXLSXArgs) Kind() string { return KindReportXLSX }

// KindOCRBukti adalah Kind pekerjaan "baca bukti transfer dengan AI/OCR lalu
// putuskan verifikasi otomatis" (kartu F2-26; SystemDesign.md §4.3, PRD §5).
const KindOCRBukti = "ocr_bukti"

// OCRBuktiArgs meminta pembacaan AI/OCR seluruh bukti satu pesanan.
//
// Job upload membawa hanya id pesanan. Job reevaluate eksplisit juga membawa
// versi allowlist dan fingerprint bukti agar worker hanya menilai snapshot
// tersebut, tanpa memanggil provider untuk bukti baru.
// Job yang terlewat/tergandakan karena itu tidak pernah menggandakan keputusan
// maupun log: bukti yang sudah dibaca tidak dipanggil lagi ke provider, dan
// keputusan untuk satu himpunan bukti yang sama tidak ditulis dua kali
// (River at-least-once; lihat catatan idempotensi di internal/payment/ocr.go).
type OCRBuktiArgs struct {
	// OrderID adalah id pesanan yang buktinya harus dibaca.
	OrderID string `json:"order_id"`
	// Dua field ini hanya diisi oleh aksi reevaluate eksplisit dan menjadi
	// dimensi idempotensi snapshot policy+bukti. Job upload lama tetap hanya
	// membawa OrderID dan tidak berubah perilakunya.
	AllowlistVersion    int64  `json:"allowlist_version,omitempty"`
	ProofSetFingerprint string `json:"proof_set_fingerprint,omitempty"`
}

// Kind mengidentifikasi jenis job pembacaan bukti di tabel River.
func (OCRBuktiArgs) Kind() string { return KindOCRBukti }
