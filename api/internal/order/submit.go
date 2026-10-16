package order

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
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
//     (siapa aktornya, C&C sah atau tidak, tanggal bisnis WIB, tenggat,
//     bentuk snapshot final baris) dan membaca setelan server (biaya
//     packaging, cnc_hari, digit_invoice);
//   - repository Submit menjalankan SATU transaksi yang mengunci baris
//     pesanan, membaca ULANG harga cabang dan keadaan produk dari master
//     (BR-05), mengambil nomor invoice/serial dari tabel counter, menulis
//     snapshot + serial + status + tanggal + tenggat + total + timeline.
//
// Semua keputusan uang/penomoran karena itu diambil di dalam transaksi:
// nomor tidak pernah terbuang saat rollback, urutan nomor tidak bolong, dan
// dua submit bersamaan tidak bisa memakai nomor yang sama (BR-09).

// TeksEventSubmit adalah kalimat timeline saat pesanan dikirim
// (PRD CP-ORD-08, SystemDesign §4.2).
const TeksEventSubmit = "Pesanan dikirim oleh cabang"

// ErrProofRequired dikembalikan bila pesanan BUKAN Cash & Carry dikirim tanpa
// satu pun bukti transfer (PRD CP-ORD-04: submit non-C&C wajib ≥1 bukti).
var ErrProofRequired = fmt.Errorf("%w: pesanan wajib disertai minimal satu bukti transfer", ErrInvalidData)

// SubmitInput adalah pilihan yang datang dari klien saat submit. Hanya penanda
// C&C yang diterima; harga, biaya, dan total TIDAK pernah dari klien
// (SystemDesign §3, BR-10).
type SubmitInput struct {
	IsCNC bool
}

// BranchFlags membaca flag `cash_and_carry` cabang. Implementasinya adalah
// branch.Service; nilainya dibaca SERVER dari master cabang sehingga cabang
// tanpa flag tidak bisa memalsukan C&C (PRD CP-CNC-01, CP-CNC-02).
type BranchFlags interface {
	IsCNC(ctx context.Context, branchID string) (bool, error)
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

// WithBranchFlags memasang sumber flag cabang (C&C). Tanpa ini, permintaan
// C&C SELALU ditolak (fail closed): flag yang tidak bisa diverifikasi tidak
// boleh dianggap benar.
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

// SubmitRequest adalah seluruh masukan transaksi submit. Tanggal dan JatuhTempo
// dihitung service dari waktu submit (WIB) supaya perilaku dekat tengah malam
// dapat diuji deterministik; harga, serial, dan nomor dihitung repository di
// dalam transaksi.
type SubmitRequest struct {
	ID       string
	BranchID string
	// ActorID adalah akun cabang pelaku submit (dicatat di timeline).
	ActorID string
	IsCNC   bool
	// Tanggal adalah tanggal bisnis submit (tengah malam WIB, BR-04/BR-11).
	Tanggal time.Time
	// JatuhTempo hanya terisi bila IsCNC (CP-CNC-04); zero berarti NULL.
	JatuhTempo time.Time
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
//   - C&C hanya sah bila cabang SESI benar-benar berflag cash_and_carry
//     (ValidateCNC); tanpa sumber flag, C&C ditolak;
//   - pesanan non-C&C wajib punya minimal satu bukti transfer, diperiksa
//     repository DI DALAM transaksi submit (bukan di sini) supaya tidak ada
//     celah balapan antara pembacaan bukti dan penguncian pesanan;
//   - tanggal pesanan = tanggal submit WIB dan tenggat C&C = tanggal + cnc_hari
//     (BR-04, BR-08, CP-CNC-04), dihitung dari clock yang disuntikkan.
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
	// tersedia, C&C ditolak alih-alih dipercaya (fail closed).
	var cabangCNC bool
	if s.flags != nil {
		cabangCNC, err = s.flags.IsCNC(ctx, branchID)
		if err != nil {
			return Order{}, fmt.Errorf("baca flag Cash & Carry cabang: %w", err)
		}
	}
	if err := ValidateCNC(cabangCNC, in.IsCNC); err != nil {
		return Order{}, err
	}
	if !in.IsCNC && s.banks != nil {
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
		ID:             id,
		BranchID:       branchID,
		ActorID:        actor.UserID,
		IsCNC:          in.IsCNC,
		Tanggal:        tanggal,
		BiayaPackaging: biaya,
		DigitInvoice:   int(digit),
	}

	// Setelan AI dibaca pada saat submit: pesanan yang dikirim dengan AI
	// menyala mendapat pekerjaan pembacaan bukti pada transaksi yang sama
	// (F2-26, SystemDesign.md §4.2). Tanpa sumber setelan, AI dianggap mati.
	if s.ai != nil {
		ai, err := s.ai.AISettings(ctx)
		if err != nil {
			return Order{}, fmt.Errorf("baca setelan %s: %w", settings.AI, err)
		}
		req.EnqueueOCR = ai.Aktif
	}
	if in.IsCNC {
		cncHari, err := s.cfg.Int(ctx, settings.CNCHari)
		if err != nil {
			return Order{}, fmt.Errorf("baca setelan %s: %w", settings.CNCHari, err)
		}
		if cncHari < 0 {
			return Order{}, fmt.Errorf("%w: cnc_hari tidak valid", ErrInvalidData)
		}
		req.JatuhTempo = JatuhTempo(tanggal, int(cncHari))
	}

	return s.repo.Submit(ctx, req)
}
