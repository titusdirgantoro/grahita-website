package report

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/xuri/excelize/v2"
)

// Nama sheet berkas laporan (AP-REP-04/05).
const (
	SheetLaporan = "Laporan"
	SheetRekap   = "Rekap Bulanan"
)

// LayoutVerifikasi adalah bentuk waktu verifikasi pada berkas laporan (WIB).
const LayoutVerifikasi = "2006-01-02 15:04"

// kolomLaporan adalah urutan kolom laporan (PRD §4.7 AP-REP-03).
// Urutan ini tidak boleh diubah tanpa mengubah pengujian dan
// kontrak OpenAPI.
var kolomLaporan = []struct {
	judul string
	lebar float64
}{
	{"Tanggal", 12},
	{"Nomor Invoice", 20},
	{"Versi", 7},
	{"Cabang", 26},
	{"Kepala Cabang", 22},
	{"C&C", 6},
	{"PI", 6},
	{"Produk", 34},
	{"Jenis", 10},
	{"Serial Number", 20},
	{"Qty", 6},
	{"Harga Satuan", 15},
	{"Subtotal", 15},
	{"Status", 10},
	{"Tanggal Verifikasi", 19},
	{"Metode Verifikasi", 17},
}

// kolomRekap adalah urutan kolom sheet rekap bulanan per cabang (AP-REP-05).
var kolomRekap = []struct {
	judul string
	lebar float64
}{
	{"Bulan", 10},
	{"Cabang", 30},
	{"Jumlah Baris", 13},
	{"Jumlah Qty", 11},
	{"Total Subtotal", 16},
}

// Rekap adalah satu baris ringkasan bulanan per cabang. Angkanya dihitung dari
// baris yang BENAR-BENAR ditulis ke sheet laporan, sehingga rekap selalu cocok
// dengan data yang diekspor (bukan query agregat terpisah yang bisa menyimpang).
type Rekap struct {
	Bulan         string `json:"bulan"`
	Cabang        string `json:"cabang"`
	JumlahBaris   int64  `json:"jumlah_baris"`
	JumlahQty     int64  `json:"jumlah_qty"`
	TotalSubtotal int64  `json:"total_subtotal"`
}

// hasilXLSX adalah hasil penulisan satu berkas laporan.
type hasilXLSX struct {
	path  string
	size  int64
	baris int64
	rekap []Rekap
}

// gayaLaporan menampung indeks gaya XLSX yang dipakai berulang.
type gayaLaporan struct {
	header int
	teks   int
	rupiah int
	angka  int
}

// penulisXLSX menulis berkas laporan memakai excelize StreamWriter: tiap baris
// ditulis lalu dilepas, sehingga memori tidak tumbuh seiring jumlah baris
// (SystemDesign.md §8 — "streaming writer untuk data besar").
type penulisXLSX struct {
	f      *excelize.File
	sw     *excelize.StreamWriter
	gaya   gayaLaporan
	baris  int
	jumlah int64
	rekap  map[string]*Rekap
	urutan []string
}

// penulisBaru membuat berkas kosong beserta sheet laporan siap tulis: lebar
// kolom, header ter-freeze (AP-REP-04), dan gaya sel.
func penulisBaru() (*penulisXLSX, error) {
	f := excelize.NewFile()
	idx, err := f.NewSheet(SheetLaporan)
	if err != nil {
		return nil, fmt.Errorf("buat sheet %s: %w", SheetLaporan, err)
	}
	f.SetActiveSheet(idx)
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, fmt.Errorf("hapus sheet bawaan: %w", err)
	}

	gaya, err := gayaBaru(f)
	if err != nil {
		return nil, err
	}
	sw, err := f.NewStreamWriter(SheetLaporan)
	if err != nil {
		return nil, fmt.Errorf("stream writer %s: %w", SheetLaporan, err)
	}
	for i, k := range kolomLaporan {
		if err := sw.SetColWidth(i+1, i+1, k.lebar); err != nil {
			return nil, fmt.Errorf("lebar kolom %s: %w", k.judul, err)
		}
	}
	// Header dibekukan supaya tetap terlihat saat baris laporan di-scroll
	// (AP-REP-04). Nilai YSplit = jumlah baris header.
	if err := sw.SetPanes(&excelize.Panes{
		Freeze:      true,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	}); err != nil {
		return nil, fmt.Errorf("bekukan header: %w", err)
	}

	p := &penulisXLSX{f: f, sw: sw, gaya: gaya, rekap: map[string]*Rekap{}}
	if err := p.tulisKepala(); err != nil {
		return nil, err
	}
	return p, nil
}

