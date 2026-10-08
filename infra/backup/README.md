# Operasi cadangan terenkripsi (F1-21)

Dokumen ini adalah **runbook operator** untuk cadangan harian terenkripsi dan
uji pulih (Architecture.md §7, PRD §9 butir 11). Semua perintah di sini
dijalankan di server, bukan di mesin pengembang. **Tidak ada rahasia di repo**:
kunci age, konfigurasi rclone, dan kata sandi selalu datang dari environment
atau berkas milik operator (Architecture.md §6).

## 1. Apa yang dicadangkan

| Jenis (`backup_runs.jenis`) | Isi | Frekuensi | Retensi |
|---|---|---|---|
| `postgres` | `pg_dump -Fc` **terenkripsi age** | Harian 02:00 WIB | 30 terbaru + 1 terbaru/bulan selama 12 bulan |
| `storage` | bukti transfer + dokumen dari object storage, **terenkripsi age**, sinkronisasi inkremental + manifest | Harian | **Tanpa batas** (bukti transfer tidak pernah dihapus) |
| `wal` | arsip WAL untuk PITR (bila aktif), tiap segmen **terenkripsi age** (`wal/<segmen>.age`) | Harian (mengikuti arsip) | 7 hari di tujuan |

Setiap pekerjaan menulis satu baris `backup_runs` (jenis, mulai, selesai,
sukses, ukuran_byte, tujuan, pesan). Kegagalan mengirim **email ke semua
Superadmin aktif** dan muncul sebagai peringatan di halaman Pengaturan admin
(`GET /api/v1/admin/backup-runs`, khusus Superadmin).

## 2. Prasyarat

- `pg_dump`/`pg_restore` dengan **versi mayor ≥ server** (server kita
  PostgreSQL 18; pg_dump 16 menolak server 18).
- `rclone` (bila `BACKUP_TARGET=rclone`).
- Kunci age (satu pasang untuk seluruh organisasi; simpan kunci privat di
  server cadangan juga, kalau tidak cadangan tidak bisa dipulihkan).
- Direktori kerja tulis untuk `BACKUP_WORK_DIR` (cadangan polos hanya hidup di
  sini, sementara, dan dihapus setelah diunggah).

## 3. Menyiapkan kunci age

```sh
# 1) Buat pasangan kunci (sekali saja). Kunci privat TIDAK PERNAH masuk repo.
age-keygen -o /etc/grahita/backup-identitas.txt      # chmod 600
grep 'public key' /etc/grahita/backup-identitas.txt  # age1...

# 2) Simpan SALINAN kunci privat di tempat aman di luar server (brankas/
#    password manager). Tanpa kunci privat, cadangan tidak bisa dibuka.
```

Environment untuk service:

```sh
BACKUP_AGE_RECIPIENT=age1...            # kunci publik (boleh ada di env)
BACKUP_AGE_IDENTITY_FILE=/etc/grahita/backup-identitas.txt   # hanya untuk restore
```

Tanpa `BACKUP_AGE_RECIPIENT`, seluruh pekerjaan cadangan **menolak berjalan**
(fail closed): tidak ada jalur yang menulis dump polos. Pemeriksaan ini terjadi
saat konfigurasi dimuat, jadi tujuan cadangan yang terisi SETENGAH (mis. baru
diisi `BACKUP_TARGET_DIR` tetapi kuncinya belum) akan menghentikan **semua**
mode binary — termasuk `--mode=migrate`/`--mode=api`. Itu disengaja: lebih baik
operator sadar lebih awal daripada menjalankan sistem tanpa cadangan.

## 4. Menyiapkan tujuan (Google Drive via rclone)

```sh
# Konfigurasi rclone dimiliki operator (mis. /etc/rclone/rclone.conf, chmod 600).
rclone --config /etc/rclone/rclone.conf config        # buat remote "gdrive"
rclone --config /etc/rclone/rclone.conf mkdir gdrive:grahita-backup
rclone --config /etc/rclone/rclone.conf lsd gdrive:grahita-backup   # uji akses
```

Environment:

```sh
BACKUP_TARGET=rclone
BACKUP_RCLONE_REMOTE=gdrive:grahita-backup
BACKUP_RCLONE_CONFIG=/etc/rclone/rclone.conf
```

