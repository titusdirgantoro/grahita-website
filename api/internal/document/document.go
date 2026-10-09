// Package document mengelola dokumen pesanan — Invoice PDF dan Surat Jalan
// XLSX/PDF (kartu F1-17; PRD §6.1; SystemDesign.md §4.4, §8; Database.md §4.6).
//
// Aturan yang ditegakkan di sini — bukan hanya disembunyikan di UI:
//
//   - Dokumen lahir HANYA untuk pesanan berstatus `done` (PRD §6.1). Permintaan
//     untuk pesanan draft/submitted dijawab ErrNotDone, sehingga invoice tanpa
//     nomor/tanggal tidak pernah bisa dibuat.
//   - Render IDEMPOTEN dan berkas lama TIDAK pernah ditimpa. Kunci object
//     storage `documents/{order_id}/{jenis}-{versi}.{ext}` memuat versi, dan
//     baris `documents` punya unique (order_id, jenis, versi). Menjalankan
//     ulang render untuk versi yang sudah ada tidak menyentuh berkasnya sama
//     sekali; revisi invoice (versi +1, F2-25) menghasilkan kunci BARU
//     sehingga berkas versi lama tetap utuh (PRD §9 no.8).
//   - Versi dokumen selalu = versi pesanan SAAT render. Merender versi lain
//     ditolak (ErrInvalidData) supaya berkas versi lama tidak pernah diisi
//     data pesanan terbaru.
//   - SATU permintaan render = SATU unit kerja DI SISI DATABASE: seluruh isi
//     dirender lebih dahulu di memori, baru ditulis ke object storage, lalu
//     SELURUH baris `documents` disimpan dalam satu transaksi. Atomisitas itu
//     HANYA berlaku untuk tabel `documents`: penulisan objek TIDAK berada di
//     dalam transaksi DB (object storage bukan transaksional), jadi tidak ada
//     klaim atomisitas lintas DB↔object storage.
//   - Jalur render TIDAK PERNAH menghapus objek — tidak pada kegagalan, tidak
//     pada jalur sukses. Kunci storage deterministik per (order, jenis, versi)
//     dipakai bersama semua permintaan render dokumen yang sama, jadi setiap
//     penghapusan kunci berisiko mencabut berkas milik baris yang sudah atau
//     akan di-commit permintaan lain, dan kerusakannya PERMANEN (render ulang
//     menganggap barisnya sudah ada sehingga berkasnya tidak pernah dibuat
//     lagi). Karena baris `documents` hanya ditulis SETELAH seluruh objeknya
//     berhasil ditulis, berlaku untuk SEMUA penjadwalan: bila baris
//     (order, jenis, versi) ada, objeknya ada. Harga aturan ini adalah objek
//     YATIM bila permintaan gagal setelah sebagian objeknya tertulis — jauh
//     lebih ringan daripada baris tanpa berkas, dan direkonsiliasi oleh F1-21
//     (lihat logObjekTertinggal).
//   - Akses selalu lewat presigned URL berumur pendek dan diperiksa per aktor:
//     akun cabang hanya boleh mengunduh INVOICE pesanan cabang SESI-nya
//     (CapInvoiceOwn), sedangkan staf Pusat dengan CapAdminOrders boleh
//     mengunduh semua dokumen semua cabang (PRD §2.1, §6.1). Dokumen cabang
//     lain dijawab 404 tanpa membocorkan keberadaannya, dan kunci storage
//     tidak pernah dikirim ke klien.
//   - Riwayat invoice cabang (kartu F1-19, PRD §3.8) dibaca lewat
//     Service.Invoices: satu baris per dokumen invoice versi berapa pun
//     (versi lama tetap terlihat setelah revisi) ditambah pesanan ber-nomor
//     yang dokumennya belum ada, selalu dibatasi branch_id SESI.
//
// Catatan kartu: PRD §8 no.7 (template resmi Surat Jalan) dan no.9 (tanda
// tangan/stempel digital) masih terbuka. Paket ini memakai template awal yang
// rapi tanpa tanda tangan/stempel; keputusan bisnis menyusul.
package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// Jenis dokumen (Database.md §3, enum doc_kind).
const (
	JenisInvoicePDF     = "invoice_pdf"
	JenisSuratJalanXLSX = "surat_jalan_xlsx"
	JenisSuratJalanPDF  = "surat_jalan_pdf"
)