// gayaBaru menyiapkan gaya: header tebal, sel tabel bergaris, dan format angka
// Rupiah ("Rp" #,##0) untuk kolom uang (AP-REP-04).
func gayaBaru(f *excelize.File) (gayaLaporan, error) {
	tepi := []excelize.Border{
		{Type: "left", Color: "CBD2D9", Style: 1},
		{Type: "right", Color: "CBD2D9", Style: 1},
		{Type: "top", Color: "CBD2D9", Style: 1},
		{Type: "bottom", Color: "CBD2D9", Style: 1},
	}
	header, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"F0F4F8"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border:    tepi,
	})
	if err != nil {
		return gayaLaporan{}, fmt.Errorf("gaya header: %w", err)
	}
	teks, err := f.NewStyle(&excelize.Style{Border: tepi})
	if err != nil {
		return gayaLaporan{}, fmt.Errorf("gaya teks: %w", err)
	}
	rupiah, err := f.NewStyle(&excelize.Style{
		Border:       tepi,
		Alignment:    &excelize.Alignment{Horizontal: "right"},
		CustomNumFmt: strPtr(`"Rp" #,##0`),
	})
	if err != nil {
		return gayaLaporan{}, fmt.Errorf("gaya rupiah: %w", err)
	}
	angka, err := f.NewStyle(&excelize.Style{
		Border:    tepi,
		Alignment: &excelize.Alignment{Horizontal: "right"},
	})
	if err != nil {
		return gayaLaporan{}, fmt.Errorf("gaya angka: %w", err)
	}
	return gayaLaporan{header: header, teks: teks, rupiah: rupiah, angka: angka}, nil
}

// tulisKepala menulis baris header kolom.
func (p *penulisXLSX) tulisKepala() error {
	nilai := make([]any, 0, len(kolomLaporan))
	for _, k := range kolomLaporan {
		nilai = append(nilai, k.judul)
	}
	if err := p.sw.SetRow("A1", nilai, excelize.RowOpts{StyleID: p.gaya.header}); err != nil {
		return fmt.Errorf("tulis header laporan: %w", err)
	}
	p.baris = 1
	return nil
}

// TulisBaris menulis satu order line ke sheet laporan dan mengakumulasi rekap.
func (p *penulisXLSX) TulisBaris(l Line) error {
	p.baris++
	sel := fmt.Sprintf("A%d", p.baris)
	nilai := []any{
		l.Tanggal,
		l.Nomor,
		l.Versi,
		l.Cabang,
		l.KepalaCabang,
		yaTidak(l.IsCNC),
		yaTidak(l.PajakIn),
		l.Produk,
		l.Jenis,
		serialTampil(l.SerialNumber),
		excelize.Cell{StyleID: p.gaya.angka, Value: l.Qty},
		excelize.Cell{StyleID: p.gaya.rupiah, Value: l.HargaSatuan},
		excelize.Cell{StyleID: p.gaya.rupiah, Value: l.Subtotal},
		l.Status,
		l.TanggalVerifikasi,
		l.MetodeVerifikasi,
	}
	if err := p.sw.SetRow(sel, nilai, excelize.RowOpts{StyleID: p.gaya.teks}); err != nil {
		return fmt.Errorf("tulis baris %d: %w", p.baris, err)
	}
	p.jumlah++
	p.tambahRekap(l)
	return nil
}

// tambahRekap mengakumulasi satu baris ke ringkasan bulanan cabangnya.
func (p *penulisXLSX) tambahRekap(l Line) {
	bulan := l.TanggalWaktu.Format("2006-01")
	if bulan == "0001-01" {
		bulan = ""
	}
	kunci := bulan + "\x00" + l.Cabang
	baris, ada := p.rekap[kunci]
	if !ada {
		baris = &Rekap{Bulan: bulan, Cabang: l.Cabang}
		p.rekap[kunci] = baris
		p.urutan = append(p.urutan, kunci)
	}
	baris.JumlahBaris++
	baris.JumlahQty += int64(l.Qty)
	baris.TotalSubtotal += l.Subtotal
}

// Selesai menutup sheet laporan, menulis sheet rekap bulanan per cabang
// (AP-REP-05), lalu mengembalikan isi rekapnya.
func (p *penulisXLSX) Selesai() ([]Rekap, error) {
	if err := p.sw.Flush(); err != nil {
		return nil, fmt.Errorf("tutup sheet laporan: %w", err)
	}
	rekap := p.daftarRekap()
	if err := p.tulisRekap(rekap); err != nil {
		return nil, err
	}
	return rekap, nil
}

// tulisRekap menulis sheet kedua: ringkasan bulanan per cabang.
func (p *penulisXLSX) tulisRekap(rekap []Rekap) error {
	if _, err := p.f.NewSheet(SheetRekap); err != nil {
		return fmt.Errorf("buat sheet %s: %w", SheetRekap, err)
	}
	sw, err := p.f.NewStreamWriter(SheetRekap)
	if err != nil {
		return fmt.Errorf("stream writer %s: %w", SheetRekap, err)
	}
	for i, k := range kolomRekap {
		if err := sw.SetColWidth(i+1, i+1, k.lebar); err != nil {
			return fmt.Errorf("lebar kolom rekap %s: %w", k.judul, err)
		}
	}
	if err := sw.SetPanes(&excelize.Panes{
		Freeze:      true,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	}); err != nil {
		return fmt.Errorf("bekukan header rekap: %w", err)
	}
	judul := make([]any, 0, len(kolomRekap))
	for _, k := range kolomRekap {
		judul = append(judul, k.judul)
	}
	if err := sw.SetRow("A1", judul, excelize.RowOpts{StyleID: p.gaya.header}); err != nil {
		return fmt.Errorf("tulis header rekap: %w", err)
	}
	baris := 1
	for _, r := range rekap {
		baris++
		nilai := []any{
			r.Bulan,
			r.Cabang,
			excelize.Cell{StyleID: p.gaya.angka, Value: r.JumlahBaris},
			excelize.Cell{StyleID: p.gaya.angka, Value: r.JumlahQty},
			excelize.Cell{StyleID: p.gaya.rupiah, Value: r.TotalSubtotal},
		}
		if err := sw.SetRow(fmt.Sprintf("A%d", baris), nilai, excelize.RowOpts{StyleID: p.gaya.teks}); err != nil {
			return fmt.Errorf("tulis rekap baris %d: %w", baris, err)
		}
	}
	if err := sw.Flush(); err != nil {
		return fmt.Errorf("tutup sheet rekap: %w", err)
	}
	return nil
}

