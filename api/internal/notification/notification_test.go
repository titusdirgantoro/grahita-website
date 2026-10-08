package notification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/users"
)

// ---------- tiruan (fake) ----------

// fakeStore adalah email_outbox in-memory dengan semantik klaim yang sama
// dengan implementasi PostgreSQL (status/attempts/lease).
type fakeStore struct {
	mu     sync.Mutex
	rows   map[string]Email
	urut   []string
	seq    int
	audits []audit.Entry
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]Email{}} }

func (f *fakeStore) Enqueue(_ context.Context, e Envelope) (Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("email-%d", f.seq)
	now := time.Now().UTC()
	row := Email{
		ID: id, OrderID: e.OrderID, Jenis: e.Jenis, To: e.To, Cc: e.Cc,
		Subject: e.Subject, Payload: e.Payload, Status: StatusQueued,
		// next_try_at di database defaultnya now(); di sini dipakai epoch
		// supaya baris langsung "siap kirim" tanpa bergantung jam dinding
		// (uji memakai Clock yang dibekukan).
		NextTryAt: time.Unix(0, 0).UTC(), CreatedAt: now,
	}
	f.rows[id] = row
	f.urut = append(f.urut, id)
	return row, nil
}

func (f *fakeStore) Claim(_ context.Context, c Claim) ([]Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Cermin implementasi PostgreSQL: baris `sending` yang sewanya habis dan
	// percobaannya sudah habis ditandai gagal (tidak tersangkut selamanya).
	for _, id := range f.urut {
		row := f.rows[id]
		if row.Status == StatusSending && row.Attempts >= c.MaxAttempts && !row.NextTryAt.After(c.Now) {
			row.Status = StatusFailed
			if row.LastError == "" {
				row.LastError = "percobaan habis saat email masih berstatus sending"
			}
			f.rows[id] = row
		}
	}
	var siap []Email
	for _, id := range f.urut {
		row := f.rows[id]
		if row.Status != StatusQueued && row.Status != StatusSending {
			continue
		}
		if row.Attempts >= c.MaxAttempts {
			continue
		}
		if row.NextTryAt.After(c.Now) {
			continue
		}
		siap = append(siap, row)
	}
	sort.SliceStable(siap, func(i, j int) bool { return siap[i].NextTryAt.Before(siap[j].NextTryAt) })
	if len(siap) > c.Limit {
		siap = siap[:c.Limit]
	}
	out := make([]Email, 0, len(siap))
	for _, row := range siap {
		row.Status = StatusSending
		row.Attempts++
		row.NextTryAt = c.Now.Add(c.Lease)
		f.rows[row.ID] = row
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeStore) MarkSent(_ context.Context, id string, at time.Time) error {
	return f.update(id, func(e *Email) {
		e.Status = StatusSent
		e.SentAt = &at
		e.LastError = ""
		e.NextTryAt = at
	})
}

func (f *fakeStore) Reschedule(_ context.Context, id string, nextTryAt time.Time, cause string) error {
	return f.update(id, func(e *Email) {
		e.Status = StatusQueued
		e.NextTryAt = nextTryAt
		e.LastError = cause
	})
}

func (f *fakeStore) GiveUp(_ context.Context, id string, cause string) error {
	return f.update(id, func(e *Email) {
		e.Status = StatusFailed
		e.LastError = cause
	})
}

func (f *fakeStore) ByOrder(_ context.Context, orderID string) ([]Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Email{}
	for _, id := range f.urut {
		if f.rows[id].OrderID == orderID {
			out = append(out, f.rows[id])
		}
	}
	return out, nil
}

func (f *fakeStore) Latest(_ context.Context, orderID, jenis string) (Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.urut) - 1; i >= 0; i-- {
		row := f.rows[f.urut[i]]
		if row.OrderID == orderID && row.Jenis == jenis {
			return row, nil
		}
	}
	return Email{}, ErrNotFound
}

func (f *fakeStore) Resend(ctx context.Context, e Envelope, rec audit.Entry) (Email, error) {
	row, err := f.Enqueue(ctx, e)
	if err != nil {
		return Email{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec.EntityID = row.ID
	f.audits = append(f.audits, rec)
	return row, nil
}

func (f *fakeStore) ActiveExists(_ context.Context, orderID, jenis string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.OrderID == orderID && row.Jenis == jenis &&
			(row.Status == StatusQueued || row.Status == StatusSending) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) update(id string, fn func(*Email)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return ErrNotFound
	}
	fn(&row)
	f.rows[id] = row
	return nil
}

func (f *fakeStore) row(t *testing.T, id string) Email {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		t.Fatalf("baris %s tidak ada", id)
	}
	return row
}

