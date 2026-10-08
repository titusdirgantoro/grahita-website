package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/platform/mailer"
)

// TemplateName mengembalikan nama template sebuah email: payload["template"]
// bila ada, kalau tidak jenis emailnya sendiri.
func TemplateName(e Email) string {
	if tpl, ok := e.Payload["template"].(string); ok && strings.TrimSpace(tpl) != "" {
		return strings.TrimSpace(tpl)
	}
	return e.Jenis
}

// DefaultSubject mengembalikan subjek baku untuk satu jenis email.
func DefaultSubject(jenis string) string {
	switch jenis {
	case JenisResetPassword:
		return "Atur ulang kata sandi portal Grahita"
	case JenisUndanganAdmin:
		return "Undangan akses Admin Portal Grahita Indonesia"
	case JenisInvoiceCabang:
		return "Invoice pesanan Anda — Grahita Indonesia"
	case JenisInvoiceAdmin:
		return "Invoice cabang — Grahita Indonesia"
	case JenisSuratJalanAdmin:
		return "Surat Jalan — Grahita Indonesia"
	case JenisReminderCNC:
		return "Pengingat pembayaran pesanan Cash & Carry"
	default:
		return "Grahita Indonesia"
	}
}

// Render menyusun pesan siap kirim (subjek, teks, HTML, lampiran) dari satu
// baris outbox. Lampiran dibaca dari object storage memakai payload["lampiran"].
func (s *Service) Render(ctx context.Context, e Email) (mailer.Message, error) {
	text, html, err := renderTemplate(TemplateName(e), e.Payload)
	if err != nil {
		return mailer.Message{}, err
	}
	lampiran, err := s.attachments(ctx, e)
	if err != nil {
		return mailer.Message{}, err
	}
	subjek := strings.TrimSpace(e.Subject)
	if subjek == "" {
		subjek = DefaultSubject(e.Jenis)
	}
	return mailer.Message{
		To:          e.To,
		Cc:          e.Cc,
		Subject:     subjek,
		Text:        text,
		HTML:        html,
		Attachments: lampiran,
	}, nil
}

// renderTemplate menghasilkan badan text + HTML untuk satu template.
//
// Template ditulis apa adanya (tanpa mesin template) supaya isi email dapat
// diuji persis dan tidak bisa menyisipkan HTML dari data pengguna tanpa
// escaping eksplisit.
func renderTemplate(nama string, payload map[string]any) (string, string, error) {
	switch nama {
	case JenisResetPassword:
		return tplResetPassword(payload), tplResetPasswordHTML(payload), nil
	case JenisUndanganAdmin:
		return tplUndanganAdmin(payload), tplUndanganAdminHTML(payload), nil
	case JenisInvoiceCabang:
		return tplInvoice(payload, true), tplInvoiceHTML(payload, true), nil
	case JenisInvoiceAdmin:
		return tplInvoice(payload, false), tplInvoiceHTML(payload, false), nil
	case JenisSuratJalanAdmin:
		return tplSuratJalan(payload), tplSuratJalanHTML(payload), nil
	case JenisReminderCNC:
		return tplReminder(payload), tplReminderHTML(payload), nil
	default:
		return "", "", fmt.Errorf("%w: template %q", ErrKindInvalid, nama)
	}
}

// attachments membaca seluruh lampiran payload dari object storage.
func (s *Service) attachments(ctx context.Context, e Email) ([]mailer.Attachment, error) {
	daftar, err := PayloadAttachments(e.Payload)
	if err != nil {
		return nil, err
	}
	if len(daftar) == 0 {
		return nil, nil
	}
	if s.obj == nil {
		return nil, fmt.Errorf("%w: object storage belum terpasang", ErrAttachmentUnavailable)
	}
	out := make([]mailer.Attachment, 0, len(daftar))
	for _, l := range daftar {
		if l.StorageKey == "" {
			return nil, fmt.Errorf("%w: lampiran tanpa storage_key", ErrInvalidData)
		}
		rc, err := s.obj.Get(ctx, l.StorageKey)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrAttachmentUnavailable, l.Nama, err)
		}
		isi, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrAttachmentUnavailable, l.Nama, err)
		}
		nama := l.Nama
		if nama == "" {
			nama = "lampiran"
		}
		out = append(out, mailer.Attachment{Name: nama, ContentType: l.ContentType, Data: isi})
	}
	return out, nil
}

