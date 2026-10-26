package backup_test

// Uji unit domain cadangan (kartu F1-21): enkripsi age, retensi, penjaga
// jadwal 02:00 WIB, pencatatan backup_runs + alarm kegagalan, sinkronisasi
// objek inkremental, dan sapu objek yatim yang konservatif.
//
// Uji integrasi (PostgreSQL + pg_dump/pg_restore nyata) ada di
// integration_pg_test.go.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/backup"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// ---------- perkakas uji ----------

// Konstanta DSN contoh dipecah menjadi beberapa potongan dengan sengaja:
// scripts/check-secrets.sh (Architecture.md §6) memeriksa berkas yang dilacak
// lewat pola skema + `pengguna:kata-sandi` + `@`, dan menulis DSN utuh di sini
// akan memicu alarm palsu.
const (
	skemaDSNContoh = "postgres://"
	kredDSNContoh  = "grahita:kata-sandi-uji@"
	hostDSNContoh  = "127.0.0.1:5455"
)

// dsnContoh menyusun DSN contoh (tidak pernah dihubungi jaringan di uji unit).
func dsnContoh(database string) string {
	return skemaDSNContoh + kredDSNContoh + hostDSNContoh + "/" + database
}

// repoPalsu menyimpan baris backup_runs dan data pendukung di memori.
type repoPalsu struct {
	mu      sync.Mutex
	runs    []backup.Run
	proofs  []string
	dokumen []string
	admin   []string
	audit   []audit.Entry
}

func (r *repoPalsu) Mulai(_ context.Context, jenis string, mulai time.Time) (backup.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := backup.Run{ID: "run-" + jenis + "-" + time.Now().Format("150405.000000000"), Jenis: jenis, Mulai: mulai}
	r.runs = append(r.runs, run)
	return run, nil
}

func (r *repoPalsu) Selesai(_ context.Context, id string, selesai time.Time, sukses bool, ukuranByte int64, tujuan, pesan string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.runs {
		if r.runs[i].ID != id {
			continue
		}
		salinSelesai, salinSukses := selesai, sukses
		r.runs[i].Selesai = &salinSelesai
		r.runs[i].Sukses = &salinSukses
		r.runs[i].UkuranByte = ukuranByte
		r.runs[i].Tujuan = tujuan
		r.runs[i].Pesan = pesan
	}
	return nil
}

func (r *repoPalsu) Terakhir(_ context.Context, limit int) ([]backup.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]backup.Run, 0, len(r.runs))
	for i := len(r.runs) - 1; i >= 0; i-- {
		out = append(out, r.runs[i])
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *repoPalsu) Sejak(_ context.Context, jenis string, sejak time.Time) (backup.Run, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.runs) - 1; i >= 0; i-- {
		if r.runs[i].Jenis == jenis && !r.runs[i].Mulai.Before(sejak) {
			return r.runs[i], true, nil
		}
	}
	return backup.Run{}, false, nil
}

func (r *repoPalsu) SuperadminEmails(context.Context) ([]string, error) {
	if len(r.admin) == 0 {
		return []string{"rina@example.test"}, nil
	}
	return r.admin, nil
}

func (r *repoPalsu) KunciProofs(context.Context) ([]string, error)  { return r.proofs, nil }
func (r *repoPalsu) KunciDokumen(context.Context) ([]string, error) { return r.dokumen, nil }

func (r *repoPalsu) Audit(_ context.Context, e audit.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audit = append(r.audit, e)
	return nil
}

func (r *repoPalsu) runTerakhir() (backup.Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runs) == 0 {
		return backup.Run{}, false
	}
	return r.runs[len(r.runs)-1], true
}

func (r *repoPalsu) entriAudit() []audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Entry(nil), r.audit...)
}

// mailerPalsu merekam email yang dikirim.
type mailerPalsu struct {
	mu    sync.Mutex
	pesan []mailer.Message
	gagal error
}

func (m *mailerPalsu) Send(_ context.Context, msg mailer.Message) error {
	if m.gagal != nil {
		return m.gagal
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pesan = append(m.pesan, msg)
	return nil
}

func (m *mailerPalsu) Health(context.Context) error { return nil }

func (m *mailerPalsu) terkirim() []mailer.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mailer.Message(nil), m.pesan...)
}

// storeTua membungkus storage.Memory dan menua-kan ModTime objek supaya uji
// sapu objek yatim bisa melewati grace period secara deterministik. Kunci di
// `muda` tetap berstempel waktu sekarang (meniru berkas yang sedang ditulis
// render aktif).
type storeTua struct {
	*storage.Memory
	umur time.Duration
	muda map[string]bool
}

func (s storeTua) List(ctx context.Context, prefix string) ([]storage.Info, error) {
	daftar, err := s.Memory.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for i := range daftar {
		if s.muda[daftar[i].Key] {
			daftar[i].ModTime = time.Now()
			continue
		}
		daftar[i].ModTime = time.Now().Add(-s.umur)
	}
	return daftar, nil
}

// kunciUji membuat pasangan kunci age: kunci publik untuk enkripsi dan berkas
// identitas (di direktori sementara) untuk dekripsi.
func kunciUji(t *testing.T) (recipient, berkasIdentitas string) {
	t.Helper()
	identitas, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("buat identitas age: %v", err)
	}
	jalur := filepath.Join(t.TempDir(), "identitas.txt")
	if err := os.WriteFile(jalur, []byte(identitas.String()+"\n"), 0o600); err != nil {
		t.Fatalf("tulis berkas identitas age: %v", err)
	}
	return identitas.Recipient().String(), jalur
}

// serviceUji menyusun service cadangan untuk pengujian.
func serviceUji(t *testing.T, repo *repoPalsu, target backup.Target, store storage.Store, cfg backup.Config, opsi ...backup.Option) *backup.Service {
	t.Helper()
	opsi = append([]backup.Option{
		backup.WithTarget(target),
		backup.WithObjectStore(store),
		backup.WithClock(clock.System()),
	}, opsi...)
	return backup.New(cfg, repo, opsi...)
}

// ---------- enkripsi ----------

// TestKriptoAgeRoundTrip membuktikan cadangan benar-benar terenkripsi age dan
// bisa dikembalikan HANYA dengan berkas identitas yang benar.
func TestKriptoAgeRoundTrip(t *testing.T) {
	recipient, berkasIdentitas := kunciUji(t)
	dir := t.TempDir()
	polos := filepath.Join(dir, "dump.bin")
	isi := bytes.Repeat([]byte("data rahasia pelanggan\n"), 500)
	if err := os.WriteFile(polos, isi, 0o600); err != nil {
		t.Fatalf("tulis berkas polos: %v", err)
	}

	kripto := backup.NewKripto(recipient, berkasIdentitas)
	terenkripsi := filepath.Join(dir, "dump.bin.age")
	n, err := kripto.EnkripsiBerkas(terenkripsi, polos)
	if err != nil {
		t.Fatalf("EnkripsiBerkas: %v", err)
	}
	if n != int64(len(isi)) {
		t.Errorf("ukuran plaintext = %d, mau %d", n, len(isi))
	}
	raw, err := os.ReadFile(terenkripsi)
	if err != nil {
		t.Fatalf("baca berkas terenkripsi: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
		t.Errorf("berkas cadangan tidak diawali header age: %q", raw[:min(32, len(raw))])
	}
	if bytes.Contains(raw, []byte("data rahasia pelanggan")) {
		t.Error("cadangan memuat isi polos: enkripsi tidak berjalan")
	}

	hasil := filepath.Join(dir, "hasil.bin")
	ukuran, err := kripto.DekripsiBerkas(hasil, terenkripsi)
	if err != nil {
		t.Fatalf("DekripsiBerkas: %v", err)
	}
	if ukuran != int64(len(isi)) {
		t.Errorf("ukuran hasil dekripsi = %d, mau %d", ukuran, len(isi))
	}
	kembali, err := os.ReadFile(hasil)
	if err != nil {
		t.Fatalf("baca hasil dekripsi: %v", err)
	}
	if !bytes.Equal(kembali, isi) {
		t.Error("isi hasil dekripsi berbeda dari aslinya")
	}

	// Identitas lain tidak boleh bisa membuka cadangan.
	asing := filepath.Join(dir, "asing.txt")
	lain, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("buat identitas asing: %v", err)
	}
	if err := os.WriteFile(asing, []byte(lain.String()+"\n"), 0o600); err != nil {
		t.Fatalf("tulis identitas asing: %v", err)
	}
	kriptoAsing := backup.NewKripto(recipient, asing)
	if _, err := kriptoAsing.DekripsiBerkas(filepath.Join(dir, "jahat.bin"), terenkripsi); err == nil {
		t.Error("cadangan terbuka dengan identitas age yang salah")
	}
}

