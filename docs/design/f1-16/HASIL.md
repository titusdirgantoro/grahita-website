# F1-16 — Email outbox, template, retry dan kirim ulang

Kartu: **t_3315b041 — F1-16 Email outbox, template, retry dan kirim ulang**

Dokumen acuan: PRD §6.2 (EM-01..EM-04); Architecture.md §2, §5; SystemDesign.md
§5.1 (transactional outbox), §5.3 (idempotensi), §6 (permukaan API);
Database.md §4.7 (`email_outbox`, `cnc_reminders`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Paket baru `api/internal/notification` (service + template + adaptor) dan
`api/internal/notification/postgres` (repository), worker River pengirim,
serta dua endpoint admin:

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/api/v1/admin/orders/{id}/emails` | Manager & Superadmin | riwayat email pesanan + status/`last_error` (PRD EM-02) |
| POST | `/api/v1/admin/orders/{id}/emails/{kind}/resend` | Manager & Superadmin | kirim ulang `invoice_cabang` / `invoice_admin` / `surat_jalan_admin`, ber-audit |
| POST | `/api/v1/admin/users/admin/{id}/resend` | Superadmin | pemulihan undangan admin yang gagal terkirim |

Perubahan jalur email yang sudah ada:

- **Lupa kata sandi** (`email_kind reset_password`): `auth.ResetMailer` sekarang
  dipenuhi adaptor `notification.ResetMailer` yang **menulis baris
  `email_outbox`**, bukan mengirim SMTP. Kontrak handler tetap: respons lupa
  selalu `200` dan non-enumeratif; tautan hanya ada di email.
- **Undangan admin** (`email_kind undangan_admin`): `users.EmailQueue` diisi
  `notification.Queue`, jadi `LogQueue` (penanda sementara) sudah tidak dipakai
  di produksi.
- **Kirim ulang tautan reset akun cabang** (`POST /admin/users/branch/{id}/reset`)
  ikut berpindah ke outbox lewat jalur yang sama.

Semua `email_kind` di Database.md §3 punya template: `reset_password`,
`undangan_admin`, `invoice_cabang`, `invoice_admin`, `surat_jalan_admin`,
`reminder_cnc`. `cnc_reminders` (tabel tahap H-2/H-0/H+1/H+3) tetap milik kartu
F2-23; template dan jenis emailnya sudah siap dipakai kartu itu.

## Keputusan desain

1. **Outbox sebagai satu-satunya jalan keluar email.** Tidak ada satu pun
   jalur PRODUKSI (handler HTTP atau service) yang memanggil `mailer.Send`.
   Satu-satunya sisa pemanggil langsung adalah `auth.EmailResetMailer`, yang
   kini ditandai `Deprecated` dan hanya dipakai uji lama (komposisi root memakai
   `notification.ResetMailer`). Jaminan ini diuji:
   setelah `POST /auth/password/forgot` dan `POST /admin/users/admin`, baris
   `email_outbox` sudah ada tetapi pengirim **belum** dipanggil sama sekali
   (uji `TestPGLupaKataSandiLewatOutboxTidakDikirimLangsung`,
   `TestPGUndanganAdminDiantreLaluDikirimWorker`).
2. **Worker memindai tabel, bukan antre job per email.** Job River untuk email
   adalah job periodik tanpa argumen (`email_outbox_pindai`, default 1 menit;
   `OUTBOX_INTERVAL`), jadi seluruh keadaan email ada di `email_outbox`.
   Keuntungannya: job yang hilang/ganda tidak pernah menggandakan email, dan
   percobaan ulang tetap terlihat di tabel (attempts/next_try_at/last_error)
   sehingga riwayat dan chip "email gagal" (EM-02/EM-03) membaca satu sumber.
3. **Klaim baris atomik dengan sewa (lease).** `Claim` memakai
   `UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED)`, menaikkan
   `attempts`, dan menyetel `next_try_at = now + lease` (default 15 menit,
   `notification.DefaultLease`). Dua worker tidak bisa memegang baris yang sama
   (`TestPGKlaimParalelMengirimTepatSekali`: 20 email, 2 worker, 20 kirim,
   0 baris dengan `attempts > 1`) dan baris `sending` yang ditinggal proses
   mati pulih sendiri setelah sewa habis
   (`TestPGKlaimMemulihkanBarisSendingYangDitinggalProsesMati`).
4. **Retry berjenjang 1m/5m/30m/2j/6j: maksimum 5 percobaan ULANG** (PRD EM-01,
   SystemDesign §5.1 "retry berjenjang ... maks 5 kali"), jadi satu email boleh
   dikirim sampai 6 kali (`notification.MaxAttempts = 1 + MaxRetry`). Semua lima
   jeda benar-benar terpakai sebelum status `failed` — kalau batasnya dibaca
   sebagai 5 percobaan total, jeda 6j tidak akan pernah tercapai. Baris `failed`
   menyimpan `last_error`, tidak pernah dicoba lagi oleh worker, dan hanya bisa
   dikirim ulang admin (`TestRetryBerjenjangSampaiMenyerahSetelahLimaPercobaanUlang`,
   `TestPGJadwalRetryTersimpanDanBerhentiSetelahLimaPercobaan`).
   Baris `sending` yang ditinggal proses mati DAN percobaannya sudah habis
   ditandai `failed` pada pemindaian berikutnya supaya tidak tersangkut
   selamanya (`TestPGBarisSendingYangPercobaannyaHabisMenjadiGagal`).
5. **Baris `sent` tidak pernah dikirim ulang oleh worker** (idempoten);
   pengiriman ulang hanya lahir dari tindakan admin eksplisit dan selalu
   tercatat di `audit_logs` (`entity='email'`, `action='resend'`, `entity_id` =
   id baris email baru).
6. **Kirim ulang = pemulihan, bukan pembuat email baru.** `ResendOrder`
   menolak (`404`) bila jenis itu belum pernah dibuat untuk pesanan tersebut,
   dan menolak (`409 EMAIL_ALREADY_QUEUED`) bila masih ada email jenis yang sama
   berstatus `queued`/`sending` — klik ganda tidak menggandakan email
   (`TestResendOrderAturanOtorisasiDanIdempotensi`,
   `TestPGResendEmailDokumenRBACRiwayatDanAudit`).
7. **Penerima selalu dihitung ulang saat email diantre maupun saat dikirim
   ulang.** Email Pusat diambil dari repository pengguna
   (`DocumentRecipients`) sehingga hanya admin **aktif DAN sudah diaktivasi**
   yang ber-flag `notif_invoice`/`notif_surat_jalan` yang menerima; admin
   undangan dan nonaktif tidak pernah masuk daftar. Tidak ada alamat Pusat
   tambahan di luar flag per admin (PRD AP-USR-01/02, §8 no.5).
   Bukti: `TestPGEmailDokumenHanyaKePenerimaYangBerhak` (3 admin ber-flag, hanya
   1 yang menerima) dan `TestPGEmailDokumenHanyaKePenerimaYangBerhak` bagian
   surat jalan, serta `TestResendOrderMemakaiPenerimaYangBerhakSaatItu`.
8. **Email dokumen menunggu render selesai.** `EnqueueOrderEmail` menuntut baris
   `documents` pada **versi pesanan yang diminta** (`invoice_pdf` untuk kedua
   jenis invoice; `surat_jalan_pdf` + `surat_jalan_xlsx` untuk surat jalan).
   Bila belum ada dijawab `ErrDocumentNotReady` (`409 DOCUMENT_NOT_READY`) dan
   **tidak ada baris outbox yang dibuat**; lampiran juga dibaca ulang dari
   object storage saat mengirim, sehingga email tidak pernah keluar tanpa
   berkas (`TestEnqueueOrderEmailMenungguDokumenSelesai`,
   `TestPGResendEmailDokumenMenungguRenderDanPenerima`).
9. **Email cabang memakai `branches.email`.** Kolom itu di Database.md §4.1
   memang diberi keterangan "email cabang = email login" dan pendaftaran
   (F1-07) menyalinnya ke akun cabang, sehingga alamat kirim invoice tidak
   bergantung pada keadaan akun cabang saat itu.
10. **Mailer SMTP punya batas waktu TOTAL operasi.** `Send` memasang
    `SetDeadline` pada soket DAN `context.WithDeadline` (default 30s,
    `SMTP_TIMEOUT`), sehingga peer yang diam tidak menggantung worker maupun
    request lupa kata sandi (temuan QA F1-05 untuk F1-16). Deadline context
    pemanggil yang lebih ketat tidak pernah dilonggarkan.
11. **Lampiran MIME sungguhan.** `mailer.Message` menerima `Attachments`;
    pesan dengan lampiran dibentuk sebagai `multipart/mixed` berisi
    `multipart/alternative` + lampiran base64, dan subjek non-ASCII di-encode
    RFC 2047. Diuji sampai protokol SMTP terhadap server SMTP palsu
    (`TestSMTPMengirimLampiranLewatServerPalsu`).
12. **Rollback tidak mengirim.** Repository menyediakan `EnqueueTx(ctx, tx, ...)`
    sehingga kartu domain dokumen/pesanan (F1-18, F2-25) dapat menulis baris
    outbox DI DALAM transaksi bisnisnya. Diuji: baris yang ditulis lalu
    rollback tidak menyisakan apa pun dan tidak ada email terkirim
    (`TestPGTransaksiRollbackTidakMengirimEmail`).
13. **Kegagalan parsial undangan admin punya jalan pemulihan.** `InviteAdmin`
    belum atomik terhadap `email_outbox` (repository pengguna dan repository
    token terpisah), jadi kartu ini menambahkan `ResendInviteAdmin` +
    `POST /admin/users/admin/{id}/resend`: tautan aktivasi dibuat ulang (tautan
    lama otomatis batal) dan email undangan diantre ulang, **tanpa** membuat akun
    kedua dan tanpa mengubah keadaan akun. Akun yang sudah aktif/nonaktif
    dijawab `409 NOT_INVITED` (memakai alur lupa kata sandi).
    Bukti: `TestPGKirimUlangUndanganAdminIdempoten`,
    `TestPGKirimUlangUndanganDitolakUntukAkunNonaktif`.
14. **Satu tautan aktif per akun kini dijamin satu pernyataan SQL.** Query
    `ReplacePasswordResetToken` membatalkan tautan lama dan menyimpan yang baru
    dalam satu pernyataan (data-modifying CTE), menutup catatan QA F1-04
    ("DeleteActivePasswordResetTokens lalu CreatePasswordResetToken tanpa
    transaksi"): kegagalan separuh jalan tidak boleh meninggalkan akun tanpa
    tautan aktif. Bukti: `TestPGTokenResetHanyaSatuYangAktif`.

## Perbaikan QA putaran 1 (b00d2e6)

QA putaran 1 (lensa artifact + eksekusi) meminta perubahan pada tiga hal plus
satu temuan dari kartu F1-15. Semuanya dikerjakan di kartu ini.

15. **Kirim ulang ATOMIK (PEMBLOKIR butir 1).** Sebelumnya `ResendOrder`
    memeriksa `Latest`/`ActiveExists` DI LUAR transaksi insert (`postgres.go`
    `Resend`), sehingga dua permintaan bersamaan sama-sama lolos dan lahirlah
    dua email + dua audit (probe QA: reproduksi 3/3). Kini `Resend`
    - membuka transaksi,
    - mengambil `pg_advisory_xact_lock(hashtext(order_id), hashtext(jenis))`
      (berlaku sampai commit, jadi permintaan lain menunggu),
    - memeriksa ulang "masih ada queued/sending" DI DALAM transaksi itu,
    - baru menyimpan baris + audit.
    Yang kedua melihat baris baru dan dijawab `409 EMAIL_ALREADY_QUEUED`.
    Pemeriksaan cepat di service tetap ada hanya sebagai jalan pintas (tidak
    otoritatif). Bukti:
    `TestPGResendBersamaanTidakMenggandakanEmail` — 8 permintaan bersamaan per
    ronde, 2 ronde → tiap ronde tepat 1 sukses dan 7 `ErrAlreadyQueued`, total
    3 baris outbox (asli + 2 kirim ulang) dan 2 entri audit. Kontrol negatif
    yang terukur: dengan kunci advisory dilepas, uji itu GAGAL 3/3 jalan
    (`sukses=8 ditolak=0`) — jadi penjaganya benar-benar bekerja. Uji ini
    diperkuat pada kartu tindak lanjut `t_9c58996f` (lihat bagian
    "Penyempurnaan uji regresi" di bawah) karena versi 5 goroutine yang lama
    ternyata FLAKY saat kunci dilepas (hanya 3 dari 5 jalan merah).
16. **Semantik at-least-once didokumentasikan dan diuji (PEMBLOKIR butir 2).**
    Pengiriman TIDAK tepat-sekali: bila proses mati (atau `MarkSent` gagal)
    SETELAH SMTP menerima, baris diklaim ulang setelah sewa 15 menit dan
    mailer dipanggil dua kali. Ini inheren pada pola outbox dan disengaja;
    tepat-sekali harus lewat idempotency key di sisi penerima (kartu terpisah
    bila produk membutuhkannya). Dinyatakan di doc paket `notification`,
    README, OpenAPI, dan diabadikan uji
    `TestPGAtLeastOnceKirimDuaKaliSetelahSMTPMenerima` (akhir: `sent`,
    `attempts=2`, mailer 2×). Klaim "baris `sent` tidak pernah dikirim ulang"
    tetap berlaku; yang tidak diklaim adalah "tepat-sekali".
17. **Pemindaian yang berhenti di tengah batch melepas baris yang belum
    dicoba (butir 3).** `Claim` mengambil sampai `Batch=50` baris dan menaikkan
    `attempts` semuanya, sementara satu kirim bisa memakai `SMTP_TIMEOUT=30s`
    dan `JobTimeout` River 2 menit — job yang dibatalkan di tengah batch
    dahulu meninggalkan baris `sending` dengan attempts TERPAKAI, sehingga
    berulang kali email bisa mencapai `failed` tanpa pernah dicoba. Kini
    `ProsesAntrean` memanggil `store.Release` untuk baris yang BELUM dicoba
    (sejak baris yang sedang diproses) pada setiap jalan keluar dini — context
    dibatalkan, `MarkSent`/`GiveUp`/`Reschedule` gagal — dan `Release`
    mengembalikan `status='queued'` serta `attempts = GREATEST(attempts-1, 0)`.
    Pelepasan memakai `context.WithoutCancel` (context-nya memang sudah mati)
    dengan tenggat 5 detik. Baris yang sudah dicoba tetap `sending`/
    dijadwalkan ulang. Sisa yang TIDAK tertutup: proses yang mati keras (SIGKILL)
    tanpa jalan keluar dini — di situ sewa 15 menit tetap berlaku dan
    `attempts` bisa terpakai; itu memang batas pola lease, dan jalan
    pemulihannya adalah kirim ulang admin. Bukti:
    `TestSisaKlaimDilepasSaatPemindaianDibatalkan` (unit) dan
    `TestPGSisaKlaimDilepasSaatPemindaianDibatalkan` (integrasi PG).
18. **Id pesanan bukan UUID dijawab 404, bukan 500 (temuan QA F1-15).**
    `ListByOrder`, `ResendOrder`, dan `buildOrderEnvelope` menolak id yang tidak
    cocok pola UUID dengan `ErrNotFound` (404) sebelum menyentuh SQL, sehingga
    SQLSTATE 22P02 tidak pernah sampai ke klien. Pola diperiksa sama persis
    dengan paket `order`. Bukti: uji unit
    `TestIdPesananBukanUUIDDijawabNotFound` dan integrasi HTTP
    `TestPGEmailDokumenIdBukanUUIDTidak500` (GET & POST resend → 404; UUID sah
    tanpa pesanan → 200 riwayat kosong). OpenAPI endpoint riwayat kini
    mencantumkan respons `404`.
19. **Nilai data di-escape pada badan HTML email (catatan non-pemblokir QA).**
    `template.go` menyisipkan `nama`/`cabang`/`nomor`/`tautan`/dst. ke HTML;
    kini semuanya melewati `html.EscapeString` (`esc()`) dan komentar yang
    keliru diperbaiki. Bukti: `TestTemplateHTMLMengEscapeNilaiData`.

## Penyimpangan / keputusan yang perlu dicatat

- **Nama paket**: SystemDesign §2 menulis `notification/`; kartu ini memakai
  `internal/notification` + `internal/notification/postgres` — persis pola
  domain lain (`order`, `users`, `bank`).
- **`runtime` job**: nama job River `email_outbox_pindai` (bukan nama modul)
  dan dijalankan sebagai job periodik. River hanya mengizinkan satu instance
  scheduler (leader) yang menyisipkan job periodik, jadi tidak ada risiko
  penjadwalan ganda; klaim baris tetap penjaga terakhirnya.
- **`api/` belum punya mode pengiriman email inline**: mode `api` hanya menulis
  outbox, mode `worker` yang mengirim. Lingkungan tanpa proses worker akan
  menumpuk email `queued` — disengaja (SystemDesign §5.1) dan terlihat di
  `GET /admin/orders/{id}/emails`.
- **`cnc_reminders`/job cron H-2..H+3** tidak dijadwalkan kartu ini (milik
  F2-23); yang disiapkan di sini adalah jenis email, template, dan jalur
  pengirimannya.
- **`retention` email terkirim 90 hari** (Database.md §7) belum ada pekerja
  pembersihnya; belum ada pemilik kartu — diusulkan ke CEO.

## Bukti verifikasi

Perintah (cwd `api/`, toolchain Go `go1.27.0`, sqlc `v1.31.1`):

```
gofmt -l .                                   # kosong
go vet ./...                                 # exit 0
go build ./...                               # exit 0
sqlc-v1.31.1 generate                        # tanpa diff (idempoten)
sh scripts/check-secrets.sh                  # tidak ada rahasia terdeteksi
TEST_DATABASE_URL=postgres://…/grahita_test  # PG16 + scripts/pg-dev-shim.sql
go test -race -p 1 -count=1 ./...            # 15 paket ok, exit=0, tanpa FAIL
```

Hasil nyata suite penuh (`go test -race -p 1 -count=1 ./...`, log
`cache/scratch/f116-final.log`): `arch`, `auth`, `bank`, `branch`, `catalog`,
`httpapi`, `notification`, `order`, `platform/apierr`, `platform/database`,
`platform/mailer`, `registration`, `settings`, `users`, `worker` — semuanya
`ok`, tidak ada FAIL/SKIP/panic.

Probe binary sungguhan (bukan test) pada DB bersih PG16 + shim
(`cache/scratch/f116-probe.sh`, `f116-api-probe.sh`):

1. **Mode worker**: `--mode=migrate` lalu `--mode=worker` dengan
   `OUTBOX_INTERVAL=2s`. Dalam 12 detik: 5 job `email_outbox_pindai` lahir di
   `river_job` (RunOnStart + tiap 2 detik) dan satu baris `email_outbox` yang
   sengaja disisipkan menjadi `status=sent attempts=1 sent_at` terisi.
   → jalur produksi (job periodik River → klaim → kirim → tandai) benar-benar
   berjalan pada binary hasil `go build`, bukan hanya di dalam test.
2. **Mode api**: `--mode=seed` (SEED_PASSWORD) lalu `--mode=api`; `POST
   /api/v1/auth/password/forgot` untuk akun cabang seed menjawab `200` dan yang
   lahir adalah baris `email_outbox` (`jenis=reset_password`,
   `to_addr=bandung@grahitaindonesia.com`, `status=queued`, payload memuat
   `http://localhost/lupa?token=…`), tanpa satu pun pemanggilan pengirim SMTP
   di proses API. → composition root `main.go` (service outbox + adaptor
   `auth.ResetMailer`) terbukti terpasang.

