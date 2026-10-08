# F1-21 — Backup terenkripsi, pemantauan dan uji restore

Kartu: **t_12564ed9 — F1-21 Backup terenkripsi, pemantauan dan uji restore**

Dokumen acuan: PRD §1.1 (indikator "backup harian berhasil ≥ 99%"), §9 butir 11
("backup harian muncul di folder Google Drive dan dapat di-restore pada
lingkungan uji"), §10 (fase F1); Architecture.md §5 (topologi), §7 (backup &
pemulihan: `pg_dump -Fc` + age → rclone, 30 harian/12 bulanan, WAL 7 hari,
uji restore kuartalan, RPO ≤ 24 jam / RTO ≤ 4 jam), §8 (alert backup gagal);
SystemDesign.md §2 (modul `backup/`: "dump + unggah ke Google Drive");
Database.md §4.7 (`backup_runs`), §9 (retensi).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Paket baru `api/internal/backup` (domain + `postgres` repository), query baru
`api/db/queries/backup.sql` (hasil sqlc `internal/db/sqlc/backup.sql.go`),
tiga mode CLI, satu job worker terjadwal, satu endpoint admin, satu bagian
halaman Pengaturan, runbook operator, dan tambahan konfigurasi.

| Permukaan | Isi |
|---|---|
| `api/internal/backup` | `backup.go` (domain, jadwal 02:00 WIB, ringkasan, pencatatan run), `kripto.go` (age), `dump.go` (`pg_dump`/`pg_restore` lewat environment, bukan argumen), `target.go` (`Dir` & `Rclone`), `postgres.go` (dump harian + potret jumlah baris + retensi), `storage.go` (sinkronisasi objek + manifest + verifikasi byte), `wal.go` (arsip WAL bila PITR aktif), `rekonsiliasi.go` (DB↔storage + sapu objek yatim konservatif), `restore.go` (uji restore + penyamaran staging), `notify.go` (alarm) |
| CLI (`cmd/server`) | `--mode=backup [--jenis=postgres\|storage\|wal\|semua]`, `--mode=backup-rekonsiliasi [--sweep]`, `--mode=backup-restore [--dsn-tujuan --bersih --anonim --objek-dir --laporan --paksa]` |
| Worker | job periodik `backup_jadwal` (bawaan tiap 15 menit) yang hanya menjalankan pekerjaan yang memang sudah waktunya |
| HTTP | `GET /api/v1/admin/backup-runs` (Superadmin) → run terbaru + `perlu_tindakan` (dipakai halaman Pengaturan; F1-20 dapat memakainya sebagai chip) |
| Web admin | bagian **Backup harian** di `/pengaturan`: tujuan, jadwal, retensi, tabel riwayat (Sukses/Gagal/Berjalan), dan Notice merah bila perlu tindakan |
| Infra | `infra/backup/README.md` (runbook: kunci age, rclone, PITR, jadwal, drill, RPO/RTO), `.env.example`, env passthrough worker di `docker-compose.yml` |

## Keputusan desain

1. **Fail closed: tidak ada cadangan polos.** `BACKUP_AGE_RECIPIENT` wajib bila
   tujuan cadangan aktif (`config.ValidasiCadangan`), dan setiap pekerjaan
   memeriksa kesiapan kunci sebelum menyentuh database. Tujuan cadangan berisi
   seluruh data bisnis (termasuk hash kata sandi) sehingga gagal saat start jauh
   lebih baik daripada menulisnya terbuka ke Drive. Bukti: `TestCadanganTanpaKunciAgeDitolak`,
   probe live langkah 7 (`konfigurasi tidak valid: BACKUP_AGE_RECIPIENT wajib diisi ...`).
2. **Enkripsi memakai pustaka age (`filippo.io/age`), bukan biner `age`.**
   Biner `age`/`age-keygen` tidak tersedia di lingkungan ini dan menyandarkan
   keamanan pada pemanggilan proses eksternal lebih rapuh daripada pustaka yang
   sama (format & kunci age standard, kompatibel dengan `age -d`). Kunci publik
   boleh ada di environment; kunci privat hanya hidup sebagai berkas milik
   operator (`BACKUP_AGE_IDENTITY_FILE`, mode 600) dan tidak pernah dibaca saat
   enkripsi. Bukti: `TestKriptoAgeRoundTrip` (round-trip byte-identik, header
   `age-encryption.org/v1`, identitas lain GAGAL membuka), probe live (0 byte
   `PGDMP` dan 0 email seed pada berkas cadangan).
