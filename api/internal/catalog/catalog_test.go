package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

func adminActor() auth.Actor {
	return auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi", Email: "budi@example.test"}
}

func branchActor(branchID string) auth.Actor {
	return auth.Actor{UserID: "usr-" + branchID, Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: branchID, Nama: "Cabang " + branchID}
}

func svc() (*Service, *Memory) {
	m := NewMemory()
	return NewService(m), m
}

func ptr[T any](v T) *T { return &v }

// TestKatalogCabangHanyaProdukBerharga memastikan produk tanpa baris harga
// TIDAK muncul di portal cabang, dan harga default bukan cadangan (PRD
// CP-KAT-01/02).
func TestKatalogCabangHanyaProdukBerharga(t *testing.T) {
	s, m := svc()
	m.PutProduct(Product{ID: "p1", Nama: "Bio Magnetik", HargaDefault: 350000, IsActive: true})
	m.PutProduct(Product{ID: "p2", Nama: "Sandwich Panel", HargaDefault: 500000, IsActive: true})
	// p2 tidak punya baris harga untuk cabang mana pun.
	m.PutPrice("br-a", "p1", 100000)
	m.PutPrice("br-b", "p1", 200000)

	ctx := context.Background()
	rowsA, err := s.Products(ctx, branchActor("br-a"))
	if err != nil {
		t.Fatalf("katalog cabang A: %v", err)
	}
	listA := rowsA.([]BranchProduct)
	if len(listA) != 1 || listA[0].ID != "p1" || listA[0].Harga != 100000 {
		t.Fatalf("cabang A harus melihat hanya p1 dengan harganya, dapat %+v", listA)
	}
	rowsB, _ := s.Products(ctx, branchActor("br-b"))
	if got := rowsB.([]BranchProduct)[0].Harga; got != 200000 {
		t.Fatalf("cabang B harus melihat harga cabangnya sendiri, dapat %d", got)
	}
	// Cabang C tidak punya harga sama sekali: katalognya kosong walau ada
	// produk aktif dengan harga default.
	rowsC, _ := s.Products(ctx, branchActor("br-c"))
	if len(rowsC.([]BranchProduct)) != 0 {
		t.Fatalf("cabang tanpa harga tidak boleh melihat produk apa pun, dapat %+v", rowsC)
	}
}

// TestKatalogCabangMengabaikanProdukNonaktif memastikan produk nonaktif hilang
// dari portal cabang walau harganya ada.
func TestKatalogCabangMengabaikanProdukNonaktif(t *testing.T) {
	s, m := svc()
	m.PutProduct(Product{ID: "p1", Nama: "Aktif", IsActive: true})
	m.PutProduct(Product{ID: "p2", Nama: "Nonaktif", IsActive: false})
	m.PutPrice("br-a", "p1", 1000)
	m.PutPrice("br-a", "p2", 2000)

	rows, _ := s.Products(context.Background(), branchActor("br-a"))
	list := rows.([]BranchProduct)
	if len(list) != 1 || list[0].ID != "p1" {
		t.Fatalf("produk nonaktif harus hilang, dapat %+v", list)
	}
}