Uji yang ditambahkan kartu ini:

- `internal/notification`: 23 uji unit (jadwal retry, antrean, klaim sewa,
  retry→failed, baris sending tersangkut, sisa klaim dilepas saat pemindaian
  dibatalkan, id pesanan bukan UUID → `ErrNotFound`, escaping HTML template,
  lampiran gagal dibaca, 6 template, aturan kirim ulang, adaptor reset/undangan)
  + 10 uji integrasi PostgreSQL (rollback, klaim paralel, pemulihan `sending`,
  jadwal retry tersimpan, `sending` yang percobaannya habis → `failed`,
  resend+audit satu transaksi, satu tautan aktif, kirim ulang bersamaan tidak
  menggandakan email, at-least-once setelah SMTP menerima, sisa klaim dilepas
  saat pemindaian dibatalkan).
- `internal/platform/mailer`: 6 uji (bentuk MIME + lampiran, deadline peer
  diam, deadline mengikuti context, kirim lampiran lewat server SMTP palsu).
- `internal/httpapi` (integrasi PG): 10 uji baru — lupa kata sandi lewat outbox
  + tautan sekali pakai, akun tak dikenal tidak mengantre, undangan admin
  diantre lalu dikirim worker, kirim ulang undangan idempoten, penerima email
  dokumen, RBAC + riwayat + audit kirim ulang, email gagal tampil di riwayat
  dan bisa dikirim ulang, dokumen belum siap / tanpa penerima, kirim ulang
  undangan untuk akun nonaktif, id pesanan bukan UUID tidak 500.

