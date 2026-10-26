package report

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// Dua cabang uji berbentuk UUID: filter cabang menuntut id yang sah, sama
// seperti validasi AdminFilter domain pesanan.
const (
	br1 = "11111111-1111-4111-8111-000000000001"
	br2 = "22222222-2222-4222-8222-000000000002"
)

func aktorSA() auth.Actor {
	return auth.Actor{UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina", Email: "rina@example.test"}
}

func aktorManager() auth.Actor {
	return auth.Actor{UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi", Email: "budi@example.test"}
}

func aktorCabang() auth.Actor {
	return auth.Actor{UserID: "usr-br", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: br1, Nama: "Bandung"}
}

// tanggalUji membuat waktu tengah malam WIB agar sama dengan nilai kolom date.
func tanggalUji(t *testing.T, v string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(LayoutTanggal, v, clock.Jakarta())
	if err != nil {
		t.Fatalf("tanggal uji %q: %v", v, err)
	}
	return parsed
}

// barisUji membuat satu baris laporan contoh.
func barisUji(t *testing.T, tanggal, nomor string, lineNo int, cabang string) Line {
	t.Helper()
	waktu := tanggalUji(t, tanggal)
	return Line{
		Tanggal:      tanggal,
		TanggalWaktu: waktu,
		Nomor:        nomor,
		Versi:        0,
		BranchID:     cabang,
		Cabang:       "Cabang " + cabang,
		KepalaCabang: "Kepala " + cabang,
		LineNo:       lineNo,
		Produk:       "Bio Magnetik",
		Jenis:        "product",
		SerialNumber: fmt.Sprintf("%s%02d", strings.ReplaceAll(tanggal, "-", ""), lineNo),
		Qty:          2,
		HargaSatuan:  100000,
		Subtotal:     200000,
		Status:       "submitted",
	}
}

func TestFilterTidakValidDitolak(t *testing.T) {
	svc := NewService(NewMemory(), storage.NewMemory("/files"))
	kasus := []struct {
		nama string
		in   Filter
	}{
		{"tanggal salah format", Filter{Dari: "08-10-2026"}},
		{"status tidak dikenal", Filter{Status: "lunas"}},
		{"status draft ditolak", Filter{Status: "draft"}},
		{"jenis tidak dikenal", Filter{Jenis: "jasa"}},
		{"cabang bukan uuid", Filter{Cabang: []string{"bandung"}}},
		{"rentang terbalik", Filter{Dari: "2026-10-31", Sampai: "2026-10-01"}},
	}
	for _, k := range kasus {
		if _, err := svc.List(context.Background(), aktorSA(), k.in); !errors.Is(err, ErrFilterInvalid) {
			t.Errorf("%s: harus ErrFilterInvalid, dapat %v", k.nama, err)
		}
	}
}

func TestPerHalamanDibatasi(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo, storage.NewMemory("/files"))
	page, err := svc.List(context.Background(), aktorSA(), Filter{PerHalaman: 5000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.PerHalaman != MaxPerHalaman {
		t.Errorf("per_halaman harus dibatasi %d, dapat %d", MaxPerHalaman, page.PerHalaman)
	}
	q := repo.Kueri()[0]
	if q.Batch != MaxPerHalaman {
		t.Errorf("query harus memakai batch %d, dapat %d", MaxPerHalaman, q.Batch)
	}
}

func TestHanyaSuperadminBolehMelihatDanMengekspor(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo, storage.NewMemory("/files"))
	ctx := context.Background()

	for _, aktor := range []auth.Actor{aktorManager(), aktorCabang(), auth.Anonymous()} {
		if _, err := svc.List(ctx, aktor, Filter{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("list oleh %s harus ErrForbidden, dapat %v", aktor.Role, err)
		}
		if _, err := svc.Request(ctx, aktor, Filter{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("request oleh %s harus ErrForbidden, dapat %v", aktor.Role, err)
		}
		if _, err := svc.Job(ctx, aktor, "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrForbidden) {
			t.Errorf("status job oleh %s harus ErrForbidden, dapat %v", aktor.Role, err)
		}
		if _, err := svc.Download(ctx, aktor, "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrForbidden) {
			t.Errorf("unduh oleh %s harus ErrForbidden, dapat %v", aktor.Role, err)
		}
	}
}

func TestDaftarLaporanSatuBarisPerOrderLineDanDraftDikecualikan(t *testing.T) {
	repo := NewMemory()
	repo.TambahLines(
		barisUji(t, "2026-10-01", "2026-10-01", 1, br1),
		barisUji(t, "2026-10-01", "2026-10-01", 2, br1),
		barisUji(t, "2026-10-02", "2026-10-02", 1, br2),
	)
	draft := barisUji(t, "2026-10-02", "", 1, br1)
	draft.Status = "draft"
	repo.TambahLines(draft)

	svc := NewService(repo, storage.NewMemory("/files"))
	page, err := svc.List(context.Background(), aktorSA(), Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 3 {
		t.Errorf("total baris harus 3 (draft dikecualikan), dapat %d", page.Total)
	}
	if len(page.Items) != 3 {
		t.Errorf("items harus 3, dapat %d", len(page.Items))
	}
	if page.TotalSubtotal != 600000 {
		t.Errorf("total subtotal harus 600000, dapat %d", page.TotalSubtotal)
	}
}

func TestFilterCabangStatusDanAtributCabang(t *testing.T) {
	repo := NewMemory()
	a := barisUji(t, "2026-10-01", "2026-10-01", 1, br1)
	a.IsCNC = true
	a.PajakIn = true
	b := barisUji(t, "2026-10-02", "2026-10-02", 1, br2)
	b.Status = "done"
	b.Jenis = "packaging"
	b.Qty = 1
	repo.TambahLines(a, b)

	svc := NewService(repo, storage.NewMemory("/files"))
	ctx := context.Background()
	ya := true

	uji := []struct {
		nama string
		in   Filter
		mau  int64
	}{
		{"cabang multi", Filter{Cabang: []string{br1, br2}}, 2},
		{"cabang tunggal", Filter{Cabang: []string{br2}}, 1},
		{"status done", Filter{Status: "done"}, 1},
		{"status submitted", Filter{Status: "submitted"}, 1},
		{"jenis packaging", Filter{Jenis: "packaging"}, 1},
		{"C&C", Filter{Cnc: &ya}, 1},
		{"PI", Filter{Pi: &ya}, 1},
		{"rentang tanggal", Filter{Dari: "2026-10-02", Sampai: "2026-10-02"}, 1},
	}
	for _, k := range uji {
		page, err := svc.List(ctx, aktorSA(), k.in)
		if err != nil {
			t.Fatalf("%s: %v", k.nama, err)
		}
		if page.Total != k.mau {
			t.Errorf("%s: total harus %d, dapat %d", k.nama, k.mau, page.Total)
		}
	}
}

func TestPaginasiMemakaiOffset(t *testing.T) {
	repo := NewMemory()
	for i := 1; i <= 5; i++ {
		repo.TambahLines(barisUji(t, "2026-10-01", fmt.Sprintf("2026-10-0%d", i), 1, br1))
	}
	svc := NewService(repo, storage.NewMemory("/files"))
	page, err := svc.List(context.Background(), aktorSA(), Filter{Halaman: 2, PerHalaman: 2})
	if err != nil {
		t.Fatalf("list halaman 2: %v", err)
	}
	if page.Total != 5 || len(page.Items) != 2 || page.Halaman != 2 || page.PerHalaman != 2 {
		t.Fatalf("paginasi salah: total=%d items=%d halaman=%d per=%d", page.Total, len(page.Items), page.Halaman, page.PerHalaman)
	}
	if page.Items[0].Nomor != "2026-10-03" {
		t.Errorf("baris pertama halaman 2 harus 2026-10-03, dapat %s", page.Items[0].Nomor)
	}
}

func TestEksporAsinkronMemakaiFilterTersimpan(t *testing.T) {
	repo := NewMemory()
	repo.TambahLines(
		barisUji(t, "2026-10-01", "2026-10-01", 1, br1),
		barisUji(t, "2026-10-02", "2026-10-02", 1, br2),
	)
	svc := NewService(repo, storage.NewMemory("/files"), WithBatchSize(100))
	ctx := context.Background()

	job, err := svc.Request(ctx, aktorSA(), Filter{Cabang: []string{strings.ToUpper(br1)}, Dari: "2026-10-01", Sampai: "2026-10-01", PerHalaman: 5000})
	if err != nil {
		t.Fatalf("request ekspor: %v", err)
	}
	if job.Status != StatusQueued || job.ID == "" || job.DimintaNama != "Rina" {
		t.Fatalf("job awal tidak sesuai: %+v", job)
	}
	// Filter tersimpan sudah dinormalkan (cabang huruf kecil, tanggal tetap).
	if len(job.Filter.Cabang) != 1 || job.Filter.Cabang[0] != br1 || job.Filter.Dari != "2026-10-01" {
		t.Errorf("filter tersimpan tidak ternormalisasi: %+v", job.Filter)
	}

	selesai, err := svc.Jalankan(ctx, job.ID)
	if err != nil {
		t.Fatalf("jalankan ekspor: %v", err)
	}
	if selesai.Status != StatusSelesai {
		t.Fatalf("status akhir harus selesai, dapat %s (pesan %s)", selesai.Status, selesai.Pesan)
	}
	// Jumlah baris ekspor HARUS sama dengan query terfilter (PRD §9 no.10).
	page, err := svc.List(ctx, aktorSA(), job.Filter)
	if err != nil {
		t.Fatalf("list pembanding: %v", err)
	}
	if selesai.JumlahBaris != page.Total || selesai.JumlahBaris != 1 {
		t.Errorf("jumlah baris ekspor %d harus sama dengan query terfilter %d", selesai.JumlahBaris, page.Total)
	}
	if selesai.NamaFile == "" || !strings.HasSuffix(selesai.NamaFile, ".xlsx") {
		t.Errorf("nama berkas hasil tidak wajar: %q", selesai.NamaFile)
	}

	dl, err := svc.Download(ctx, aktorSA(), job.ID)
	if err != nil {
		t.Fatalf("unduh: %v", err)
	}
	if dl.URL == "" || dl.ExpiresIn != int(DefaultPresignTTL.Seconds()) || dl.MimeType != MimeXLSX {
		t.Errorf("tautan unduh tidak sesuai: %+v", dl)
	}
	if dl.NamaFile != selesai.NamaFile {
		t.Errorf("nama berkas unduh harus sama dengan metadata job: %q vs %q", dl.NamaFile, selesai.NamaFile)
	}
}

func TestUnduhSebelumSelesaiDitolak(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo, storage.NewMemory("/files"))
	ctx := context.Background()
	job, err := svc.Request(ctx, aktorSA(), Filter{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := svc.Download(ctx, aktorSA(), job.ID); !errors.Is(err, ErrJobNotReady) {
		t.Errorf("unduh sebelum selesai harus ErrJobNotReady, dapat %v", err)
	}
	if _, err := svc.Job(ctx, aktorSA(), "bukan-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("id bukan UUID harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.Download(ctx, aktorSA(), "bukan-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unduh id bukan UUID harus ErrNotFound, dapat %v", err)
	}
}

func TestStorageBelumSiapMenolakPermintaanEkspor(t *testing.T) {
	svc := NewService(NewMemory(), storage.Disabled{})
	if _, err := svc.Request(context.Background(), aktorSA(), Filter{}); !errors.Is(err, ErrStorageUnavailable) {
		t.Errorf("storage belum siap harus ErrStorageUnavailable, dapat %v", err)
	}
}

func TestAntreanBelumSiapTidakMembuatPekerjaanGantung(t *testing.T) {
	repo := NewMemory()
	repo.TanpaAntrean = true
	svc := NewService(repo, storage.NewMemory("/files"))
	if _, err := svc.Request(context.Background(), aktorSA(), Filter{}); !errors.Is(err, ErrQueueUnavailable) {
		t.Errorf("antrean belum siap harus ErrQueueUnavailable, dapat %v", err)
	}
	if len(repo.Lines()) != 0 {
		t.Errorf("tidak boleh ada baris tersisa: %v", repo.Lines())
	}
}

func TestEksporBertahapTidakMenahanSeluruhData(t *testing.T) {
	repo := NewMemory()
	for i := 1; i <= 5; i++ {
		repo.TambahLines(barisUji(t, "2026-10-01", fmt.Sprintf("2026-10-0%d", i), 1, br1))
	}
	svc := NewService(repo, storage.NewMemory("/files"), WithBatchSize(2))
	ctx := context.Background()
	job, err := svc.Request(ctx, aktorSA(), Filter{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	hasil, err := svc.Jalankan(ctx, job.ID)
	if err != nil {
		t.Fatalf("jalankan: %v", err)
	}
	if hasil.JumlahBaris != 5 {
		t.Fatalf("jumlah baris harus 5, dapat %d", hasil.JumlahBaris)
	}

	// 5 baris dengan batch 2 → 3 permintaan (2, 2, 1). Setiap permintaan
	// memakai kursor baris terakhir batch sebelumnya — inilah bukti baris
	// dibaca bertahap, bukan sekali ambil seluruh isi.
	kueri := repo.Kueri()
	if len(kueri) != 3 {
		t.Fatalf("harus 3 permintaan batch, dapat %d (%+v)", len(kueri), kueri)
	}
	if kueri[0].Batch != 2 || kueri[0].SetelahTanggal != nil {
		t.Errorf("permintaan pertama harus tanpa kursor dan batch 2: %+v", kueri[0])
	}
	for i := 1; i < len(kueri); i++ {
		if kueri[i].SetelahTanggal == nil || kueri[i].SetelahNomor == "" {
			t.Errorf("permintaan ke-%d harus memakai kursor: %+v", i+1, kueri[i])
		}
	}
	if kueri[1].SetelahNomor != "2026-10-02" || kueri[2].SetelahNomor != "2026-10-04" {
		t.Errorf("kursor tidak maju sesuai batch: %q, %q", kueri[1].SetelahNomor, kueri[2].SetelahNomor)
	}
}

func TestJalankanUlangIdempoten(t *testing.T) {
	repo := NewMemory()
	repo.TambahLines(barisUji(t, "2026-10-01", "2026-10-01", 1, br1))
	store := &hitungStore{Store: storage.NewMemory("/files")}
	svc := NewService(repo, store, WithBatchSize(100))
	ctx := context.Background()
	job, err := svc.Request(ctx, aktorSA(), Filter{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := svc.Jalankan(ctx, job.ID); err != nil {
		t.Fatalf("jalankan pertama: %v", err)
	}
	putPertama := store.put
	if _, err := svc.Jalankan(ctx, job.ID); err != nil {
		t.Fatalf("jalankan kedua: %v", err)
	}
	if store.put != putPertama {
		t.Errorf("pekerjaan yang sudah selesai tidak boleh menulis ulang objek (%d → %d)", putPertama, store.put)
	}
	if _, err := svc.Job(ctx, aktorSA(), job.ID); err != nil {
		t.Fatalf("job setelah ekspor: %v", err)
	}
}

func TestIsiBerkasXLSXSesuaiKontrak(t *testing.T) {
	repo := NewMemory()
	pertama := barisUji(t, "2026-09-30", "2026-09-01", 1, br1)
	pertama.Status = "done"
	pertama.TanggalVerifikasi = "2026-09-30 14:05"
	pertama.MetodeVerifikasi = "manual"
	pertama.IsCNC = true
	kedua := barisUji(t, "2026-10-01", "2026-10-01", 2, br2)
	kedua.Jenis = "packaging"
	kedua.SerialNumber = ""
	kedua.Qty = 1
	kedua.HargaSatuan = 3000
	kedua.Subtotal = 3000
	repo.TambahLines(pertama, kedua)

	store := storage.NewMemory("/files")
	svc := NewService(repo, store, WithBatchSize(1))

	job, err := svc.Request(context.Background(), aktorSA(), Filter{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := svc.Jalankan(context.Background(), job.ID); err != nil {
		t.Fatalf("jalankan: %v", err)
	}

	raw, err := store.Get(context.Background(), StorageKey(JenisOrderLines, job.ID))
	if err != nil {
		t.Fatalf("ambil berkas hasil: %v", err)
	}
	defer func() { _ = raw.Close() }()
	isi, err := io.ReadAll(raw)
	if err != nil {
		t.Fatalf("baca berkas hasil: %v", err)
	}

	f, err := excelize.OpenReader(bytes.NewReader(isi))
	if err != nil {
		t.Fatalf("buka xlsx hasil: %v", err)
	}
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetList()
	if len(sheet) != 2 || sheet[0] != SheetLaporan || sheet[1] != SheetRekap {
		t.Fatalf("sheet harus [%s %s], dapat %v", SheetLaporan, SheetRekap, sheet)
	}

	baris, err := f.GetRows(SheetLaporan, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("baca sheet laporan: %v", err)
	}
	cocokJudul := []string{
		"Tanggal", "Nomor Invoice", "Versi", "Cabang", "Kepala Cabang", "C&C", "PI",
		"Produk", "Jenis", "Serial Number", "Qty", "Harga Satuan", "Subtotal", "Status",
		"Tanggal Verifikasi", "Metode Verifikasi",
	}
	if len(baris) == 0 || len(baris[0]) != len(cocokJudul) {
		t.Fatalf("header tidak sesuai: %v", baris)
	}
	for i, judul := range cocokJudul {
		if baris[0][i] != judul {
			t.Errorf("kolom %d harus %q, dapat %q", i+1, judul, baris[0][i])
		}
	}
	if len(baris) != 3 {
		t.Fatalf("sheet laporan harus 3 baris (header + 2 data), dapat %d", len(baris))
	}
	if baris[1][5] != "Ya" {
		t.Errorf("C&C harus Ya: %v", baris[1])
	}
	if baris[1][14] != "2026-09-30 14:05" || baris[1][15] != "manual" {
		t.Errorf("kolom verifikasi salah: %v", baris[1])
	}
	if baris[2][9] != "-" {
		t.Errorf("serial kosong harus tampil '-', dapat %q", baris[2][9])
	}

	// Header dibekukan (AP-REP-04).
	panes, err := f.GetPanes(SheetLaporan)
	if err != nil {
		t.Fatalf("baca panes: %v", err)
	}
	if !panes.Freeze || panes.YSplit != 1 {
		t.Errorf("header harus ter-freeze: %+v", panes)
	}

	// Format Rupiah pada kolom harga satuan & subtotal (L dan M).
	for _, sel := range []string{"L2", "M2", "M3"} {
		id, err := f.GetCellStyle(SheetLaporan, sel)
		if err != nil {
			t.Fatalf("gaya sel %s: %v", sel, err)
		}
		style, err := f.GetStyle(id)
		if err != nil {
			t.Fatalf("baca gaya %s: %v", sel, err)
		}
		if style.CustomNumFmt == nil || !strings.Contains(*style.CustomNumFmt, "Rp") {
			t.Errorf("sel %s harus berformat Rupiah, dapat %v", sel, style.CustomNumFmt)
		}
	}

	// Nilai uang tetap ANGKA (bisa dijumlahkan), bukan teks — baris dibaca
	// mentah (nilai tampilan sudah diformat Rupiah oleh numFmt di atas).
	if baris[1][11] != "100000" {
		t.Errorf("harga satuan harus angka 100000, dapat %q", baris[1][11])
	}
	if baris[2][12] != "3000" {
		t.Errorf("subtotal packaging harus angka 3000, dapat %q", baris[2][12])
	}
	if baris[1][10] != "2" {
		t.Errorf("qty harus angka 2, dapat %q", baris[1][10])
	}

	// Sheet rekap bulanan per cabang: 2 bulan × 2 cabang + header.
	rekap, err := f.GetRows(SheetRekap, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("baca sheet rekap: %v", err)
	}
	if len(rekap) != 3 {
		t.Fatalf("rekap harus 3 baris (header + 2), dapat %d: %v", len(rekap), rekap)
	}
	judulRekap := []string{"Bulan", "Cabang", "Jumlah Baris", "Jumlah Qty", "Total Subtotal"}
	for i, judul := range judulRekap {
		if rekap[0][i] != judul {
			t.Errorf("header rekap kolom %d harus %q, dapat %q", i+1, judul, rekap[0][i])
		}
	}
	if rekap[1][0] != "2026-09" || rekap[1][1] != "Cabang "+br1 || rekap[1][2] != "1" || rekap[1][4] != "200000" {
		t.Errorf("rekap baris pertama salah: %v", rekap[1])
	}
	if rekap[2][0] != "2026-10" || rekap[2][1] != "Cabang "+br2 || rekap[2][4] != "3000" {
		t.Errorf("rekap baris kedua salah: %v", rekap[2])
	}
}

// hitungStore menghitung berapa kali objek ditulis (uji idempotensi).
type hitungStore struct {
	storage.Store
	put int
}

func (s *hitungStore) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	s.put++
	return s.Store.Put(ctx, key, body, size, contentType)
}

// gagalStore selalu menolak Put: dipakai membuktikan jalur gagal ekspor tidak
// meninggalkan berkas sementara di direktori temp.
type gagalStore struct{ storage.Store }

func (gagalStore) Put(context.Context, string, io.Reader, int64, string) error {
	return errors.New("object storage sedang penuh")
}

// berkasLaporan mengembalikan nama berkas laporan-*.xlsx yang tersisa di dir.
func berkasLaporan(t *testing.T, dir string) []string {
	t.Helper()
	sisa, err := filepath.Glob(filepath.Join(dir, "laporan-*.xlsx"))
	if err != nil {
		t.Fatalf("glob berkas sementara: %v", err)
	}
	return sisa
}

// TestTidakMeninggalkanBerkasSementaraSaatGagal membuktikan jalur gagal
// penulisan XLSX tidak menumpuk berkas yatim di direktori temp: percobaan ulang
// worker River (at-least-once) pada storage/disk yang bermasalah tidak boleh
// menumpuk berkas laporan-*.xlsx tak terpakai.
func TestTidakMeninggalkanBerkasSementaraSaatGagal(t *testing.T) {
	dir := t.TempDir()
	sebab := errors.New("penulisan berkas gagal")

	// Satu percobaan gagal: galat diteruskan apa adanya, berkas dibersihkan.
	if _, _, err := tulisKeTemp(dir, "laporan-*.xlsx", func(io.Writer) (int64, error) {
		return 0, sebab
	}); !errors.Is(err, sebab) {
		t.Fatalf("galat penulisan tidak diteruskan: %v", err)
	}
	if sisa := berkasLaporan(t, dir); len(sisa) != 0 {
		t.Fatalf("berkas sementara tersisa setelah gagal: %v", sisa)
	}

	// Percobaan ulang berkali-kali tidak boleh menumpuk apa pun.
	for i := 0; i < 20; i++ {
		if _, _, err := tulisKeTemp(dir, "laporan-*.xlsx", func(io.Writer) (int64, error) {
			return 0, sebab
		}); err == nil {
			t.Fatal("penulisan gagal seharusnya mengembalikan galat")
		}
	}
	if sisa := berkasLaporan(t, dir); len(sisa) != 0 {
		t.Fatalf("berkas sementara menumpuk setelah percobaan ulang: %v", sisa)
	}

	// Jalur sukses: berkas ada dan isinya utuh (pemanggil yang menghapusnya).
	path, n, err := tulisKeTemp(dir, "laporan-*.xlsx", func(w io.Writer) (int64, error) {
		isi := []byte("laporan")
		if _, err := w.Write(isi); err != nil {
			return 0, err
		}
		return int64(len(isi)), nil
	})
	if err != nil {
		t.Fatalf("jalur sukses gagal: %v", err)
	}
	if n != int64(len("laporan")) {
		t.Fatalf("ukuran berkas = %d, ingin %d", n, len("laporan"))
	}
	isi, err := os.ReadFile(path)
	if err != nil || string(isi) != "laporan" {
		t.Fatalf("isi berkas sementara = %q (err %v)", isi, err)
	}
	if sisa := berkasLaporan(t, dir); len(sisa) != 1 {
		t.Fatalf("jalur sukses harus menyisakan tepat satu berkas, dapat %v", sisa)
	}
}

// TestJalankanGagalTidakMeninggalkanBerkasSementara membuktikan pada tingkat
// service: bila pengunggahan hasil ekspor gagal, direktori temp kembali kosong
// (berkas sementara dihapus oleh pemanggil pada jalur gagal).
func TestJalankanGagalTidakMeninggalkanBerkasSementara(t *testing.T) {
	dir := t.TempDir()
	repo := NewMemory()
	repo.TambahLines(barisUji(t, "2026-10-01", "2026-10-01", 1, br1))
	svc := NewService(repo, gagalStore{storage.NewMemory("/files")}, WithTempDir(dir), WithBatchSize(100))

	job, err := svc.Request(context.Background(), aktorSA(), Filter{PerHalaman: 5000})
	if err != nil {
		t.Fatalf("request ekspor: %v", err)
	}
	if _, err := svc.Jalankan(context.Background(), job.ID); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("ekspor dengan storage gagal harus mengembalikan ErrStorageUnavailable, dapat %v", err)
	}
	if sisa := berkasLaporan(t, dir); len(sisa) != 0 {
		t.Fatalf("berkas sementara tersisa setelah ekspor gagal: %v", sisa)
	}
	status, err := svc.Job(context.Background(), aktorSA(), job.ID)
	if err != nil {
		t.Fatalf("job setelah gagal: %v", err)
	}
	if status.Status != StatusGagal {
		t.Fatalf("status job setelah storage gagal = %s, ingin %s", status.Status, StatusGagal)
	}
}
