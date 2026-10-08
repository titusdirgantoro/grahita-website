// Package mailer mengirim email lewat SMTP.
//
// Email transaksional TIDAK PERNAH dikirim langsung dari handler HTTP:
// service menulis baris ke email_outbox, worker yang mengirim
// (SystemDesign.md §5.1). Paket ini hanya alat transportnya.
package mailer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// ErrNotConfigured dikembalikan bila SMTP belum diisi.
var ErrNotConfigured = errors.New("smtp belum dikonfigurasi")

// DefaultTimeout adalah batas waktu TOTAL satu operasi kirim (dial, auth,
// DATA, dan QUIT). Peer SMTP yang diam tidak boleh menggantung job worker
// maupun request atur ulang kata sandi (temuan QA F1-05 untuk F1-16).
const DefaultTimeout = 30 * time.Second

// Attachment adalah satu lampiran biner pada sebuah email.
type Attachment struct {
	// Name adalah nama berkas yang tampil di klien penerima.
	Name string
	// ContentType adalah MIME type lampiran (mis. application/pdf).
	ContentType string
	// Data adalah isi berkas apa adanya (belum di-encode).
	Data []byte
}

// Message adalah satu email yang siap dikirim.
type Message struct {
	To          []string
	Cc          []string
	Subject     string
	Text        string
	HTML        string
	Attachments []Attachment
}

// Mailer mengirim email.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
	Health(ctx context.Context) error
}

// SMTP mengirim email lewat relay SMTP (Mailpit saat lokal, Gmail di produksi).
type SMTP struct {
	host    string
	port    int
	user    string
	pass    string
	from    string
	timeout time.Duration
}

// NewSMTP membuat pengirim SMTP.
func NewSMTP(host string, port int, user, pass, from string) *SMTP {
	return &SMTP{host: host, port: port, user: user, pass: pass, from: from, timeout: DefaultTimeout}
}

// SetTimeout menetapkan batas waktu TOTAL satu operasi kirim. Nilai <= 0
// memakai DefaultTimeout.
func (s *SMTP) SetTimeout(d time.Duration) {
	if d <= 0 {
		d = DefaultTimeout
	}
	s.timeout = d
}

// deadline menghasilkan context DAN tenggat absolut untuk satu operasi kirim.
//
// Deadline yang sudah ada di ctx tidak pernah dilonggarkan: kalau pemanggil
// (mis. job worker) memberi batas lebih ketat, batas itulah yang dipakai.
func (s *SMTP) deadline(ctx context.Context) (context.Context, time.Time, context.CancelFunc) {
	timeout := s.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	bts := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(bts) {
		bts = d
	}
	// context.WithDeadline dipakai (bukan WithTimeout) supaya batas context
	// dan batas koneksi persis sama.
	opCtx, cancel := context.WithDeadline(ctx, bts)
	return opCtx, bts, cancel
}

// Send mengirim satu email; kegagalan SMTP dikembalikan apa adanya supaya
// worker bisa menjadwalkan ulang.
//
// Seluruh operasi tunduk pada batas waktu total: koneksi diberi SetDeadline
// sehingga peer yang diam (tidak menutup koneksi, tidak membalas) tidak
// menggantung pemanggil.
func (s *SMTP) Send(ctx context.Context, msg Message) error {
	if s.host == "" {
		return ErrNotConfigured
	}
	opCtx, batas, batal := s.deadline(ctx)
	defer batal()
	if err := opCtx.Err(); err != nil {
		return fmt.Errorf("smtp dibatalkan sebelum dikirim: %w", err)
	}
	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))

	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.pass, s.host)
	}

	// Konteks pembatalan: dialer dengan timeout mengikuti ctx.
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(opCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// Tenggat mutlak pada soket: berlaku untuk SEMUA operasi berikutnya
	// (handshake, AUTH, MAIL/RCPT, DATA, QUIT) sehingga peer diam berakhir
	// dengan error, bukan menggantung selamanya.
	if err := conn.SetDeadline(batas); err != nil {
		return fmt.Errorf("pasang deadline smtp: %w", err)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer func() { _ = client.Close() }()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, to := range append(append([]string{}, msg.To...), msg.Cc...) {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", to, err)
		}
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := wc.Write(buildRFC822(s.from, msg)); err != nil {
		_ = wc.Close()
		return fmt.Errorf("tulis body email: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("tutup body email: %w", err)
	}
	return client.Quit()
}

// Health memeriksa bahwa port SMTP bisa dihubungi.
func (s *SMTP) Health(ctx context.Context) error {
	if s.host == "" {
		return ErrNotConfigured
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port)))
	if err != nil {
		return err
	}
	return conn.Close()
}