Alternatif tanpa rclone: mount Drive (atau direktori yang disinkronkan alat
lain) lalu pakai `BACKUP_TARGET=dir` + `BACKUP_TARGET_DIR=/var/backups/grahita`.
Struktur di tujuan selalu sama:

```
postgres/grahita-YYYY-MM-DD.dump.age
storage/proofs/<order_id>/<nama>.<ext>.age
storage/documents/<order_id>/<jenis>-<versi>.<ext>.age
storage/_manifest.json
wal/<berkas WAL>.age
```

Semua berkas di tujuan berakhiran `.age`: **tidak ada satu pun jalur yang
menulis byte polos** (dump, salinan bukti/dokumen, maupun segmen WAL) —
Architecture.md §6.

## 5. Menjadwalkan

Cadangan berjalan dari **worker** (`--mode=worker`): job periodik
`backup_jadwal` (bawaan tiap `BACKUP_INTERVAL`, 15 menit) memeriksa penjaga
jadwal — sekali per hari per jenis, **dimulai setelah 02:00 WIB**, dan mengejar
bila worker sempat mati.

Bila worker tidak dijalankan terus-menerus, jadwalkan mode sekali-jalan:

```sh
# crontab (server memakai zona WIB)
0 2 * * *  cd /srv/grahita && /usr/local/bin/api --mode=backup >> /var/log/grahita-backup.log 2>&1
```

`--mode=backup --jenis=postgres|storage|wal|semua` juga dipakai operator untuk
menjalankan ulang cadangan yang gagal di luar jadwal.

## 6. PITR / arsip WAL (opsional, menurunkan RPO ke ≤ 5 menit)

PITR diaktifkan di PostgreSQL, bukan di aplikasi:

```
# postgresql.conf (server SUMBER)
archive_mode   = on
archive_command = 'test ! -f /var/lib/postgresql/wal-arsip/%f && cp %p /var/lib/postgresql/wal-arsip/%f'
```

Lalu isi `BACKUP_WAL_DIR=/var/lib/postgresql/wal-arsip` agar arsipnya ikut
dibawa ke tujuan (retensi 7 hari di tujuan, Architecture.md §7).

**Di tujuan, arsip itu terenkripsi age** (`wal/<berkas WAL>.age`), sama seperti
dump: WAL memuat seluruh perubahan baris, termasuk hash kata sandi, sehingga
tidak ada satu byte pun yang boleh mendarat polos di Drive. Pada server
PEMULIHAN, dekripsi dulu arsipnya lalu arahkan `restore_command` ke direktori
hasil dekripsi:

```sh
# 1) Unduh + dekripsi arsip WAL dari tujuan ke direktori lokal.
#    Butuh BACKUP_AGE_IDENTITY_FILE (kunci privat milik operator).
#    Langkah ini TIDAK menyentuh database: drill/PITR sering dijalankan justru
#    ketika database sumber tidak tersedia.
/usr/local/bin/api --mode=backup-pulih-wal \
  --dir-wal /var/lib/postgresql/wal-arsip-pulih \
  --laporan /srv/grahita/restore-uji/wal-$(date +%F).json

# 2) postgresql.conf (server PEMULIHAN)
#    restore_command = 'cp /var/lib/postgresql/wal-arsip-pulih/%f %p'
```

Perintah dekripsi itu **menolak** berkas yang bukan hasil age, jadi WAL polos
tidak pernah bisa lolos ke jalur pemulihan. Bila di tujuan masih ada arsip polos
dari versi lama (tanpa `.age`), pekerjaan `wal` melaporkannya sebagai
`PERINGATAN` di `backup_runs.pesan` dan tidak pernah memakainya; operator
memutuskan sendiri nasibnya (konfigurasi lama: hapus setelah dipastikan tidak
diperlukan).

## 7. Uji pulih kuartalan (WAJIB)

> Backup yang tidak pernah diuji tidak dihitung sebagai backup
> (Architecture.md §7).

