// Package payment mengelola bukti transfer pesanan: validasi berkas,
// penyimpanan ke object storage, pencatatan metadata, dan tautan unduh
// berumur pendek (kartu F1-13).
//
// Aturan yang ditegakkan di sini — bukan hanya disembunyikan di UI:
//
//   - Setiap akses dibatasi data pemiliknya. Akun CABANG hanya boleh menyentuh
//     pesanan milik cabang SESI-nya (Actor.BranchID); pesanan cabang lain —
//     maupun id yang bukan UUID — dijawab 404 tanpa membocorkan keberadaannya
//     (isolasi data cabang, Architecture.md §6, PRD §2.1).
//   - Berkas TIDAK PERNAH disimpan di database. Isinya masuk object storage
//     S3-compatible dengan kunci acak `proofs/{order_id}/{acak}.{ext}`, dan
//     nama berkas asli hanya dipakai sebagai metadata tampilan. Nama acak
//     membuat berkas tidak bisa ditebak/diiterasi lewat URL storage.
//   - Isi berkas ditentukan oleh MAGIC BYTES, bukan oleh nama atau
//     Content-Type kiriman klien. Ekstensi yang tidak cocok dengan isinya
//     ditolak (PRD CP-ORD-04), sehingga berkas dengan ekstensi palsu tidak
//     masuk (mis. skrip berkedok `.jpg`).
//   - Batas `max_file` × `max_mb` dibaca dari SETELAN SERVER (PRD §4.9),
//     bukan dari klien; jumlah dihitung dari baris yang benar-benar tersimpan.
//   - SHA-256 isi berkas dicatat dan wajib unik per pesanan: mengunggah berkas
//     yang sama dua kali ditolak 409 (SystemDesign.md §5.3) — ditegakkan juga
//     oleh indeks unik (order_id, sha256) di database.
//   - Unduhan selalu lewat presigned URL berumur pendek (Architecture.md §6,
//     SystemDesign.md §8) dengan Content-Disposition attachment bernama asli.
//     Kunci storage TIDAK pernah diberikan lewat API: satu-satunya tempat ia
//     muncul adalah di dalam URL bertanda tangan yang kedaluwarsa, dan karena
//     namanya acak berkas tidak bisa ditebak maupun diiterasi.
//   - Unggahan dicatat di timeline pesanan beserta NAMA pengunggahnya — cabang
//     atau admin (PRD CP-ORD-08, AP-ORD-06).
//
// Catatan batas kartu: transaksi submit (Draft→Submitted, penomoran, status
// C&C) dan verifikasi pembayaran adalah kartu F1-14/F1-17. Paket ini
// menyediakan penyimpanan bukti yang dipakai keduanya; ia sengaja TIDAK
// mengubah status pesanan.
package payment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// Sentinel error domain. Pemetaan ke bentuk HTTP ada di lapisan transport.
var (
	// ErrForbidden dikembalikan bila aktor tidak berhak atas bukti pesanan ini.
	ErrForbidden = errors.New("tidak berhak mengakses bukti pesanan ini")
	// ErrNotFound dikembalikan bila pesanan/bukti tidak ada ATAU bukan milik
	// aktor — keduanya dijawab sama supaya keberadaannya tidak bocor.
	ErrNotFound = errors.New("pesanan atau bukti tidak ditemukan")
	// ErrNoBranch dikembalikan bila akun cabang tidak terhubung ke cabang.
	ErrNoBranch = errors.New("akun tidak terhubung ke cabang")
	// ErrLocked dikembalikan bila cabang mencoba menambah bukti pada pesanan
	// yang sudah selesai (PRD BR-07). Admin tetap boleh (revisi/koreksi).
	ErrLocked = errors.New("pesanan selesai tidak dapat ditambah bukti oleh cabang")
	// ErrNoFile dikembalikan bila permintaan tidak memuat satu berkas pun.
	ErrNoFile = errors.New("tidak ada berkas bukti yang dikirim")
	// ErrTooMany dikembalikan bila jumlah bukti pesanan akan melampaui setelan
	// `max_file` (PRD §4.9, CP-ORD-04).
	ErrTooMany = errors.New("jumlah bukti melebihi batas")
	// ErrTooLarge dikembalikan bila satu berkas melampaui setelan `max_mb`.
	ErrTooLarge = errors.New("ukuran berkas melebihi batas")
	// ErrUnsupported dikembalikan bila jenis berkas bukan JPG/PNG/PDF.
	ErrUnsupported = errors.New("jenis berkas tidak didukung")
	// ErrFakeExt dikembalikan bila isi berkas tidak cocok dengan ekstensinya
	// (ekstensi palsu) atau ekstensinya bukan salah satu yang diizinkan.
	ErrFakeExt = errors.New("isi berkas tidak cocok dengan ekstensinya")
	// ErrDuplicate dikembalikan bila isi berkas yang sama sudah ada pada
	// pesanan itu (SHA-256 sama, SystemDesign.md §5.3).
	ErrDuplicate = errors.New("berkas bukti yang sama sudah pernah diunggah")
	// ErrStorageUnavailable dikembalikan bila object storage belum/tidak bisa
	// dipakai sehingga berkas tidak mungkin disimpan dengan aman.
	ErrStorageUnavailable = errors.New("penyimpanan berkas belum tersedia")
)