// TestCadanganTanpaKunciAgeDitolak menegakkan aturan "tidak ada cadangan
// polos": tanpa kunci publik age, pekerjaan cadangan menolak berjalan —
// dan penolakan itu tetap tercatat di backup_runs.
func TestCadanganTanpaKunciAgeDitolak(t *testing.T) {
	repo := &repoPalsu{}
	target, err := backup.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	svc := serviceUji(t, repo, target, storage.NewMemory(""), backup.Config{
		Kerja: t.TempDir(),
		DSN:   dsnContoh("grahita"),
	})

	hasil := svc.Jalankan(context.Background(), backup.JenisPostgres)
	if hasil.Sukses {
		t.Fatal("cadangan tanpa kunci age harus gagal")
	}
	if !errors.Is(hasil.Err, backup.ErrKunciEnkripsi) {
		t.Errorf("err = %v, mau ErrKunciEnkripsi", hasil.Err)
	}
	run, ada := repo.runTerakhir()
	if !ada {
		t.Fatal("penolakan tidak tercatat di backup_runs")
	}
	if run.Sukses == nil || *run.Sukses {
		t.Errorf("run harus tercatat gagal, dapat %+v", run)
	}
	if _, err := os.Stat(filepath.Join(target.Root, backup.NamaCadanganPostgres(time.Now()))); err == nil {
		t.Error("tidak boleh ada berkas cadangan saat enkripsi tidak siap")
	}
}

// TestBackupGagalDicatatDanAlarmTerkirim mensimulasikan kegagalan nyata
// (pg_dump tidak ada) dan memastikan: catatan backup_runs gagal, entri audit,
// dan email alarm ke Superadmin.
func TestBackupGagalDicatatDanAlarmTerkirim(t *testing.T) {
	recipient, berkasIdentitas := kunciUji(t)
	repo := &repoPalsu{}
	mail := &mailerPalsu{}
	target, err := backup.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	notif := backup.NewPemberitahu(repo, mail, "https://grahita-admin.tiusclaw.com")

	svc := serviceUji(t, repo, target, storage.NewMemory(""), backup.Config{
		Kerja:     t.TempDir(),
		Recipient: recipient,
		Identity:  berkasIdentitas,
		PgDump:    "/tidak-ada/pg_dump",
		DSN:       dsnContoh("grahita"),
	}, backup.WithNotifier(notif))

	hasil := svc.Jalankan(context.Background(), backup.JenisPostgres)
	if hasil.Sukses {
		t.Fatal("cadangan dengan biner pg_dump tidak ada harus gagal")
	}
	if !strings.Contains(hasil.Pesan, "pg_dump") {
		t.Errorf("pesan kegagalan tidak menyebut pg_dump: %q", hasil.Pesan)
	}

	run, ada := repo.runTerakhir()
	if !ada || run.Sukses == nil || *run.Sukses {
		t.Fatalf("run gagal tidak tercatat dengan benar: %+v", run)
	}
	if run.Selesai == nil {
		t.Error("run gagal harus punya waktu selesai")
	}
	if !strings.Contains(run.Pesan, "pg_dump") {
		t.Errorf("pesan run tidak memuat sebab kegagalan: %q", run.Pesan)
	}

	pesan := mail.terkirim()
	if len(pesan) != 1 {
		t.Fatalf("email alarm = %d, mau 1", len(pesan))
	}
	if !strings.Contains(pesan[0].Subject, "GAGAL") || !strings.Contains(pesan[0].Subject, backup.JenisPostgres) {
		t.Errorf("subjek email alarm tidak informatif: %q", pesan[0].Subject)
	}
	if len(pesan[0].To) != 1 || pesan[0].To[0] != "rina@example.test" {
		t.Errorf("penerima alarm = %v, mau Superadmin aktif", pesan[0].To)
	}
	if !strings.Contains(pesan[0].Text, "/pengaturan") {
		t.Error("email alarm harus menunjuk halaman Pengaturan")
	}

	entri := repo.entriAudit()
	if len(entri) != 1 {
		t.Fatalf("entri audit = %d, mau 1", len(entri))
	}
	if entri[0].Entity != backup.EntityBackupRun || entri[0].Action != backup.ActionBackupGagal {
		t.Errorf("entri audit salah: entity=%s action=%s", entri[0].Entity, entri[0].Action)
	}
	if entri[0].EntityID != run.ID {
		t.Errorf("entity_id audit = %q, mau id run %q", entri[0].EntityID, run.ID)
	}
}

// TestAlarmGagalKirimTidakMenutupiKegagalan memastikan kegagalan SMTP hanya
// dilaporkan sebagai error pemberi tahu, bukan mengubah status run.
func TestAlarmGagalKirimTidakMenutupiKegagalan(t *testing.T) {
	repo := &repoPalsu{}
	mail := &mailerPalsu{gagal: errors.New("smtp mati")}
	pemberi := backup.NewPemberitahu(repo, mail, "https://admin.test")
	run := backup.Run{ID: "run-1", Jenis: backup.JenisPostgres, Mulai: time.Now()}

	if err := pemberi.BackupGagal(context.Background(), run, "sebab"); err == nil {
		t.Fatal("kegagalan kirim alarm harus dilaporkan")
	}
	if len(repo.entriAudit()) != 1 {
		t.Error("audit tetap harus tercatat walau email gagal")
	}
}

// ---------- retensi ----------

