package payment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/settings"
)

// Konfigurasi setelan palsu untuk batas unggah.
type fakeConfig struct{ nilai map[string]int64 }

func (c fakeConfig) Int(_ context.Context, key string) (int64, error) {
	v, ok := c.nilai[key]
	if !ok {
		return 0, settings.ErrNotFound
	}
	return v, nil
}

func configBatas(maxFile, maxMB int64) fakeConfig {
	return fakeConfig{nilai: map[string]int64{settings.MaxFile: maxFile, settings.MaxMB: maxMB}}
}

// Berkas contoh dengan magic bytes yang benar.
func berkasJPEG(isi string) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(isi)...)
}

func berkasPNG(isi string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte(isi)...)
}

func berkasPDF(isi string) []byte {
	return append([]byte("%PDF-1.7\n"), []byte(isi)...)
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Alamat pesanan uji. Repository & storage in-memory sudah cukup untuk menguji
// seluruh aturan domain.
const (
	orderA = "0193c0de-0000-7000-8000-00000000000a"
	orderB = "0193c0de-0000-7000-8000-00000000000b"
)

// setup menyiapkan service dengan repository & storage in-memory.
func setup(t *testing.T, maxFile, maxMB int64, orders ...OrderRef) (*Service, *Memory, *storage.Memory) {
	t.Helper()
	repo := NewMemory()
	src := NewMemoryOrderSource()
	for _, o := range orders {
		src.Put(o)
	}
	store := storage.NewMemory("/files")
	svc := NewService(repo, src, store, configBatas(maxFile, maxMB))
	return svc, repo, store
}

func branchActor(id, branchID string) auth.Actor {
	return auth.Actor{UserID: id, Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: branchID, Nama: "Grahita Bandung"}
}

func adminActor(id, nama string) auth.Actor {
	return auth.Actor{UserID: id, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: nama}
}

func input(nama string, isi []byte) FileInput {
	return FileInput{NamaFile: nama, Content: bytes.NewReader(isi), UkuranHint: int64(len(isi))}
}

func TestKenaliMagicBytesDanEkstensiPalsu(t *testing.T) {
	svc, _, _ := setup(t, 5, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	actor := branchActor("u1", "br-a")

	kasus := []struct {
		nama      string
		isi       []byte
		mauErr    error
		mauMime   string
		mauNama   string
		judulKasu string
	}{
		{"bukti.jpg", berkasJPEG("foto-1"), nil, MimeJPEG, "bukti.jpg", "JPEG asli"},
		{"bukti.JPEG", berkasJPEG("foto-2"), nil, MimeJPEG, "bukti.JPEG", "ekstensi huruf besar"},
		{"bukti.png", berkasPNG("gambar"), nil, MimePNG, "bukti.png", "PNG asli"},
		{"bukti.pdf", berkasPDF("transfer"), nil, MimePDF, "bukti.pdf", "PDF asli"},
		{"bukti.jpg", berkasPDF("transfer-palsu"), ErrFakeExt, "", "", "PDF berkedok .jpg"},
		{"bukti.pdf", berkasJPEG("foto-palsu"), ErrFakeExt, "", "", "JPEG berkedok .pdf"},
		{"bukti.png", berkasJPEG("foto-palsu-2"), ErrFakeExt, "", "", "JPEG berkedok .png"},
		{"bukti.jpg", []byte("#!/bin/sh\nrm -rf /"), ErrUnsupported, "", "", "skrip berkedok .jpg"},
		{"bukti.txt", berkasJPEG("foto-3"), ErrFakeExt, "", "", "ekstensi tidak dikenal"},
		{"bukti", berkasJPEG("foto-4"), ErrFakeExt, "", "", "tanpa ekstensi"},
		{"bukti.jpg", nil, ErrUnsupported, "", "", "berkas kosong"},
	}
	for _, k := range kasus {
		t.Run(k.judulKasu, func(t *testing.T) {
			got, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input(k.nama, k.isi)})
			if k.mauErr != nil {
				if !errors.Is(err, k.mauErr) {
					t.Fatalf("mau error %v, dapat %v", k.mauErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("tidak diharapkan error: %v", err)
			}
			if got[0].MimeType != k.mauMime {
				t.Errorf("mime tersimpan %q, mau %q", got[0].MimeType, k.mauMime)
			}
			if got[0].NamaFile != k.mauNama {
				t.Errorf("nama tersimpan %q, mau %q", got[0].NamaFile, k.mauNama)
			}
		})
	}
}

func TestUploadMenyimpanKunciAcakDanSha256(t *testing.T) {
	svc, repo, store := setup(t, 5, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	isi := berkasPNG("gambar-bukti")
	got, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("../../etc/passwd.png", isi)})
	if err != nil {
		t.Fatalf("unggah: %v", err)
	}
	p := got[0]
	if p.Sha256 != shaHex(isi) {
		t.Errorf("sha256 tersimpan %q, mau %q", p.Sha256, shaHex(isi))
	}
	if p.UkuranByte != int64(len(isi)) {
		t.Errorf("ukuran tersimpan %d, mau %d", p.UkuranByte, len(isi))
	}
	// Nama berkas kiriman dibersihkan; kunci storage TIDAK memakai nama asli.
	if strings.Contains(p.StorageKey, "passwd") || strings.Contains(p.StorageKey, "..") {
		t.Errorf("kunci storage membocorkan nama kiriman: %q", p.StorageKey)
	}
	if !strings.HasPrefix(p.StorageKey, "proofs/"+orderA+"/") {
		t.Errorf("kunci storage tidak berawalan proofs/{order_id}/: %q", p.StorageKey)
	}
	if !strings.HasSuffix(p.StorageKey, ".png") {
		t.Errorf("ekstensi kunci storage harus dari isi berkas: %q", p.StorageKey)
	}
	if p.NamaFile != "passwd.png" {
		t.Errorf("nama berkas tersimpan %q, mau passwd.png (komponen direktori dibuang)", p.NamaFile)
	}
	if p.UploadedByKind != "branch" || p.UploadedByNama != "" {
		t.Errorf("penanda unggahan cabang salah: %+v", p)
	}
	// Isi benar-benar tersimpan dan bisa dibaca kembali.
	rc, err := store.Get(context.Background(), p.StorageKey)
	if err != nil {
		t.Fatalf("baca kembali objek: %v", err)
	}
	defer rc.Close()
	tersimpan, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("baca objek: %v", err)
	}
	if !bytes.Equal(tersimpan, isi) {
		t.Error("isi objek tidak sama dengan yang diunggah")
	}
	// Bukti tersimpan di repository.
	list, err := repo.ListByOrder(context.Background(), orderA)
	if err != nil || len(list) != 1 {
		t.Fatalf("daftar bukti = %d (%v), mau 1", len(list), err)
	}
}

