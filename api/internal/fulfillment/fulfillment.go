// Package fulfillment menjalankan pekerjaan lanjutan setelah pesanan berstatus
// Done: render Invoice PDF + Surat Jalan XLSX/PDF, LALU mengantre email dokumen
// (kartu F1-18; SystemDesign.md §4.4, PRD §6.1–6.2).
//
// Urutannya penting dan disengaja: email dokumen hanya boleh masuk antrean
// SETELAH dokumennya benar-benar ada, sebab lampirannya dibaca dari object
// storage saat pengiriman (domain email menolak mengantre email dokumen yang
// belum dirender — `notification.ErrDocumentNotReady`). Inilah "worker menunggu
// dokumen sebelum email": pekerjaan ini dijalankan worker, dan bila mesin PDF
// atau object storage belum siap, pekerjaan gagal dan diulang River nanti —
// bukan melahirkan email tanpa lampiran.
//
// Seluruh langkahnya IDEMPOTEN karena job River bersifat at-least-once:
//
//   - render dokumen memakai kunci unik (order_id, jenis, versi) — menjalankan
//     ulang tidak menyentuh berkas yang sudah ada;
//   - pengantrean email memakai jalur "sekali per (pesanan, jenis)" sehingga
//     pekerjaan yang diulang tidak menggandakan email dokumen pesanan yang sama.
//
// Paket ini sengaja tipis: keputusan bisnis (status, log, audit, penjadwalan)
// ada di domain verifikasi, dan pembuatan dokumen ada di domain dokumen.
package fulfillment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/notification"
)

// Documents adalah pembuat dokumen pesanan (document.Service).
type Documents interface {
	// Ensure memastikan Invoice PDF + Surat Jalan XLSX/PDF versi berjalan ada.
	// Idempoten dan MENOLAK pesanan yang belum Done (document.ErrNotDone).
	Ensure(ctx context.Context, orderID string) ([]document.Document, error)
	// EnsureInvoice memastikan dokumen INVOICE versi berjalan ada dan
	// mengembalikannya. Dipakai revisi (F2-25): versi pesanan sudah naik,
	// sehingga invoice versi baru dirender TANPA menyentuh berkas versi lama.
	EnsureInvoice(ctx context.Context, orderID string) (document.Document, error)
}

// Emailer adalah pengantre email dokumen (notification.Service).
type Emailer interface {
	// EnqueueOrderEmailSekali mengantre email dokumen satu jenis hanya bila
	// jenis itu belum pernah dibuat untuk pesanan tersebut.
	EnqueueOrderEmailSekali(ctx context.Context, in notification.OrderEmailInput) (notification.Email, bool, error)
	// EnqueueOrderEmailSekaliVersi mengantre email dokumen satu jenis hanya
	// bila (pesanan, jenis, versi) itu belum pernah dibuat — dipakai revisi
	// agar tiap versi baru boleh mendapat email baru (AP-REV-04) sementara
	// pekerjaan worker yang diulang tidak menggandakannya (F2-25).
	EnqueueOrderEmailSekaliVersi(ctx context.Context, in notification.OrderEmailInput) (notification.Email, bool, error)
}

// jenisEmailSaatDone adalah email yang diantre setelah dokumen siap
// (SystemDesign.md §4.4): invoice ke cabang, invoice ke admin ber-flag, dan
// surat jalan ke admin ber-flag. Urutannya tetap supaya hasilnya deterministik.
var jenisEmailSaatDone = []string{
	notification.JenisInvoiceCabang,
	notification.JenisInvoiceAdmin,
	notification.JenisSuratJalanAdmin,
}

// jenisEmailSaatRevisi adalah email yang diantre setelah invoice versi baru
// tersedia (SystemDesign.md §4.5, PRD AP-REV-04): HANYA invoice ke cabang dan
// invoice ke admin ber-flag. Surat Jalan TIDAK dikirim ulang — revisi hanya
// menyentuh invoice (§6.2).
var jenisEmailSaatRevisi = []string{
	notification.JenisInvoiceCabang,
	notification.JenisInvoiceAdmin,
}

// Hasil merangkum satu penyelesaian pesanan (dipakai log & pengujian).
type Hasil struct {
	// Dokumen adalah jumlah dokumen versi berjalan yang tersedia sesudahnya.
	Dokumen int
	// EmailDibuat berisi jenis email yang BARU diantre pada pemanggilan ini.
	EmailDibuat []string
	// EmailSudahAda berisi jenis email yang sudah ada sebelumnya (tidak
	// digandakan — pekerjaan yang diulang).
	EmailSudahAda []string
	// EmailTanpaPenerima berisi jenis email yang tidak punya penerima berhak
	// (mis. belum ada admin ber-flag). Bukan kegagalan: tidak ada yang HARUS
	// menerima email itu sekarang.
	EmailTanpaPenerima []string
}

// Service menjalankan efek samping Done.
type Service struct {
	docs Documents
	mail Emailer
}

// NewService membuat service pemenuhan pesanan. docs dan mail tidak boleh nil;
// pemanggil yang tidak melengkapinya akan gagal terang-terangan saat dipakai.
func NewService(docs Documents, mail Emailer) *Service {
	return &Service{docs: docs, mail: mail}
}

