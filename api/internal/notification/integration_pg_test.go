package notification_test

// Uji integrasi outbox email pada PostgreSQL nyata (F1-16).
//
// Yang diuji di sini hanya bisa dibuktikan dengan SQL sungguhan: klaim paralel
// `FOR UPDATE SKIP LOCKED`, pemulihan baris `sending` yang ditinggal proses
// mati, jadwal retry yang benar-benar tersimpan, dan rollback transaksi bisnis
// yang tidak boleh mengirim email.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	authpg "github.com/grahita/portal/api/internal/auth/postgres"
	"github.com/grahita/portal/api/internal/notification"
	notificationpg "github.com/grahita/portal/api/internal/notification/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/mailer"
	userspg "github.com/grahita/portal/api/internal/users/postgres"
)

// ---------- harness skema uji ----------

func skemaUji(t *testing.T) (*database.Pool, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi outbox email dilewati")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	adminPool, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi database uji: %v", err)
	}
	t.Cleanup(adminPool.Close)

	skema := fmt.Sprintf("f116_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+skema); err != nil {
		t.Fatalf("buat skema uji: %v", err)
	}
	t.Cleanup(func() {
		bersih, batal := context.WithTimeout(context.Background(), 30*time.Second)
		defer batal()
		if _, err := adminPool.Exec(bersih, `DROP SCHEMA IF EXISTS `+skema+` CASCADE`); err != nil {
			t.Logf("gagal menghapus skema uji %s: %v", skema, err)
		}
	})
	if _, err := adminPool.Exec(ctx, `CREATE TABLE `+skema+`.goose_db_version (
		id serial NOT NULL, version_id bigint NOT NULL, is_applied boolean NOT NULL,
		tstamp timestamp NULL DEFAULT now(), PRIMARY KEY (id))`); err != nil {
		t.Fatalf("buat tabel versi goose: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `INSERT INTO `+skema+`.goose_db_version (version_id, is_applied) VALUES (0, true)`); err != nil {
		t.Fatalf("isi tabel versi goose: %v", err)
	}

	scoped := scopedDSN(t, dsn, skema)
	pool, err := database.Connect(ctx, scoped)
	if err != nil {
		t.Fatalf("koneksi pool skema uji: %v", err)
	}
	t.Cleanup(pool.Close)

	var ada bool
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('uuidv7()') IS NOT NULL`).Scan(&ada); err != nil {
		t.Fatalf("cek uuidv7: %v", err)
	}
	if !ada {
		shim, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "pg-dev-shim.sql"))
		if err != nil {
			t.Fatalf("baca shim uuidv7: %v", err)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("ambil koneksi shim: %v", err)
		}
		_, err = conn.Conn().PgConn().Exec(ctx, string(shim)).ReadAll()
		conn.Release()
		if err != nil {
			t.Fatalf("pasang shim uuidv7: %v", err)
		}
	}
	if err := database.Up(ctx, scoped); err != nil {
		t.Fatalf("jalankan migrasi skema uji: %v", err)
	}
	return pool, scoped
}

func scopedDSN(t *testing.T, dsn, skema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN tidak valid: %v", err)
	}
	q := u.Query()
	q.Set("search_path", skema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

// ---------- tiruan mailer yang aman dari goroutine paralel ----------

type hitungMailer struct {
	mu    sync.Mutex
	kirim int
	gagal error
	pesan []mailer.Message
}

func (m *hitungMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pesan = append(m.pesan, msg)
	if m.gagal != nil {
		return m.gagal
	}
	m.kirim++
	return nil
}

func (m *hitungMailer) Health(context.Context) error { return nil }

func (m *hitungMailer) jumlah() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.kirim
}

func (m *hitungMailer) percobaan() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pesan)
}

// ---------- uji ----------

func TestPGTransaksiRollbackTidakMengirimEmail(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	m := &hitungMailer{}
	svc := notification.New(repo, m, notification.WithClock(clock.System()))

	env := notification.Envelope{
		Jenis:   notification.JenisResetPassword,
		To:      []string{"a@example.test"},
		Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=rollback"},
	}

	// 1. Baris outbox ditulis DI DALAM transaksi bisnis yang kemudian rollback:
	//    tidak boleh ada baris dan tidak ada email.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := repo.EnqueueTx(ctx, tx, env); err != nil {
		t.Fatalf("EnqueueTx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := hitungBaris(t, pool); n != 0 {
		t.Fatalf("rollback harus menghapus baris outbox, tersisa %d", n)
	}
	hasil, err := svc.ProsesAntrean(ctx, 0)
	if err != nil {
		t.Fatalf("ProsesAntrean: %v", err)
	}
	if hasil.Diklaim != 0 || m.jumlah() != 0 {
		t.Fatalf("rollback tidak boleh mengirim email (diklaim=%d kirim=%d)", hasil.Diklaim, m.jumlah())
	}

	// 2. Transaksi yang commit: baris ada dan benar-benar terkirim.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin kedua: %v", err)
	}
	if _, err := repo.EnqueueTx(ctx, tx2, env); err != nil {
		t.Fatalf("EnqueueTx kedua: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	hasil, err = svc.ProsesAntrean(ctx, 0)
	if err != nil {
		t.Fatalf("ProsesAntrean kedua: %v", err)
	}
	if hasil.Terkirim != 1 || m.jumlah() != 1 {
		t.Fatalf("transaksi commit harus mengirim 1 email, hasil %+v kirim=%d", hasil, m.jumlah())
	}
}

func TestPGKlaimParalelMengirimTepatSekali(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	m := &hitungMailer{}

	const jumlah = 20
	for i := 0; i < jumlah; i++ {
		if _, err := repo.Enqueue(ctx, notification.Envelope{
			Jenis:   notification.JenisResetPassword,
			To:      []string{fmt.Sprintf("cabang%d@example.test", i)},
			Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=x"},
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Dua worker bersaing mengklaim antrean yang sama.
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := notification.New(repo, m, notification.WithClock(clock.System()))
			for i := 0; i < 10; i++ {
				if _, err := svc.ProsesAntrean(ctx, 3); err != nil {
					t.Errorf("ProsesAntrean: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if m.jumlah() != jumlah {
		t.Fatalf("harus tepat %d email terkirim, dapat %d", jumlah, m.jumlah())
	}
	var (
		terkirim int
		ganda    int
	)
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='sent'),
		count(*) FILTER (WHERE attempts > 1) FROM email_outbox`).Scan(&terkirim, &ganda); err != nil {
		t.Fatalf("rekap status: %v", err)
	}
	if terkirim != jumlah {
		t.Fatalf("semua baris harus sent, dapat %d dari %d", terkirim, jumlah)
	}
	if ganda != 0 {
		t.Fatalf("tidak boleh ada baris yang diklaim dua kali, dapat %d", ganda)
	}
	if m.jumlah() != jumlah {
		t.Fatalf("jumlah email sukses harus %d, dapat %d", jumlah, m.jumlah())
	}
	if m.percobaan() != jumlah {
		t.Fatalf("setiap baris harus dicoba tepat sekali, dapat %d percobaan", m.percobaan())
	}
}