func TestUploadMenolakDuplikatDanBatasJumlah(t *testing.T) {
	svc, _, _ := setup(t, 2, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	actor := branchActor("u1", "br-a")

	isi := berkasJPEG("sama")
	if _, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input("a.jpg", isi)}); err != nil {
		t.Fatalf("unggah pertama: %v", err)
	}
	// Isi identik (nama berkas beda) tetap duplikat: yang diperiksa sha256.
	_, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input("b.jpg", isi)})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("unggah isi sama harus ErrDuplicate, dapat %v", err)
	}
	// Duplikat di dalam SATU permintaan juga ditolak sebelum apa pun tersimpan
	// (batas jumlah dilepas supaya yang diuji benar-benar pemeriksaan sha256).
	svcLonggar, repoLonggar, _ := setup(t, 5, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	_, err = svcLonggar.Upload(context.Background(), actor, orderA,
		[]FileInput{input("c.jpg", berkasJPEG("ccc")), input("d.jpg", berkasJPEG("ccc"))})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplikat dalam satu permintaan harus ErrDuplicate, dapat %v", err)
	}
	if sisa, _ := repoLonggar.ListByOrder(context.Background(), orderA); len(sisa) != 0 {
		t.Errorf("permintaan yang gagal tidak boleh menyisakan bukti, dapat %d", len(sisa))
	}
	// Batas max_file=2: tersimpan 1, minta 2 lagi → ErrTooMany.
	_, err = svc.Upload(context.Background(), actor, orderA,
		[]FileInput{input("e.jpg", berkasJPEG("eee")), input("f.jpg", berkasJPEG("fff"))})
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("melebihi max_file harus ErrTooMany, dapat %v", err)
	}
	// Tepat sampai batas tetap boleh.
	if _, err := svc.Upload(context.Background(), actor, orderA,
		[]FileInput{input("g.jpg", berkasJPEG("ggg"))}); err != nil {
		t.Fatalf("unggah sampai batas: %v", err)
	}
	_, err = svc.Upload(context.Background(), actor, orderA, []FileInput{input("h.jpg", berkasJPEG("hhh"))})
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("unggah setelah penuh harus ErrTooMany, dapat %v", err)
	}
}