// Jenis MIME yang diterima untuk bukti transfer (PRD CP-ORD-04).
const (
	MimeJPEG = "image/jpeg"
	MimePNG  = "image/png"
	MimePDF  = "application/pdf"
)

// Status pesanan yang relevan (mirror enum order_status, Database.md §3).
const (
	StatusDraft     = "draft"
	StatusSubmitted = "submitted"
	StatusDone      = "done"
)

// FieldBerkas adalah nama field multipart yang diterima endpoint unggah.
const FieldBerkas = "file"

// DefaultPresignTTL adalah umur tautan unduh bukti. Sengaja pendek: tautan
// hanya seumur satu sesi menonton/pratinjau (Architecture.md §6).
const DefaultPresignTTL = 5 * time.Minute

// Proof adalah metadata satu bukti transfer. Isi berkasnya tidak ada di sini:
// hanya `storage_key` pada object storage.
type Proof struct {
	ID         string `json:"id"`
	OrderID    string `json:"order_id"`
	NamaFile   string `json:"nama_file"`
	MimeType   string `json:"mime_type"`
	UkuranByte int64  `json:"ukuran_byte"`
	// Sha256 adalah sidik jari isi berkas (hex), dipakai mencegah unggahan
	// ganda pada satu pesanan.
	Sha256 string `json:"sha256"`
	// UploadedByKind bernilai "branch" atau "admin" (kolom uploaded_by_kind).
	UploadedByKind string `json:"uploaded_by_kind"`
	UploadedByID   string `json:"uploaded_by_id,omitempty"`
	// UploadedByNama adalah penanda pengunggah admin (PRD AP-ORD-06);
	// untuk akun cabang dikosongkan.
	UploadedByNama string    `json:"uploaded_by_nama,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	// StorageKey tidak pernah dikirim ke klien (json:"-").
	StorageKey string `json:"-"`
}

// Download adalah hasil permintaan tautan unduh: URL berumur pendek beserta
// masa berlakunya (detik).
type Download struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
	NamaFile  string `json:"nama_file"`
	MimeType  string `json:"mime_type"`
}

// FileInput adalah satu berkas yang diterima dari permintaan multipart.
//
// ContentType dari klien sengaja TIDAK dipercaya: yang menentukan jenis
// berkas adalah magic bytes isinya.
type FileInput struct {
	NamaFile   string
	Content    io.Reader
	UkuranHint int64
}

// OrderRef adalah konteks pesanan yang dibutuhkan domain bukti.
type OrderRef struct {
	ID       string
	BranchID string
	Status   string
	Nomor    string
}

// Repository memuat dan menyimpan metadata bukti. Semua pencarian dibatasi
// order_id supaya bukti tidak pernah bisa diambil lewat pesanan lain.
type Repository interface {
	// CountByOrder menghitung bukti yang sudah tersimpan pada satu pesanan.
	CountByOrder(ctx context.Context, orderID string) (int64, error)
	// Add menyimpan metadata satu bukti; duplikat (order_id, sha256)
	// dikembalikan sebagai ErrDuplicate.
	Add(ctx context.Context, p Proof) (Proof, error)
	// ListByOrder mengembalikan seluruh bukti satu pesanan, terlama dulu.
	ListByOrder(ctx context.Context, orderID string) ([]Proof, error)
	// Get mengembalikan satu bukti milik pesanan tersebut, atau ErrNotFound.
	Get(ctx context.Context, id, orderID string) (Proof, error)
}

// OrderSource membaca konteks pesanan (pemilik + status). Ia sengaja berupa
// interface sendiri supaya domain bukti tidak bergantung pada repository
// pesanan; implementasinya ada di payment/postgres.
type OrderSource interface {
	OrderForProof(ctx context.Context, orderID string) (OrderRef, error)
}

// Timeline mencatat peristiwa unggahan bukti pada timeline pesanan
// (PRD CP-ORD-08, AP-ORD-06). Aktornya dicatat apa adanya.
type Timeline interface {
	RecordProofUpload(ctx context.Context, orderID, nama, kind string, actorID string) error
}

// Config adalah sudut pandang domain pengaturan yang dibutuhkan batas unggah
// (max_file/max_mb); nilainya dibaca SERVER, bukan dari klien.
type Config interface {
	Int(ctx context.Context, key string) (int64, error)
}

// DownloadSigner adalah object storage yang bisa menghasilkan tautan unduh
// dengan paksaan Content-Disposition attachment.
//
// Ini opsional: Store dasar tetap cukup (PresignGet). Bila storage
// mengimplementasikannya, tautan unduh membawa nama berkas asli sehingga
// browser menyimpannya dengan nama yang benar.
type DownloadSigner interface {
	PresignDownload(ctx context.Context, key, namaFile string, ttl time.Duration) (string, error)
}

// Service adalah logika bisnis bukti transfer.
type Service struct {
	repo   Repository
	orders OrderSource
	store  storage.Store
	cfg    Config
	// timeline boleh nil (mis. pada pengujian domain): pencatatan timeline
	// adalah efek samping, bukan syarat tersimpannya bukti.
	timeline Timeline
	ttl      time.Duration
}

// Option mengubah perilaku service.
type Option func(*Service)

// WithTimeline memasang pencatat timeline pesanan.
func WithTimeline(t Timeline) Option { return func(s *Service) { s.timeline = t } }

// WithPresignTTL mengubah umur tautan unduh (detik).
func WithPresignTTL(d time.Duration) Option { return func(s *Service) { s.ttl = d } }

// NewService membuat service bukti transfer. store dan cfg tidak boleh nil:
// berkas wajib punya tujuan penyimpanan, dan batas unggah wajib dari server.
func NewService(repo Repository, orders OrderSource, store storage.Store, cfg Config, opts ...Option) *Service {
	s := &Service{repo: repo, orders: orders, store: store, cfg: cfg, ttl: DefaultPresignTTL}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Upload memvalidasi, menyimpan, dan mencatat seluruh berkas bukti satu
// permintaan. Seluruh berkas divalidasi LEBIH DAHULU: bila ada satu yang tidak
// lolos, tidak ada yang tersimpan (all-or-nothing di sisi berkas).
func (s *Service) Upload(ctx context.Context, actor auth.Actor, orderID string, files []FileInput) ([]Proof, error) {
	if len(files) == 0 {
		return nil, ErrNoFile
	}
	ref, err := s.authorize(ctx, actor, orderID, true)
	if err != nil {
		return nil, err
	}

	maxFile, maxBytes, err := s.limits(ctx)
	if err != nil {
		return nil, err
	}

	ada, err := s.repo.CountByOrder(ctx, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("hitung bukti tersimpan: %w", err)
	}
	if ada+int64(len(files)) > maxFile {
		return nil, fmt.Errorf("%w: maksimal %d berkas per pesanan (tersimpan %d)",
			ErrTooMany, maxFile, ada)
	}

	// Tahap 1 — validasi & sidik jari seluruh berkas, tanpa menulis apa pun.
	disiapkan := make([]prepared, 0, len(files))
	terlihat := map[string]bool{}
	for i, f := range files {
		p, err := bacaBerkas(f, maxBytes)
		if err != nil {
			return nil, fmt.Errorf("berkas ke-%d: %w", i+1, err)
		}
		if terlihat[p.sha256Hex] {
			return nil, fmt.Errorf("berkas ke-%d: %w", i+1, ErrDuplicate)
		}
		terlihat[p.sha256Hex] = true
		disiapkan = append(disiapkan, p)
	}

	// Tahap 2 — simpan isi ke object storage, lalu catat metadatanya.
	out := make([]Proof, 0, len(disiapkan))
	for i, p := range disiapkan {
		key, err := kunciAcak(ref.ID, p.kind)
		if err != nil {
			return nil, err
		}
		if err := s.store.Put(ctx, key, bytes.NewReader(p.isi), int64(len(p.isi)), p.mime); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		saved, err := s.repo.Add(ctx, Proof{
			OrderID:        ref.ID,
			StorageKey:     key,
			NamaFile:       p.namaFile,
			MimeType:       p.mime,
			UkuranByte:     int64(len(p.isi)),
			Sha256:         p.sha256Hex,
			UploadedByKind: string(actor.Kind),
			UploadedByID:   actor.UserID,
			UploadedByNama: namaPengunggah(actor),
		})
		if err != nil {
			// Metadata gagal dicatat: berkas yang baru ditulis dibuang lagi
			// supaya storage tidak menyimpan objek yatim.
			_ = s.store.Delete(ctx, key)
			if errors.Is(err, ErrDuplicate) {
				return nil, fmt.Errorf("berkas ke-%d: %w", i+1, ErrDuplicate)
			}
			return nil, err
		}
		out = append(out, saved)
	}

	// Timeline hanya untuk pesanan yang sudah dikirim: draft belum punya
	// timeline (keputusan kartu F1-11/F1-14).
	if s.timeline != nil && ref.Status != StatusDraft {
		for _, p := range out {
			if err := s.timeline.RecordProofUpload(ctx, ref.ID, p.UploadedByNama, p.UploadedByKind, p.UploadedByID); err != nil {
				return nil, fmt.Errorf("catat timeline bukti: %w", err)
			}
		}
	}
	return out, nil
}

// List mengembalikan seluruh bukti satu pesanan yang boleh dilihat aktor.
func (s *Service) List(ctx context.Context, actor auth.Actor, orderID string) ([]Proof, error) {
	ref, err := s.authorize(ctx, actor, orderID, false)
	if err != nil {
		return nil, err
	}
	return s.repo.ListByOrder(ctx, ref.ID)
}

// DownloadURL membuat tautan unduh berumur pendek untuk SATU bukti pesanan.
func (s *Service) DownloadURL(ctx context.Context, actor auth.Actor, orderID, proofID string) (Download, error) {
	ref, err := s.authorize(ctx, actor, orderID, false)
	if err != nil {
		return Download{}, err
	}
	id := strings.TrimSpace(proofID)
	if !uuidRe.MatchString(id) {
		return Download{}, ErrNotFound
	}
	p, err := s.repo.Get(ctx, id, ref.ID)
	if err != nil {
		return Download{}, err
	}
	url, err := s.presign(ctx, p)
	if err != nil {
		return Download{}, err
	}
	return Download{
		URL:       url,
		ExpiresIn: int(s.ttl.Seconds()),
		NamaFile:  p.NamaFile,
		MimeType:  p.MimeType,
	}, nil
}

// presign membuat tautan unduh; bila storage mendukung paksaan
// Content-Disposition attachment, nama berkas asli ikut dibawa.
func (s *Service) presign(ctx context.Context, p Proof) (string, error) {
	var (
		url string
		err error
	)
	if ds, ok := s.store.(DownloadSigner); ok {
		url, err = ds.PresignDownload(ctx, p.StorageKey, p.NamaFile, s.ttl)
	} else {
		url, err = s.store.PresignGet(ctx, p.StorageKey, s.ttl)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
	}
	return url, nil
}

// authorize memeriksa aktor dan mengembalikan konteks pesanannya.
//
// Akun cabang: wajib punya CapOrderWrite dan hanya boleh menyentuh pesanan
// cabang SESI-nya; menambah bukti pada pesanan Done ditolak (BR-07), sedangkan
// membaca tetap boleh. Admin: wajib punya CapAdminOrders (Manager maupun
// Superadmin — PRD §2.1) dan boleh menyentuh pesanan mana pun.
func (s *Service) authorize(ctx context.Context, actor auth.Actor, orderID string, tulis bool) (OrderRef, error) {
	switch {
	case actor.IsBranch():
		if !actor.Can(auth.CapOrderWrite) {
			return OrderRef{}, ErrForbidden
		}
		if actor.BranchID == "" {
			return OrderRef{}, ErrNoBranch
		}
	case actor.IsAdmin():
		if !actor.Can(auth.CapAdminOrders) {
			return OrderRef{}, ErrForbidden
		}
	default:
		return OrderRef{}, ErrForbidden
	}

	id := strings.TrimSpace(orderID)
	if !uuidRe.MatchString(id) {
		return OrderRef{}, ErrNotFound
	}
	ref, err := s.orders.OrderForProof(ctx, id)
	if err != nil {
		return OrderRef{}, err
	}
	if actor.IsBranch() {
		if ref.BranchID != actor.BranchID {
			// Pesanan cabang lain: dijawab 404, bukan 403, supaya
			// keberadaannya tidak bocor.
			return OrderRef{}, ErrNotFound
		}
		if tulis && ref.Status == StatusDone {
			return OrderRef{}, ErrLocked
		}
	}
	return ref, nil
}

// limits membaca batas unggah yang berlaku dari setelan server.
func (s *Service) limits(ctx context.Context) (maxFile int64, maxBytes int64, err error) {
	maxFile, err = s.cfg.Int(ctx, keyMaxFile)
	if err != nil {
		return 0, 0, fmt.Errorf("baca setelan %s: %w", keyMaxFile, err)
	}
	maxMB, err := s.cfg.Int(ctx, keyMaxMB)
	if err != nil {
		return 0, 0, fmt.Errorf("baca setelan %s: %w", keyMaxMB, err)
	}
	if maxFile < 1 {
		maxFile = 1
	}
	if maxMB < 1 {
		maxMB = 1
	}
	return maxFile, maxMB * 1024 * 1024, nil
}

// MaxRequestBytes adalah batas ukuran badan permintaan multipart yang dipakai
// lapisan transport: seluruh berkas sekaligus + kelonggaran header multipart.
// Dibaca dari setelan yang sama supaya UI, service, dan transport sepakat.
func (s *Service) MaxRequestBytes(ctx context.Context) (int64, error) {
	maxFile, maxBytes, err := s.limits(ctx)
	if err != nil {
		return 0, err
	}
	return maxFile*maxBytes + (1 << 20), nil
}

// prepared adalah hasil validasi satu berkas: sudah pasti jenisnya dan sudah
// punya sidik jari, tetapi belum menyentuh storage.
type prepared struct {
	isi       []byte
	kind      berkasKind
	mime      string
	namaFile  string
	sha256Hex string
}

// bacaBerkas membaca satu berkas (dibatasi maxBytes) lalu menetapkannya jenis
// dari magic bytes isinya — bukan dari nama/Content-Type klien.
func bacaBerkas(f FileInput, maxBytes int64) (prepared, error) {
	isi, err := io.ReadAll(io.LimitReader(f.Content, maxBytes+1))
	if err != nil {
		return prepared{}, fmt.Errorf("baca berkas: %w", err)
	}
	if int64(len(isi)) > maxBytes {
		return prepared{}, ErrTooLarge
	}
	if len(isi) == 0 {
		return prepared{}, fmt.Errorf("%w: berkas kosong", ErrUnsupported)
	}
	kind, ok := kenali(isi)
	if !ok {
		return prepared{}, fmt.Errorf("%w: hanya JPG, PNG, atau PDF", ErrUnsupported)
	}
	nama := bersihkanNama(f.NamaFile, kind)
	if ext := ekstensiBerkas(nama); !kind.extCocok[ext] {
		return prepared{}, fmt.Errorf("%w: ekstensi %q bukan %s", ErrFakeExt, ext, kind.nama)
	}
	sum := sha256.Sum256(isi)
	return prepared{
		isi:       isi,
		kind:      kind,
		mime:      kind.mime,
		namaFile:  nama,
		sha256Hex: hex.EncodeToString(sum[:]),
	}, nil
}

// namaPengunggah mengisi penanda pengunggah: nama admin dicatat apa adanya
// (PRD AP-ORD-06), akun cabang dikosongkan karena satu cabang satu akun.
func namaPengunggah(actor auth.Actor) string {
	if actor.IsAdmin() {
		return actor.Nama
	}
	return ""
}

// kunciAcak menyusun kunci object storage dengan nama berkas ACAK sehingga
// tidak bisa ditebak maupun diiterasi.
func kunciAcak(orderID string, kind berkasKind) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("acak nama berkas: %w", err)
	}
	return "proofs/" + orderID + "/" + hex.EncodeToString(buf) + "." + kind.ext, nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// namaBerkasMaks membatasi panjang nama berkas yang disimpan.
const namaBerkasMaks = 120

// bersihkanNama membersihkan nama berkas kiriman klien: tanpa komponen
// direktori, tanpa karakter kendali (cegah header injection pada
// Content-Disposition), panjang dibatasi. Nama kosong memakai nama bawaan.
func bersihkanNama(nama string, kind berkasKind) string {
	nama = strings.TrimSpace(nama)
	if i := strings.LastIndexAny(nama, `/\`); i >= 0 {
		nama = nama[i+1:]
	}
	nama = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, nama)
	nama = strings.TrimSpace(nama)
	if nama == "" || nama == "." || nama == ".." {
		return "bukti." + kind.ext
	}
	if len([]rune(nama)) > namaBerkasMaks {
		runes := []rune(nama)
		nama = string(runes[:namaBerkasMaks])
	}
	return nama
}
