package document

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestInvoiceHTMLMemuatIdentitasDanAngka menjaga isi invoice sesuai PRD §6.1:
// identitas PT, nomor + versi, tanggal, data cabang, baris + serial, total,
// rekening, dan penanda LUNAS / Cash & Carry.
func TestInvoiceHTMLMemuatIdentitasDanAngka(t *testing.T) {
	o := fixtureOrder("o1")
	b, err := invoiceHTML(o)
	if err != nil {
		t.Fatalf("invoiceHTML: %v", err)
	}
	html := string(b)

	wajib := []string{
		"PT GRAHITA INDONESIA",
		"INVOICE",
		"Nomor: 2026-10-0001",
		"08 Oktober 2026",
		"Grahita Bandung",
		"Sandi",
		"bandung@example.test",
		"022-123456",
		"Bio Magnetik",
		"20261008001",
		"20261008002",
		"Rp 5.003.000",
		"2.500.000",
		"5.000.000",
		"BCA — 1234567890 a.n. PT Grahita Indonesia",
		"BRI — 0987654321 a.n. PT Grahita Indonesia",
		"LUNAS",
		"Tolong dikirim pagi",
	}
	for _, s := range wajib {
		if !strings.Contains(html, s) {
			t.Errorf("invoice HTML tidak memuat %q", s)
		}
	}
	// Versi 0 tidak menampilkan penanda revisi.
	if strings.Contains(html, "Revisi v") {
		t.Error("invoice versi 0 tidak boleh menyebut revisi")
	}
}

// TestInvoiceHTMLVersiDanPenanda menguji penanda revisi dan Cash & Carry.
func TestInvoiceHTMLVersiDanPenanda(t *testing.T) {
	o := fixtureOrder("o1")
	o.Versi = 2
	o.IsCNC = true
	b, err := invoiceHTML(o)
	if err != nil {
		t.Fatalf("invoiceHTML: %v", err)
	}
	html := string(b)
	if !strings.Contains(html, "Revisi v2") {
		t.Error("invoice revisi harus menyebut versinya")
	}
	if !strings.Contains(html, "CASH &amp; CARRY — jatuh tempo 15 Oktober 2026") {
		t.Errorf("penanda C&C salah: %s", cuplikan(html, "CASH", 120))
	}
	if strings.Contains(html, ">LUNAS<") {
		t.Error("pesanan C&C tidak boleh ditandai LUNAS")
	}
}

// TestInvoiceTanpaJatuhTempoTetapCNC menjaga pesanan C&C tanpa jatuh tempo
// tidak mencetak tanggal kosong.
func TestInvoiceTanpaJatuhTempoTetapCNC(t *testing.T) {
	o := fixtureOrder("o1")
	o.IsCNC = true
	o.JatuhTempo = nil
	b, err := invoiceHTML(o)
	if err != nil {
		t.Fatalf("invoiceHTML: %v", err)
	}
	if !strings.Contains(string(b), "CASH &amp; CARRY") {
		t.Error("penanda C&C harus tetap tercetak")
	}
}

// TestTemplateMengEscapeNilaiDariDatabase menjaga nilai dari database tidak
// bisa menyuntikkan HTML ke dokumen.
func TestTemplateMengEscapeNilaiDariDatabase(t *testing.T) {
	o := fixtureOrder("o1")
	o.Cabang.Nama = `<script>alert("xss")</script>`
	o.Catatan = `<img src=x onerror=alert(1)>`
	b, err := invoiceHTML(o)
	if err != nil {
		t.Fatalf("invoiceHTML: %v", err)
	}
	html := string(b)
	// Markup mentah tidak boleh lolos; teks "onerror=alert" sendiri tidak
	// berbahaya selama tag-nya di-escape, jadi yang diperiksa adalah tag-nya.
	if strings.Contains(html, "<script>alert") || strings.Contains(html, "<img src=x") {
		t.Error("nilai dari database harus di-escape oleh html/template")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("nilai yang di-escape tidak terlihat di dokumen")
	}
	s, err := suratJalanHTML(o)
	if err != nil {
		t.Fatalf("suratJalanHTML: %v", err)
	}
	if strings.Contains(string(s), "<script>alert") {
		t.Error("surat jalan juga harus meng-escape nilai dari database")
	}
}