func TestUploadMenolakUkuranMelebihiMaxMB(t *testing.T) {
	svc, _, _ := setup(t, 5, 1, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	actor := branchActor("u1", "br-a")

	// 1 MB + 1 byte → ditolak; tepat 1 MB → diterima.
	besar := berkasJPEG(strings.Repeat("x", 1024*1024))
	if _, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input("besar.jpg", besar)}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("berkas lebih dari max_mb harus ErrTooLarge, dapat %v", err)
	}
	tepat := berkasJPEG(strings.Repeat("y", 1024*1024-len(berkasJPEG(""))))
	if len(tepat) != 1024*1024 {
		t.Fatalf("prasyarat uji: ukuran berkas %d, mau %d", len(tepat), 1024*1024)
	}
	if _, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input("tepat.jpg", tepat)}); err != nil {
		t.Fatalf("berkas tepat max_mb harus diterima: %v", err)
	}
}

func TestUploadIsolasiCabangDanPenguncianStatus(t *testing.T) {
	svc, _, _ := setup(t, 5, 5,
		OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted},
		OrderRef{ID: orderB, BranchID: "br-b", Status: StatusSubmitted},
	)
	isi := berkasPDF("transfer")

	// Cabang B tidak bisa menyentuh pesanan cabang A: 404, bukan 403.
	_, err := svc.Upload(context.Background(), branchActor("u2", "br-b"), orderA, []FileInput{input("x.pdf", isi)})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("lintas cabang harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.List(context.Background(), branchActor("u2", "br-b"), orderA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("daftar lintas cabang harus ErrNotFound, dapat %v", err)
	}
	// Id bukan UUID juga 404 (bukan 500 dari PostgreSQL).
	if _, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), "bukan-uuid", []FileInput{input("x.pdf", isi)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("id bukan UUID harus ErrNotFound, dapat %v", err)
	}
	// Aktor tanpa sesi / role salah ditolak.
	if _, err := svc.Upload(context.Background(), auth.Anonymous(), orderA, []FileInput{input("x.pdf", isi)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonim harus ErrForbidden, dapat %v", err)
	}

	// Cabang tidak boleh menambah bukti ke pesanan Done; admin tetap boleh.
	svc2, _, _ := setup(t, 5, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusDone})
	if _, err := svc2.Upload(context.Background(), branchActor("u1", "br-a"), orderA, []FileInput{input("x.pdf", isi)}); !errors.Is(err, ErrLocked) {
		t.Fatalf("pesanan Done untuk cabang harus ErrLocked, dapat %v", err)
	}
	if _, err := svc2.List(context.Background(), branchActor("u1", "br-a"), orderA); err != nil {
		t.Fatalf("membaca bukti pesanan Done tetap boleh: %v", err)
	}
	if _, err := svc2.Upload(context.Background(), adminActor("sa", "Rina"), orderA, []FileInput{input("x.pdf", isi)}); err != nil {
		t.Fatalf("admin harus boleh menambah bukti pada pesanan Done: %v", err)
	}
}

