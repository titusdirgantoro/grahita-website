package fulfillment

// Uji unit pemenuhan pesanan Done (kartu F1-18): urutan "dokumen dulu, email
// kemudian", idempotensi saat pekerjaan dijalankan ulang, dan sikap terhadap
// penerima yang belum berflag.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/notification"
)

type docsPalsu struct {
	dipanggil        int
	dipanggilInvoice int
	versiInvoice     int
	docs             []document.Document
	invoice          document.Document
	err              error
	invoiceErr       error
}

func (d *docsPalsu) Ensure(_ context.Context, _ string) ([]document.Document, error) {
	d.dipanggil++
	return d.docs, d.err
}

func (d *docsPalsu) EnsureInvoiceVersi(_ context.Context, _ string, versi int) (document.Document, error) {
	d.dipanggilInvoice++
	d.versiInvoice = versi
	return d.invoice, d.invoiceErr
}

type mailPalsu struct {
	jenis         []string
	jenisVersi    []string
	adaSudah      map[string]bool
	adaSudahVersi map[string]bool
	versiTerakhir int
	tanpaPenerima map[string]bool
	err           error
}

func newMailPalsu() *mailPalsu {
	return &mailPalsu{
		adaSudah:      map[string]bool{},
		adaSudahVersi: map[string]bool{},
		tanpaPenerima: map[string]bool{},
	}
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

func (m *mailPalsu) EnqueueOrderEmailSekaliVersi(_ context.Context, in notification.OrderEmailInput) (notification.Email, bool, error) {
	if m.err != nil {
		return notification.Email{}, false, m.err
	}
	if m.tanpaPenerima[in.Jenis] {
		return notification.Email{}, false, notification.ErrNoRecipient
	}
	kunci := fmt.Sprintf("%s@%d", in.Jenis, in.Versi)
	if m.adaSudahVersi[kunci] {
		return notification.Email{ID: "lama"}, false, nil
	}
	m.jenisVersi = append(m.jenisVersi, in.Jenis)
	m.adaSudahVersi[kunci] = true
	m.versiTerakhir = in.Versi
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

// ---------- revisi invoice (F2-25) ----------

// TestPesananDirevisiHanyaInvoiceTanpaSuratJalan membuktikan jalur revisi
// merender/memastikan HANYA invoice VERSI YANG DIPATOK, lalu mengantre HANYA
// email invoice (cabang + admin ber-flag) — Surat Jalan tidak disentuh/dikirim
// ulang.
func TestPesananDirevisiHanyaInvoiceTanpaSuratJalan(t *testing.T) {
	docs := &docsPalsu{invoice: document.Document{ID: "doc-v1", Jenis: document.JenisInvoicePDF, Versi: 1}}
	mail := newMailPalsu()
	hasil, err := NewService(docs, mail).PesananDirevisi(context.Background(), "order-1", 1)
	if err != nil {
		t.Fatalf("revisi harus berhasil: %v", err)
	}
	if docs.dipanggilInvoice != 1 {
		t.Errorf("invoice harus dipastikan tepat sekali, dapat %d", docs.dipanggilInvoice)
	}
	if docs.versiInvoice != 1 {
		t.Errorf("invoice harus dipastikan pada VERSI YANG DIPATOK (1), dapat %d", docs.versiInvoice)
	}
	if mail.versiTerakhir != 1 {
		t.Errorf("email invoice harus memakai versi yang dipatok (1), dapat %d", mail.versiTerakhir)
	}
	if docs.dipanggil != 0 {
		t.Errorf("jalur revisi tidak boleh merender ketiga dokumen Done, dapat %d", docs.dipanggil)
	}
	if hasil.Dokumen != 1 {
		t.Errorf("satu dokumen (invoice) harus terlihat, dapat %d", hasil.Dokumen)
	}
	mau := []string{notification.JenisInvoiceCabang, notification.JenisInvoiceAdmin}
	if len(hasil.EmailDibuat) != len(mau) {
		t.Fatalf("email invoice harus diantre: %v", hasil.EmailDibuat)
	}
	for i, jenis := range mau {
		if hasil.EmailDibuat[i] != jenis {
			t.Errorf("email ke-%d harus %q, dapat %q", i+1, jenis, hasil.EmailDibuat[i])
		}
	}
	for _, jenis := range mail.jenisVersi {
		if jenis == notification.JenisSuratJalanAdmin {
			t.Errorf("surat jalan TIDAK boleh dikirim ulang saat revisi: %v", mail.jenisVersi)
		}
	}
}

// TestPesananDirevisiIdempotenUntukVersiYangSama: pekerjaan worker yang
// dijalankan ulang (River at-least-once) untuk versi yang sama tidak
// melahirkan email invoice kedua.
func TestPesananDirevisiIdempotenUntukVersiYangSama(t *testing.T) {
	docs := &docsPalsu{invoice: document.Document{ID: "doc-v1", Jenis: document.JenisInvoicePDF, Versi: 1}}
	mail := newMailPalsu()
	svc := NewService(docs, mail)

	if _, err := svc.PesananDirevisi(context.Background(), "order-1", 1); err != nil {
		t.Fatalf("pemanggilan pertama harus berhasil: %v", err)
	}
	hasil, err := svc.PesananDirevisi(context.Background(), "order-1", 1)
	if err != nil {
		t.Fatalf("pemanggilan kedua harus berhasil: %v", err)
	}
	if len(hasil.EmailDibuat) != 0 {
		t.Errorf("ulangan tidak boleh membuat email baru, dapat %v", hasil.EmailDibuat)
	}
	if len(hasil.EmailSudahAda) != 2 {
		t.Errorf("dua jenis invoice harus dikenali sudah ada, dapat %v", hasil.EmailSudahAda)
	}
	if len(mail.jenisVersi) != 2 {
		t.Errorf("total email invoice harus tetap dua, dapat %d", len(mail.jenisVersi))
	}
}

// TestPesananDirevisiVersiBaruMendapatEmailBaru: ketika model versi bergerak
// (penjaga per (pesanan, jenis, versi) meloloskan versi berikutnya), pekerjaan
// revisi berikutnya tetap mengantre email invoice barunya — dan versi yang
// dipakai adalah versi YANG DIPATOK pada pekerjaan itu.
func TestPesananDirevisiVersiBaruMendapatEmailBaru(t *testing.T) {
	docs := &docsPalsu{invoice: document.Document{ID: "doc-v1", Jenis: document.JenisInvoicePDF, Versi: 1}}
	mail := newMailPalsu()
	svc := NewService(docs, mail)

	if _, err := svc.PesananDirevisi(context.Background(), "order-1", 1); err != nil {
		t.Fatalf("revisi pertama harus berhasil: %v", err)
	}
	if mail.versiTerakhir != 1 {
		t.Fatalf("revisi pertama harus memakai versi 1, dapat %d", mail.versiTerakhir)
	}
	hasil, err := svc.PesananDirevisi(context.Background(), "order-1", 2)
	if err != nil {
		t.Fatalf("revisi kedua harus berhasil: %v", err)
	}
	if len(hasil.EmailDibuat) != 2 {
		t.Errorf("versi baru harus mendapat dua email invoice baru, dapat %v", hasil.EmailDibuat)
	}
	if docs.versiInvoice != 2 {
		t.Errorf("revisi kedua harus memastikan invoice versi 2, dapat %d", docs.versiInvoice)
	}
	if mail.versiTerakhir != 2 {
		t.Errorf("revisi kedua harus mengantre email versi 2, dapat %d", mail.versiTerakhir)
	}
}

// TestPesananDirevisiTanpaVersiMemakaiVersiInvoice: muatan job lama (tanpa
// kolom versi, Versi == 0) tetap berperilaku seperti sebelumnya — invoice
// versi berjalan yang dipastikan, dan emailnya memakai versi invoice itu.
func TestPesananDirevisiTanpaVersiMemakaiVersiInvoice(t *testing.T) {
	docs := &docsPalsu{invoice: document.Document{ID: "doc-v3", Jenis: document.JenisInvoicePDF, Versi: 3}}
	mail := newMailPalsu()
	hasil, err := NewService(docs, mail).PesananDirevisi(context.Background(), "order-1", 0)
	if err != nil {
		t.Fatalf("job lama harus tetap berhasil: %v", err)
	}
	if docs.versiInvoice != 0 {
		t.Errorf("job lama harus meminta versi berjalan (0), dapat %d", docs.versiInvoice)
	}
	if mail.versiTerakhir != 3 {
		t.Errorf("email job lama harus memakai versi invoice (3), dapat %d", mail.versiTerakhir)
	}
	if len(hasil.EmailDibuat) != 2 {
		t.Errorf("dua email invoice harus diantre, dapat %v", hasil.EmailDibuat)
	}
}

// TestPesananDirevisiMenahanEmailSaatInvoiceGagal: kegagalan render diteruskan
// supaya worker mengulang, dan email tidak diantre sebelum invoice ada.
func TestPesananDirevisiMenahanEmailSaatInvoiceGagal(t *testing.T) {
	docs := &docsPalsu{invoiceErr: document.ErrRenderUnavailable}
	mail := newMailPalsu()
	_, err := NewService(docs, mail).PesananDirevisi(context.Background(), "order-1", 1)
	if err == nil {
		t.Fatal("kegagalan render harus diteruskan supaya pekerjaan diulang")
	}
	if !errors.Is(err, document.ErrRenderUnavailable) {
		t.Errorf("penyebab asli harus ikut terbawa, dapat %v", err)
	}
	if len(mail.jenisVersi) != 0 {
		t.Errorf("email tidak boleh diantre sebelum invoice ada, dapat %v", mail.jenisVersi)
	}
}
