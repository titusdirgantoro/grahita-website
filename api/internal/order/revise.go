// Revisi invoice berversi dan audit perubahannya (kartu F2-25; PRD §4.3
// AP-REV-01..05, §3.8, §7, §10; SystemDesign.md §4.5; Database.md §4.6, §8).
//
// Revisi adalah satu-satunya jalur yang boleh mengubah pesanan yang sudah Done
// (BR-07): cabang tidak pernah bisa mengubahnya, dan admin Pusat hanya lewat
// revisi yang WAJIB beralasan.
//
// Aturan yang ditegakkan di lapisan ini — bukan hanya disembunyikan di UI:
//
//   - Hanya staf Pusat ber-kapabilitas `admin.revise`. Manager hanya boleh
//     bila setelan `manager_boleh_revisi` sedang aktif (PRD §4.9); Superadmin
//     selalu boleh. Setelan dibaca SERVER, sehingga mematikan toggle benar-
//     benar menutup jalur ini — bukan sekadar menyembunyikan tombol.
//   - Alasan WAJIB diisi (AP-REV-01) dan dibatasi panjangnya; pesanan yang
//     belum Done dijawab ErrNotDone (dokumen lahir saat Done, PRD §6.1).
//   - Daftar baris yang dikirim adalah DAFTAR FINAL, bukan diff: baris lama
//     yang tidak lagi disebut memang dihapus, dan baris baru disusun server.
//     Qty/harga divalidasi di sini; nama baris baru diambil dari master dan
//     harga Sertifikat SELALU 0 (CP-SER-01), jadi angka dari klien tidak
//     pernah lolos apa adanya untuk produk sertifikat.
//   - Nomor invoice TIDAK berubah; versi naik satu langkah di dalam transaksi
//     yang mengunci baris pesanan, sehingga dua revisi bersamaan berbaris rapi.
//   - Revisi DITOLAK (409, fail closed) selama invoice VERSI BERJALAN belum
//     dirender. Pekerjaan lanjutan merender invoice versi pesanan saat itu,
//     jadi tanpa penjaga ini revisi yang sudah dijawab 200 bisa kehilangan
//     invoice versinya (versi dilewati) — lihat ErrInvoiceVersiBelumAda.
//   - Total TIDAK pernah datang dari klien: repository menghitungnya dari
//     baris final (BR-10, SystemDesign §3).
//
// Penulisan sesungguhnya — snapshot versi lama ke `invoice_revisions` (jsonb),
// penggantian baris, kenaikan versi, timeline, audit, dan penjadwalan render
// invoice versi baru + email — dilakukan repository DI DALAM SATU transaksi
// (SystemDesign.md §4.5).
package order

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/settings"
)