func TestUploadAdminMenandaiNamaDanTimeline(t *testing.T) {
	svc, repo, _ := setup(t, 5, 5,
		OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted},
		OrderRef{ID: orderB, BranchID: "br-b", Status: StatusDraft},
	)
	// Admin mengunggah atas nama cabang lain.
	got, err := svc.Upload(context.Background(), adminActor("sa", "Rina"), orderB,
		[]FileInput{input("cicilan.pdf", berkasPDF("cicilan 1"))})
	if err != nil {
		t.Fatalf("unggah admin: %v", err)
	}
	if got[0].UploadedByKind != "admin" || got[0].UploadedByNama != "Rina" {
		t.Errorf("penanda pengunggah admin salah: %+v", got[0])
	}
	// Draft belum punya timeline (keputusan kartu F1-11/F1-14).
	if ev := repo.Events(); len(ev) != 0 {
		t.Errorf("draft tidak boleh menghasilkan event timeline, dapat %v", ev)
	}
	// Pesanan Submitted → tiap bukti mencatat satu event dengan nama pengunggah.
	if _, err := svc.Upload(context.Background(), adminActor("sa", "Rina"), orderA,
		[]FileInput{input("a.jpg", berkasJPEG("a")), input("b.jpg", berkasJPEG("b"))}); err != nil {
		t.Fatalf("unggah admin 2 berkas: %v", err)
	}
	ev := repo.Events()
	if len(ev) != 2 {
		t.Fatalf("event timeline = %d, mau 2: %v", len(ev), ev)
	}
	for _, e := range ev {
		if e.OrderID != orderA || e.Kind != "admin" || e.Nama != "Rina" || e.ActorID != "sa" {
			t.Errorf("event timeline tidak menandai pengunggah admin: %+v", e)
		}
	}
}

func TestDownloadURLAmanDanBerumurPendek(t *testing.T) {
	svc, _, _ := setup(t, 5, 5,
		OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted},
		OrderRef{ID: orderB, BranchID: "br-b", Status: StatusSubmitted},
	)
	actorB := branchActor("u2", "br-b")
	if _, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("bukti.jpg", berkasJPEG("foto"))}); err != nil {
		t.Fatalf("unggah: %v", err)
	}
	list, err := svc.List(context.Background(), branchActor("u1", "br-a"), orderA)
	if err != nil || len(list) != 1 {
		t.Fatalf("daftar bukti: %v (%d)", err, len(list))
	}
	id := list[0].ID

	// Pemilik pesanan mendapat tautan berumur pendek, berisi nama berkas asli.
	dl, err := svc.DownloadURL(context.Background(), branchActor("u1", "br-a"), orderA, id)
	if err != nil {
		t.Fatalf("tautan unduh: %v", err)
	}
	if dl.ExpiresIn != int(DefaultPresignTTL.Seconds()) {
		t.Errorf("expires_in = %d, mau %d", dl.ExpiresIn, int(DefaultPresignTTL.Seconds()))
	}
	if !strings.Contains(dl.URL, "download=bukti.jpg") {
		t.Errorf("tautan unduh harus memaksa nama berkas asli: %q", dl.URL)
	}
	if dl.NamaFile != "bukti.jpg" || dl.MimeType != MimeJPEG {
		t.Errorf("metadata tautan unduh salah: %+v", dl)
	}
	if !strings.Contains(dl.URL, "/files/") {
		t.Errorf("tautan unduh harus berasal dari object storage: %q", dl.URL)
	}

	// Cabang lain: 404 (tidak membocorkan keberadaan bukti).
	if _, err := svc.DownloadURL(context.Background(), actorB, orderA, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tautan lintas cabang harus ErrNotFound, dapat %v", err)
	}
	// Bukti milik pesanan lain tidak bisa diambil lewat order_id yang bukan miliknya.
	if _, err := svc.DownloadURL(context.Background(), branchActor("u1", "br-a"), orderB, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bukti tidak cocok pesanan harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.DownloadURL(context.Background(), branchActor("u1", "br-a"), orderA, "bukan-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("id bukti bukan UUID harus ErrNotFound, dapat %v", err)
	}
}

