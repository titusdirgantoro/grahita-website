package order

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/settings"
)

// Berkas ini memuat submit pesanan: transisi Draft -> Submitted dengan
// penomoran transaksional (kartu F1-14; SystemDesign.md §4.2, §5.2–5.3;
// Database.md §4.4, §6; PRD §3.5–3.7, §7).
//
// Pembagian tanggung jawab:
//
//   - lapisan ini menegakkan aturan yang bisa diputuskan tanpa database
//     (siapa aktornya, apakah cabangnya Cash & Carry, tanggal bisnis WIB,
//     bentuk snapshot final baris) dan membaca setelan server (biaya
//     packaging, digit_invoice);
//   - repository Submit menjalankan SATU transaksi yang mengunci baris
//     pesanan, membaca ULANG harga cabang dan keadaan produk dari master
//     (BR-05), mengambil nomor invoice/serial dari tabel counter, menulis
//     snapshot + serial + status + tanggal + total + timeline, dan untuk
//     cabang C&C mendebit saldo deposit pada transaksi yang sama.
//
// Semua keputusan uang/penomoran karena itu diambil di dalam transaksi:
// nomor tidak pernah terbuang saat rollback, urutan nomor tidak bolong, dan
// dua submit bersamaan tidak bisa memakai nomor yang sama (BR-09).

// TeksEventSubmit adalah kalimat timeline saat pesanan dikirim
// (PRD CP-ORD-08, SystemDesign §4.2).
const TeksEventSubmit = "Pesanan dikirim oleh cabang"

// TeksEventDeposit adalah kalimat timeline saat submit Cash & Carry mendebit
// saldo deposit cabang.
func TeksEventDeposit(potong int64) string {
	return "Dibayar dari saldo deposit Rp " + ribuan(potong)
}

