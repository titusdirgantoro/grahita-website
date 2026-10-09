package notification_test

// Uji integrasi pengingat pembayaran Cash & Carry terjadwal (F2-23) pada
// PostgreSQL nyata.
//
// Yang hanya bisa dibuktikan dengan SQL sungguhan: kandidat yang benar-benar
// dipilih (C&C, bukan Done, punya jatuh tempo), penulisan baris
// cnc_reminders + email_outbox + audit yang atomik, penjagaan
// PRIMARY KEY (order_id, tahap) terhadap pemindaian ulang, dan perjalanan
// retry email yang gagal.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/notification"
	notificationpg "github.com/grahita/portal/api/internal/notification/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/database"
)

// clockUji adalah clock yang dibekukan sekaligus dapat dipindah antar hari.
type clockUji interface {
	clock.Clock
	Set(time.Time)
}

// seedCabangCNC menyemai satu cabang C&C dan mengembalikan id + emailnya.
func seedCabangCNC(t *testing.T, pool *database.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()
	const email = "cabang-cnc@example.test"
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO branches (nama, nama_kepala, email, is_cnc)
		VALUES ('Cabang CNC','Kepala CNC',$1,true) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("seed cabang C&C: %v", err)
	}
	return id, email
}

// seedPesananCNC menyemai satu pesanan C&C berstatus `status` dengan (opsional)
// jatuh tempo; total disemai 350000.
func seedPesananCNC(t *testing.T, pool *database.Pool, branchID, nomor, status, jatuhTempo string) string {
	t.Helper()
	ctx := context.Background()
	var jt, done any
	if jatuhTempo != "" {
		jt = jatuhTempo
	}
	if status == "done" {
		done = time.Now().UTC()
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (branch_id, nomor, versi, status, tanggal, is_cnc, jatuh_tempo, total, submitted_at, done_at)
		VALUES ($1, $2, 0, $3::order_status, $4::date, true, $5::date, 350000, now(), $6)
		RETURNING id`, branchID, nomor, status, "2026-10-01", jt, done).Scan(&id); err != nil {
		t.Fatalf("seed pesanan C&C %s: %v", nomor, err)
	}
	return id
}

// timeMustWIB menyusun waktu pukul jam:00 WIB pada tanggal YYYY-MM-DD.
func timeMustWIB(t *testing.T, tanggal string, jam int) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", tanggal, clock.Jakarta())
	if err != nil {
		t.Fatalf("tanggal %q: %v", tanggal, err)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), jam, 0, 0, 0, clock.Jakarta())
}

// hariCNC mengembalikan clock yang dibekukan pada pukul 07:00 WIB tanggal itu.
func hariCNC(t *testing.T, tanggal string) clockUji {
	t.Helper()
	return clock.Fixed(timeMustWIB(t, tanggal, 7))
}

func jumlahCnc(t *testing.T, pool *database.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM cnc_reminders`).Scan(&n); err != nil {
		t.Fatalf("hitung cnc_reminders: %v", err)
	}
	return n
}

