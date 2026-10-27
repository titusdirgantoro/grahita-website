package document

import (
	"fmt"

	"github.com/xuri/excelize/v2"
)

// This file menyusun Surat Jalan XLSX dengan excelize (SystemDesign.md §8):
// header kolom tetap, SATU BARIS PER ORDER LINE, siap cetak (A4, lebar pas,
// baris header diulang di tiap halaman, header dibekukan saat dilihat).
//
// Datanya sama persis dengan Surat Jalan PDF (render.go) sehingga kedua berkas
// tidak mungkin berbeda isi.

// SheetSuratJalan adalah nama sheet Surat Jalan pada berkas XLSX.
const SheetSuratJalan = "Surat Jalan"

// Tata letak baris tetap: kop, identitas, header tabel, lalu data.
const (
	rowKop    = 1 // PT GRAHITA INDONESIA
	rowJudul  = 2 // SURAT JALAN
	rowInfo1  = 3 // Nomor / Tanggal
	rowInfo2  = 4 // Cabang / Jumlah baris
	rowInfo3  = 5 // Kepala cabang / Total qty
	rowInfo4  = 6 // Email / Telepon
	rowHeader = 8 // header kolom tabel
	rowAwal   = 9 // baris data pertama
)

// Kolom tetap (A..E): No, Nama Barang, Nama Instansi, Qty, Serial Number.
const (
	colNo       = "A"
	colNama     = "B"
	colInstansi = "C"
	colQty      = "D"
	colSerial   = "E"
)

