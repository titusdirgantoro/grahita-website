package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/grahita/portal/api/internal/platform/mailer"
)

// ErrResetInvalid dikembalikan bila tautan atur ulang tidak dikenali, sudah
// dipakai, atau sudah kedaluwarsa (PRD CP-AUT-04). Pesannya sengaja seragam
// supaya tidak membocorkan status token.
var ErrResetInvalid = errors.New("tautan atur ulang kata sandi tidak valid, sudah dipakai, atau kedaluwarsa")

// ErrWrongPassword dikembalikan bila verifikasi kata sandi lama gagal saat
// ganti kata sandi dari halaman Profil (PRD CP-AUT-06).
var ErrWrongPassword = errors.New("kata sandi lama salah")

// DefaultResetTTL adalah masa berlaku tautan atur ulang bila pengaturan
// `reset_menit` tidak tersedia (PRD §4.9: default 60 menit).
const DefaultResetTTL = 60 * time.Minute

// ResetToken adalah isi baris password_reset_tokens (Database.md §4.2).
//
// Nilai token mentah TIDAK pernah disimpan: database hanya memuat SHA-256-nya,
// dan nilai aslinya hanya ada di tautan yang dikirim lewat email.
type ResetToken struct {
	ID        string
	Kind      Kind
	UserID    string
	ExpiresAt time.Time
}

// ResetStore menyimpan token atur ulang kata sandi.
//
// CreateResetToken wajib membatalkan token aktif sebelumnya milik pengguna
// yang sama sehingga hanya satu tautan yang berlaku pada satu waktu.
// ConsumeResetToken wajib atomik: satu token hanya boleh terpakai sekali
// meski ada dua permintaan bersamaan.
type ResetStore interface {
	CreateResetToken(ctx context.Context, t ResetToken, tokenHash []byte) error
	ConsumeResetToken(ctx context.Context, tokenHash []byte, now time.Time) (ResetToken, error)
}

// ResetMessage adalah data yang dibutuhkan pengirim email tautan atur ulang.
type ResetMessage struct {
	To        string
	Nama      string
	Kind      Kind
	Token     string
	ExpiresAt time.Time
	// TTL adalah masa berlaku yang dikirim ke pengguna. Dipakai untuk
	// mencantumkan "berlaku N menit" tanpa bergantung jam dinding pengirim.
	TTL time.Duration
}

// ResetMailer mengirim tautan atur ulang kata sandi.
//
// Implementasi produksi membungkus mailer.Mailer. Pada tahap ini pengiriman
// dilakukan langsung dari service; pemindahan ke email_outbox + worker
// (SystemDesign.md §5.1) adalah lingkup F1-16.
type ResetMailer interface {
	SendPasswordReset(ctx context.Context, m ResetMessage) error
}

// DefaultResetPath adalah rute halaman atur ulang pada kedua portal.
const DefaultResetPath = "/lupa"

// EmailResetMailer membangun tautan atur ulang lalu mengirimkannya lewat
// mailer.Mailer. Portal tujuan dipilih dari jenis akun: cabang ke portal
// cabang, admin ke portal admin — keduanya memakai rute DefaultResetPath.
type EmailResetMailer struct {
	mail         mailer.Mailer
	path         string
	customerBase string
	adminBase    string
}

// NewEmailResetMailer membuat pengirim tautan atur ulang. path kosong berarti
// DefaultResetPath.
func NewEmailResetMailer(m mailer.Mailer, customerBase, adminBase, path string) *EmailResetMailer {
	if path == "" {
		path = DefaultResetPath
	}
	return &EmailResetMailer{mail: m, path: path, customerBase: customerBase, adminBase: adminBase}
}

// ResetURL menyusun tautan atur ulang untuk satu token.
func (e *EmailResetMailer) ResetURL(kind Kind, token string) string {
	base := e.customerBase
	if kind == KindAdmin {
		base = e.adminBase
	}
	return base + e.path + "?token=" + url.QueryEscape(token)
}

// SendPasswordReset mengirim email berisi tautan atur ulang.
func (e *EmailResetMailer) SendPasswordReset(ctx context.Context, m ResetMessage) error {
	link := e.ResetURL(m.Kind, m.Token)
	berlaku := int(m.TTL.Minutes())
	if berlaku < 1 {
		// TTL tidak dikirim: perkirakan dari waktu kedaluwarsa.
		berlaku = int(time.Until(m.ExpiresAt).Minutes())
	}
	if berlaku < 1 {
		berlaku = 1
	}
	sapaan := m.Nama
	if sapaan == "" {
		sapaan = "Pengguna Grahita"
	}
	text := fmt.Sprintf(
		"Halo %s,\n\n"+
			"Kami menerima permintaan atur ulang kata sandi untuk akun %s.\n"+
			"Buka tautan berikut untuk membuat kata sandi baru (berlaku %d menit):\n\n%s\n\n"+
			"Bila Anda tidak meminta ini, abaikan email ini — kata sandi Anda tidak berubah.\n",
		sapaan, m.To, berlaku, link)
	html := fmt.Sprintf(
		"<p>Halo %s,</p>"+
			"<p>Kami menerima permintaan atur ulang kata sandi untuk akun <strong>%s</strong>.</p>"+
			"<p><a href=\"%s\">Buat kata sandi baru</a> — tautan berlaku %d menit.</p>"+
			"<p>Bila Anda tidak meminta ini, abaikan email ini; kata sandi Anda tidak berubah.</p>",
		sapaan, m.To, link, berlaku)
	return e.mail.Send(ctx, mailer.Message{
		To:      []string{m.To},
		Subject: "Atur ulang kata sandi portal Grahita",
		Text:    text,
		HTML:    html,
	})
}