## Bukti verifikasi putaran 2 (perbaikan QA putaran 1)

Perintah dijalankan di repo target (cwd `api/`, Go `go1.27.1`, sqlc `v1.31.1`,
PG16.15 + `scripts/pg-dev-shim.sql`):

```
gofmt -l .                                   # kosong
go vet ./...                                 # exit 0
go build ./... && go build ./cmd/server      # exit 0
sqlc-v1.31.1 generate                        # tanpa diff (git status bersih)
sh scripts/check-secrets.sh                  # tidak ada rahasia terdeteksi (exit 0)
node @redocly/cli lint openapi/openapi.yaml  # "API description is valid" (10 peringatan lama)
TEST_DATABASE_URL=…/grahita_test  (PG16)
go test -race -p 1 -count=1 ./...            # 19 paket ok, 0 FAIL, exit 0
                                             # (log cache/scratch/f116r2-full.log)
```

Kontrol negatif putaran 2 (dicatat apa adanya setelah temuan QA): dengan
`pg_advisory_xact_lock` di `Resend` dinonaktifkan sementara, uji implementer
saat itu (`TestPGResendBersamaanTidakMenggandakanEmail`, 5 goroutine) memang
GAGAL (`sukses=2 ditolak=3`) — tetapi hanya 3 dari 5 jalan, jadi angka itu
kebetulan, bukan jaminan. Kunci dipulihkan dan uji hijau kembali. Uji yang
benar-benar dapat diandalkan sebagai penjaga regresi baru dibuat pada kartu
tindak lanjut `t_9c58996f` (bagian berikutnya) dengan hasil kontrol negatif
yang deterministik.

