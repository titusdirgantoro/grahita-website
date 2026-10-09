# F2-23 — Reminder pembayaran C&C terjadwal

Kartu: **t_fe92acd7 — F2-23 Reminder pembayaran C&C terjadwal**

Dokumen acuan: `docs/PRD.md` §3.7 (CP-CNC-02..07), §6.2 (template "Reminder Cash
& Carry" + EM-01..04), §7 (BR-08/11, state machine §7.1), §10 (Fase 2);
`docs/SystemDesign.md` §4.6 (job terjadwal 07:00 WIB, `cnc_reminders`), §5.1
(transactional outbox), §5.5 (Clock); `docs/Database.md` §4.7 (`email_outbox`,
`cnc_reminders`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Tidak ada permukaan HTTP baru: pengingat murni pekerjaan latar (River). Yang
ditambahkan:

| Berkas | Isi |
|---|---|
| `api/internal/notification/reminder.go` (baru) | domain: konstanta tahap, `TahapReminder`, `KandidatCNC`, `ReminderStore`, `HasilReminder`, `WithReminderStore`, `Service.JalankanReminderCNC` |
| `api/internal/notification/postgres/reminder.go` (baru) | `Repo.KandidatCNC` + `Repo.AntreReminderCNC` (satu transaksi: `cnc_reminders` + `email_outbox` + audit) |
| `api/internal/notification/notification.go` | bidang `Service.reminder` |
| `api/internal/platform/jobs/jobs.go` | kind job `reminder_cnc` + `ReminderCNCArgs` |
| `api/internal/worker/worker.go` | `ReminderCNCWorker`, `JadwalHarianWIB`, registrasi + job periodik 07:00 WIB |
| `api/internal/platform/config/config.go` | `ReminderJamWIB` (env `REMINDER_JAM_WIB`, bawaan 7) |
| `api/internal/audit/audit.go` | `EntityCNCReminder` + `ActionRemind` |
| `api/cmd/server/main.go` | `notification.WithReminderStore(repo)` |
| `.env.example` | dokumentasi `REMINDER_JAM_WIB` |
| `api/internal/notification/reminder_test.go` (baru) | 9 uji unit domain |
| `api/internal/notification/integration_reminder_pg_test.go` (baru) | 4 uji integrasi PostgreSQL nyata |
| `api/internal/worker/jadwal_test.go` (baru) | 4 sub-uji jadwal harian WIB |
| `api/internal/worker/reminder_integration_pg_test.go` (baru) | 1 uji ujung-ke-ujung lewat River nyata |

Domain tetap di paket `notification` (bukan paket baru): `SystemDesign.md` §2
menyebut paket itu memiliki "outbox email, template, **reminder**", dan di sana
sudah tersedia template `reminder_cnc` (`tplReminder`), `DefaultSubject`,
jenis email `reminder_cnc`, serta sumber waktu (`clock.Clock`) yang bisa
disuntik. `ReminderStore` sengaja dipisah dari `Store` supaya kontrak outbox
yang sudah ada (dan seluruh tiruannya di uji) tidak ikut berubah.

## Keputusan desain

1. **Tanggal bisnis, bukan jam.** `TahapReminder(hari, jatuhTempo)` menormalkan
   kedua waktu ke tengah malam **WIB** (`clock.Jakarta()`) lalu menghitung
   selisih hari bulat: `-2 → h-2`, `0 → h-0`, `+1 → h+1`, `+3 → h+3`; selain itu
   tidak ada tahap. Karena Jakarta selalu UTC+7 (tanpa DST), jadwal tidak
   pernah bergeser satu jam — tetapi transisi UTC/WIB tetap diuji (pukul 00:30
   WIB sudah masuk hari WIB berikutnya walau jam server UTC masih hari
   sebelumnya).
2. **Tepat di hari tahap (tanpa catch-up).** Pengingat hanya lahir pada hari
   yang tepat; H-1, H+2, dan H+4 tidak menghasilkan email. Konsekuensinya bila
   worker mati satu hari penuh, tahap hari itu terlewat — disengaja, karena
   "mengejar" tahap yang terlewat justru mengirim dua email dalam satu hari
   (h-2 dan h-0 sekaligus) dan membuat riwayat salah. Pemulihan harian
   dijaga `RunOnStart` penjadwal.
3. **Satu baris `cnc_reminders (order_id, tahap)` = tahap sudah diantre.**
   Baris reminder, baris `email_outbox`, dan entri audit ditulis dalam **satu
   transaksi** (`AntreReminderCNC`). PRIMARY KEY `(order_id, tahap)` adalah
   penjaga "job ulang tidak ganda": `INSERT ... ON CONFLICT DO NOTHING
   RETURNING true` mengembalikan tanpa baris bila tahap itu sudah ada (termasuk
   bila transaksi lain sedang menulisnya — ON CONFLICT menunggu commit lalu
   tidak menulis apa pun), sehingga hasilnya `dibuat=false` dan email tidak
   digandakan.
4. **Gagal kirim ≠ gagal diantre.** Baris `cnc_reminders` tetap ada walau
   emailnya gagal terkirim; pengiriman diserahkan ke outbox (retry berjenjang
   1m/5m/30m/2j/6j, PRD EM-01). Baris outbox tetap `queued` (bukan `sent`,
   `sent_at` NULL) sampai SMTP benar-benar menerima pesan, lalu muncul sebagai
   chip "email gagal" bila percobaannya habis (EM-02/03) — **tidak** dianggap
   terkirim sebelum sukses.
5. **Kegagalan penulisan dicoba lagi, bukan hilang.** Bila transaksi gagal
   (mis. database menolak), rollback menghapus baris `cnc_reminders` juga,
   sehingga tahap itu belum tercatat dan akan dicoba pada pemindaian
   berikutnya. Kegagalan satu kandidat tidak menggagalkan seluruh job: service
   melanjutkan kandidat lain dan hanya mencatat `HasilReminder.Gagal`.
6. **Kandidat = `is_cnc AND status <> 'done' AND jatuh_tempo IS NOT NULL`.**
   Filter `status <> 'done'` inilah yang membuat pesanan yang lunas sebelum
   tahap berikutnya berhenti diingatkan. Pesanan non-C&C dan C&C tanpa jatuh
   tempo tidak pernah masuk. Pengingat **tidak mengubah status**: C&C lewat
   jatuh tempo tetap `submitted` sampai admin memverifikasi pembayaran
   (CP-CNC-07, F1-18).
7. **Penerima tunggal = email cabang** (PRD §6.2), diambil dari
   `branches.email` lewat join; kandidat tanpa email tidak membuat baris outbox
   tanpa penerima (dilewati, `ErrNoRecipient`).
8. **Audit hasil.** Setiap pengingat yang benar-benar dibuat meninggalkan satu
   entri `audit_logs` dengan `actor_kind='system'`, `actor_id` UUID nol
   (konvensi `backup.AktorSistem`), `entity='cnc_reminder'`, `action='remind'`,
   `entity_id=<id pesanan>`, dan `after` berisi `tahap`, `nomor`,
   `jatuh_tempo`, `penerima` — di dalam transaksi yang sama, jadi tidak ada
   email yang lahir tanpa jejak audit.
9. **Jadwal 07:00 WIB lewat `PeriodicSchedule` River.** `JadwalHarianWIB`
   menghitung pemicu berikutnya pada jam dinding WIB lalu mengembalikannya
   dalam UTC; jam dapat dikonfigurasi (`REMINDER_JAM_WIB`, bawaan 7) dan
   di luar 0–23 jatuh ke bawaan. `RunOnStart` dipasang sebagai jaring pengaman
   bila proses sempat mati melewati 07:00 — idempoten, jadi pemicu tambahan
   tidak pernah menggandakan email.

## Bukti verifikasi (dijalankan di repo target)

Gerbang:

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | 0 diff (tidak ada kueri sqlc baru) |
| `./scripts/check-secrets.sh` | exit 0 — tidak ada rahasia |
| `go test -count=1 ./...` (dengan `TEST_DATABASE_URL`) | semua paket ok, 0 FAIL, EXIT=0 (`httpapi` 276,2 s; `notification` 13,4 s; `worker` 2,1 s; `backup` 9,9 s) |
| `go test -race -count=1 ./internal/notification/... ./internal/worker/...` (dengan `TEST_DATABASE_URL`) | ok, EXIT=0 (`notification` 15,1 s; `worker` 3,9 s) |

Commit: pekerjaan ini berada di `main` repo `/home/hermes/projects/grahita-website`
(hash pasti ada di serah terima kartu kanban).

Uji unit (`go test ./internal/notification/ ./internal/worker/`):

- `TestTahapReminderEmpatTahap` — 8 geseran hari: hanya -2/0/+1/+3 punya tahap.
- `TestTahapReminderMemakaiTanggalWIB` / `TestJalankanReminderCNCMemakaiTanggalWIB`
  — pukul 00:30 WIB (UTC hari sebelumnya) sudah memicu tahap hari WIB itu;
  23:59 WIB belum.
- `TestJalankanReminderCNCIsiEmail` — penerima email cabang, jenis
  `reminder_cnc`, payload `template/nomor/tahap/jatuh_tempo/total/nama`.
- `TestJalankanReminderCNCTidakMenggandakanEmail` — dua pemindaian hari yang
  sama ⇒ tahap kedua dilewati, total email tetap.
- `TestJalankanReminderCNCTahapBerlanjutMemakaiClockYangDipindah` — clock
  dipindah 07:00 WIB hari demi hari ⇒ h-2, (h-1 kosong), h-0, h+1, (h+2
  kosong), h+3, (h+4 kosong).
- `TestJalankanReminderCNCGagalTidakDianggapTerkirim` — penulisan gagal ⇒ tidak
  ada baris reminder; pemindaian berikutnya berhasil.
- `TestJalankanReminderCNCTanpaPenerima` / `...TanpaStore`.
- `TestJadwalHarianWIBNextSelaluTujuhPagiWIB` — Next selalu 07:00 WIB dan
  SELALU setelah current, termasuk saat jam server UTC.

Uji integrasi PostgreSQL nyata (`-/run TestPGReminderCNC`, skema terisolasi +
shim uuidv7):

1. `TestPGReminderCNCJadwalEmpatTahapDanIdempoten` — memindahkan hari 06–13
   Oktober: hanya 4 hari mengantre; tiap hari dipindai DUA kali dan tidak ada
   penambahan; akhirnya tepat 4 baris `cnc_reminders` + 4 baris `email_outbox`,
   semuanya `reminder_cnc`, penerima `cabang-cnc@example.test`, payload tahap
   terisi.
2. `TestPGReminderCNCDoneDanNonCncDikecualikan` — dari 3 pesanan (Done,
   submitted, non-C&C) hanya yang belum Done jadi kandidat; setelah pesanan
   ditandai Done sebelum H+1, pemindaian H+1 mengantre 0 dan baris reminder
   tetap 1.
3. `TestPGReminderCNCAuditTercatat` — entri `audit_logs` `system` /
   `cnc_reminder` / `remind`, `entity_id` = id pesanan, `after.tahap` = `h-2`.
4. `TestPGReminderCNCGagalKirimDapatRetryTidakDianggapTerkirim` — kirim pertama
   ditolak SMTP ⇒ status tetap bukan `sent`, `sent_at` NULL, `attempts=1`,
   `cnc_reminders` tetap 1; setelah jeda retry, kirim kedua ⇒ `sent` dengan
   `sent_at` terisi dan baris tidak bertambah.

Ujung-ke-ujung lewat River (`go test -run TestWorkerMenjalankanPengingatCNC`):
worker dengan job `reminder_cnc` terdaftar benar-benar menjalankan pengingat
(job jadwal `RunOnStart` + satu job manual) dan menghasilkan **tepat satu**
baris `cnc_reminders` (h-2) + satu email `reminder_cnc` — membuktikan penjagaan
`(order_id, tahap)` bekerja lintas dua kali eksekusi job.

## Batas bukti & risiko tersisa (non-pemblokir)

- PG 16.15 + `scripts/pg-dev-shim.sql`, bukan PG 18; `golangci-lint` tidak
  tersedia di sesi ini (sama seperti kartu-kartu sebelumnya).
- Jadwal 07:00 WIB diuji lewat `JadwalHarianWIB.Next` dan satu job River nyata;
  penjadwal periodik River di produksi mengandalkan pemimpin terpilih dan hanya
  menyimpan state di memori — karena itu `RunOnStart` dipasang dan domainnya
  idempoten (tidak ada klaim ketepatan pukul yang lebih kuat).
- Pengiriman SMTP nyata tidak diuji (mailer tiruan); jalur outbox, retry, dan
  status `sent` sudah diuji di paket notification.
- Tahap yang terlewat bila worker mati satu hari penuh tidak dikejar (keputusan
  §2 di atas) — disengaja, tidak dianggap cacat.

## Tindak lanjut non-pemblokir QA F2-23 (kartu t_9b6a1dbf)

Dua catatan non-pemblokir QA dikerjakan di sini. Perilaku pengingat **tidak
berubah**: himpunan tahap yang lahir identik (dibuktikan uji), yang berubah
hanya dokumentasi semantik kolom dan jumlah baris yang dibaca kueri kandidat.

### 1. Semantik `cnc_reminders.sent_at`

`sent_at` mengambil `DEFAULT now()` saat baris pengingat LAHIR (waktu tahap
DIANTRE oleh `AntreReminderCNC`), BUKAN waktu email benar-benar terkirim. Nama
kolom mengikuti `Database.md` §4.7 dan tidak diubah; yang ditambahkan hanya
penjelas agar kolom itu tidak dibaca sebagai laporan pengiriman:

| Berkas | Yang ditambahkan |
|---|---|
| `api/db/migrations/0007_cnc_reminders_sent_at.sql` (baru) | `COMMENT ON COLUMN cnc_reminders.sent_at`; bagian `Down` mengosongkan komentar |
| `api/db/migrations/0001_init.sql` | komentar sebaris pada kolom `sent_at` (komentar saja, tanpa perubahan skema) |
| `docs/Database.md` §4.7 | komentar yang sama pada blok SQL `cnc_reminders` |
| `api/internal/db/sqlc/models.go` | hasil `sqlc generate`: komentar mengalir ke bidang `CncReminder.SentAt` |

Kebenaran "email sudah terkirim" tetap di `email_outbox.sent_at`, yang hanya
terisi setelah SMTP menerima pesan
(`TestPGReminderCNCGagalKirimDapatRetryTidakDianggapTerkirim`). Migrasi 0007
tidak mengubah tipe/nullability/data, jadi aman dijalankan pada basis data yang
sudah berisi dan lolos `scripts/check-migration-kompatibel.sh`.

### 2. Jendela tanggal kandidat `KandidatCNC`

Kueri kandidat dulu membaca SELURUH pesanan C&C `status <> 'done'` yang
berjatuh tempo, lalu menyaring tahap per baris di Go. Sekarang ia dibatasi
jendela tanggal jatuh tempo `[hari-3, hari+2]`
(`notification.JendelaKandidatCNC`):

- Jendela itu diturunkan dari jadwal tahap (h-2 → jatuh tempo hari+2; h-0 →
  hari; h+1 → hari-1; h+3 → hari-3), jadi ia **superset** dari tanggal
  ber-tahap. `TahapReminder` tetap satu-satunya penentu tahap; jendela hanya
  membatasi baris yang dibaca sehingga himpunan tahap yang lahir tidak berubah.
- `ReminderStore.KandidatCNC` kini menerima `hari` (tanggal bisnis WIB) supaya
  jendela mengikuti jam aplikasi (`clock.TodayWIB`), bukan jam dinding basis
  data; `JalankanReminderCNC` menghitung `hari` sebelum memanggil.
- Batas jendela dikirim sebagai teks `$1::text::date` (pola yang sama dengan
  kueri audit) supaya tidak bergantung zona waktu sesi basis data.
- Efek pada `HasilReminder`: `Diperiksa`/`Dilewati` turun ke jumlah kandidat di
  dalam jendela (maknanya tidak berubah); `Diantre`/`Gagal` tidak berubah.

Uji yang mengunci sifat ini:

- `TestJendelaKandidatCNCMenutupSemuaTahap` (unit) — menyapu ±400 hari: SETIAP
  jatuh tempo yang menghasilkan tahap menurut `TahapReminder` berada di dalam
  jendela; sekaligus membuktikan jendela memang lebih luas (hari-2 dan hari+1
  tidak ber-tahap).
- `TestJendelaKandidatCNCMemakaiTanggalWIB` (unit) — batas jendela dihitung pada
  tanggal bisnis WIB.
- `TestJalankanReminderCNCDiteruskanHariBisnisWIB` (unit) — service meneruskan
  hari bisnis WIB dan kandidat di luar jendela tidak dibaca.
- `TestPGReminderCNCJendelaTidakMengubahTahap` (integrasi PostgreSQL) — 21 hari
  uji × 16 pesanan (jatuh tempo −60..+60 hari): tiap hari, kandidat berjendela
  dibandingkan dengan acuan kueri LAMA (tanpa jendela); kandidat berjendela
  wajib subset acuan, semua pesanan acuan yang ber-tahap hari itu wajib ada di
  jendela, dan tahap yang benar-benar lahir sama persis dengan acuan — sekaligus
  terbukti jendela benar-benar memangkas.
- `TestPGReminderCNCJendelaMemakaiBatasWIB` (integrasi PostgreSQL) — batas
  inklusif: pada hari bisnis 2026-10-07 hanya jatuh tempo 10-04..10-09 yang
  dibaca.

Bukti pemangkasan pada volume (20.060 pesanan C&C belum Done; 60 di antaranya di
jendela), diukur di basis data uji dengan `EXPLAIN (ANALYZE, BUFFERS)`:

| Kueri | Baris hasil | Pemindaian | Buffer | Waktu |
|---|---|---|---|---|
| Berjendela (`2026-10-04`..`2026-10-09`) | 60 | `Index Scan` pada indeks parsial `orders_cnc_jatuh_tempo` (kondisi indeks langsung memuat rentang) | `shared hit=16` | 0,66 ms |
| Lama tanpa jendela | 20.060 | `Seq Scan on orders` + sort | `shared hit=352` | 32,9 ms |

### Gerbang ulang (tindak lanjut, kode akhir)

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | 0 diff (idempoten; `models.go` sudah memuat komentar kolom) |
| `scripts/check-migration-kompatibel.sh` | exit 0 (0007 hanya `COMMENT ON COLUMN`) |
| `./scripts/check-secrets.sh` | exit 0 |
| `go test -race -p 1 -count=1 ./internal/notification/... ./internal/worker/...` | ok (notification 20,8 s; worker 4,7 s), EXIT=0 |
| `go test -p 1 -count=1 ./...` (basis data segar + shim) | 29 paket ok, 0 FAIL, EXIT=0 (httpapi 307,4 s; notification 13,1 s; worker 2,1 s) |

Catatan: `-p 1` wajib saat menjalankan beberapa paket pada SATU basis data —
`CREATE EXTENSION IF NOT EXISTS citext` pada dua paket paralel bisa balapan
(galat unik `pg_extension_name_index`) atau meninggalkan ekstensi di skema uji
yang membuat paket berikutnya tidak menemukan tipe `citext`. Bukan cacat kode;
muncul hanya karena eksekusi paralel lintas paket pada basis data bersama.