// daftarRekap mengembalikan rekap terurut (bulan, lalu cabang) supaya berkas
// deterministik.
func (p *penulisXLSX) daftarRekap() []Rekap {
	out := make([]Rekap, 0, len(p.rekap))
	for _, kunci := range p.urutan {
		out = append(out, *p.rekap[kunci])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bulan != out[j].Bulan {
			return out[i].Bulan < out[j].Bulan
		}
		return out[i].Cabang < out[j].Cabang
	})
	return out
}

// Simpan menuliskan berkas XLSX ke w dan mengembalikan ukurannya.
func (p *penulisXLSX) Simpan(w io.Writer) (int64, error) {
	n, err := p.f.WriteTo(w)
	if err != nil {
		return 0, fmt.Errorf("tulis berkas xlsx: %w", err)
	}
	return n, nil
}

// tulisXLSX membaca baris laporan BERTAHAP dari repository dan menuliskannya
// streaming ke satu berkas sementara, lalu mengembalikan path + jumlah baris
// + rekapnya. Pemanggil bertanggung jawab menghapus berkasnya.
//
// Pembacaan memakai kursor (tanggal, nomor, line_no) alih-alih menahan seluruh
// hasil: yang hidup di memori hanya satu batch (BatchEkspor baris). Baris
// berikutnya diambil SETELAH batch sebelumnya selesai ditulis, sehingga ekspor
// 50.000 baris tidak menumpuk seluruh data (SystemDesign.md §8, §9).
func (s *Service) tulisXLSX(ctx context.Context, q Query) (hasilXLSX, error) {
	p, err := penulisBaru()
	if err != nil {
		return hasilXLSX{}, err
	}
	defer func() { _ = p.f.Close() }()

	for {
		batch := q
		batch.Offset = 0
		rows, err := s.repo.ListLines(ctx, batch)
		if err != nil {
			return hasilXLSX{}, fmt.Errorf("baca baris laporan: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			if r.TanggalWaktu.IsZero() {
				return hasilXLSX{}, fmt.Errorf("%w: pesanan %s tanpa tanggal", ErrDataInvalid, r.Nomor)
			}
			if err := p.TulisBaris(r); err != nil {
				return hasilXLSX{}, err
			}
		}
		if len(rows) < q.Batch {
			break
		}
		terakhir := rows[len(rows)-1]
		tanggal := terakhir.TanggalWaktu
		q.SetelahTanggal = &tanggal
		q.SetelahNomor = terakhir.Nomor
		q.SetelahLineNo = terakhir.LineNo
	}

	rekap, err := p.Selesai()
	if err != nil {
		return hasilXLSX{}, err
	}

	path, size, err := tulisKeTemp(s.tempDir, "laporan-*.xlsx", p.Simpan)
	if err != nil {
		return hasilXLSX{}, err
	}
	return hasilXLSX{path: path, size: size, baris: p.jumlah, rekap: rekap}, nil
}

// tulisKeTemp menulis isi laporan ke satu berkas sementara di dir lewat tulis,
// lalu menutupnya dan mengembalikan path + ukurannya.
//
// Berkas sementara wajib hilang pada SETIAP jalur gagal: percobaan ulang worker
// River (at-least-once) tidak boleh menumpuk berkas yatim di direktori temp.
// Pemanggil pada jalur sukses (Service.Jalankan) yang menghapus berkas setelah
// di-unggah ke object storage.
func tulisKeTemp(dir, pattern string, tulis func(io.Writer) (int64, error)) (string, int64, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", 0, fmt.Errorf("buat berkas laporan: %w", err)
	}
	sukses := false
	defer func() {
		if !sukses {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	size, err := tulis(f)
	if err != nil {
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		return "", 0, fmt.Errorf("tutup berkas laporan: %w", err)
	}
	sukses = true
	return f.Name(), size, nil
}

// yaTidak menyajikan atribut boolean pada berkas laporan.
func yaTidak(v bool) string {
	if v {
		return "Ya"
	}
	return "Tidak"
}

// serialTampil menampilkan serial number; baris packaging tidak punya serial.
func serialTampil(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func strPtr(v string) *string { return &v }