// tahapTersimpan mengembalikan pasangan "nomor|tahap" yang tersimpan.
func tahapTersimpan(t *testing.T, pool *database.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT o.nomor || '|' || r.tahap FROM cnc_reminders r
		 JOIN orders o ON o.id = r.order_id ORDER BY o.nomor, r.tahap`)
	if err != nil {
		t.Fatalf("baca cnc_reminders: %v", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan cnc_reminders: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// TestPGReminderCNCJadwalEmpatTahapDanIdempoten menempuh keempat tahap satu
// pesanan dengan memindahkan jam hari demi hari, memastikan hari tanpa tahap
// tidak menghasilkan email, dan pemindaian ulang pada hari yang sama tidak
// menggandakan apa pun (kriteria kartu: "job ulang tidak ganda").
func TestPGReminderCNCJadwalEmpatTahapDanIdempoten(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	branchID, emailCabang := seedCabangCNC(t, pool)
	seedPesananCNC(t, pool, branchID, "2026-10-0001", "submitted", "2026-10-09")

	clk := hariCNC(t, "2026-10-07")
	m := &hitungMailer{}
	svc := notification.New(repo, m,
		notification.WithClock(clk), notification.WithReminderStore(repo))

	jadwal := []struct {
		tanggal string
		tahap   string // "" = tidak boleh ada pengingat
	}{
		{"2026-10-06", ""},    // h-3: terlalu awal
		{"2026-10-07", "h-2"}, // H-2
		{"2026-10-08", ""},    // h-1
		{"2026-10-09", "h-0"}, // H-0 (jatuh tempo)
		{"2026-10-10", "h+1"}, // H+1
		{"2026-10-11", ""},    // h+2
		{"2026-10-12", "h+3"}, // H+3
		{"2026-10-13", ""},    // h+4
	}
	for _, j := range jadwal {
		clk.Set(timeMustWIB(t, j.tanggal, 7))
		hasil, err := svc.JalankanReminderCNC(ctx)
		if err != nil {
			t.Fatalf("JalankanReminderCNC %s: %v", j.tanggal, err)
		}
		if j.tahap == "" {
			if hasil.Diantre != 0 {
				t.Errorf("%s (tanpa tahap) tidak boleh mengantre, hasil %+v", j.tanggal, hasil)
			}
		} else if hasil.Diantre != 1 {
			t.Errorf("%s harus mengantre 1 (%s), hasil %+v", j.tanggal, j.tahap, hasil)
		}
		// Pemindaian ulang pada hari yang sama: tidak ada penambahan.
		ulang, err := svc.JalankanReminderCNC(ctx)
		if err != nil {
			t.Fatalf("pemindaian ulang %s: %v", j.tanggal, err)
		}
		if ulang.Diantre != 0 {
			t.Errorf("%s: pemindaian ulang tidak boleh mengantre, hasil %+v", j.tanggal, ulang)
		}
	}

	if dapat := tahapTersimpan(t, pool); len(dapat) != 4 {
		t.Fatalf("harus 4 baris cnc_reminders, dapat %v", dapat)
	}
	if n := hitungBaris(t, pool); n != 4 {
		t.Fatalf("harus 4 baris email_outbox, dapat %d", n)
	}

	// Setiap baris outbox: jenis reminder_cnc, penerima email cabang, dan
	// payload memuat tahap (dipakai template email).
	rows, err := pool.Query(ctx, `SELECT jenis::text, to_addr, payload->>'tahap'
		FROM email_outbox ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("baca email_outbox: %v", err)
	}
	defer rows.Close()
	jumlah := 0
	for rows.Next() {
		var jenis, tahap string
		var to []string
		if err := rows.Scan(&jenis, &to, &tahap); err != nil {
			t.Fatalf("scan email_outbox: %v", err)
		}
		jumlah++
		if jenis != notification.JenisReminderCNC {
			t.Errorf("jenis harus %s, dapat %s", notification.JenisReminderCNC, jenis)
		}
		if len(to) != 1 || to[0] != emailCabang {
			t.Errorf("penerima harus %s, dapat %v", emailCabang, to)
		}
		if tahap == "" {
			t.Error("payload tahap tidak boleh kosong")
		}
	}
	if jumlah != 4 {
		t.Fatalf("baris outbox terbaca %d, mau 4", jumlah)
	}
}