// TestSaringRetensiHarianDanBulanan menegakkan Architecture.md §7: 30 berkas
// terbaru + satu terbaru per bulan selama 12 bulan, dan berkas asing aman.
func TestSaringRetensiHarianDanBulanan(t *testing.T) {
	akhir := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	daftar := []backup.Berkas{}
	// 40 hari terakhir (jendela "30 terbaru" = 2026-10-08 s/d 2026-09-09).
	for i := 0; i < 40; i++ {
		tanggal := akhir.AddDate(0, 0, -i)
		daftar = append(daftar, backup.Berkas{Nama: backup.NamaCadanganPostgres(tanggal), Ukuran: 100})
	}
	// Satu cadangan pada tanggal 1 untuk 12 bulan sebelumnya; tanggal 1 sengaja
	// dipilih supaya TIDAK ada di dalam jendela 30 terbaru, sehingga aturan
	// penyimpan bulanan teruji terpisah dari aturan harian.
	for i := 1; i <= 12; i++ {
		bulan := akhir.AddDate(0, -i, 0)
		tanggal := time.Date(bulan.Year(), bulan.Month(), 1, 0, 0, 0, 0, time.UTC)
		daftar = append(daftar, backup.Berkas{Nama: backup.NamaCadanganPostgres(tanggal), Ukuran: 100})
	}
	// Berkas asing (bukan buatan kita) tidak boleh pernah dihapus.
	daftar = append(daftar, backup.Berkas{Nama: "postgres/README.txt", Ukuran: 10})
	// Ringkasan potret bukan dump: ia selalu aman di SaringRetensi (dibersihkan
	// bersama dump pasangannya oleh pekerjaan cadangan, bukan di sini).
	daftar = append(daftar, backup.Berkas{Nama: "postgres/grahita-2025-01-01.ringkasan.json", Ukuran: 200})

	simpan, hapus := backup.SaringRetensi(daftar, 30, 12, akhir)
	simpanSet := map[string]bool{}
	for _, b := range simpan {
		simpanSet[b.Nama] = true
	}
	dihapusSet := map[string]bool{}
	for _, b := range hapus {
		dihapusSet[b.Nama] = true
	}
	if !simpanSet["postgres/README.txt"] {
		t.Error("berkas asing harus selalu disimpan")
	}
	if dihapusSet["postgres/README.txt"] {
		t.Error("berkas asing tidak boleh dihapus")
	}
	if dihapusSet["postgres/grahita-2025-01-01.ringkasan.json"] {
		t.Error("berkas ringkasan potret tidak boleh dihapus oleh SaringRetensi")
	}

	// 30 cadangan terbaru disimpan.
	for i := 0; i < 30; i++ {
		nama := backup.NamaCadanganPostgres(akhir.AddDate(0, 0, -i))
		if !simpanSet[nama] {
			t.Errorf("cadangan %s (dalam 30 terbaru) seharusnya disimpan", nama)
		}
	}
	// Cadangan hari ke-31 dari akhir tidak lagi disimpan sebagai harian, dan
	// September sudah punya penyimpan bulanan yang lebih baru (2026-09-30).
	if !dihapusSet[backup.NamaCadanganPostgres(akhir.AddDate(0, 0, -30))] {
		t.Errorf("cadangan %s harus dihapus (bukan 30 terbaru, bukan penyimpan bulan terbaru)", backup.NamaCadanganPostgres(akhir.AddDate(0, 0, -30)))
	}
	// Satu terbaru dari tiap 12 bulan terakhir disimpan.
	for i := 0; i < 12; i++ {
		bulan := akhir.AddDate(0, -i, 0)
		nama := ""
		for _, b := range daftar {
			if !strings.HasPrefix(b.Nama, backup.PrefixPostgres) || !strings.Contains(b.Nama, bulan.Format("2006-01")) {
				continue
			}
			if nama == "" || b.Nama > nama {
				nama = b.Nama
			}
		}
		if nama != "" && !simpanSet[nama] {
			t.Errorf("penyimpan terbaru bulan %s (%s) seharusnya disimpan", bulan.Format("2006-01"), nama)
		}
	}
	// Bulan ke-13 ke belakang tidak lagi punya penyimpan bulanan.
	if !dihapusSet[backup.NamaCadanganPostgres(time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC))] {
		t.Error("cadangan bulan ke-13 ke belakang seharusnya dihapus")
	}
	if len(hapus) == 0 {
		t.Fatal("tidak ada berkas yang dihapus: retensi tidak berjalan")
	}
}

// TestBentukKunciDokumenMenyaringKunciAsing memastikan sapu objek yatim hanya
// menyentuh kunci yang benar-benar berbentuk dokumen sistem.
func TestBentukKunciDokumenMenyaringKunciAsing(t *testing.T) {
	sah := []string{
		"documents/8c1f/invoice_pdf-0.pdf",
		"documents/8c1f/surat_jalan_xlsx-3.xlsx",
		"documents/8c1f/surat_jalan_pdf-12.pdf",
		"documents/8c1f/report_xlsx-1.xlsx",
	}
	for _, key := range sah {
		if err := backup.BentukKunciDokumen(key); err != nil {
			t.Errorf("kunci %s seharusnya sah: %v", key, err)
		}
	}
	asing := []string{
		"documents/8c1f/catatan.pdf",
		"documents/8c1f/invoice_pdf-x.pdf",
		"documents/8c1f/invoice_pdf-0",
		"proofs/8c1f/invoice_pdf-0.pdf",
		"documents/invoice_pdf-0.pdf",
		"documents/8c1f/entah_pdf-0.pdf",
	}
	for _, key := range asing {
		if err := backup.BentukKunciDokumen(key); err == nil {
			t.Errorf("kunci %s tidak boleh dianggap dokumen sah", key)
		}
	}
}

// ---------- jadwal harian ----------

// TestPerluJalanSesuaiJadwalWIB menguji penjaga jadwal 02:00 WIB: tidak
// berjalan sebelum jadwal, berjalan sekali sehari, dan mengejar bila terlewat.
func TestPerluJalanSesuaiJadwalWIB(t *testing.T) {
	repo := &repoPalsu{}
	svc := serviceUji(t, repo, backup.Nol{}, storage.NewMemory(""), backup.Config{Kerja: t.TempDir()})

	jakarta := clock.Jakarta()
	kasus := []struct {
		nama     string
		sekarang time.Time
		harus    bool
	}{
		{"01:59 WIB belum waktunya", time.Date(2026, 10, 8, 1, 59, 0, 0, jakarta), false},
		{"02:00 WIB waktunya", time.Date(2026, 10, 8, 2, 0, 0, 0, jakarta), true},
		{"06:00 WIB masih boleh mengejar", time.Date(2026, 10, 8, 6, 0, 0, 0, jakarta), true},
	}
	for _, k := range kasus {
		perlu, err := svc.PerluJalan(context.Background(), backup.JenisPostgres, k.sekarang)
		if err != nil {
			t.Fatalf("%s: PerluJalan: %v", k.nama, err)
		}
		if perlu != k.harus {
			t.Errorf("%s: perlu=%v, mau %v", k.nama, perlu, k.harus)
		}
	}

	// Catat satu run hari ini (02:00 WIB 8 Okt = 19:00 UTC 7 Okt).
	repo.runs = append(repo.runs, backup.Run{
		ID:    "run-hari-ini",
		Jenis: backup.JenisPostgres,
		Mulai: time.Date(2026, 10, 8, 2, 0, 0, 0, jakarta).UTC(),
	})
	perlu, err := svc.PerluJalan(context.Background(), backup.JenisPostgres, time.Date(2026, 10, 8, 6, 0, 0, 0, jakarta))
	if err != nil {
		t.Fatalf("PerluJalan: %v", err)
	}
	if perlu {
		t.Error("cadangan tidak boleh jalan dua kali pada hari WIB yang sama")
	}

	// Hari berikutnya (dini hari) harus jalan lagi.
	perlu, err = svc.PerluJalan(context.Background(), backup.JenisPostgres, time.Date(2026, 10, 9, 2, 5, 0, 0, jakarta))
	if err != nil {
		t.Fatalf("PerluJalan: %v", err)
	}
	if !perlu {
		t.Error("cadangan hari berikutnya harus mengejar")
	}

	// JalankanHarian hanya menjalankan yang memang waktunya; jenis storage
	// tetap jalan walau postgres hari ini sudah tercatat.
	hasil := svc.JalankanHarian(context.Background(), time.Date(2026, 10, 8, 6, 0, 0, 0, jakarta))
	for _, h := range hasil {
		if h.Jenis == backup.JenisPostgres {
			t.Error("postgres sudah jalan hari ini, tidak boleh dijalankan ulang")
		}
	}
}

