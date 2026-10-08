package notification

import (
	"context"
	"fmt"
	"strings"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/users"
)

// ResetMailer adalah jembatan kontrak auth.ResetMailer ke email_outbox.
//
// Sejak F1-16 tidak ada lagi jalur email yang dikirim langsung dari service:
// permintaan lupa kata sandi (maupun kirim ulang tautan oleh admin) menulis
// baris outbox jenis `reset_password`, dan worker yang mengirimnya. Kontrak
// keamanan handler tetap: respons lupa SELALU sama dan non-enumeratif.
type ResetMailer struct {
	svc   *Service
	build auth.ResetURLBuilder
}

// NewResetMailer membuat jembatan tautan atur ulang -> outbox.
func NewResetMailer(svc *Service, customerBase, adminBase, path string) *ResetMailer {
	return &ResetMailer{svc: svc, build: auth.NewResetURLBuilder(customerBase, adminBase, path)}
}

// ResetURL menyusun tautan atur ulang untuk satu token (dipakai pengujian dan
// pemanggil yang hanya butuh tautannya).
func (m *ResetMailer) ResetURL(kind auth.Kind, token string) string {
	return m.build(kind, token)
}

// SendPasswordReset mengantre email tautan atur ulang ke outbox.
func (m *ResetMailer) SendPasswordReset(ctx context.Context, msg auth.ResetMessage) error {
	berlaku := int(msg.TTL.Minutes())
	if berlaku < 1 {
		berlaku = 1
	}
	_, err := m.svc.Enqueue(ctx, Envelope{
		Jenis:   JenisResetPassword,
		To:      []string{msg.To},
		Subject: DefaultSubject(JenisResetPassword),
		Payload: map[string]any{
			"template":      JenisResetPassword,
			"nama":          msg.Nama,
			"email":         msg.To,
			"tautan":        m.build(msg.Kind, msg.Token),
			"berlaku_menit": berlaku,
		},
	})
	if err != nil {
		return fmt.Errorf("antre email atur ulang: %w", err)
	}
	return nil
}

// Queue adalah jembatan kontrak users.EmailQueue ke email_outbox: pengelolaan
// pengguna (undangan admin, kirim ulang undangan) hanya menulis outbox.
type Queue struct {
	svc *Service
}

// NewQueue membuat jembatan antrean email pengelolaan pengguna.
func NewQueue(svc *Service) *Queue { return &Queue{svc: svc} }

// Enqueue mengantre satu email pengelolaan pengguna.
func (q *Queue) Enqueue(ctx context.Context, m users.QueuedEmail) error {
	jenis := strings.TrimSpace(string(m.Jenis))
	if jenis == "" {
		jenis = JenisUndanganAdmin
	}
	_, err := q.svc.Enqueue(ctx, Envelope{
		Jenis:   jenis,
		To:      m.To,
		Subject: m.Subject,
		Payload: m.Payload,
	})
	if err != nil {
		return fmt.Errorf("antre email %s: %w", jenis, err)
	}
	return nil
}