func TestUploadTanpaBerkasDanBatasPermintaan(t *testing.T) {
	svc, _, _ := setup(t, 5, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	if _, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA, nil); !errors.Is(err, ErrNoFile) {
		t.Fatalf("tanpa berkas harus ErrNoFile, dapat %v", err)
	}
	// Batas badan permintaan = max_file × max_mb + kelonggaran.
	batas, err := svc.MaxRequestBytes(context.Background())
	if err != nil {
		t.Fatalf("batas permintaan: %v", err)
	}
	if mau := int64(5*5*1024*1024) + (1 << 20); batas != mau {
		t.Errorf("batas permintaan %d, mau %d", batas, mau)
	}
}

func TestUploadStorageBelumSiap(t *testing.T) {
	repo := NewMemory()
	src := NewMemoryOrderSource()
	src.Put(OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	svc := NewService(repo, src, storage.Disabled{}, configBatas(5, 5))
	_, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("bukti.jpg", berkasJPEG("foto"))})
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("storage belum siap harus ErrStorageUnavailable, dapat %v", err)
	}
	// Tidak ada metadata yang tertinggal saat penyimpanan gagal.
	list, _ := repo.ListByOrder(context.Background(), orderA)
	if len(list) != 0 {
		t.Errorf("tidak boleh ada baris bukti saat storage gagal, dapat %d", len(list))
	}
}

func TestUploadBersihkanNamaBerkas(t *testing.T) {
	svc, _, _ := setup(t, 5, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	got, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("/tmp/a\x00b\r\n.jpg", berkasJPEG("foto"))})
	if err != nil {
		t.Fatalf("unggah: %v", err)
	}
	nama := got[0].NamaFile
	for _, terlarang := range []string{"/", "\x00", "\r", "\n"} {
		if strings.Contains(nama, terlarang) {
			t.Errorf("nama berkas masih memuat %q: %q", terlarang, nama)
		}
	}
	// Nama kosong memakai nama bawaan sesuai jenis.
	got, err = svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("", berkasPDF("transfer"))})
	if err != nil {
		t.Fatalf("unggah tanpa nama: %v", err)
	}
	if got[0].NamaFile != "bukti.pdf" {
		t.Errorf("nama berkas bawaan %q, mau bukti.pdf", got[0].NamaFile)
	}
}

func TestTimelinePesananDraftTidakDicatat(t *testing.T) {
	svc, repo, _ := setup(t, 5, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusDraft})
	if _, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("modal.jpg", berkasJPEG("m"))}); err != nil {
		t.Fatalf("unggah pada draft (modal submit F1-14): %v", err)
	}
	if ev := repo.Events(); len(ev) != 0 {
		t.Errorf("draft tidak boleh menulis timeline, dapat %v", ev)
	}
}

func TestKunciAcakSelaluUnik(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k, err := kunciAcak(orderA, kindJPEG)
		if err != nil {
			t.Fatalf("kunci ke-%d: %v", i, err)
		}
		if seen[k] {
			t.Fatalf("kunci berulang: %s", k)
		}
		seen[k] = true
	}
}

func TestBatasDariSetelanBukanKonstanta(t *testing.T) {
	// max_file=1 → unggahan kedua ditolak, walau default PRD 5.
	svc, _, _ := setup(t, 1, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	actor := branchActor("u1", "br-a")
	if _, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input("a.jpg", berkasJPEG("a"))}); err != nil {
		t.Fatalf("unggah pertama: %v", err)
	}
	_, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input("b.jpg", berkasJPEG("b"))})
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("max_file=1 harus menolak unggahan kedua, dapat %v", err)
	}
}