// TestSuratJalanXLSXSatuBarisPerLine menjaga SystemDesign.md §8: header kolom
// tetap dan satu baris per order line pada berkas XLSX yang benar-benar valid.
func TestSuratJalanXLSXSatuBarisPerLine(t *testing.T) {
	o := fixtureOrder("o1")
	// Tambah baris packaging supaya kolom serial kosong ikut teruji.
	o.Lines = append(o.Lines, Line{LineNo: 3, Jenis: "packaging", Nama: "Biaya packaging",
		Qty: 1, HargaSatuan: 3000, Subtotal: 3000})

	b, err := suratJalanXLSX(o)
	if err != nil {
		t.Fatalf("suratJalanXLSX: %v", err)
	}
	if !bytes.HasPrefix(b, []byte("PK")) {
		t.Fatal("berkas XLSX tidak valid: bukan arsip zip")
	}

	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("berkas XLSX tidak dapat dibuka excelize: %v", err)
	}
	defer func() { _ = f.Close() }()

	sheet := SheetSuratJalan
	if _, err := f.GetSheetIndex(sheet); err != nil {
		t.Fatalf("sheet %q tidak ada: %v", sheet, err)
	}

	// Kop dan identitas dokumen.
	if v, _ := f.GetCellValue(sheet, "A1"); v != "PT GRAHITA INDONESIA" {
		t.Errorf("A1 = %q", v)
	}
	if v, _ := f.GetCellValue(sheet, "A2"); v != "SURAT JALAN" {
		t.Errorf("A2 = %q", v)
	}
	if v, _ := f.GetCellValue(sheet, "B3"); v != "2026-10-0001" {
		t.Errorf("B3 nomor = %q", v)
	}
	if v, _ := f.GetCellValue(sheet, "D3"); v != "08 Oktober 2026" {
		t.Errorf("D3 tanggal = %q", v)
	}
	if v, _ := f.GetCellValue(sheet, "B4"); v != "Grahita Bandung" {
		t.Errorf("B4 cabang = %q", v)
	}

	// Header kolom tetap.
	header := []struct {
		sel string
		mau string
	}{
		{"A8", "No"},
		{"B8", "Nama Barang"},
		{"C8", "Qty"},
		{"D8", "Serial Number"},
	}
	for _, h := range header {
		if v, _ := f.GetCellValue(sheet, h.sel); v != h.mau {
			t.Errorf("%s = %q, mau %q", h.sel, v, h.mau)
		}
	}

	// Satu baris per order line, urut line_no.
	mauBaris := [][]string{
		{"1", "Bio Magnetik", "2", "20261008001"},
		{"2", "Bio Listrik", "1", "20261008002"},
		{"3", "Biaya packaging", "1", "-"},
	}
	for i, mau := range mauBaris {
		row := rowAwal + i
		for j, kolom := range []string{"A", "B", "C", "D"} {
			sel := kolom + itoa(row)
			v, err := f.GetCellValue(sheet, sel)
			if err != nil {
				t.Fatalf("baca %s: %v", sel, err)
			}
			if v != mau[j] {
				t.Errorf("%s = %q, mau %q", sel, v, mau[j])
			}
		}
	}
	// Tidak ada baris data berlebih.
	if v, _ := f.GetCellValue(sheet, "A"+itoa(rowAwal+len(mauBaris))); v != "" {
		t.Errorf("ada baris data berlebih: %q", v)
	}

	// Siap cetak: A4 tegak, muat satu halaman ke samping, header dibekukan.
	layout, err := f.GetPageLayout(sheet)
	if err != nil {
		t.Fatalf("GetPageLayout: %v", err)
	}
	if layout.Orientation == nil || *layout.Orientation != "portrait" {
		t.Errorf("orientasi halaman = %v", layout.Orientation)
	}
	if layout.Size == nil || *layout.Size != 9 {
		t.Errorf("ukuran kertas bukan A4: %v", layout.Size)
	}
	if layout.FitToWidth == nil || *layout.FitToWidth != 1 {
		t.Errorf("fitToWidth = %v", layout.FitToWidth)
	}
	panes, err := f.GetPanes(sheet)
	if err != nil {
		t.Fatalf("GetPanes: %v", err)
	}
	if !panes.Freeze || panes.YSplit != rowHeader {
		t.Errorf("header tidak dibekukan: %+v", panes)
	}

	// Area cetak berhenti di baris data TERAKHIR — bukan satu baris setelahnya,
	// supaya tidak ada baris kosong yang ikut tercetak.
	mauName := map[string]string{
		"_xlnm.Print_Area":   fmt.Sprintf("'%s'!$A$1:$D$%d", sheet, rowAwal+len(mauBaris)-1),
		"_xlnm.Print_Titles": fmt.Sprintf("'%s'!$%d:$%d", sheet, rowHeader, rowHeader),
	}
	for _, dn := range f.GetDefinedName() {
		mau, ok := mauName[dn.Name]
		if !ok {
			continue
		}
		if dn.RefersTo != mau {
			t.Errorf("%s = %q, mau %q", dn.Name, dn.RefersTo, mau)
		}
		delete(mauName, dn.Name)
	}
	if len(mauName) > 0 {
		t.Errorf("defined name cetak tidak ditemukan: %v", mauName)
	}
}

// TestSuratJalanHTMLKonsistenDenganXLSX menjaga kedua berkas surat jalan
// memuat himpunan baris dan serial yang sama.
func TestSuratJalanHTMLKonsistenDenganXLSX(t *testing.T) {
	o := fixtureOrder("o1")
	b, err := suratJalanHTML(o)
	if err != nil {
		t.Fatalf("suratJalanHTML: %v", err)
	}
	html := string(b)
	for _, s := range []string{"SURAT JALAN", "Nomor: 2026-10-0001", "08 Oktober 2026",
		"Grahita Bandung", "20261008001", "20261008002", "Bio Magnetik", "Bio Listrik"} {
		if !strings.Contains(html, s) {
			t.Errorf("surat jalan HTML tidak memuat %q", s)
		}
	}
	// Tanggal bisnis dipakai apa adanya (BR-04/BR-11): tidak ada pergeseran
	// zona waktu pada dokumen.
	if strings.Contains(html, "7 Oktober 2026") {
		t.Error("tanggal bergeser satu hari — konversi zona waktu tidak diinginkan")
	}
}

// TestVersiRevisi kosmetik kecil: label versi dokumen.
func TestVersiRevisi(t *testing.T) {
	if got := versiRevisi(0); got != "" {
		t.Errorf("versi 0 = %q", got)
	}
	if got := versiRevisi(1); got != "Revisi v1" {
		t.Errorf("versi 1 = %q", got)
	}
	if got := versiRevisi(-1); got != "" {
		t.Errorf("versi negatif = %q", got)
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func cuplikan(s, mulai string, n int) string {
	i := strings.Index(s, mulai)
	if i < 0 {
		return "(tidak ditemukan)"
	}
	akhir := i + n
	if akhir > len(s) {
		akhir = len(s)
	}
	return s[i:akhir]
}
