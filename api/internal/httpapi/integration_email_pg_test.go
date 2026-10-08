package httpapi

// Uji integrasi email outbox (F1-16) pada PostgreSQL nyata.
//
// Cakupan: email tidak pernah dikirim langsung dari handler, tautan reset &
// undangan benar-benar lahir dari baris email_outbox, worker mengirimkannya,
// aturan penerima email dokumen, kirim ulang oleh admin beserta audit, serta
// email gagal yang tampak di riwayat dan bisa dikirim ulang.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/notification"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/users"
)

// ---------- perekam + penerus ke outbox ----------

// resetSpy mencatat permintaan tautan atur ulang DAN meneruskannya ke outbox
// produksi, sehingga pengujian lama tetap bisa membaca token sementara jalur
// email yang sebenarnya (email_outbox + worker) tetap teruji.
type resetSpy struct {
	delegat *notification.ResetMailer

	mu   sync.Mutex
	msgs []auth.ResetMessage
}

func (r *resetSpy) SendPasswordReset(ctx context.Context, m auth.ResetMessage) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
	return r.delegat.SendPasswordReset(ctx, m)
}

func (r *resetSpy) jumlah() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func (r *resetSpy) tokenTerakhir(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.msgs) == 0 {
		t.Fatal("tidak ada permintaan tautan atur ulang")
	}
	return r.msgs[len(r.msgs)-1].Token
}

// queueSpy mencatat email pengelolaan pengguna DAN meneruskannya ke outbox.
type queueSpy struct {
	delegat *notification.Queue

	mu     sync.Mutex
	queued []users.QueuedEmail
}

func (q *queueSpy) Enqueue(ctx context.Context, m users.QueuedEmail) error {
	q.mu.Lock()
	q.queued = append(q.queued, m)
	q.mu.Unlock()
	return q.delegat.Enqueue(ctx, m)
}

func (q *queueSpy) terakhir(t *testing.T) users.QueuedEmail {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queued) == 0 {
		t.Fatal("tidak ada email yang diantre")
	}
	return q.queued[len(q.queued)-1]
}

// fakeSender adalah pengirim email pengganti SMTP yang mencatat apa yang
// benar-benar dikirim worker dan bisa dipaksa gagal.
type fakeSender struct {
	mu    sync.Mutex
	pesan []mailer.Message
	gagal error
}

func (s *fakeSender) Send(_ context.Context, msg mailer.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pesan = append(s.pesan, msg)
	return s.gagal
}

func (s *fakeSender) Health(context.Context) error { return nil }

func (s *fakeSender) jumlah() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pesan)
}

func (s *fakeSender) terakhir(t *testing.T) mailer.Message {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pesan) == 0 {
		t.Fatal("belum ada email yang benar-benar dikirim")
	}
	return s.pesan[len(s.pesan)-1]
}

func (s *fakeSender) setGagal(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gagal = err
}

// ---------- helper fixture ----------

// kirimAntrean menjalankan satu siklus worker pengirim email.
func (f *pgFixture) kirimAntrean(t *testing.T) notification.Hasil {
	t.Helper()
	hasil, err := f.notif.ProsesAntrean(context.Background(), 0)
	if err != nil {
		t.Fatalf("ProsesAntrean: %v", err)
	}
	return hasil
}

// emailTerakhir membaca baris email_outbox terbaru untuk satu pesanan+jenis.
func (f *pgFixture) emailTerakhir(t *testing.T, orderID, jenis string) notification.Email {
	t.Helper()
	var row notification.Email
	var payload []byte
	var lastError *string
	ctx := context.Background()
	err := f.pool.QueryRow(ctx, `SELECT id, jenis::text, to_addr, subject, payload, status::text, attempts, last_error
		FROM email_outbox WHERE order_id = $1::uuid AND jenis = $2::email_kind
		ORDER BY created_at DESC, id DESC LIMIT 1`, orderID, jenis).
		Scan(&row.ID, &row.Jenis, &row.To, &row.Subject, &payload, &row.Status, &row.Attempts, &lastError)
	if err != nil {
		t.Fatalf("baca email_outbox %s: %v", jenis, err)
	}
	if lastError != nil {
		row.LastError = *lastError
	}
	if err := json.Unmarshal(payload, &row.Payload); err != nil {
		t.Fatalf("urai payload email: %v", err)
	}
	return row
}