func (f *fakeStore) semua() []Email {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Email, 0, len(f.urut))
	for _, id := range f.urut {
		out = append(out, f.rows[id])
	}
	return out
}

// fakeMailer menangkap email yang dikirim dan bisa dipaksa gagal.
type fakeMailer struct {
	mu    sync.Mutex
	sent  []mailer.Message
	gagal error
}

func (m *fakeMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return m.gagal
}

func (m *fakeMailer) Health(context.Context) error { return nil }

func (m *fakeMailer) jumlah() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *fakeMailer) terakhir(t *testing.T) mailer.Message {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("belum ada email yang dikirim")
	}
	return m.sent[len(m.sent)-1]
}

// fakeObjects adalah object storage in-memory untuk lampiran.
type fakeObjects struct {
	isi map[string][]byte
	err error
}

func (o *fakeObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if o.err != nil {
		return nil, o.err
	}
	data, ok := o.isi[key]
	if !ok {
		return nil, errors.New("objek tidak ada")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// fakeOrders adalah pembaca pesanan + dokumen.
type fakeOrders struct {
	ref  OrderRef
	docs []Dokumen
	err  error
}

func (o *fakeOrders) OrderByID(context.Context, string) (OrderRef, error) {
	if o.err != nil {
		return OrderRef{}, o.err
	}
	return o.ref, nil
}

func (o *fakeOrders) Documents(context.Context, string) ([]Dokumen, error) { return o.docs, nil }

// fakeAdmins adalah sumber daftar admin penerima dokumen.
type fakeAdmins struct {
	invoice    []string
	suratJalan []string
}

func (a *fakeAdmins) DocumentRecipients(_ context.Context, kind users.DocKind) ([]string, error) {
	if kind == users.DocInvoice {
		return a.invoice, nil
	}
	return a.suratJalan, nil
}

// ---------- helper uji ----------

func newTestService(t *testing.T, store Store, m mailer.Mailer, clk clock.Clock, opts ...Option) *Service {
	t.Helper()
	base := []Option{WithClock(clk), WithObjectStore(&fakeObjects{}), WithAdminRecipients(&fakeAdmins{})}
	return New(store, m, append(base, opts...)...)
}

func mustEnqueue(t *testing.T, s *Service, e Envelope) Email {
	t.Helper()
	row, err := s.Enqueue(context.Background(), e)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return row
}

// ---------- uji jadwal retry ----------

func TestJadwalRetryBerjenjangSesuaiSystemDesign(t *testing.T) {
	mau := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	for i, d := range mau {
		if got := RetryDelay(i + 1); got != d {
			t.Errorf("RetryDelay(%d) = %v, mau %v", i+1, got, d)
		}
	}
	if got := RetryDelay(9); got != 6*time.Hour {
		t.Errorf("RetryDelay(9) = %v, mau 6h", got)
	}
	if MaxRetry != 5 {
		t.Errorf("MaxRetry = %d, mau 5 (PRD EM-01)", MaxRetry)
	}
	// Lima jeda retry hanya terpakai habis bila email boleh dikirim 6 kali:
	// percobaan pertama + 5 percobaan ulang.
	if MaxAttempts != len(mau)+1 {
		t.Errorf("MaxAttempts = %d, mau %d supaya seluruh jeda terpakai", MaxAttempts, len(mau)+1)
	}
}

// ---------- uji antrean ----------

func TestEnqueueMenolakPenerimaKosongDanJenisTakDikenal(t *testing.T) {
	store := newFakeStore()
	s := newTestService(t, store, &fakeMailer{}, clock.System())

	if _, err := s.Enqueue(context.Background(), Envelope{Jenis: JenisResetPassword}); !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("enqueue tanpa penerima harus ErrNoRecipient, dapat %v", err)
	}
	if _, err := s.Enqueue(context.Background(), Envelope{Jenis: "tidak_ada", To: []string{"a@example.test"}}); !errors.Is(err, ErrKindInvalid) {
		t.Fatalf("jenis tak dikenal harus ErrKindInvalid, dapat %v", err)
	}
	if len(store.semua()) != 0 {
		t.Errorf("tidak boleh ada baris outbox yang dibuat dari permintaan tidak sah")
	}
}

func TestEnqueueMerapikanAlamatDanMengisiTemplate(t *testing.T) {
	store := newFakeStore()
	s := newTestService(t, store, &fakeMailer{}, clock.System())
	row := mustEnqueue(t, s, Envelope{
		Jenis: JenisResetPassword,
		To:    []string{" A@Example.test ", "a@example.test", ""},
	})
	if len(row.To) != 1 || row.To[0] != "a@example.test" {
		t.Fatalf("alamat harus dirapikan & unik, dapat %v", row.To)
	}
	if row.Payload["template"] != JenisResetPassword {
		t.Errorf("template harus terisi otomatis, dapat %v", row.Payload["template"])
	}
	if row.Subject == "" {
		t.Error("subjek baku harus terisi")
	}
}

func TestProsesAntreanMengirimSekaliDanIdempoten(t *testing.T) {
	store := newFakeStore()
	m := &fakeMailer{}
	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	s := newTestService(t, store, m, clk)

	row := mustEnqueue(t, s, Envelope{Jenis: JenisResetPassword, To: []string{"a@example.test"},
		Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=x"}})

	hasil, err := s.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("ProsesAntrean: %v", err)
	}
	if hasil.Terkirim != 1 {
		t.Fatalf("harus 1 email terkirim, hasil %+v", hasil)
	}
	if m.jumlah() != 1 {
		t.Fatalf("mailer harus dipanggil sekali, dapat %d", m.jumlah())
	}
	if got := store.row(t, row.ID); got.Status != StatusSent || got.SentAt == nil {
		t.Fatalf("baris harus sent dengan sent_at terisi, dapat %+v", got)
	}

	// Pemindaian berikutnya tidak boleh mengirim ulang baris yang sudah sent.
	hasil, err = s.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("ProsesAntrean kedua: %v", err)
	}
	if hasil.Diklaim != 0 || m.jumlah() != 1 {
		t.Fatalf("baris sent tidak boleh dikirim ulang (diklaim=%d, kirim=%d)", hasil.Diklaim, m.jumlah())
	}
}

func TestKlaimMencegahKirimGandaSaatDuaPemindaianBersamaan(t *testing.T) {
	store := newFakeStore()
	m := &fakeMailer{}
	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	s := newTestService(t, store, m, clk)

	mustEnqueue(t, s, Envelope{Jenis: JenisUndanganAdmin, To: []string{"admin@example.test"},
		Payload: map[string]any{"tautan": "https://admin.test/lupa?token=y"}})

	// Pemindaian pertama mengklaim baris dan mengirimnya.
	if _, err := s.ProsesAntrean(context.Background(), 0); err != nil {
		t.Fatalf("ProsesAntrean: %v", err)
	}
	// Pemindaian kedua pada waktu yang sama: baris masih dalam masa sewa
	// klaim, jadi TIDAK boleh diklaim lagi meski statusnya belum sent.
	m.gagal = errors.New("smtp sibuk")
	hasil, err := s.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("ProsesAntrean kedua: %v", err)
	}
	if hasil.Diklaim != 0 {
		t.Fatalf("baris dalam masa sewa tidak boleh diklaim ulang, diklaim=%d", hasil.Diklaim)
	}
	if m.jumlah() != 1 {
		t.Fatalf("mailer tidak boleh dipanggil dua kali, dapat %d", m.jumlah())
	}
}