## Penyempurnaan uji regresi (kartu tindak lanjut t_9c58996f)

Temuan non-pemblokir QA putaran 2: uji balapan kirim ulang terlalu lemah
sebagai penjaga regresi. Versi lama (5 goroutine, hanya penghalang start dari
luar) bergantung pada penjadwalan Go — permintaan pertama bisa menyelesaikan
seluruh transaksinya sebelum yang lain memeriksa antrean, sehingga uji tetap
HIJAU meski kunci advisory sudah dilepas (terukur: 3 dari 5 jalan merah).
Perilaku produksi TIDAK diubah (`Resend` tetap sama).

Yang diperkuat pada `TestPGResendBersamaanTidakMenggandakanEmail`
(`api/internal/notification/integration_pg_test.go`):

- 8 permintaan bersamaan (dari 5) dan **2 ronde** berurutan pada pesanan yang
  sama (baris ronde sebelumnya ditandai `sent` lebih dulu) sehingga hasilnya
  bukan kebetulan satu kali jalan.
- **Penghalang start dua lapis**: `close(mulai)` sebelum `ResendOrder`, plus
  `penghalangResend` — pembungkus `notification.Store` yang menahan SETIAP
  panggilan `store.Resend` sampai kedelapan goroutine tiba, lalu melepas
  semuanya sekaligus. Kedelapan permintaan karena itu masuk ke transaksi
  produksi (`postgres.Repo.Resend`) pada saat yang hampir sama; pembungkusnya
  hanya menyinkronkan, tidak mengubah kode produksi.