// emailTerbaruJenis membaca baris email_outbox terbaru untuk satu jenis
// (dipakai email yang tidak terikat pesanan: reset & undangan).
func (f *pgFixture) emailTerbaruJenis(t *testing.T, jenis string) notification.Email {
	t.Helper()
	var row notification.Email
	var payload []byte
	ctx := context.Background()
	err := f.pool.QueryRow(ctx, `SELECT id, to_addr, subject, payload, status::text, attempts
		FROM email_outbox WHERE jenis = $1::email_kind ORDER BY created_at DESC, id DESC LIMIT 1`, jenis).
		Scan(&row.ID, &row.To, &row.Subject, &payload, &row.Status, &row.Attempts)
	if err != nil {
		t.Fatalf("baca email_outbox %s: %v", jenis, err)
	}
	if err := json.Unmarshal(payload, &row.Payload); err != nil {
		t.Fatalf("urai payload email: %v", err)
	}
	row.Jenis = jenis
	return row
}

func (f *pgFixture) hitungEmail(t *testing.T, jenis string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM email_outbox WHERE jenis = $1::email_kind`, jenis).Scan(&n); err != nil {
		t.Fatalf("hitung email %s: %v", jenis, err)
	}
	return n
}

// seedPesanan membuat satu pesanan Submitted beserta dokumen yang diminta.
func (f *pgFixture) seedPesanan(t *testing.T, nomor string, docKinds ...string) string {
	t.Helper()
	ctx := context.Background()
	var orderID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO orders (branch_id, nomor, versi, status, tanggal, total)
		VALUES ($1, $2, 0, 'submitted', current_date, 350000) RETURNING id`, f.branchA, nomor).Scan(&orderID); err != nil {
		t.Fatalf("seed pesanan: %v", err)
	}
	for _, kind := range docKinds {
		key := fmt.Sprintf("orders/%s/%s", orderID, kind)
		lampiran := []byte("isi-" + kind)
		if err := f.objek.Put(ctx, key, strings.NewReader(string(lampiran)), int64(len(lampiran)), "application/octet-stream"); err != nil {
			t.Fatalf("simpan dokumen %s: %v", kind, err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO documents (order_id, jenis, versi, storage_key)
			VALUES ($1::uuid, $2::doc_kind, 0, $3)`, orderID, kind, key); err != nil {
			t.Fatalf("seed dokumen %s: %v", kind, err)
		}
	}
	return orderID
}

func (f *pgFixture) setAdminFlag(t *testing.T, adminID string, invoice, suratJalan bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE admin_users SET notif_invoice = $2, notif_surat_jalan = $3 WHERE id = $1::uuid`,
		adminID, invoice, suratJalan); err != nil {
		t.Fatalf("set flag notifikasi admin: %v", err)
	}
}

// ---------- uji ----------

func TestPGLupaKataSandiLewatOutboxTidakDikirimLangsung(t *testing.T) {
	f := newPGFixture(t)

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/forgot", `{"email":"a@example.test"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("lupa kata sandi harus 200, dapat %d (%v)", status, body)
	}

	// Email masuk antrean, belum ada satu pun yang benar-benar dikirim.
	if n := f.hitungEmail(t, notification.JenisResetPassword); n != 1 {
		t.Fatalf("harus ada 1 baris outbox reset_password, dapat %d", n)
	}
	if f.kirim.jumlah() != 0 {
		t.Fatalf("handler tidak boleh mengirim email langsung, sudah terkirim %d", f.kirim.jumlah())
	}
	row := f.emailTerbaruJenis(t, notification.JenisResetPassword)
	tautan, _ := row.Payload["tautan"].(string)
	if !strings.Contains(tautan, "grahita-branch") || !strings.Contains(tautan, "token=") {
		t.Fatalf("tautan reset harus menunjuk portal cabang dan memuat token, dapat %q", tautan)
	}
	if len(row.To) != 1 || row.To[0] != "a@example.test" {
		t.Fatalf("penerima reset salah: %v", row.To)
	}

	// Worker mengirim; email yang benar-benar keluar memuat tautan yang sama.
	hasil := f.kirimAntrean(t)
	if hasil.Terkirim != 1 || f.kirim.jumlah() != 1 {
		t.Fatalf("worker harus mengirim 1 email, hasil %+v kirim=%d", hasil, f.kirim.jumlah())
	}
	pesan := f.kirim.terakhir(t)
	if !strings.Contains(pesan.Text, tautan) {
		t.Fatalf("email terkirim harus memuat tautan reset")
	}
	if terkirim := f.emailTerbaruJenis(t, notification.JenisResetPassword); terkirim.Status != notification.StatusSent {
		t.Fatalf("baris outbox harus sent, dapat %s", terkirim.Status)
	}

	// Tautan dari email benar-benar bisa dipakai sekali.
	token := tokenDariTautan(t, tautan)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("atur ulang harus 204, dapat %d (%v)", status, body)
	}
	pgLogin(t, f.srv, "a@example.test", "sandibaru123")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandilain123"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("tautan yang sudah dipakai harus 400, dapat %d", status)
	}
}

