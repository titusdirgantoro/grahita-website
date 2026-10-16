package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// orderA dan orderB adalah id pesanan berbentuk UUID: service menolak id yang
// bukan UUID dengan ErrNotFound (tidak membocorkan keberadaan).
const (
	orderA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	orderB = "0f8fad5b-d9cb-469f-a165-70867728950f"
)

// pdfPalsu adalah PDF minimal yang tetap berstruktur PDF; dipakai renderer
// palsu supaya pengujian unit tidak membutuhkan Gotenberg. Validitas PDF
// sungguhan diperiksa lewat uji/probe yang memakai Chromium nyata.
var pdfPalsu = []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]>>endobj\n" +
	"trailer<</Root 1 0 R>>\n%%EOF\n")

// rendererPalsu mencatat HTML yang diterima dan mengembalikan pdfPalsu.
type rendererPalsu struct {
	mu     sync.Mutex
	html   []string
	err    error
	kosong bool
}

func (r *rendererPalsu) HTMLToPDF(_ context.Context, html []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.html = append(r.html, string(html))
	if r.err != nil {
		return nil, r.err
	}
	if r.kosong {
		return nil, nil
	}
	return pdfPalsu, nil
}

// Health melengkapi kontrak pdf.Renderer (tidak dipakai domain dokumen).
func (r *rendererPalsu) Health(context.Context) error { return nil }

// storeHitung membungkus storage.Memory: menghitung Put/Delete dan bisa
// dipaksa gagal pada Put ke-n.
type storeHitung struct {
	*storage.Memory
	mu      sync.Mutex
	put     int
	hapus   int
	gagalKe int
	putErr  error
}

func newStoreHitung() *storeHitung {
	return &storeHitung{Memory: storage.NewMemory("/files")}
}

func (s *storeHitung) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	s.mu.Lock()
	s.put++
	ke := s.put
	gagal := s.putErr
	s.mu.Unlock()
	if gagal != nil && (s.gagalKe == 0 || s.gagalKe == ke) {
		return gagal
	}
	return s.Memory.Put(ctx, key, body, size, contentType)
}

func (s *storeHitung) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.hapus++
	s.mu.Unlock()
	return s.Memory.Delete(ctx, key)
}

func (s *storeHitung) jumlahPut() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.put
}

// isiObjek membaca isi satu objek; gagal bila tidak ada.
func isiObjek(t *testing.T, s *storeHitung, key string) []byte {
	t.Helper()
	rc, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("objek %s tidak ada: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("baca objek %s: %v", key, err)
	}
	return b
}

// fixtureOrder adalah pesanan Done satu cabang dengan dua baris produk dan satu
// baris packaging, lengkap dengan serialnya.
func fixtureOrder(id string) Order {
	tanggal := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)
	jt := time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)
	return Order{
		ID:       id,
		Nomor:    "2026-10-0001",
		Status:   StatusDone,
		Versi:    0,
		BranchID: "br-1",
		Total:    5003000,
		Catatan:  "Tolong dikirim pagi",
		Tanggal:  tanggal,
		Cabang: Cabang{
			Nama:    "Grahita Bandung",
			Kepala:  "Sandi",
			Email:   "bandung@example.test",
			Telepon: "022-123456",
		},
		Lines: []Line{
			{LineNo: 1, Jenis: "product", Nama: "Bio Magnetik", Qty: 2, HargaSatuan: 2500000, Subtotal: 5000000, SerialNumber: "20261008001"},
			{LineNo: 2, Jenis: "product", Nama: "Bio Listrik", Qty: 1, HargaSatuan: 3000, Subtotal: 3000, SerialNumber: "20261008002"},
		},
		Rekening: []Rekening{
			{Bank: "BCA", Nomor: "1234567890", AtasNama: "PT Grahita Indonesia"},
			{Bank: "BRI", Nomor: "0987654321", AtasNama: "PT Grahita Indonesia"},
		},
		JatuhTempo: &jt,
	}
}

// actorCabang & actorAdmin adalah aktor pengujian.
func actorCabang(branchID string) auth.Actor {
	return auth.Actor{UserID: "u-cabang", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: branchID, Nama: "Grahita Bandung"}
}

func actorAdmin() auth.Actor {
	return auth.Actor{UserID: "u-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
}

func TestFormatAngkaDanTanggal(t *testing.T) {
	kasus := []struct {
		n   int64
		mau string
	}{
		{0, "0"},
		{5, "5"},
		{999, "999"},
		{1000, "1.000"},
		{5003000, "5.003.000"},
		{1234567890, "1.234.567.890"},
		{-2500, "-2.500"},
	}
	for _, k := range kasus {
		if got := FormatAngka(k.n); got != k.mau {
			t.Errorf("FormatAngka(%d) = %q, mau %q", k.n, got, k.mau)
		}
	}
	if got := FormatRupiah(5003000); got != "Rp 5.003.000" {
		t.Errorf("FormatRupiah = %q", got)
	}
	if got := FormatTanggal(time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)); got != "08 Oktober 2026" {
		t.Errorf("FormatTanggal = %q", got)
	}
}