// Status pesanan yang relevan (mirror enum order_status, Database.md §3).
const StatusDone = "done"

// JenisBarisProduk adalah jenis baris pesanan yang dihitung sebagai item
// pesanan (mirror enum line_kind, Database.md §3): baris biaya packaging
// pesanan Sertifikat TIDAK dihitung.
const JenisBarisProduk = "product"

// DirStorage adalah awalan kunci object storage seluruh dokumen
// (SystemDesign.md §8).
const DirStorage = "documents"

// DefaultPresignTTL adalah umur tautan unduh dokumen. Sengaja pendek: tautan
// hanya seumur satu sesi mengunduh/mencetak (Architecture.md §6).
const DefaultPresignTTL = 5 * time.Minute

// MimeType dokumen.
const (
	MimePDF  = "application/pdf"
	MimeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// jenisSaatDone adalah ketiga dokumen yang lahir saat pesanan Done, dengan
// urutan render seperti SystemDesign.md §4.4.
var jenisSaatDone = []string{JenisInvoicePDF, JenisSuratJalanXLSX, JenisSuratJalanPDF}

// Sentinel error domain. Pemetaan ke bentuk HTTP ada di lapisan transport.
var (
	// ErrForbidden dikembalikan bila aktor tidak berhak atas dokumen ini.
	ErrForbidden = errors.New("tidak berhak mengakses dokumen pesanan")
	// ErrNotFound dikembalikan bila pesanan/dokumen tidak ada ATAU bukan milik
	// aktor — keduanya dijawab sama supaya keberadaannya tidak bocor.
	ErrNotFound = errors.New("pesanan atau dokumen tidak ditemukan")
	// ErrNotDone dikembalikan bila dokumen diminta untuk pesanan yang belum
	// selesai: dokumen lahir saat status Done (PRD §6.1).
	ErrNotDone = errors.New("dokumen hanya dibuat untuk pesanan berstatus done")
	// ErrStorageUnavailable dikembalikan bila object storage belum/tidak bisa
	// dipakai sehingga berkas tidak mungkin disimpan dengan aman.
	ErrStorageUnavailable = errors.New("penyimpanan dokumen belum tersedia")
	// ErrRenderUnavailable dikembalikan bila mesin pembuat PDF (Gotenberg)
	// belum dikonfigurasi sehingga dokumen tidak mungkin dirender.
	ErrRenderUnavailable = errors.New("mesin pembuat PDF belum tersedia")
	// ErrInvalidData dikembalikan bila data pesanan tidak cukup untuk
	// merender dokumen yang dimintai.
	ErrInvalidData = errors.New("data pesanan tidak cukup untuk dokumen")
	// ErrNoBranch dikembalikan bila akun cabang tidak terhubung ke cabang,
	// sehingga tidak ada riwayat invoice yang bisa ditampilkan.
	ErrNoBranch = errors.New("akun tidak terhubung ke cabang")
	// ErrFilterInvalid dikembalikan bila filter periode riwayat invoice tidak
	// bisa dibaca (bukan tanggal YYYY-MM-DD atau rentang terbalik).
	ErrFilterInvalid = errors.New("filter periode tidak valid")
)

// TanggalLayout adalah bentuk tanggal bisnis (WIB) yang dipakai filter dan
// jawaban API riwayat invoice — sama dengan orders.tanggal (BR-04).
const TanggalLayout = "2006-01-02"

// Document adalah satu baris tabel `documents` yang aman dikirim ke klien.
// Kunci storage tidak pernah ikut (json:"-"); unduhan memakai presigned URL.
type Document struct {
	ID         string    `json:"id"`
	OrderID    string    `json:"order_id"`
	Jenis      string    `json:"jenis"`
	Versi      int       `json:"versi"`
	UkuranByte int64     `json:"ukuran_byte"`
	CreatedAt  time.Time `json:"created_at"`
	// StorageKey hanya hidup di dalam proses (repository <-> service).
	StorageKey string `json:"-"`
	// BranchID adalah pemilik pesanan; dipakai menegakkan isolasi cabang.
	BranchID string `json:"-"`
	// OrderNomor adalah nomor pesanan pemilik dokumen; dipakai menamai berkas
	// unduhan. Kosong bila dokumen tidak punya pesanan.
	OrderNomor string `json:"-"`
}

// Download adalah hasil permintaan tautan unduh: URL berumur pendek beserta
// masa berlakunya (detik) dan nama berkas yang dipakai browser.
type Download struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
	NamaFile  string `json:"nama_file"`
	MimeType  string `json:"mime_type"`
}