func TestPGLupaKataSandiAkunTidakAdaTidakMengantreEmail(t *testing.T) {
	f := newPGFixture(t)

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/forgot", `{"email":"tidak-ada@example.test"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("akun tidak ada harus tetap 200, dapat %d (%v)", status, body)
	}
	if n := f.hitungEmail(t, notification.JenisResetPassword); n != 0 {
		t.Errorf("email ke akun tak dikenal tidak boleh diantre, dapat %d", n)
	}
	if f.kirim.jumlah() != 0 {
		t.Errorf("tidak boleh ada email terkirim, dapat %d", f.kirim.jumlah())
	}
}

func TestPGUndanganAdminDiantreLaluDikirimWorker(t *testing.T) {
	f := newPGFixture(t)
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Dewi","email":"dewi@example.test","role":"manager","notif_invoice":true,"notif_surat_jalan":true}`,
		cookiesSA, csrfSA)
	if status != http.StatusCreated {
		t.Fatalf("undang admin harus 201, dapat %d (%v)", status, body)
	}
	if f.kirim.jumlah() != 0 {
		t.Fatal("undangan tidak boleh dikirim langsung dari handler")
	}

	row := f.emailTerbaruJenis(t, notification.JenisUndanganAdmin)
	if len(row.To) != 1 || row.To[0] != "dewi@example.test" {
		t.Fatalf("penerima undangan salah: %v", row.To)
	}
	tautan, _ := row.Payload["tautan"].(string)
	if !strings.Contains(tautan, "grahita-admin") {
		t.Fatalf("tautan undangan harus menunjuk portal admin, dapat %q", tautan)
	}

	// Admin undangan belum bisa login & belum menerima email dokumen.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"dewi@example.test","password":"sandibaru123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("admin undangan tidak boleh login, dapat %d", status)
	}
	invoice, err := f.users.DocumentRecipients(context.Background(), users.DocInvoice)
	if err != nil {
		t.Fatalf("penerima invoice: %v", err)
	}
	for _, alamat := range invoice {
		if alamat == "dewi@example.test" {
			t.Fatal("admin yang belum diaktivasi tidak boleh menjadi penerima dokumen")
		}
	}

	// Worker mengirim undangan, lalu tautannya bisa dipakai mengaktivasi.
	if hasil := f.kirimAntrean(t); hasil.Terkirim != 1 {
		t.Fatalf("worker harus mengirim 1 undangan, hasil %+v", hasil)
	}
	pesan := f.kirim.terakhir(t)
	if pesan.Subject == "" || !strings.Contains(pesan.Text, tautan) {
		t.Fatal("email undangan terkirim harus memuat tautan aktivasi")
	}
	token := tokenDariTautan(t, tautan)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("aktivasi undangan harus 204, dapat %d (%v)", status, body)
	}
	pgLogin(t, f.srv, "dewi@example.test", "sandibaru123")
}