// PayloadAttachments mengambil daftar lampiran dari payload. Nilai payload
// berasal dari jsonb (setelah perjalanan JSON) maupun dari map Go langsung,
// jadi keduanya diterima.
func PayloadAttachments(payload map[string]any) ([]Lampiran, error) {
	raw, ok := payload["lampiran"]
	if !ok || raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: lampiran tidak dapat dibaca", ErrInvalidData)
	}
	var out []Lampiran
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%w: lampiran bukan daftar objek", ErrInvalidData)
	}
	return out, nil
}

// ---------- helper payload ----------

func str(payload map[string]any, key string) string {
	if v, ok := payload[key].(string); ok {
		return v
	}
	if v, ok := payload[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func num(payload map[string]any, key string) int64 {
	switch v := payload[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func berkas(payload map[string]any, key string) string {
	if v, ok := payload[key].(time.Time); ok {
		return v.In(time.FixedZone("WIB", 7*60*60)).Format("02 Jan 2006 15:04 WIB")
	}
	return str(payload, key)
}

// tanggalID memformat waktu sebagai tanggal Indonesia (mis. 08 Okt 2026).
func tanggalID(t time.Time) string {
	bulan := [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
	return fmt.Sprintf("%02d %s %04d", t.Day(), bulan[int(t.Month())-1], t.Year())
}

// rupiah memformat angka Rupiah bulat tanpa sen (BR-10).
func rupiah(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := "Rp" + b.String()
	if neg {
		return "-" + out
	}
	return out
}

func sapaan(payload map[string]any) string {
	nama := strings.TrimSpace(str(payload, "nama"))
	if nama == "" {
		return "Pengguna Grahita"
	}
	return nama
}

// ---------- template: reset_password ----------

func tplResetPassword(p map[string]any) string {
	return fmt.Sprintf(
		"Halo %s,\n\n"+
			"Kami menerima permintaan atur ulang kata sandi untuk akun %s.\n"+
			"Buka tautan berikut untuk membuat kata sandi baru (berlaku %d menit):\n\n%s\n\n"+
			"Bila Anda tidak meminta ini, abaikan email ini — kata sandi Anda tidak berubah.\n",
		sapaan(p), str(p, "email"), num(p, "berlaku_menit"), str(p, "tautan"))
}

func tplResetPasswordHTML(p map[string]any) string {
	return fmt.Sprintf(
		"<p>Halo %s,</p>"+
			"<p>Kami menerima permintaan atur ulang kata sandi untuk akun <strong>%s</strong>.</p>"+
			"<p><a href=\"%s\">Buat kata sandi baru</a> — tautan berlaku %d menit.</p>"+
			"<p>Bila Anda tidak meminta ini, abaikan email ini; kata sandi Anda tidak berubah.</p>",
		sapaan(p), str(p, "email"), str(p, "tautan"), num(p, "berlaku_menit"))
}

// ---------- template: undangan_admin ----------

func tplUndanganAdmin(p map[string]any) string {
	catatan := str(p, "catatan")
	if catatan == "" {
		catatan = "Buat kata sandi lewat tautan ini; akun baru aktif setelah kata sandi dibuat."
	}
	return fmt.Sprintf(
		"Halo %s,\n\n"+
			"Anda diundang sebagai %s di Admin Portal Grahita Indonesia (%s).\n"+
			"Buka tautan berikut untuk membuat kata sandi Anda:\n\n%s\n\n"+
			"%s\nTautan berlaku sampai %s.\n",
		sapaan(p), str(p, "role"), str(p, "email"), str(p, "tautan"), catatan, berkas(p, "berlaku_sampai"))
}

func tplUndanganAdminHTML(p map[string]any) string {
	catatan := str(p, "catatan")
	if catatan == "" {
		catatan = "Buat kata sandi lewat tautan ini; akun baru aktif setelah kata sandi dibuat."
	}
	return fmt.Sprintf(
		"<p>Halo %s,</p>"+
			"<p>Anda diundang sebagai <strong>%s</strong> di Admin Portal Grahita Indonesia (%s).</p>"+
			"<p><a href=\"%s\">Buat kata sandi Anda</a></p>"+
			"<p>%s Tautan berlaku sampai %s.</p>",
		sapaan(p), str(p, "role"), str(p, "email"), str(p, "tautan"), catatan, berkas(p, "berlaku_sampai"))
}

// ---------- template: invoice (cabang & admin) ----------

func tplInvoice(p map[string]any, keCabang bool) string {
	if !keCabang {
		return fmt.Sprintf(
			"Halo Tim Pusat,\n\n"+
				"Invoice pesanan %s dari cabang %s telah diterbitkan%s.\n"+
				"Total pesanan: %s.\n\n"+
				"Berkas invoice kami lampirkan pada email ini.\n",
			str(p, "nomor"), str(p, "cabang"), versiTeks(p), rupiah(num(p, "total")))
	}
	return fmt.Sprintf(
		"Halo %s,\n\n"+
			"Invoice pesanan %s dari cabang %s telah diterbitkan%s.\n"+
			"Total pesanan: %s.\n\n"+
			"Berkas invoice kami lampirkan pada email ini.\n",
		sapaan(p), str(p, "nomor"), str(p, "cabang"), versiTeks(p), rupiah(num(p, "total")))
}

func tplInvoiceHTML(p map[string]any, keCabang bool) string {
	if !keCabang {
		return fmt.Sprintf(
			"<p>Halo Tim Pusat,</p>"+
				"<p>Invoice pesanan <strong>%s</strong> dari cabang <strong>%s</strong> telah diterbitkan%s.</p>"+
				"<p>Total pesanan: <strong>%s</strong>.</p>"+
				"<p>Berkas invoice kami lampirkan pada email ini.</p>",
			str(p, "nomor"), str(p, "cabang"), versiTeks(p), rupiah(num(p, "total")))
	}
	return fmt.Sprintf(
		"<p>Halo %s,</p>"+
			"<p>Invoice pesanan <strong>%s</strong> dari cabang <strong>%s</strong> telah diterbitkan%s.</p>"+
			"<p>Total pesanan: <strong>%s</strong>.</p>"+
			"<p>Berkas invoice kami lampirkan pada email ini.</p>",
		sapaan(p), str(p, "nomor"), str(p, "cabang"), versiTeks(p), rupiah(num(p, "total")))
}

func versiTeks(p map[string]any) string {
	if v := num(p, "versi"); v > 0 {
		return fmt.Sprintf(" (revisi versi %d)", v)
	}
	return ""
}

// ---------- template: surat_jalan_admin ----------

func tplSuratJalan(p map[string]any) string {
	return fmt.Sprintf(
		"Halo Tim Pusat,\n\n"+
			"Surat Jalan untuk pesanan %s dari cabang %s sudah siap (tanggal pesanan %s).\n"+
			"Total pesanan: %s.\n\n"+
			"Berkas Surat Jalan (XLSX dan PDF) kami lampirkan pada email ini.\n",
		str(p, "nomor"), str(p, "cabang"), str(p, "tanggal"), rupiah(num(p, "total")))
}

func tplSuratJalanHTML(p map[string]any) string {
	return fmt.Sprintf(
		"<p>Halo Tim Pusat,</p>"+
			"<p>Surat Jalan untuk pesanan <strong>%s</strong> dari cabang <strong>%s</strong> sudah siap "+
			"(tanggal pesanan %s).</p>"+
			"<p>Total pesanan: <strong>%s</strong>.</p>"+
			"<p>Berkas Surat Jalan (XLSX dan PDF) kami lampirkan pada email ini.</p>",
		str(p, "nomor"), str(p, "cabang"), str(p, "tanggal"), rupiah(num(p, "total")))
}

// ---------- template: reminder_cnc ----------

func tplReminder(p map[string]any) string {
	return fmt.Sprintf(
		"Halo %s,\n\n"+
			"Pengingat pembayaran (%s) untuk pesanan Cash & Carry %s.\n"+
			"Jatuh tempo: %s.\nTotal pesanan: %s.\n\n"+
			"Mohon selesaikan pembayaran agar pesanan dapat diproses.\n",
		sapaan(p), str(p, "tahap"), str(p, "nomor"), str(p, "jatuh_tempo"), rupiah(num(p, "total")))
}

func tplReminderHTML(p map[string]any) string {
	return fmt.Sprintf(
		"<p>Halo %s,</p>"+
			"<p>Pengingat pembayaran (%s) untuk pesanan Cash &amp; Carry <strong>%s</strong>.</p>"+
			"<p>Jatuh tempo: <strong>%s</strong>.<br>Total pesanan: <strong>%s</strong>.</p>"+
			"<p>Mohon selesaikan pembayaran agar pesanan dapat diproses.</p>",
		sapaan(p), str(p, "tahap"), str(p, "nomor"), str(p, "jatuh_tempo"), rupiah(num(p, "total")))
}