// InvoiceFilter adalah filter periode riwayat invoice (PRD §3.8): tanggal
// bisnis WIB dalam bentuk YYYY-MM-DD, inklusif di kedua ujungnya. Kosong
// berarti tanpa batas.
type InvoiceFilter struct {
	Dari   string
	Sampai string
}

// InvoiceRow adalah satu baris riwayat invoice cabang (kartu F1-19).
//
// Satu baris = satu DOKUMEN invoice (satu versi). Karena revisi invoice
// menghasilkan dokumen versi baru tanpa menimpa yang lama (F1-17, F2-25),
// versi lama tetap tampil di daftar ini; `Terbaru` menandai baris yang
// versinya sama dengan versi pesanan saat ini.
//
// Pesanan ber-nomor yang dokumennya belum ada (mis. masih Submitted, atau
// render dokumen belum selesai) tetap muncul dengan VersiDokumen nil dan
// DapatDiunduh false — bentuk "invoice belum bisa diunduh" di UI.
//
// Kunci storage tidak pernah ikut: unduhan selalu lewat presigned URL.
type InvoiceRow struct {
	OrderID    string `json:"order_id"`
	Nomor      string `json:"nomor"`
	Tanggal    string `json:"tanggal,omitempty"`
	Status     string `json:"status"`
	Versi      int    `json:"versi"`
	IsCNC      bool   `json:"is_cnc"`
	JatuhTempo string `json:"jatuh_tempo,omitempty"`
	Total      int64  `json:"total"`
	JumlahItem int64  `json:"jumlah_item"`
	// DokumenID adalah dokumen invoice versi baris ini; kosong bila belum ada.
	DokumenID string `json:"dokumen_id,omitempty"`
	// VersiDokumen adalah versi DOKUMEN baris ini; null bila belum ada.
	VersiDokumen *int  `json:"versi_dokumen"`
	UkuranByte   int64 `json:"ukuran_byte"`
	// Terbaru menandai dokumen yang versinya sama dengan versi pesanan.
	Terbaru bool `json:"terbaru"`
	// DapatDiunduh true hanya bila dokumen invoice-nya benar-benar ada dan
	// pesanannya sudah Done — invoice yang belum selesai tidak bisa diunduh.
	DapatDiunduh bool `json:"dapat_diunduh"`
}

// Line adalah satu baris pesanan yang tampil di invoice/surat jalan.
type Line struct {
	LineNo       int
	Jenis        string
	Nama         string
	Satuan       string
	Qty          int
	HargaSatuan  int64
	Subtotal     int64
	SerialNumber string
}

// Rekening adalah satu rekening pembayaran aktif yang dicetak di invoice
// (PRD §6.1: rekening pembayaran).
type Rekening struct {
	Bank     string
	Nomor    string
	AtasNama string
}

// Cabang adalah identitas cabang pemesan.
type Cabang struct {
	Nama    string
	Kepala  string
	Email   string
	Telepon string
}

// Order adalah snapshot pesanan yang dibutuhkan generator dokumen. Isi angka
// dan serial datang apa adanya dari database (sudah di-snapshot saat submit,
// BR-05), jadi dokumen selalu cocok dengan pesanan yang tersimpan.
type Order struct {
	ID           string
	Nomor        string
	Status       string
	Versi        int
	BranchID     string
	IsCNC        bool
	IsSertifikat bool
	Total        int64
	Catatan      string
	// Tanggal adalah tanggal bisnis (WIB) dari kolom `date`; hanya ada setelah
	// submit (BR-04).
	Tanggal    time.Time
	JatuhTempo *time.Time
	Cabang     Cabang
	Lines      []Line
	Rekening   []Rekening
}