func TestPGKirimUlangUndanganAdminIdempoten(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Undangan pertama "gagal" di sisi email: admin tetap ada dan tidak bisa
	// login, tapi email tidak pernah terkirim.
	f.kirim.setGagal(fmt.Errorf("smtp mati"))
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Eka","email":"eka@example.test","role":"manager"}`, cookiesSA, csrfSA)
	if status != http.StatusCreated {
		t.Fatalf("undang admin harus 201, dapat %d (%v)", status, body)
	}
	if hasil := f.kirimAntrean(t); hasil.Terkirim != 0 {
		t.Fatalf("pengiriman harus gagal pada tahap ini, hasil %+v", hasil)
	}
	var adminID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM admin_users WHERE email = 'eka@example.test'`).Scan(&adminID); err != nil {
		t.Fatalf("baca admin undangan: %v", err)
	}

	// Jalan pemulihan resmi: kirim ulang undangan membuat tautan baru dan
	// mengantre email baru TANPA membuat akun kedua.
	f.kirim.setGagal(nil)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/admin/"+adminID+"/resend", "", cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("kirim ulang undangan harus 200, dapat %d (%v)", status, body)
	}
	if n := f.hitungEmail(t, notification.JenisUndanganAdmin); n != 2 {
		t.Fatalf("harus ada 2 baris undangan (asli + kirim ulang), dapat %d", n)
	}
	var jumlahAkun int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM admin_users WHERE email = 'eka@example.test'`).Scan(&jumlahAkun); err != nil {
		t.Fatalf("hitung akun: %v", err)
	}
	if jumlahAkun != 1 {
		t.Fatalf("kirim ulang tidak boleh membuat akun baru, dapat %d", jumlahAkun)
	}
	if hasil := f.kirimAntrean(t); hasil.Terkirim != 1 {
		t.Fatalf("email undangan baru harus terkirim, hasil %+v", hasil)
	}

	// Tautan terbaru mengaktivasi akun; yang lama sudah tidak berlaku.
	row := f.emailTerbaruJenis(t, notification.JenisUndanganAdmin)
	tautan, _ := row.Payload["tautan"].(string)
	token := tokenDariTautan(t, tautan)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("aktivasi lewat tautan terbaru harus 204, dapat %d (%v)", status, body)
	}
	pgLogin(t, f.srv, "eka@example.test", "sandibaru123")

	// Akun yang sudah aktif bukan lagi sasaran kirim ulang undangan.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/admin/"+adminID+"/resend", "", cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("kirim ulang undangan untuk akun aktif harus 409, dapat %d (%v)", status, body)
	}
	if !strings.Contains(fmt.Sprint(body), "NOT_INVITED") {
		t.Errorf("kode error harus NOT_INVITED, dapat %v", body)
	}
}