- **Kolam koneksi dipanaskan** (`panaskanKolam`: 8 koneksi dibuka lalu
  dilepas) supaya pool `MinConns=1` tidak mendial koneksi baru di tengah
  perlombaan dan menyerialkannya secara palsu.

Hasil kontrol negatif yang benar-benar terukur (kunci
`pg_advisory_xact_lock` dilepas pada salinan kerja `postgres.go`, lalu
dipulihkan dan dibuktikan identik dengan HEAD lewat `git diff` kosong):

```
go test -race -count=1 -run TestPGResendBersamaanTidakMenggandakanEmail ./internal/notification/
# kunci DILEPAS  -> 3/3 jalan GAGAL, semuanya persis:
#   "ronde 1: harus tepat 1 kirim ulang sukses dan 7 ditolak, dapat sukses=8 ditolak=0 lain=0"
# kunci DIPULIHKAN -> 3/3 jalan PASS (0.86s-1.00s)
```

Dengan kata lain uji sekarang MERAH secara deterministik saat penjaga atomik
hilang (bukan 3/5 seperti sebelumnya) dan HIJAU saat penjaganya ada.

Gerbang pada repo target saat tindak lanjut ini (PG16.15 +
`scripts/pg-dev-shim.sql`, Go `go1.27.1`, cwd `api/`), semuanya hijau:
`gofmt -l .` kosong; `go vet ./...` exit 0; `go build ./...` dan
`go build ./cmd/server` exit 0; `sh scripts/check-secrets.sh` exit 0; dan
`go test -race -p 1 -count=1 -timeout 30m ./...` → 26 paket `ok`, tanpa
FAIL/SKIP/panic/data race (httpapi 667.9s, notification 17.3s), exit 0
(log `cache/scratch/f116tl_suite.log`). Perubahan hanya di
`integration_pg_test.go`, `.env.example`, dan halaman ini; `postgres.go`
dipulihkan persis seperti semula (`git diff` kosong).