// Sentinel error domain revisi.
var (
	// ErrNotDone dikembalikan bila revisi diminta sebelum pesanan disetujui.
	// Revisi adalah hak atas invoice yang sudah terbit (PRD §6.1, §7.1).
	ErrNotDone = errors.New("revisi hanya untuk pesanan yang disetujui")
	// ErrAlasanWajib dikembalikan bila alasan revisi kosong (AP-REV-01).
	ErrAlasanWajib = fmt.Errorf("%w: alasan revisi wajib diisi", ErrInvalidData)
	// ErrRevisiTidakDiizinkan dikembalikan bila Manager mencoba merevisi
	// sedangkan setelan `manager_boleh_revisi` nonaktif (PRD §4.9).
	ErrRevisiTidakDiizinkan = errors.New("revisi invoice tidak diizinkan untuk peran ini")
	// ErrQueueUnavailable dikembalikan bila penjadwal pekerjaan (River) belum
	// terpasang: transaksi yang menuntut pekerjaan lanjutan — revisi invoice
	// (F2-25) maupun submit pesanan saat AI menyala (F2-26) — DITOLAK alih-alih
	// menyimpan keadaan yang kehilangan pekerjaannya (fail closed).
	ErrQueueUnavailable = errors.New("penjadwal pekerjaan latar belum tersedia")
	// ErrInvoiceVersiBelumAda dikembalikan bila invoice VERSI BERJALAN pesanan
	// belum dirender saat revisi diminta (fail closed, temuan QA putaran 1).
	//
	// Pekerjaan lanjutan revisi merender invoice versi pesanan SAAT ITU, jadi
	// revisi berikutnya hanya boleh menaikkan versi bila versi berjalan sudah
	// punya invoice. Tanpa penjaga ini, dua revisi yang commit sebelum worker
	// menjalankan job pertama membuat invoice versi antara TIDAK pernah
	// dirender walau API sudah menjawab 200 (melanggar AP-REV-03/AP-REV-04).
	ErrInvoiceVersiBelumAda = errors.New("invoice versi berjalan belum tersedia")
	// ErrReversalPlanRequired: total turun di bawah deposit committed tanpa
	// rencana reversal yang cukup (C0 §3.4). Tidak ada reversal otomatis.
	ErrReversalPlanRequired = errors.New("revisi turun membutuhkan reversal plan deposit yang cukup")
	// ErrReversalPlanInvalid: item plan salah order/cabang, nominal/alasan
	// tidak sah, duplikat, atau plan dikirim untuk total yang tidak turun.
	ErrReversalPlanInvalid = errors.New("reversal plan deposit revisi tidak valid")
	// ErrRevisionKeyRequired: plan reversal wajib membawa Idempotency-Key.
	ErrRevisionKeyRequired = errors.New("header Idempotency-Key wajib untuk reversal plan")
)

// DepositReversalInput adalah satu item reversal plan revisi turun.
type DepositReversalInput struct {
	AllocationID string `json:"allocation_id"`
	Amount       int64  `json:"amount"`
	Reason       string `json:"reason"`
}

// Batas panjang teks revisi (kolom text) dan jumlah baris per revisi.
const (
	MaxAlasanRevisi  = 500
	MaxCatatanRevisi = 2000
)

// Teks & ikon timeline revisi.
const (
	IconEventRevisi = "revisi"
)

// ReviseLineInput adalah satu baris pada daftar baris FINAL yang dikirim admin.
//
//   - LineNo > 0 berarti baris lama yang dipertahankan (qty/harga boleh
//     diubah; serial, jenis, dan produknya TIDAK berubah).
//   - LineNo == 0 berarti baris produk BARU; ProductID wajib diisi dan baris
//     itu mendapat serial number baru bertanggal pesanan asli.
type ReviseLineInput struct {
	LineNo       int     `json:"line_no"`
	ProductID    string  `json:"product_id"`
	NamaInstansi *string `json:"nama_instansi,omitempty"`
	Qty          int     `json:"qty"`
	HargaSatuan  int64   `json:"harga_satuan"`
}

// ReviseInput adalah isi permintaan revisi satu pesanan Done.
type ReviseInput struct {
	// Alasan wajib (AP-REV-01) dan tercatat di timeline + audit.
	Alasan string `json:"alasan"`
	// Catatan bebas opsional.
	Catatan string `json:"catatan"`
	// Lines adalah daftar baris FINAL (bukan diff).
	Lines []ReviseLineInput `json:"lines"`
	// DepositReversals adalah reversal plan eksplisit untuk total turun di
	// bawah deposit committed (C0 §5.1); kosong untuk revisi lain.
	DepositReversals []DepositReversalInput `json:"deposit_reversals"`
	// IdempotencyKey berasal dari header, mencakup seluruh plan.
	IdempotencyKey string `json:"-"`
}

// ReviseLine adalah baris final yang sudah divalidasi bentuknya dan siap
// dieksekusi repository (produk/keberadaan diverifikasi di dalam transaksi).
type ReviseLine struct {
	LineNo       int
	ProductID    string
	NamaInstansi *string
	Qty          int
	HargaSatuan  int64
}