func TestKunciStorageDanNamaBerkas(t *testing.T) {
	kasus := []struct {
		jenis string
		key   string
		mime  string
	}{
		{JenisInvoicePDF, StorageKey(orderA, JenisInvoicePDF, 0), MimePDF},
		{JenisSuratJalanPDF, StorageKey(orderA, JenisSuratJalanPDF, 3), MimePDF},
		{JenisSuratJalanXLSX, StorageKey(orderA, JenisSuratJalanXLSX, 3), MimeXLSX},
	}
	for _, k := range kasus {
		if got := StorageKey(orderA, k.jenis, 0); k.jenis == JenisInvoicePDF && got != k.key {
			t.Errorf("StorageKey(%s) = %q, mau %q", k.jenis, got, k.key)
		}
		if got := MimeType(k.jenis); got != k.mime {
			t.Errorf("MimeType(%s) = %q", k.jenis, got)
		}
	}
	if got := StorageKey(orderA, JenisSuratJalanPDF, 3); got != StorageKey(orderA, JenisSuratJalanPDF, 3) {
		t.Errorf("kunci versi 3 = %q", got)
	}
	if got := StorageKey(orderA, JenisSuratJalanXLSX, 3); got != StorageKey(orderA, JenisSuratJalanXLSX, 3) {
		t.Errorf("kunci xlsx versi 3 = %q", got)
	}
	if got := NamaBerkas(JenisInvoicePDF, "2026-10-0001"); got != "Invoice-2026-10-0001.pdf" {
		t.Errorf("nama invoice = %q", got)
	}
	if got := NamaBerkas(JenisSuratJalanXLSX, "2026-10-0001"); got != "Surat-Jalan-2026-10-0001.xlsx" {
		t.Errorf("nama surat jalan xlsx = %q", got)
	}
	if got := NamaBerkas(JenisSuratJalanPDF, "2026-10-0001"); got != "Surat-Jalan-2026-10-0001.pdf" {
		t.Errorf("nama surat jalan pdf = %q", got)
	}
}

// TestEnsureMembuatTigaDokumen memeriksa janji utama kartu: satu permintaan
// render menghasilkan ketiga berkas dengan kunci `documents/{order}/{jenis}-{versi}.{ext}`.
func TestEnsureMembuatTigaDokumen(t *testing.T) {
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))
	store := newStoreHitung()
	rend := &rendererPalsu{}
	svc := NewService(repo, store, rend)

	docs, err := svc.Ensure(context.Background(), orderA)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("harus 3 dokumen, dapat %d", len(docs))
	}
	mau := map[string]string{
		JenisInvoicePDF:     StorageKey(orderA, JenisInvoicePDF, 0),
		JenisSuratJalanXLSX: StorageKey(orderA, JenisSuratJalanXLSX, 0),
		JenisSuratJalanPDF:  StorageKey(orderA, JenisSuratJalanPDF, 0),
	}
	for _, d := range docs {
		if d.StorageKey != mau[d.Jenis] {
			t.Errorf("kunci %s = %q, mau %q", d.Jenis, d.StorageKey, mau[d.Jenis])
		}
		if d.ID == "" {
			t.Errorf("dokumen %s tanpa id", d.Jenis)
		}
		if d.UkuranByte <= 0 {
			t.Errorf("dokumen %s tanpa ukuran", d.Jenis)
		}
	}
	if store.jumlahPut() != 3 {
		t.Errorf("harus 3 tulisan storage, dapat %d", store.jumlahPut())
	}
	// XLSX harus benar-benar berkas XLSX (zip PK), PDF dari renderer.
	if b := isiObjek(t, store, mau[JenisSuratJalanXLSX]); !bytes.HasPrefix(b, []byte("PK")) {
		t.Error("berkas surat jalan XLSX bukan berkas xlsx (tidak berawalan PK)")
	}
	if b := isiObjek(t, store, mau[JenisInvoicePDF]); !bytes.HasPrefix(b, []byte("%PDF")) {
		t.Error("berkas invoice bukan PDF")
	}
	// Dua dokumen PDF dirender dari HTML invoice/surat jalan.
	if len(rend.html) != 2 {
		t.Fatalf("harus 2 render HTML, dapat %d", len(rend.html))
	}
	if !strings.Contains(rend.html[0], "INVOICE") || !strings.Contains(rend.html[0], "2026-10-0001") {
		t.Error("HTML invoice tidak memuat judul/nomor")
	}
	if !strings.Contains(rend.html[1], "SURAT JALAN") {
		t.Error("HTML surat jalan tidak memuat judul")
	}
}

