package backup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// TabelInti adalah tabel yang jumlah barisnya dibandingkan antara database
// sumber dan hasil restore. Cadangan yang tidak bisa menghasilkan jumlah baris
// yang sama bukan cadangan.
//
// `backup_runs` SENGAJA tidak dibandingkan: tabel itu adalah buku catatan
// cadangan itu sendiri, sehingga jumlah barisnya berubah setiap kali pekerjaan
// baru berjalan (baris "sedang berjalan" pun dibuat SEBELUM dump diambil).
// Keberadaannya di hasil restore diperiksa terpisah lewat `JejakCadangan`.
// Tabel antrean job River juga bukan data bisnis, tetapi ikut dibandingkan
// bila memang ada di kedua sisi.
var TabelInti = []string{
	"branches", "admin_users", "branch_users", "registration_tokens",
	"password_reset_tokens", "sessions", "products", "branch_prices",
	"bank_accounts", "orders", "order_lines", "payment_proofs", "documents",
	"order_events", "verification_logs", "invoice_revisions", "cnc_reminders",
	"tempo_receipts", "tempo_hold_grace", "tempo_credit_entries", "tempo_charges",
	"tempo_allocations", "tempo_adjustments", "tempo_statements", "tempo_statement_lines", "tempo_reminders",
	"email_outbox", "settings", "audit_logs", "numbering_counters", "river_job",
}

// OpsiUjiRestore mengatur uji restore (Architecture.md §7: "Uji restore
// dijadwalkan setiap kuartal ke lingkungan staging").
type OpsiUjiRestore struct {
	// Berkas adalah nama cadangan yang dipakai; kosong berarti yang terbaru.
	Berkas string
	// DSNTujuan adalah database uji tempat pemulihan dilakukan.
	DSNTujuan string
	// ObjekDir adalah direktori pemulihan bukti/dokumen (kosong = dilewati).
	ObjekDir string
	// Anonim menjalankan penyamaran data setelah restore (lingkungan staging).
	Anonim bool
	// Bersih menghapus lalu membuat ulang database tujuan sebelum restore.
	Bersih bool
	// Paksa mengizinkan `Bersih` pada nama database yang tidak berakhiran
	// _uji/_staging (pengaman salah ketik: jangan sampai produksi terhapus).
	Paksa bool
}

// HasilAnonim merangkum pemeriksaan penyamaran data.
type HasilAnonim struct {
	EmailCabangBocor       int64 `json:"email_cabang_bocor"`
	EmailAdminBocor        int64 `json:"email_admin_bocor"`
	EmailOutboxBocor       int64 `json:"email_outbox_bocor"`
	SesiTersisa            int64 `json:"sesi_tersisa"`
	TokenResetTersisa      int64 `json:"token_reset_tersisa"`
	TokenRegistrasiTersisa int64 `json:"token_registrasi_tersisa"`
	IPAuditTersisa         int64 `json:"ip_audit_tersisa"`
}

// Bersih melaporkan apakah tidak ada lagi data pribadi di salinan staging.
func (h HasilAnonim) Bersih() bool {
	return h.EmailCabangBocor == 0 && h.EmailAdminBocor == 0 && h.EmailOutboxBocor == 0 &&
		h.SesiTersisa == 0 && h.TokenResetTersisa == 0 && h.TokenRegistrasiTersisa == 0 &&
		h.IPAuditTersisa == 0
}

// LaporanUjiRestore adalah bukti uji restore (boleh dilampirkan ke kartu QA).
type LaporanUjiRestore struct {
	Mulai           time.Time        `json:"mulai"`
	Selesai         time.Time        `json:"selesai"`
	Berkas          string           `json:"berkas"`
	UkuranDumpPolos int64            `json:"ukuran_dump_polos"`
	DatabaseTujuan  string           `json:"database_tujuan"`
	DatabaseDibuat  bool             `json:"database_dibuat"`
	StatistikSumber map[string]int64 `json:"statistik_sumber"`
	StatistikTujuan map[string]int64 `json:"statistik_tujuan"`
	TabelBeda       []string         `json:"tabel_beda,omitempty"`
	DataCocok       bool             `json:"data_cocok"`
	PotretDipakai   bool             `json:"potret_dipakai"`
	JejakCadangan   int64            `json:"jejak_cadangan"`
	ObjekDir        string           `json:"objek_dir,omitempty"`
	ObjekDiperiksa  int              `json:"objek_diperiksa"`
	ObjekDipulihkan int              `json:"objek_dipulihkan"`
	ObjekMasalah    []string         `json:"objek_masalah,omitempty"`
	ObjekCocok      bool             `json:"objek_cocok"`
	Anonim          bool             `json:"anonim"`
	AnonimHasil     HasilAnonim      `json:"anonim_hasil,omitempty"`
	AnonimCocok     bool             `json:"anonim_cocok"`
}