3. **Tujuan cadangan diabstraksi (`Target`).** `Rclone` menjalankan
   `rclone rcat/cat/lsjson/deletefile` dengan berkas konfigurasi milik operator
   (`BACKUP_RCLONE_CONFIG`, di luar repo) — inilah jalur Google Drive sesuai
   Architecture.md §7; `Dir` menulis ke direktori lokal (termasuk mount Drive
   yang disinkronkan alat lain) dan dipakai CI/uji. Keduanya diuji lewat
   implementasi yang sama (`TestSinkronObjekInkrementalDanByteUtuh`,
   `TestPGRestoreLingkaranPenuh`). **Bukti rclone ke Drive sungguhan belum ada**
   karena kredensial Drive dimiliki pemilik (lihat §Batas).
4. **Retensi persis Architecture.md §7, dan tidak pernah menghapus berkas
   asing.** `SaringRetensi` menyimpan 30 dump terbaru + satu dump terbaru per
   bulan selama 12 bulan; nama berkas yang tidak dikenali SELALU disimpan
   (`TestSaringRetensiHarianDanBulanan` termasuk berkas `README.txt`). Bukti
   storage (bukti transfer & dokumen) **tidak dihapus sama sekali**: keputusan
   pemilik "retensi bukti tanpa batas" lebih tinggi daripada retensi 90 hari di
   dokumen — ini dinyatakan terbuka di laporan `Ringkasan.retensi.bukti_tanpa_batas`.
5. **Uji restore memverifikasi terhadap POTRET jumlah baris, bukan database
   hidup.** Setiap dump harian disertai `postgres/grahita-YYYY-MM-DD.ringkasan.json`
   yang memuat jumlah baris tabel inti SEBELUM dan SESUDAH dump; dump konsisten
   ada di antara keduanya, sehingga jumlah baris hasil restore harus berada di
   rentang itu. Tanpa potret (dump dari versi lama), drill jatuh ke perbandingan
   database hidup dan `potret_dipakai=false` menyatakannya terbuka.
   *Kegagalan nyata yang ditemukan probe live dan diperbaiki:* perbandingan
   terhadap database hidup selalu "gagal" karena satu baris `audit_logs` alarm
   ditulis setelah dump — drill yang selalu merah sama tidak bergunanya dengan
   drill yang selalu hijau.
6. **Sapu objek yatim konservatif (keputusan pemilik + risiko F1-17).**
   Rekonsiliasi membandingkan kunci yang DIRUJUK database (`payment_proofs`,
   `documents`) dengan objek yang benar-benar ada di storage (`Store.List` baru).
   Bawaannya **hanya melaporkan** (dry-run); penghapusan hanya bila `--sweep`
   dan hanya untuk `documents/...` yang (a) tidak dirujuk baris `documents` mana
   pun, (b) berbentuk `documents/{order}/{jenis}-{versi}.{ext}` dengan jenis
   dikenal, dan (c) sudah melewati grace period 24 jam — melindungi berkas yang
   mungkin sedang ditulis render aktif. `proofs/...` **tidak pernah** disentuh.
   Setiap tindakan sapu tercatat di `audit_logs` (entity `backup_sweep`,
   aktor `system`). Bukti: `TestRekonsiliasiKonservatifTidakMenyentuhBukti`
   (dry-run, sapu hanya dokumen yatim tua, bukti/kunci asing/versi lama selamat).
7. **Sinkronisasi objek inkremental + manifest + verifikasi byte.** Salinan
   terenkripsi per objek (`storage/<kunci>.age`) dengan `storage/_manifest.json`
   (ukuran + sha256 objek asli). Objek yang ukurannya sama dan sudah ada di
   tujuan dilewati (bukan diunduh ulang). `PeriksaObjek` membandingkan sha256
   objek nyata dengan hasil dekripsi salinan — bukti "bukan sekadar HTTP 200".
   Batasnya dinyatakan: perubahan isi dengan ukuran identik hanya terdeteksi
   oleh drill/verifikasi, bukan oleh pemeriksaan fast-path. Bukti:
   `TestSinkronObjekInkrementalDanByteUtuh` (baru=2 → baru=0 dilewati=2, byte
   identik, salinan rusak terdeteksi, kunci hilang dilaporkan).