func TestPGEmailDokumenHanyaKePenerimaYangBerhak(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Hanya superadmin aktif yang ber-flag; manager undangan dan manager
	// nonaktif sama-sama ber-flag tetapi TIDAK boleh menerima.
	f.setAdminFlag(t, f.adminSA, true, true)
	var undanganID, nonaktifID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM admin_users WHERE email = 'undang@example.test'`).Scan(&undanganID); err != nil {
		t.Fatalf("baca admin undangan: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT id FROM admin_users WHERE email = 'mantan@example.test'`).Scan(&nonaktifID); err != nil {
		t.Fatalf("baca admin nonaktif: %v", err)
	}
	f.setAdminFlag(t, undanganID, true, true)
	f.setAdminFlag(t, nonaktifID, true, true)

	orderID := f.seedPesanan(t, "2026-10-0001", "invoice_pdf", "surat_jalan_pdf", "surat_jalan_xlsx")

	invoiceAdmin, err := f.notif.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: orderID, Jenis: notification.JenisInvoiceAdmin})
	if err != nil {
		t.Fatalf("enqueue invoice admin: %v", err)
	}
	if len(invoiceAdmin.To) != 1 || invoiceAdmin.To[0] != "rina@example.test" {
		t.Fatalf("hanya superadmin aktif ber-flag yang boleh menerima, dapat %v", invoiceAdmin.To)
	}

	suratJalan, err := f.notif.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: orderID, Jenis: notification.JenisSuratJalanAdmin})
	if err != nil {
		t.Fatalf("enqueue surat jalan: %v", err)
	}
	if len(suratJalan.To) != 1 || suratJalan.To[0] != "rina@example.test" {
		t.Fatalf("surat jalan hanya ke admin berhak, dapat %v", suratJalan.To)
	}
	lampiran, err := notification.PayloadAttachments(suratJalan.Payload)
	if err != nil {
		t.Fatalf("lampiran surat jalan: %v", err)
	}
	if len(lampiran) != 2 {
		t.Fatalf("surat jalan harus 2 lampiran (PDF + XLSX), dapat %d", len(lampiran))
	}

	invoiceCabang, err := f.notif.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: orderID, Jenis: notification.JenisInvoiceCabang})
	if err != nil {
		t.Fatalf("enqueue invoice cabang: %v", err)
	}
	if len(invoiceCabang.To) != 1 || invoiceCabang.To[0] != "cabang-a@example.test" {
		t.Fatalf("invoice cabang hanya ke email cabang, dapat %v", invoiceCabang.To)
	}

	// Semua penerima yang tersimpan di outbox juga harus berhak.
	var salahAlamat int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM email_outbox e
		WHERE e.jenis IN ('invoice_admin','surat_jalan_admin')
		  AND EXISTS (SELECT 1 FROM unnest(e.to_addr) a
		              WHERE a IN ('undang@example.test','mantan@example.test'))`).Scan(&salahAlamat); err != nil {
		t.Fatalf("periksa penerima tak berhak: %v", err)
	}
	if salahAlamat != 0 {
		t.Fatalf("tidak boleh ada email ke admin undangan/nonaktif, ditemukan %d", salahAlamat)
	}
}

func TestPGResendEmailDokumenRBACRiwayatDanAudit(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	f.setAdminFlag(t, f.adminSA, true, true)
	orderID := f.seedPesanan(t, "2026-10-0002", "invoice_pdf")

	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	cookiesCabang, csrfCabang := pgLogin(t, f.srv, "a@example.test", "rahasia123")

	// Akun cabang ditolak.
	status, _, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails/invoice_admin/resend", "", cookiesCabang, csrfCabang)
	if status != http.StatusForbidden {
		t.Fatalf("cabang harus 403, dapat %d", status)
	}
	// Jenis email yang tidak dikenal ditolak dengan 400.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails/apa_saja/resend", "", cookiesMgr, csrfMgr)
	if status != http.StatusBadRequest {
		t.Fatalf("jenis tidak dikenal harus 400, dapat %d (%v)", status, body)
	}
	// Belum pernah ada email jenis itu: 404, bukan email baru.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails/invoice_admin/resend", "", cookiesMgr, csrfMgr)
	if status != http.StatusNotFound {
		t.Fatalf("tanpa email sebelumnya harus 404, dapat %d (%v)", status, body)
	}

	// Setelah email pertama dikirim, kirim ulang berhasil dan tercatat di audit.
	if _, err := f.notif.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: orderID, Jenis: notification.JenisInvoiceAdmin}); err != nil {
		t.Fatalf("enqueue invoice admin: %v", err)
	}
	if hasil := f.kirimAntrean(t); hasil.Terkirim != 1 {
		t.Fatalf("email pertama harus terkirim, hasil %+v", hasil)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails/invoice_admin/resend", "", cookiesMgr, csrfMgr)
	if status != http.StatusCreated {
		t.Fatalf("kirim ulang harus 201, dapat %d (%v)", status, body)
	}
	ulang := f.emailTerakhir(t, orderID, notification.JenisInvoiceAdmin)
	if ulang.Status != notification.StatusQueued {
		t.Fatalf("baris kirim ulang harus queued, dapat %s", ulang.Status)
	}
	var (
		entity  string
		action  string
		actorID string
		emailID string
	)
	if err := f.pool.QueryRow(ctx, `SELECT entity, action, actor_id::text, entity_id::text
		FROM audit_logs WHERE entity = 'email' AND action = 'resend'
		ORDER BY at DESC LIMIT 1`).Scan(&entity, &action, &actorID, &emailID); err != nil {
		t.Fatalf("baca audit kirim ulang: %v", err)
	}
	if entity != audit.EntityEmail || action != audit.ActionResend || emailID != ulang.ID {
		t.Fatalf("audit kirim ulang salah: entity=%s action=%s entity_id=%s (email %s)", entity, action, emailID, ulang.ID)
	}
	if actorID == "" {
		t.Error("audit harus mencatat admin yang meminta kirim ulang")
	}

	// Klik ganda saat masih antre ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails/invoice_admin/resend", "", cookiesMgr, csrfMgr)
	if status != http.StatusConflict || !strings.Contains(fmt.Sprint(body), "EMAIL_ALREADY_QUEUED") {
		t.Fatalf("klik ganda harus 409 EMAIL_ALREADY_QUEUED, dapat %d (%v)", status, body)
	}

	// Riwayat email pesanan tampil untuk admin, ditolak untuk cabang.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails", "", cookiesMgr, "")
	if status != http.StatusOK {
		t.Fatalf("riwayat email harus 200, dapat %d (%v)", status, body)
	}
	daftar := dataList(t, body)
	if len(daftar) != 2 {
		t.Fatalf("riwayat harus memuat 2 baris (terkirim + antre), dapat %d", len(daftar))
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails", "", cookiesCabang, "")
	if status != http.StatusForbidden {
		t.Fatalf("cabang membaca riwayat email harus 403, dapat %d", status)
	}
}

func TestPGEmailGagalTampilDiRiwayatDanBisaDikirimUlang(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	f.setAdminFlag(t, f.adminSA, true, true)
	orderID := f.seedPesanan(t, "2026-10-0003", "invoice_pdf")

	if _, err := f.notif.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: orderID, Jenis: notification.JenisInvoiceAdmin}); err != nil {
		t.Fatalf("enqueue invoice admin: %v", err)
	}
	// Pengiriman gagal total: seluruh percobaan habis (1 + 5 retry) lalu status
	// failed — keadaan akhir disimulasikan langsung supaya uji tidak menunggu
	// jeda 6 jam.
	row := f.emailTerakhir(t, orderID, notification.JenisInvoiceAdmin)
	if _, err := f.pool.Exec(ctx, `UPDATE email_outbox
		SET status = 'failed', attempts = $2, last_error = 'smtp mati' WHERE id = $1::uuid`,
		row.ID, notification.MaxAttempts); err != nil {
		t.Fatalf("tandai email gagal: %v", err)
	}

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails", "", cookiesSA, "")
	if status != http.StatusOK {
		t.Fatalf("riwayat email harus 200, dapat %d (%v)", status, body)
	}
	daftar := dataList(t, body)
	if len(daftar) != 1 {
		t.Fatalf("riwayat harus 1 baris, dapat %d", len(daftar))
	}
	gagal, _ := daftar[0].(map[string]any)
	if gagal["status"] != notification.StatusFailed {
		t.Fatalf("status di riwayat harus failed, dapat %v", gagal["status"])
	}
	if gagal["last_error"] != "smtp mati" {
		t.Errorf("riwayat harus menjelaskan sebab kegagalan, dapat %v", gagal["last_error"])
	}

	// Kirim ulang berhasil dan benar-benar terkirim oleh worker.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/emails/invoice_admin/resend", "", cookiesSA, csrfSA)
	if status != http.StatusCreated {
		t.Fatalf("kirim ulang email gagal harus 201, dapat %d (%v)", status, body)
	}
	hasil := f.kirimAntrean(t)
	if hasil.Terkirim != 1 {
		t.Fatalf("email kirim ulang harus terkirim, hasil %+v", hasil)
	}
	pesan := f.kirim.terakhir(t)
	if len(pesan.Attachments) != 1 || pesan.Attachments[0].Name != "Invoice-2026-10-0003.pdf" {
		t.Fatalf("lampiran email kirim ulang harus tepat, dapat %+v", pesan.Attachments)
	}
	if terkirim := f.emailTerakhir(t, orderID, notification.JenisInvoiceAdmin); terkirim.Status != notification.StatusSent {
		t.Fatalf("baris terbaru harus sent, dapat %s", terkirim.Status)
	}
}

func TestPGResendEmailDokumenMenungguRenderDanPenerima(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Dokumen belum ada: email belum boleh diantre, apalagi dikirim.
	orderTanpaDokumen := f.seedPesanan(t, "2026-10-0004")
	f.setAdminFlag(t, f.adminSA, true, true)
	_, err := f.notif.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: orderTanpaDokumen, Jenis: notification.JenisInvoiceAdmin})
	if err == nil || !strings.Contains(err.Error(), "belum selesai dirender") {
		t.Fatalf("dokumen belum siap harus ditolak, dapat %v", err)
	}

	// Dokumen ada tetapi tidak ada admin ber-flag yang berhak: ditolak.
	order := f.seedPesanan(t, "2026-10-0005", "invoice_pdf")
	f.setAdminFlag(t, f.adminSA, false, false)
	_, err = f.notif.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: order, Jenis: notification.JenisInvoiceAdmin})
	if err == nil || !strings.Contains(err.Error(), "tidak ada penerima") {
		t.Fatalf("tanpa penerima berhak harus ditolak, dapat %v", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox`).Scan(&n); err != nil {
		t.Fatalf("hitung outbox: %v", err)
	}
	if n != 0 {
		t.Fatalf("tidak boleh ada email diantre pada kedua kasus di atas, dapat %d", n)
	}
}

func TestPGKirimUlangUndanganDitolakUntukAkunNonaktif(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	var nonaktifID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM admin_users WHERE email = 'mantan@example.test'`).Scan(&nonaktifID); err != nil {
		t.Fatalf("baca admin nonaktif: %v", err)
	}
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/admin/"+nonaktifID+"/resend", "", cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("kirim ulang untuk admin nonaktif harus 409, dapat %d (%v)", status, body)
	}
	if n := f.hitungEmail(t, notification.JenisUndanganAdmin); n != 0 {
		t.Fatalf("tidak boleh ada email undangan untuk admin nonaktif, dapat %d", n)
	}

	// Manager (bukan Superadmin) ditolak oleh kapabilitas.
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/users/admin/"+nonaktifID+"/resend", "", cookiesMgr, csrfMgr)
	if status != http.StatusForbidden {
		t.Fatalf("manager kirim ulang undangan harus 403, dapat %d", status)
	}
}

// dataList/dataMap sudah tersedia dari router_catalog_test.go.
