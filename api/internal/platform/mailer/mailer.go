// Package mailer mengirim email lewat SMTP.
//
// Email transaksional TIDAK PERNAH dikirim langsung dari handler HTTP:
// service menulis baris ke email_outbox, worker yang mengirim
// (SystemDesign.md §5.1). Paket ini hanya alat transportnya.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// ErrNotConfigured dikembalikan bila SMTP belum diisi.
var ErrNotConfigured = errors.New("smtp belum dikonfigurasi")

// Message adalah satu email yang siap dikirim.
type Message struct {
	To      []string
	Cc      []string
	Subject string
	Text    string
	HTML    string
}

// Mailer mengirim email.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
	Health(ctx context.Context) error
}

// SMTP mengirim email lewat relay SMTP (Mailpit saat lokal, Gmail di produksi).
type SMTP struct {
	host string
	port int
	user string
	pass string
	from string
}

// NewSMTP membuat pengirim SMTP.
func NewSMTP(host string, port int, user, pass, from string) *SMTP {
	return &SMTP{host: host, port: port, user: user, pass: pass, from: from}
}

// Send mengirim satu email; kegagalan SMTP dikembalikan apa adanya supaya
// worker bisa menjadwalkan ulang.
func (s *SMTP) Send(ctx context.Context, msg Message) error {
	if s.host == "" {
		return ErrNotConfigured
	}
	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))

	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.pass, s.host)
	}

	// Konteks pembatalan: dialer dengan timeout mengikuti ctx.
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
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
	slog.Info("email (tidak dikirim, SMTP tidak dikonfigurasi)",
		"to", strings.Join(msg.To, ","), "subject", msg.Subject)
	return nil
}

// Health selalu sehat.
func (Log) Health(context.Context) error { return nil }

func buildRFC822(from string, msg Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(msg.To, ", ") + "\r\n")
	if len(msg.Cc) > 0 {
		b.WriteString("Cc: " + strings.Join(msg.Cc, ", ") + "\r\n")
	}
	b.WriteString("Subject: " + msg.Subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	if msg.HTML != "" {
		boundary := "grahita-boundary"
		b.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + msg.Text + "\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + msg.HTML + "\r\n")
		b.WriteString("--" + boundary + "--\r\n")
	} else {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n" + msg.Text + "\r\n")
	}
	return []byte(b.String())
}