func TestRetryBerjenjangSampaiMenyerahSetelahLimaPercobaanUlang(t *testing.T) {
	store := newFakeStore()
	m := &fakeMailer{gagal: errors.New("smtp mati")}
	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	s := newTestService(t, store, m, clk)

	row := mustEnqueue(t, s, Envelope{Jenis: JenisResetPassword, To: []string{"a@example.test"},
		Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=z"}})

	// Lima percobaan pertama masing-masing gagal dan dijadwalkan ulang dengan
	// jeda berjenjang 1m, 5m, 30m, 2j, 6j.
	mau := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	for i, jeda := range mau {
		hasil, err := s.ProsesAntrean(context.Background(), 0)
		if err != nil {
			t.Fatalf("perulangan %d: %v", i, err)
		}
		if hasil.Diklaim != 1 {
			t.Fatalf("perulangan %d: harus mengklaim 1 baris, dapat %+v", i, hasil)
		}
		got := store.row(t, row.ID)
		if got.Attempts != i+1 {
			t.Fatalf("percobaan ke-%d: attempts = %d", i+1, got.Attempts)
		}
		if got.Status != StatusQueued {
			t.Fatalf("percobaan ke-%d: status harus queued, dapat %s", i+1, got.Status)
		}
		harus := clk.Now().Add(jeda)
		if got.NextTryAt.Sub(harus) > time.Second {
			t.Fatalf("percobaan ke-%d: next_try_at %v, mau sekitar %v", i+1, got.NextTryAt, harus)
		}
		clk.Set(got.NextTryAt)
	}

	// Percobaan ke-6 (percobaan ulang terakhir) menghabiskan jatah: email
	// ditandai gagal, bukan dijadwalkan lagi.
	hasil, err := s.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("percobaan terakhir: %v", err)
	}
	if hasil.Menyerah != 1 {
		t.Fatalf("percobaan terakhir harus menyerah, hasil %+v", hasil)
	}
	got := store.row(t, row.ID)
	if got.Status != StatusFailed {
		t.Fatalf("setelah 5 percobaan ulang status harus failed, dapat %s", got.Status)
	}
	if got.LastError == "" {
		t.Error("last_error harus menjelaskan kegagalan supaya tampil di riwayat")
	}
	if m.jumlah() != MaxAttempts {
		t.Fatalf("mailer harus dipanggil %d kali (1 + 5 retry), dapat %d", MaxAttempts, m.jumlah())
	}

	// Pemindaian berikutnya tidak boleh mencoba lagi.
	hasil, err = s.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("ProsesAntrean setelah menyerah: %v", err)
	}
	if hasil.Diklaim != 0 || m.jumlah() != MaxAttempts {
		t.Fatalf("email failed tidak boleh dicoba lagi (diklaim=%d, kirim=%d)", hasil.Diklaim, m.jumlah())
	}
}