// TestCRUDProduk mencatat audit dan menegakkan kode unik.
func TestCRUDProduk(t *testing.T) {
	s, m := svc()
	ctx := context.Background()
	actor := adminActor()

	p, err := s.CreateProduct(ctx, actor, NewProduct{Nama: "Bio Magnetik", Kode: "P-01", Jenis: JenisReguler, Satuan: "pcs", HargaDefault: 350000, Urutan: 1})
	if err != nil {
		t.Fatalf("buat produk: %v", err)
	}
	if !p.IsActive {
		t.Error("produk baru harus aktif")
	}
	if _, err := s.CreateProduct(ctx, actor, NewProduct{Nama: "Lain", Kode: "P-01"}); !errors.Is(err, ErrKodeTaken) {
		t.Fatalf("kode duplikat harus ErrKodeTaken, dapat %v", err)
	}

	got, err := s.GetProduct(ctx, actor, p.ID)
	if err != nil || got.Nama != "Bio Magnetik" {
		t.Fatalf("baca produk: %v %+v", err, got)
	}

	up, err := s.UpdateProduct(ctx, actor, p.ID, ProductPatch{Nama: ptr("Bio Magnetik 2"), IsActive: ptr(false)})
	if err != nil {
		t.Fatalf("ubah produk: %v", err)
	}
	if up.Nama != "Bio Magnetik 2" || up.IsActive {
		t.Fatalf("hasil ubah tidak sesuai: %+v", up)
	}
	// Kode tidak dikirim → tidak berubah.
	if up.Kode != "P-01" {
		t.Fatalf("kode tidak boleh berubah bila tidak dikirim, dapat %q", up.Kode)
	}

	// Produk yang sudah dipakai pesanan tidak boleh dihapus.
	m.PutUsed(p.ID)
	if err := s.DeleteProduct(ctx, actor, p.ID); !errors.Is(err, ErrProductUsed) {
		t.Fatalf("produk terpakai harus ErrProductUsed, dapat %v", err)
	}

	recs := m.AuditEntries()
	if len(recs) != 2 {
		t.Fatalf("harus ada 2 entri audit (create+update), dapat %d", len(recs))
	}
	for _, r := range recs {
		if r.Entity != audit.EntityProduct || r.ActorID != actor.UserID {
			t.Fatalf("entri audit tidak sesuai: %+v", r)
		}
	}
	if recs[0].Action != audit.ActionCreate || recs[1].Action != audit.ActionUpdate {
		t.Fatalf("aksi audit tidak sesuai: %s, %s", recs[0].Action, recs[1].Action)
	}
}

// TestHapusProdukMencatatAudit memastikan produk tanpa pesanan bisa dihapus.
func TestHapusProdukMencatatAudit(t *testing.T) {
	s, m := svc()
	m.PutProduct(Product{ID: "p1", Nama: "Sementara"})
	if err := s.DeleteProduct(context.Background(), adminActor(), "p1"); err != nil {
		t.Fatalf("hapus produk: %v", err)
	}
	if _, err := s.GetProduct(context.Background(), adminActor(), "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("produk harus hilang, dapat %v", err)
	}
	recs := m.AuditEntries()
	if len(recs) != 1 || recs[0].Action != audit.ActionDelete {
		t.Fatalf("harus ada satu entri audit delete, dapat %+v", recs)
	}
}

// TestSertifikatSelaluNol menegakkan harga 0 untuk produk Sertifikat, baik
// pada harga default maupun pada matriks harga (PRD CP-SER-01, BR-06).
func TestSertifikatSelaluNol(t *testing.T) {
	s, _ := svc()
	ctx := context.Background()
	actor := adminActor()

	if _, err := s.CreateProduct(ctx, actor, NewProduct{Nama: "Sertifikat", Jenis: JenisSertifikat, HargaDefault: 5000}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("sertifikat berharga default > 0 harus ditolak, dapat %v", err)
	}
	ser, err := s.CreateProduct(ctx, actor, NewProduct{Nama: "Sertifikat", Jenis: JenisSertifikat, HargaDefault: 0})
	if err != nil {
		t.Fatalf("sertifikat harga 0 harus diterima: %v", err)
	}
	if _, err := s.SetPrices(ctx, actor, []PriceInput{{BranchID: "br-a", ProductID: ser.ID, Harga: ptr(int64(1000))}}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("harga cabang sertifikat selain 0 harus ditolak, dapat %v", err)
	}
	res, err := s.SetPrices(ctx, actor, []PriceInput{{BranchID: "br-a", ProductID: ser.ID, Harga: ptr(int64(0))}})
	if err != nil {
		t.Fatalf("harga cabang sertifikat 0 harus diterima: %v", err)
	}
	if res.Diubah != 1 {
		t.Fatalf("sel sertifikat harus tercatat diubah, dapat %+v", res)
	}
}

