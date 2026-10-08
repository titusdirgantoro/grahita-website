package database_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/platform/database"
)

// ---------- bantuan ----------

// siapkanSkema memastikan seluruh migrasi diterapkan lalu mengembalikan pool.
// Uji di berkas ini hanya berjalan bila TEST_DATABASE_URL diisi (CI: postgres:18).
func siapkanSkema(t *testing.T) (context.Context, *database.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi skema dilewati")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi up: %v", err)
	}
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("koneksi pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func kodeSQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// harusGagal menjalankan fn pada savepoint dan memastikan PostgreSQL menolaknya
// dengan SQLSTATE yang diharapkan. Savepoint dipakai supaya transaksi uji tetap
// hidup setelah statement yang sengaja gagal.
func harusGagal(ctx context.Context, t *testing.T, tx pgx.Tx, kode string, fn func(pgx.Tx) error) {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("buat savepoint: %v", err)
	}
	terjadi := fn(sp)
	if err := sp.Rollback(ctx); err != nil {
		t.Fatalf("rollback savepoint: %v", err)
	}
	if kode == "" {
		if terjadi == nil {
			t.Fatal("operasi seharusnya gagal, tetapi berhasil")
		}
		return
	}
	if kodeSQLState(terjadi) != kode {
		t.Fatalf("SQLSTATE = %q, ingin %q (error: %v)", kodeSQLState(terjadi), kode, terjadi)
	}
}

// unik menghasilkan akhiran unik per pemanggilan agar data uji tidak bertabrakan.
func unik(suffix string) string {
	return fmt.Sprintf("%d%s", time.Now().UnixNano(), suffix)
}

func buatCabang(ctx context.Context, t *testing.T, tx pgx.Tx, suffix string) string {
	t.Helper()
	var id string
	err := tx.QueryRow(ctx,
		`INSERT INTO branches (nama, nama_kepala, email)
		 VALUES ($1, 'Kepala Uji', $2) RETURNING id`,
		"Cabang Uji "+suffix, "cabang-"+suffix+"@contoh.id").Scan(&id)
	if err != nil {
		t.Fatalf("buat cabang: %v", err)
	}
	return id
}

func buatProduk(ctx context.Context, t *testing.T, tx pgx.Tx, suffix, jenis string, harga int64) string {
	t.Helper()
	var id string
	err := tx.QueryRow(ctx,
		`INSERT INTO products (kode, nama, jenis, harga_default)
		 VALUES ($1, $2, $3::product_kind, $4) RETURNING id`,
		"UJI-"+suffix, "Produk Uji "+suffix, jenis, harga).Scan(&id)
	if err != nil {
		t.Fatalf("buat produk %s: %v", jenis, err)
	}
	return id
}

func buatOrderDraft(ctx context.Context, t *testing.T, tx pgx.Tx, branchID string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO orders (branch_id) VALUES ($1) RETURNING id`, branchID).Scan(&id); err != nil {
		t.Fatalf("buat draft: %v", err)
	}
	return id
}

// tambahBarisProduk menambah satu baris produk pada pesanan.
func tambahBarisProduk(ctx context.Context, tx pgx.Tx, orderID, productID, nama string,
	qty int, harga int64, serial string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan, serial_number)
		 SELECT $1, COALESCE(max(line_no), 0) + 1, 'product', $2, $3, $4, $5, NULLIF($6, '')
		   FROM order_lines WHERE order_id = $1`,
		orderID, productID, nama, qty, harga, serial)
	return err
}