// ribuan menulis bilangan bulat dengan pemisah ribuan titik ("150.000").
func ribuan(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 0 {
		return "-" + ribuan(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
}

// ErrProofRequired dikembalikan bila bagian pesanan yang dibayar transfer
// dikirim tanpa satu pun bukti: seluruh pesanan non-C&C, atau sisa pesanan C&C
// yang tidak tertutup saldo deposit (PRD CP-ORD-04, CP-CNC-02).
var ErrProofRequired = fmt.Errorf("%w: pesanan wajib disertai minimal satu bukti transfer", ErrInvalidData)

// SubmitInput adalah pilihan yang datang dari klien saat submit. Harga, biaya,
// total, dan status Cash & Carry TIDAK pernah dari klien (SystemDesign §3,
// BR-10).
type SubmitInput struct {
	// ExpectedJournalVersion adalah versi saldo deposit yang dilihat cabang
	// C&C saat menekan Kirim (GET /deposit); wajib untuk cabang C&C dan
	// diabaikan untuk cabang lain. Bila saldo sudah berubah, submit ditolak
	// ErrDepositChanged.
	ExpectedJournalVersion *int64
}

// BranchFlags membaca flag `cash_and_carry` cabang. Implementasinya adalah
// branch.Service; nilainya dibaca SERVER dari master cabang sehingga cabang
// tidak bisa memilih sendiri jalur pembayaran deposit (PRD CP-CNC-01).
type BranchFlags interface {
	IsCNC(ctx context.Context, branchID string) (bool, error)
}

// TempoMode is implemented by the branch service. The repository reads the
// same settings under a row lock before committing the submit snapshot.
type TempoMode interface {
	PaymentMode(ctx context.Context, branchID string) (branch.Branch, error)
}

type PaymentAccountSource interface {
	PaymentAvailable(ctx context.Context, branchID string) (bool, error)
}

// Option mengubah perilaku Service pesanan.
type Option func(*Service)

// AISettingsSource membaca setelan AI yang sedang berlaku (PRD §4.9). Dipakai
// submit untuk memutuskan apakah pesanan yang baru dikirim harus menitipkan
// pekerjaan pembacaan bukti (kartu F2-26, SystemDesign.md §4.2).
// Implementasinya adalah settings.Service.
type AISettingsSource interface {
	AISettings(ctx context.Context) (settings.AISettings, error)
}

// WithAISettings memasang sumber setelan AI. Tanpa ini, AI dianggap mati:
// submit tidak pernah menitipkan pekerjaan pembacaan bukti.
func WithAISettings(a AISettingsSource) Option { return func(s *Service) { s.ai = a } }

// WithBranchFlags memasang sumber flag cabang (C&C). Tanpa ini, setiap
// cabang diperlakukan non-C&C (wajib bukti transfer): flag yang tidak bisa
// diverifikasi tidak boleh membuka jalur deposit.
func WithBranchFlags(f BranchFlags) Option { return func(s *Service) { s.flags = f } }

func WithPaymentAccounts(b PaymentAccountSource) Option { return func(s *Service) { s.banks = b } }

// WithClock memasang sumber waktu (SystemDesign §5.5). Bawaannya clock.System.
func WithClock(c clock.Clock) Option {
	return func(s *Service) {
		if c != nil {
			s.clk = c
		}
	}
}

// SubmitRequest adalah seluruh masukan transaksi submit. Tanggal dihitung
// service dari waktu submit (WIB) supaya perilaku dekat tengah malam dapat diuji
// deterministik; harga, serial, nomor, dan potongan deposit dihitung repository
// di dalam transaksi.
type SubmitRequest struct {
	ID       string
	BranchID string
	// ActorID adalah akun cabang pelaku submit (dicatat di timeline).
	ActorID string
	// IsCNC adalah flag cabang yang dibaca server: pesanan dibayar dari saldo
	// deposit sebanyak PotonganDeposit (CP-CNC-02).
	IsCNC        bool
	IsTempo      bool
	TanggalBayar int
	// ExpectedJournalVersion adalah versi saldo deposit yang dilihat cabang;
	// hanya berarti bila IsCNC.
	ExpectedJournalVersion int64
	// Tanggal adalah tanggal bisnis submit (tengah malam WIB, BR-04/BR-11).
	Tanggal time.Time
	// BiayaPackaging adalah snapshot setelan `biaya_packaging` saat submit,
	// dipakai menyegarkan baris packaging pesanan Sertifikat (CP-SER-02/05).
	BiayaPackaging int64
	// DigitInvoice adalah setelan `digit_invoice` saat submit (BR-03).
	DigitInvoice int
	// EnqueueOCR meminta repository menitipkan pekerjaan pembacaan bukti
	// AI/OCR pada transaksi submit yang sama (kartu F2-26, SystemDesign.md
	// §4.2). Diisi service HANYA ketika `ai.aktif = true`.
	EnqueueOCR bool
}

// SubmitLine adalah satu baris draft beserta keadaan master dan harga cabang
// pada saat submit dibaca. Repository menyusunnya di dalam transaksi.
type SubmitLine struct {
	LineNo int
	Jenis  string
	Qty    int

	// NamaLama adalah snapshot yang tersimpan di draft; dipakai sebagai
	// cadangan bila teks master kosong.
	NamaLama string

	// Keadaan master produk (hanya berarti untuk baris jenis "product").
	ProdukAda   bool
	ProdukAktif bool
	ProdukNama  string

	ProdukJenis    string
	HargaCabang    int64
	AdaHargaCabang bool
}

// SnapshotSubmit adalah nilai final satu baris yang ditulis saat submit.
type SnapshotSubmit struct {
	LineNo int
	Nama   string

	HargaSatuan int64
}

// SiapkanSubmit menegakkan aturan isi pesanan SAAT SUBMIT dan menghitung
// snapshot final setiap baris (BR-05):
//
//   - produk yang sudah tidak aktif, tidak dijual ke cabang ini (tanpa baris
//     branch_prices), atau hilang ditolak — harga_default bukan cadangan
//     (PRD CP-KAT-02);
//   - harga baris diambil ULANG dari master/harga cabang saat submit, bukan
//     dari angka yang tersimpan di draft, sehingga perubahan harga sesudah
//     draft disimpan ikut terpakai;
//   - Sertifikat selalu berharga 0 berapa pun qty-nya (CP-SER-01) dan tidak
//     boleh dicampur produk reguler (CP-SER-03);
//   - pesanan Sertifikat wajib punya TEPAT SATU baris packaging, dan baris itu
//     disegarkan ke setelan `biaya_packaging` yang berlaku saat submit
//     (CP-SER-02/05). Pesanan non-Sertifikat tidak boleh punya baris
//     packaging.
//
// Baris packaging tidak pernah mendapat serial number (Database.md §6);
// pemberian serial dan nomor invoice dilakukan repository di dalam transaksi.
func SiapkanSubmit(lines []SubmitLine, biayaPackaging int64) ([]SnapshotSubmit, error) {
	jumlah := len(lines)
	if jumlah == 0 {
		return nil, ErrEmptyLines
	}
	if biayaPackaging < 0 {
		return nil, fmt.Errorf("%w: biaya packaging tidak valid", ErrInvalidData)
	}

	out := make([]SnapshotSubmit, 0, jumlah)
	var produk, sertifikat, reguler, packaging int
	for i, l := range lines {
		switch l.Jenis {
		case JenisPackaging:
			packaging++
			if l.Qty != 1 {
				return nil, fmt.Errorf("%w: baris packaging harus berjumlah 1", ErrInvalidData)
			}
			if packaging > 1 {
				return nil, fmt.Errorf("%w: pesanan hanya boleh punya satu baris packaging", ErrInvalidData)
			}
			out = append(out, SnapshotSubmit{
				LineNo: l.LineNo,
				// Nama baris biaya packaging adalah teks baku server.
				Nama:        NamaBiayaPackaging,
				HargaSatuan: biayaPackaging,
			})
		case JenisProduct:
			produk++
			if l.Qty < 1 {
				return nil, fmt.Errorf("%w: qty baris %d minimal 1", ErrInvalidData, i+1)
			}
			if !l.ProdukAda || !l.ProdukAktif || !l.AdaHargaCabang {
				return nil, fmt.Errorf("%w: baris %d", ErrProductNotSold, i+1)
			}
			harga := l.HargaCabang
			if l.ProdukJenis == catalog.JenisSertifikat {
				harga = 0
				sertifikat++
			} else {
				reguler++
			}
			nama := l.ProdukNama
			if nama == "" {
				nama = l.NamaLama
			}

			out = append(out, SnapshotSubmit{
				LineNo: l.LineNo,
				Nama:   nama,

				HargaSatuan: harga,
			})
		default:
			return nil, fmt.Errorf("%w: jenis baris %q tidak dikenal", ErrInvalidData, l.Jenis)
		}
	}

	if produk == 0 {
		return nil, ErrEmptyLines
	}
	if sertifikat > 0 && reguler > 0 {
		return nil, fmt.Errorf("%w: Sertifikat dipesan terpisah dan tidak boleh dicampur produk reguler", ErrInvalidData)
	}
	if sertifikat > 0 {
		if packaging != 1 {
			return nil, fmt.Errorf("%w: pesanan Sertifikat wajib punya tepat satu baris biaya packaging", ErrInvalidData)
		}
	} else if packaging > 0 {
		return nil, fmt.Errorf("%w: baris biaya packaging hanya untuk pesanan Sertifikat", ErrInvalidData)
	}
	return out, nil
}

// Submit mengirim satu draft milik cabang sesi: status berpindah ke Submitted,
// nomor invoice dan serial number terbit, pesanan terkunci (PRD CP-ORD-05,
// CP-ORD-09, BR-07).
//
// Aturan yang diputuskan di sini:
//
//   - hanya akun cabang pemilik pesanan (branch_id SESI) yang boleh submit;
//   - pesanan cabang berflag cash_and_carry (dibaca server; tanpa sumber flag
//     dianggap non-C&C) dibayar dari saldo deposit DI DALAM transaksi submit,
//     dan cabang wajib menyebut versi saldo deposit yang ia lihat;
//   - bagian yang dibayar transfer (seluruh pesanan non-C&C, atau sisa C&C
//     setelah deposit) wajib punya minimal satu bukti, diperiksa repository
//     DI DALAM transaksi submit supaya tidak ada celah balapan antara
//     pembacaan bukti/saldo dan penguncian pesanan;
//   - tanggal pesanan = tanggal submit WIB (BR-04), dihitung dari clock yang
//     disuntikkan.
//
// Submit ulang pada pesanan yang sudah Submitted/Done dijawab ErrLocked → 409
// ORDER_LOCKED; tidak ada pembatalan pesanan Submitted.
func (s *Service) Submit(ctx context.Context, actor auth.Actor, id string, in SubmitInput) (Order, error) {
	branchID, err := branchActor(actor)
	if err != nil {
		return Order{}, err
	}
	id = strings.TrimSpace(id)
	if !uuidRe.MatchString(id) {
		return Order{}, ErrNotFound
	}

	// Flag cabang dibaca server (bukan dari klien); bila sumbernya tidak
	// tersedia, cabang diperlakukan non-C&C (fail closed: wajib bukti).
	var cabangCNC bool
	var cabangTempo bool
	var tanggalBayar int
	if s.flags != nil {
		if modes, ok := s.flags.(TempoMode); ok {
			var b branch.Branch
			b, err = modes.PaymentMode(ctx, branchID)
			cabangCNC, cabangTempo, tanggalBayar = b.IsCNC, b.IsTempo, b.TanggalBayar
		} else {
			cabangCNC, err = s.flags.IsCNC(ctx, branchID)
		}
		if err != nil {
			return Order{}, fmt.Errorf("baca flag Cash & Carry cabang: %w", err)
		}
	}
	var expectedJournal int64
	if cabangCNC {
		if in.ExpectedJournalVersion == nil || *in.ExpectedJournalVersion < 0 {
			return Order{}, ErrDepositVersionRequired
		}
		expectedJournal = *in.ExpectedJournalVersion
	}
	// Pra-cek rekening hanya untuk cabang non-C&C. Pesanan C&C baru butuh
	// rekening bila deposit tidak menutup totalnya; itu diputuskan repository
	// setelah total final diketahui.
	if !cabangCNC && !cabangTempo && s.banks != nil {
		available, err := s.banks.PaymentAvailable(ctx, branchID)
		if err != nil {
			return Order{}, fmt.Errorf("baca rekening pembayaran cabang: %w", err)
		}
		if !available {
			return Order{}, bank.ErrUnavailable
		}
	}

	// Setelan dibaca pada saat submit: nilai final yang di-snapshot tidak
	// pernah berasal dari draft maupun dari klien (CP-SER-05, BR-03).
	biaya, err := s.cfg.Int(ctx, settings.BiayaPackaging)
	if err != nil {
		return Order{}, fmt.Errorf("baca setelan %s: %w", settings.BiayaPackaging, err)
	}
	if biaya < 0 {
		return Order{}, fmt.Errorf("%w: biaya packaging tidak valid", ErrInvalidData)
	}
	digit, err := s.cfg.Int(ctx, settings.DigitInvoice)
	if err != nil {
		return Order{}, fmt.Errorf("baca setelan %s: %w", settings.DigitInvoice, err)
	}
	if digit < 1 {
		digit = 1
	}

	tanggal := TanggalWIB(s.clk.Now())
	req := SubmitRequest{
		ID:                     id,
		BranchID:               branchID,
		ActorID:                actor.UserID,
		IsCNC:                  cabangCNC,
		IsTempo:                cabangTempo,
		TanggalBayar:           tanggalBayar,
		ExpectedJournalVersion: expectedJournal,
		Tanggal:                tanggal,
		BiayaPackaging:         biaya,
		DigitInvoice:           int(digit),
	}

	// Setelan AI dibaca pada saat submit: pesanan yang dikirim dengan AI
	// menyala mendapat pekerjaan pembacaan bukti pada transaksi yang sama
	// (F2-26, SystemDesign.md §4.2). Tanpa sumber setelan, AI dianggap mati.
	if s.ai != nil {
		ai, err := s.ai.AISettings(ctx)
		if err != nil {
			return Order{}, fmt.Errorf("baca setelan %s: %w", settings.AI, err)
		}
		req.EnqueueOCR = ai.Aktif && !cabangTempo
	}
	return s.repo.Submit(ctx, req)
}