// TestUbahProdukJadiSertifikatMenolakHargaCabang menegakkan bahwa produk yang
// matriksnya masih berisi harga bukan nol tidak bisa dijadikan Sertifikat.
func TestUbahProdukJadiSertifikatMenolakHargaCabang(t *testing.T) {
	s, m := svc()
	ctx := context.Background()
	actor := adminActor()
	m.PutProduct(Product{ID: "p1", Nama: "Reguler", Jenis: JenisReguler, Satuan: "pcs"})
	m.PutPrice("br-a", "p1", 100000)

	if _, err := s.UpdateProduct(ctx, actor, "p1", ProductPatch{Jenis: ptr(JenisSertifikat), HargaDefault: ptr(int64(0))}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ubah jadi sertifikat dengan harga cabang > 0 harus ditolak, dapat %v", err)
	}
	m.PutPrice("br-a", "p1", 0)
	if _, err := s.UpdateProduct(ctx, actor, "p1", ProductPatch{Jenis: ptr(JenisSertifikat), HargaDefault: ptr(int64(0))}); err != nil {
		t.Fatalf("setelah semua sel 0 harus bisa dijadikan sertifikat: %v", err)
	}
}

// TestBatchEditMatriksHarga menguji batch edit: tambah, ubah, kosongkan, dan
// pengabaian sel yang nilainya sama (PRD AP-KAT-02/03/06).
func TestBatchEditMatriksHarga(t *testing.T) {
	s, m := svc()
	ctx := context.Background()
	actor := adminActor()
	m.PutProduct(Product{ID: "p1", Nama: "A", IsActive: true})
	m.PutProduct(Product{ID: "p2", Nama: "B", IsActive: true})
	m.PutPrice("br-a", "p1", 100000)

	res, err := s.SetPrices(ctx, actor, []PriceInput{
		{BranchID: "br-a", ProductID: "p1", Harga: ptr(int64(100000))}, // sama → diabaikan
		{BranchID: "br-a", ProductID: "p2", Harga: ptr(int64(250000))}, // baru
		{BranchID: "br-b", ProductID: "p1", Harga: ptr(int64(120000))}, // baru
	})
	if err != nil {
		t.Fatalf("batch edit: %v", err)
	}
	if res.Diubah != 2 || res.Dihapus != 0 || res.Diabaikan != 1 {
		t.Fatalf("ringkasan tidak sesuai: %+v", res)
	}
	if v, ok := m.PriceOf("br-b", "p1"); !ok || v != 120000 {
		t.Fatalf("sel baru tidak tersimpan: %d %v", v, ok)
	}

	// Sel dikosongkan → baris terhapus (produk tidak dijual lagi).
	res, err = s.SetPrices(ctx, actor, []PriceInput{{BranchID: "br-b", ProductID: "p1"}})
	if err != nil {
		t.Fatalf("kosongkan sel: %v", err)
	}
	if res.Dihapus != 1 {
		t.Fatalf("harus satu sel terhapus, dapat %+v", res)
	}
	if _, ok := m.PriceOf("br-b", "p1"); ok {
		t.Fatal("sel yang dikosongkan harus hilang dari matriks")
	}
	// Mengosongkan sel yang memang kosong = diabaikan, bukan audit palsu.
	res, err = s.SetPrices(ctx, actor, []PriceInput{{BranchID: "br-b", ProductID: "p1"}})
	if err != nil || res.Dihapus != 0 || res.Diabaikan != 1 {
		t.Fatalf("sel kosong yang dikosongkan lagi harus diabaikan: %+v %v", res, err)
	}

	// Audit: 2 create + 1 update (ubah sel br-b? tidak) — periksa jumlah entri
	// untuk entity branch_price.
	priceRecs := 0
	for _, r := range m.AuditEntries() {
		if r.Entity == audit.EntityBranchPrice {
			priceRecs++
		}
	}
	if priceRecs != 3 {
		t.Fatalf("harus 3 entri audit harga (2 sel baru + 1 hapus), dapat %d", priceRecs)
	}
}