// TestEnsureIdempotenTidakMenimpaBerkas memeriksa dua janji: render ulang tidak
// menggandakan dokumen DAN berkas versi yang sudah ada tidak ditulis ulang
// walau data pesanannya berubah setelahnya.
func TestEnsureIdempotenTidakMenimpaBerkas(t *testing.T) {
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))
	store := newStoreHitung()
	svc := NewService(repo, store, &rendererPalsu{})

	docs1, err := svc.Ensure(context.Background(), orderA)
	if err != nil {
		t.Fatalf("Ensure pertama: %v", err)
	}
	kunciInvoice := StorageKey(orderA, JenisInvoicePDF, 0)
	sebelum := isiObjek(t, store, kunciInvoice)

	// Data pesanan berubah (mis. admin menyimpan catatan) — dokumen versi 0
	// TIDAK boleh ikut berubah.
	o := fixtureOrder(orderA)
	o.Catatan = "catatan berubah setelah render"
	o.Total = 9_999_999
	repo.SetOrder(o)

	docs2, err := svc.Ensure(context.Background(), orderA)
	if err != nil {
		t.Fatalf("Ensure kedua: %v", err)
	}
	if repo.JumlahDokumen() != 3 {
		t.Errorf("render ulang tidak boleh menggandakan dokumen, dapat %d", repo.JumlahDokumen())
	}
	if store.jumlahPut() != 3 {
		t.Errorf("render ulang tidak boleh menulis ulang berkas, total Put = %d", store.jumlahPut())
	}
	if !bytes.Equal(sebelum, isiObjek(t, store, kunciInvoice)) {
		t.Error("berkas invoice versi 0 berubah padahal versinya sama")
	}
	for i, d := range docs1 {
		if docs2[i].ID != d.ID {
			t.Errorf("id dokumen %s berubah: %s -> %s", d.Jenis, d.ID, docs2[i].ID)
		}
	}
}

// TestVersiBaruMemakaiKunciBaru memastikan revisi invoice (versi pesanan naik)
// menghasilkan berkas BARU dan meninggalkan berkas versi lama utuh.
func TestVersiBaruMemakaiKunciBaru(t *testing.T) {
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))
	store := newStoreHitung()
	svc := NewService(repo, store, &rendererPalsu{})

	if _, err := svc.Ensure(context.Background(), orderA); err != nil {
		t.Fatalf("Ensure versi 0: %v", err)
	}
	lama := isiObjek(t, store, StorageKey(orderA, JenisInvoicePDF, 0))

	o := fixtureOrder(orderA)
	o.Versi = 1
	o.Total = 6_000_000
	o.Catatan = "sudah direvisi"
	repo.SetOrder(o)

	doc, err := svc.EnsureInvoice(context.Background(), orderA)
	if err != nil {
		t.Fatalf("EnsureInvoice versi 1: %v", err)
	}
	if doc.Versi != 1 || doc.StorageKey != StorageKey(orderA, JenisInvoicePDF, 1) {
		t.Fatalf("dokumen revisi salah: %+v", doc)
	}
	if repo.JumlahDokumen() != 4 {
		t.Errorf("harus 4 baris (3 versi 0 + 1 versi 1), dapat %d", repo.JumlahDokumen())
	}
	if !bytes.Equal(lama, isiObjek(t, store, StorageKey(orderA, JenisInvoicePDF, 0))) {
		t.Error("berkas invoice versi 0 tertimpa oleh versi 1")
	}
	baru := isiObjek(t, store, StorageKey(orderA, JenisInvoicePDF, 1))
	if len(baru) == 0 {
		t.Error("berkas versi 1 kosong")
	}
}

// TestPesananBelumDoneDitolak menjaga PRD §6.1: dokumen lahir saat Done.
func TestPesananBelumDoneDitolak(t *testing.T) {
	for _, status := range []string{"draft", "submitted"} {
		repo := NewMemory()
		o := fixtureOrder(orderA)
		o.Status = status
		repo.SetOrder(o)
		store := newStoreHitung()
		svc := NewService(repo, store, &rendererPalsu{})

		if _, err := svc.Ensure(context.Background(), orderA); !errors.Is(err, ErrNotDone) {
			t.Errorf("status %s harus ErrNotDone, dapat %v", status, err)
		}
		if repo.JumlahDokumen() != 0 || store.jumlahPut() != 0 {
			t.Errorf("status %s tidak boleh menghasilkan dokumen/berkas", status)
		}
	}
}

