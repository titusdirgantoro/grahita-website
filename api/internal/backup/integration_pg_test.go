package backup_test

// Uji integrasi cadangan pada PostgreSQL NYATA (kartu F1-21): dump `pg_dump`
// yang benar-benar dienkripsi age, pencatatan `backup_runs`, sinkronisasi
// objek, pemulihan `pg_restore` ke database uji, penyamaran data staging, dan
// alarm kegagalan.
//
// Uji dijalankan bila TEST_DATABASE_URL diisi (CI menyediakannya). Database
// yang dipakai selalu dibuat ulang oleh uji ini dan diberi akhiran `_uji`
// sehingga penjaga "jangan hapus database non-uji" ikut teruji.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/backup"
	backuppg "github.com/grahita/portal/api/internal/backup/postgres"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// dsnDariKoneksi menyusun DSN untuk satu database pada host/user yang sama.
func dsnDariKoneksi(k backup.Koneksi, nama string) string {
	host := k.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := k.Port
	if port == 0 {
		port = 5432
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(k.User, k.Password),
		Host:   net.JoinHostPort(host, strconv.Itoa(int(port))),
		Path:   "/" + nama,
	}
	if k.SSLMode != "" {
		q := url.Values{}
		q.Set("sslmode", k.SSLMode)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// siapkanDatabaseUji membuat database bersih berakhiran `_uji`.
func siapkanDatabaseUji(t *testing.T, ctx context.Context, base backup.Koneksi, nama string) string {
	t.Helper()
	koneksi := base
	koneksi.Database = nama
	dibuat, err := backup.SiapkanDatabase(ctx, koneksi, true, false)
	if err != nil {
		t.Fatalf("siapkan database %s: %v", nama, err)
	}
	if !dibuat {
		t.Fatalf("database %s seharusnya dibuat ulang", nama)
	}
	return dsnDariKoneksi(base, nama)
}

// pasangShimBilaPerlu memasang `uuidv7()` pada PostgreSQL < 18 memakai
// scripts/pg-dev-shim.sql. Target resmi proyek adalah PG 18 (uuidv7 bawaan);
// bila fungsinya sudah ada, skrip TIDAK dijalankan sama sekali.
func pasangShimBilaPerlu(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi untuk shim: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var ada bool
	if err := conn.QueryRow(ctx, `SELECT to_regproc('uuidv7') IS NOT NULL`).Scan(&ada); err != nil {
		t.Fatalf("periksa uuidv7: %v", err)
	}
	if ada {
		return
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "pg-dev-shim.sql"))
	if err != nil {
		t.Fatalf("baca shim PG: %v", err)
	}
	// Buang baris komentar supaya yang dieksekusi benar-benar pernyataan SQL.
	bersih := []string{}
	for _, baris := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(baris), "--") {
			continue
		}
		bersih = append(bersih, baris)
	}
	if _, err := conn.Exec(ctx, strings.Join(bersih, "\n")); err != nil {
		t.Fatalf("pasang shim uuidv7: %v", err)
	}
}