// TestBatchEditMenolakMasukanTidakValid menegakkan validasi batch.
func TestBatchEditMenolakMasukanTidakValid(t *testing.T) {
	s, m := svc()
	ctx := context.Background()
	actor := adminActor()
	m.PutProduct(Product{ID: "p1", Nama: "A"})

	cases := []struct {
		nama  string
		items []PriceInput
	}{
		{"kosong", nil},
		{"harga negatif", []PriceInput{{BranchID: "br-a", ProductID: "p1", Harga: ptr(int64(-1))}}},
		{"produk tidak dikenal", []PriceInput{{BranchID: "br-a", ProductID: "p9", Harga: ptr(int64(1))}}},
		{"product_id kosong", []PriceInput{{BranchID: "br-a", Harga: ptr(int64(1))}}},
	}
	for _, c := range cases {
		if _, err := s.SetPrices(ctx, actor, c.items); !errors.Is(err, ErrInvalidData) {
			t.Errorf("%s: harus ErrInvalidData, dapat %v", c.nama, err)
		}
	}
	if len(m.AuditEntries()) != 0 {
		t.Error("permintaan tidak valid tidak boleh menulis audit")
	}
}

// TestSalinHargaMenggantikanMatriksTujuan menguji aksi salin harga
// (PRD AP-KAT-04) beserta auditnya.
func TestSalinHargaMenggantikanMatriksTujuan(t *testing.T) {
	s, m := svc()
	ctx := context.Background()
	actor := adminActor()
	m.PutProduct(Product{ID: "p1", Nama: "A"})
	m.PutProduct(Product{ID: "p2", Nama: "B"})
	m.PutPrice("br-a", "p1", 100000)
	m.PutPrice("br-a", "p2", 200000)
	m.PutPrice("br-b", "p1", 150000) // akan tergantikan
	m.PutPrice("br-b", "p3", 999)    // produk yang tidak ada di sumber → terhapus

	res, err := s.CopyPrices(ctx, actor, CopyInput{FromBranchID: "br-a", ToBranchIDs: []string{"br-b", "br-c"}})
	if err != nil {
		t.Fatalf("salin harga: %v", err)
	}
	if res.Jumlah != 2 || len(res.Salinan) != 2 {
		t.Fatalf("ringkasan salin tidak sesuai: %+v", res)
	}
	for _, b := range []string{"br-b", "br-c"} {
		if v, ok := m.PriceOf(b, "p1"); !ok || v != 100000 {
			t.Errorf("cabang %s harus ikut harga sumber untuk p1, dapat %d %v", b, v, ok)
		}
		if v, ok := m.PriceOf(b, "p2"); !ok || v != 200000 {
			t.Errorf("cabang %s harus ikut harga sumber untuk p2, dapat %d %v", b, v, ok)
		}
	}
	if _, ok := m.PriceOf("br-b", "p3"); ok {
		t.Error("sel yang tidak ada di cabang sumber harus terhapus di cabang tujuan")
	}

	salinRecs := 0
	for _, r := range m.AuditEntries() {
		if r.Action == audit.ActionCopy {
			salinRecs++
		}
	}
	if salinRecs != 2 {
		t.Fatalf("harus satu entri audit copy per cabang tujuan, dapat %d", salinRecs)
	}

	// Cabang sumber tanpa harga tidak boleh dijadikan sumber (akan
	// mengosongkan seluruh cabang tujuan tanpa peringatan).
	if _, err := s.CopyPrices(ctx, actor, CopyInput{FromBranchID: "br-kosong", ToBranchIDs: []string{"br-b"}}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("sumber kosong harus ditolak, dapat %v", err)
	}
	// Tidak ada tujuan yang berbeda dari sumber.
	if _, err := s.CopyPrices(ctx, actor, CopyInput{FromBranchID: "br-a", ToBranchIDs: []string{"br-a"}}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("tujuan sama dengan sumber harus ditolak, dapat %v", err)
	}
}