// TestPGReminderCNCDoneDanNonCncDikecualikan menutup kriteria "Done sebelum
// tahap berikutnya tidak diingatkan": pesanan yang menjadi Done sebelum H+1
// tidak lagi muncul di kandidat maupun diantre, sedangkan pesanan non-C&C dan
// C&C yang sudah Done sejak awal juga tidak pernah masuk.
func TestPGReminderCNCDoneDanNonCncDikecualikan(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	branchID, _ := seedCabangCNC(t, pool)

	sudahDone := seedPesananCNC(t, pool, branchID, "2026-10-0001", "done", "2026-10-09")
	masihJalan := seedPesananCNC(t, pool, branchID, "2026-10-0002", "submitted", "2026-10-09")
	if _, err := pool.Exec(ctx, `INSERT INTO orders (branch_id, nomor, versi, status, tanggal, is_cnc, total, submitted_at)
		VALUES ($1,'2026-10-0003',0,'submitted','2026-10-01',false,100000,now())`, branchID); err != nil {
		t.Fatalf("seed pesanan non-C&C: %v", err)
	}

	clk := hariCNC(t, "2026-10-07") // h-2 untuk jatuh tempo 2026-10-09
	svc := notification.New(repo, &hitungMailer{},
		notification.WithClock(clk), notification.WithReminderStore(repo))

	kandidat, err := repo.KandidatCNC(ctx)
	if err != nil {
		t.Fatalf("KandidatCNC: %v", err)
	}
	if len(kandidat) != 1 || kandidat[0].OrderID != masihJalan {
		t.Fatalf("hanya pesanan C&C belum Done yang boleh jadi kandidat, dapat %+v", kandidat)
	}

	hasil, err := svc.JalankanReminderCNC(ctx)
	if err != nil {
		t.Fatalf("JalankanReminderCNC: %v", err)
	}
	if hasil.Diantre != 1 {
		t.Fatalf("hanya satu pengingat h-2 (pesanan belum Done), hasil %+v", hasil)
	}
	if tersimpan := tahapTersimpan(t, pool); len(tersimpan) != 1 || tersimpan[0] != "2026-10-0002|h-2" {
		t.Fatalf("pengingat harus hanya untuk pesanan belum Done, dapat %v", tersimpan)
	}
	var untukDone int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cnc_reminders WHERE order_id=$1::uuid`, sudahDone).Scan(&untukDone); err != nil {
		t.Fatalf("cek pengingat pesanan Done: %v", err)
	}
	if untukDone != 0 {
		t.Fatalf("pesanan berstatus Done tidak boleh diingatkan, dapat %d baris", untukDone)
	}

	// Pesanan 'masihJalan' lunas sebelum H+1: berubah menjadi Done.
	if _, err := pool.Exec(ctx, `UPDATE orders SET status='done', done_at=now() WHERE id=$1::uuid`, masihJalan); err != nil {
		t.Fatalf("tandai Done: %v", err)
	}
	clk.Set(timeMustWIB(t, "2026-10-10", 7)) // H+1
	hasil2, err := svc.JalankanReminderCNC(ctx)
	if err != nil {
		t.Fatalf("JalankanReminderCNC hari H+1: %v", err)
	}
	if hasil2.Diantre != 0 || hasil2.Diperiksa != 0 {
		t.Fatalf("pesanan Done tidak boleh diingatkan H+1, hasil %+v", hasil2)
	}
	if n := jumlahCnc(t, pool); n != 1 {
		t.Fatalf("tetap hanya 1 baris cnc_reminders, dapat %d", n)
	}
}

// TestPGReminderCNCAuditTercatat memastikan tiap pengingat meninggalkan entri
// audit 'system' (entity 'cnc_reminder', action 'remind') — kriteria kartu
// "enqueue via email_outbox dan audit hasil".
func TestPGReminderCNCAuditTercatat(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	branchID, _ := seedCabangCNC(t, pool)
	orderID := seedPesananCNC(t, pool, branchID, "2026-10-0001", "submitted", "2026-10-09")

	clk := hariCNC(t, "2026-10-07")
	svc := notification.New(repo, &hitungMailer{},
		notification.WithClock(clk), notification.WithReminderStore(repo))
	if _, err := svc.JalankanReminderCNC(ctx); err != nil {
		t.Fatalf("JalankanReminderCNC: %v", err)
	}

	var aktor, entity, action, entityID, tahap string
	if err := pool.QueryRow(ctx, `SELECT actor_kind::text, entity, action, entity_id::text, after->>'tahap'
		FROM audit_logs WHERE entity = $1`, audit.EntityCNCReminder).
		Scan(&aktor, &entity, &action, &entityID, &tahap); err != nil {
		t.Fatalf("baca audit pengingat: %v", err)
	}
	if aktor != "system" {
		t.Errorf("actor_kind harus system, dapat %q", aktor)
	}
	if entity != audit.EntityCNCReminder || action != audit.ActionRemind {
		t.Errorf("entity/action salah: %s/%s", entity, action)
	}
	if entityID != orderID {
		t.Errorf("entity_id harus id pesanan %s, dapat %s", orderID, entityID)
	}
	if tahap != "h-2" {
		t.Errorf("after.tahap harus h-2, dapat %q", tahap)
	}
}

// TestPGReminderCNCGagalKirimDapatRetryTidakDianggapTerkirim menutup kriteria
// "gagal kirim dapat retry dan tidak dianggap terkirim sebelum sukses": email
// pengingat yang ditolak SMTP tetap `queued` (bukan `sent`, sent_at NULL) dan
// dikirim pada pemindaian berikutnya, tanpa menggandakan baris.
func TestPGReminderCNCGagalKirimDapatRetryTidakDianggapTerkirim(t *testing.T) {
	pool, _ := skemaUji(t)
	ctx := context.Background()
	repo := notificationpg.New(pool)
	branchID, _ := seedCabangCNC(t, pool)
	seedPesananCNC(t, pool, branchID, "2026-10-0001", "submitted", "2026-10-09")

	clk := hariCNC(t, "2026-10-07")
	m := &hitungMailer{}
	svc := notification.New(repo, m,
		notification.WithClock(clk), notification.WithReminderStore(repo))

	if _, err := svc.JalankanReminderCNC(ctx); err != nil {
		t.Fatalf("JalankanReminderCNC: %v", err)
	}
	if n := hitungBaris(t, pool); n != 1 {
		t.Fatalf("harus 1 baris outbox, dapat %d", n)
	}
	// Baris yang baru diantre memakai next_try_at = now() jam database
	// (dinding nyata), sedangkan uji memakai clock yang dibekukan di masa
	// lalu; majukan jadwalnya supaya bisa diklaim pada jam uji.
	if _, err := pool.Exec(ctx, `UPDATE email_outbox SET next_try_at = TIMESTAMPTZ '2026-10-06 23:00:00+07'`); err != nil {
		t.Fatalf("majukan next_try_at: %v", err)
	}

	// Kirim pertama GAGAL.
	m.gagal = errors.New("smtp menolak")
	hasil, err := svc.ProsesAntrean(ctx, 0)
	if err != nil {
		t.Fatalf("ProsesAntrean gagal: %v", err)
	}
	if hasil.Terkirim != 0 || hasil.Ditunda != 1 {
		t.Fatalf("kirim gagal harus ditunda, hasil %+v", hasil)
	}
	var status string
	var attempts int
	var sentAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT status::text, attempts, sent_at FROM email_outbox`).
		Scan(&status, &attempts, &sentAt); err != nil {
		t.Fatalf("baca status email: %v", err)
	}
	if status == notification.StatusSent || sentAt != nil {
		t.Fatalf("email gagal TIDAK boleh dianggap terkirim: status=%s sent_at=%v", status, sentAt)
	}
	if attempts != 1 {
		t.Fatalf("attempts harus 1, dapat %d", attempts)
	}
	if n := jumlahCnc(t, pool); n != 1 {
		t.Fatalf("cnc_reminders tetap 1 (retry tidak menggandakan), dapat %d", n)
	}

	// Lewati jeda retry (1m) lalu kirim lagi: BERHASIL.
	m.gagal = nil
	clk.Set(timeMustWIB(t, "2026-10-07", 7).Add(2 * time.Minute))
	hasil2, err := svc.ProsesAntrean(ctx, 0)
	if err != nil {
		t.Fatalf("ProsesAntrean kedua: %v", err)
	}
	if hasil2.Terkirim != 1 {
		t.Fatalf("percobaan kedua harus terkirim, hasil %+v", hasil2)
	}
	if err := pool.QueryRow(ctx, `SELECT status::text, sent_at FROM email_outbox`).
		Scan(&status, &sentAt); err != nil {
		t.Fatalf("baca status email akhir: %v", err)
	}
	if status != notification.StatusSent || sentAt == nil {
		t.Fatalf("email harus sent dengan sent_at terisi, dapat %s %v", status, sentAt)
	}
	if n := hitungBaris(t, pool); n != 1 {
		t.Fatalf("retry tidak boleh menggandakan baris, dapat %d", n)
	}
}