// TestKegagalanRenderTidakMenyisakanBarisDokumen memeriksa sifat satu-unit-kerja
// di sisi database: gagal di tengah jalan tidak pernah meninggalkan baris
// dokumen. Objek yang terlanjur ditulis pada jalur gagal DIBIARKAN dengan
// sengaja (aturan putaran 3 — lihat renderDanSimpan): menghapus kunci
// deterministik bisa mencabut berkas permintaan lain yang menang balapan.
func TestKegagalanRenderTidakMenyisakanBarisDokumen(t *testing.T) {
	t.Run("mesin PDF tidak siap", func(t *testing.T) {
		repo := NewMemory()
		repo.SetOrder(fixtureOrder(orderA))
		store := newStoreHitung()
		rend := &rendererPalsu{err: pdf.ErrNotConfigured}
		svc := NewService(repo, store, rend)

		_, err := svc.Ensure(context.Background(), orderA)
		if !errors.Is(err, ErrRenderUnavailable) {
			t.Fatalf("harus ErrRenderUnavailable, dapat %v", err)
		}
		if repo.JumlahDokumen() != 0 || store.jumlahPut() != 0 {
			t.Errorf("gagal render tidak boleh menyimpan apa pun: dok=%d put=%d", repo.JumlahDokumen(), store.jumlahPut())
		}
	})

	t.Run("gagal menulis storage", func(t *testing.T) {
		repo := NewMemory()
		repo.SetOrder(fixtureOrder(orderA))
		store := newStoreHitung()
		store.putErr = errors.New("storage mati")
		store.gagalKe = 2 // dokumen pertama lolos, kedua gagal
		svc := NewService(repo, store, &rendererPalsu{})

		_, err := svc.Ensure(context.Background(), orderA)
		if !errors.Is(err, ErrStorageUnavailable) {
			t.Fatalf("harus ErrStorageUnavailable, dapat %v", err)
		}
		if repo.JumlahDokumen() != 0 {
			t.Errorf("tidak boleh ada baris dokumen, dapat %d", repo.JumlahDokumen())
		}
		// Aturan putaran 3: objek yang terlanjur ditulis TIDAK dihapus. Menghapus
		// kunci deterministik bisa mencabut berkas permintaan lain yang menang
		// balapan; objek yatim direkonsiliasi F1-21.
		if store.hapus != 0 {
			t.Errorf("jalur gagal tidak boleh menghapus objek, dapat %d penghapusan", store.hapus)
		}
		if isi := isiObjek(t, store, StorageKey(orderA, JenisInvoicePDF, 0)); len(isi) == 0 {
			t.Error("objek yang terlanjur ditulis harus tetap ada (kandidat rekonsiliasi F1-21)")
		}
		if _, err := store.Get(context.Background(), StorageKey(orderA, JenisSuratJalanXLSX, 0)); err == nil {
			t.Error("Put yang gagal tidak boleh menghasilkan objek")
		}
	})

	t.Run("gagal menyimpan metadata", func(t *testing.T) {
		repo := NewMemory()
		repo.SetOrder(fixtureOrder(orderA))
		store := newStoreHitung()
		repo.GagalUntuk = orderA
		svc := NewService(repo, store, &rendererPalsu{})

		if _, err := svc.Ensure(context.Background(), orderA); err == nil {
			t.Fatal("harus gagal saat metadata tidak bisa disimpan")
		}
		if repo.JumlahDokumen() != 0 {
			t.Errorf("tidak boleh ada baris dokumen, dapat %d", repo.JumlahDokumen())
		}
		if store.hapus != 0 {
			t.Errorf("jalur gagal tidak boleh menghapus objek, dapat %d penghapusan", store.hapus)
		}
		for _, k := range []string{
			StorageKey(orderA, JenisInvoicePDF, 0),
			StorageKey(orderA, JenisSuratJalanXLSX, 0),
			StorageKey(orderA, JenisSuratJalanPDF, 0),
		} {
			if isi := isiObjek(t, store, k); len(isi) == 0 {
				t.Errorf("objek %s harus tetap ada (kandidat rekonsiliasi F1-21)", k)
			}
		}
	})
}

// storeGerbang menahan SaveAll (lihat repoBalapanNyata) sampai sejumlah
// penulisan objek terjadi, supaya dua permintaan paralel benar-benar MELEWATI
// jalur render+Put lebih dulu dan balapannya terjadi di titik penyimpanan
// metadata — bukan salah satu pulang sebelum sempat merender.
type storeGerbang struct {
	*storeHitung
	mu     sync.Mutex
	n      int
	batas  int
	siap   chan struct{}
	sekali sync.Once
}

func newStoreGerbang(batas int) *storeGerbang {
	return &storeGerbang{storeHitung: newStoreHitung(), batas: batas, siap: make(chan struct{})}
}

func (g *storeGerbang) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := g.storeHitung.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	g.mu.Lock()
	g.n++
	penuh := g.n >= g.batas
	g.mu.Unlock()
	if penuh {
		g.sekali.Do(func() { close(g.siap) })
	}
	return nil
}

// repoBalapanNyata meniru dua permintaan render PARALEL atas dokumen yang sama:
// panggilan SaveAll pertama commit barisnya, panggilan berikutnya GAGAL setelah
// baris itu terlihat (mis. pool acquire timeout / error Commit dengan ctx masih
// hidup) — persis skenario yang dilaporkan QA pada f7d035b.
type repoBalapanNyata struct {
	*Memory
	siap    <-chan struct{}
	mu      sync.Mutex
	panggil int
}

