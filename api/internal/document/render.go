package document

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"time"
)

//go:embed assets/logo-grahita-lockup-320.png
var logoGrahita []byte

var logoGrahitaURL = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(logoGrahita))

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

// TerbilangRupiah menulis nilai invoice dengan kata-kata untuk bagian
// "Terbilang" seperti pada format cetak Grahita.
func TerbilangRupiah(n int64) string {
	if n < 0 {
		return "Nilai tidak valid"
	}
	if n == 0 {
		return "Nol rupiah"
	}
	kata := terbilang(n)
	return strings.ToUpper(kata[:1]) + kata[1:] + " rupiah"
}

func terbilang(n int64) string {
	satuan := [...]string{"", "satu", "dua", "tiga", "empat", "lima", "enam", "tujuh", "delapan", "sembilan", "sepuluh", "sebelas"}
	switch {
	case n < 12:
		return satuan[n]
	case n < 20:
		return terbilang(n-10) + " belas"
	case n < 100:
		return strings.TrimSpace(terbilang(n/10) + " puluh " + terbilang(n%10))
	case n < 200:
		return strings.TrimSpace("seratus " + terbilang(n-100))
	case n < 1000:
		return strings.TrimSpace(terbilang(n/100) + " ratus " + terbilang(n%100))
	case n < 2000:
		return strings.TrimSpace("seribu " + terbilang(n-1000))
	case n < 1_000_000:
		return strings.TrimSpace(terbilang(n/1000) + " ribu " + terbilang(n%1000))
	case n < 1_000_000_000:
		return strings.TrimSpace(terbilang(n/1_000_000) + " juta " + terbilang(n%1_000_000))
	case n < 1_000_000_000_000:
		return strings.TrimSpace(terbilang(n/1_000_000_000) + " miliar " + terbilang(n%1_000_000_000))
	case n < 1_000_000_000_000_000:
		return strings.TrimSpace(terbilang(n/1_000_000_000_000) + " triliun " + terbilang(n%1_000_000_000_000))
	default:
		return strings.TrimSpace(terbilang(n/1_000_000_000_000_000) + " kuadriliun " + terbilang(n%1_000_000_000_000_000))
	}
}

// penandaPembayaran menentukan penanda pembayaran invoice (PRD §6.1).
// Invoice Tempo dapat terbit sebelum lunas, sedangkan Transfer dan C&C
// terbit setelah settlement lengkap.
func penandaPembayaran(o Order) string {
	if o.IsTempo {
		return "PEMBAYARAN TEMPO — jatuh tempo " + FormatTanggal(o.TempoDueDate)
	}
	if o.IsCNC {
		return "LUNAS — CASH & CARRY"
	}
	return "LUNAS"
}

// lineView adalah satu baris tabel dokumen yang sudah diformat.
type lineView struct {
	No       string
	Nama     string
	Instansi string
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
	Logo        template.URL
	Nomor       string
	Versi       string
	Tanggal     string
	Penanda     string
	Cabang      string
	Kepala      string
	Email       string
	Telepon     string
	Catatan     string
	JumlahItem  string
	Total       string
	Produksi    string
	Terbilang   string
	BarisKosong []int
	Lines       []lineView
	Rekening    []rekeningView
}

// suratJalanView adalah model tampilan surat jalan (HTML/PDF). XLSX memakai
// data yang sama lewat penyusun tersendiri.
type suratJalanView struct {
	Logo     template.URL
	Nomor    string
	Tanggal  string
	Cabang   string
	Kepala   string
	Email    string
	Telepon  string
	Catatan  string
	Pages    []suratJalanPage
	Jumlah   string
	TotalQty string
}

type suratJalanPage struct {
	Lines       []lineView
	BarisKosong []int
	Continued   bool
	Last        bool
}

func barisKosong(jumlah, minimum int) []int {
	if jumlah >= minimum {
		return nil
	}
	return make([]int, minimum-jumlah)
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
			Instansi: l.NamaInstansi,
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
	var produksi int64
	for _, l := range o.Lines {
		produksi += l.Subtotal
	}
	return invoiceView{
		Logo:        logoGrahitaURL,
		Nomor:       o.Nomor,
		Versi:       versiRevisi(o.Versi),
		Tanggal:     FormatTanggal(o.Tanggal),
		Penanda:     penandaPembayaran(o),
		Cabang:      o.Cabang.Nama,
		Kepala:      o.Cabang.Kepala,
		Email:       o.Cabang.Email,
		Telepon:     o.Cabang.Telepon,
		Catatan:     strings.TrimSpace(o.Catatan),
		JumlahItem:  fmt.Sprintf("%d", jumlahItem(o.Lines)),
		Total:       FormatRupiah(o.Total),
		Produksi:    FormatAngka(produksi),
		Terbilang:   TerbilangRupiah(o.Total),
		BarisKosong: barisKosong(len(o.Lines), 11),
		Lines:       viewLines(o),
		Rekening:    viewRekening(o),
	}
}