// Revision adalah satu baris `invoice_revisions` yang aman dikirim ke klien.
type Revision struct {
	ID         string `json:"id"`
	VersiLama  int    `json:"versi_lama"`
	VersiBaru  int    `json:"versi_baru"`
	Alasan     string `json:"alasan"`
	DeltaTotal int64  `json:"delta_total"`
	AdminNama  string `json:"admin_nama,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// ReviseOutcome adalah hasil satu revisi: keadaan pesanan sesudahnya dan baris
// revisinya. `Order.Versi` = `Revisi.VersiBaru`; nomor invoice tetap.
type ReviseOutcome struct {
	OrderID    string   `json:"order_id"`
	Nomor      string   `json:"nomor"`
	Versi      int      `json:"versi"`
	Total      int64    `json:"total"`
	JumlahItem int64    `json:"jumlah_item"`
	DeltaTotal int64    `json:"delta_total"`
	Lines      []Line   `json:"lines"`
	Revisi     Revision `json:"revisi"`
}

// ReviseRequest adalah seluruh masukan transaksi revisi. Aktor dan entri audit
// ikut supaya repository mencatat pelaku/alasan/delta apa adanya di dalam
// transaksi yang sama.
type ReviseRequest struct {
	OrderID string
	Alasan  string
	Catatan string
	// AdminID & Actor dipakai untuk kolom admin_id, audit, dan timeline.
	AdminID string
	Actor   auth.Actor
	Audit   audit.Entry
	// Lines adalah daftar baris final yang sudah lolos validasi bentuk.
	Lines []ReviseLine
	// BiayaPackaging adalah snapshot setelan `biaya_packaging` saat revisi,
	// dipakai menyusun ulang baris packaging pesanan Sertifikat.
	BiayaPackaging int64
	// TeksEvent adalah kalimat timeline yang ditulis apa adanya.
	TeksEvent string
	// DepositReversals & IdempotencyKey: reversal plan yang sudah divalidasi.
	DepositReversals []DepositReversalInput
	IdempotencyKey   string
}

// RevisionRepository menjalankan satu transaksi revisi lengkap.
type RevisionRepository interface {
	// Revise menegakkan seluruh perubahan revisi dalam SATU transaksi:
	// mengunci baris pesanan, memastikan status Done, menyimpan snapshot versi
	// lama + menaikkan versi, mengganti baris (serial lama dipertahankan, baris
	// baru berserial baru), menghitung ulang total, menulis timeline + audit,
	// dan menitipkan pekerjaan render invoice versi baru + email.
	// ErrNotFound bila pesanan tidak ada; ErrNotDone bila belum Done.
	Revise(ctx context.Context, req ReviseRequest) (ReviseOutcome, error)
}

// RevisionConfig adalah sudut pandang domain pengaturan yang dibutuhkan revisi:
// izin Manager (`manager_boleh_revisi`) dan biaya packaging pesanan Sertifikat.
// Implementasinya adalah settings.Service; nilainya dibaca SERVER.
type RevisionConfig interface {
	Bool(ctx context.Context, key string) (bool, error)
	Int(ctx context.Context, key string) (int64, error)
}

// RevisionService adalah logika bisnis revisi invoice.
type RevisionService struct {
	repo RevisionRepository
	cfg  RevisionConfig
}

// RevisionOption mengubah perilaku RevisionService.
type RevisionOption func(*RevisionService)

// NewRevisionService membuat service revisi invoice. repo dan cfg tidak boleh
// nil: revisi tanpa penyimpanan atau tanpa pembacaan setelan izin tidak dapat
// dilaksanakan dengan aman.
func NewRevisionService(repo RevisionRepository, cfg RevisionConfig, opts ...RevisionOption) *RevisionService {
	s := &RevisionService{repo: repo, cfg: cfg}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Revise merevisi satu pesanan Done: alasan wajib, versi bertambah satu, nomor
// tetap, snapshot versi lama tersimpan, dan invoice versi baru dijadwalkan.
//
// Aturan yang diputuskan di sini (tanpa database): siapa yang berhak
// (capability + setelan), bentuk teks alasan/catatan, dan bentuk daftar baris
// final. Keberadaan produk, aturan Sertifikat, serial baru, total, dan
// penguncian pesanan ditegakkan repository di dalam transaknya.
func (s *RevisionService) Revise(ctx context.Context, actor auth.Actor, orderID string, in ReviseInput) (ReviseOutcome, error) {
	if err := revisiActor(actor); err != nil {
		return ReviseOutcome{}, err
	}
	id := strings.TrimSpace(orderID)
	if !uuidRe.MatchString(id) {
		return ReviseOutcome{}, ErrNotFound
	}
	if s.cfg == nil || s.repo == nil {
		return ReviseOutcome{}, ErrQueueUnavailable
	}

	alasan := strings.TrimSpace(in.Alasan)
	switch {
	case alasan == "":
		return ReviseOutcome{}, ErrAlasanWajib
	case len([]rune(alasan)) > MaxAlasanRevisi:
		return ReviseOutcome{}, fmt.Errorf("%w: alasan revisi maksimal %d karakter", ErrInvalidData, MaxAlasanRevisi)
	}
	catatan := strings.TrimSpace(in.Catatan)
	if len([]rune(catatan)) > MaxCatatanRevisi {
		return ReviseOutcome{}, fmt.Errorf("%w: catatan revisi maksimal %d karakter", ErrInvalidData, MaxCatatanRevisi)
	}

	lines, err := validasiBarisRevisi(in.Lines)
	if err != nil {
		return ReviseOutcome{}, err
	}
	plan, err := validasiReversalPlan(actor, in)
	if err != nil {
		return ReviseOutcome{}, err
	}

	// Izin Manager dibaca SERVER dari setelan (PRD §4.9). Superadmin tidak
	// pernah bergantung pada toggle.
	if !actor.IsSuperadmin() {
		boleh, err := s.cfg.Bool(ctx, settings.ManagerBolehRevisi)
		if err != nil {
			return ReviseOutcome{}, fmt.Errorf("baca setelan %s: %w", settings.ManagerBolehRevisi, err)
		}
		if !boleh {
			return ReviseOutcome{}, ErrRevisiTidakDiizinkan
		}
	}

	biaya, err := s.cfg.Int(ctx, settings.BiayaPackaging)
	if err != nil {
		return ReviseOutcome{}, fmt.Errorf("baca setelan %s: %w", settings.BiayaPackaging, err)
	}
	if biaya < 0 {
		return ReviseOutcome{}, fmt.Errorf("%w: biaya packaging tidak valid", ErrInvalidData)
	}

	req := ReviseRequest{
		OrderID:        id,
		Alasan:         alasan,
		Catatan:        catatan,
		AdminID:        actor.UserID,
		Actor:          actor,
		Lines:          lines,
		BiayaPackaging: biaya,
		TeksEvent:      TeksEventRevisi(actor.Nama, alasan),
		// Reversal plan mengembalikan debit committed: hak admin.verify (C0 §5.0).
		DepositReversals: plan,
		IdempotencyKey:   strings.TrimSpace(in.IdempotencyKey),
	}
	// Audit disusun di sini supaya aktor + IP request tercatat; Before/After
	// diisi repository dari baris yang dikunci (tidak pernah snapshot basi).
	req.Audit = audit.NewEntry(ctx, actor, audit.EntityOrder, id, audit.ActionRevise, nil, nil)
	return s.repo.Revise(ctx, req)
}

// validasiReversalPlan menegakkan bentuk plan; kecukupan dan kepemilikan
// alokasi diperiksa repository di bawah kunci.
func validasiReversalPlan(actor auth.Actor, in ReviseInput) ([]DepositReversalInput, error) {
	if len(in.DepositReversals) == 0 {
		return nil, nil
	}
	if !actor.Can(auth.CapAdminVerify) {
		return nil, ErrForbidden
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, ErrRevisionKeyRequired
	}
	seen := map[string]bool{}
	out := make([]DepositReversalInput, 0, len(in.DepositReversals))
	for _, p := range in.DepositReversals {
		p.AllocationID = strings.ToLower(strings.TrimSpace(p.AllocationID))
		p.Reason = strings.TrimSpace(p.Reason)
		if !uuidRe.MatchString(p.AllocationID) || seen[p.AllocationID] || p.Amount <= 0 ||
			p.Reason == "" || len([]rune(p.Reason)) > MaxAlasanRevisi {
			return nil, ErrReversalPlanInvalid
		}
		seen[p.AllocationID] = true
		out = append(out, p)
	}
	return out, nil
}

// validasiBarisRevisi menegakkan bentuk daftar baris final: minimal satu baris
// produk, qty/harga dalam rentang, tiap baris lama unik, dan tiap baris baru
// menunjuk product_id berbentuk UUID (keberadaannya diperiksa di transaksi).
func validasiBarisRevisi(in []ReviseLineInput) ([]ReviseLine, error) {
	switch {
	case len(in) == 0:
		return nil, ErrEmptyLines
	case len(in) > MaxLines:
		return nil, fmt.Errorf("%w: maksimal %d baris per revisi", ErrInvalidData, MaxLines)
	}
	out := make([]ReviseLine, 0, len(in))
	dipakai := map[int]bool{}
	for i, l := range in {
		if l.Qty < 1 {
			return nil, fmt.Errorf("%w: qty baris %d minimal 1", ErrInvalidData, i+1)
		}
		if l.Qty > maxQty {
			return nil, fmt.Errorf("%w: qty baris %d maksimal %d", ErrInvalidData, i+1, maxQty)
		}
		if l.HargaSatuan < 0 {
			return nil, fmt.Errorf("%w: harga baris %d tidak boleh negatif", ErrInvalidData, i+1)
		}
		pid := strings.TrimSpace(l.ProductID)
		if l.NamaInstansi != nil {
			instansi := strings.TrimSpace(*l.NamaInstansi)
			if instansi == "" || len([]rune(instansi)) > MaxNamaInstansi {
				return nil, fmt.Errorf("%w: nama instansi baris ke-%d wajib diisi, maksimal %d karakter", ErrInvalidData, i+1, MaxNamaInstansi)
			}
			l.NamaInstansi = &instansi
		}
		if l.LineNo < 0 {
			return nil, fmt.Errorf("%w: line_no baris ke-%d tidak boleh negatif", ErrInvalidData, i+1)
		}
		if l.LineNo > 0 {
			if dipakai[l.LineNo] {
				return nil, fmt.Errorf("%w: baris %d disebut lebih dari sekali", ErrInvalidData, l.LineNo)
			}
			dipakai[l.LineNo] = true
		} else if pid == "" {
			return nil, fmt.Errorf("%w: baris baru ke-%d belum memilih produk", ErrInvalidData, i+1)
		} else if !uuidRe.MatchString(pid) {
			return nil, fmt.Errorf("%w: product_id baris baru ke-%d bukan UUID", ErrInvalidData, i+1)
		} else if l.NamaInstansi == nil {
			return nil, fmt.Errorf("%w: nama instansi baris baru ke-%d wajib diisi", ErrInvalidData, i+1)
		}
		out = append(out, ReviseLine{LineNo: l.LineNo, ProductID: pid, NamaInstansi: l.NamaInstansi, Qty: l.Qty, HargaSatuan: l.HargaSatuan})
	}
	return out, nil
}

// revisiActor menegakkan PRD §2.1: hanya staf Pusat ber-kapabilitas
// `admin.revise` (Manager maupun Superadmin) yang boleh menyentuh revisi;
// akun cabang dijawab ErrForbidden.
func revisiActor(actor auth.Actor) error {
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminRevise) {
		return ErrForbidden
	}
	return nil
}

// TeksEventRevisi menyusun kalimat timeline saat invoice direvisi.
func TeksEventRevisi(nama, alasan string) string {
	return "Invoice direvisi oleh " + namaRevisi(nama) + " — alasan: " + strings.TrimSpace(alasan)
}

// namaRevisi memakai nama admin apa adanya; akun tanpa nama memakai sebutan
// netral supaya timeline tidak kosong.
func namaRevisi(nama string) string {
	nama = strings.TrimSpace(nama)
	if nama == "" {
		return "admin Pusat"
	}
	return nama
}