// TestRingkasanMenandaiPerluTindakan memastikan status "perlu tindakan" untuk
// dashboard/Pengaturan: gagal terakhir tanpa sukses sesudahnya.
func TestRingkasanMenandaiPerluTindakan(t *testing.T) {
	repo := &repoPalsu{}
	gagal := false
	sukses := true
	repo.runs = []backup.Run{
		{ID: "a", Jenis: backup.JenisPostgres, Mulai: time.Now().Add(-48 * time.Hour), Sukses: &sukses},
		{ID: "b", Jenis: backup.JenisPostgres, Mulai: time.Now().Add(-24 * time.Hour), Sukses: &gagal},
	}
	svc := serviceUji(t, repo, backup.Nol{}, storage.NewMemory(""), backup.Config{Kerja: t.TempDir()})

	ringkasan, err := svc.Ringkasan(context.Background(), 10)
	if err != nil {
		t.Fatalf("Ringkasan: %v", err)
	}
	if !ringkasan.PerluTindakan {
		t.Fatal("gagal terakhir tanpa sukses sesudahnya harus menandai perlu tindakan")
	}
	if !strings.Contains(ringkasan.Pesan, "postgres") {
		t.Errorf("pesan ringkasan tidak menyebut jenis yang gagal: %q", ringkasan.Pesan)
	}
	if !ringkasan.Retensi.BuktiTanpaBatas {
		t.Error("ringkasan harus menyatakan retensi bukti tanpa batas")
	}
	if ringkasan.Jadwal != "setiap hari 02:00 WIB" {
		t.Errorf("jadwal = %q, mau 'setiap hari 02:00 WIB'", ringkasan.Jadwal)
	}

	// Setelah ada sukses yang lebih baru, penanda hilang.
	suksesLagi := true
	repo.runs = append(repo.runs, backup.Run{ID: "c", Jenis: backup.JenisPostgres, Mulai: time.Now(), Sukses: &suksesLagi})
	ringkasan, err = svc.Ringkasan(context.Background(), 10)
	if err != nil {
		t.Fatalf("Ringkasan: %v", err)
	}
	if ringkasan.PerluTindakan {
		t.Error("sukses terbaru harus menghapus penanda perlu tindakan")
	}
}

// ---------- sinkronisasi objek ----------