// Log adalah pengirim yang hanya mencatat email — dipakai bila SMTP tidak
// dikonfigurasi (mis. pengujian).
type Log struct{}

// Send mencatat email tanpa mengirimnya.
func (Log) Send(_ context.Context, msg Message) error {
	lampiran := make([]string, 0, len(msg.Attachments))
	for _, a := range msg.Attachments {
		lampiran = append(lampiran, a.Name)
	}
	slog.Info("email (tidak dikirim, SMTP tidak dikonfigurasi)",
		"to", strings.Join(msg.To, ", "), "subject", msg.Subject,
		"lampiran", strings.Join(lampiran, ", "))
	return nil
}

// Health selalu sehat.
func (Log) Health(context.Context) error { return nil }

// envelopeBoundary dan alternativeBoundary sengaja tetap: MIME hanya butuh
// pembatas yang tidak muncul di isi pesan.
const (
	envelopeBoundary    = "grahita-mixed-boundary"
	alternativeBoundary = "grahita-alt-boundary"
)

func buildRFC822(from string, msg Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(msg.To, ", ") + "\r\n")
	if len(msg.Cc) > 0 {
		b.WriteString("Cc: " + strings.Join(msg.Cc, ", ") + "\r\n")
	}
	// Subject di-encode MIME bila mengandung karakter non-ASCII.
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")

	body := alternativeBody(msg)
	if len(msg.Attachments) == 0 {
		b.WriteString(body)
		return []byte(b.String())
	}

	// multipart/mixed: bagian pertama adalah badan (alternative), sisanya
	// lampiran base64.
	b.WriteString("Content-Type: multipart/mixed; boundary=\"" + envelopeBoundary + "\"\r\n\r\n")
	b.WriteString("--" + envelopeBoundary + "\r\n")
	b.WriteString(body)
	for _, a := range msg.Attachments {
		b.WriteString("--" + envelopeBoundary + "\r\n")
		b.WriteString("Content-Type: " + attachmentType(a) + "; name=\"" + a.Name + "\"\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"" + a.Name + "\"\r\n\r\n")
		b.WriteString(wrapBase64(a.Data))
		b.WriteString("\r\n")
	}
	b.WriteString("--" + envelopeBoundary + "--\r\n")
	return []byte(b.String())
}

// alternativeBody membentuk badan text/plain (+HTML bila ada) dengan header
// Content-Type-nya sendiri sehingga bisa dipakai sebagai bagian multipart.
func alternativeBody(msg Message) string {
	var b strings.Builder
	if msg.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n" + msg.Text + "\r\n")
		return b.String()
	}
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + alternativeBoundary + "\"\r\n\r\n")
	b.WriteString("--" + alternativeBoundary + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + msg.Text + "\r\n")
	b.WriteString("--" + alternativeBoundary + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + msg.HTML + "\r\n")
	b.WriteString("--" + alternativeBoundary + "--\r\n")
	return b.String()
}

func attachmentType(a Attachment) string {
	if a.ContentType != "" {
		return a.ContentType
	}
	return "application/octet-stream"
}

// wrapBase64 membungkus isi lampiran pada 76 kolom sesuai RFC 2045.
func wrapBase64(data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}