func TestPGKlaimMemulihkanBarisSendingYangDitinggalProsesMati(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	// Basis waktu harus sedikit DI DEPAN now() database (next_try_at baris baru
	// diisi now()), jadi clock uji tidak boleh berada di masa lalu.
	now := time.Now().UTC().Add(time.Second)

	row, err := repo.Enqueue(ctx, notification.Envelope{
		Jenis:   notification.JenisResetPassword,
		To:      []string{"a@example.test"},
		Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=x"},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Klaim pertama: baris menjadi `sending` dengan sewa.
	batch, err := repo.Claim(ctx, notification.Claim{Now: now, Limit: 1, Lease: 15 * time.Minute, MaxAttempts: 5})
	if err != nil {
		t.Fatalf("claim pertama: %v", err)
	}
	if len(batch) != 1 || batch[0].Status != notification.StatusSending || batch[0].Attempts != 1 {
		t.Fatalf("klaim pertama salah: %+v", batch)
	}

	// Selama sewa belum habis, baris tidak boleh diklaim lagi.
	batch, err = repo.Claim(ctx, notification.Claim{Now: now.Add(time.Minute), Limit: 1, Lease: 15 * time.Minute, MaxAttempts: 5})
	if err != nil {
		t.Fatalf("claim kedua: %v", err)
	}
	if len(batch) != 0 {
		t.Fatalf("baris dalam masa sewa tidak boleh diklaim, dapat %d", len(batch))
	}

	// Setelah sewa habis (proses pengirim mati), baris diklaim ulang.
	batch, err = repo.Claim(ctx, notification.Claim{Now: now.Add(16 * time.Minute), Limit: 1, Lease: 15 * time.Minute, MaxAttempts: 5})
	if err != nil {
		t.Fatalf("claim ketiga: %v", err)
	}
	if len(batch) != 1 || batch[0].ID != row.ID || batch[0].Attempts != 2 {
		t.Fatalf("baris harus dipulihkan setelah sewa habis, dapat %+v", batch)
	}
}

func TestPGJadwalRetryTersimpanDanBerhentiSetelahLimaPercobaan(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	m := &hitungMailer{gagal: fmt.Errorf("smtp mati")}
	// Clock uji harus di depan now() database (lihat catatan klaim di atas).
	clk := clock.Fixed(time.Now().UTC().Add(time.Second))
	svc := notification.New(repo, m, notification.WithClock(clk))

	row, err := repo.Enqueue(ctx, notification.Envelope{
		Jenis:   notification.JenisResetPassword,
		To:      []string{"a@example.test"},
		Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=x"},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	mauJeda := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	for i, jeda := range mauJeda {
		if _, err := svc.ProsesAntrean(ctx, 0); err != nil {
			t.Fatalf("putaran %d: %v", i, err)
		}
		var (
			status   string
			attempts int
			nextTry  time.Time
		)
		if err := pool.QueryRow(ctx, `SELECT status::text, attempts, next_try_at FROM email_outbox WHERE id=$1::uuid`, row.ID).
			Scan(&status, &attempts, &nextTry); err != nil {
			t.Fatalf("baca baris: %v", err)
		}
		if attempts != i+1 {
			t.Fatalf("putaran %d: attempts=%d", i+1, attempts)
		}
		if status != notification.StatusQueued {
			t.Fatalf("putaran %d: status=%s, mau queued", i+1, status)
		}
		target := clk.Now().Add(jeda)
		if d := nextTry.Sub(target); d > time.Second || d < -time.Second {
			t.Fatalf("putaran %d: next_try_at %v, mau sekitar %v", i+1, nextTry, target)
		}
		clk.Set(nextTry)
	}

	// Percobaan terakhir (ke-6 = percobaan pertama + 5 retry) menyerah.
	if _, err := svc.ProsesAntrean(ctx, 0); err != nil {
		t.Fatalf("percobaan terakhir: %v", err)
	}

	var (
		status    string
		lastError *string
		attempts  int
	)
	if err := pool.QueryRow(ctx, `SELECT status::text, last_error, attempts FROM email_outbox WHERE id=$1::uuid`, row.ID).
		Scan(&status, &lastError, &attempts); err != nil {
		t.Fatalf("baca baris akhir: %v", err)
	}
	if status != notification.StatusFailed {
		t.Fatalf("setelah 5 percobaan ulang status harus failed, dapat %s", status)
	}
	if attempts != notification.MaxAttempts {
		t.Fatalf("attempts harus %d, dapat %d", notification.MaxAttempts, attempts)
	}
	if lastError == nil || *lastError == "" {
		t.Error("last_error harus tersimpan supaya tampil di riwayat email")
	}
	if m.percobaan() != notification.MaxAttempts {
		t.Fatalf("mailer harus dicoba %d kali, dapat %d", notification.MaxAttempts, m.percobaan())
	}

	// Pemindaian berikutnya tidak boleh mencoba lagi.
	if _, err := svc.ProsesAntrean(ctx, 0); err != nil {
		t.Fatalf("ProsesAntrean setelah failed: %v", err)
	}
	if m.percobaan() != notification.MaxAttempts {
		t.Fatalf("email failed tidak boleh dicoba lagi, percobaan=%d", m.percobaan())
	}
}

func TestPGBarisSendingYangPercobaannyaHabisMenjadiGagal(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	now := time.Now().UTC().Add(time.Second)

	row, err := repo.Enqueue(ctx, notification.Envelope{Jenis: notification.JenisResetPassword,
		To: []string{"a@example.test"}, Payload: map[string]any{"tautan": "https://x/y?token=z"}})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Worker mati tepat setelah klaim terakhir: baris sending, percobaan habis,
	// sewa kedaluwarsa.
	if _, err := pool.Exec(ctx, `UPDATE email_outbox
		SET status='sending', attempts=5, next_try_at = now() - interval '1 minute' WHERE id=$1::uuid`, row.ID); err != nil {
		t.Fatalf("siapkan baris sending: %v", err)
	}

	batch, err := repo.Claim(ctx, notification.Claim{Now: now, Limit: 10, Lease: notification.DefaultLease, MaxAttempts: 5})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(batch) != 0 {
		t.Fatalf("baris dengan percobaan habis tidak boleh diklaim, dapat %d", len(batch))
	}
	var (
		status    string
		lastError *string
	)
	if err := pool.QueryRow(ctx, `SELECT status::text, last_error FROM email_outbox WHERE id=$1::uuid`, row.ID).
		Scan(&status, &lastError); err != nil {
		t.Fatalf("baca baris: %v", err)
	}
	if status != notification.StatusFailed {
		t.Fatalf("baris harus menjadi failed (tidak tersangkut di sending), dapat %s", status)
	}
	if lastError == nil || *lastError == "" {
		t.Error("last_error harus terisi supaya email tampil di riwayat sebagai gagal")
	}
}

func TestPGResendMenulisBarisDanAuditDalamSatuTransaksi(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)

	env := notification.Envelope{
		Jenis:   notification.JenisInvoiceAdmin,
		To:      []string{"sa@example.test"},
		Payload: map[string]any{"nomor": "2026-10-0001"},
	}

	// 1. Entri audit yang tidak lengkap membuat SELURUH transaksi kirim ulang
	//    gagal: baris email pun tidak boleh tertinggal (atomik).
	if _, err := repo.Resend(ctx, env, audit.Entry{}); err == nil {
		t.Fatal("Resend dengan entri audit tidak lengkap harus gagal")
	}
	if n := hitungBaris(t, pool); n != 0 {
		t.Fatalf("kegagalan audit harus membatalkan baris email, tersisa %d", n)
	}

	// 2. Entri audit yang sah: baris email DAN audit tersimpan bersama, dan
	//    audit menunjuk ke id baris email yang baru.
	rec := audit.Entry{
		ActorKind: auth.KindAdmin, ActorID: "11111111-1111-1111-1111-111111111111",
		ActorNama: "Rina", Entity: audit.EntityEmail, Action: audit.ActionResend,
		After: map[string]any{"jenis": notification.JenisInvoiceAdmin},
	}
	row, err := repo.Resend(ctx, env, rec)
	if err != nil {
		t.Fatalf("Resend: %v", err)
	}
	var (
		entity  string
		action  string
		emailID string
	)
	if err := pool.QueryRow(ctx, `SELECT entity, action, entity_id::text FROM audit_logs ORDER BY at DESC LIMIT 1`).
		Scan(&entity, &action, &emailID); err != nil {
		t.Fatalf("baca audit: %v", err)
	}
	if entity != audit.EntityEmail || action != audit.ActionResend {
		t.Fatalf("audit kirim ulang salah: entity=%s action=%s", entity, action)
	}
	if emailID != row.ID {
		t.Fatalf("audit harus menunjuk id email baru; email=%s audit=%s", row.ID, emailID)
	}
	if n := hitungBaris(t, pool); n != 1 {
		t.Fatalf("harus ada tepat satu baris email, dapat %d", n)
	}
}

func TestPGTokenResetHanyaSatuYangAktif(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()

	var branchID, userID string
	if err := pool.QueryRow(ctx, `INSERT INTO branches (nama, nama_kepala, email)
		VALUES ('Cabang Uji','Kepala','uji@example.test') RETURNING id`).Scan(&branchID); err != nil {
		t.Fatalf("seed cabang: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO branch_users (branch_id, email, password_hash)
		VALUES ($1,'uji@example.test','x') RETURNING id`, branchID).Scan(&userID); err != nil {
		t.Fatalf("seed akun cabang: %v", err)
	}

	repo := authpg.New(pool)
	akhir := time.Now().UTC().Add(time.Hour)
	if err := repo.CreateResetToken(ctx, auth.ResetToken{Kind: auth.KindBranch, UserID: userID, ExpiresAt: akhir}, []byte("hash-pertama")); err != nil {
		t.Fatalf("CreateResetToken pertama: %v", err)
	}
	if err := repo.CreateResetToken(ctx, auth.ResetToken{Kind: auth.KindBranch, UserID: userID, ExpiresAt: akhir}, []byte("hash-kedua")); err != nil {
		t.Fatalf("CreateResetToken kedua: %v", err)
	}

	var aktif int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE user_id=$1::uuid AND used_at IS NULL`, userID).Scan(&aktif); err != nil {
		t.Fatalf("hitung token aktif: %v", err)
	}
	if aktif != 1 {
		t.Fatalf("harus tepat satu tautan aktif per akun, dapat %d", aktif)
	}
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM password_reset_tokens WHERE user_id=$1::uuid AND used_at IS NULL`, userID).Scan(&hash); err != nil {
		t.Fatalf("baca token aktif: %v", err)
	}
	if string(hash) != "hash-kedua" {
		t.Errorf("token aktif harus yang terbaru, dapat %q", hash)
	}
}

// ---------- temuan QA putaran 1 ----------

// mailerBatal membatalkan context pemindaian pada kirim ke-`pada` lalu gagal,
// meniru job River yang kehabisan waktu di tengah batch.
type mailerBatal struct {
	mu    sync.Mutex
	pada  int
	kirim int
	batal context.CancelFunc
}

func (m *mailerBatal) Send(_ context.Context, _ mailer.Message) error {
	m.mu.Lock()
	m.kirim++
	n := m.kirim
	batal := m.batal
	m.mu.Unlock()
	if n >= m.pada {
		batal()
		return errors.New("pemindaian dibatalkan")
	}
	return nil
}

func (m *mailerBatal) Health(context.Context) error { return nil }

// seedPesananDokumen menyiapkan cabang + pesanan + dokumen invoice + satu admin
// ber-flag penerima, supaya jalur dokumen (ResendOrder) dapat diuji utuh.
func seedPesananDokumen(t *testing.T, pool *database.Pool, nomor string) string {
	t.Helper()
	ctx := context.Background()
	var branchID string
	if err := pool.QueryRow(ctx, `INSERT INTO branches (nama, nama_kepala, email)
		VALUES ('Cabang Uji','Kepala Uji','cabang-uji@example.test') RETURNING id`).Scan(&branchID); err != nil {
		t.Fatalf("seed cabang: %v", err)
	}
	var orderID string
	if err := pool.QueryRow(ctx, `INSERT INTO orders (branch_id, nomor, versi, status, tanggal, total)
		VALUES ($1, $2, 0, 'submitted', current_date, 350000) RETURNING id`, branchID, nomor).Scan(&orderID); err != nil {
		t.Fatalf("seed pesanan: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO documents (order_id, jenis, versi, storage_key)
		VALUES ($1::uuid, 'invoice_pdf', 0, $2)`, orderID, "orders/"+orderID+"/invoice.pdf"); err != nil {
		t.Fatalf("seed dokumen: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users
		(nama, email, password_hash, role, is_active, activated_at, invited_at, notif_invoice, notif_surat_jalan)
		VALUES ('SA Uji','sa-uji@example.test','x','superadmin',true,now(),now(),true,true)`); err != nil {
		t.Fatalf("seed admin penerima: %v", err)
	}
	return orderID
}

// TestPGResendBersamaanTidakMenggandakanEmail menutup PEMBLOKIR butir 1: dua
// permintaan kirim ulang yang bersamaan harus menghasilkan paling banyak SATU
// email baru dan SATU entri audit, sisanya 409 EMAIL_ALREADY_QUEUED.
func TestPGResendBersamaanTidakMenggandakanEmail(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	orderID := seedPesananDokumen(t, pool, "2026-10-9001")

	svc := notification.New(repo, mailer.Log{},
		notification.WithClock(clock.System()),
		notification.WithOrderReader(repo),
		notification.WithAdminRecipients(userspg.New(pool)),
	)

	// Email pertama harus ada lebih dulu (kirim ulang = pemulihan) dan tidak
	// lagi dalam antrean supaya penjaga "masih antre" tidak langsung menolak.
	if _, err := svc.EnqueueOrderEmail(ctx, notification.OrderEmailInput{
		OrderID: orderID, Jenis: notification.JenisInvoiceAdmin}); err != nil {
		t.Fatalf("enqueue email pertama: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE email_outbox SET status = 'sent', sent_at = now()`); err != nil {
		t.Fatalf("tandai email pertama terkirim: %v", err)
	}

	aktor := auth.Actor{UserID: "11111111-1111-1111-1111-111111111111",
		Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}

	const jumlah = 5
	var (
		wg                    sync.WaitGroup
		mu                    sync.Mutex
		sukses, ditolak, lain int
	)
	mulai := make(chan struct{})
	for i := 0; i < jumlah; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-mulai // lepaskan semua bersamaan
			_, err := svc.ResendOrder(ctx, aktor, orderID, notification.JenisInvoiceAdmin)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				sukses++
			case errors.Is(err, notification.ErrAlreadyQueued):
				ditolak++
			default:
				lain++
				t.Errorf("kirim ulang mengembalikan galat tak terduga: %v", err)
			}
		}()
	}
	close(mulai)
	wg.Wait()

	if sukses != 1 || ditolak != jumlah-1 || lain != 0 {
		t.Fatalf("harus tepat 1 kirim ulang sukses dan %d ditolak, dapat sukses=%d ditolak=%d lain=%d",
			jumlah-1, sukses, ditolak, lain)
	}

	var baris, audit int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox
		WHERE order_id = $1::uuid AND jenis = 'invoice_admin'`, orderID).Scan(&baris); err != nil {
		t.Fatalf("hitung baris outbox: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs
		WHERE entity = 'email' AND action = 'resend'`).Scan(&audit); err != nil {
		t.Fatalf("hitung audit: %v", err)
	}
	if baris != 2 {
		t.Fatalf("harus tepat 2 baris (asli + 1 kirim ulang), dapat %d", baris)
	}
	if audit != 1 {
		t.Fatalf("harus tepat 1 entri audit resend, dapat %d", audit)
	}
}