func (r *repoBalapanNyata) SaveAll(ctx context.Context, req SaveRequest) ([]Document, error) {
	select {
	case <-r.siap:
	case <-time.After(30 * time.Second):
		return nil, errors.New("uji buntu: gerbang balapan tidak pernah terbuka")
	}
	r.mu.Lock()
	r.panggil++
	ke := r.panggil
	r.mu.Unlock()
	if ke > 1 {
		return nil, errors.New("simulasi: SaveAll gagal setelah pesaing commit")
	}
	return r.Memory.SaveAll(ctx, req)
}

// TestKegagalanSimpanMetadataTidakMencabutBerkasBarisLain adalah uji regresi
// temuan pemblokir QA F1-17: kunci storage deterministik dipakai bersama, jadi
// permintaan yang SaveAll-nya gagal TIDAK boleh menghapus objek yang sudah
// dirujuk baris `documents` milik permintaan lain yang lebih dulu commit.
// Kalau dihapus, pesanan kehilangan Invoice/Surat Jalan secara permanen karena
// render ulang menganggap barisnya sudah ada.
func TestKegagalanSimpanMetadataTidakMencabutBerkasBarisLain(t *testing.T) {
	ctx := context.Background()
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))
	store := newStoreGerbang(6) // 2 permintaan x 3 dokumen
	svc := NewService(&repoBalapanNyata{Memory: repo, siap: store.siap}, store, &rendererPalsu{})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Ensure(ctx, orderA)
		}(i)
	}
	wg.Wait()

	gagal := 0
	for _, err := range errs {
		if err != nil {
			gagal++
		}
	}
	if gagal != 1 {
		t.Fatalf("harus tepat satu permintaan gagal, dapat %d (errs=%v)", gagal, errs)
	}

	rows, err := repo.ListByOrder(ctx, orderA)
	if err != nil {
		t.Fatalf("ListByOrder: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("harus 3 baris dokumen, dapat %d", len(rows))
	}
	if store.hapus != 0 {
		t.Errorf("permintaan yang gagal tidak boleh menghapus berkas milik baris yang sah: %d penghapusan", store.hapus)
	}
	for _, d := range rows {
		if _, err := store.Get(ctx, d.StorageKey); err != nil {
			t.Errorf("baris %s kehilangan berkasnya di storage: %v", d.Jenis, err)
		}
	}

	// Render ulang tidak menyembuhkan berkas yang hilang, jadi yang dijaga di
	// sini adalah keadaan akhirnya: setiap baris punya berkas DAN tautan unduh
	// masih menunjuk objek yang ada.
	docs, err := svc.Ensure(ctx, orderA)
	if err != nil {
		t.Fatalf("Ensure ulang: %v", err)
	}
	if len(docs) != 3 || store.jumlahPut() != 6 {
		t.Errorf("Ensure ulang harus memakai baris yang ada tanpa merender ulang (docs=%d put=%d)", len(docs), store.jumlahPut())
	}
	for _, d := range docs {
		dl, err := svc.DownloadURL(ctx, actorAdmin(), d.ID)
		if err != nil {
			t.Fatalf("unduh %s: %v", d.Jenis, err)
		}
		if !strings.Contains(dl.URL, d.StorageKey) {
			t.Errorf("tautan unduh %s tidak menunjuk objeknya: %q", d.Jenis, dl.URL)
		}
	}
}

// TestKegagalanSimpanMetadataMenyisakanObjekYatimTanpaMenghapus menegakkan
// aturan putaran 3 (temuan QA putaran 2): ketika permintaan render gagal dan
// tidak ada baris `documents` mana pun yang menunjuk objeknya, objek itu TETAP
// DIBIARKAN — bukan dibuang. Alasannya keselamatan, bukan kerapian storage:
// kunci deterministik dipakai bersama permintaan render lain, dan penghapusan
// yang bertumpu pada snapshot rujukan bisa menghabisi berkas baris pesaing
// yang sudah menulis objek tetapi belum commit (kehilangan permanen). Objek
// yatim direkonsiliasi F1-21.
func TestKegagalanSimpanMetadataMenyisakanObjekYatimTanpaMenghapus(t *testing.T) {
	ctx := context.Background()
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))
	repo.GagalUntuk = orderA
	store := newStoreHitung()
	svc := NewService(repo, store, &rendererPalsu{})

	if _, err := svc.Ensure(ctx, orderA); err == nil {
		t.Fatal("harus gagal saat metadata tidak bisa disimpan")
	}
	if repo.JumlahDokumen() != 0 {
		t.Fatalf("tidak boleh ada baris dokumen, dapat %d", repo.JumlahDokumen())
	}
	if store.hapus != 0 {
		t.Errorf("tidak boleh ada penghapusan objek pada jalur gagal, dapat %d", store.hapus)
	}
	for _, k := range []string{
		StorageKey(orderA, JenisInvoicePDF, 0),
		StorageKey(orderA, JenisSuratJalanXLSX, 0),
		StorageKey(orderA, JenisSuratJalanPDF, 0),
	} {
		if isi := isiObjek(t, store, k); len(isi) == 0 {
			t.Errorf("objek yatim %s harus dibiarkan (kandidat rekonsiliasi F1-21)", k)
		}
	}
}