// PesananSelesai menjalankan rantai Done → dokumen → email untuk satu pesanan.
//
// Kegagalan render/penyimpanan dikembalikan apa adanya supaya worker mengulang
// pekerjaan nanti (dokumen tetap tidak ada, jadi email belum boleh dibuat).
// Ketiadaan penerima yang berhak BUKAN kegagalan: jenis itu dilewati dan
// dilaporkan, sisanya tetap diproses.
func (s *Service) PesananSelesai(ctx context.Context, orderID string) (Hasil, error) {
	var hasil Hasil
	id := strings.TrimSpace(orderID)
	if id == "" {
		return hasil, fmt.Errorf("pesanan tanpa id tidak bisa diselesaikan")
	}
	if s.docs == nil || s.mail == nil {
		return hasil, fmt.Errorf("layanan dokumen/email belum terpasang")
	}

	dokumen, err := s.docs.Ensure(ctx, id)
	if err != nil {
		return hasil, fmt.Errorf("render dokumen pesanan %s: %w", id, err)
	}
	hasil.Dokumen = len(dokumen)
	// Dokumen harus benar-benar ada sebelum email diantre: bila Ensure tidak
	// mengembalikan berkas (mis. versi dokumen belum lengkap), email ditahan.
	if len(dokumen) == 0 {
		return hasil, fmt.Errorf("dokumen pesanan %s belum tersedia", id)
	}
	if err := s.antreEmail(ctx, id, jenisEmailSaatDone, false, &hasil); err != nil {
		return hasil, err
	}
	return hasil, nil
}

// PesananDirevisi menjalankan rantai revisi → invoice versi baru → email
// invoice untuk satu pesanan (SystemDesign.md §4.5, PRD AP-REV-04).
//
// Berbeda dari PesananSelesai, jalur ini:
//
//   - hanya memastikan dokumen INVOICE versi berjalan ada; Surat Jalan tidak
//     disentuh dan tidak dikirim ulang — revisi hanya menyentuh invoice (§6.2);
//   - mengantre HANYA email invoice (cabang + admin ber-flag) dan dijaga per
//     (pesanan, jenis, versi), sehingga revisi berikutnya tetap mendapat email
//     baru sementara pekerjaan yang dijalankan ulang tidak menggandakannya.
//
// Kegagalan render dikembalikan apa adanya supaya worker mengulang nanti;
// ketiadaan penerima yang berhak bukan kegagalan.
func (s *Service) PesananDirevisi(ctx context.Context, orderID string) (Hasil, error) {
	var hasil Hasil
	id := strings.TrimSpace(orderID)
	if id == "" {
		return hasil, fmt.Errorf("pesanan tanpa id tidak bisa direvisi")
	}
	if s.docs == nil || s.mail == nil {
		return hasil, fmt.Errorf("layanan dokumen/email belum terpasang")
	}

	doc, err := s.docs.EnsureInvoice(ctx, id)
	if err != nil {
		return hasil, fmt.Errorf("render invoice pesanan %s: %w", id, err)
	}
	if strings.TrimSpace(doc.ID) == "" {
		return hasil, fmt.Errorf("invoice pesanan %s belum tersedia", id)
	}
	hasil.Dokumen = 1
	if err := s.antreEmail(ctx, id, jenisEmailSaatRevisi, true, &hasil); err != nil {
		return hasil, err
	}
	return hasil, nil
}

// antreEmail mengantre sekumpulan email dokumen satu pesanan dan memilah
// hasilnya (baru / sudah ada / tanpa penerima) ke dalam hasil.
//
// perVersi=true memakai penjaga (pesanan, jenis, VERSI) — jalur revisi;
// perVersi=false memakai penjaga (pesanan, jenis) — jalur Done pertama.
func (s *Service) antreEmail(ctx context.Context, id string, jenis []string, perVersi bool, hasil *Hasil) error {
	for _, j := range jenis {
		in := notification.OrderEmailInput{OrderID: id, Jenis: j}
		var (
			dibuat bool
			err    error
		)
		if perVersi {
			_, dibuat, err = s.mail.EnqueueOrderEmailSekaliVersi(ctx, in)
		} else {
			_, dibuat, err = s.mail.EnqueueOrderEmailSekali(ctx, in)
		}
		switch {
		case err == nil && dibuat:
			hasil.EmailDibuat = append(hasil.EmailDibuat, j)
		case err == nil:
			hasil.EmailSudahAda = append(hasil.EmailSudahAda, j)
		case errors.Is(err, notification.ErrNoRecipient):
			// Tidak ada admin ber-flag untuk jenis ini: bukan kegagalan yang
			// bisa diperbaiki dengan mengulang pekerjaan. Dicatat supaya
			// terlihat di log, sisanya tetap dikerjakan.
			hasil.EmailTanpaPenerima = append(hasil.EmailTanpaPenerima, j)
			slog.WarnContext(ctx, "email dokumen dilewati: tidak ada penerima yang berhak",
				"order_id", id, "jenis", j)
		default:
			return fmt.Errorf("antre email %s pesanan %s: %w", j, id, err)
		}
	}
	return nil
}