func TestPresignTTLBisaDiubah(t *testing.T) {
	repo := NewMemory()
	src := NewMemoryOrderSource()
	src.Put(OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	svc := NewService(repo, src, storage.NewMemory("/files"), configBatas(5, 5), WithPresignTTL(time.Minute))
	actor := branchActor("u1", "br-a")
	if _, err := svc.Upload(context.Background(), actor, orderA, []FileInput{input("a.pdf", berkasPDF("a"))}); err != nil {
		t.Fatalf("unggah: %v", err)
	}
	list, _ := svc.List(context.Background(), actor, orderA)
	dl, err := svc.DownloadURL(context.Background(), actor, orderA, list[0].ID)
	if err != nil {
		t.Fatalf("tautan: %v", err)
	}
	if dl.ExpiresIn != 60 {
		t.Errorf("expires_in = %d, mau 60", dl.ExpiresIn)
	}
}

// storeUji membungkus storage.Memory dan mencatat kunci objek yang benar-benar
// tersimpan, supaya pengujian bisa memastikan tidak ada objek yatim.
type storeUji struct {
	*storage.Memory
	mu    sync.Mutex
	kunci map[string]bool
}

func newStoreUji() *storeUji {
	return &storeUji{Memory: storage.NewMemory("/files"), kunci: map[string]bool{}}
}

func (s *storeUji) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := s.Memory.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	s.mu.Lock()
	s.kunci[key] = true
	s.mu.Unlock()
	return nil
}

func (s *storeUji) Delete(ctx context.Context, key string) error {
	if err := s.Memory.Delete(ctx, key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.kunci, key)
	s.mu.Unlock()
	return nil
}

func (s *storeUji) jumlah() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.kunci)
}

// storeGagal meniru object storage yang gagal di tengah penulisan: berkas
// ke-`gagalMulai` dan seterusnya tidak pernah masuk.
type storeGagal struct {
	*storeUji
	gagalMulai int
	ke         int
}

func (s *storeGagal) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	s.ke++
	if s.ke >= s.gagalMulai {
		return errors.New("storage penuh")
	}
	return s.storeUji.Put(ctx, key, body, size, contentType)
}

// repoGagalCommit selalu gagal saat komit metadata: dipakai memastikan service
// membersihkan SELURUH objek yang sudah ditulis, bukan hanya yang terakhir.
type repoGagalCommit struct{ *Memory }

func (repoGagalCommit) CommitProofs(context.Context, CommitRequest) ([]Proof, error) {
	return nil, ErrStorageUnavailable
}

// TestUploadPermintaanGagalTidakMenyisakanApaPun mengulang temuan QA: satu
// permintaan berisi berkas BARU + berkas yang isinya sudah tersimpan harus
// ditolak tanpa menambah baris maupun objek apa pun. Dulu berkas baru itu
// ikut tersimpan permanen walau responsnya 409.
func TestUploadPermintaanGagalTidakMenyisakanApaPun(t *testing.T) {
	repo := NewMemory()
	src := NewMemoryOrderSource()
	src.Put(OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	store := newStoreUji()
	svc := NewService(repo, src, store, configBatas(5, 5))
	actor := branchActor("u1", "br-a")
	ctx := context.Background()

	isiLama := berkasJPEG("sudah-ada")
	if _, err := svc.Upload(ctx, actor, orderA, []FileInput{input("a.jpg", isiLama)}); err != nil {
		t.Fatalf("unggah pertama: %v", err)
	}
	// Satu permintaan: satu berkas baru + satu berkas yang isinya duplikat.
	_, err := svc.Upload(ctx, actor, orderA,
		[]FileInput{input("b.jpg", berkasJPEG("baru")), input("c.jpg", isiLama)})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplikat dalam satu permintaan harus ErrDuplicate, dapat %v", err)
	}
	list, err := repo.ListByOrder(ctx, orderA)
	if err != nil {
		t.Fatalf("daftar bukti: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("permintaan gagal tidak boleh menambah baris, dapat %d", len(list))
	}
	if n := store.jumlah(); n != 1 {
		t.Errorf("storage harus tetap berisi 1 objek, dapat %d", n)
	}
	if ev := repo.Events(); len(ev) != 1 {
		t.Errorf("timeline tidak boleh bertambah dari permintaan gagal, dapat %d", len(ev))
	}
}

// TestUploadGagalDiTengahStorageMembersihkanSemuaObjek memastikan kegagalan
// penulisan berkas ke-2 membersihkan berkas ke-1 yang sudah terlanjur ditulis.
func TestUploadGagalDiTengahStorageMembersihkanSemuaObjek(t *testing.T) {
	repo := NewMemory()
	src := NewMemoryOrderSource()
	src.Put(OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	store := &storeGagal{storeUji: newStoreUji(), gagalMulai: 2}
	svc := NewService(repo, src, store, configBatas(5, 5))

	_, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("a.jpg", berkasJPEG("a")), input("b.jpg", berkasJPEG("b"))})
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("kegagalan storage harus ErrStorageUnavailable, dapat %v", err)
	}
	if n := store.jumlah(); n != 0 {
		t.Errorf("objek yatim tersisa setelah kegagalan storage: %d", n)
	}
	if list, _ := repo.ListByOrder(context.Background(), orderA); len(list) != 0 {
		t.Errorf("kegagalan storage tidak boleh menyisakan baris bukti: %d", len(list))
	}
}

