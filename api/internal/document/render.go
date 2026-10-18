package document

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

// This file berisi penyusun isi dokumen: formatter angka/tanggal Indonesia,
// model tampilan, dan template HTML kedua PDF (invoice & surat jalan).
//
// Seluruh angka dan tanggal DIFORMAT DI GO, bukan di template, supaya template
// hanya berisi susunan halaman dan pengujian bisa memeriksa nilai yang benar
// tanpa harus mengenal HTML. Nilai dari database tetap di-escape otomatis oleh
// html/template.

// bulanID adalah nama bulan untuk tanggal dokumen (Bahasa Indonesia).
var bulanID = [...]string{
	"Januari", "Februari", "Maret", "April", "Mei", "Juni",
	"Juli", "Agustus", "September", "Oktober", "November", "Desember",
}

// FormatTanggal menampilkan tanggal bisnis seperti "08 Oktober 2026".
// Komponen tanggal dipakai apa adanya: kolom `date` sudah merupakan tanggal
// bisnis WIB (BR-04, BR-11), jadi tidak dikonversi ulang ke zona lain.
func FormatTanggal(t time.Time) string {
	return fmt.Sprintf("%02d %s %d", t.Day(), bulanID[int(t.Month())-1], t.Year())
}

// FormatAngka menampilkan bilangan bulat Rupiah dengan pemisah ribuan titik
// ("1.250.000"). Perhitungan uang selalu bilangan bulat (BR-10), jadi tidak
// ada bagian desimal yang perlu ditampilkan.
func FormatAngka(n int64) string {
	negatif := n < 0
	if negatif {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if negatif {
		return "-" + b.String()
	}
	return b.String()
}

// FormatRupiah menampilkan nilai uang lengkap dengan awalan "Rp".
func FormatRupiah(n int64) string { return "Rp " + FormatAngka(n) }

// penandaPembayaran menentukan penanda pembayaran invoice (PRD §6.1). Invoice
// baru lahir saat pesanan selesai diverifikasi, jadi semuanya LUNAS; pesanan
// Cash & Carry (dibayar dari deposit cabang) diberi keterangan tambahan.
func penandaPembayaran(o Order) string {
	if o.IsCNC {
		return "LUNAS — CASH & CARRY"
	}
	return "LUNAS"
}

// lineView adalah satu baris tabel dokumen yang sudah diformat.
type lineView struct {
	No       string
	Nama     string
	Qty      string
	Harga    string
	Subtotal string
	Serial   string
}

// rekeningView adalah satu rekening pembayaran yang sudah diformat.
type rekeningView struct {
	Bank     string
	Nomor    string
	AtasNama string
}

// invoiceView adalah model tampilan invoice.
type invoiceView struct {
	Nomor      string
	Versi      string
	Tanggal    string
	Penanda    string
	Cabang     string
	Kepala     string
	Email      string
	Telepon    string
	Catatan    string
	JumlahItem string
	Total      string
	Lines      []lineView
	Rekening   []rekeningView
}

// suratJalanView adalah model tampilan surat jalan (HTML/PDF). XLSX memakai
// data yang sama lewat penyusun tersendiri.
type suratJalanView struct {
	Nomor    string
	Tanggal  string
	Cabang   string
	Kepala   string
	Email    string
	Telepon  string
	Catatan  string
	Lines    []lineView
	Jumlah   string
	TotalQty string
}

// viewLines menyusun baris tabel dokumen: satu baris per order line
// (termasuk baris biaya packaging pesanan Sertifikat).
func viewLines(o Order) []lineView {
	out := make([]lineView, 0, len(o.Lines))
	for _, l := range o.Lines {
		serial := strings.TrimSpace(l.SerialNumber)
		if serial == "" {
			serial = "—"
		}
		out = append(out, lineView{
			No:       fmt.Sprintf("%d", l.LineNo),
			Nama:     l.Nama,
			Qty:      fmt.Sprintf("%d", l.Qty),
			Harga:    FormatAngka(l.HargaSatuan),
			Subtotal: FormatAngka(l.Subtotal),
			Serial:   serial,
		})
	}
	return out
}

// viewRekening menyusun daftar rekening pembayaran yang dicetak di invoice.
func viewRekening(o Order) []rekeningView {
	out := make([]rekeningView, 0, len(o.Rekening))
	for _, r := range o.Rekening {
		out = append(out, rekeningView(r))
	}
	return out
}

// versiRevisi menampilkan versi revisi invoice: kosong untuk versi 0 (invoice
// pertama), "Revisi v<N>" untuk versi berikutnya (PRD §6.1).
func versiRevisi(versi int) string {
	if versi <= 0 {
		return ""
	}
	return fmt.Sprintf("Revisi v%d", versi)
}

func newInvoiceView(o Order) invoiceView {
	return invoiceView{
		Nomor:      o.Nomor,
		Versi:      versiRevisi(o.Versi),
		Tanggal:    FormatTanggal(o.Tanggal),
		Penanda:    penandaPembayaran(o),
		Cabang:     o.Cabang.Nama,
		Kepala:     o.Cabang.Kepala,
		Email:      o.Cabang.Email,
		Telepon:    o.Cabang.Telepon,
		Catatan:    strings.TrimSpace(o.Catatan),
		JumlahItem: fmt.Sprintf("%d", jumlahItem(o.Lines)),
		Total:      FormatRupiah(o.Total),
		Lines:      viewLines(o),
		Rekening:   viewRekening(o),
	}
}

func newSuratJalanView(o Order) suratJalanView {
	totalQty := 0
	var jumlah int
	for _, l := range o.Lines {
		totalQty += l.Qty
	}
	jumlah = len(o.Lines)
	return suratJalanView{
		Nomor:    o.Nomor,
		Tanggal:  FormatTanggal(o.Tanggal),
		Cabang:   o.Cabang.Nama,
		Kepala:   o.Cabang.Kepala,
		Email:    o.Cabang.Email,
		Telepon:  o.Cabang.Telepon,
		Catatan:  strings.TrimSpace(o.Catatan),
		Lines:    viewLines(o),
		Jumlah:   fmt.Sprintf("%d", jumlah),
		TotalQty: fmt.Sprintf("%d", totalQty),
	}
}

// jumlahItem menjumlahkan qty seluruh baris (prototype menampilkan "jumlah
// item", yaitu banyaknya unit yang dikirim).
func jumlahItem(lines []Line) int {
	total := 0
	for _, l := range lines {
		total += l.Qty
	}
	return total
}

// invoiceHTML merender invoice menjadi HTML A4 siap kirim ke Gotenberg
// (SystemDesign.md §8).
func invoiceHTML(o Order) ([]byte, error) {
	return renderHTML("invoice", templateInvoice, newInvoiceView(o))
}

// suratJalanHTML merender surat jalan menjadi HTML dengan data yang sama
// seperti XLSX-nya, sehingga kedua berkas konsisten (SystemDesign.md §8).
func suratJalanHTML(o Order) ([]byte, error) {
	return renderHTML("surat_jalan", templateSuratJalan, newSuratJalanView(o))
}

// renderHTML menyusun satu dokumen HTML. Template diurai per pemanggilan:
// dokumen dirender jarang (saat pesanan selesai/revisi), jadi tidak ada
// kebutuhan menyimpan template hasil parse di memori proses.
func renderHTML(nama, isi string, data any) ([]byte, error) {
	t, err := template.New(nama).Parse(isi)
	if err != nil {
		return nil, fmt.Errorf("%w: template %s tidak dapat diurai: %v", ErrInvalidData, nama, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return nil, fmt.Errorf("%w: template %s gagal dirender: %v", ErrInvalidData, nama, err)
	}
	return []byte(b.String()), nil
}

// templateInvoice adalah kerangka invoice A4. Sengaja tanpa tanda tangan atau
// stempel digital: PRD §8 no.9 masih terbuka, jadi menambahkan citra tanda
// tangan sekarang berarti menebak keputusan bisnis.
const templateInvoice = `<!DOCTYPE html>
<html lang="id">
<head>
<meta charset="utf-8">
<title>Invoice {{.Nomor}}</title>
<style>
  @page { size: A4; margin: 12mm; }
  * { box-sizing: border-box; }
  body { font-family: "Helvetica Neue", Arial, sans-serif; font-size: 11px; color: #1f2933; margin: 0; }
  .kop { display: flex; justify-content: space-between; align-items: flex-start; border-bottom: 2px solid #1f2933; padding-bottom: 10px; }
  .merek { display: flex; gap: 10px; align-items: center; }
  .logo { width: 44px; height: 44px; border-radius: 8px; background: #1f2933; color: #fff; font-size: 18px; font-weight: 700; display: flex; align-items: center; justify-content: center; }
  .pt { font-size: 15px; font-weight: 700; letter-spacing: 0.4px; }
  .sub { font-size: 10px; color: #52606d; }
  .judul { text-align: right; }
  .judul h1 { font-size: 20px; margin: 0 0 4px; letter-spacing: 1px; }
  .nomor { font-size: 12px; font-weight: 700; }
  .versi { font-size: 10px; color: #52606d; }
  .penanda { display: inline-block; margin-top: 6px; padding: 3px 8px; border: 1px solid #1f2933; border-radius: 999px; font-size: 10px; font-weight: 700; }
  .info { display: flex; gap: 24px; margin: 14px 0 12px; }
  .info table { border-collapse: collapse; }
  .info td { padding: 2px 0; vertical-align: top; }
  .info td.label { color: #52606d; padding-right: 8px; white-space: nowrap; }
  table.baris { width: 100%; border-collapse: collapse; margin-top: 4px; }
  table.baris th, table.baris td { border: 1px solid #cbd2d9; padding: 5px 6px; text-align: left; }
  table.baris th { background: #f0f4f8; font-size: 10px; text-transform: uppercase; letter-spacing: 0.3px; }
  table.baris td.num, table.baris th.num { text-align: right; }
  table.baris tr { page-break-inside: avoid; }
  tr.total td { font-weight: 700; background: #f8fafc; }
  .rekening { margin-top: 14px; page-break-inside: avoid; }
  .rekening h2 { font-size: 11px; text-transform: uppercase; letter-spacing: 0.4px; margin: 0 0 6px; }
  .rekening ul { margin: 0; padding-left: 16px; }
  .rekening li { margin-bottom: 2px; }
  .catatan { margin-top: 14px; padding-top: 8px; border-top: 1px solid #cbd2d9; color: #52606d; font-size: 10px; }
</style>
</head>
<body>
  <div class="kop">
    <div class="merek">
      <div class="logo">GI</div>
      <div>
        <div class="pt">PT GRAHITA INDONESIA</div>
        <div class="sub">Sistem Pemesanan Cabang</div>
      </div>
    </div>
    <div class="judul">
      <h1>INVOICE</h1>
      <div class="nomor">Nomor: {{.Nomor}}</div>
      {{if .Versi}}<div class="versi">{{.Versi}}</div>{{end}}
      <div class="penanda">{{.Penanda}}</div>
    </div>
  </div>

  <div class="info">
    <table>
      <tr><td class="label">Tanggal</td><td>{{.Tanggal}}</td></tr>
      <tr><td class="label">Cabang</td><td>{{.Cabang}}</td></tr>
      <tr><td class="label">Kepala cabang</td><td>{{.Kepala}}</td></tr>
      <tr><td class="label">Email</td><td>{{.Email}}</td></tr>
      {{if .Telepon}}<tr><td class="label">Telepon</td><td>{{.Telepon}}</td></tr>{{end}}
    </table>
    <table>
      <tr><td class="label">Jumlah item</td><td>{{.JumlahItem}}</td></tr>
      {{if .Catatan}}<tr><td class="label">Catatan</td><td>{{.Catatan}}</td></tr>{{end}}
    </table>
  </div>

  <table class="baris">
    <thead>
      <tr>
        <th>No</th>
        <th>Nama</th>
        <th class="num">Qty</th>
        <th class="num">Harga</th>
        <th class="num">Subtotal</th>
        <th>Serial number</th>
      </tr>
    </thead>
    <tbody>
      {{range .Lines}}
      <tr>
        <td>{{.No}}</td>
        <td>{{.Nama}}</td>
        <td class="num">{{.Qty}}</td>
        <td class="num">{{.Harga}}</td>
        <td class="num">{{.Subtotal}}</td>
        <td>{{.Serial}}</td>
      </tr>
      {{end}}
    </tbody>
    <tfoot>
      <tr class="total">
        <td colspan="4">TOTAL</td>
        <td class="num">{{.Total}}</td>
        <td></td>
      </tr>
    </tfoot>
  </table>

  <div class="rekening">
    <h2>Rekening pembayaran</h2>
    {{if .Rekening}}
    <ul>
      {{range .Rekening}}<li>{{.Bank}} — {{.Nomor}} a.n. {{.AtasNama}}</li>{{end}}
    </ul>
    {{else}}
    <p>Tidak ada rekening aktif. Hubungi admin Pusat.</p>
    {{end}}
  </div>

  <div class="catatan">
    Dokumen ini dibuat otomatis oleh sistem dan belum memuat tanda tangan atau stempel digital.
    Total dalam Rupiah bulat (tanpa sen). Serial number dicetak apa adanya dari pesanan.
  </div>
</body>
</html>
`

// templateSuratJalan adalah kerangka surat jalan PDF — kolomnya sengaja sama
// dengan berkas XLSX-nya agar kedua berkas konsisten.
const templateSuratJalan = `<!DOCTYPE html>
<html lang="id">
<head>
<meta charset="utf-8">
<title>Surat Jalan {{.Nomor}}</title>
<style>
  @page { size: A4; margin: 12mm; }
  * { box-sizing: border-box; }
  body { font-family: "Helvetica Neue", Arial, sans-serif; font-size: 11px; color: #1f2933; margin: 0; }
  .kop { display: flex; justify-content: space-between; align-items: flex-start; border-bottom: 2px solid #1f2933; padding-bottom: 10px; }
  .merek { display: flex; gap: 10px; align-items: center; }
  .logo { width: 44px; height: 44px; border-radius: 8px; background: #1f2933; color: #fff; font-size: 18px; font-weight: 700; display: flex; align-items: center; justify-content: center; }
  .pt { font-size: 15px; font-weight: 700; letter-spacing: 0.4px; }
  .sub { font-size: 10px; color: #52606d; }
  .judul { text-align: right; }
  .judul h1 { font-size: 20px; margin: 0 0 4px; letter-spacing: 1px; }
  .nomor { font-size: 12px; font-weight: 700; }
  .info { display: flex; gap: 24px; margin: 14px 0 12px; }
  .info table { border-collapse: collapse; }
  .info td { padding: 2px 0; vertical-align: top; }
  .info td.label { color: #52606d; padding-right: 8px; white-space: nowrap; }
  table.baris { width: 100%; border-collapse: collapse; margin-top: 4px; }
  table.baris th, table.baris td { border: 1px solid #cbd2d9; padding: 6px; text-align: left; }
  table.baris th { background: #f0f4f8; font-size: 10px; text-transform: uppercase; letter-spacing: 0.3px; }
  table.baris td.num, table.baris th.num { text-align: right; }
  table.baris tr { page-break-inside: avoid; }
  tr.total td { font-weight: 700; background: #f8fafc; }
  .catatan { margin-top: 16px; padding-top: 8px; border-top: 1px solid #cbd2d9; color: #52606d; font-size: 10px; }
</style>
</head>
<body>
  <div class="kop">
    <div class="merek">
      <div class="logo">GI</div>
      <div>
        <div class="pt">PT GRAHITA INDONESIA</div>
        <div class="sub">Sistem Pemesanan Cabang</div>
      </div>
    </div>
    <div class="judul">
      <h1>SURAT JALAN</h1>
      <div class="nomor">Nomor: {{.Nomor}}</div>
    </div>
  </div>

  <div class="info">
    <table>
      <tr><td class="label">Tanggal</td><td>{{.Tanggal}}</td></tr>
      <tr><td class="label">Cabang</td><td>{{.Cabang}}</td></tr>
      <tr><td class="label">Kepala cabang</td><td>{{.Kepala}}</td></tr>
      <tr><td class="label">Email</td><td>{{.Email}}</td></tr>
      {{if .Telepon}}<tr><td class="label">Telepon</td><td>{{.Telepon}}</td></tr>{{end}}
    </table>
    <table>
      <tr><td class="label">Jumlah baris</td><td>{{.Jumlah}}</td></tr>
      <tr><td class="label">Total qty</td><td>{{.TotalQty}}</td></tr>
      {{if .Catatan}}<tr><td class="label">Catatan</td><td>{{.Catatan}}</td></tr>{{end}}
    </table>
  </div>

  <table class="baris">
    <thead>
      <tr>
        <th>No</th>
        <th>Nama barang</th>
        <th class="num">Qty</th>
        <th>Serial number</th>
      </tr>
    </thead>
    <tbody>
      {{range .Lines}}
      <tr>
        <td>{{.No}}</td>
        <td>{{.Nama}}</td>
        <td class="num">{{.Qty}}</td>
        <td>{{.Serial}}</td>
      </tr>
      {{end}}
    </tbody>
    <tfoot>
      <tr class="total">
        <td colspan="2">TOTAL</td>
        <td class="num">{{.TotalQty}}</td>
        <td></td>
      </tr>
    </tfoot>
  </table>

  <div class="catatan">
    Surat jalan dibuat otomatis oleh sistem dan belum memuat tanda tangan atau stempel digital.
    Barang diperiksa saat diterima; kekurangan/kerusakan mohon dilaporkan ke admin Pusat.
  </div>
</body>
</html>
`