// UjiRestore menjalankan uji pemulihan lengkap: ambil cadangan terbaru dari
// tujuan, dekripsi, pulihkan ke database uji, bandingkan jumlah baris tiap
// tabel inti, pulihkan byte bukti/dokumen, dan (opsional) samarkan data
// pribadi untuk staging.
func (s *Service) UjiRestore(ctx context.Context, opsi OpsiUjiRestore) (LaporanUjiRestore, error) {
	laporan := LaporanUjiRestore{Mulai: s.clk.Now().UTC()}
	if err := s.targetSiap(); err != nil {
		return laporan, err
	}
	if strings.TrimSpace(opsi.DSNTujuan) == "" {
		return laporan, errors.New("DSN database tujuan uji restore wajib diisi (BACKUP_RESTORE_DSN)")
	}
	koneksiTujuan, err := KoneksiDariDSN(opsi.DSNTujuan)
	if err != nil {
		return laporan, err
	}
	if err := os.MkdirAll(s.cfg.Kerja, 0o700); err != nil {
		return laporan, fmt.Errorf("siapkan direktori kerja: %w", err)
	}

	rc, nama, err := s.BukaCadanganPostgres(ctx, opsi.Berkas)
	if err != nil {
		return laporan, err
	}
	polos := filepath.Join(s.cfg.Kerja, "restore-"+strconvNano(time.Now())+".dump")
	defer func() { _ = os.Remove(polos) }()

	ukuran, err := s.kripto.DekripsiPembacaKeBerkas(rc, polos)
	_ = rc.Close()
	if err != nil {
		return laporan, err
	}
	laporan.Berkas = nama
	laporan.UkuranDumpPolos = ukuran
	laporan.DatabaseTujuan = koneksiTujuan.Samarkan()

	dibuat, err := SiapkanDatabase(ctx, koneksiTujuan, opsi.Bersih, opsi.Paksa)
	if err != nil {
		return laporan, err
	}
	laporan.DatabaseDibuat = dibuat

	if err := s.dumper.Restore(ctx, koneksiTujuan, polos); err != nil {
		return laporan, err
	}

	sumber, err := statistikDatabase(ctx, s.cfg.DSN, TabelInti)
	if err != nil {
		return laporan, fmt.Errorf("statistik database sumber: %w", err)
	}
	tujuan, err := statistikDatabase(ctx, opsi.DSNTujuan, TabelInti)
	if err != nil {
		return laporan, fmt.Errorf("statistik database hasil restore: %w", err)
	}
	laporan.StatistikSumber = sumber
	laporan.StatistikTujuan = tujuan

	// Verifikasi terhadap POTRET jumlah baris yang diambil saat dump dibuat
	// (bukan terhadap database hidup yang terus berubah). Tanpa potret (dump
	// lama), perbandingan jatuh ke database hidup dan itu dinyatakan terbuka
	// di laporan supaya tidak disalahartikan sebagai bukti yang sama kuatnya.
	potret, adaPotret, err := MuatRingkasan(ctx, s.target, nama)
	if err != nil {
		return laporan, err
	}
	laporan.PotretDipakai = adaPotret
	if adaPotret {
		laporan.TabelBeda = bandingkanPotret(potret, tujuan)
	} else {
		laporan.TabelBeda = bandingkanStatistik(sumber, tujuan)
	}
	laporan.DataCocok = len(laporan.TabelBeda) == 0

	// Bukti bahwa salinan benar-benar cadangan milik sistem: hasil restore
	// harus memuat jejak backup_runs. Tanpa itu, "restore berhasil" bisa
	// berarti database kosong yang berhasil dipulihkan.
	//
	// Baris yang dicari TIDAK disyaratkan sudah selesai: baris run yang
	// menghasilkan dump ini memang masih berstatus "sedang berjalan" di dalam
	// dump (baris dibuat SEBELUM pg_dump, ditutup SESUDAHNYA).
	jejak, err := hitungJejakCadangan(ctx, opsi.DSNTujuan)
	if err != nil {
		return laporan, err
	}
	laporan.JejakCadangan = jejak
	if jejak == 0 {
		laporan.DataCocok = false
		laporan.TabelBeda = append(laporan.TabelBeda, "backup_runs(tanpa jejak cadangan)")
	}

	if strings.TrimSpace(opsi.ObjekDir) != "" {
		manifest, err := s.MuatManifestAtauDariDB(ctx)
		if err != nil {
			return laporan, err
		}
		laporan.ObjekDir = opsi.ObjekDir
		laporan.ObjekDiperiksa = len(manifest.Objek)
		dipulihkan, masalah, err := s.PulihkanObjek(ctx, opsi.ObjekDir, manifest)
		if err != nil {
			return laporan, err
		}
		laporan.ObjekDipulihkan = dipulihkan
		laporan.ObjekMasalah = masalah
		laporan.ObjekCocok = len(masalah) == 0 && dipulihkan == laporan.ObjekDiperiksa
	}

	if opsi.Anonim {
		if err := AnonimkanDatabase(ctx, opsi.DSNTujuan); err != nil {
			return laporan, err
		}
		hasil, err := verifikasiAnonim(ctx, opsi.DSNTujuan)
		if err != nil {
			return laporan, err
		}
		laporan.Anonim = true
		laporan.AnonimHasil = hasil
		laporan.AnonimCocok = hasil.Bersih()
	}

	laporan.Selesai = s.clk.Now().UTC()
	if !laporan.DataCocok {
		return laporan, fmt.Errorf("hasil restore berbeda dari sumber pada tabel: %s", strings.Join(laporan.TabelBeda, ", "))
	}
	if laporan.ObjekDir != "" && !laporan.ObjekCocok {
		return laporan, fmt.Errorf("%d objek gagal dipulihkan: %s", len(laporan.ObjekMasalah), batasi(laporan.ObjekMasalah, 5))
	}
	if laporan.Anonim && !laporan.AnonimCocok {
		return laporan, fmt.Errorf("penyamaran data belum bersih: %+v", laporan.AnonimHasil)
	}
	return laporan, nil
}