func TestLampiranGagalDibacaMenundaKirim(t *testing.T) {
	store := newFakeStore()
	m := &fakeMailer{}
	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	obj := &fakeObjects{err: errors.New("storage mati")}
	s := New(store, m, WithClock(clk), WithObjectStore(obj), WithAdminRecipients(&fakeAdmins{}))

	row := mustEnqueue(t, s, Envelope{
		Jenis:   JenisInvoiceAdmin,
		To:      []string{"admin@example.test"},
		Payload: map[string]any{"lampiran": []Lampiran{{StorageKey: "k/invoice.pdf", Nama: "Invoice.pdf"}}},
	})
	hasil, err := s.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("ProsesAntrean: %v", err)
	}
	if hasil.Ditunda != 1 || m.jumlah() != 0 {
		t.Fatalf("lampiran yang tak terbaca harus menunda kirim, hasil %+v kirim=%d", hasil, m.jumlah())
	}
	if store.row(t, row.ID).Status != StatusQueued {
		t.Errorf("baris harus kembali queued untuk dicoba lagi")
	}
}

func TestBarisSendingYangPercobaannyaHabisTidakTersangkutSelamanya(t *testing.T) {
	store := newFakeStore()
	m := &fakeMailer{}
	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	s := newTestService(t, store, m, clk)

	row := mustEnqueue(t, s, Envelope{Jenis: JenisResetPassword, To: []string{"a@example.test"},
		Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=x"}})

	// Simulasi worker yang mati tepat setelah klaim terakhir: baris `sending`,
	// percobaan sudah habis, dan sewa klaimnya kedaluwarsa.
	if err := store.update(row.ID, func(e *Email) {
		e.Status = StatusSending
		e.Attempts = MaxAttempts
		e.NextTryAt = clk.Now().Add(-time.Minute)
	}); err != nil {
		t.Fatalf("siapkan baris sending: %v", err)
	}

	hasil, err := s.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("ProsesAntrean: %v", err)
	}
	if hasil.Diklaim != 0 || m.jumlah() != 0 {
		t.Fatalf("percobaan yang sudah habis tidak boleh dikirim lagi (diklaim=%d kirim=%d)", hasil.Diklaim, m.jumlah())
	}
	got := store.row(t, row.ID)
	if got.Status != StatusFailed {
		t.Fatalf("baris sending yang percobaannya habis harus menjadi failed, dapat %s", got.Status)
	}
	if got.LastError == "" {
		t.Error("baris failed harus punya last_error supaya tampil di riwayat dan bisa dikirim ulang")
	}
}

// ---------- uji template ----------
func TestTemplateSetiapJenisMenghasilkanIsiYangTepat(t *testing.T) {
	store := newFakeStore()
	m := &fakeMailer{}
	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	obj := &fakeObjects{isi: map[string][]byte{"k/invoice.pdf": []byte("PDF-ISI")}}
	s := New(store, m, WithClock(clk), WithObjectStore(obj), WithAdminRecipients(&fakeAdmins{}))

	lampiran := []Lampiran{{StorageKey: "k/invoice.pdf", Nama: "Invoice-2026-10-0001.pdf", ContentType: "application/pdf"}}
	kasus := []struct {
		jenis    string
		payload  map[string]any
		harusAda []string
	}{
		{JenisResetPassword, map[string]any{"nama": "Rina", "email": "rina@example.test",
			"tautan": "https://cabang.test/lupa?token=t", "berlaku_menit": 60},
			[]string{"Rina", "https://cabang.test/lupa?token=t", "60 menit"}},
		{JenisUndanganAdmin, map[string]any{"nama": "Budi", "email": "budi@example.test",
			"role": "manager", "tautan": "https://admin.test/lupa?token=u"},
			[]string{"Budi", "manager", "https://admin.test/lupa?token=u"}},
		{JenisInvoiceCabang, map[string]any{"nama": "Kepala A", "nomor": "2026-10-0001",
			"cabang": "Cabang A", "total": int64(350000), "lampiran": lampiran},
			[]string{"2026-10-0001", "Cabang A", "Rp350.000"}},
		{JenisInvoiceAdmin, map[string]any{"nomor": "2026-10-0001", "cabang": "Cabang A",
			"total": int64(1250000), "lampiran": lampiran},
			[]string{"Tim Pusat", "Rp1.250.000", "2026-10-0001"}},
		{JenisSuratJalanAdmin, map[string]any{"nomor": "2026-10-0002", "cabang": "Cabang B",
			"total": int64(1000), "tanggal": "08 Okt 2026", "lampiran": lampiran},
			[]string{"Tim Pusat", "2026-10-0002", "08 Okt 2026"}},
		{JenisReminderCNC, map[string]any{"nama": "Kepala B", "nomor": "2026-10-0003",
			"tahap": "h-2", "jatuh_tempo": "10 Okt 2026", "total": int64(500)},
			[]string{"h-2", "2026-10-0003", "10 Okt 2026", "Rp500"}},
	}

	for _, k := range kasus {
		e := Email{Jenis: k.jenis, To: []string{"x@example.test"}, Subject: DefaultSubject(k.jenis), Payload: k.payload}
		msg, err := s.Render(context.Background(), e)
		if err != nil {
			t.Fatalf("%s: Render: %v", k.jenis, err)
		}
		for _, teks := range k.harusAda {
			if !strings.Contains(msg.Text, teks) {
				t.Errorf("%s: teks harus memuat %q, dapat:\n%s", k.jenis, teks, msg.Text)
			}
			if !strings.Contains(msg.HTML, teks) {
				t.Errorf("%s: HTML harus memuat %q", k.jenis, teks)
			}
		}
		if msg.Subject == "" {
			t.Errorf("%s: subjek tidak boleh kosong", k.jenis)
		}
	}
}

func TestTemplateTakDikenalDitolak(t *testing.T) {
	s := New(newFakeStore(), &fakeMailer{}, WithClock(clock.System()))
	_, err := s.Render(context.Background(), Email{Jenis: "apa_saja", Payload: map[string]any{"template": "apa_saja"}})
	if !errors.Is(err, ErrKindInvalid) {
		t.Fatalf("template tak dikenal harus ErrKindInvalid, dapat %v", err)
	}
}

func TestRenderMelampirkanDokumenDenganNamaDanTipeTepat(t *testing.T) {
	obj := &fakeObjects{isi: map[string][]byte{
		"k/invoice.pdf":      []byte("PDF-ISI"),
		"k/surat_jalan.pdf":  []byte("SJ-PDF"),
		"k/surat_jalan.xlsx": []byte("SJ-XLSX"),
	}}
	s := New(newFakeStore(), &fakeMailer{}, WithClock(clock.System()), WithObjectStore(obj))

	msg, err := s.Render(context.Background(), Email{
		Jenis:   JenisSuratJalanAdmin,
		To:      []string{"admin@example.test"},
		Subject: "Surat Jalan",
		Payload: map[string]any{
			"nomor": "2026-10-0009", "cabang": "Cabang A",
			"lampiran": []Lampiran{
				{StorageKey: "k/surat_jalan.pdf", Nama: "Surat-Jalan-2026-10-0009.pdf", ContentType: "application/pdf"},
				{StorageKey: "k/surat_jalan.xlsx", Nama: "Surat-Jalan-2026-10-0009.xlsx",
					ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(msg.Attachments) != 2 {
		t.Fatalf("harus ada 2 lampiran, dapat %d", len(msg.Attachments))
	}
	if msg.Attachments[0].Name != "Surat-Jalan-2026-10-0009.pdf" || string(msg.Attachments[0].Data) != "SJ-PDF" {
		t.Errorf("lampiran pertama salah: %+v", msg.Attachments[0])
	}
	if msg.Attachments[1].ContentType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("tipe lampiran XLSX salah: %q", msg.Attachments[1].ContentType)
	}
}

// ---------- uji email dokumen + kirim ulang ----------

func fixtureOrder() OrderRef {
	return OrderRef{
		ID: "order-1", Nomor: "2026-10-0001", Cabang: "Cabang A", NamaKepala: "Kepala A",
		CabangEmail: "cabang-a@example.test", Versi: 0, Total: 350000,
		Tanggal: time.Date(2026, 10, 8, 0, 0, 0, 0, clock.Jakarta()),
	}
}

func newOrderService(t *testing.T, store Store, m mailer.Mailer, orders OrderReader, admins AdminRecipients) *Service {
	t.Helper()
	return New(store, m,
		WithClock(clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))),
		WithObjectStore(&fakeObjects{isi: map[string][]byte{"k/invoice.pdf": []byte("PDF")}}),
		WithOrderReader(orders),
		WithAdminRecipients(admins),
	)
}

func TestEnqueueOrderEmailMenungguDokumenSelesai(t *testing.T) {
	store := newFakeStore()
	orders := &fakeOrders{ref: fixtureOrder()} // tanpa dokumen
	s := newOrderService(t, store, &fakeMailer{}, orders, &fakeAdmins{invoice: []string{"sa@example.test"}})

	_, err := s.EnqueueOrderEmail(context.Background(), OrderEmailInput{OrderID: "order-1", Jenis: JenisInvoiceAdmin})
	if !errors.Is(err, ErrDocumentNotReady) {
		t.Fatalf("email dokumen sebelum render selesai harus ErrDocumentNotReady, dapat %v", err)
	}
	if len(store.semua()) != 0 {
		t.Error("tidak boleh ada baris outbox sebelum dokumen siap")
	}
}

func TestEnqueueOrderEmailMenolakTanpaPenerimaYangBerhak(t *testing.T) {
	store := newFakeStore()
	orders := &fakeOrders{ref: fixtureOrder(), docs: []Dokumen{{Jenis: "invoice_pdf", Versi: 0, StorageKey: "k/invoice.pdf"}}}
	// Admin undangan/nonaktif: repository pengguna mengembalikan daftar kosong.
	s := newOrderService(t, store, &fakeMailer{}, orders, &fakeAdmins{})

	_, err := s.EnqueueOrderEmail(context.Background(), OrderEmailInput{OrderID: "order-1", Jenis: JenisInvoiceAdmin})
	if !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("tanpa penerima berhak harus ErrNoRecipient, dapat %v", err)
	}
	if len(store.semua()) != 0 {
		t.Error("email tanpa penerima tidak boleh masuk antrean")
	}
}

func TestEnqueueOrderEmailInvoiceCabangKeEmailCabangSaja(t *testing.T) {
	store := newFakeStore()
	orders := &fakeOrders{ref: fixtureOrder(), docs: []Dokumen{{Jenis: "invoice_pdf", Versi: 0, StorageKey: "k/invoice.pdf"}}}
	s := newOrderService(t, store, &fakeMailer{}, orders, &fakeAdmins{invoice: []string{"sa@example.test"}})

	row, err := s.EnqueueOrderEmail(context.Background(), OrderEmailInput{OrderID: "order-1", Jenis: JenisInvoiceCabang})
	if err != nil {
		t.Fatalf("EnqueueOrderEmail: %v", err)
	}
	if len(row.To) != 1 || row.To[0] != "cabang-a@example.test" {
		t.Fatalf("invoice cabang hanya ke email cabang, dapat %v", row.To)
	}
	lampiran, err := PayloadAttachments(row.Payload)
	if err != nil {
		t.Fatalf("lampiran: %v", err)
	}
	if len(lampiran) != 1 || lampiran[0].Nama != "Invoice-2026-10-0001.pdf" {
		t.Fatalf("lampiran harus tepat satu invoice dengan nama benar, dapat %+v", lampiran)
	}
}

func TestResendOrderAturanOtorisasiDanIdempotensi(t *testing.T) {
	store := newFakeStore()
	orders := &fakeOrders{ref: fixtureOrder(), docs: []Dokumen{{Jenis: "invoice_pdf", Versi: 0, StorageKey: "k/invoice.pdf"}}}
	admins := &fakeAdmins{invoice: []string{"sa@example.test"}}
	s := newOrderService(t, store, &fakeMailer{}, orders, admins)
	ctx := context.Background()

	manager := auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	cabang := auth.Actor{UserID: "usr-1", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "b-1"}

	// 1. Akun cabang tidak boleh mengirim ulang email dokumen.
	if _, err := s.ResendOrder(ctx, cabang, "order-1", JenisInvoiceAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cabang harus ditolak, dapat %v", err)
	}
	// 2. Jenis non-dokumen ditolak.
	if _, err := s.ResendOrder(ctx, manager, "order-1", JenisResetPassword); !errors.Is(err, ErrKindInvalid) {
		t.Fatalf("jenis non-dokumen harus ErrKindInvalid, dapat %v", err)
	}
	// 3. Belum pernah ada email -> tidak boleh memicu email baru.
	if _, err := s.ResendOrder(ctx, manager, "order-1", JenisInvoiceAdmin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("email yang belum pernah ada harus ErrNotFound, dapat %v", err)
	}

	// 4. Setelah email pertama ada DAN sudah terkirim, kirim ulang berhasil dan
	// tercatat di audit.
	if _, err := s.EnqueueOrderEmail(ctx, OrderEmailInput{OrderID: "order-1", Jenis: JenisInvoiceAdmin}); err != nil {
		t.Fatalf("enqueue pertama: %v", err)
	}
	// Masih antre: kirim ulang ditolak supaya tidak menggandakan email.
	if _, err := s.ResendOrder(ctx, manager, "order-1", JenisInvoiceAdmin); !errors.Is(err, ErrAlreadyQueued) {
		t.Fatalf("saat email masih antre kirim ulang harus ErrAlreadyQueued, dapat %v", err)
	}
	// Pemindaian worker mengirim email pertama sehingga riwayat punya `sent`.
	pengirim := newOrderService(t, store, &fakeMailer{}, orders, admins)
	if _, err := pengirim.ProsesAntrean(ctx, 0); err != nil {
		t.Fatalf("ProsesAntrean email pertama: %v", err)
	}

	ulang, err := s.ResendOrder(ctx, manager, "order-1", JenisInvoiceAdmin)
	if err != nil {
		t.Fatalf("ResendOrder: %v", err)
	}
	if ulang.ID == "" || ulang.Status != StatusQueued {
		t.Fatalf("baris kirim ulang harus queued, dapat %+v", ulang)
	}
	if len(store.audits) != 1 {
		t.Fatalf("harus ada 1 entri audit, dapat %d", len(store.audits))
	}
	rec := store.audits[0]
	if rec.Entity != audit.EntityEmail || rec.Action != audit.ActionResend || rec.EntityID != ulang.ID {
		t.Fatalf("audit kirim ulang salah: %+v", rec)
	}
	if rec.ActorID != "adm-1" {
		t.Errorf("aktor audit harus admin yang meminta, dapat %q", rec.ActorID)
	}

	// 5. Klik ganda saat masih antre ditolak supaya email tidak berganda.
	if _, err := s.ResendOrder(ctx, manager, "order-1", JenisInvoiceAdmin); !errors.Is(err, ErrAlreadyQueued) {
		t.Fatalf("kirim ulang ganda harus ErrAlreadyQueued, dapat %v", err)
	}
	if len(store.semua()) != 2 {
		t.Fatalf("harus tepat 2 baris outbox, dapat %d", len(store.semua()))
	}

	// 6. Setelah semua terkirim, kirim ulang boleh lagi (riwayat tetap utuh).
	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	s2 := New(store, &fakeMailer{},
		WithClock(clk),
		WithObjectStore(&fakeObjects{isi: map[string][]byte{"k/invoice.pdf": []byte("PDF")}}),
		WithOrderReader(orders),
		WithAdminRecipients(admins))
	if _, err := s2.ProsesAntrean(ctx, 0); err != nil {
		t.Fatalf("ProsesAntrean: %v", err)
	}
	for _, row := range store.semua() {
		if row.Status != StatusSent {
			t.Fatalf("semua baris harus sent, dapat %s", row.Status)
		}
	}
	if _, err := s.ResendOrder(ctx, manager, "order-1", JenisInvoiceAdmin); err != nil {
		t.Fatalf("kirim ulang setelah sent harus boleh: %v", err)
	}
}

func TestResendOrderMemakaiPenerimaYangBerhakSaatItu(t *testing.T) {
	store := newFakeStore()
	orders := &fakeOrders{ref: fixtureOrder(), docs: []Dokumen{{Jenis: "invoice_pdf", Versi: 0, StorageKey: "k/invoice.pdf"}}}
	// Awalnya hanya superadmin yang ber-flag.
	admins := &fakeAdmins{invoice: []string{"sa@example.test"}}
	s := newOrderService(t, store, &fakeMailer{}, orders, admins)
	ctx := context.Background()
	manager := auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleManager}

	if _, err := s.EnqueueOrderEmail(ctx, OrderEmailInput{OrderID: "order-1", Jenis: JenisInvoiceAdmin}); err != nil {
		t.Fatalf("enqueue pertama: %v", err)
	}
	// Kirim email pertama supaya barisnya `sent` (bukan lagi dalam antrean).
	pengirim := newOrderService(t, store, &fakeMailer{}, orders, admins)
	if _, err := pengirim.ProsesAntrean(ctx, 0); err != nil {
		t.Fatalf("ProsesAntrean email pertama: %v", err)
	}
	// Keadaan berubah: SA nonaktif, flag pindah ke manager lain.
	admins.invoice = []string{"manager2@example.test"}

	ulang, err := s.ResendOrder(ctx, manager, "order-1", JenisInvoiceAdmin)
	if err != nil {
		t.Fatalf("ResendOrder: %v", err)
	}
	if len(ulang.To) != 1 || ulang.To[0] != "manager2@example.test" {
		t.Fatalf("penerima harus dihitung ulang, dapat %v", ulang.To)
	}
}

func TestListByOrderButuhKapabilitasAdminOrders(t *testing.T) {
	store := newFakeStore()
	s := New(store, &fakeMailer{}, WithClock(clock.System()))
	ctx := context.Background()

	cabang := auth.Actor{UserID: "u", Kind: auth.KindBranch, Role: auth.RoleBranch}
	if _, err := s.ListByOrder(ctx, cabang, "order-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cabang harus ditolak, dapat %v", err)
	}
	mgr := auth.Actor{UserID: "a", Kind: auth.KindAdmin, Role: auth.RoleManager}
	list, err := s.ListByOrder(ctx, mgr, "order-1")
	if err != nil {
		t.Fatalf("manager harus boleh membaca riwayat: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("riwayat kosong diharapkan, dapat %d baris", len(list))
	}
}

// ---------- uji adapter ----------

func TestResetMailerMengantreDanTidakMengirimLangsung(t *testing.T) {
	store := newFakeStore()
	m := &fakeMailer{}
	s := New(store, m, WithClock(clock.System()))
	adapter := NewResetMailer(s, "https://cabang.test", "https://admin.test", "/lupa")

	if err := adapter.SendPasswordReset(context.Background(), auth.ResetMessage{
		To: "a@example.test", Nama: "Rina", Kind: auth.KindBranch, Token: "tok-123", TTL: 60 * time.Minute,
	}); err != nil {
		t.Fatalf("SendPasswordReset: %v", err)
	}
	if m.jumlah() != 0 {
		t.Fatal("adapter tidak boleh mengirim email langsung; worker yang mengirim")
	}
	rows := store.semua()
	if len(rows) != 1 || rows[0].Jenis != JenisResetPassword {
		t.Fatalf("harus ada 1 baris outbox reset_password, dapat %+v", rows)
	}
	if got := rows[0].Payload["tautan"]; got != "https://cabang.test/lupa?token=tok-123" {
		t.Errorf("tautan reset harus memakai portal cabang, dapat %v", got)
	}
}

func TestQueueMengantreUndanganAdmin(t *testing.T) {
	store := newFakeStore()
	s := New(store, &fakeMailer{}, WithClock(clock.System()))
	q := NewQueue(s)

	if err := q.Enqueue(context.Background(), users.QueuedEmail{
		Jenis: users.JenisUndanganAdmin,
		To:    []string{"baru@example.test"},
		Payload: map[string]any{
			"template": "undangan_admin", "tautan": "https://admin.test/lupa?token=inv",
		},
	}); err != nil {
		t.Fatalf("Enqueue undangan: %v", err)
	}
	rows := store.semua()
	if len(rows) != 1 || rows[0].Jenis != JenisUndanganAdmin {
		t.Fatalf("harus ada baris undangan_admin, dapat %+v", rows)
	}
	if rows[0].Subject == "" {
		t.Error("subjek undangan harus terisi")
	}
}

func TestRupiahDanTanggalIndonesia(t *testing.T) {
	if got := rupiah(1250000); got != "Rp1.250.000" {
		t.Errorf("rupiah(1250000) = %q", got)
	}
	if got := tanggalID(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)); got != "08 Okt 2026" {
		t.Errorf("tanggalID = %q", got)
	}
}