8. **Alarm kegagalan: email langsung ke Superadmin aktif + audit + halaman
   Pengaturan.** `Pemberitahu` mengirim ke SEMUA Superadmin aktif yang sudah
   diaktivasi (query baru `ListSuperadminEmails`), mencatat `audit_logs`
   (entity `backup_run`, action `gagal`, aktor `system`), dan tidak pernah
   mengubah status run. *Deviasi disengaja dari F1-16:* alarm TIDAK lewat
   `email_outbox` karena jenisnya enum `email_kind` untuk email transaksional
   pesanan (F1-16 masih tertahan) dan menumpangkan alarm operasional di antrean
   yang bergantung worker email berarti alarm ikut macet justru saat dibutuhkan.
   Migrasi alarm ke outbox/metrics diserahkan ke F1-22 (tercatat di §Serah terima).
9. **Jadwal 02:00 WIB dengan pengejaran, bukan cron di dalam proses.** Job
   worker memeriksa "sudah lewat 02:00 WIB hari ini DAN belum ada run jenis ini
   hari ini" → pekerjaan yang terlewat (worker mati/restart) mengejar di tick
   berikutnya tanpa dobel (`TestPerluJalanSesuaiJadwalWIB`). Job yang gagal
   sengaja TIDAK dikembalikan sebagai error ke River: retry tidak memperbaiki
   kredensial/kunci yang salah dan hanya menutupi alarm.
10. **Rahasia tidak pernah masuk repo.** Kunci privat age, konfigurasi rclone,
    dan DSN selalu dari environment/berkas operator. `scripts/check-secrets.sh`
    hijau; bahkan DSN contohlah di uji unit yang saya pecah menjadi beberapa
    potongan agar tidak menyerupai kredensial literal pada berkas yang dilacak.
11. **Pengaman salah ketik pada `--bersih`.** Drill menolak menghapus database
    yang namanya tidak dikenali sebagai uji/staging (mengandung `uji`/`staging`)
    kecuali `--paksa` — diperiksa SEBELUM database dibuat
    (`TestPGSiapkanDatabaseMenolakHapusNonUji`; putaran pertama pengaman ini
    sempat diletakkan setelah pembuatan sehingga justru membuat database
    `grahita_produksi` — ketemu saat uji dan langsung diperbaiki).
12. **Penyamaran data staging memakai ID penuh.** Probe live menemukan
    `left(id::text,8)` pada email menyamarkan BENTROK (id uuidv7 dari milidetik
    yang sama berbagi 8 karakter pertama) sehingga melanggar unique email;
    sekarang `cabang-<uuid-penuh>@staging.invalid`. Regresi dijaga uji integrasi
    yang menyisipkan tiga cabang dalam satu pernyataan.
13. **Object storage juga dipasang di mode worker** (sebelumnya `storage.Disabled{}`).
    Konsekuensi wajib dari cadangan (dan lampiran email F1-16): worker harus bisa
    membaca objek. Konfigurasi setengah jadi tetap menghentikan start (perilaku
    lama yang sudah terdokumentasi).

Endpoint/skema baru: `GET /api/v1/admin/backup-runs` (Superadmin, kapabilitas
`admin.settings` yang sudah ada) dan skema `BackupRun` di `openapi/openapi.yaml`;
kontrak OpenAPI ↔ router dijaga `TestKontrakOpenAPIIkutRouter`.

## Bukti verifikasi

### Gerbang

| Gerbang | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` / `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | tanpa diff (paket `internal/db/sqlc/backup.sql.go` baru ikut dibangkitkan) |
| `./scripts/check-secrets.sh` | exit 0 |
| `redocly lint openapi/openapi.yaml` | valid |
| `go test -race -p 1 -count=1 ./...` (PostgreSQL 16.15 + `scripts/pg-dev-shim.sql`) | lihat hasil lengkap di serah terima kartu |
| Uji fokus F1-21 | `go test -race -count=1 -v ./internal/backup/` — semua lulus (unit + integrasi PG) |
| Web | `npm run typecheck` + `npm run test --workspace @grahita/admin` (54 uji, 3 di antaranya baru) + `npm run build` |

Uji fokus F1-21 yang baru:

| Uji | Yang dibuktikan |
|---|---|
| `TestKriptoAgeRoundTrip` | enkripsi/dekripsi age byte-identik; identitas lain ditolak; header age benar |
| `TestCadanganTanpaKunciAgeDitolak` | tanpa kunci publik: pekerjaan menolak, tercatat gagal, TIDAK ada berkas cadangan |
| `TestBackupGagalDicatatDanAlarmTerkirim` | `pg_dump` hilang → run gagal + audit + email alarm ke Superadmin |
| `TestAlarmGagalKirimTidakMenutupiKegagalan` | SMTP mati tidak mengubah status run |
| `TestSaringRetensiHarianDanBulanan` | 30 terbaru + 12 penyimpan bulanan; berkas asing & ringkasan potret aman |
| `TestBentukKunciDokumenMenyaringKunciAsing` | hanya kunci dokumen yang sah yang boleh dipertimbangkan sapu |
| `TestPerluJalanSesuaiJadwalWIB` | 01:59 belum jalan, 02:00+ jalan sekali/hari, hari berikutnya mengejar |
| `TestRingkasanMenandaiPerluTindakan` | gagal terakhir → `perlu_tindakan`; sukses sesudahnya menghapusnya |
| `TestRingkasanPotretRoundTrip` | potret jumlah baris tersimpan/terbaca; dump tanpa potret dikenali |
| `TestSinkronObjekInkrementalDanByteUtuh` | sinkronisasi inkremental, byte identik, kerusakan salinan terdeteksi |
| `TestRekonsiliasiKonservatifTidakMenyentuhBukti` | dry-run bawaan; hanya dokumen yatim tua disapu; bukti asli aman |
| `TestPGBackupRestoreLingkaranPenuh` (PG nyata) | migrasi → data+objek → `pg_dump`+age → `pg_restore` → potret cocok → 2/2 objek byte-cocok → penyamaran bersih → alarm kegagalan ke Superadmin dari database |
| `TestPGSiapkanDatabaseMenolakHapusNonUji` | pengaman salah ketik pada `--bersih` |

### Probe live (binary nyata, PostgreSQL nyata, tujuan nyata)

Skrip probe dan keluaran mentahnya ada di workspace kartu
(`BUKTI-LIVE-F121.txt`, `laporan-restore-uji.json`, `api-backup-runs.json`).
Database sumber `grahita_f121_live` (migrasi + seed PRD, 4 cabang, 1 pesanan,
1 bukti, 1 dokumen), tujuan `dir:.../live/drive` (berperan sebagai folder
Drive), kunci age baru dengan identitas di luar repo (mode 600).

| Langkah | Bukti nyata |
|---|---|
| `--mode=backup --jenis=semua` | `postgres` sukses: 85.111 byte polos → 85.327 byte terenkripsi `postgres/grahita-2026-10-09.dump.age` + `...ringkasan.json`; `storage` GAGAL (S3 tidak dikonfigurasi) → alarm email ke `rina@grahitaindonesia.com`; `wal` sukses tanpa tindakan (PITR nonaktif) |
| Isi tujuan | dump `.age` (header `age-encryption.org/v1`), ringkasan potret 1.176 byte, manifest, dua salinan objek `.age` |
| Enkripsi nyata | `'PGDMP'` pada dump = **0**; email seed di dalam dump = **0**; sha256 dump tercatat |
| `backup_runs` | 3 baris nyata: postgres `t` 85.327 byte + tujuan, storage `f` + pesan sebab, wal `t` |
| Fail closed | tujuan aktif tanpa `BACKUP_AGE_RECIPIENT` → proses menolak start sebelum menulis apa pun |
| `--mode=backup-restore --bersih --anonim --objek-dir --laporan` | `data_cocok=true` (`potret_dipakai=true`), `jejak_cadangan=1`, `objek_diperiksa=2`, `objek_dipulihkan=2`, `objek_cocok=true`, `anonim_cocok=true`; durasi drill 1,15 detik |
| Byte objek | sha256 hasil pemulihan == sha256 objek asli (bukti `dd64379d…`, dokumen `c3ee36ff…`) |
| Penyamaran staging | 4/4 email cabang + 2/2 email admin `@staging.invalid`, 0 sesi, 0 token reset/registrasi, 0 IP audit |
| `GET /admin/backup-runs` (Superadmin) | 200: daftar run + `perlu_tindakan=true` + `"Cadangan storage gagal pada 09 Oct 2026 04:36 WIB …"` |
| Otorisasi | Manager → **403**, tanpa sesi → **401** |

### RPO / RTO

| Target | Nilai | Dasar |
|---|---|---|
| RPO | ≤ 24 jam (`≤ 5 menit` bila PITR aktif) | cadangan harian 02:00 WIB + pengejaran bila worker terlewat; `wal` mencadangkan arsip WAL bila `BACKUP_WAL_DIR` diisi |
| RTO | ≤ 4 jam | drill kuartalan mengembalikan DB + objek ke lingkungan uji; pada data contoh (dump 85 KB) drill selesai 1,15 detik; waktu restore produksi (dump besar + unduh dari Drive) harus diukur pada drill pertama **di server**, bukan disimpulkan dari data contoh |