// MuatManifestAtauDariDB memuat manifest tujuan; bila belum ada (mis. tujuan
// baru), manifest disusun dari kunci yang DIRUJUK database supaya setiap baris
// documents/payment_proofs tetap diverifikasi ada salinannya.
func (s *Service) MuatManifestAtauDariDB(ctx context.Context) (Manifest, error) {
	manifest, err := MuatManifest(ctx, s.target)
	if err != nil {
		return manifest, err
	}
	if len(manifest.Objek) > 0 {
		return manifest, nil
	}
	proofs, err := s.repo.KunciProofs(ctx)
	if err != nil {
		return manifest, fmt.Errorf("baca kunci bukti transfer: %w", err)
	}
	dokumen, err := s.repo.KunciDokumen(ctx)
	if err != nil {
		return manifest, fmt.Errorf("baca kunci dokumen: %w", err)
	}
	for _, key := range append(proofs, dokumen...) {
		manifest.Objek[key] = EntriManifest{Nama: NamaCadanganObjek(key)}
	}
	return manifest, nil
}

// bandingkanPotret memverifikasi jumlah baris hasil restore terhadap potret
// yang diambil saat dump dibuat: dump adalah snapshot konsisten di antara
// penghitungan sebelum dan sesudah, jadi jumlah baris hasil restore harus
// berada di dalam rentang itu.
func bandingkanPotret(potret RingkasanCadangan, tujuan map[string]int64) []string {
	beda := []string{}
	for _, tabel := range TabelInti {
		n, ada := tujuan[tabel]
		sebelum, adaSebelum := potret.Sebelum[tabel]
		sesudah, adaSesudah := potret.Sesudah[tabel]
		if !ada || !adaSebelum || !adaSesudah {
			continue
		}
		if sebelum < 0 && sesudah < 0 {
			continue // tabel tidak ada saat dump (mis. antrean River belum dibuat)
		}
		if sebelum > sesudah {
			sebelum, sesudah = sesudah, sebelum
		}
		if n < sebelum || n > sesudah {
			beda = append(beda, fmt.Sprintf("%s(%d di luar rentang %d..%d)", tabel, n, sebelum, sesudah))
		}
	}
	sort.Strings(beda)
	return beda
}