// TestOtorisasiDaftarDanUnduh menegakkan isolasi cabang dan pembagian peran:
// akun cabang hanya invoice pesanannya sendiri; staf Pusat boleh semuanya.
func TestOtorisasiDaftarDanUnduh(t *testing.T) {
	ctx := context.Background()
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA)) // milik br-1
	o2 := fixtureOrder(orderB)
	o2.ID = orderB
	o2.BranchID = "br-2"
	o2.Nomor = "2026-10-0002"
	repo.SetOrder(o2)
	store := newStoreHitung()
	svc := NewService(repo, store, &rendererPalsu{})

	docsO1, err := svc.Ensure(ctx, orderA)
	if err != nil {
		t.Fatalf("Ensure o1: %v", err)
	}
	docsO2, err := svc.Ensure(ctx, orderB)
	if err != nil {
		t.Fatalf("Ensure o2: %v", err)
	}
	var invoiceO1, suratJalanO1, invoiceO2 Document
	for _, d := range docsO1 {
		switch d.Jenis {
		case JenisInvoicePDF:
			invoiceO1 = d
		case JenisSuratJalanXLSX:
			suratJalanO1 = d
		}
	}
	for _, d := range docsO2 {
		if d.Jenis == JenisInvoicePDF {
			invoiceO2 = d
		}
	}

	// Cabang br-1: daftar hanya invoice pesanannya sendiri.
	list, err := svc.List(ctx, actorCabang("br-1"), orderA)
	if err != nil {
		t.Fatalf("List cabang: %v", err)
	}
	if len(list) != 1 || list[0].Jenis != JenisInvoicePDF {
		t.Errorf("cabang hanya boleh melihat invoice: %+v", list)
	}
	// Pesanan cabang lain: seolah tidak ada.
	if _, err := svc.List(ctx, actorCabang("br-1"), orderB); !errors.Is(err, ErrNotFound) {
		t.Errorf("pesanan cabang lain harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.List(ctx, actorCabang("br-1"), "bukan-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("id bukan uuid harus ErrNotFound, dapat %v", err)
	}

	// Unduhan: invoice sendiri boleh, surat jalan tidak, dokumen cabang lain tidak.
	dl, err := svc.DownloadURL(ctx, actorCabang("br-1"), invoiceO1.ID)
	if err != nil {
		t.Fatalf("unduh invoice sendiri: %v", err)
	}
	if dl.ExpiresIn != int(DefaultPresignTTL.Seconds()) || dl.URL == "" {
		t.Errorf("hasil unduh tidak sesuai: %+v", dl)
	}
	if dl.NamaFile != "Invoice-2026-10-0001.pdf" || dl.MimeType != MimePDF {
		t.Errorf("nama/mime unduhan tidak sesuai: %+v", dl)
	}
	if _, err := svc.DownloadURL(ctx, actorCabang("br-1"), suratJalanO1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("surat jalan bukan dokumen cabang, harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.DownloadURL(ctx, actorCabang("br-1"), invoiceO2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("dokumen cabang lain harus ErrNotFound, dapat %v", err)
	}

	// Staf Pusat: seluruh dokumen semua cabang.
	all, err := svc.List(ctx, actorAdmin(), orderA)
	if err != nil {
		t.Fatalf("List admin: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("admin harus melihat 3 dokumen, dapat %d", len(all))
	}
	if _, err := svc.DownloadURL(ctx, actorAdmin(), suratJalanO1.ID); err != nil {
		t.Errorf("admin harus boleh unduh surat jalan: %v", err)
	}

	// Aktor tanpa hak & dokumen tak dikenal.
	if _, err := svc.List(ctx, auth.Actor{}, orderA); !errors.Is(err, ErrForbidden) {
		t.Errorf("anonim harus ErrForbidden, dapat %v", err)
	}
	if _, err := svc.DownloadURL(ctx, actorAdmin(), "bukan-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("id bukan uuid harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.DownloadURL(ctx, actorAdmin(), "11111111-2222-3333-4444-555555555555"); !errors.Is(err, ErrNotFound) {
		t.Errorf("dokumen tak dikenal harus ErrNotFound, dapat %v", err)
	}
}

// TestGenerateButuhKapabilitasAdmin menjaga pintu masuk HTTP: hanya staf Pusat
// dengan CapAdminOrders yang boleh memicu render.
func TestGenerateButuhKapabilitasAdmin(t *testing.T) {
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))
	svc := NewService(repo, newStoreHitung(), &rendererPalsu{})

	if _, err := svc.Generate(context.Background(), actorCabang("br-1"), orderA); !errors.Is(err, ErrForbidden) {
		t.Errorf("cabang harus ErrForbidden, dapat %v", err)
	}
	manager := auth.Actor{UserID: "u-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Generate(context.Background(), manager, orderA); err != nil {
		t.Errorf("manager ber-CapAdminOrders harus boleh generate: %v", err)
	}
	if repo.JumlahDokumen() != 3 {
		t.Errorf("manager harus menghasilkan 3 dokumen, dapat %d", repo.JumlahDokumen())
	}
}

// TestHasilRenderKosongDitolak memastikan renderer yang mengembalikan PDF
// kosong tidak menghasilkan dokumen cacat.
func TestHasilRenderKosongDitolak(t *testing.T) {
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))
	store := newStoreHitung()
	svc := NewService(repo, store, &rendererPalsu{kosong: true})

	if _, err := svc.Ensure(context.Background(), orderA); err == nil {
		t.Fatal("render kosong harus ditolak")
	}
	if repo.JumlahDokumen() != 0 || store.jumlahPut() != 0 {
		t.Error("render kosong tidak boleh menyimpan apa pun")
	}
}

