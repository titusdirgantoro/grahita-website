package document

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// serviceInvoice menyiapkan service dokumen dengan repository in-memory untuk
// pengujian riwayat invoice (kartu F1-19).
func serviceInvoice(mem *Memory) *Service {
	return NewService(mem, newStoreHitung(), &rendererPalsu{})
}

// orderInvoice adalah snapshot pesanan satu cabang untuk pengujian riwayat.
func orderInvoice(id, branchID, nomor string, status string, versi int, tanggal time.Time) Order {
	return Order{
		ID:       id,
		Nomor:    nomor,
		Status:   status,
		Versi:    versi,
		BranchID: branchID,
		Total:    250000,
		Tanggal:  tanggal,
		Lines: []Line{
			{LineNo: 1, Jenis: "product", Nama: "Bio Magnetik", Qty: 2, HargaSatuan: 100000, Subtotal: 200000},
			{LineNo: 2, Jenis: "packaging", Nama: "Biaya packaging", Qty: 1, HargaSatuan: 50000, Subtotal: 50000},
		},
	}
}

// TestInvoicesTerbatasCabangSesi menjaga isolasi data cabang: daftar invoice
// hanya memuat pesanan cabang SESI, akun Pusat ditolak, dan akun cabang tanpa
// cabang dijawab ErrNoBranch.
func TestInvoicesTerbatasCabangSesi(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	tgl := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)
	mem.SetOrder(orderInvoice(orderA, "br-1", "2026-10-0001", StatusDone, 0, tgl))
	mem.SetOrder(orderInvoice(orderB, "br-2", "2026-10-0002", StatusDone, 0, tgl))

	svc := serviceInvoice(mem)

	rows, err := svc.Invoices(ctx, actorCabang("br-1"), InvoiceFilter{})
	if err != nil {
		t.Fatalf("Invoices cabang: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("harus 1 invoice milik cabang sesi, dapat %d (%+v)", len(rows), rows)
	}
	if rows[0].OrderID != orderA || rows[0].Nomor != "2026-10-0001" {
		t.Errorf("invoice cabang lain ikut tampil: %+v", rows[0])
	}
	// Pesanan cabang lain tidak pernah muncul, walau ada dokumennya.
	for _, r := range rows {
		if r.OrderID == orderB {
			t.Error("pesanan cabang lain tidak boleh tampil di riwayat invoice")
		}
	}

	// Akun Pusat memakai jalur admin, bukan endpoint riwayat invoice cabang.
	if _, err := svc.Invoices(ctx, actorAdmin(), InvoiceFilter{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("akun admin harus ErrForbidden, dapat %v", err)
	}
	// Akun cabang tanpa cabang: tidak ada riwayat yang bisa ditampilkan.
	tanpaCabang := actorCabang("")
	if _, err := svc.Invoices(ctx, tanpaCabang, InvoiceFilter{}); !errors.Is(err, ErrNoBranch) {
		t.Errorf("akun tanpa cabang harus ErrNoBranch, dapat %v", err)
	}
}

// TestInvoicesVersiLamaTetapTampil membuktikan janji kartu bahwa versi lama
// tetap dapat dilihat setelah revisi, dan invoice yang belum Done (dokumennya
// tidak ada) tampil tanpa PDF yang bisa diunduh.
func TestInvoicesVersiLamaTetapTampil(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	tgl := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)

	// Pesanan yang sudah direvisi sekali: versi pesanan 1, dokumen v0 & v1 ada.
	revisi := orderInvoice(orderA, "br-1", "2026-10-0001", StatusDone, 1, tgl)
	mem.SetOrder(revisi)
	v0 := mem.TambahDokumen(Document{OrderID: orderA, Jenis: JenisInvoicePDF, Versi: 0,
		StorageKey: StorageKey(orderA, JenisInvoicePDF, 0), UkuranByte: 100})
	v1 := mem.TambahDokumen(Document{OrderID: orderA, Jenis: JenisInvoicePDF, Versi: 1,
		StorageKey: StorageKey(orderA, JenisInvoicePDF, 1), UkuranByte: 120})

	// Pesanan yang baru dikirim (Submitted) belum punya dokumen sama sekali.
	mem.SetOrder(orderInvoice(orderB, "br-1", "2026-10-0002", "submitted", 0, tgl))

	rows, err := serviceInvoice(mem).Invoices(ctx, actorCabang("br-1"), InvoiceFilter{})
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("harus 3 baris (v1, v0, submitted), dapat %d (%+v)", len(rows), rows)
	}

	byVersi := map[int]InvoiceRow{}
	for _, r := range rows {
		if r.OrderID != orderA {
			continue
		}
		if r.VersiDokumen == nil {
			t.Fatalf("baris pesanan revisi harus punya dokumen: %+v", r)
		}
		byVersi[*r.VersiDokumen] = r
	}
	if _, ada := byVersi[0]; !ada {
		t.Fatal("dokumen versi lama (v0) hilang dari riwayat setelah revisi")
	}
	if _, ada := byVersi[1]; !ada {
		t.Fatal("dokumen versi baru (v1) tidak tampil")
	}
	if byVersi[1].DokumenID != v1.ID || byVersi[0].DokumenID != v0.ID {
		t.Errorf("dokumen tertukar: v0=%q v1=%q", byVersi[0].DokumenID, byVersi[1].DokumenID)
	}
	if !byVersi[1].Terbaru {
		t.Error("dokumen versi 1 harus ditandai terbaru (versi pesanan = 1)")
	}
	if byVersi[0].Terbaru {
		t.Error("dokumen versi 0 bukan versi terbaru lagi")
	}
	for versi, r := range byVersi {
		if !r.DapatDiunduh {
			t.Errorf("invoice Done versi %d harus dapat diunduh", versi)
		}
		if r.DapatDiunduh && r.DokumenID == "" {
			t.Errorf("dapat_diunduh tanpa dokumen: %+v", r)
		}
	}

	// Baris pesanan Submitted: tampil, tanpa dokumen, tidak bisa diunduh.
	var submitted *InvoiceRow
	for i := range rows {
		if rows[i].OrderID == orderB {
			submitted = &rows[i]
		}
	}
	if submitted == nil {
		t.Fatal("invoice Submitted harus tetap tampil sebagai baris tanpa dokumen")
	}
	if submitted.VersiDokumen != nil || submitted.DokumenID != "" {
		t.Errorf("invoice belum Done tidak boleh punya dokumen: %+v", *submitted)
	}
	if submitted.DapatDiunduh {
		t.Error("invoice belum Done tidak boleh dapat diunduh")
	}
	if submitted.JumlahItem != 2 {
		t.Errorf("jumlah item = %d, mau 2 (baris packaging tidak dihitung)", submitted.JumlahItem)
	}
}