// bandingkanStatistik mengembalikan tabel yang jumlah barisnya berbeda
// (dipakai hanya bila potret dump tidak tersedia).
func bandingkanStatistik(sumber, tujuan map[string]int64) []string {
	beda := []string{}
	for _, tabel := range TabelInti {
		s, adaS := sumber[tabel]
		t, adaT := tujuan[tabel]
		if !adaS || !adaT || (s < 0 && t < 0) {
			// Tabel tidak ada di KEDUA sisi (mis. tabel antrean River belum
			// dibuat di lingkungan uji) bukan perbedaan cadangan.
			continue
		}
		if s < 0 || t < 0 {
			beda = append(beda, tabel+"(tidak ada di salah satu sisi)")
			continue
		}
		if s != t {
			beda = append(beda, fmt.Sprintf("%s(%d!=%d)", tabel, s, t))
		}
	}
	sort.Strings(beda)
	return beda
}

// SiapkanDatabase memastikan database tujuan ada (membuatnya bila perlu).
//
// `bersih` menghapus lebih dulu; penghapusan HANYA diizinkan untuk nama
// database yang jelas-jelas lingkungan uji (mengandung uji/staging) kecuali
// `paksa` diminta eksplisit — pengaman terhadap salah ketik yang bisa
// menghapus database produksi. Penjaga ini diperiksa SEBELUM apa pun dibuat,
// supaya nama non-uji tidak pernah "dibuat lalu dihapus" oleh kelalaian.
func SiapkanDatabase(ctx context.Context, koneksi Koneksi, bersih, paksa bool) (dibuat bool, err error) {
	if bersih && !akhiranUji(koneksi.Database) && !paksa {
		return false, fmt.Errorf("menolak menghapus database %q: nama tidak dikenali sebagai lingkungan uji (pakai --paksa bila memang disengaja)", koneksi.Database)
	}
	ada, err := databaseAda(ctx, koneksi)
	if err != nil {
		return false, err
	}
	if ada && bersih {
		if err := hapusDatabase(ctx, koneksi); err != nil {
			return false, err
		}
		ada = false
	}
	if ada {
		return false, nil
	}
	if err := buatDatabase(ctx, koneksi); err != nil {
		return false, err
	}
	return true, nil
}

// akhiranUji melaporkan apakah nama database menandakan lingkungan uji.
func akhiranUji(nama string) bool {
	n := strings.ToLower(nama)
	return strings.HasSuffix(n, "_uji") || strings.HasSuffix(n, "_staging") || strings.Contains(n, "uji") || strings.Contains(n, "staging")
}