// TestSnapshotKurangDitolak menjaga data rusak (mis. nomor kosong) tidak
// pernah menjadi dokumen bernomor kosong.
func TestSnapshotKurangDitolak(t *testing.T) {
	repo := NewMemory()
	o := fixtureOrder(orderA)
	o.Nomor = ""
	repo.SetOrder(o)
	svc := NewService(repo, newStoreHitung(), &rendererPalsu{})

	if _, err := svc.Ensure(context.Background(), orderA); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("nomor kosong harus ErrInvalidData, dapat %v", err)
	}
	if _, err := svc.Ensure(context.Background(), "tidak-ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pesanan tidak ada harus ErrNotFound, dapat %v", err)
	}
}

// storePemenang membungkus object storage BERSAMA dan menandai (menutup
// `siap`) saat pemiliknya selesai menulis seluruh objeknya — dipakai supaya
// permintaan pesaing yang gagal benar-benar menjalankan pembersihannya SETELAH
// objek pemenang tertulis, tetapi SEBELUM pemenang commit.
type storePemenang struct {
	*storeHitung
	mu     sync.Mutex
	n      int
	butuh  int
	siap   chan struct{}
	sekali sync.Once
}

func newStorePemenang(butuh int) *storePemenang {
	return &storePemenang{storeHitung: newStoreHitung(), butuh: butuh, siap: make(chan struct{})}
}

func (s *storePemenang) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := s.storeHitung.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	s.mu.Lock()
	s.n++
	penuh := s.n >= s.butuh
	s.mu.Unlock()
	if penuh {
		s.sekali.Do(func() { close(s.siap) })
	}
	return nil
}

// storeGagalSatu meniru permintaan yang memakai object storage yang sama tetapi
// SELALU gagal menulis satu kunci tertentu (mis. Surat Jalan XLSX). Sebelum
// melaporkan kegagalan ia boleh menunggu `tunggu` supaya urutan kejadiannya
// deterministik.
type storeGagalSatu struct {
	*storeHitung
	gagalKey string
	tunggu   <-chan struct{}
}

func (s *storeGagalSatu) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if key == s.gagalKey {
		if s.tunggu != nil {
			select {
			case <-s.tunggu:
			case <-time.After(30 * time.Second):
				return errors.New("uji buntu: pemenang tidak menyelesaikan penulisan objeknya")
			}
		}
		return errors.New("simulasi: storage mati saat menulis dokumen kedua")
	}
	return s.storeHitung.Put(ctx, key, body, size, contentType)
}

// repoTahanSimpan menahan SaveAll sampai gerbangnya dibuka: meniru permintaan
// pemenang yang sudah menulis seluruh objeknya tetapi BELUM commit ketika
// permintaan pesaing menjalankan pembersihannya — jendela yang dilaporkan QA
// putaran 2 (pesaing commit setelah pembacaan rujukan).
type repoTahanSimpan struct {
	*Memory
	siap <-chan struct{}
}

func (r *repoTahanSimpan) SaveAll(ctx context.Context, req SaveRequest) ([]Document, error) {
	select {
	case <-r.siap:
	case <-time.After(30 * time.Second):
		return nil, errors.New("uji buntu: gerbang commit tidak pernah dibuka")
	}
	return r.Memory.SaveAll(ctx, req)
}

