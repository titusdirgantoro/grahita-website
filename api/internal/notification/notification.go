// Package notification mengelola email transaksional lewat tabel email_outbox
// (transactional outbox, SystemDesign.md §5.1, kartu F1-16).
//
// Aturan yang ditegakkan di sini:
//
//   - Email TIDAK PERNAH dikirim dari handler HTTP. Service bisnis menulis
//     baris ke email_outbox (Enqueue/EnqueueOrderEmail), worker (River)
//     memindainya lewat ProsesAntrean, mengirim, lalu memperbarui status.
//   - Satu baris outbox = satu percobaan kirim yang utuh. Klaim baris memakai
//     `FOR UPDATE SKIP LOCKED` + sewa (lease) sehingga dua worker tidak pernah
//     mengirim baris yang sama, dan baris yang ditinggal proses mati pulih
//     sendiri setelah sewa kedaluwarsa.
//   - Pengiriman bersifat AT-LEAST-ONCE, bukan tepat-sekali: bila proses mati
//     (atau penandaan `sent` gagal) SETELAH SMTP menerima pesan, baris diklaim
//     ulang setelah sewa habis dan email bisa terkirim dua kali. Ini inheren
//     pada pola outbox dan disengaja; deduplikasi tepat-sekali (bila kelak
//     dibutuhkan) harus lewat idempotency key di sisi penerima. Yang dijamin
//     di sini: baris `sent` tidak pernah dikirim ulang oleh worker, dan
//     pemindaian yang berhenti di tengah batch melepas baris yang belum
//     dicoba kembali ke `queued` tanpa menghabiskan jatah retry.
//   - Retry berjenjang 1m, 5m, 30m, 2j, 6j sampai maksimum 5 percobaan
//     (PRD EM-01). Setelah itu status `failed` dan muncul di riwayat pesanan
//     dengan tombol kirim ulang (PRD EM-02/EM-03).
//   - Baris yang SUDAH `sent` tidak pernah dikirim ulang oleh worker; kirim
//     ulang hanya terjadi karena tindakan admin eksplisit (ResendOrder) dan
//     selalu tercatat di audit_logs.
//   - Penerima dihitung ULANG setiap kali email diantre maupun dikirim ulang:
//     admin nonaktif/undangan tidak pernah menerima (PRD AP-USR-01/02), dan
//     tidak ada alamat Pusat tambahan di luar flag per admin (PRD §8 no.5).
package notification

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/users"
)

// Jenis email (kolom email_outbox.jenis, Database.md §4.7).
const (
	JenisResetPassword   = "reset_password"
	JenisUndanganAdmin   = "undangan_admin"
	JenisInvoiceCabang   = "invoice_cabang"
	JenisInvoiceAdmin    = "invoice_admin"
	JenisSuratJalanAdmin = "surat_jalan_admin"
	JenisReminderCNC     = "reminder_cnc"
)

// Status email (enum email_status, Database.md §3).
const (
	StatusQueued  = "queued"
	StatusSending = "sending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrForbidden: aktor tidak punya kapabilitas yang dibutuhkan.
	ErrForbidden = errors.New("tidak berhak mengakses email pesanan")
	// ErrNotFound: email yang dimaksud belum pernah ada.
	ErrNotFound = errors.New("email tidak ditemukan")
	// ErrKindInvalid: jenis email tidak dikenal atau tidak dikirim ke admin.
	ErrKindInvalid = errors.New("jenis email tidak dikenal")
	// ErrNoRecipient: tidak ada penerima yang berhak menerima email ini.
	ErrNoRecipient = errors.New("tidak ada penerima yang berhak")
	// ErrDocumentNotReady: dokumen pesanan belum selesai dirender sehingga
	// email dokumen belum boleh dikirim (PRD §6.2).
	ErrDocumentNotReady = errors.New("dokumen pesanan belum selesai dirender")
	// ErrAlreadyQueued: masih ada email jenis yang sama dalam antrean.
	ErrAlreadyQueued = errors.New("email jenis ini masih dalam antrean")
	// ErrAttachmentUnavailable: lampiran tidak bisa dibaca dari object storage.
	ErrAttachmentUnavailable = errors.New("lampiran tidak dapat dibaca")
	// ErrInvalidData: isi email tidak lolos validasi domain.
	ErrInvalidData = errors.New("data email tidak valid")
)

