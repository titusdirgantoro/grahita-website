package notification

// Pengingat pembayaran Cash & Carry terjadwal (F2-23, SystemDesign.md §4.6,
// PRD CP-CNC-05/§6.2).
//
// Pekerjaan River "reminder_cnc" berjalan setiap hari pukul 07:00 WIB,
// memilih pesanan `is_cnc = true AND status <> 'done'` yang punya jatuh tempo,
// lalu mengantre email pengingat ke email CABANG pada H-2, H-0, H+1, dan H+3
// relatif jatuh tempo (Database.md §4.7: tabel `cnc_reminders (order_id,
// tahap)`).
//
// Aturan yang ditegakkan di sini:
//
//   - EMAIL TIDAK PERNAH dikirim langsung: pengingat menulis baris ke
//     email_outbox dan worker ProsesAntrean yang mengirim (SystemDesign.md
//     §5.1). Karena itu kegagalan kirim TIDAK menghilangkan pengingat — baris
//     outbox dijadwalkan ulang dengan backoff berjenjang, dan baris `sent` baru
//     lahir setelah SMTP benar-benar menerima pesan.
//   - Sekali per (pesanan, tahap): baris cnc_reminders ditulis DI DALAM
//     transaksi yang sama dengan baris email_outbox (transactional outbox) dan
//     dijaga PRIMARY KEY (order_id, tahap). Job yang dijalankan ulang — River
//     bersifat at-least-once, dan penjadwal bisa memicu saat start — tidak
//     pernah menggandakan email tahap yang sama.
//   - Bila penulisan gagal (mis. database menolak), TIDAK ada baris
//     cnc_reminders yang lahir, sehingga tahap itu dicoba lagi pada pemindaian
//     berikutnya. Sebaliknya, bila emailnya gagal TERKIRIM, baris cnc_reminders
//     tetap ada (tahap tidak diulang) dan pengiriman diserahkan ke outbox.
//   - Pesanan `done` tidak pernah diingatkan (difilter di query kandidat), jadi
//     pesanan yang lunas sebelum tahap berikutnya tidak diingatkan lagi.
//   - C&C yang lewat jatuh tempo TIDAK diubah menjadi Done otomatis di sini:
//     status hanya berubah setelah admin memverifikasi pembayaran (F1-18,
//     PRD CP-CNC-07). Pengingat murni menambah baris email + audit.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
)

// Tahap pengingat (Database.md §4.7: kolom cnc_reminders.tahap).
const (
	TahapReminderH2 = "h-2"
	TahapReminderH0 = "h-0"
	TahapReminderP1 = "h+1"
	TahapReminderP3 = "h+3"
)

// AktorSistemCNC adalah actor_id entri audit pengingat C&C: pekerjaan latar
// bukan tindakan manusia, jadi actor_kind 'system' dengan UUID nol — konvensi
// yang sama dengan backup.AktorSistem (F1-21).
const AktorSistemCNC = "00000000-0000-0000-0000-000000000000"

// NamaAktorSistemCNC adalah actor_nama entri audit pengingat C&C.
const NamaAktorSistemCNC = "pengingat-cnc"

// KandidatCNC adalah satu pesanan C&C yang perlu dipertimbangkan pengingatnya.
type KandidatCNC struct {
	// OrderID adalah id pesanan (uuid).
	OrderID string
	// Nomor adalah nomor pesanan/invoice.
	Nomor string
	// Cabang adalah nama cabang pemilik pesanan.
	Cabang string
	// NamaKepala adalah nama kepala cabang (sapaan email).
	NamaKepala string
	// CabangEmail adalah alamat email cabang — satu-satunya penerima pengingat
	// (PRD §6.2: "Reminder Cash & Carry — Email cabang").
	CabangEmail string
	// Total adalah total pesanan dalam Rupiah bulat.
	Total int64
	// JatuhTempo adalah tanggal jatuh tempo (tanggal bisnis WIB).
	JatuhTempo time.Time
}

// ReminderStore menyimpan pengingat C&C: membaca kandidat dan menulis baris
// cnc_reminders + email_outbox + audit dalam satu transaksi.
//
// Interface ini dipisah dari Store supaya paket lain tidak ikut menanggung
// kewajiban pengingat; implementasinya adalah notification/postgres.Repo.
type ReminderStore interface {
	// KandidatCNC mengembalikan pesanan C&C yang belum Done dan punya jatuh
	// tempo.
	KandidatCNC(ctx context.Context) ([]KandidatCNC, error)
	// AntreReminderCNC menyimpan baris cnc_reminders (order_id, tahap) DAN
	// baris email_outbox DAN entri audit dalam SATU transaksi. Bool kedua
	// bernilai true bila baris reminder baru dibuat; false berarti tahap itu
	// sudah pernah diantre (tidak digandakan).
	AntreReminderCNC(ctx context.Context, orderID, tahap string, e Envelope) (bool, error)
}