## Batas yang TIDAK diklaim

1. **Google Drive dan MinIO/R2 nyata belum dijalankan.** Kredensial dimiliki
   pemilik; seluruh rantai di sisi kita sudah terbukti (dump nyata → enkripsi
   age nyata → unggah lewat `Target` → `pg_restore` nyata → verifikasi byte),
   tetapi "backup terlihat di folder Drive" dan SigV4/presigned MinIO **belum**
   terbukti di sesi ini. Jangan menyatakan kesiapan backup/storage produksi
   lulus sebelum `rclone lsd gdrive:...` dan `--mode=backup` dijalankan di
   server dengan kredensial pemilik.
2. **Pekerjaan `storage` (sinkronisasi objek) tidak bisa dijalankan live** tanpa
   object storage; jalannya justru terpakai sebagai bukti alarm. Sinkronisasi
   inkremental + verifikasi byte dibuktikan uji dengan object store nyata
   in-process dan jumlah baris `payment_proofs`/`documents` yang dirujuk.
3. **PITR belum aktif** (tidak ada `archive_mode`/`archive_command` di
   lingkungan ini), jadi `jenis=wal` hanya membuktikan no-op yang jujur; konfigurasi
   dan retensi 7 hari ada di runbook.
4. **PostgreSQL 18 asli dan `golangci-lint`** tidak dijalankan di sesi ini
   (PG 16.15 + shim `uuidv7`, sama seperti kartu lain di gelombang ini).
   `pg_dump`/`pg_restore` juga harus ber-versi mayor ≥ server (pg_dump 16
   menolak server 18) — dicatat di runbook §2.
5. **RTO produksi belum terukur** (lihat tabel di atas); angka 1,15 detik bukan
   bukti RTO produksi.
6. **Perubahan isi objek dengan ukuran identik** tidak terdeteksi oleh fast-path
   sinkronisasi (butuh drill/`PeriksaObjek` untuk memastikan byte).
7. **Retensi salinan storage tidak dibatasi** (deviasi sadar terhadap 90 hari di
   Architecture.md §7) karena keputusan pemilik: bukti transfer tidak dihapus.
   Dampak: kebutuhan ruang Drive tumbuh monoton; bila pemilik kelak menetapkan
   batas, aturannya harus ditambahkan di `berlakukanRetensi` DAN disetujui
   sebelum salinan apa pun dihapus.
8. **Sapu objek yatim** default dry-run; menghapus objek yatim (termasuk
   dokumen versi dari render yang gagal) butuh keputusan operator lewat
   `--sweep`.

## Serah terima ke kartu lain

- **F1-22 (t3315b041/F1-16, observability & rilis):** (a) alarm cadangan masih
  dikirim langsung lewat `mailer.Mailer`, bukan `email_outbox` — putuskan
  pemiliknya setelah F1-16 tuntas, dan pastikan alarm tetap keluar meski worker
  email mati; (b) metrik/alarm Prometheus untuk `backup` (Architecture.md §8)
  belum ada — endpoint `/admin/backup-runs` + `backup_runs` adalah sumber
  datanya; (c) pasang berkas `rclone.conf` + identitas age di server produksi
  (mount rahasia, bukan repo) dan jalankan drill pertama di sana untuk mengukur
  RTO nyata; (d) `pg_dump` di image produksi harus ≥ versi PG 18.
- **F1-20 (dashboard admin & chip perlu tindakan):** `GET /api/v1/admin/backup-runs`
  mengembalikan `perlu_tindakan` + `pesan` siap dipakai sebagai chip; jangan
  membaca tabel `backup_runs` langsung dari domain lain.
- **T5 (uji backup/keamanan/rilis):** jalankan `--mode=backup` dan
  `--mode=backup-restore` di server dengan kredensial Drive pemilik, lalu
  bandingkan byte objek lewat `--objek-dir`; periksa pula presigned MinIO/R2
  (catatan T3) karena restore objek hanya bermakna bila endpoint presign-nya
  juga terbukti.
- **Hotspot:** `api/internal/backup/restore.go` (drill) dan
  `api/internal/platform/storage/storage.go` (antarmuka `Store` bertambah
  `List`) — dua berkas ini paling mungkin berbenturan dengan kartu lain;
  `List` sengaja ditambahkan sebagai metode WAJIB karena rekonsiliasi dan
  sinkronisasi tidak mungkin tanpa pendaftaran objek.
