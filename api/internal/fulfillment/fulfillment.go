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
}

// Emailer adalah pengantre email dokumen (notification.Service).
type Emailer interface {
	// EnqueueOrderEmailSekali mengantre email dokumen satu jenis hanya bila
	// jenis itu belum pernah dibuat untuk pesanan tersebut.
	EnqueueOrderEmailSekali(ctx context.Context, in notification.OrderEmailInput) (notification.Email, bool, error)
}

// jenisEmailSaatDone adalah email yang diantre setelah dokumen siap
// (SystemDesign.md §4.4): invoice ke cabang, invoice ke admin ber-flag, dan
// surat jalan ke admin ber-flag. Urutannya tetap supaya hasilnya deterministik.
var jenisEmailSaatDone = []string{
	notification.JenisInvoiceCabang,
	notification.JenisInvoiceAdmin,
	notification.JenisSuratJalanAdmin,
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

	for _, jenis := range jenisEmailSaatDone {
		_, dibuat, err := s.mail.EnqueueOrderEmailSekali(ctx, notification.OrderEmailInput{
			OrderID: id,
			Jenis:   jenis,
		})
		switch {
		case err == nil && dibuat:
			hasil.EmailDibuat = append(hasil.EmailDibuat, jenis)
		case err == nil:
			hasil.EmailSudahAda = append(hasil.EmailSudahAda, jenis)
		case errors.Is(err, notification.ErrNoRecipient):
			// Tidak ada admin ber-flag untuk jenis ini: bukan kegagalan yang
			// bisa diperbaiki dengan mengulang pekerjaan. Dicatat supaya
			// terlihat di log, sisanya tetap dikerjakan.
			hasil.EmailTanpaPenerima = append(hasil.EmailTanpaPenerima, jenis)
			slog.WarnContext(ctx, "email dokumen dilewati: tidak ada penerima yang berhak",
				"order_id", id, "jenis", jenis)
		default:
			return hasil, fmt.Errorf("antre email %s pesanan %s: %w", jenis, id, err)
		}
	}
	return hasil, nil
}