// TestPGAtLeastOnceKirimDuaKaliSetelahSMTPMenerima mengabadikan semantik
// at-least-once (PEMBLOKIR butir 2): bila proses mati SETELAH SMTP menerima
// tetapi sebelum status `sent` tersimpan, baris diklaim ulang setelah sewa
// habis dan mailer dipanggil dua kali. Ini disengaja, bukan bug tepat-sekali.
func TestPGAtLeastOnceKirimDuaKaliSetelahSMTPMenerima(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	m := &hitungMailer{}
	svc := notification.New(repo, m, notification.WithClock(clock.System()))

	now := time.Now().UTC().Add(time.Second)
	row, err := repo.Enqueue(ctx, notification.Envelope{
		Jenis:   notification.JenisResetPassword,
		To:      []string{"a@example.test"},
		Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=x"},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// 1. Klaim + kirim sukses, tetapi proses "mati" sebelum MarkSent.
	batch, err := repo.Claim(ctx, notification.Claim{
		Now: now, Limit: 1, Lease: notification.DefaultLease, MaxAttempts: notification.MaxAttempts})
	if err != nil {
		t.Fatalf("claim pertama: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("harus mengklaim 1 baris, dapat %d", len(batch))
	}
	if err := svc.Kirim(ctx, batch[0]); err != nil {
		t.Fatalf("kirim pertama: %v", err)
	}
	// Sengaja TIDAK MarkSent: meniru proses yang mati setelah SMTP menerima.

	// 2. Setelah sewa habis baris diklaim ulang dan dikirim lagi.
	batch2, err := repo.Claim(ctx, notification.Claim{
		Now: now.Add(notification.DefaultLease + time.Minute), Limit: 1,
		Lease: notification.DefaultLease, MaxAttempts: notification.MaxAttempts})
	if err != nil {
		t.Fatalf("claim kedua: %v", err)
	}
	if len(batch2) != 1 || batch2[0].ID != row.ID || batch2[0].Attempts != 2 {
		t.Fatalf("baris harus diklaim ulang dengan attempts=2, dapat %+v", batch2)
	}
	if err := svc.Kirim(ctx, batch2[0]); err != nil {
		t.Fatalf("kirim kedua: %v", err)
	}
	if err := repo.MarkSent(ctx, batch2[0].ID, now.Add(notification.DefaultLease+time.Minute)); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	if m.percobaan() != 2 {
		t.Fatalf("mailer harus dipanggil 2 kali (at-least-once), dapat %d", m.percobaan())
	}
	var (
		status   string
		attempts int
	)
	if err := pool.QueryRow(ctx, `SELECT status::text, attempts FROM email_outbox WHERE id = $1::uuid`, row.ID).
		Scan(&status, &attempts); err != nil {
		t.Fatalf("baca baris akhir: %v", err)
	}
	if status != notification.StatusSent || attempts != 2 {
		t.Fatalf("akhir harus sent attempts=2, dapat %s attempts=%d", status, attempts)
	}
}

// TestPGSisaKlaimDilepasSaatPemindaianDibatalkan menutup butir 3 pada jalur
// nyata: pemindaian yang context-nya dibatalkan di tengah batch melepas baris
// yang belum dicoba kembali ke `queued` TANPA menghabiskan jatah retry.
func TestPGSisaKlaimDilepasSaatPemindaianDibatalkan(t *testing.T) {
	pool, _ := skemaUji(t)
	repo := notificationpg.New(pool)

	ctx, batal := context.WithCancel(context.Background())
	defer batal()
	m := &mailerBatal{pada: 2, batal: batal}
	// Clock sedikit di depan now() database supaya baris baru langsung siap
	// diklaim (next_try_at defaultnya now() database).
	svc := notification.New(repo, m, notification.WithClock(clock.Fixed(time.Now().UTC().Add(time.Second))))

	for i := 0; i < 3; i++ {
		if _, err := repo.Enqueue(ctx, notification.Envelope{
			Jenis:   notification.JenisResetPassword,
			To:      []string{fmt.Sprintf("cabang%d@example.test", i)},
			Payload: map[string]any{"tautan": "https://cabang.test/lupa?token=x"},
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	if _, err := svc.ProsesAntrean(ctx, 0); err == nil {
		t.Fatal("pemindaian dengan context yang dibatalkan harus mengembalikan error")
	}

	var terkirim, sedangDicoba, dilepas int
	if err := pool.QueryRow(context.Background(), `SELECT
		count(*) FILTER (WHERE status = 'sent'),
		count(*) FILTER (WHERE status = 'sending' AND attempts = 1),
		count(*) FILTER (WHERE status = 'queued' AND attempts = 0)
		FROM email_outbox`).Scan(&terkirim, &sedangDicoba, &dilepas); err != nil {
		t.Fatalf("rekap status: %v", err)
	}
	if terkirim != 1 {
		t.Errorf("harus 1 email terkirim sebelum pembatalan, dapat %d", terkirim)
	}
	if sedangDicoba != 1 {
		t.Errorf("harus 1 baris sempat dicoba (sending attempts=1), dapat %d", sedangDicoba)
	}
	if dilepas != 1 {
		t.Errorf("harus 1 baris belum dicoba dilepas (queued attempts=0), dapat %d", dilepas)
	}
}

// ---------- helper ----------

func hitungBaris(t *testing.T, pool *database.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox`).Scan(&n); err != nil {
		t.Fatalf("hitung baris outbox: %v", err)
	}
	return n
}