func tambahBarisPackaging(ctx context.Context, tx pgx.Tx, orderID string, harga int64, serial string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO order_lines (order_id, line_no, jenis, nama_snapshot, qty, harga_satuan, serial_number)
		 SELECT $1, COALESCE(max(line_no), 0) + 1, 'packaging', 'Biaya packaging', 1, $2, NULLIF($3, '')
		   FROM order_lines WHERE order_id = $1`,
		orderID, harga, serial)
	return err
}

// submitOrder memindahkan pesanan draft -> submitted seperti yang dilakukan service.
func submitOrder(ctx context.Context, tx pgx.Tx, orderID, nomor string, isSertifikat bool) error {
	_, err := tx.Exec(ctx,
		`UPDATE orders
		    SET status = 'submitted', nomor = $2, tanggal = current_date,
		        submitted_at = now(), is_sertifikat = $3
		  WHERE id = $1`,
		orderID, nomor, isSertifikat)
	return err
}

// ---------- skema & seed ----------

func TestSkemaIntiLengkap(t *testing.T) {
	ctx, pool := siapkanSkema(t)

	tabel := []string{
		"branches", "branch_users", "admin_users", "registration_tokens",
		"password_reset_tokens", "sessions", "products", "branch_prices",
		"bank_accounts", "orders", "order_lines", "payment_proofs",
		"verification_logs", "documents", "invoice_revisions", "order_events",
		"email_outbox", "cnc_reminders", "settings", "audit_logs",
		"backup_runs", "numbering_counters",
	}
	for _, nama := range tabel {
		var ada bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                 WHERE table_schema = 'public' AND table_name = $1)`, nama).Scan(&ada); err != nil {
			t.Fatalf("cek tabel %s: %v", nama, err)
		}
		if !ada {
			t.Errorf("tabel %s tidak ada setelah migrasi", nama)
		}
	}

	enum := []string{
		"actor_kind", "admin_role", "product_kind", "order_status", "verif_status",
		"review_reason", "line_kind", "verify_method", "doc_kind", "email_kind",
		"email_status", "bank_code",
	}
	for _, nama := range enum {
		var ada bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_type WHERE typname = $1 AND typtype = 'e')`, nama).Scan(&ada); err != nil {
			t.Fatalf("cek enum %s: %v", nama, err)
		}
		if !ada {
			t.Errorf("enum %s tidak ada setelah migrasi", nama)
		}
	}
}

func TestSeedPengaturanPRD(t *testing.T) {
	ctx, pool := siapkanSkema(t)

	harusAda := map[string]string{
		"biaya_packaging":      "3000",
		"cnc_hari":             "7",
		"token_hari":           "14",
		"reset_menit":          "60",
		"max_mb":               "5",
		"max_file":             "5",
		"digit_invoice":        "2",
		"manager_boleh_revisi": "true",
	}
	for key, ingin := range harusAda {
		var dapat string
		if err := pool.QueryRow(ctx, `SELECT value::text FROM settings WHERE key = $1`, key).Scan(&dapat); err != nil {
			t.Fatalf("setting %s tidak ada: %v", key, err)
		}
		if dapat != ingin {
			t.Errorf("setting %s = %s, ingin %s", key, dapat, ingin)
		}
	}

	var ai map[string]any
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'ai'`).Scan(&ai); err != nil {
		t.Fatalf("setting ai tidak ada: %v", err)
	}
	inginAI := map[string]float64{
		"aktif":         0, // false
		"min_keyakinan": 0.9,
		"tol_kurang":    0,
		"tol_lebih":     0,
		"digit_rek":     4,
		"mundur":        1,
	}
	for kunci, ingin := range inginAI {
		nilai, ok := ai[kunci]
		if !ok {
			t.Errorf("ai.%s tidak ada di seed", kunci)
			continue
		}
		var angka float64
		switch v := nilai.(type) {
		case float64:
			angka = v
		case bool:
			if v {
				angka = 1
			}
		default:
			t.Errorf("ai.%s bertipe %T, ingin angka/bool", kunci, nilai)
			continue
		}
		if angka != ingin {
			t.Errorf("ai.%s = %v, ingin %v", kunci, angka, ingin)
		}
	}
}

// ---------- constraint ----------

func TestConstraintEmailDanSatuAkunPerCabang(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	s := unik("")
	branchID := buatCabang(ctx, t, tx, s)

	// Email cabang unik tanpa memandang besar-kecil huruf (citext).
	harusGagal(ctx, t, tx, "23505", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx,
			`INSERT INTO branches (nama, nama_kepala, email) VALUES ('Cabang Kembar', 'X', $1)`,
			strings.ToUpper("cabang-"+s+"@contoh.id"))
		return err
	})

	// Email admin unik tanpa memandang besar-kecil huruf.
	emailAdmin := "admin-" + s + "@contoh.id"
	if _, err := tx.Exec(ctx,
		`INSERT INTO admin_users (nama, email, role) VALUES ('Admin A', $1, 'manager')`, emailAdmin); err != nil {
		t.Fatalf("insert admin: %v", err)
	}
	harusGagal(ctx, t, tx, "23505", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx,
			`INSERT INTO admin_users (nama, email, role) VALUES ('Admin B', $1, 'manager')`, strings.ToUpper(emailAdmin))
		return err
	})

	// 1 cabang = 1 akun: dua akun cabang untuk cabang yang sama ditolak.
	if _, err := tx.Exec(ctx,
		`INSERT INTO branch_users (branch_id, email, password_hash) VALUES ($1, $2, 'x')`,
		branchID, "akun1-"+s+"@contoh.id"); err != nil {
		t.Fatalf("akun cabang pertama: %v", err)
	}
	harusGagal(ctx, t, tx, "23505", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx,
			`INSERT INTO branch_users (branch_id, email, password_hash) VALUES ($1, $2, 'x')`,
			branchID, "akun2-"+s+"@contoh.id")
		return err
	})

	// Email akun cabang unik lintas cabang.
	other := buatCabang(ctx, t, tx, s+"b")
	harusGagal(ctx, t, tx, "23505", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx,
			`INSERT INTO branch_users (branch_id, email, password_hash) VALUES ($1, $2, 'x')`,
			other, "akun1-"+s+"@contoh.id")
		return err
	})
}