```sh
# Database tujuan WAJIB bernama jelas lingkungan uji (mengandung "uji"/"staging");
# tanpa itu perintah menolak menghapusnya (pengaman salah ketik).
# Kata sandi diambil dari environment (skrip check-secrets menolak DSN literal).
export BACKUP_RESTORE_DSN="postgres://grahita:${BACKUP_RESTORE_PASS}@127.0.0.1:5432/grahita_staging_uji"
mkdir -p /srv/grahita/restore-uji/objek

/usr/local/bin/api --mode=backup-restore \
  --bersih --anonim \
  --objek-dir /srv/grahita/restore-uji/objek \
  --laporan /srv/grahita/restore-uji/laporan-$(date +%F).json
```

Yang diperiksa perintah ini (keluar dengan status ≠ 0 bila ada yang gagal):

1. berkas cadangan terbaru diambil dan **didekripsi** dengan kunci age;
2. `pg_restore` ke database uji (dibuat ulang oleh perintah ini);
3. jumlah baris **setiap tabel inti** masuk ke dalam **potret jumlah baris**
   yang diambil saat dump dibuat (`postgres/grahita-YYYY-MM-DD.ringkasan.json`,
   penghitungan sebelum dan sesudah dump) — bukan dibandingkan dengan database
   hidup yang terus berubah;
4. hasil restore memuat jejak `backup_runs` (bukti itu memang cadangan sistem);
5. **setiap baris** `payment_proofs` dan `documents` dipulihkan byte-nya dan
   cocok dengan manifest (sha256);
6. penyamaran data staging bersih: email `@staging.invalid`, IP audit NULL,
   sesi dan token (reset/registrasi) terhapus.

Simpan `laporan-*.json` sebagai bukti. Untuk mengukur RTO, catat durasi
`selesai - mulai` dari laporan.

## 8. Rekonsiliasi DB ↔ object storage

```sh
# Bawaan: HANYA MELAPORKAN (dry-run).
/usr/local/bin/api --mode=backup-rekonsiliasi

# Menghapus objek yatim documents/ yang tidak dirujuk baris documents,
# lebih tua dari grace period 24 jam, berbentuk kunci dokumen yang dikenali.
/usr/local/bin/api --mode=backup-rekonsiliasi --sweep
```

Yang **tidak pernah** disentuh: seluruh `proofs/...` (retensi bukti tanpa
batas), kunci yang tidak mengikuti pola `documents/{order}/{jenis}-{versi}.{ext}`
dengan jenis dikenal, dan objek yang belum melewati `BACKUP_SWEEP_GRACE`
(melindungi berkas yang sedang ditulis render aktif). Setiap tindakan sapu
tercatat di `audit_logs` (entity `backup_sweep`).

Penghapusan **hanya** bisa dipersenjatai lewat flag CLI eksplisit `--sweep` di
atas; tidak ada variabel lingkungan yang mengaktifkannya diam-diam (knop
`BACKUP_SWEEP_HAPUS` yang dulu ada dihapus karena tidak pernah dibaca domain
cadangan — operator mengira sapu otomatis aktif padahal tidak).

## 9. RPO / RTO

| Target | Nilai | Cara memenuhi / membuktikan |
|---|---|---|
| RPO | ≤ 24 jam (≤ 5 menit bila PITR aktif) | cadangan harian 02:00 WIB + pengejaran bila worker terlambat; arsip WAL menutup celah antar-dump |
| RTO | ≤ 4 jam | uji pulih kuartalan mengembalikan DB + objek ke lingkungan uji; durasi drill dicatat di laporan |

Rincian angka dan bukti uji ada di `docs/design/f1-21/HASIL.md`.

## 10. Yang belum bisa dibuktikan tanpa kredensial pemilik

Integrasi Google Drive dan MinIO/R2 nyata memerlukan kredensial pemilik.
Sampai itu tersedia, yang sudah terbukti adalah seluruh rantai di sisi kita:
`pg_dump` nyata → enkripsi age nyata → unggah ke tujuan (rclone) →
`pg_restore` nyata → verifikasi byte objek → penyamaran staging. Jangan
menyatakan "Drive/MinIO produksi lulus" sebelum kredensial nyata dipasang dan
`rclone lsd`/`--mode=backup` dijalankan di server.