Alamat email pribadi pengelola integrasi juga dihapus dari berkas yang
dilacak: komentar `MAIL_FROM` di `.env.example` kini memakai placeholder
generik `nama@contoh.id`; nilai bawaan tetap `no-reply@grahita.id` (README
memang tidak pernah memuat alamat itu). Bukti: pencarian alamat email
pengelola lama pada seluruh berkas terlacak tidak menemukan apa pun (0 hasil)
dan `sh scripts/check-secrets.sh` lolos (exit 0).

## Risiko residu

- **At-least-once**: email yang sama bisa terkirim dua kali pada celah
  pasca-SMTP (proses mati setelah SMTP menerima, sebelum status `sent`
  tersimpan). Pengguna menerima risiko duplikat LANGKA ini; ia BUKAN izin untuk
  duplikasi klik/resend biasa (klik ganda kini atomik dan dijawab 409).
- **Proses mati keras tanpa jalan keluar dini** (SIGKILL/mati listrik) tidak
  dapat memanggil `Release`, jadi baris yang belum dicoba menunggu sewa 15 menit
  dan `attempts`-nya sudah terpakai. Ini batas pola lease; pemulihannya lewat
  kirim ulang admin bila email akhirnya `failed`.
- PG18 asli (`postgres:18-alpine`) belum dieksekusi; uji integrasi berjalan di
  PG16 + shim `uuidv7()` — sama dengan kartu F1-02..F1-11.