func TestConstraintSerialUnikDanFormat(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	s := unik("")
	branchID := buatCabang(ctx, t, tx, s)
	produk := buatProduk(ctx, t, tx, s, "reguler", 100000)
	orderID := buatOrderDraft(ctx, t, tx, branchID)

	serial := "20261008001"
	if err := tambahBarisProduk(ctx, tx, orderID, produk, "Produk A", 1, 100000, serial); err != nil {
		t.Fatalf("baris pertama: %v", err)
	}

	// Serial kembar di pesanan (atau lintas pesanan) ditolak.
	harusGagal(ctx, t, tx, "23505", func(sp pgx.Tx) error {
		return tambahBarisProduk(ctx, sp, orderID, produk, "Produk B", 1, 100000, serial)
	})

	// Serial harus berformat YYYYMMDDXXX minimal.
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		return tambahBarisProduk(ctx, sp, orderID, produk, "Produk C", 1, 100000, "bukan-angka")
	})

	// Baris packaging tidak boleh punya serial.
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		return tambahBarisPackaging(ctx, sp, orderID, 3000, "20261008002")
	})
}

func TestConstraintSatuSuperadminAktif(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	s := unik("")
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(ctx,
			`INSERT INTO admin_users (nama, email, role, is_active)
			 VALUES ($1, $2, 'superadmin', true)`,
			fmt.Sprintf("Superadmin %d", i), fmt.Sprintf("sa-%d-%s@contoh.id", i, s)); err != nil {
			t.Fatalf("insert superadmin: %v", err)
		}
	}

	// Susutkan seluruh superadmin aktif lain sampai tersisa satu.
	_, err = tx.Exec(ctx,
		`UPDATE admin_users SET is_active = false
		  WHERE role = 'superadmin' AND is_active
		    AND id <> (SELECT id FROM admin_users WHERE role = 'superadmin' AND is_active
		                ORDER BY created_at LIMIT 1)`)
	if err != nil {
		t.Fatalf("menonaktifkan superadmin berlebih: %v", err)
	}

	var sisa int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM admin_users WHERE role = 'superadmin' AND is_active`).Scan(&sisa); err != nil {
		t.Fatalf("hitung superadmin: %v", err)
	}
	if sisa != 1 {
		t.Fatalf("prasyarat uji: harus tersisa 1 superadmin aktif, dapat %d", sisa)
	}

	// Superadmin aktif terakhir tidak boleh dinonaktifkan.
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx,
			`UPDATE admin_users SET is_active = false WHERE role = 'superadmin' AND is_active`)
		return err
	})
}

// ---------- penomoran ----------

func TestCounterBersamaanTanpaCelah(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM numbering_counters WHERE periode LIKE 'uji-%'`)
	})

	for _, scope := range []string{"invoice", "serial"} {
		scope := scope
		periode := "uji-" + scope + "-" + unik("")
		const jumlah = 24

		hasil := make([]int, jumlah)
		galat := make([]error, jumlah)
		var wg sync.WaitGroup
		for i := 0; i < jumlah; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				tx, err := pool.Begin(ctx)
				if err != nil {
					galat[i] = err
					return
				}
				defer func() { _ = tx.Rollback(ctx) }()
				if err := tx.QueryRow(ctx,
					`SELECT next_numbers($1, $2, 1)`, scope, periode).Scan(&hasil[i]); err != nil {
					galat[i] = err
					return
				}
				galat[i] = tx.Commit(ctx)
			}(i)
		}
		wg.Wait()

		terlihat := map[int]bool{}
		for i := 0; i < jumlah; i++ {
			if galat[i] != nil {
				t.Fatalf("[%s] goroutine %d gagal: %v", scope, i, galat[i])
			}
			if hasil[i] < 1 || hasil[i] > jumlah {
				t.Fatalf("[%s] nomor di luar rentang 1..%d: %d", scope, jumlah, hasil[i])
			}
			if terlihat[hasil[i]] {
				t.Fatalf("[%s] nomor kembar dari submit bersamaan: %d", scope, hasil[i])
			}
			terlihat[hasil[i]] = true
		}
		for v := 1; v <= jumlah; v++ {
			if !terlihat[v] {
				t.Fatalf("[%s] nomor %d bolong setelah %d submit bersamaan", scope, v, jumlah)
			}
		}
	}
}

