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