- `golangci-lint` tidak tersedia di sesi ini (gate lint tidak direproduksi).
- Wire email end-to-end ke Mailpit sungguhan tidak dijalankan (SMTP diuji
  terhadap server palsu dan unit); verifikasi manual `docker compose up` +
  Mailpit ada di kartu smoke/rilis F1-22.
- `resend` untuk email non-dokumen (mis. `reminder_cnc`) sengaja tidak
  disediakan: endpoint kirim ulang hanya untuk email dokumen pesanan sesuai
  SystemDesign §6.
- Batas 25 MB lampiran (batas attachment kanban, bukan aplikasi): tidak ada
  batas ukuran lampiran di service; ukuran dokumen dibatasi alur render
  (F1-18).

## Kewajiban untuk kartu turunan

- **F1-18 (verifikasi → Done)**: enqueue email dokumen HARUS memakai
  `notification.EnqueueOrderEmail` di dalam transaksi yang sama dengan
  status/documents/audit, dan `store.EnqueueTx` untuk menulis barisnya;
  penerima invoice/surat jalan tidak dihitung di kartu itu.
- **F2-25 (revisi invoice)**: kirim ulang versi baru memakai `Versi` pada
  `OrderEmailInput`; dokumen versi lama tidak boleh dipakai.
- **F2-23 (reminder C&C)**: memakai jenis `reminder_cnc` + `cnc_reminders`
  sebagai penjaga sekali-per-tahap; template & pengiriman sudah tersedia.
- **F1-20 (dashboard)**: chip "email gagal" membaca `email_outbox.status='failed'`
  (Database.md §7.2) atau `GET /admin/orders/{id}/emails`; detail pesanan admin
  perlu memakai endpoint itu untuk panel riwayat email + tombol **Kirim ulang**
  per jenis dokumen (belum ada UI-nya di kartu ini).
- **F1-22 (rilis)**: implementasi `storage.Store` untuk S3/MinIO harus dipasang
  di mode `api` DAN `worker` — selama storage masih `Disabled`, email dokumen
  akan gagal membaca lampiran dan berakhir `failed` (sengaja: lebih baik gagal
  daripada terkirim tanpa berkas).

## Hotspot

- `openapi/openapi.yaml` dan `api/db/queries/reset.sql` dipakai bersama kartu
  lain; perubahan di sini kecil dan aditif, tetapi tetap perlu rebase hati-hati
  bila kartu F1-18/F2-23/F2-24 menambah path/skema pada saat bersamaan.