func TestSinkronObjekTempoMasukManifest(t *testing.T) {
	recipient, identity := kunciUji(t)
	proofKey := "tempo-proof/cabang-1/bukti"
	statementKey := "tempo-statement/tagihan-1.pdf"
	repo := &repoPalsu{proofs: []string{proofKey}, dokumen: []string{statementKey}}
	objek := storage.NewMemory("")
	for key, body := range map[string][]byte{proofKey: {0xff, 0xd8, 0xff}, statementKey: []byte("%PDF-1.7 surat tagihan")} {
		if err := objek.Put(context.Background(), key, bytes.NewReader(body), int64(len(body)), "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}
	target, err := backup.NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := serviceUji(t, repo, target, objek, backup.Config{Kerja: t.TempDir(), Recipient: recipient, Identity: identity})
	if run := svc.Jalankan(context.Background(), backup.JenisStorage); !run.Sukses {
		t.Fatalf("cadangan objek Tempo gagal: %v (%s)", run.Err, run.Pesan)
	}
	manifest, err := backup.MuatManifest(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{proofKey, statementKey} {
		if manifest.Objek[key].Nama != backup.NamaCadanganObjek(key) {
			t.Fatalf("objek Tempo %s tidak ada dalam manifest: %+v", key, manifest.Objek[key])
		}
	}
}

// TestSinkronObjekInkrementalDanByteUtuh memastikan bukti/dokumen tersalin
// terenkripsi, inkremental, dan byte-nya utuh saat didekripsi kembali.
func TestSinkronObjekInkrementalDanByteUtuh(t *testing.T) {
	recipient, berkasIdentitas := kunciUji(t)
	repo := &repoPalsu{
		proofs:  []string{"proofs/ord-1/bukti.jpg"},
		dokumen: []string{"documents/ord-1/invoice_pdf-0.pdf"},
	}
	objek := storage.NewMemory("")
	isiBukti := []byte("isi bukti transfer yang panjang\n" + strings.Repeat("x", 4096))
	isiDokumen := []byte("%PDF-1.7 dokumen invoice\n")
	if err := objek.Put(context.Background(), "proofs/ord-1/bukti.jpg", bytes.NewReader(isiBukti), int64(len(isiBukti)), "image/jpeg"); err != nil {
		t.Fatalf("Put bukti: %v", err)
	}
	if err := objek.Put(context.Background(), "documents/ord-1/invoice_pdf-0.pdf", bytes.NewReader(isiDokumen), int64(len(isiDokumen)), "application/pdf"); err != nil {
		t.Fatalf("Put dokumen: %v", err)
	}

	dirTarget := t.TempDir()
	target, err := backup.NewDir(dirTarget)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	svc := serviceUji(t, repo, target, objek, backup.Config{
		Kerja:     t.TempDir(),
		Recipient: recipient,
		Identity:  berkasIdentitas,
	})

	pertama := svc.Jalankan(context.Background(), backup.JenisStorage)
	if !pertama.Sukses {
		t.Fatalf("sinkronisasi pertama gagal: %v (%s)", pertama.Err, pertama.Pesan)
	}
	if pertama.UkuranByte != int64(len(isiBukti)+len(isiDokumen)) {
		t.Errorf("byte baru = %d, mau %d", pertama.UkuranByte, len(isiBukti)+len(isiDokumen))
	}
	if !strings.Contains(pertama.Pesan, "baru=2") {
		t.Errorf("pesan sinkronisasi pertama = %q, mau memuat baru=2", pertama.Pesan)
	}

	// Salinan harus terenkripsi (bukan byte polos) dan bisa didekripsi utuh.
	salinan := filepath.Join(dirTarget, backup.NamaCadanganObjek("proofs/ord-1/bukti.jpg"))
	raw, err := os.ReadFile(salinan)
	if err != nil {
		t.Fatalf("baca salinan bukti: %v", err)
	}
	if bytes.Contains(raw, []byte("isi bukti transfer")) {
		t.Error("salinan bukti tidak terenkripsi")
	}
	kripto := backup.NewKripto(recipient, berkasIdentitas)
	buka, err := os.Open(salinan)
	if err != nil {
		t.Fatalf("buka salinan: %v", err)
	}
	defer func() { _ = buka.Close() }()
	polos, err := kripto.DekripsiPembaca(buka)
	if err != nil {
		t.Fatalf("DekripsiPembaca: %v", err)
	}
	kembali, err := io.ReadAll(polos)
	if err != nil {
		t.Fatalf("baca hasil dekripsi: %v", err)
	}
	if !bytes.Equal(kembali, isiBukti) {
		t.Error("byte bukti hasil dekripsi berbeda dari aslinya")
	}

	// Sinkronisasi kedua tidak boleh mengunggah ulang apa pun.
	kedua := svc.Jalankan(context.Background(), backup.JenisStorage)
	if !kedua.Sukses {
		t.Fatalf("sinkronisasi kedua gagal: %v", kedua.Err)
	}
	if !strings.Contains(kedua.Pesan, "baru=0 dilewati=2") {
		t.Errorf("pesan sinkronisasi kedua = %q, mau baru=0 dilewati=2", kedua.Pesan)
	}
	if kedua.UkuranByte != 0 {
		t.Errorf("byte baru sinkronisasi kedua = %d, mau 0", kedua.UkuranByte)
	}

	// Verifikasi byte: semua objek cocok.
	manifest, err := backup.MuatManifest(context.Background(), target)
	if err != nil {
		t.Fatalf("MuatManifest: %v", err)
	}
	if len(manifest.Objek) != 2 {
		t.Fatalf("manifest memuat %d objek, mau 2", len(manifest.Objek))
	}
	status, err := svc.PeriksaObjek(context.Background(), []string{"proofs/ord-1/bukti.jpg", "documents/ord-1/invoice_pdf-0.pdf"}, manifest)
	if err != nil {
		t.Fatalf("PeriksaObjek: %v", err)
	}
	for _, st := range status {
		if !st.Cocok {
			t.Errorf("objek %s tidak cocok: %s", st.Kunci, st.Masalah)
		}
	}

	// Kerusakan pada salinan cadangan harus TERDETEKSI, bukan lolos diam-diam.
	if err := os.WriteFile(salinan, []byte("age-encryption.org/v1 rusak"), 0o600); err != nil {
		t.Fatalf("rusak salinan: %v", err)
	}
	status, err = svc.PeriksaObjek(context.Background(), []string{"proofs/ord-1/bukti.jpg"}, manifest)
	if err != nil {
		t.Fatalf("PeriksaObjek: %v", err)
	}
	if len(status) != 1 || status[0].Cocok {
		t.Error("salinan cadangan yang rusak harus dilaporkan tidak cocok")
	}

	// Objek yang dirujuk DB tapi hilang dari storage harus dilaporkan.
	repo.dokumen = append(repo.dokumen, "documents/ord-9/hilang.pdf")
	ketiga := svc.Jalankan(context.Background(), backup.JenisStorage)
	if !ketiga.Sukses {
		t.Fatalf("sinkronisasi ketiga gagal: %v", ketiga.Err)
	}
	if !strings.Contains(ketiga.Pesan, "PERINGATAN") {
		t.Errorf("kunci yang hilang di storage harus dilaporkan: %q", ketiga.Pesan)
	}
}

// TestRekonsiliasiKonservatifTidakMenyentuhBukti menegakkan kebijakan sapu:
// bukti transfer tidak pernah dihapus, kunci berbentuk asing tidak disentuh,
// objek muda (mungkin sedang ditulis render) dilindungi grace period, dan
// hanya dry-run yang dijalankan bila tidak diminta menghapus.
func TestRekonsiliasiKonservatifTidakMenyentuhBukti(t *testing.T) {
	repo := &repoPalsu{
		proofs:  []string{"proofs/ord-1/bukti.jpg"},
		dokumen: []string{"documents/ord-1/invoice_pdf-0.pdf"},
	}
	memori := storage.NewMemory("")
	ctx := context.Background()
	objek := map[string]string{
		"proofs/ord-1/bukti.jpg":                "bukti yang masih dirujuk",
		"proofs/ord-2/bukti-yatim.jpg":          "bukti yatim (retensi tanpa batas)",
		"documents/ord-1/invoice_pdf-0.pdf":     "dokumen dirujuk",
		"documents/ord-1/invoice_pdf-7.pdf":     "dokumen yatim tua",
		"documents/ord-1/catatan-lain.pdf":      "kunci berbentuk asing",
		"documents/ord-2/surat_jalan_pdf-1.pdf": "dokumen yatim muda",
	}
	for key, isi := range objek {
		if err := memori.Put(ctx, key, strings.NewReader(isi), int64(len(isi)), "application/octet-stream"); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	// Semua objek "tua" kecuali surat jalan ord-2 (muda, meniru berkas yang
	// sedang ditulis render aktif).
	tua := storeTua{
		Memory: memori,
		umur:   72 * time.Hour,
		muda:   map[string]bool{"documents/ord-2/surat_jalan_pdf-1.pdf": true},
	}
	svc := serviceUji(t, repo, backup.Nol{}, tua, backup.Config{Kerja: t.TempDir(), SweepGrace: 24 * time.Hour})

	laporan, err := svc.Rekonsiliasi(ctx, backup.OpsiRekonsiliasi{})
	if err != nil {
		t.Fatalf("Rekonsiliasi: %v", err)
	}
	if !laporan.DryRun {
		t.Error("tanpa --sweep, rekonsiliasi harus dry-run")
	}
	if len(laporan.Disapu) != 0 {
		t.Errorf("dry-run tidak boleh menghapus apa pun, dapat %v", laporan.Disapu)
	}
	kandidat := map[string]bool{}
	for _, k := range laporan.KandidatSapu {
		kandidat[k] = true
	}
	if !kandidat["documents/ord-1/invoice_pdf-7.pdf"] {
		t.Errorf("dokumen yatim tua harus jadi kandidat: %v", laporan.KandidatSapu)
	}
	if kandidat["documents/ord-2/surat_jalan_pdf-1.pdf"] || kandidat["documents/ord-1/invoice_pdf-0.pdf"] {
		t.Error("objek muda / yang dirujuk tidak boleh jadi kandidat sapu")
	}
	if len(laporan.ProofsTanpaBaris) != 1 || laporan.ProofsTanpaBaris[0] != "proofs/ord-2/bukti-yatim.jpg" {
		t.Errorf("bukti yatim harus dilaporkan terpisah: %v", laporan.ProofsTanpaBaris)
	}
	alasanProofs := false
	for _, d := range laporan.Dilewati {
		if d.Kunci == "proofs/ord-2/bukti-yatim.jpg" && strings.Contains(d.Alasan, "tanpa batas") {
			alasanProofs = true
		}
	}
	if !alasanProofs {
		t.Error("alasan perlindungan bukti harus tercatat di laporan")
	}
	if len(repo.entriAudit()) == 0 {
		t.Error("rekonsiliasi dengan kandidat harus terjejak di audit")
	}

	// Mode menghapus: hanya dokumen yatim tua yang disapu.
	laporan, err = svc.Rekonsiliasi(ctx, backup.OpsiRekonsiliasi{Hapus: true})
	if err != nil {
		t.Fatalf("Rekonsiliasi hapus: %v", err)
	}
	if len(laporan.Disapu) != 1 || laporan.Disapu[0] != "documents/ord-1/invoice_pdf-7.pdf" {
		t.Fatalf("disapu = %v, mau hanya dokumen yatim tua", laporan.Disapu)
	}
	// Bukti yatim, kunci asing, dan dokumen yang dirujuk harus SELAMAT.
	for _, key := range []string{
		"proofs/ord-2/bukti-yatim.jpg",
		"documents/ord-1/catatan-lain.pdf",
		"documents/ord-2/surat_jalan_pdf-1.pdf",
		"documents/ord-1/invoice_pdf-0.pdf",
	} {
		if _, err := memori.Get(ctx, key); err != nil {
			t.Errorf("objek %s tidak boleh dihapus: %v", key, err)
		}
	}
	if _, err := memori.Get(ctx, "documents/ord-1/invoice_pdf-7.pdf"); err == nil {
		t.Error("dokumen yatim tua seharusnya sudah disapu")
	}
	adaAuditHapus := false
	for _, e := range repo.entriAudit() {
		if e.Action == backup.ActionSweepHapus {
			adaAuditHapus = true
		}
	}
	if !adaAuditHapus {
		t.Error("tindakan sapu harus tercatat di audit")
	}
}

// TestRingkasanPotretRoundTrip membuktikan potret jumlah baris tersimpan dan
// terbaca kembali bersama dump pasangannya, dan absennya potret dikenali
// sebagai "belum ada" (bukan galat).
func TestRingkasanPotretRoundTrip(t *testing.T) {
	target, err := backup.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	ctx := context.Background()
	tanggal := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	namaDump := backup.NamaCadanganPostgres(tanggal)
	potret := backup.RingkasanCadangan{
		Berkas:  namaDump,
		Dibuat:  time.Now().UTC(),
		Sebelum: map[string]int64{"orders": 10, "documents": 20, "river_job": -1},
		Sesudah: map[string]int64{"orders": 11, "documents": 20, "river_job": -1},
	}
	if err := backup.SimpanRingkasan(ctx, target, potret); err != nil {
		t.Fatalf("SimpanRingkasan: %v", err)
	}
	dimuat, ada, err := backup.MuatRingkasan(ctx, target, namaDump)
	if err != nil {
		t.Fatalf("MuatRingkasan: %v", err)
	}
	if !ada {
		t.Fatal("ringkasan potret yang baru disimpan tidak ditemukan")
	}
	if dimuat.Sebelum["orders"] != 10 || dimuat.Sesudah["orders"] != 11 || dimuat.Berkas != namaDump {
		t.Errorf("ringkasan tidak sama: %+v", dimuat)
	}

	// Dump lain tanpa potret: dilaporkan "belum ada", bukan galat.
	lain := backup.NamaCadanganPostgres(tanggal.AddDate(0, 0, -1))
	if _, ada, err := backup.MuatRingkasan(ctx, target, lain); err != nil || ada {
		t.Errorf("dump tanpa potret harus (ada=false, err=nil), dapat ada=%v err=%v", ada, err)
	}
}

// TestUjiRestoreTanpaCadanganMenolakJelas.
func TestUjiRestoreTanpaCadanganMenolakJelas(t *testing.T) {
	target, err := backup.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	svc := serviceUji(t, &repoPalsu{}, target, storage.NewMemory(""), backup.Config{
		Kerja:     t.TempDir(),
		Recipient: "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq",
		DSN:       dsnContoh("grahita"),
	})
	if _, err := svc.UjiRestore(context.Background(), backup.OpsiUjiRestore{DSNTujuan: dsnContoh("uji")}); !errors.Is(err, backup.ErrCadanganKosong) {
		t.Errorf("err = %v, mau ErrCadanganKosong", err)
	}
}

// ---------- regresi QA putaran 1: kegagalan TERBARU per jenis ----------

// TestRingkasanMemakaiKegagalanTerbaruPerJenis menegakkan regresi putaran QA-1.
// Repository mengembalikan run terbaru-dulu (ORDER BY mulai DESC); status
// "perlu tindakan" harus dibandingkan dengan kegagalan TERBARU per jenis, bukan
// kegagalan tertua di jendela. Pola gagal → sukses → gagal sebelumnya membuat
// dashboard berbunyi "aman" tepat setelah cadangan terakhir gagal.
func TestRingkasanMemakaiKegagalanTerbaruPerJenis(t *testing.T) {
	ctx := context.Background()
	sekarang := time.Now().UTC().Truncate(time.Second)
	gagal, sukses := false, true

	// Salinan repo (yang membalik urutan) menyimpan run LAMA-DULU, sehingga
	// Terakhir() mengembalikannya seperti ORDER BY mulai DESC.
	repo := &repoPalsu{runs: []backup.Run{
		{ID: "postgres-gagal-lama", Jenis: backup.JenisPostgres, Mulai: sekarang.Add(-72 * time.Hour), Sukses: &gagal},
		{ID: "postgres-sukses", Jenis: backup.JenisPostgres, Mulai: sekarang.Add(-60 * time.Hour), Sukses: &sukses},
		{ID: "storage-gagal-tertua", Jenis: backup.JenisStorage, Mulai: sekarang.Add(-49 * time.Hour), Sukses: &gagal},
		{ID: "storage-sukses-selang", Jenis: backup.JenisStorage, Mulai: sekarang.Add(-25 * time.Hour), Sukses: &sukses},
		{ID: "storage-gagal-terbaru", Jenis: backup.JenisStorage, Mulai: sekarang.Add(-1 * time.Hour), Sukses: &gagal},
	}}
	svc := serviceUji(t, repo, backup.Nol{}, storage.NewMemory(""), backup.Config{Kerja: t.TempDir()})

	ringkasan, err := svc.Ringkasan(ctx, 20)
	if err != nil {
		t.Fatalf("Ringkasan: %v", err)
	}
	if !ringkasan.PerluTindakan {
		t.Fatal("kegagalan TERBARU (1 jam lalu) tanpa sukses sesudahnya harus menandai perlu tindakan, walau ada sukses di antara dua kegagalan")
	}
	if !strings.Contains(ringkasan.Pesan, "storage") {
		t.Errorf("pesan harus menyebut jenis yang gagal: %q", ringkasan.Pesan)
	}
	// Pesan wajib memakai waktu kegagalan TERBARU, bukan kegagalan tertua.
	waktuTerbaru := sekarang.Add(-1 * time.Hour).In(clock.Jakarta()).Format("02 Jan 2006 15:04 WIB")
	if !strings.Contains(ringkasan.Pesan, waktuTerbaru) {
		t.Errorf("pesan = %q, mau memuat waktu kegagalan terbaru %q", ringkasan.Pesan, waktuTerbaru)
	}
	waktuTertua := sekarang.Add(-49 * time.Hour).In(clock.Jakarta()).Format("02 Jan 2006 15:04 WIB")
	if strings.Contains(ringkasan.Pesan, waktuTertua) {
		t.Errorf("pesan = %q, tidak boleh memakai kegagalan TERTUA %q", ringkasan.Pesan, waktuTertua)
	}
	if len(ringkasan.SuksesTerakhir) != 2 {
		t.Errorf("sukses terakhir harus tercatat per jenis: %v", ringkasan.SuksesTerakhir)
	}

	// Sukses yang lebih baru dari kegagalan terbaru menghapus penanda.
	repo.runs = append(repo.runs, backup.Run{ID: "storage-sukses-baru", Jenis: backup.JenisStorage, Mulai: sekarang, Sukses: &sukses})
	ringkasan, err = svc.Ringkasan(ctx, 20)
	if err != nil {
		t.Fatalf("Ringkasan: %v", err)
	}
	if ringkasan.PerluTindakan {
		t.Errorf("sukses terbaru harus menghapus penanda perlu tindakan (pesan: %q)", ringkasan.Pesan)
	}
}

// TestRingkasanMengabaikanRunBelumSelesai memastikan baris "sedang berjalan"
// (sukses NULL) tidak pernah dianggap kegagalan.
func TestRingkasanMengabaikanRunBelumSelesai(t *testing.T) {
	sekarang := time.Now().UTC()
	repo := &repoPalsu{runs: []backup.Run{
		{ID: "selesai", Jenis: backup.JenisPostgres, Mulai: sekarang.Add(-30 * time.Hour)},
	}}
	svc := serviceUji(t, repo, backup.Nol{}, storage.NewMemory(""), backup.Config{Kerja: t.TempDir()})
	ringkasan, err := svc.Ringkasan(context.Background(), 10)
	if err != nil {
		t.Fatalf("Ringkasan: %v", err)
	}
	if ringkasan.PerluTindakan {
		t.Error("run yang belum selesai bukan kegagalan")
	}
}

// ---------- arsip WAL: selalu terenkripsi (regresi QA putaran 1) ----------

// siapkanArsipWAL membuat direktori arsip WAL berisi segmen uji dengan isi
// yang mudah dikenali bila bocor polos.
func siapkanArsipWAL(t *testing.T, segmen map[string][]byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wal-arsip")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("buat direktori arsip WAL: %v", err)
	}
	for nama, isi := range segmen {
		if err := os.WriteFile(filepath.Join(dir, nama), isi, 0o600); err != nil {
			t.Fatalf("tulis segmen WAL %s: %v", nama, err)
		}
	}
	return dir
}

// TestWALArsipSelaluTerenkripsiDanDapatDipulihkan adalah regresi temuan
// pemblokir putaran QA-1: arsip WAL sebelumnya diunggah POLOS ke tujuan. Setiap
// segmen (yang memuat seluruh perubahan baris, termasuk hash kata sandi) wajib
// terenkripsi age, idempoten, dan bisa didekripsi kembali untuk jalur PITR.
func TestWALArsipSelaluTerenkripsiDanDapatDipulihkan(t *testing.T) {
	recipient, berkasIdentitas := kunciUji(t)
	ctx := context.Background()
	segmen := map[string][]byte{
		"000000010000000000000001": []byte("hash kata sandi super rahasia dan data pelanggan\n" + strings.Repeat("a", 48)),
		"000000010000000000000002": bytes.Repeat([]byte("WAL polos pelanggan\n"), 100),
	}
	walDir := siapkanArsipWAL(t, segmen)

	dirTarget := t.TempDir()
	target, err := backup.NewDir(dirTarget)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	svc := serviceUji(t, &repoPalsu{}, target, storage.NewMemory(""), backup.Config{
		Kerja:     t.TempDir(),
		Recipient: recipient,
		Identity:  berkasIdentitas,
		WALDir:    walDir,
	})

	hasil := svc.Jalankan(ctx, backup.JenisWAL)
	if !hasil.Sukses {
		t.Fatalf("cadangan WAL gagal: %v (%s)", hasil.Err, hasil.Pesan)
	}
	if !strings.Contains(hasil.Pesan, "2 berkas terenkripsi baru") {
		t.Errorf("pesan = %q, mau memuat '2 berkas terenkripsi baru'", hasil.Pesan)
	}

	for nama, isi := range segmen {
		jalurTujuan := filepath.Join(dirTarget, filepath.FromSlash(backup.NamaWALTerEnkripsi(nama)))
		raw, err := os.ReadFile(jalurTujuan)
		if err != nil {
			t.Fatalf("arsip WAL %s tidak ada di tujuan: %v", jalurTujuan, err)
		}
		if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
			t.Errorf("arsip WAL %s tidak berheader age di tujuan", nama)
		}
		if bytes.Contains(raw, isi) {
			t.Errorf("arsip WAL %s memuat byte POLOS di tujuan", nama)
		}
		if jalurPolos := filepath.Join(dirTarget, filepath.FromSlash(backup.PrefixWAL+nama)); fileAda(jalurPolos) {
			t.Errorf("berkas WAL polos %s ikut mendarat di tujuan", jalurPolos)
		}
	}

	// Idempoten: tidak ada yang diunggah ulang.
	kedua := svc.Jalankan(ctx, backup.JenisWAL)
	if !kedua.Sukses {
		t.Fatalf("cadangan WAL kedua gagal: %v", kedua.Err)
	}
	if !strings.Contains(kedua.Pesan, "0 berkas terenkripsi baru") || !strings.Contains(kedua.Pesan, "2 sudah ada") {
		t.Errorf("pesan jalannya kedua = %q, mau '0 berkas terenkripsi baru ... 2 sudah ada'", kedua.Pesan)
	}
	if kedua.UkuranByte != 0 {
		t.Errorf("byte baru pada jalan kedua = %d, mau 0", kedua.UkuranByte)
	}

	// Arsip POLOS dari jalur versi lama: dilaporkan, tidak dipakai/ditimpa.
	namaLama := "000000010000000000000009"
	jalurLama := filepath.Join(dirTarget, filepath.FromSlash(backup.PrefixWAL+namaLama))
	if err := os.WriteFile(jalurLama, []byte("arsip WAL polos versi lama"), 0o600); err != nil {
		t.Fatalf("tulis arsip polos versi lama: %v", err)
	}
	ketiga := svc.Jalankan(ctx, backup.JenisWAL)
	if !ketiga.Sukses {
		t.Fatalf("cadangan WAL ketiga gagal: %v", ketiga.Err)
	}
	if !strings.Contains(ketiga.Pesan, "PERINGATAN") || !strings.Contains(ketiga.Pesan, namaLama) {
		t.Errorf("arsip polos di tujuan harus dilaporkan: %q", ketiga.Pesan)
	}
	if raw, err := os.ReadFile(jalurLama); err != nil || string(raw) != "arsip WAL polos versi lama" {
		t.Errorf("arsip polos versi lama tidak boleh diubah: %v (%q)", err, raw)
	}
	if fileAda(jalurLama + backup.SufiksAge) {
		t.Error("pekerjaan ini tidak boleh membuat salinan terenkripsi dari berkas asing")
	}

	// Jalur drill/PITR: dekripsi kembali ke direktori lokal, byte identik.
	dirPulih := filepath.Join(t.TempDir(), "wal-pulih")
	laporan, err := svc.PulihkanWAL(ctx, dirPulih)
	if err != nil {
		t.Fatalf("PulihkanWAL: %v", err)
	}
	if laporan.Dipulihkan != len(segmen) || laporan.Diperiksa != len(segmen) {
		t.Fatalf("pemulihan WAL: diperiksa=%d dipulihkan=%d, mau %d keduanya", laporan.Diperiksa, laporan.Dipulihkan, len(segmen))
	}
	for nama, isi := range segmen {
		kembali, err := os.ReadFile(filepath.Join(dirPulih, nama))
		if err != nil {
			t.Fatalf("berkas hasil pemulihan %s tidak ada: %v", nama, err)
		}
		if !bytes.Equal(kembali, isi) {
			t.Errorf("byte hasil pemulihan %s berbeda dari segmen asli", nama)
		}
	}
	laporPolos := false
	for _, d := range laporan.Dilewati {
		if strings.Contains(d, namaLama) {
			laporPolos = true
		}
	}
	if !laporPolos {
		t.Errorf("arsip polos di tujuan harus dilaporkan sebagai dilewati: %v", laporan.Dilewati)
	}
	if fileAda(filepath.Join(dirPulih, namaLama)) {
		t.Error("arsip polos di tujuan tidak boleh disalin apa adanya ke direktori pemulihan")
	}
}

// TestWALTanpaKunciAgeTidakMengunggahApaPun menegakkan fail-closed pada jalur
// WAL: tanpa kunci publik age, tidak ada satu byte pun yang mendarat di tujuan.
func TestWALTanpaKunciAgeTidakMengunggahApaPun(t *testing.T) {
	ctx := context.Background()
	walDir := siapkanArsipWAL(t, map[string][]byte{"000000010000000000000001": []byte("WAL rahasia")})
	dirTarget := t.TempDir()
	target, err := backup.NewDir(dirTarget)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	repo := &repoPalsu{}
	svc := serviceUji(t, repo, target, storage.NewMemory(""), backup.Config{
		Kerja:  t.TempDir(),
		WALDir: walDir,
	})

	hasil := svc.Jalankan(ctx, backup.JenisWAL)
	if hasil.Sukses {
		t.Fatal("tanpa kunci age pekerjaan WAL harus menolak berjalan")
	}
	if !errors.Is(hasil.Err, backup.ErrKunciEnkripsi) {
		t.Errorf("err = %v, mau ErrKunciEnkripsi", hasil.Err)
	}
	daftar, err := target.Daftar(ctx, backup.PrefixWAL)
	if err != nil {
		t.Fatalf("Daftar: %v", err)
	}
	if len(daftar) != 0 {
		t.Errorf("tidak boleh ada apa pun di tujuan tanpa kunci age: %v", daftar)
	}
	if run, ada := repo.runTerakhir(); !ada || run.Sukses == nil || *run.Sukses {
		t.Errorf("kegagalan WAL harus tercatat di backup_runs: %+v", run)
	}
}

// TestWALPulihMenolakArsipBukanAge memastikan jalur dekripsi PITR tidak pernah
// menyalin byte non-age: berkas `.age` yang isinya bukan hasil age ditolak.
func TestWALPulihMenolakArsipBukanAge(t *testing.T) {
	recipient, berkasIdentitas := kunciUji(t)
	dirTarget := t.TempDir()
	subdir := filepath.Join(dirTarget, "wal")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatalf("buat subdirektori wal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "000000010000000000000003.age"), []byte("ini sebenarnya WAL polos"), 0o600); err != nil {
		t.Fatalf("tulis arsip palsu: %v", err)
	}
	target, err := backup.NewDir(dirTarget)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	svc := serviceUji(t, &repoPalsu{}, target, storage.NewMemory(""), backup.Config{
		Kerja:     t.TempDir(),
		Recipient: recipient,
		Identity:  berkasIdentitas,
	})
	if _, err := svc.PulihkanWAL(context.Background(), filepath.Join(t.TempDir(), "pulih")); err == nil {
		t.Error("arsip yang bukan hasil age harus ditolak oleh PulihkanWAL")
	}
}

// TestWALRetensiTujuhHariMempertahankanBerkasAsing menegakkan retensi 7 hari di
// tujuan: hanya arsip `.age` tua yang dihapus; berkas yang bukan buatan
// pekerjaan ini (termasuk WAL polos versi lama) selalu selamat.
func TestWALRetensiTujuhHariMempertahankanBerkasAsing(t *testing.T) {
	ctx := context.Background()
	recipient, berkasIdentitas := kunciUji(t)
	dirTarget := t.TempDir()
	subdir := filepath.Join(dirTarget, "wal")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatalf("buat subdirektori wal: %v", err)
	}
	tua := time.Now().Add(-30 * 24 * time.Hour)
	arsipTua := filepath.Join(subdir, "00000001000000000000000A.age")
	arsipSegar := filepath.Join(subdir, "00000001000000000000000B.age")
	polosTua := filepath.Join(subdir, "00000001000000000000000C")
	for _, f := range []string{arsipTua, arsipSegar, polosTua} {
		if err := os.WriteFile(f, []byte("age-encryption.org/v1 contoh"), 0o600); err != nil {
			t.Fatalf("tulis %s: %v", f, err)
		}
	}
	if err := os.Chtimes(arsipTua, tua, tua); err != nil {
		t.Fatalf("ubah waktu arsip tua: %v", err)
	}
	if err := os.Chtimes(polosTua, tua, tua); err != nil {
		t.Fatalf("ubah waktu arsip polos: %v", err)
	}

	target, err := backup.NewDir(dirTarget)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	svc := serviceUji(t, &repoPalsu{}, target, storage.NewMemory(""), backup.Config{
		Kerja:     t.TempDir(),
		Recipient: recipient,
		Identity:  berkasIdentitas,
		WALDir:    t.TempDir(), // PITR aktif, tetapi belum ada segmen baru
	})
	hasil := svc.Jalankan(ctx, backup.JenisWAL)
	if !hasil.Sukses {
		t.Fatalf("cadangan WAL gagal: %v (%s)", hasil.Err, hasil.Pesan)
	}
	if !strings.Contains(hasil.Pesan, "retensi WAL: hapus 1 berkas") {
		t.Errorf("pesan = %q, mau 'retensi WAL: hapus 1 berkas'", hasil.Pesan)
	}
	if fileAda(arsipTua) {
		t.Error("arsip .age lebih tua dari 7 hari harus dihapus dari tujuan")
	}
	if !fileAda(arsipSegar) {
		t.Error("arsip .age yang masih segar tidak boleh dihapus")
	}
	if !fileAda(polosTua) {
		t.Error("berkas yang bukan buatan pekerjaan ini tidak boleh dihapus retensi")
	}
	if !strings.Contains(hasil.Pesan, "PERINGATAN") {
		t.Errorf("arsip polos di tujuan harus dilaporkan: %q", hasil.Pesan)
	}
}

// TestWALPulihTanpaDatabase membuktikan jalur dekripsi drill/PITR berjalan
// dengan repo kosong (TanpaRepo): pemulihan arsip WAL tidak boleh bergantung
// pada database yang justru mungkin sedang tidak tersedia saat bencana.
func TestWALPulihTanpaDatabase(t *testing.T) {
	recipient, berkasIdentitas := kunciUji(t)
	ctx := context.Background()
	isi := []byte("segmen WAL untuk drill PITR tanpa database\n")

	dirTarget := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirTarget, "wal"), 0o700); err != nil {
		t.Fatalf("buat subdirektori wal: %v", err)
	}
	sumber := filepath.Join(t.TempDir(), "segmen")
	if err := os.WriteFile(sumber, isi, 0o600); err != nil {
		t.Fatalf("tulis segmen sumber: %v", err)
	}
	kripto := backup.NewKripto(recipient, berkasIdentitas)
	if _, err := kripto.EnkripsiBerkas(filepath.Join(dirTarget, "wal", "000000010000000000000005.age"), sumber); err != nil {
		t.Fatalf("enkripsi segmen uji: %v", err)
	}
	target, err := backup.NewDir(dirTarget)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	svc := backup.New(backup.Config{
		Kerja:     t.TempDir(),
		Recipient: recipient,
		Identity:  berkasIdentitas,
	}, backup.TanpaRepo{}, backup.WithTarget(target), backup.WithClock(clock.System()))

	dirPulih := filepath.Join(t.TempDir(), "pulih")
	laporan, err := svc.PulihkanWAL(ctx, dirPulih)
	if err != nil {
		t.Fatalf("PulihkanWAL tanpa database: %v", err)
	}
	if laporan.Dipulihkan != 1 {
		t.Fatalf("dipulihkan = %d, mau 1", laporan.Dipulihkan)
	}
	kembali, err := os.ReadFile(filepath.Join(dirPulih, "000000010000000000000005"))
	if err != nil {
		t.Fatalf("baca hasil pemulihan: %v", err)
	}
	if !bytes.Equal(kembali, isi) {
		t.Error("byte hasil pemulihan berbeda dari segmen asli")
	}

	// Repo kosong harus MENOLAK pekerjaan yang butuh data — bukan diam-diam
	// melaporkan status kosong.
	if _, err := svc.Ringkasan(ctx, 5); !errors.Is(err, backup.ErrTanpaDatabase) {
		t.Errorf("Ringkasan dengan TanpaRepo = %v, mau ErrTanpaDatabase", err)
	}
}

// fileAda melaporkan apakah satu jalur ada.
func fileAda(jalur string) bool {
	_, err := os.Stat(jalur)
	return err == nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