// TestKegagalanPutTidakMencabutBerkasPesaingBelumCommit adalah uji regresi
// putaran 3 untuk urutan yang terbukti MERUSAK pada 8f91c3a: permintaan
// pesaing sudah menulis seluruh objeknya tetapi belum commit, sementara
// permintaan ini gagal di Put dokumen kedua lalu menjalankan pembersihannya.
// Guard "hapus hanya bila tidak ada baris yang merujuk" membaca snapshot yang
// memang masih kosong (baris pemenang belum commit), lalu menghapus kunci
// deterministik yang dipakai bersama — begitu pemenang commit, barisnya ada
// tetapi berkasnya hilang PERMANEN (render ulang menganggap barisnya sudah
// ada). Karena aturan putaran 3 tidak pernah menghapus objek di jalur render,
// urutan ini aman untuk seluruh penjadwalan.
func TestKegagalanPutTidakMencabutBerkasPesaingBelumCommit(t *testing.T) {
	ctx := context.Background()
	repo := NewMemory()
	repo.SetOrder(fixtureOrder(orderA))

	store := newStorePemenang(3)
	gagal := &storeGagalSatu{
		storeHitung: store.storeHitung,
		gagalKey:    StorageKey(orderA, JenisSuratJalanXLSX, 0),
		tunggu:      store.siap, // tunggu sampai pemenang menulis 3 objeknya
	}
	gerbangCommit := make(chan struct{})
	repoTahan := &repoTahanSimpan{Memory: repo, siap: gerbangCommit}

	svcPemenang := NewService(repoTahan, store, &rendererPalsu{})
	svcKalah := NewService(repoTahan, gagal, &rendererPalsu{})

	var wg sync.WaitGroup
	var errPemenang, errKalah error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errPemenang = svcPemenang.Ensure(ctx, orderA)
	}()
	go func() {
		defer wg.Done()
		_, errKalah = svcKalah.Ensure(ctx, orderA)
		// Pemenang baru boleh commit setelah permintaan ini selesai — sehingga
		// jendela "objek sudah tertulis, baris belum commit" pasti dilewati.
		close(gerbangCommit)
	}()
	wg.Wait()

	if errKalah == nil {
		t.Fatal("permintaan yang Put-nya gagal harus mengembalikan error")
	}
	if errPemenang != nil {
		t.Fatalf("permintaan pemenang harus sukses, dapat %v", errPemenang)
	}
	if store.hapus != 0 {
		t.Errorf("permintaan yang gagal tidak boleh menghapus objek: %d penghapusan", store.hapus)
	}
	rows, err := repo.ListByOrder(ctx, orderA)
	if err != nil {
		t.Fatalf("ListByOrder: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("harus 3 baris dokumen, dapat %d", len(rows))
	}
	for _, d := range rows {
		if _, err := store.Get(ctx, d.StorageKey); err != nil {
			t.Errorf("baris %s kehilangan berkasnya di storage: %v", d.Jenis, err)
			continue
		}
		dl, err := svcPemenang.DownloadURL(ctx, actorAdmin(), d.ID)
		if err != nil {
			t.Fatalf("unduh %s: %v", d.Jenis, err)
		}
		if !strings.Contains(dl.URL, d.StorageKey) {
			t.Errorf("tautan unduh %s tidak menunjuk objeknya: %q", d.Jenis, dl.URL)
		}
	}
}

// TestKeterjadianAlamiTidakAdaBerkasHilang mengukur keterjadian ALAMI (tanpa
// gerbang urutan apa pun): 20 pesanan, masing-masing dirender oleh DUA
// permintaan paralel yang mulai bersamaan sementara satu di antaranya selalu
// gagal di Put dokumen kedua — meniru dua klik "Buat dokumen" di mana satu
// klik gagal menyimpan. Probe QA putaran 2 mengukur 2/10 percobaan berakhir
// dengan berkas hilang permanen pada 8f91c3a; dengan aturan putaran 3 (tak ada
// penghapusan di jalur render) harus NOL dari seluruh percobaan.
func TestKeterjadianAlamiTidakAdaBerkasHilang(t *testing.T) {
	ctx := context.Background()
	const percobaan = 20
	hilang := 0
	for i := 0; i < percobaan; i++ {
		id := fmt.Sprintf("0f8fad5b-d9cb-469f-a165-708677289%03d", i)
		repo := NewMemory()
		repo.SetOrder(fixtureOrder(id))

		store := newStoreHitung()
		gagal := &storeGagalSatu{
			storeHitung: store,
			gagalKey:    StorageKey(id, JenisSuratJalanXLSX, 0),
		}
		svcPemenang := NewService(repo, store, &rendererPalsu{})
		svcKalah := NewService(repo, gagal, &rendererPalsu{})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = svcPemenang.Ensure(ctx, id)
		}()
		go func() {
			defer wg.Done()
			_, _ = svcKalah.Ensure(ctx, id)
		}()
		wg.Wait()

		rows, err := repo.ListByOrder(ctx, id)
		if err != nil {
			t.Fatalf("ListByOrder %s: %v", id, err)
		}
		if len(rows) != 3 {
			t.Fatalf("pesanan %s harus punya 3 baris dokumen, dapat %d", id, len(rows))
		}
		for _, d := range rows {
			if _, err := store.Get(ctx, d.StorageKey); err != nil {
				hilang++
				t.Logf("percobaan %d: baris %s kehilangan berkasnya: %v", i, d.Jenis, err)
				break
			}
		}
	}
	if hilang != 0 {
		t.Errorf("ada %d/%d percobaan yang kehilangan berkas meski barisnya ada (harus 0)", hilang, percobaan)
	}
}