// TestInvoicesFilterPeriode memeriksa filter periode inklusif dan penolakan
// parameter yang tidak terbaca — bukan diam-diam mengembalikan daftar kosong.
func TestInvoicesFilterPeriode(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	awal := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	tengah := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)
	akhir := time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)
	mem.SetOrder(orderInvoice(orderA, "br-1", "2026-09-0001", StatusDone, 0, awal))
	mem.SetOrder(orderInvoice(orderB, "br-1", "2026-10-0002", StatusDone, 0, tengah))

	// orderC memakai id UUID lain supaya bisa ditambahkan tanpa bentrok.
	const orderC = "0f8fad5b-d9cb-469f-a165-708677289510"
	mem.SetOrder(orderInvoice(orderC, "br-1", "2026-10-0003", StatusDone, 0, akhir))

	svc := serviceInvoice(mem)
	cab := actorCabang("br-1")

	rows, err := svc.Invoices(ctx, cab, InvoiceFilter{Dari: "2026-10-01", Sampai: "2026-10-15"})
	if err != nil {
		t.Fatalf("Invoices periode: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("periode 1–15 Okt harus 2 invoice, dapat %d (%+v)", len(rows), rows)
	}
	// Inklusif di kedua ujungnya: nomor terbaru lebih dahulu.
	if rows[0].Nomor != "2026-10-0003" || rows[1].Nomor != "2026-10-0002" {
		t.Errorf("urutan/inklusivitas periode salah: %+v", rows)
	}

	// Tanpa batas: ketiganya tampil.
	semua, err := svc.Invoices(ctx, cab, InvoiceFilter{})
	if err != nil {
		t.Fatalf("Invoices tanpa filter: %v", err)
	}
	if len(semua) != 3 {
		t.Fatalf("tanpa filter harus 3 invoice, dapat %d", len(semua))
	}

	kasus := []struct {
		nama string
		f    InvoiceFilter
	}{
		{"format dari salah", InvoiceFilter{Dari: "01-10-2026"}},
		{"format sampai salah", InvoiceFilter{Sampai: "kemarin"}},
		{"rentang terbalik", InvoiceFilter{Dari: "2026-10-15", Sampai: "2026-10-01"}},
	}
	for _, k := range kasus {
		if _, err := svc.Invoices(ctx, cab, k.f); !errors.Is(err, ErrFilterInvalid) {
			t.Errorf("%s: harus ErrFilterInvalid, dapat %v", k.nama, err)
		}
	}
}

// TestInvoicesTidakMembocorkanKunciStorage memastikan jawaban riwayat invoice
// tidak pernah memuat kunci object storage: unduhan selalu lewat presigned URL.
func TestInvoicesTidakMembocorkanKunciStorage(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	tgl := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)
	mem.SetOrder(orderInvoice(orderA, "br-1", "2026-10-0001", StatusDone, 0, tgl))
	mem.TambahDokumen(Document{OrderID: orderA, Jenis: JenisInvoicePDF, Versi: 0,
		StorageKey: StorageKey(orderA, JenisInvoicePDF, 0), UkuranByte: 100})

	rows, err := serviceInvoice(mem).Invoices(ctx, actorCabang("br-1"), InvoiceFilter{})
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	j, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(j), "storage_key") || strings.Contains(string(j), StorageKey(orderA, JenisInvoicePDF, 0)) {
		t.Errorf("kunci storage bocor ke klien: %s", j)
	}
	for _, wajib := range []string{`"dokumen_id"`, `"terbaru"`, `"dapat_diunduh"`} {
		if !strings.Contains(string(j), wajib) {
			t.Errorf("jawaban tidak memuat %s: %s", wajib, j)
		}
	}
}