// HasilReminder merangkum satu pemindaian pengingat C&C (dipakai log & uji).
type HasilReminder struct {
	// Diperiksa adalah jumlah kandidat yang dibaca.
	Diperiksa int
	// Diantre adalah jumlah pengingat baru yang diantre ke outbox.
	Diantre int
	// Dilewati adalah jumlah kandidat yang tahapnya hari itu tidak jatuh atau
	// sudah pernah diantre.
	Dilewati int
	// Gagal adalah jumlah kandidat yang penulisannya gagal (dicoba lagi pada
	// pemindaian berikutnya).
	Gagal int
}

// WithReminderStore memasang penyimpanan pengingat C&C (cnc_reminders +
// email_outbox). Tanpa ini, JalankanReminderCNC menolak berjalan.
func WithReminderStore(r ReminderStore) Option {
	return func(s *Service) {
		if r != nil {
			s.reminder = r
		}
	}
}

// tanggalWIB memotong waktu ke tengah malam tanggal bisnis WIB.
func tanggalWIB(t time.Time) time.Time {
	l := t.In(clock.Jakarta())
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, clock.Jakarta())
}

// TahapReminder mengembalikan tahap pengingat yang jatuh pada tanggal bisnis
// `hari` untuk pesanan berjatuh tempo `jatuhTempo`, beserta true bila ada.
//
// Perbandingan dilakukan pada TANGGAL bisnis WIB (bukan jam), sehingga pemicu
// jam 07:00 WIB tetap mengirim walau jam server memakai UTC: `hari` dan
// `jatuhTempo` dinormalkan lebih dulu ke tengah malam WIB. Jakarta tidak punya
// DST (selalu UTC+7), jadi selisih hari selalu bulat.
func TahapReminder(hari, jatuhTempo time.Time) (string, bool) {
	selisih := int(tanggalWIB(hari).Sub(tanggalWIB(jatuhTempo)).Hours() / 24)
	switch selisih {
	case -2:
		return TahapReminderH2, true
	case 0:
		return TahapReminderH0, true
	case 1:
		return TahapReminderP1, true
	case 3:
		return TahapReminderP3, true
	default:
		return "", false
	}
}

// labelTahap adalah teks tahap yang tercetak di badan email.
func labelTahap(tahap string) string { return strings.ToUpper(tahap) }

// envelopeReminder menyusun email pengingat untuk satu kandidat + tahap.
func envelopeReminder(k KandidatCNC, tahap string) (Envelope, error) {
	to := NormalizeAddress([]string{k.CabangEmail})
	if len(to) == 0 {
		return Envelope{}, ErrNoRecipient
	}
	return Envelope{
		OrderID: k.OrderID,
		Jenis:   JenisReminderCNC,
		To:      to,
		Subject: fmt.Sprintf("Pengingat pembayaran C&C %s — %s", k.Nomor, labelTahap(tahap)),
		Payload: map[string]any{
			"template":    JenisReminderCNC,
			"nomor":       k.Nomor,
			"cabang":      k.Cabang,
			"nama":        k.NamaKepala,
			"tahap":       labelTahap(tahap),
			"jatuh_tempo": tanggalWIB(k.JatuhTempo).Format("2006-01-02"),
			"total":       k.Total,
		},
	}, nil
}

// JalankanReminderCNC menjalankan satu pemindaian pengingat C&C.
//
// Ia membaca kandidat, lalu untuk setiap kandidat yang tahapnya jatuh HARI INI
// (WIB) mengantre email pengingat ke cabang lewat email_outbox. Penulisan
// baris cnc_reminders + email_outbox + audit dilakukan atomik oleh repository,
// jadi pemindaian ulang pada hari yang sama tidak menggandakan email.
//
// Kegagalan satu kandidat tidak membatalkan sisanya (kandidat lain tetap
// diproses) dan tidak dikembalikan sebagai error job: pekerjaan terlewat,
// bukan gagal total. Tahap yang gagal ditulis akan dicoba lagi pada pemindaian
// berikutnya karena barisnya belum ada.
func (s *Service) JalankanReminderCNC(ctx context.Context) (HasilReminder, error) {
	var hasil HasilReminder
	if s.reminder == nil {
		return hasil, fmt.Errorf("%w: penyimpanan pengingat C&C belum terpasang", ErrInvalidData)
	}
	kandidat, err := s.reminder.KandidatCNC(ctx)
	if err != nil {
		return hasil, fmt.Errorf("baca kandidat pengingat C&C: %w", err)
	}

	hari := clock.TodayWIB(s.clk)
	for _, k := range kandidat {
		hasil.Diperiksa++
		tahap, ok := TahapReminder(hari, k.JatuhTempo)
		if !ok {
			hasil.Dilewati++
			continue
		}
		env, err := envelopeReminder(k, tahap)
		if err != nil {
			hasil.Gagal++
			slog.WarnContext(ctx, "pengingat C&C dilewati: email tidak dapat disusun",
				"order_id", k.OrderID, "tahap", tahap, "err", err)
			continue
		}
		dibuat, err := s.reminder.AntreReminderCNC(ctx, k.OrderID, tahap, env)
		if err != nil {
			hasil.Gagal++
			slog.ErrorContext(ctx, "antre pengingat C&C gagal",
				"order_id", k.OrderID, "tahap", tahap, "err", err)
			continue
		}
		if !dibuat {
			hasil.Dilewati++
			continue
		}
		hasil.Diantre++
	}
	return hasil, nil
}