// suratJalanXLSX merender Surat Jalan menjadi berkas XLSX yang valid.
func suratJalanXLSX(o Order) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	idx, err := f.NewSheet(SheetSuratJalan)
	if err != nil {
		return nil, fmt.Errorf("buat sheet %s: %w", SheetSuratJalan, err)
	}
	f.SetActiveSheet(idx)
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, fmt.Errorf("hapus sheet bawaan: %w", err)
	}

	kop, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 14},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})
	if err != nil {
		return nil, fmt.Errorf("gaya kop: %w", err)
	}
	judul, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 12},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})
	if err != nil {
		return nil, fmt.Errorf("gaya judul: %w", err)
	}
	label, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, fmt.Errorf("gaya label: %w", err)
	}
	header, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"F0F4F8"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border:    tepiTabel(),
	})
	if err != nil {
		return nil, fmt.Errorf("gaya header: %w", err)
	}
	sel, err := f.NewStyle(&excelize.Style{Border: tepiTabel()})
	if err != nil {
		return nil, fmt.Errorf("gaya sel: %w", err)
	}
	angka, err := f.NewStyle(&excelize.Style{
		Border:    tepiTabel(),
		Alignment: &excelize.Alignment{Horizontal: "right"},
	})
	if err != nil {
		return nil, fmt.Errorf("gaya angka: %w", err)
	}

	// Kop & identitas dokumen.
	if err := f.MergeCell(SheetSuratJalan, "A1", "E1"); err != nil {
		return nil, fmt.Errorf("gabung kop: %w", err)
	}
	if err := f.MergeCell(SheetSuratJalan, "A2", "E2"); err != nil {
		return nil, fmt.Errorf("gabung judul: %w", err)
	}
	if err := f.SetCellValue(SheetSuratJalan, "A1", "PT GRAHITA INDONESIA"); err != nil {
		return nil, fmt.Errorf("tulis kop: %w", err)
	}
	if err := f.SetCellValue(SheetSuratJalan, "A2", "SURAT JALAN"); err != nil {
		return nil, fmt.Errorf("tulis judul: %w", err)
	}
	if err := f.SetCellStyle(SheetSuratJalan, "A1", "E1", kop); err != nil {
		return nil, fmt.Errorf("gaya kop: %w", err)
	}
	if err := f.SetCellStyle(SheetSuratJalan, "A2", "E2", judul); err != nil {
		return nil, fmt.Errorf("gaya judul: %w", err)
	}

	info := []struct {
		row        int
		labelKiri  string
		nilaiKiri  string
		labelKanan string
		nilaiKanan string
	}{
		{rowInfo1, "Nomor", o.Nomor, "Tanggal", FormatTanggal(o.Tanggal)},
		{rowInfo2, "Cabang", o.Cabang.Nama, "Jumlah baris", fmt.Sprintf("%d", len(o.Lines))},
		{rowInfo3, "Kepala cabang", o.Cabang.Kepala, "Total qty", fmt.Sprintf("%d", jumlahItem(o.Lines))},
		{rowInfo4, "Email", o.Cabang.Email, "Telepon", o.Cabang.Telepon},
	}
	for _, it := range info {
		if err := f.SetCellValue(SheetSuratJalan, fmt.Sprintf("A%d", it.row), it.labelKiri); err != nil {
			return nil, fmt.Errorf("tulis label: %w", err)
		}
		if err := f.SetCellStyle(SheetSuratJalan, fmt.Sprintf("A%d", it.row), fmt.Sprintf("A%d", it.row), label); err != nil {
			return nil, fmt.Errorf("gaya label: %w", err)
		}
		if err := f.SetCellValue(SheetSuratJalan, fmt.Sprintf("B%d", it.row), it.nilaiKiri); err != nil {
			return nil, fmt.Errorf("tulis nilai: %w", err)
		}
		if err := f.SetCellValue(SheetSuratJalan, fmt.Sprintf("C%d", it.row), it.labelKanan); err != nil {
			return nil, fmt.Errorf("tulis label: %w", err)
		}
		if err := f.SetCellStyle(SheetSuratJalan, fmt.Sprintf("C%d", it.row), fmt.Sprintf("C%d", it.row), label); err != nil {
			return nil, fmt.Errorf("gaya label: %w", err)
		}
		if err := f.SetCellValue(SheetSuratJalan, fmt.Sprintf("D%d", it.row), it.nilaiKanan); err != nil {
			return nil, fmt.Errorf("tulis nilai: %w", err)
		}
	}

	// Header kolom tetap.
	headers := []struct {
		kolom string
		judul string
	}{
		{colNo, "No"},
		{colNama, "Nama Barang"},
		{colInstansi, "Nama Instansi"},
		{colQty, "Qty"},
		{colSerial, "Serial Number"},
	}
	for _, h := range headers {
		if err := f.SetCellValue(SheetSuratJalan, fmt.Sprintf("%s%d", h.kolom, rowHeader), h.judul); err != nil {
			return nil, fmt.Errorf("tulis header: %w", err)
		}
	}
	if err := f.SetCellStyle(SheetSuratJalan, fmt.Sprintf("A%d", rowHeader), fmt.Sprintf("E%d", rowHeader), header); err != nil {
		return nil, fmt.Errorf("gaya header: %w", err)
	}

	// SATU BARIS PER ORDER LINE, urut line_no, serial apa adanya (packaging
	// tidak punya serial).
	baris := rowAwal
	for _, l := range o.Lines {
		serial := l.SerialNumber
		if serial == "" {
			serial = "-"
		}
		nilai := []struct {
			kolom string
			isi   any
		}{
			{colNo, l.LineNo},
			{colNama, l.Nama},
			{colInstansi, l.NamaInstansi},
			{colQty, l.Qty},
			{colSerial, serial},
		}
		for _, n := range nilai {
			sel := fmt.Sprintf("%s%d", n.kolom, baris)
			if err := f.SetCellValue(SheetSuratJalan, sel, n.isi); err != nil {
				return nil, fmt.Errorf("tulis baris %d: %w", baris, err)
			}
		}
		if err := f.SetCellStyle(SheetSuratJalan, fmt.Sprintf("A%d", baris), fmt.Sprintf("E%d", baris), sel); err != nil {
			return nil, fmt.Errorf("gaya baris %d: %w", baris, err)
		}
		if err := f.SetCellStyle(SheetSuratJalan, fmt.Sprintf("%s%d", colQty, baris), fmt.Sprintf("%s%d", colQty, baris), angka); err != nil {
			return nil, fmt.Errorf("gaya qty %d: %w", baris, err)
		}
		baris++
	}

	// Siap cetak: A4 tegak, muat satu halaman ke samping, header tabel
	// diulang, area cetak sebatas isi.
	if err := f.SetPageLayout(SheetSuratJalan, &excelize.PageLayoutOptions{
		Size:        intPtr(9), // 9 = A4 (210 mm x 297 mm)
		Orientation: strPtr("landscape"),
		FitToWidth:  intPtr(1),
		FitToHeight: intPtr(0),
	}); err != nil {
		return nil, fmt.Errorf("tata halaman: %w", err)
	}
	if err := f.SetPageMargins(SheetSuratJalan, &excelize.PageLayoutMarginsOptions{
		Top: floatPtr(0.6), Bottom: floatPtr(0.6), Left: floatPtr(0.5), Right: floatPtr(0.5),
	}); err != nil {
		return nil, fmt.Errorf("margin halaman: %w", err)
	}
	if err := f.SetDefinedName(&excelize.DefinedName{
		Name:     "_xlnm.Print_Titles",
		Scope:    SheetSuratJalan,
		RefersTo: fmt.Sprintf("'%s'!$%d:$%d", SheetSuratJalan, rowHeader, rowHeader),
	}); err != nil {
		return nil, fmt.Errorf("baris header cetak: %w", err)
	}
	if err := f.SetDefinedName(&excelize.DefinedName{
		Name:     "_xlnm.Print_Area",
		Scope:    SheetSuratJalan,
		RefersTo: fmt.Sprintf("'%s'!$A$1:$E$%d", SheetSuratJalan, rowDataTerakhir(len(o.Lines))),
	}); err != nil {
		return nil, fmt.Errorf("area cetak: %w", err)
	}
	if err := f.SetPanes(SheetSuratJalan, &excelize.Panes{
		Freeze:      true,
		YSplit:      rowHeader,
		TopLeftCell: fmt.Sprintf("A%d", rowAwal),
		ActivePane:  "bottomLeft",
	}); err != nil {
		return nil, fmt.Errorf("bekukan header: %w", err)
	}
	if err := f.SetColWidth(SheetSuratJalan, colNo, colNo, 5); err != nil {
		return nil, fmt.Errorf("lebar kolom No: %w", err)
	}
	if err := f.SetColWidth(SheetSuratJalan, colNama, colNama, 40); err != nil {
		return nil, fmt.Errorf("lebar kolom Nama: %w", err)
	}
	if err := f.SetColWidth(SheetSuratJalan, colInstansi, colInstansi, 38); err != nil {
		return nil, fmt.Errorf("lebar kolom Instansi: %w", err)
	}

	if err := f.SetColWidth(SheetSuratJalan, colQty, colQty, 8); err != nil {
		return nil, fmt.Errorf("lebar kolom Qty: %w", err)
	}
	if err := f.SetColWidth(SheetSuratJalan, colSerial, colSerial, 22); err != nil {
		return nil, fmt.Errorf("lebar kolom Serial: %w", err)
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, fmt.Errorf("tulis berkas xlsx: %w", err)
	}
	return buf.Bytes(), nil
}

// rowDataTerakhir adalah nomor baris data terakhir pada sheet Surat Jalan.
// Area cetak harus berhenti di sini, bukan satu baris setelahnya, supaya tidak
// ada baris kosong ikut tercetak.
func rowDataTerakhir(jumlahBaris int) int {
	if jumlahBaris < 1 {
		return rowHeader
	}
	return rowAwal + jumlahBaris - 1
}

// tepiTabel adalah garis tipis keliling sel tabel.
func tepiTabel() []excelize.Border {
	return []excelize.Border{
		{Type: "left", Color: "CBD2D9", Style: 1},
		{Type: "right", Color: "CBD2D9", Style: 1},
		{Type: "top", Color: "CBD2D9", Style: 1},
		{Type: "bottom", Color: "CBD2D9", Style: 1},
	}
}

func intPtr(v int) *int           { return &v }
func strPtr(v string) *string     { return &v }
func floatPtr(v float64) *float64 { return &v }