// SaveRequest adalah satu unit kerja penyimpanan metadata dokumen: seluruh
// baris (dengan StorageKey yang sudah terisi) ditulis bersama dalam satu
// transaksi repository.
type SaveRequest struct {
	OrderID string
	Docs    []Document
}

// Repository memuat pesanan/dokumen dan menyimpan metadata dokumen.
//
// Penulisan HANYA lewat SaveAll supaya satu permintaan render = satu unit
// kerja atomik dan idempoten (unique (order_id, jenis, versi)).
type Repository interface {
	// OrderForDocument membaca snapshot pesanan lengkap dengan identitas
	// cabang, barisnya, dan rekening pembayaran AKTIF yang berlaku sekarang.
	// ErrNotFound dikembalikan bila pesanannya tidak ada.
	OrderForDocument(ctx context.Context, orderID string) (Order, error)
	// ListByOrder mengembalikan seluruh dokumen satu pesanan (versi lalu
	// jenis).
	ListByOrder(ctx context.Context, orderID string) ([]Document, error)
	// GetByID mengembalikan satu dokumen beserta pemilik pesanannya
	// (BranchID, OrderNomor); ErrNotFound bila tidak ada.
	GetByID(ctx context.Context, id string) (Document, error)
	// SaveAll menyimpan seluruh dokumen satu permintaan dalam SATU transaksi.
	// Baris yang sudah ada TIDAK ditimpa (ON CONFLICT DO NOTHING) dan baris
	// finalnya dikembalikan, sehingga render ulang tidak pernah menggandakan
	// dokumen.
	SaveAll(ctx context.Context, req SaveRequest) ([]Document, error)
	// ListInvoices mengembalikan riwayat invoice SATU cabang: satu baris per
	// dokumen invoice versi berapa pun, ditambah pesanan ber-nomor yang
	// dokumennya belum ada. branchID berasal dari SESI dan wajib dipakai
	// sebagai syarat query; dari/sampai (bila tidak nil) menyaring tanggal
	// bisnis pesanan secara inklusif.
	ListInvoices(ctx context.Context, branchID string, dari, sampai *time.Time) ([]InvoiceRow, error)
}

// Service adalah logika bisnis dokumen pesanan.
type Service struct {
	repo     Repository
	store    storage.Store
	renderer pdf.Renderer
	ttl      time.Duration
}

// Option mengubah perilaku service.
type Option func(*Service)

// WithPresignTTL mengubah umur tautan unduh dokumen.
func WithPresignTTL(d time.Duration) Option { return func(s *Service) { s.ttl = d } }