// dsnDari menyusun DSN untuk satu database pada koneksi yang sama.
func dsnDari(k Koneksi, database string) string {
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
		Path:   "/" + database,
	}
	if k.SSLMode != "" {
		q := url.Values{}
		q.Set("sslmode", k.SSLMode)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// databaseAda memeriksa keberadaan database (lewat katalog pg_database).
func databaseAda(ctx context.Context, koneksi Koneksi) (bool, error) {
	conn, err := pgx.Connect(ctx, dsnDari(koneksi, "postgres"))
	if err != nil {
		return false, fmt.Errorf("hubungi database pemeliharaan: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var ada bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, koneksi.Database).Scan(&ada); err != nil {
		return false, fmt.Errorf("periksa keberadaan database %s: %w", koneksi.Database, err)
	}
	return ada, nil
}

// buatDatabase membuat database tujuan.
func buatDatabase(ctx context.Context, koneksi Koneksi) error {
	conn, err := pgx.Connect(ctx, dsnDari(koneksi, "postgres"))
	if err != nil {
		return fmt.Errorf("hubungi database pemeliharaan: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	sql := "CREATE DATABASE " + pgx.Identifier{koneksi.Database}.Sanitize()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("buat database %s: %w", koneksi.Database, err)
	}
	return nil
}

// hapusDatabase menghapus database tujuan beserta koneksi yang menggantung.
func hapusDatabase(ctx context.Context, koneksi Koneksi) error {
	conn, err := pgx.Connect(ctx, dsnDari(koneksi, "postgres"))
	if err != nil {
		return fmt.Errorf("hubungi database pemeliharaan: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	// Putuskan sesi lain supaya DROP tidak gagal karena koneksi menggantung.
	_, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, koneksi.Database)
	sql := "DROP DATABASE IF EXISTS " + pgx.Identifier{koneksi.Database}.Sanitize()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("hapus database %s: %w", koneksi.Database, err)
	}
	return nil
}

// statistikDatabase menghitung jumlah baris tabel inti; tabel yang tidak ada
// dilaporkan sebagai -1 (bukan kegagalan) supaya perbedaannya terlihat.
func statistikDatabase(ctx context.Context, dsn string, tabel []string) (map[string]int64, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("hubungi database: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	out := make(map[string]int64, len(tabel))
	for _, t := range tabel {
		var n int64
		sql := "SELECT count(*) FROM " + pgx.Identifier{t}.Sanitize()
		if err := conn.QueryRow(ctx, sql).Scan(&n); err != nil {
			out[t] = -1
			continue
		}
		out[t] = n
	}
	return out, nil
}

// hitungJejakCadangan menghitung jejak cadangan sukses di database hasil
// restore (bukti salinan itu memang cadangan sistem, bukan database kosong).
func hitungJejakCadangan(ctx context.Context, dsn string) (int64, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, fmt.Errorf("hubungi database hasil restore: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int64
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM backup_runs WHERE jenis = 'postgres'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("hitung jejak cadangan: %w", err)
	}
	return n, nil
}

// sqlAnonim adalah rangkaian penyamaran data untuk salinan staging.
//
// Yang disamarkan: identitas cabang/admin, alamat email outbox, pesan galat,
// dan IP audit. Sesi serta token (reset & registrasi) DIHAPUS karena keduanya
// adalah kredensial yang bisa dipakai masuk ke salinan.
var sqlAnonim = []string{
	// Email disamarkan memakai ID penuh: awalan 8 karakter id uuidv7 berasal
	// dari milidetik dan bisa BENTROK antar baris (ditemukan pada probe live
	// F1-21: beberapa cabang seed lahir pada milidetik yang sama sehingga
	// `left(id::text,8)` melanggar unique branches.email).
	`UPDATE branches SET email = 'cabang-' || id::text || '@staging.invalid',
	       nama = 'Cabang ' || left(id::text, 8), nama_kepala = 'Kepala Cabang', telepon = NULL`,
	`UPDATE admin_users SET email = 'admin-' || id::text || '@staging.invalid',
	       nama = 'Admin ' || left(id::text, 8)`,
	`UPDATE audit_logs SET ip = NULL, actor_nama = NULL`,
	`UPDATE email_outbox SET to_addr = ARRAY['staging.invalid'::text], cc_addr = NULL,
	       payload = '{}'::jsonb, last_error = NULL`,
	`DELETE FROM sessions`,
	`DELETE FROM password_reset_tokens`,
	`DELETE FROM registration_tokens`,
}

// AnonimkanDatabase menyamarkan data pribadi pada database hasil restore.
func AnonimkanDatabase(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("hubungi database hasil restore: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, sql := range sqlAnonim {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("samarkan data (%s): %w", ringkasSQL(sql), err)
		}
	}
	return nil
}

// verifikasiAnonim memastikan tidak ada data pribadi yang tersisa.
func verifikasiAnonim(ctx context.Context, dsn string) (HasilAnonim, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return HasilAnonim{}, fmt.Errorf("hubungi database hasil restore: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var h HasilAnonim
	periksa := []struct {
		tujuan *int64
		sql    string
	}{
		{&h.EmailCabangBocor, `SELECT count(*) FROM branches WHERE email NOT LIKE '%@staging.invalid'`},
		{&h.EmailAdminBocor, `SELECT count(*) FROM admin_users WHERE email NOT LIKE '%@staging.invalid'`},
		{&h.EmailOutboxBocor, `SELECT count(*) FROM email_outbox WHERE NOT (to_addr @> ARRAY['staging.invalid'::text])`},
		{&h.SesiTersisa, `SELECT count(*) FROM sessions`},
		{&h.TokenResetTersisa, `SELECT count(*) FROM password_reset_tokens`},
		{&h.TokenRegistrasiTersisa, `SELECT count(*) FROM registration_tokens`},
		{&h.IPAuditTersisa, `SELECT count(*) FROM audit_logs WHERE ip IS NOT NULL`},
	}
	for _, p := range periksa {
		if err := conn.QueryRow(ctx, p.sql).Scan(p.tujuan); err != nil {
			return HasilAnonim{}, fmt.Errorf("verifikasi penyamaran: %w", err)
		}
	}
	return h, nil
}

// ringkasSQL memotong SQL supaya pesan galat tetap terbaca.
func ringkasSQL(sql string) string {
	sql = strings.Join(strings.Fields(sql), " ")
	if len(sql) > 60 {
		return sql[:60] + "..."
	}
	return sql
}