func newSuratJalanView(o Order) suratJalanView {
	totalQty := 0
	for _, l := range o.Lines {
		totalQty += l.Qty
	}
	return suratJalanView{
		Logo:     logoGrahitaURL,
		Nomor:    o.Nomor,
		Tanggal:  FormatTanggal(o.Tanggal),
		Cabang:   o.Cabang.Nama,
		Kepala:   o.Cabang.Kepala,
		Email:    o.Cabang.Email,
		Telepon:  o.Cabang.Telepon,
		Catatan:  strings.TrimSpace(o.Catatan),
		Pages:    suratJalanPages(viewLines(o)),
		Jumlah:   fmt.Sprintf("%d", len(o.Lines)),
		TotalQty: fmt.Sprintf("%d", totalQty),
	}
}

// Halaman terakhir menyisakan tempat untuk catatan dan tanda tangan. Saat
// rincian panjang, beberapa baris ikut halaman terakhir agar tanda tangan
// tidak terdorong sendirian ke lembar berikutnya.
func suratJalanPages(lines []lineView) []suratJalanPage {
	if len(lines) <= 22 {
		return []suratJalanPage{{Lines: lines, BarisKosong: barisKosong(len(lines), 18), Last: true}}
	}
	pages := make([]suratJalanPage, 0, (len(lines)+23)/24)
	for len(lines) > 22 {
		take := min(24, len(lines)-5)
		pages = append(pages, suratJalanPage{Lines: lines[:take], Continued: len(pages) > 0})
		lines = lines[take:]
	}
	pages = append(pages, suratJalanPage{Lines: lines, Continued: true, Last: true})
	return pages
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

// templateInvoice mengikuti tata letak contoh Grahita: A4 mendatar, kop
// ungu-kuning, rincian per instansi, terbilang, ringkasan, dan ruang tanda
// tangan. Kolom biaya tambahan ditandai kosong karena belum ada di model
// pesanan; angka tagihan selalu berasal dari snapshot pesanan.
const templateInvoice = `<!DOCTYPE html>
<html lang="id"><head><meta charset="utf-8"><title>Invoice {{.Nomor}}</title>
<style>
@page { size: A4 landscape; margin: 10mm; }
* { box-sizing: border-box; }
body { margin: 0; color: #22202b; font: 9px Arial, Helvetica, sans-serif; -webkit-print-color-adjust: exact; print-color-adjust: exact; }
.brand-purple { color: #4a2879; }
.kop { display: flex; justify-content: space-between; align-items: flex-start; height: 95px; }
.logo { display: block; width: 86px; height: 78px; object-fit: contain; object-position: left top; }
.judul { text-align: right; padding-top: 6px; }
.judul h1 { margin: 0 0 5px; color: #4a2879; font-size: 30px; letter-spacing: 1px; line-height: 1; }
.metadata { margin-left: auto; border-collapse: collapse; font-size: 10px; }
.metadata td { padding: 2px 0 2px 10px; }
.metadata td:first-child { color: #5c5365; text-align: left; }
.metadata td:last-child { font-weight: bold; }
.rule { height: 4px; background: linear-gradient(to right, #f9d732 0 15%, #4a2879 15% 100%); }
.recipient { width: 36%; min-height: 67px; margin: 12px 0 12px; padding: 9px 12px; border-left: 4px solid #4a2879; border-radius: 4px; background: #f3eef9; line-height: 1.45; }
.recipient .label { color: #4a2879; font-size: 9px; font-weight: bold; letter-spacing: .5px; }
.recipient strong { display: block; font-size: 11px; }
.recipient .minor { color: #5c5365; }
.baris { width: 100%; border-collapse: collapse; table-layout: fixed; font-size: 8px; }
.baris th, .baris td { border: 1px solid #d9d1e4; padding: 5px 3px; vertical-align: middle; overflow-wrap: anywhere; }
.baris thead th { background: #4a2879; color: white; border-color: #4a2879; text-align: center; font-size: 7.4px; line-height: 1.3; text-transform: uppercase; }
.baris thead tr:nth-child(2) th { background: #5b3987; }
.baris tbody tr:nth-child(even) { background: #f7f4fb; }
.baris tbody tr { break-inside: avoid; }
.baris .empty td { height: 18px; color: transparent; }
.baris .num { text-align: right; white-space: nowrap; }
.baris .center { text-align: center; }
.baris tfoot td { background: #fff5c8; color: #33224c; font-weight: bold; border-color: #d9d1e4; }
.baris thead { display: table-header-group; }
.baris tfoot { display: table-row-group; }
.closing { display: grid; grid-template-columns: 43% 20% 35%; gap: 1%; margin-top: 14px; break-inside: avoid; min-height: 112px; }
.small-label { color: #4a2879; font-size: 9px; font-weight: bold; text-transform: uppercase; letter-spacing: .4px; }
.terbilang { margin: 4px 0 12px; font-style: italic; line-height: 1.45; }
.note { margin-top: 4px; white-space: pre-wrap; line-height: 1.35; }
.sign { text-align: center; padding: 2px 8px; }
.sign .line { border-bottom: 1px solid #68587a; margin: 48px 14px 4px; }
.sign .hint { color: #655b70; font-size: 8px; }
.summary { width: 100%; border-collapse: collapse; font-size: 9px; }
.summary td { padding: 5px 6px; border-bottom: 1px solid #e2dce9; }
.summary td:last-child { text-align: right; white-space: nowrap; }
.summary tr.grand td { padding: 9px 6px; background: #4a2879; color: white; border: 0; font-weight: bold; font-size: 10px; }
.payment { margin-top: 5px; font-size: 8px; color: #544a60; }
.bank { margin-top: 5px; font-size: 8px; line-height: 1.4; }
.footer { display: flex; justify-content: space-between; border-top: 1px solid #d9d1e4; margin-top: 12px; padding-top: 6px; color: #71697b; font-size: 7.5px; }
</style></head><body>
<header class="kop">
  <img class="logo" src="{{.Logo}}" alt="Grahita Indonesia">
  <div class="judul"><h1>INVOICE</h1><table class="metadata">
    <tr><td>Nomor</td><td>: {{.Nomor}}</td></tr>
    <tr><td>Tanggal</td><td>: {{.Tanggal}}</td></tr>
    {{if .Versi}}<tr><td>Dokumen</td><td>: {{.Versi}}</td></tr>{{end}}
  </table></div>
</header>
<div class="rule"></div>
<section class="recipient"><div class="label">KEPADA YTH.</div>
  {{if .Kepala}}<strong>{{.Kepala}}</strong>{{end}}
  <strong>{{.Cabang}}</strong>
  {{if .Telepon}}<div>Telp. {{.Telepon}}</div>{{end}}
  {{if .Email}}<div class="minor">{{.Email}}</div>{{end}}
</section>
<table class="baris" aria-label="Rincian invoice">
  <colgroup><col style="width:3%"><col style="width:9%"><col style="width:17%"><col style="width:8%"><col style="width:4%"><col style="width:10%"><col style="width:8%"><col style="width:8%"><col style="width:6%"><col style="width:6%"><col style="width:6%"><col style="width:10%"><col style="width:5%"></colgroup>
  <thead><tr><th rowspan="2">No</th><th rowspan="2">Serial Number</th><th rowspan="2">Nama Instansi</th><th rowspan="2">Cabang</th><th rowspan="2">Jml Pcs</th><th rowspan="2">Produk</th><th rowspan="2">Harga per Pcs</th><th colspan="4">Tagihan</th><th rowspan="2">Total Tagihan</th><th rowspan="2">Ket</th></tr>
    <tr><th>Produksi</th><th>PI (5%)</th><th>Expres</th><th>Bea SPV</th></tr></thead>
  <tbody>{{range .Lines}}<tr><td class="center">{{.No}}</td><td>{{.Serial}}</td><td>{{if .Instansi}}{{.Instansi}}{{else}}—{{end}}</td><td>{{$.Cabang}}</td><td class="center">{{.Qty}}</td><td>{{.Nama}}</td><td class="num">{{.Harga}}</td><td class="num">{{.Subtotal}}</td><td class="center">—</td><td class="center">—</td><td class="center">—</td><td class="num">{{.Subtotal}}</td><td></td></tr>{{end}}
    {{range .BarisKosong}}<tr class="empty"><td>&nbsp;</td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td><td></td></tr>{{end}}
  </tbody>
  <tfoot><tr><td colspan="4">JUMLAH</td><td class="center">{{.JumlahItem}}</td><td colspan="2"></td><td class="num">{{.Produksi}}</td><td class="center">—</td><td class="center">—</td><td class="center">—</td><td class="num">{{.Total}}</td><td></td></tr></tfoot>
</table>
<section class="closing">
  <div><div class="small-label">Terbilang</div><div class="terbilang"># {{.Terbilang}} #</div>
    <div class="small-label">Catatan</div>{{if .Catatan}}<div class="note">{{.Catatan}}</div>{{else}}<div class="note">—</div>{{end}}
    <div class="bank"><strong>Rekening pembayaran:</strong><br>{{if .Rekening}}{{range .Rekening}}{{.Bank}} — {{.Nomor}} a.n. {{.AtasNama}}<br>{{end}}{{else}}Hubungi admin Pusat.{{end}}</div>
  </div>
  <div class="sign">Tangerang, {{.Tanggal}}<br>Hormat kami,<div class="line"></div><div class="hint">Nama jelas</div></div>
  <div><table class="summary"><tr><td>Total tagihan periode ini</td><td>{{.Total}}</td></tr><tr><td>Tagihan lama / lebih setor</td><td>—</td></tr><tr><td>Tagihan baru</td><td>—</td></tr><tr class="grand"><td>TOTAL TAGIHAN</td><td>{{.Total}}</td></tr></table><div class="payment">{{.Penanda}}</div></div>
</section>
<footer class="footer"><span>S.E. &amp; O. — Kesalahan dan kelalaian dikecualikan. Mohon cocokkan rincian sebelum melakukan pembayaran.</span><span>PT GRAHITA INDONESIA</span></footer>
</body></html>`

// templateSuratJalan memakai identitas dan rincian yang sama, dalam A4 tegak
// seperti contoh. Ruang tanda tangan dibiarkan kosong agar penerima dan
// pengemudi dapat menandatangani hasil cetaknya.
const templateSuratJalan = `<!DOCTYPE html>
<html lang="id"><head><meta charset="utf-8"><title>Surat Jalan {{.Nomor}}</title>
<style>
@page { size: A4 portrait; margin: 10mm; }
* { box-sizing: border-box; }
body { margin: 0; color: #22202b; font: 10px Arial, Helvetica, sans-serif; -webkit-print-color-adjust: exact; print-color-adjust: exact; }
.kop { display: flex; justify-content: space-between; align-items: flex-start; height: 104px; }
.logo { display: block; width: 86px; height: 78px; object-fit: contain; object-position: left top; }
.judul { text-align: right; padding-top: 8px; }
.judul h1 { margin: 0 0 6px; color: #4a2879; font-size: 30px; letter-spacing: 1px; line-height: 1; }
.metadata { margin-left: auto; border-collapse: collapse; font-size: 10px; }
.metadata td { padding: 2px 0 2px 8px; }
.metadata td:first-child { color: #5c5365; text-align: left; }
.metadata td:last-child { font-weight: bold; }
.rule { height: 4px; background: linear-gradient(to right, #f9d732 0 17%, #4a2879 17% 100%); }
.info { display: grid; grid-template-columns: 48% 48%; gap: 4%; margin: 15px 0 16px; min-height: 77px; }
.recipient { padding: 10px 12px; border-left: 4px solid #4a2879; border-radius: 4px; background: #f3eef9; line-height: 1.45; }
.recipient .label { color: #4a2879; font-size: 9px; font-weight: bold; letter-spacing: .5px; }
.recipient strong { display: block; font-size: 11px; }
.recipient .minor { color: #5c5365; font-size: 9px; }
.transport { padding-top: 3px; line-height: 1.8; }
.transport .row { display: flex; gap: 6px; }
.transport .row span { min-width: 62px; }
.transport .blank { border-bottom: 1px solid #b8a7cb; flex: 1; }
.baris { width: 100%; border-collapse: collapse; table-layout: fixed; font-size: 8.5px; }
.baris th, .baris td { border: 1px solid #d9d1e4; padding: 6px 5px; vertical-align: middle; overflow-wrap: anywhere; }
.baris thead th { background: #4a2879; color: white; border-color: #4a2879; text-align: center; font-size: 8px; text-transform: uppercase; line-height: 1.3; }
.baris tbody tr:nth-child(even) { background: #f7f4fb; }
.baris tbody tr { break-inside: avoid; }
.baris .empty td { height: 22px; color: transparent; }
.baris .center { text-align: center; }
.baris tfoot td { background: #fff5c8; color: #33224c; font-weight: bold; }
.baris thead { display: table-header-group; }
.baris tfoot { display: table-row-group; }
.continued { break-before: page; margin: 0 0 8px; color: #4a2879; font-size: 9px; font-weight: bold; }
.delivery-closing { break-inside: avoid; }
.notes { margin-top: 15px; break-inside: avoid; }
.label { color: #4a2879; font-weight: bold; text-transform: uppercase; font-size: 9px; letter-spacing: .4px; }
.notes .box { border: 1px solid #d9d1e4; border-radius: 4px; min-height: 42px; margin-top: 5px; padding: 8px; white-space: pre-wrap; }
.ack { margin-top: 12px; font-size: 9px; font-style: italic; }
.place { text-align: right; margin-top: 15px; font-size: 9px; }
.signatures { display: grid; grid-template-columns: repeat(3, 1fr); gap: 14px; text-align: center; margin-top: 9px; break-inside: avoid; }
.signature .line { border-bottom: 1px solid #68587a; margin: 43px 15px 5px; }
.signature .hint { color: #655b70; font-size: 8px; }
.footer { display: flex; justify-content: space-between; border-top: 1px solid #d9d1e4; margin-top: 18px; padding-top: 6px; color: #71697b; font-size: 7.5px; }
</style></head><body>
<header class="kop"><img class="logo" src="{{.Logo}}" alt="Grahita Indonesia"><div class="judul"><h1>SURAT JALAN</h1><table class="metadata"><tr><td>Nomor</td><td>: {{.Nomor}}</td></tr><tr><td>Tanggal</td><td>: {{.Tanggal}}</td></tr></table></div></header>
<div class="rule"></div>
<section class="info"><div class="recipient"><div class="label">KEPADA YTH.</div><strong>{{.Cabang}}</strong>{{if .Telepon}}<div>Telp. {{.Telepon}}</div>{{end}}{{if .Email}}<div class="minor">{{.Email}}</div>{{end}}</div>
  <div class="transport">Kami kirimkan barang-barang di bawah ini dengan:<div class="row"><span>Kendaraan</span><div class="blank"></div></div><div class="row"><span>No. Polisi</span><div class="blank"></div></div></div></section>
{{range .Pages}}
{{if .Continued}}<div class="continued">SURAT JALAN {{$.Nomor}} — lanjutan</div>{{end}}
<table class="baris" aria-label="Rincian surat jalan"><colgroup><col style="width:5%"><col style="width:17%"><col style="width:29%"><col style="width:15%"><col style="width:16%"><col style="width:8%"><col style="width:10%"></colgroup>
  <thead><tr><th>No</th><th>Serial Number</th><th>Nama Instansi</th><th>Asal Cabang</th><th>Produk</th><th>Jumlah</th><th>Ket</th></tr></thead>
  <tbody>{{range .Lines}}<tr><td class="center">{{.No}}</td><td>{{.Serial}}</td><td>{{if .Instansi}}{{.Instansi}}{{else}}—{{end}}</td><td>{{$.Cabang}}</td><td>{{.Nama}}</td><td class="center">{{.Qty}}</td><td></td></tr>{{end}}
    {{range .BarisKosong}}<tr class="empty"><td>&nbsp;</td><td></td><td></td><td></td><td></td><td></td><td></td></tr>{{end}}</tbody>
  {{if .Last}}<tfoot><tr><td colspan="5" style="text-align:right">JUMLAH</td><td class="center">{{$.TotalQty}}</td><td></td></tr></tfoot>{{end}}</table>
{{end}}
<section class="delivery-closing">
<section class="notes"><div class="label">Catatan</div><div class="box">{{.Catatan}}</div></section>
<div class="ack">Barang telah diterima dalam keadaan baik dan jumlah sesuai. Mohon kembalikan lembar tanda terima yang telah ditandatangani.</div>
<div class="place">Tangerang, {{.Tanggal}}</div>
<section class="signatures"><div class="signature">Penerima,<div class="line"></div><div class="hint">Tanda terima &amp; stempel</div></div><div class="signature">Pengemudi,<div class="line"></div><div class="hint">Nama jelas</div></div><div class="signature">Hormat kami,<div class="line"></div><div class="hint">Nama jelas</div></div></section>
<footer class="footer"><span>Lembar 1: Penerima &nbsp;·&nbsp; Lembar 2: Pengirim (arsip) &nbsp;·&nbsp; Lembar 3: Pengemudi</span><span>PT GRAHITA INDONESIA</span></footer>
</section>
</body></html>`
