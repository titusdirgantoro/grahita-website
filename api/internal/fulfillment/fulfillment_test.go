package fulfillment

// Uji unit pemenuhan pesanan Done (kartu F1-18): urutan "dokumen dulu, email
// kemudian", idempotensi saat pekerjaan dijalankan ulang, dan sikap terhadap
// penerima yang belum berflag.

import (
	"context"
	"errors"
	"testing"

	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/notification"
)

type docsPalsu struct {
	dipanggil int
	docs      []document.Document
	err       error
}

func (d *docsPalsu) Ensure(_ context.Context, _ string) ([]document.Document, error) {
	d.dipanggil++
	return d.docs, d.err
}

type mailPalsu struct {
	jenis         []string
	adaSudah      map[string]bool
	tanpaPenerima map[string]bool
	err           error
}

func newMailPalsu() *mailPalsu {
	return &mailPalsu{adaSudah: map[string]bool{}, tanpaPenerima: map[string]bool{}}
}

func (m *mailPalsu) EnqueueOrderEmailSekali(_ context.Context, in notification.OrderEmailInput) (notification.Email, bool, error) {
	if m.err != nil {
		return notification.Email{}, false, m.err
	}
	if m.tanpaPenerima[in.Jenis] {
		return notification.Email{}, false, notification.ErrNoRecipient
	}
	if m.adaSudah[in.Jenis] {
		return notification.Email{ID: "lama"}, false, nil
	}
	m.jenis = append(m.jenis, in.Jenis)
	m.adaSudah[in.Jenis] = true
	return notification.Email{ID: "baru", Jenis: in.Jenis}, true, nil
}

func dokTiga() []document.Document {
	return []document.Document{
		{Jenis: document.JenisInvoicePDF},
		{Jenis: document.JenisSuratJalanXLSX},
		{Jenis: document.JenisSuratJalanPDF},
	}
}

func TestPesananSelesaiMerenderDuluLaluMengantreEmail(t *testing.T) {
	docs := &docsPalsu{docs: dokTiga()}
	mail := newMailPalsu()
	hasil, err := NewService(docs, mail).PesananSelesai(context.Background(), "order-1")
	if err != nil {
		t.Fatalf("pemenuhan harus berhasil: %v", err)
	}
	if docs.dipanggil != 1 {
		t.Errorf("dokumen harus dipastikan tepat sekali, dapat %d", docs.dipanggil)
	}
	if hasil.Dokumen != 3 {
		t.Errorf("tiga dokumen harus terlihat, dapat %d", hasil.Dokumen)
	}
	mau := []string{notification.JenisInvoiceCabang, notification.JenisInvoiceAdmin, notification.JenisSuratJalanAdmin}
	if len(hasil.EmailDibuat) != len(mau) {
		t.Fatalf("semua email dokumen harus diantre: %v", hasil.EmailDibuat)
	}
	for i, jenis := range mau {
		if hasil.EmailDibuat[i] != jenis {
			t.Errorf("email ke-%d harus %q, dapat %q", i+1, jenis, hasil.EmailDibuat[i])
		}
		if mail.jenis[i] != jenis {
			t.Errorf("pengantrean ke-%d harus %q, dapat %q", i+1, jenis, mail.jenis[i])
		}
	}
}

func TestPesananSelesaiIdempotenSaatDiulang(t *testing.T) {
	docs := &docsPalsu{docs: dokTiga()}
	mail := newMailPalsu()
	svc := NewService(docs, mail)

	if _, err := svc.PesananSelesai(context.Background(), "order-1"); err != nil {
		t.Fatalf("pemanggilan pertama harus berhasil: %v", err)
	}
	hasil, err := svc.PesananSelesai(context.Background(), "order-1")
	if err != nil {
		t.Fatalf("pemanggilan kedua harus berhasil: %v", err)
	}
	if len(hasil.EmailDibuat) != 0 {
		t.Errorf("ulangan tidak boleh membuat email baru, dapat %v", hasil.EmailDibuat)
	}
	if len(hasil.EmailSudahAda) != 3 {
		t.Errorf("tiga jenis harus dikenali sudah ada, dapat %v", hasil.EmailSudahAda)
	}
	if len(mail.jenis) != 3 {
		t.Errorf("total email harus tetap tiga, dapat %d", len(mail.jenis))
	}
}

func TestPesananSelesaiMenahanEmailSaatDokumenGagal(t *testing.T) {
	docs := &docsPalsu{err: document.ErrRenderUnavailable}
	mail := newMailPalsu()
	_, err := NewService(docs, mail).PesananSelesai(context.Background(), "order-1")
	if err == nil {
		t.Fatal("kegagalan render harus diteruskan supaya pekerjaan diulang")
	}
	if !errors.Is(err, document.ErrRenderUnavailable) {
		t.Errorf("penyebab asli harus ikut terbawa, dapat %v", err)
	}
	if len(mail.jenis) != 0 {
		t.Errorf("email tidak boleh diantre sebelum dokumen ada, dapat %v", mail.jenis)
	}
}

func TestPesananSelesaiMenolakDokumenKosong(t *testing.T) {
	docs := &docsPalsu{}
	mail := newMailPalsu()
	if _, err := NewService(docs, mail).PesananSelesai(context.Background(), "order-1"); err == nil {
		t.Fatal("tanpa dokumen, email tidak boleh diantre")
	}
	if len(mail.jenis) != 0 {
		t.Errorf("email tidak boleh diantre tanpa dokumen, dapat %v", mail.jenis)
	}
}

func TestPesananSelesaiMelewatiJenisTanpaPenerima(t *testing.T) {
	docs := &docsPalsu{docs: dokTiga()}
	mail := newMailPalsu()
	// Belum ada admin ber-flag invoice: hanya email cabang yang bisa dibuat.
	mail.tanpaPenerima[notification.JenisInvoiceAdmin] = true
	mail.tanpaPenerima[notification.JenisSuratJalanAdmin] = true

	hasil, err := NewService(docs, mail).PesananSelesai(context.Background(), "order-1")
	if err != nil {
		t.Fatalf("penerima yang belum ada bukan kegagalan pekerjaan: %v", err)
	}
	if len(hasil.EmailDibuat) != 1 || hasil.EmailDibuat[0] != notification.JenisInvoiceCabang {
		t.Errorf("hanya email cabang yang boleh dibuat: %v", hasil.EmailDibuat)
	}
	if len(hasil.EmailTanpaPenerima) != 2 {
		t.Errorf("dua jenis harus dilaporkan tanpa penerima: %v", hasil.EmailTanpaPenerima)
	}
}

func TestPesananSelesaiMeneruskanKegagalanLain(t *testing.T) {
	docs := &docsPalsu{docs: dokTiga()}
	mail := newMailPalsu()
	mail.err = errors.New("database mati")
	if _, err := NewService(docs, mail).PesananSelesai(context.Background(), "order-1"); err == nil {
		t.Fatal("kegagalan pengantrean harus diteruskan supaya pekerjaan diulang")
	}
}

func TestPesananSelesaiMenolakMasukanTidakLengkap(t *testing.T) {
	if _, err := NewService(&docsPalsu{docs: dokTiga()}, newMailPalsu()).PesananSelesai(context.Background(), "   "); err == nil {
		t.Error("pesanan tanpa id harus ditolak")
	}
	if _, err := NewService(nil, nil).PesananSelesai(context.Background(), "order-1"); err == nil {
		t.Error("layanan yang belum terpasang harus ditolak")
	}
}