// TestPGBackupRestoreLingkaranPenuh menjalankan siklus lengkap: migrasi
// database sumber → isi data bisnis + objek → cadangan terenkripsi → pulihkan
// ke database uji → bandingkan jumlah baris tiap tabel inti → pulihkan byte
// bukti/dokumen → samarkan data staging → simulasi kegagalan yang memicu alarm.
func TestPGBackupRestoreLingkaranPenuh(t *testing.T) {
	dsnUji := os.Getenv("TEST_DATABASE_URL")
	if dsnUji == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi cadangan dilewati")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	base, err := backup.KoneksiDariDSN(dsnUji)
	if err != nil {
		t.Fatalf("urai DSN uji: %v", err)
	}
	namaSumber := "grahita_f121_sumber_uji"
	namaTujuan := "grahita_f121_tujuan_uji"
	dsnSumber := siapkanDatabaseUji(t, ctx, base, namaSumber)
	dsnTujuan := siapkanDatabaseUji(t, ctx, base, namaTujuan)

	pasangShimBilaPerlu(t, ctx, dsnSumber)
	if err := database.Up(ctx, dsnSumber); err != nil {
		t.Fatalf("migrasi database sumber: %v", err)
	}
	pasangShimBilaPerlu(t, ctx, dsnTujuan)
	// Data bisnis minimal + objek bukti/dokumen.
	pool, err := database.Connect(ctx, dsnSumber)
	if err != nil {
		t.Fatalf("koneksi database sumber: %v", err)
	}
	defer pool.Close()

	kunciBukti := "proofs/order-uji/bukti-uji.jpg"
	kunciDokumen := "documents/order-uji/invoice_pdf-0.pdf"
	isiBukti := []byte("bukti transfer uji F1-21\n" + strings.Repeat("b", 2048))
	isiDokumen := []byte("%PDF-1.7 invoice uji F1-21\n")

	objek := storage.NewMemory("")
	if err := objek.Put(ctx, kunciBukti, strings.NewReader(string(isiBukti)), int64(len(isiBukti)), "image/jpeg"); err != nil {
		t.Fatalf("Put bukti: %v", err)
	}
	if err := objek.Put(ctx, kunciDokumen, strings.NewReader(string(isiDokumen)), int64(len(isiDokumen)), "application/pdf"); err != nil {
		t.Fatalf("Put dokumen: %v", err)
	}

	var branchID, orderID string
	// TIGA cabang dalam SATU pernyataan: id uuidv7 yang lahir pada milidetik
	// yang sama berbagi 8 karakter pertama, jadi penyamaran email yang memakai
	// awalan id akan melanggar unique branches.email — regresi yang ditemukan
	// probe live dan sekarang dijaga uji ini.
	if err := pool.QueryRow(ctx,
		`INSERT INTO branches (nama, nama_kepala, email, telepon) VALUES
		   ('Cabang Uji', 'Kepala Uji', 'cabang.uji@example.test', '0812000000'),
		   ('Cabang Uji 2', 'Kepala Uji 2', 'cabang.uji2@example.test', '0812000001'),
		   ('Cabang Uji 3', 'Kepala Uji 3', 'cabang.uji3@example.test', '0812000002')
		 RETURNING id`).Scan(&branchID); err != nil {
		t.Fatalf("insert cabang: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO admin_users (nama, email, role, password_hash, is_active, activated_at)
		 VALUES ('Rina Superadmin', 'rina.superadmin@example.test', 'superadmin', 'x', true, now())`); err != nil {
		t.Fatalf("insert superadmin: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, status)
		 VALUES ($1, 'draft') RETURNING id`, branchID).Scan(&orderID); err != nil {
		t.Fatalf("insert pesanan: %v", err)
	}
	hash := sha256.Sum256(isiBukti)
	if _, err := pool.Exec(ctx,
		`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256,
		                             uploaded_by_kind, uploaded_by_id, uploaded_by_nama)
		 VALUES ($1, $2, $3, 'bukti-uji.jpg', 'image/jpeg', $4, $5, 'branch', $2, 'Cabang Uji')`,
		orderID, branchID, kunciBukti, len(isiBukti), hash[:]); err != nil {
		t.Fatalf("insert bukti: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (order_id, jenis, versi, storage_key, ukuran_byte)
		 VALUES ($1, 'invoice_pdf', 0, $2, $3)`, orderID, kunciDokumen, len(isiDokumen)); err != nil {
		t.Fatalf("insert dokumen: %v", err)
	}
	// Sesi aktif + token reset: keduanya HARUS hilang dari salinan staging.
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (actor_kind, user_id, branch_id, token_hash, expires_at)
		 VALUES ('branch', $1, $1, 'sesi-uji', now() + interval '8 hours')`, branchID); err != nil {
		t.Fatalf("insert sesi: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO password_reset_tokens (actor_kind, user_id, token_hash, expires_at)
		 VALUES ('branch', $1, 'rahasia', now() + interval '1 hour')`, branchID); err != nil {
		t.Fatalf("insert token reset: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO audit_logs (actor_kind, actor_id, actor_nama, ip, entity, action)
		 VALUES ('admin', $1, 'Rina Superadmin', '203.0.113.7', 'order', 'verify')`,
		"00000000-0000-0000-0000-0000000000aa"); err != nil {
		t.Fatalf("insert audit: %v", err)
	}

	recipient, berkasIdentitas := kunciUji(t)
	dirTarget := t.TempDir()
	target, err := backup.NewDir(dirTarget)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	mail := &mailerPalsu{}
	repo := backuppg.New(pool)
	svc := backup.New(backup.Config{
		Kerja:          t.TempDir(),
		Recipient:      recipient,
		Identity:       berkasIdentitas,
		PgDump:         "pg_dump",
		PgRestore:      "pg_restore",
		DSN:            dsnSumber,
		RetensiHarian:  30,
		RetensiBulanan: 12,
		SweepGrace:     24 * time.Hour,
	}, repo,
		backup.WithTarget(target),
		backup.WithObjectStore(objek),
		backup.WithNotifier(backup.NewPemberitahu(repo, mail, "https://grahita-admin.tiusclaw.com")),
	)

	// 1) Cadangan database: pg_dump → age → tujuan.
	hasil := svc.Jalankan(ctx, backup.JenisPostgres)
	if !hasil.Sukses {
		t.Fatalf("cadangan postgres gagal: %v (%s)", hasil.Err, hasil.Pesan)
	}
	if hasil.UkuranByte <= 0 {
		t.Fatalf("ukuran cadangan = %d, mau > 0", hasil.UkuranByte)
	}
	if !strings.Contains(hasil.Tujuan, backup.PrefixPostgres) {
		t.Errorf("tujuan cadangan = %q, tidak menunjuk berkas di tujuan", hasil.Tujuan)
	}
	berkasCadangan := filepath.Join(dirTarget, backup.NamaCadanganPostgres(time.Now().In(jakartaUji())))
	raw, err := os.ReadFile(berkasCadangan)
	if err != nil {
		t.Fatalf("baca berkas cadangan: %v", err)
	}
	if !strings.HasPrefix(string(raw), "age-encryption.org/v1") {
		t.Fatal("berkas cadangan tidak terenkripsi age")
	}
	if strings.Contains(string(raw), "cabang.uji@example.test") {
		t.Fatal("cadangan memuat data polos — enkripsi tidak berjalan")
	}

	// 2) Catatan backup_runs nyata di database.
	var jenis, pesan string
	var sukses bool
	var ukuran int64
	var tujuan string
	if err := pool.QueryRow(ctx,
		`SELECT jenis, sukses, ukuran_byte, tujuan, pesan FROM backup_runs WHERE jenis = 'postgres' ORDER BY mulai DESC LIMIT 1`).
		Scan(&jenis, &sukses, &ukuran, &tujuan, &pesan); err != nil {
		t.Fatalf("baca backup_runs: %v", err)
	}
	if !sukses || ukuran <= 0 || !strings.Contains(tujuan, backup.PrefixPostgres) {
		t.Errorf("baris backup_runs tidak lengkap: jenis=%s sukses=%v ukuran=%d tujuan=%q pesan=%q", jenis, sukses, ukuran, tujuan, pesan)
	}

	// 3) sinkronisasi bukti/dokumen (terenkripsi + manifest).
	hasilStorage := svc.Jalankan(ctx, backup.JenisStorage)
	if !hasilStorage.Sukses {
		t.Fatalf("sinkronisasi storage gagal: %v (%s)", hasilStorage.Err, hasilStorage.Pesan)
	}
	if !strings.Contains(hasilStorage.Pesan, "baru=2") {
		t.Errorf("pesan sinkronisasi = %q, mau baru=2", hasilStorage.Pesan)
	}

	// 4) Uji restore ke database uji (anonymized staging) + pemulihan objek.
	dirObjek := t.TempDir()
	laporan, err := svc.UjiRestore(ctx, backup.OpsiUjiRestore{
		DSNTujuan: dsnTujuan,
		ObjekDir:  dirObjek,
		Anonim:    true,
		Bersih:    true,
	})
	if err != nil {
		t.Fatalf("uji restore gagal: %v (laporan=%+v)", err, laporan)
	}
	if !laporan.DataCocok {
		t.Errorf("jumlah baris hasil restore berbeda dari potret dump: %v", laporan.TabelBeda)
	}
	if !laporan.PotretDipakai {
		t.Error("uji restore harus memakai potret jumlah baris yang diambil saat dump dibuat")
	}
	if !laporan.ObjekCocok || laporan.ObjekDipulihkan != 2 {
		t.Errorf("pemulihan objek tidak utuh: dipulihkan=%d masalah=%v", laporan.ObjekDipulihkan, laporan.ObjekMasalah)
	}
	if laporan.UkuranDumpPolos <= 0 {
		t.Error("laporan tidak mencatat ukuran dump hasil dekripsi")
	}
	if !laporan.AnonimCocok {
		t.Errorf("penyamaran data staging tidak bersih: %+v", laporan.AnonimHasil)
	}

	// Objek yang dipulihkan harus byte-identik dengan aslinya.
	polosBukti, err := os.ReadFile(filepath.Join(dirObjek, filepath.FromSlash(kunciBukti)))
	if err != nil {
		t.Fatalf("baca bukti hasil pemulihan: %v", err)
	}
	if string(polosBukti) != string(isiBukti) {
		t.Error("byte bukti hasil pemulihan berbeda dari objek asli")
	}

	// Database hasil restore benar-benar berisi data, dan data pribadinya sudah
	// disamarkan (email cabang/admin, IP audit, sesi, token).
	conn, err := pgx.Connect(ctx, dsnTujuan)
	if err != nil {
		t.Fatalf("koneksi database tujuan: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var jumlahCabang int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM branches`).Scan(&jumlahCabang); err != nil {
		t.Fatalf("hitung cabang di hasil restore: %v", err)
	}
	if jumlahCabang != 3 {
		t.Errorf("cabang di hasil restore = %d, mau 3", jumlahCabang)
	}
	var emailCabang string
	if err := conn.QueryRow(ctx, `SELECT min(email::text) FROM branches`).Scan(&emailCabang); err != nil {
		t.Fatalf("baca email cabang: %v", err)
	}
	if !strings.HasSuffix(emailCabang, "@staging.invalid") {
		t.Errorf("email cabang di staging belum disamarkan: %q", emailCabang)
	}
	var emailUnik int
	if err := conn.QueryRow(ctx, `SELECT count(DISTINCT email) FROM branches`).Scan(&emailUnik); err != nil {
		t.Fatalf("hitung email unik: %v", err)
	}
	if emailUnik != 3 {
		t.Errorf("email hasil penyamaran tidak unik: %d dari 3", emailUnik)
	}
	var jumlahBukti int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM payment_proofs`).Scan(&jumlahBukti); err != nil {
		t.Fatalf("hitung bukti di hasil restore: %v", err)
	}
	if jumlahBukti != 1 {
		t.Errorf("baris payment_proofs di hasil restore = %d, mau 1 (restore baris DB↔objek)", jumlahBukti)
	}

	// 5) Simulasi kegagalan cadangan berikutnya: pg_dump tidak ada → run gagal
	//    tercatat dan alarm email terkirim.
	svcRusak := backup.New(backup.Config{
		Kerja:     t.TempDir(),
		Recipient: recipient,
		Identity:  berkasIdentitas,
		PgDump:    "/tidak-ada/pg_dump",
		DSN:       dsnSumber,
	}, repo, backup.WithTarget(target), backup.WithObjectStore(objek),
		backup.WithNotifier(backup.NewPemberitahu(repo, mail, "https://grahita-admin.tiusclaw.com")))
	gagal := svcRusak.Jalankan(ctx, backup.JenisPostgres)
	if gagal.Sukses {
		t.Fatal("cadangan dengan pg_dump tidak ada harus gagal")
	}
	var adaGagal bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM backup_runs WHERE jenis = 'postgres' AND sukses = false)`).Scan(&adaGagal); err != nil {
		t.Fatalf("periksa run gagal: %v", err)
	}
	if !adaGagal {
		t.Error("kegagalan cadangan tidak tercatat di backup_runs")
	}
	terkirim := mail.terkirim()
	if len(terkirim) != 1 {
		t.Fatalf("email alarm = %d, mau 1", len(terkirim))
	}
	if len(terkirim[0].To) != 1 || terkirim[0].To[0] != "rina.superadmin@example.test" {
		t.Errorf("penerima alarm = %v, mau Superadmin aktif dari database", terkirim[0].To)
	}

	// 6) Ringkasan "perlu tindakan" benar-benar terlihat dari data nyata.
	ringkasan, err := svc.Ringkasan(ctx, 10)
	if err != nil {
		t.Fatalf("Ringkasan: %v", err)
	}
	if !ringkasan.PerluTindakan {
		t.Error("kegagalan terakhir harus menandai perlu tindakan")
	}
	if len(ringkasan.Terakhir) < 3 {
		t.Errorf("ringkasan memuat %d run, mau minimal 3", len(ringkasan.Terakhir))
	}

	// 7) Verifikasi byte objek terhadap salinan cadangan (yang dipakai drill).
	manifest, err := backup.MuatManifest(ctx, target)
	if err != nil {
		t.Fatalf("MuatManifest: %v", err)
	}
	status, err := svc.PeriksaObjek(ctx, []string{kunciBukti, kunciDokumen}, manifest)
	if err != nil {
		t.Fatalf("PeriksaObjek: %v", err)
	}
	for _, st := range status {
		if !st.Cocok {
			t.Errorf("objek %s tidak cocok: %s", st.Kunci, st.Masalah)
		}
	}
	if hex.EncodeToString(hash[:]) == "" {
		t.Error("hash bukti uji tidak terhitung")
	}
}

// TestPGSiapkanDatabaseMenolakHapusNonUji menjaga pengaman "jangan sampai
// produksi terhapus karena salah ketik".
func TestPGSiapkanDatabaseMenolakHapusNonUji(t *testing.T) {
	dsnUji := os.Getenv("TEST_DATABASE_URL")
	if dsnUji == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi cadangan dilewati")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	koneksi, err := backup.KoneksiDariDSN(dsnUji)
	if err != nil {
		t.Fatalf("urai DSN uji: %v", err)
	}
	koneksi.Database = "grahita_produksi"
	if _, err := backup.SiapkanDatabase(ctx, koneksi, true, false); err == nil {
		t.Fatal("menghapus database bernama produksi harus ditolak")
	} else if !strings.Contains(err.Error(), "uji") {
		t.Errorf("pesan penolakan tidak menjelaskan pengamannya: %v", err)
	}
}

// jakartaUji mengembalikan zona WIB untuk nama berkas cadangan.
func jakartaUji() *time.Location {
	if loc, err := time.LoadLocation("Asia/Jakarta"); err == nil {
		return loc
	}
	return time.FixedZone("WIB", 7*60*60)
}