// NewService membuat service dokumen. store dan renderer tidak boleh nil:
// berkas wajib punya tujuan penyimpanan dan PDF wajib punya mesin render.
func NewService(repo Repository, store storage.Store, renderer pdf.Renderer, opts ...Option) *Service {
	s := &Service{repo: repo, store: store, renderer: renderer, ttl: DefaultPresignTTL}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Ensure memastikan ketiga dokumen versi berjalan (invoice_pdf,
// surat_jalan_xlsx, surat_jalan_pdf) sudah tersedia untuk satu pesanan.
//
// Ini adalah pintu masuk yang dipakai transisi Done (F1-18) dan revisi
// invoice (F2-25); sifatnya idempoten, jadi pemanggil boleh mengulanginya
// dengan aman (mis. worker render dijalankan dua kali).
func (s *Service) Ensure(ctx context.Context, orderID string) ([]Document, error) {
	return s.ensure(ctx, orderID, jenisSaatDone)
}

// EnsureInvoice memastikan dokumen INVOICE versi berjalan sudah tersedia dan
// mengembalikannya. Dipakai revisi invoice (F2-25): setelah versi pesanan
// naik, invoice versi baru dirender tanpa menyentuh berkas versi lama.
func (s *Service) EnsureInvoice(ctx context.Context, orderID string) (Document, error) {
	docs, err := s.ensure(ctx, orderID, []string{JenisInvoicePDF})
	if err != nil {
		return Document{}, err
	}
	for _, d := range docs {
		if d.Jenis == JenisInvoicePDF {
			return d, nil
		}
	}
	return Document{}, ErrNotFound
}

// Generate memastikan ketiga dokumen satu pesanan atas permintaan admin
// (CapAdminOrders) — pintu masuk HTTP untuk membuat/menyegarkan dokumen
// (mis. setelah worker render gagal karena mesin PDF sempat mati).
func (s *Service) Generate(ctx context.Context, actor auth.Actor, orderID string) ([]Document, error) {
	if !actor.Can(auth.CapAdminOrders) {
		return nil, ErrForbidden
	}
	id := strings.TrimSpace(orderID)
	if !uuidRe.MatchString(id) {
		return nil, ErrNotFound
	}
	return s.Ensure(ctx, id)
}

// List mengembalikan dokumen satu pesanan yang boleh dilihat aktor.
//
// Akun cabang hanya melihat INVOICE pesanan cabang SESI-nya (PRD §6.1:
// surat jalan dikirim ke admin Pusat, bukan diunduh cabang); staf Pusat
// dengan CapAdminOrders melihat seluruh dokumen pesanan mana pun. Pesanan
// cabang lain dijawab ErrNotFound.
func (s *Service) List(ctx context.Context, actor auth.Actor, orderID string) ([]Document, error) {
	if err := bolehAktor(actor); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(orderID)
	if !uuidRe.MatchString(id) {
		return nil, ErrNotFound
	}
	order, err := s.repo.OrderForDocument(ctx, id)
	if err != nil {
		return nil, err
	}
	if actor.IsBranch() && order.BranchID != actor.BranchID {
		return nil, ErrNotFound
	}
	rows, err := s.repo.ListByOrder(ctx, id)
	if err != nil {
		return nil, err
	}
	if !actor.IsBranch() {
		return rows, nil
	}
	out := make([]Document, 0, 1)
	for _, d := range rows {
		if d.Jenis == JenisInvoicePDF {
			out = append(out, d)
		}
	}
	return out, nil
}

// Invoices mengembalikan riwayat invoice cabang SESI (kartu F1-19, PRD §3.8):
// nomor, tanggal, versi, total, dan ketersediaan PDF per versi dokumen.
//
// Endpoint ini khusus portal cabang: staf Pusat memakai jalur admin. Akun
// cabang tanpa cabang dijawab ErrNoBranch, dan tidak ada satu pun parameter
// yang bisa menunjuk cabang lain — branch_id selalu dari SESI.
func (s *Service) Invoices(ctx context.Context, actor auth.Actor, f InvoiceFilter) ([]InvoiceRow, error) {
	if !actor.IsBranch() || !actor.Can(auth.CapInvoiceOwn) {
		return nil, ErrForbidden
	}
	branchID := strings.TrimSpace(actor.BranchID)
	if branchID == "" {
		return nil, ErrNoBranch
	}
	dari, sampai, err := periodeInvoice(f)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListInvoices(ctx, branchID, dari, sampai)
	if err != nil {
		return nil, fmt.Errorf("riwayat invoice: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		// Baris tanpa dokumen tidak pernah bisa diunduh; dokumen hanya lahir
		// untuk pesanan Done (PRD §6.1), jadi syarat status di sini bersifat
		// sabuk pengaman terhadap data yang tidak konsisten.
		r.DapatDiunduh = r.DokumenID != "" && r.Status == StatusDone
		r.Terbaru = r.VersiDokumen != nil && *r.VersiDokumen == r.Versi
	}
	if rows == nil {
		rows = []InvoiceRow{}
	}
	return rows, nil
}

// periodeInvoice membaca filter periode menjadi batas tanggal. Nilai kosong
// berarti tanpa batas; rentang terbalik ditolak supaya UI tidak diam-diam
// menampilkan daftar kosong karena kesalahan penulisan.
func periodeInvoice(f InvoiceFilter) (dari, sampai *time.Time, err error) {
	if dari, err = bacaTanggal("dari", f.Dari); err != nil {
		return nil, nil, err
	}
	if sampai, err = bacaTanggal("sampai", f.Sampai); err != nil {
		return nil, nil, err
	}
	if dari != nil && sampai != nil && dari.After(*sampai) {
		return nil, nil, fmt.Errorf("%w: tanggal dari (%s) melewati sampai (%s)",
			ErrFilterInvalid, dari.Format(TanggalLayout), sampai.Format(TanggalLayout))
	}
	return dari, sampai, nil
}

// bacaTanggal membaca satu batas periode (YYYY-MM-DD); kosong berarti nil.
func bacaTanggal(nama, nilai string) (*time.Time, error) {
	nilai = strings.TrimSpace(nilai)
	if nilai == "" {
		return nil, nil
	}
	t, err := time.Parse(TanggalLayout, nilai)
	if err != nil {
		return nil, fmt.Errorf("%w: %s harus berformat YYYY-MM-DD", ErrFilterInvalid, nama)
	}
	return &t, nil
}

// DownloadURL membuat tautan unduh berumur pendek untuk SATU dokumen.
func (s *Service) DownloadURL(ctx context.Context, actor auth.Actor, documentID string) (Download, error) {
	if err := bolehAktor(actor); err != nil {
		return Download{}, err
	}
	id := strings.TrimSpace(documentID)
	if !uuidRe.MatchString(id) {
		return Download{}, ErrNotFound
	}
	doc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return Download{}, err
	}
	if actor.IsBranch() {
		// Surat jalan bukan dokumen unduhan cabang (PRD §6.1): dijawab sama
		// seperti dokumen yang tidak ada supaya jenis dokumen Pusat tidak
		// terbaca dari percobaan unduh.
		if doc.BranchID != actor.BranchID || doc.Jenis != JenisInvoicePDF {
			return Download{}, ErrNotFound
		}
	}
	url, err := s.presign(ctx, doc)
	if err != nil {
		return Download{}, err
	}
	return Download{
		URL:       url,
		ExpiresIn: int(s.ttl.Seconds()),
		NamaFile:  NamaBerkas(doc.Jenis, doc.OrderNomor),
		MimeType:  MimeType(doc.Jenis),
	}, nil
}

// ensure adalah inti generator dokumen: memastikan seluruh `kinds` versi
// berjalan tersedia untuk satu pesanan, tanpa pernah menimpa yang sudah ada.
func (s *Service) ensure(ctx context.Context, orderID string, kinds []string) ([]Document, error) {
	id := strings.TrimSpace(orderID)
	if !uuidRe.MatchString(id) {
		return nil, ErrNotFound
	}
	order, err := s.repo.OrderForDocument(ctx, id)
	if err != nil {
		return nil, err
	}
	if order.Status != StatusDone {
		return nil, fmt.Errorf("%w: pesanan %s berstatus %s", ErrNotDone, order.Nomor, order.Status)
	}
	if err := validSnapshot(order); err != nil {
		return nil, err
	}

	tersimpan, err := s.repo.ListByOrder(ctx, id)
	if err != nil {
		return nil, err
	}
	ada := make(map[string]Document, len(tersimpan))
	for _, d := range tersimpan {
		if d.Versi == order.Versi {
			ada[d.Jenis] = d
		}
	}

	kurang := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if _, ok := ada[k]; !ok {
			kurang = append(kurang, k)
		}
	}

	if len(kurang) > 0 {
		baru, err := s.renderDanSimpan(ctx, order, kurang)
		if err != nil {
			return nil, err
		}
		for _, d := range baru {
			ada[d.Jenis] = d
		}
	}

	out := make([]Document, 0, len(kinds))
	for _, k := range kinds {
		if d, ok := ada[k]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// renderDanSimpan merender seluruh dokumen yang belum ada, menulis isinya ke
// object storage, lalu menyimpan metadatanya dalam satu transaksi.
//
// ATURAN KESELAMATAN (putaran QA-2 → perbaikan putaran 3): jalur ini TIDAK
// PERNAH menghapus objek — baik pada kegagalan maupun pada jalur sukses.
//
// Kunci storage deterministik per (order, jenis, versi) dipakai bersama oleh
// semua permintaan render dokumen yang sama, jadi penghapusan kunci apa pun
// (termasuk "hapus hanya bila tidak ada baris yang merujuk") selalu bertumpu
// pada snapshot rujukan yang bisa basi: pesaing yang sudah menulis objek
// tetapi belum commit akan kehilangan berkasnya begitu barisnya commit, dan
// kehilangan itu PERMANEN karena render ulang menganggap barisnya sudah ada.
// Keterjadiannya bukan teoretis — probe QA putaran 2 mengukur 2/10 percobaan
// kehilangan berkas permanen pada dua render paralel.
//
// Karena baris `documents` HANYA ditulis setelah SEMUA objeknya berhasil
// ditulis, dan objek yang sudah ditulis tidak pernah dihapus, invariannya
// berlaku untuk seluruh penjadwalan:
//
//	bila baris (order, jenis, versi) ada  ⇒  objek storage-nya ada.
//
// Harganya: permintaan yang gagal setelah sebagian objeknya tertulis
// meninggalkan objek YATIM (objek tanpa baris). Objek yatim jauh lebih ringan
// daripada baris tanpa berkas, dan rekonsiliasinya adalah lingkup F1-21 —
// dicatat lewat logObjekTertinggal supaya bisa ditemukan.
func (s *Service) renderDanSimpan(ctx context.Context, order Order, kinds []string) ([]Document, error) {
	isi := make([][]byte, 0, len(kinds))
	for _, k := range kinds {
		b, err := s.render(ctx, order, k)
		if err != nil {
			return nil, err
		}
		isi = append(isi, b)
	}

	kunci := make([]string, 0, len(kinds))
	docs := make([]Document, 0, len(kinds))
	for i, k := range kinds {
		key := StorageKey(order.ID, k, order.Versi)
		if err := s.store.Put(ctx, key, bytes.NewReader(isi[i]), int64(len(isi[i])), MimeType(k)); err != nil {
			logObjekTertinggal(ctx, order, kunci, key)
			return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		kunci = append(kunci, key)
		docs = append(docs, Document{
			OrderID:    order.ID,
			Jenis:      k,
			Versi:      order.Versi,
			StorageKey: key,
			UkuranByte: int64(len(isi[i])),
			BranchID:   order.BranchID,
			OrderNomor: order.Nomor,
		})
	}

	simpan, err := s.repo.SaveAll(ctx, SaveRequest{OrderID: order.ID, Docs: docs})
	if err != nil {
		logObjekTertinggal(ctx, order, kunci, "")
		return nil, err
	}
	return simpan, nil
}

// render mengubah snapshot pesanan menjadi isi berkas dokumen.
func (s *Service) render(ctx context.Context, order Order, jenis string) ([]byte, error) {
	switch jenis {
	case JenisInvoicePDF:
		html, err := invoiceHTML(order)
		if err != nil {
			return nil, err
		}
		return s.toPDF(ctx, html)
	case JenisSuratJalanPDF:
		html, err := suratJalanHTML(order)
		if err != nil {
			return nil, err
		}
		return s.toPDF(ctx, html)
	case JenisSuratJalanXLSX:
		return suratJalanXLSX(order)
	default:
		return nil, fmt.Errorf("%w: jenis dokumen %q tidak dikenal", ErrInvalidData, jenis)
	}
}

// toPDF merender HTML menjadi PDF A4 lewat Gotenberg (SystemDesign.md §8).
func (s *Service) toPDF(ctx context.Context, html []byte) ([]byte, error) {
	out, err := s.renderer.HTMLToPDF(ctx, html)
	if err != nil {
		if errors.Is(err, pdf.ErrNotConfigured) {
			return nil, fmt.Errorf("%w: %v", ErrRenderUnavailable, err)
		}
		return nil, fmt.Errorf("render PDF: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("render PDF: hasil kosong")
	}
	return out, nil
}

// presign membuat tautan unduh; bila storage mendukung paksaan
// Content-Disposition attachment, nama berkas yang benar ikut dibawa.
func (s *Service) presign(ctx context.Context, d Document) (string, error) {
	nama := NamaBerkas(d.Jenis, d.OrderNomor)
	var (
		url string
		err error
	)
	if ds, ok := s.store.(interface {
		PresignDownload(context.Context, string, string, time.Duration) (string, error)
	}); ok {
		url, err = ds.PresignDownload(ctx, d.StorageKey, nama, s.ttl)
	} else {
		url, err = s.store.PresignGet(ctx, d.StorageKey, s.ttl)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
	}
	return url, nil
}

// logObjekTertinggal mencatat objek yang sudah terlanjur ditulis tetapi
// barisnya tidak (atau belum) ada — pendamping ATURAN KESELAMATAN di
// renderDanSimpan: objek sengaja DIBIARKAN karena menghapus kunci
// deterministik selalu berisiko mencabut berkas permintaan lain yang menang
// balapan (dan bila barisnya sudah commit, kerusakannya permanen).
//
// gkey adalah kunci yang Put-nya gagal (boleh kosong); kunci berisi kunci yang
// sudah berhasil ditulis sebelum kegagalan. Objek-objek ini menjadi kandidat
// rekonsiliasi F1-21.
func logObjekTertinggal(ctx context.Context, order Order, kunci []string, gkey string) {
	slog.WarnContext(ctx, "render dokumen gagal: objek yang sudah tertulis DIBIARKAN (bukan dihapus)",
		"order_id", order.ID, "versi", order.Versi,
		"storage_key_tertulis", kunci, "storage_key_put_gagal", gkey)
}

// bolehAktor memastikan aktor berhak menyentuh dokumen sama sekali: akun
// cabang dengan CapInvoiceOwn atau staf Pusat dengan CapAdminOrders.
func bolehAktor(actor auth.Actor) error {
	switch {
	case actor.IsBranch():
		if !actor.Can(auth.CapInvoiceOwn) {
			return ErrForbidden
		}
	case actor.IsAdmin():
		if !actor.Can(auth.CapAdminOrders) {
			return ErrForbidden
		}
	default:
		return ErrForbidden
	}
	return nil
}

// validSnapshot memastikan data pesanan cukup untuk dirender. Pesanan Done
// selalu punya nomor dan tanggal (constraint draft_tanpa_nomor), jadi
// pelanggaran di sini menandakan data rusak, bukan permintaan pengguna.
func validSnapshot(o Order) error {
	switch {
	case strings.TrimSpace(o.Nomor) == "":
		return fmt.Errorf("%w: nomor pesanan kosong", ErrInvalidData)
	case o.Tanggal.IsZero():
		return fmt.Errorf("%w: tanggal pesanan kosong", ErrInvalidData)
	case len(o.Lines) == 0:
		return fmt.Errorf("%w: pesanan tanpa baris", ErrInvalidData)
	}
	return nil
}

// StorageKey menyusun kunci object storage dokumen:
// `documents/{order_id}/{jenis}-{versi}.{ext}` (SystemDesign.md §8).
func StorageKey(orderID, jenis string, versi int) string {
	return DirStorage + "/" + orderID + "/" + jenis + "-" + strconv.Itoa(versi) + "." + ext(jenis)
}

// ext memetakan jenis dokumen ke ekstensi berkasnya.
func ext(jenis string) string {
	if jenis == JenisSuratJalanXLSX {
		return "xlsx"
	}
	return "pdf"
}

// MimeType memetakan jenis dokumen ke MIME type.
func MimeType(jenis string) string {
	if jenis == JenisSuratJalanXLSX {
		return MimeXLSX
	}
	return MimePDF
}

// NamaBerkas menyusun nama berkas unduhan/lampiran email, memakai aturan yang
// SAMA dengan lampiran email domain notification (order_email.go):
// Invoice-{nomor}.pdf, Surat-Jalan-{nomor}.pdf, dan Surat-Jalan-{nomor}.xlsx.
func NamaBerkas(jenis, nomor string) string {
	nomor = strings.TrimSpace(nomor)
	switch jenis {
	case JenisInvoicePDF:
		return "Invoice-" + nomor + ".pdf"
	case JenisSuratJalanPDF:
		return "Surat-Jalan-" + nomor + ".pdf"
	case JenisSuratJalanXLSX:
		return "Surat-Jalan-" + nomor + ".xlsx"
	default:
		return nomor + "-" + jenis
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