// TestFilterDaftarProdukAdmin menguji filter q/jenis/aktif.
func TestFilterDaftarProdukAdmin(t *testing.T) {
	s, m := svc()
	ctx := context.Background()
	m.PutProduct(Product{ID: "p1", Kode: "P-01", Nama: "Bio Magnetik", Jenis: JenisReguler, IsActive: true})
	m.PutProduct(Product{ID: "p2", Kode: "P-02", Nama: "Sertifikat", Jenis: JenisSertifikat, IsActive: true})
	m.PutProduct(Product{ID: "p3", Kode: "P-03", Nama: "Lama", Jenis: JenisReguler, IsActive: false})

	semua, err := s.ListProducts(ctx, adminActor(), ProductFilter{})
	if err != nil || len(semua) != 3 {
		t.Fatalf("tanpa filter harus 3 produk (termasuk nonaktif), dapat %d %v", len(semua), err)
	}
	aktif, _ := s.ListProducts(ctx, adminActor(), ProductFilter{Active: ptr(true)})
	if len(aktif) != 2 {
		t.Fatalf("filter aktif harus 2 produk, dapat %d", len(aktif))
	}
	ser, _ := s.ListProducts(ctx, adminActor(), ProductFilter{Jenis: "sertifikat"})
	if len(ser) != 1 || ser[0].ID != "p2" {
		t.Fatalf("filter jenis salah: %+v", ser)
	}
	cari, _ := s.ListProducts(ctx, adminActor(), ProductFilter{Q: "bio"})
	if len(cari) != 1 || cari[0].ID != "p1" {
		t.Fatalf("filter q salah: %+v", cari)
	}
	if _, err := s.ListProducts(ctx, adminActor(), ProductFilter{Jenis: "mungkin"}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("jenis tidak dikenal harus ditolak, dapat %v", err)
	}
}

// TestOtorisasiKatalog memastikan akun cabang tidak bisa menyentuh permukaan
// admin katalog (PRD §2.1).
func TestOtorisasiKatalog(t *testing.T) {
	s, m := svc()
	ctx := context.Background()
	m.PutProduct(Product{ID: "p1", Nama: "A", IsActive: true})
	cabang := branchActor("br-a")

	if _, err := s.ListProducts(ctx, cabang, ProductFilter{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("cabang tidak boleh daftar produk admin: %v", err)
	}
	if _, err := s.GetProduct(ctx, cabang, "p1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("cabang tidak boleh baca produk admin: %v", err)
	}
	if _, err := s.CreateProduct(ctx, cabang, NewProduct{Nama: "X"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("cabang tidak boleh buat produk: %v", err)
	}
	if _, err := s.SetPrices(ctx, cabang, []PriceInput{{BranchID: "br-a", ProductID: "p1", Harga: ptr(int64(1))}}); !errors.Is(err, ErrForbidden) {
		t.Errorf("cabang tidak boleh ubah harga: %v", err)
	}
	if _, err := s.CopyPrices(ctx, cabang, CopyInput{FromBranchID: "br-a", ToBranchIDs: []string{"br-b"}}); !errors.Is(err, ErrForbidden) {
		t.Errorf("cabang tidak boleh salin harga: %v", err)
	}
	// Anonymous juga ditolak.
	if _, err := s.Products(ctx, auth.Anonymous()); !errors.Is(err, ErrForbidden) {
		t.Errorf("anonim harus ditolak: %v", err)
	}
}