// JenisDokumen yang diantre ke admin Pusat (semua kecuali reset & undangan).
var jenisDokumen = map[string]bool{
	JenisInvoiceCabang:   true,
	JenisInvoiceAdmin:    true,
	JenisSuratJalanAdmin: true,
}

// uuidRe menolak id pesanan yang jelas bukan UUID dengan jawaban 404, bukan
// 500 dari SQLSTATE 22P02 (temuan QA F1-15 untuk GET /admin/orders/{id}/emails).
// Id pesanan selalu diterbitkan sistem (uuidv7), jadi formatnya dapat diperiksa
// tanpa menyentuh database. Pola ini sengaja sama dengan paket order.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Lampiran menunjuk satu dokumen di object storage yang harus dilampirkan.
type Lampiran struct {
	StorageKey  string `json:"storage_key"`
	Nama        string `json:"nama"`
	ContentType string `json:"content_type"`
}

// Email adalah satu baris email_outbox.
type Email struct {
	ID        string         `json:"id"`
	OrderID   string         `json:"order_id,omitempty"`
	Jenis     string         `json:"jenis"`
	To        []string       `json:"to_addr"`
	Cc        []string       `json:"cc_addr,omitempty"`
	Subject   string         `json:"subject"`
	Payload   map[string]any `json:"payload,omitempty"`
	Status    string         `json:"status"`
	Attempts  int            `json:"attempts"`
	NextTryAt time.Time      `json:"next_try_at"`
	LastError string         `json:"last_error,omitempty"`
	SentAt    *time.Time     `json:"sent_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Envelope adalah email yang siap diantre.
type Envelope struct {
	OrderID string
	Jenis   string
	To      []string
	Cc      []string
	Subject string
	Payload map[string]any
}

// Claim adalah permintaan klaim sekumpulan email siap kirim.
type Claim struct {
	// Now adalah waktu "sekarang" dari clock aplikasi.
	Now time.Time
	// Limit adalah jumlah maksimum baris yang diklaim.
	Limit int
	// Lease adalah masa sewa; baris `sending` yang sewanya habis (proses mati
	// di tengah jalan) boleh diklaim ulang.
	Lease time.Duration
	// MaxAttempts adalah batas percobaan; baris yang sudah mencapai batas
	// tidak diklaim lagi.
	MaxAttempts int
}

// Store menyimpan dan memutasi email_outbox.
type Store interface {
	Enqueue(ctx context.Context, e Envelope) (Email, error)
	Claim(ctx context.Context, c Claim) ([]Email, error)
	MarkSent(ctx context.Context, id string, at time.Time) error
	Reschedule(ctx context.Context, id string, nextTryAt time.Time, cause string) error
	GiveUp(ctx context.Context, id string, cause string) error
	ByOrder(ctx context.Context, orderID string) ([]Email, error)
	// Latest mengembalikan email terbaru untuk satu pesanan + jenis, atau
	// ErrNotFound bila belum pernah ada. Dipakai ResendOrder untuk memastikan
	// admin hanya mengirim ulang email yang memang pernah dibuat.
	Latest(ctx context.Context, orderID, jenis string) (Email, error)
	// Resend menyimpan salinan email baru DAN entri audit dalam SATU
	// transaksi; EntityID pada rec diisi repository dengan id baris baru.
	Resend(ctx context.Context, e Envelope, rec audit.Entry) (Email, error)
	// ActiveExists melaporkan apakah masih ada email status queued/sending
	// untuk satu pesanan + jenis (penjaga klik ganda kirim ulang).
	ActiveExists(ctx context.Context, orderID, jenis string) (bool, error)
	// Release mengembalikan baris `sending` yang BELUM sempat dicoba ke
	// `queued` TANPA menambah percobaan (`attempts` dikurangi satu, kebalikan
	// dari klaim). Dipakai ProsesAntrean ketika pemindaian berhenti di tengah
	// batch (mis. context job dibatalkan) supaya jatah retry tidak habis oleh
	// percobaan yang tidak pernah terjadi.
	Release(ctx context.Context, ids []string, now time.Time) error
	// PesananEmailGagal mengembalikan id pesanan (DISTINCT) yang punya minimal
	// satu email berstatus `failed`. Dipakai riwayat pemesanan admin (F1-15)
	// untuk penanda & filter "email gagal" tanpa domain lain membaca
	// email_outbox.
	PesananEmailGagal(ctx context.Context) ([]string, error)
}

// OrderRef adalah data pesanan yang dibutuhkan template dokumen.
type OrderRef struct {
	ID          string
	Nomor       string
	Cabang      string
	NamaKepala  string
	CabangEmail string
	Versi       int
	Total       int64
	Tanggal     time.Time
	JatuhTempo  *time.Time
	IsCNC       bool
}

// Dokumen adalah satu baris tabel documents milik pesanan.
type Dokumen struct {
	Jenis       string
	Versi       int
	StorageKey  string
	ContentType string
}

// OrderReader membaca data pesanan + dokumennya.
type OrderReader interface {
	OrderByID(ctx context.Context, id string) (OrderRef, error)
	Documents(ctx context.Context, orderID string) ([]Dokumen, error)
}

// AdminRecipients adalah daftar penerima admin ber-flag (users.Service).
type AdminRecipients interface {
	DocumentRecipients(ctx context.Context, kind users.DocKind) ([]string, error)
}

// ObjectStore membaca isi lampiran dari object storage.
type ObjectStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Config adalah parameter operasional outbox.
type Config struct {
	// MaxAttempts adalah jumlah percobaan KIRIM maksimum untuk satu email
	// (default: 1 percobaan pertama + 5 retry berjenjang, PRD EM-01).
	MaxAttempts int
	// Lease adalah masa sewa satu klaim; baris `sending` yang ditinggal proses
	// mati boleh diklaim ulang setelah sewa habis.
	Lease time.Duration
	// SendTimeout adalah batas waktu total satu percobaan kirim.
	SendTimeout time.Duration
	// Batch adalah jumlah baris maksimum per pemindaian.
	Batch int
}

// DefaultConfig mengembalikan parameter baku outbox (Architecture.md §2).
func DefaultConfig() Config {
	return Config{
		MaxAttempts: MaxAttempts,
		Lease:       DefaultLease,
		SendTimeout: mailer.DefaultTimeout,
		Batch:       50,
	}
}

// Hasil merangkum satu pemindaian outbox (dipakai log & pengujian).
type Hasil struct {
	Diklaim  int
	Terkirim int
	Ditunda  int
	Menyerah int
}

// Service adalah logika bisnis outbox email.
type Service struct {
	store  Store
	mail   mailer.Mailer
	obj    ObjectStore
	orders OrderReader
	admins AdminRecipients
	clk    clock.Clock
	cfg    Config
}

// Option menyesuaikan Service.
type Option func(*Service)

// WithObjectStore memasang pembaca lampiran (storage.Store).
func WithObjectStore(o ObjectStore) Option {
	return func(s *Service) {
		if o != nil {
			s.obj = o
		}
	}
}

// WithOrderReader memasang pembaca pesanan + dokumen (butuh PostgreSQL).
func WithOrderReader(r OrderReader) Option {
	return func(s *Service) {
		if r != nil {
			s.orders = r
		}
	}
}

// WithAdminRecipients memasang sumber daftar admin penerima dokumen.
func WithAdminRecipients(r AdminRecipients) Option {
	return func(s *Service) {
		if r != nil {
			s.admins = r
		}
	}
}

// WithClock memasang sumber waktu (pengujian deterministik).
func WithClock(c clock.Clock) Option {
	return func(s *Service) {
		if c != nil {
			s.clk = c
		}
	}
}

// WithConfig mengganti parameter operasional outbox.
func WithConfig(c Config) Option {
	return func(s *Service) {
		if c.MaxAttempts > 0 {
			s.cfg.MaxAttempts = c.MaxAttempts
		}
		if c.Lease > 0 {
			s.cfg.Lease = c.Lease
		}
		if c.SendTimeout > 0 {
			s.cfg.SendTimeout = c.SendTimeout
		}
		if c.Batch > 0 {
			s.cfg.Batch = c.Batch
		}
	}
}

// New membuat service outbox email.
func New(store Store, mail mailer.Mailer, opts ...Option) *Service {
	if mail == nil {
		mail = mailer.Log{}
	}
	s := &Service{store: store, mail: mail, clk: clock.System(), cfg: DefaultConfig()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// NormalizeAddress merapikan daftar alamat: huruf kecil, tanpa spasi, unik,
// dan tanpa alamat kosong.
func NormalizeAddress(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	seen := map[string]bool{}
	for _, a := range addrs {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// Enqueue menyimpan satu email transaksional ke outbox.
//
// Payload WAJIB memuat kunci "template"; bila tidak diisi, jenis email dipakai
// sebagai nama template. Penerima kosong ditolak: baris outbox tanpa penerima
// tidak pernah dibuat sehingga email tak berhak tidak mungkin terkirim.
func (s *Service) Enqueue(ctx context.Context, e Envelope) (Email, error) {
	e.Jenis = strings.TrimSpace(e.Jenis)
	if !validJenis(e.Jenis) {
		return Email{}, fmt.Errorf("%w: %q", ErrKindInvalid, e.Jenis)
	}
	e.To = NormalizeAddress(e.To)
	e.Cc = NormalizeAddress(e.Cc)
	if len(e.To) == 0 {
		return Email{}, ErrNoRecipient
	}
	if e.Payload == nil {
		e.Payload = map[string]any{}
	}
	if _, ok := e.Payload["template"]; !ok {
		e.Payload["template"] = e.Jenis
	}
	if strings.TrimSpace(e.Subject) == "" {
		e.Subject = DefaultSubject(e.Jenis)
	}
	return s.store.Enqueue(ctx, e)
}

// ProsesAntrean mengklaim sekumpulan email siap kirim lalu mengirimkannya.
//
// Ia aman dijalankan bersamaan dari beberapa worker: klaim memakai
// `FOR UPDATE SKIP LOCKED` sehingga satu baris hanya dipegang satu worker.
//
// Bila pemindaian berhenti di tengah batch (mis. context job dibatalkan River
// pada JobTimeout), baris yang BELUM sempat dicoba dilepas kembali ke `queued`
// dengan `attempts` seperti sebelum klaim. Tanpa itu, satu job yang dibatalkan
// akan menghabiskan jatah retry email yang bahkan tidak pernah dikirim
// (temuan QA putaran 1, butir 3).
func (s *Service) ProsesAntrean(ctx context.Context, limit int) (Hasil, error) {
	var hasil Hasil
	if limit <= 0 || limit > s.cfg.Batch {
		limit = s.cfg.Batch
	}
	now := s.clk.Now()
	batch, err := s.store.Claim(ctx, Claim{
		Now:         now,
		Limit:       limit,
		Lease:       s.cfg.Lease,
		MaxAttempts: s.cfg.MaxAttempts,
	})
	if err != nil {
		return hasil, fmt.Errorf("klaim antrean email: %w", err)
	}
	hasil.Diklaim = len(batch)

	for i := range batch {
		e := batch[i]
		// Baris sesudah baris ini belum dicoba sama sekali; merekalah yang
		// harus dilepas bila pemindaian berhenti sebelum sempat mengirimnya.
		sisa := batch[i+1:]
		if err := ctx.Err(); err != nil {
			s.lepasSisa(ctx, append([]Email{e}, sisa...))
			return hasil, err
		}
		err := s.Kirim(ctx, e)
		if err == nil {
			if err := s.store.MarkSent(ctx, e.ID, s.clk.Now()); err != nil {
				s.lepasSisa(ctx, sisa)
				return hasil, fmt.Errorf("tandai email %s terkirim: %w", e.ID, err)
			}
			hasil.Terkirim++
			continue
		}
		slog.WarnContext(ctx, "kirim email gagal", "email_id", e.ID, "jenis", e.Jenis,
			"percobaan", e.Attempts, "err", err)
		if e.Attempts >= s.cfg.MaxAttempts {
			if err := s.store.GiveUp(ctx, e.ID, err.Error()); err != nil {
				s.lepasSisa(ctx, sisa)
				return hasil, fmt.Errorf("tandai email %s gagal: %w", e.ID, err)
			}
			hasil.Menyerah++
			continue
		}
		delay := RetryDelay(e.Attempts)
		if err := s.store.Reschedule(ctx, e.ID, s.clk.Now().Add(delay), err.Error()); err != nil {
			s.lepasSisa(ctx, sisa)
			return hasil, fmt.Errorf("jadwalkan ulang email %s: %w", e.ID, err)
		}
		hasil.Ditunda++
	}
	return hasil, nil
}

// lepasSisa mengembalikan baris yang belum sempat dicoba ke antrean. Context
// pemanggil biasanya sudah dibatalkan (itu sebabnya pemindaian berhenti), jadi
// pelepasan memakai context tanpa pembatalan dengan tenggat pendek supaya
// baris tidak tertahan `sending` sampai sewa 15 menit habis.
func (s *Service) lepasSisa(ctx context.Context, sisa []Email) {
	if len(sisa) == 0 {
		return
	}
	ids := make([]string, 0, len(sisa))
	for _, e := range sisa {
		ids = append(ids, e.ID)
	}
	lepasCtx, batal := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer batal()
	if err := s.store.Release(lepasCtx, ids, s.clk.Now()); err != nil {
		slog.WarnContext(ctx, "lepas sisa klaim email gagal", "jumlah", len(ids), "err", err)
	}
}

// Kirim merender dan mengirim SATU email outbox. Ia tidak mengubah status;
// pemanggil (ProsesAntrean) yang mencatat hasilnya supaya urutan "kirim dulu,
// baru tandai" selalu terjaga.
func (s *Service) Kirim(ctx context.Context, e Email) error {
	msg, err := s.Render(ctx, e)
	if err != nil {
		return err
	}
	sendCtx, batal := context.WithTimeout(ctx, s.cfg.SendTimeout)
	defer batal()
	if err := s.mail.Send(sendCtx, msg); err != nil {
		return fmt.Errorf("kirim ke %s: %w", strings.Join(msg.To, ", "), err)
	}
	return nil
}

// PesananEmailGagal menyajikan id pesanan yang punya email berstatus gagal.
//
// Dipakai riwayat pemesanan admin (F1-15) untuk penanda & filter "email gagal"
// (PRD AP-ORD-01, Database.md §7.2). Pemanggilnya sudah otorisasi di domainnya
// sendiri, jadi method ini tidak menerima Actor — ia bukan permukaan publik.
func (s *Service) PesananEmailGagal(ctx context.Context) ([]string, error) {
	return s.store.PesananEmailGagal(ctx)
}

// validJenis melaporkan apakah jenis email dikenal.
func validJenis(jenis string) bool {
	switch jenis {
	case JenisResetPassword, JenisUndanganAdmin, JenisInvoiceCabang,
		JenisInvoiceAdmin, JenisSuratJalanAdmin, JenisReminderCNC:
		return true
	}
	return false
}