// TestUploadGagalCommitMembersihkanSemuaObjek menutup celah object orphan:
// bila transaksi metadata batal, seluruh objek yang sudah ditulis dihapus.
func TestUploadGagalCommitMembersihkanSemuaObjek(t *testing.T) {
	repo := repoGagalCommit{Memory: NewMemory()}
	src := NewMemoryOrderSource()
	src.Put(OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	store := newStoreUji()
	svc := NewService(repo, src, store, configBatas(5, 5))

	_, err := svc.Upload(context.Background(), branchActor("u1", "br-a"), orderA,
		[]FileInput{input("a.jpg", berkasJPEG("a")), input("b.jpg", berkasJPEG("b"))})
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("commit gagal harus diteruskan sebagai ErrStorageUnavailable, dapat %v", err)
	}
	if n := store.jumlah(); n != 0 {
		t.Errorf("semua objek harus dibersihkan saat commit batal, sisa %d", n)
	}
}

// TestUploadParalelTidakMelampauiMaxFile menuntut batas jumlah tidak bisa
// dilampaui unggahan serentak: 2×max_file permintaan bersamaan harus
// menyisakan tepat max_file baris.
func TestUploadParalelTidakMelampauiMaxFile(t *testing.T) {
	const maxFile = 3
	total := 2 * maxFile
	svc, repo, _ := setup(t, maxFile, 5, OrderRef{ID: orderA, BranchID: "br-a", Status: StatusSubmitted})
	actor := branchActor("u1", "br-a")

	errs := make([]error, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nama := fmt.Sprintf("paralel-%d.jpg", i)
			_, errs[i] = svc.Upload(context.Background(), actor, orderA,
				[]FileInput{input(nama, berkasJPEG(nama))})
		}(i)
	}
	wg.Wait()

	var sukses, ditolak int
	for i, err := range errs {
		switch {
		case err == nil:
			sukses++
		case errors.Is(err, ErrTooMany):
			ditolak++
		default:
			t.Errorf("unggahan ke-%d gagal tak terduga: %v", i, err)
		}
	}
	list, err := repo.ListByOrder(context.Background(), orderA)
	if err != nil {
		t.Fatalf("daftar bukti: %v", err)
	}
	if len(list) != maxFile {
		t.Fatalf("tepat %d bukti harus tersimpan, dapat %d (sukses=%d ditolak=%d)",
			maxFile, len(list), sukses, ditolak)
	}
	if sukses != maxFile || ditolak != total-maxFile {
		t.Errorf("sebaran hasil salah: sukses=%d ditolak=%d (mau %d/%d)",
			sukses, ditolak, maxFile, total-maxFile)
	}
}