func TestCounterRollbackTanpaCelah(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	periode := "uji-rb-" + unik("")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM numbering_counters WHERE scope = 'invoice' AND periode = $1`, periode)
	})

	ambil := func(jumlah int, batalkan bool) int {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("mulai tx: %v", err)
		}
		var v int
		if err := tx.QueryRow(ctx,
			`SELECT next_numbers('invoice', $1, $2)`, periode, jumlah).Scan(&v); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("ambil nomor: %v", err)
		}
		if batalkan {
			if err := tx.Rollback(ctx); err != nil {
				t.Fatalf("rollback: %v", err)
			}
		} else if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return v
	}

	pertama := ambil(5, true)
	kedua := ambil(5, true)
	if pertama != kedua {
		t.Fatalf("rollback membocorkan nomor: %d lalu %d", pertama, kedua)
	}
	if kedua != 1 {
		t.Fatalf("nomor pertama setelah rollback harus 1, dapat %d", kedua)
	}

	if v := ambil(3, false); v != 1 {
		t.Fatalf("setelah rentang 3 nomor, nomor pertama harus 1, dapat %d", v)
	}
	if v := ambil(1, false); v != 4 {
		t.Fatalf("nomor berikut setelah 3 terpakai harus 4, dapat %d", v)
	}
}

func TestFungsiPenomoranWIB(t *testing.T) {
	ctx, pool := siapkanSkema(t)

	var periodeInvoice, periodeSerial, nomorInvoice, nomorInvoiceBesar, nomorSerial string
	err := pool.QueryRow(ctx, `
		SELECT invoice_periode('2026-10-07'),
		       serial_periode('2026-10-08'),
		       format_invoice_number('2026-10-07', 7, 2),
		       format_invoice_number('2026-10-07', 100, 2),
		       format_serial_number('2026-10-08', 3)`).
		Scan(&periodeInvoice, &periodeSerial, &nomorInvoice, &nomorInvoiceBesar, &nomorSerial)
	if err != nil {
		t.Fatalf("panggil fungsi penomoran: %v", err)
	}
	if periodeInvoice != "2026-10" {
		t.Errorf("invoice_periode = %q, ingin 2026-10", periodeInvoice)
	}
	if periodeSerial != "20261008" {
		t.Errorf("serial_periode = %q, ingin 20261008", periodeSerial)
	}
	if nomorInvoice != "2026-10-07" {
		t.Errorf("format_invoice_number(7,2) = %q, ingin 2026-10-07", nomorInvoice)
	}
	if nomorInvoiceBesar != "2026-10-100" {
		t.Errorf("format_invoice_number(100,2) harus menambah digit = %q, ingin 2026-10-100", nomorInvoiceBesar)
	}
	if nomorSerial != "20261008003" {
		t.Errorf("format_serial_number(3) = %q, ingin 20261008003", nomorSerial)
	}

	var wibBenar bool
	if err := pool.QueryRow(ctx,
		`SELECT wib_today() = (now() AT TIME ZONE 'Asia/Jakarta')::date`).Scan(&wibBenar); err != nil {
		t.Fatalf("wib_today: %v", err)
	}
	if !wibBenar {
		t.Error("wib_today() tidak sama dengan tanggal Asia/Jakarta saat ini")
	}
}

// ---------- invariant submit ----------

func TestInvariantSubmitPesanan(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("mulai tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	s := unik("")
	branchID := buatCabang(ctx, t, tx, s)
	produkReguler := buatProduk(ctx, t, tx, s, "reguler", 350000)
	produkSertifikat := buatProduk(ctx, t, tx, s+"-s", "sertifikat", 0)

	// 1. Draft boleh kosong (draft belum lengkap tidak boleh dihalangi trigger).
	draftKosong := buatOrderDraft(ctx, t, tx, branchID)
	var jumlahBaris int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM order_lines WHERE order_id = $1`, draftKosong).Scan(&jumlahBaris); err != nil {
		t.Fatalf("hitung baris draft: %v", err)
	}
	if jumlahBaris != 0 {
		t.Fatalf("draft kosong harus boleh, dapat %d baris", jumlahBaris)
	}

	// 2. Mencampur produk sertifikat & reguler ditolak.
	orderCampur := buatOrderDraft(ctx, t, tx, branchID)
	if err := tambahBarisProduk(ctx, tx, orderCampur, produkReguler, "Reguler", 1, 350000, "20261008001"); err != nil {
		t.Fatalf("baris reguler: %v", err)
	}
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		return tambahBarisProduk(ctx, sp, orderCampur, produkSertifikat, "Sertifikat", 1, 0, "20261008002")
	})

	// 3. Submit tanpa baris produk ditolak.
	orderHampa := buatOrderDraft(ctx, t, tx, branchID)
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		return submitOrder(ctx, sp, orderHampa, "uji-"+s+"-hampa", false)
	})

	// 4. Pesanan sertifikat wajib punya tepat satu baris packaging.
	orderSer := buatOrderDraft(ctx, t, tx, branchID)
	if err := tambahBarisProduk(ctx, tx, orderSer, produkSertifikat, "Sertifikat", 2, 0, "20261008003"); err != nil {
		t.Fatalf("baris sertifikat: %v", err)
	}
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		return submitOrder(ctx, sp, orderSer, "uji-"+s+"-ser", true)
	})
	if err := tambahBarisPackaging(ctx, tx, orderSer, 3000, ""); err != nil {
		t.Fatalf("tambah packaging: %v", err)
	}
	if err := submitOrder(ctx, tx, orderSer, "uji-"+s+"-ser", true); err != nil {
		t.Fatalf("submit sertifikat lengkap harus berhasil: %v", err)
	}

	// 5. Setiap baris produk wajib berserial saat submit (BR-09).
	orderTanpaSerial := buatOrderDraft(ctx, t, tx, branchID)
	if err := tambahBarisProduk(ctx, tx, orderTanpaSerial, produkReguler, "Reguler", 1, 350000, ""); err != nil {
		t.Fatalf("baris tanpa serial: %v", err)
	}
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		return submitOrder(ctx, sp, orderTanpaSerial, "uji-"+s+"-noserial", false)
	})
	if _, err := tx.Exec(ctx,
		`UPDATE order_lines SET serial_number = '20261008004' WHERE order_id = $1`, orderTanpaSerial); err != nil {
		t.Fatalf("isi serial: %v", err)
	}
	if err := submitOrder(ctx, tx, orderTanpaSerial, "uji-"+s+"-noserial", false); err != nil {
		t.Fatalf("submit setelah serial diisi harus berhasil: %v", err)
	}

	// 6. Baris packaging hanya untuk pesanan sertifikat.
	orderReguler := buatOrderDraft(ctx, t, tx, branchID)
	if err := tambahBarisProduk(ctx, tx, orderReguler, produkReguler, "Reguler", 1, 350000, "20261008005"); err != nil {
		t.Fatalf("baris reguler: %v", err)
	}
	if err := tambahBarisPackaging(ctx, tx, orderReguler, 3000, ""); err != nil {
		t.Fatalf("packaging pada reguler (insert): %v", err)
	}
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		return submitOrder(ctx, sp, orderReguler, "uji-"+s+"-reg", false)
	})

	// 7. Revisi admin yang sah tetap boleh mengubah baris pesanan Submitted.
	orderRevisi := buatOrderDraft(ctx, t, tx, branchID)
	if err := tambahBarisProduk(ctx, tx, orderRevisi, produkReguler, "Reguler", 1, 350000, "20261008006"); err != nil {
		t.Fatalf("baris reguler: %v", err)
	}
	if err := submitOrder(ctx, tx, orderRevisi, "uji-"+s+"-rev", false); err != nil {
		t.Fatalf("submit pesanan reguler: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE order_lines SET qty = qty + 1 WHERE order_id = $1`, orderRevisi); err != nil {
		t.Fatalf("revisi baris pesanan submitted harus boleh: %v", err)
	}
	var total int64
	if err := tx.QueryRow(ctx, `SELECT total FROM orders WHERE id = $1`, orderRevisi).Scan(&total); err != nil {
		t.Fatalf("baca total: %v", err)
	}
	if total != 700000 {
		t.Errorf("total setelah revisi qty 2 x 350000 harus 700000, dapat %d", total)
	}
}
