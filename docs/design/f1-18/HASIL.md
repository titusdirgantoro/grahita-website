# F1-18 — Verifikasi pembayaran manual dan transisi Done

Kartu: **t_59b01658 — F1-18 Verifikasi pembayaran manual dan transisi Done**

Dokumen acuan: PRD §4.2 (`AP-ORD-07`), §5 (verifikasi bukti transfer), §7
(BR-07/BR-08) dan §7.1 (state machine), §9 no.6; SystemDesign.md §4.3
(verifikasi pembayaran), §4.4 (alur setelah Done), §5.3–5.4 (idempotensi,
audit); Database.md §4.4 (`orders`), §4.5 (`verification_logs`), §4.6
(`documents`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

F1-18 adalah transisi **Submitted → Done** dan sub-status **perlu review**,
beserta seluruh efek sampingnya. Yang baru:

| Berkas / artefak | Isi |
|---|---|
| `api/db/queries/verify.sql` (+ `internal/db/sqlc/verify.sql.go`) | lima query transaksi verifikasi |
| `api/internal/payment/verify.go` | domain verifikasi: `VerifyInput`, `VerificationService.MarkVerified`, aturan hasil/alasan/metode, teks timeline |
| `api/internal/payment/postgres/verify.go` | SATU transaksi verifikasi + audit + penjadwalan pekerjaan dokumen |
| `api/internal/platform/jobs/jobs.go` | muatan job bersama produsen/konsumen (`RenderDokumenArgs`) |
| `api/internal/fulfillment/` | rantai Done → dokumen → email (dipakai worker) |
| `api/internal/worker/worker.go` | `RenderDokumenWorker` + kind job di River |
| `api/internal/notification` | `Store.EnqueueOnce` + `Service.EnqueueOrderEmailSekali` (idempoten) |
| `api/internal/httpapi/handlers_verify.go` + router + `errors.go` + `deps.go` | endpoint `POST /api/v1/admin/orders/{id}/verify` |
| `openapi/openapi.yaml` | path + skema `VerifyResult`/`VerifiedOrder` (+ dua kode error baru di enum `ErrorEnvelope`) |
| `cmd/server/main.go` | wiring mode `api` (klien River insert-only) dan mode `worker` |
| `docker-compose.yml`, `README.md` | worker memakai object storage yang sama (`S3_*`) + dokumentasi F1-18 |

Endpoint:

| Method | Path | Peran | Catatan |
|---|---|---|---|
| POST | `/api/v1/admin/orders/{id}/verify` | Manager & Superadmin (`admin.verify`) | `hasil: cocok` → Done; `hasil: perlu_review` → tetap Submitted |

Tidak ada migrasi baru: seluruh kolom yang dibutuhkan sudah ada di
Database.md §4.4–4.6 (`status`, `verif_status`, `done_at`, `alasan_review`,
`verification_logs`).

## Keputusan desain

1. **Satu jalur transisi untuk semua cara verifikasi.** `payment.VerificationService.MarkVerified`
   adalah satu-satunya pintu Done (SystemDesign.md §4.3). Endpoint admin
   memanggilnya dengan `metode = manual`; kelak OCR/AI (F2-24) dan callback
   Virtual Account memanggil method yang sama dengan `ai` /
   `virtual_account`. Efek sampingnya (status + `done_at`, log, timeline,
   audit, penjadwalan dokumen/email) jadi identik, tidak peduli siapa yang
   memutuskan. `metode` **tidak** diterima dari klien — permintaan HTTP tidak
   bisa mengaku sebagai AI.
2. **Satu transaksi, kunci baris pesanan.** Repository melakukan
   `LockOrderForVerify … FOR UPDATE` → periksa status → tulis transisi →
   `verification_logs` → `order_events` → `audit_logs` → **titipkan job River**,
   lalu commit. Karena keputusan dibaca dari baris yang dikunci, dua
   verifikasi bersamaan berbaris rapi: satu menang, sisanya dijawab
   `409 ORDER_ALREADY_DONE` **tanpa** menulis log/dokumen/email kedua. Bukti:
   `TestPGVerifikasiBersamaanTepatSekali` (8 permintaan serentak → 1 sukses,
   7 konflik, 1 log, 1 audit, 1 job, dan tetap satu set dokumen/email).
3. **Verifikasi kedua dijawab 409, bukan 200 idempoten.** Pesanan yang sudah
   `done` tidak punya "hasil verifikasi" yang sama untuk dikembalikan (nominal
   yang dibaca dua admin bisa berbeda), jadi jalur yang jujur adalah menolak
   jelas-jelas. Yang penting dan diuji: percobaan kedua **tidak menghasilkan
   efek samping apa pun** (tidak ada dokumen/email/log/audit kedua). Sama
   dengan pola submit `409 ORDER_LOCKED` di kartu F1-14.
4. **`perlu_review` adalah sub-status, bukan status (PRD §7.1).** Keputusan
   review `200` dan pesanan **tetap Submitted**; yang berubah hanya
   `verif_status = perlu_review` + `alasan_review`. Ia tidak melahirkan
   dokumen, email, maupun job render. Admin boleh menimpanya nanti dengan
   persetujuan (PRD §5: keputusan akhir selalu milik manusia), dan setiap
   keputusan meninggalkan satu baris `verification_logs` — log adalah histori,
   bukan state.
5. **Alasan tertutup dan wajib.** `perlu_review` wajib membawa `alasan`
   (`kurang_bayar`, `lebih_bayar`, `tidak_terbaca`, `rekening_beda`,
   `lainnya`); `cocok` **tidak boleh** membawa alasan (alasan yang tertinggal
   dari pilihan sebelumnya tidak tersimpan diam-diam). Ditegakkan di domain
   (`ValidasiAlasan`), bukan hanya di UI.
6. **`total_terbaca` adalah angka yang dibaca/diisi admin.** Kolom
   `verification_logs.total_terbaca` menyimpan nominal itu dan `selisih`
   dihitung kolom **generated** database (`total_terbaca − total_pesanan`),
   jadi angkanya tidak pernah berasal dari perhitungan klien. `total_pesanan`
   dibaca repository dari baris yang dikunci. Kosong = tidak ada nominal
   terbaca (tersimpan `NULL`). Verifikasi bukti non-OCR memang menuntut admin
   mengisi nominalnya sendiri; kolom OCR (`payment_proofs.ocr_*`) baru dipakai
   Fase 2 lewat method yang sama.
7. **Approval tidak dipaksakan selisih nol.** PRD §5 menyerahkan keputusan
   akhir kepada manusia; toleransi (`ai.tol_kurang`/`tol_lebih`) adalah aturan
   AI Fase 2. Manual: admin melihat total, nominal terbaca, dan selisih, lalu
   memutuskan — angka selisihnya tetap tercatat untuk audit.
8. **Efek samping dijalankan pekerjaan worker, bukan request HTTP.**
   Transaksi verifikasi menitipkan job `render_dokumen_pesanan`
   (`platform/jobs`) ke tabel `river_job` **pada transaksi yang sama**, jadi
   tidak mungkin ada pesanan selesai tanpa pekerjaan dokumennya (pola
   SystemDesign.md §4.2/§5.1). Worker-lah yang merender Invoice/Surat Jalan
   lalu mengantre email — **email baru masuk antrean setelah dokumennya ada**,
   karena lampirannya dibaca dari object storage saat pengiriman
   (`notification.ErrDocumentNotReady`). Bila mesin PDF/storage belum siap,
   job gagal dan diulang River; tidak pernah ada email tanpa lampiran. Bukti:
   `TestPGPekerjaanMenungguDokumenSebelumEmail`.
9. **Pekerjaan wajib idempoten.** River bersifat *at-least-once*: job yang
   sama bisa dijalankan ulang (mis. proses mati setelah dokumen dirender).
   Render dokumen sudah idempoten lewat `documents(unique order_id, jenis,
   versi)` + kunci storage ber-versi (F1-17); jalur baru di kartu ini adalah
   pengantrean email: `notification.EnqueueOrderEmailSekali` memakai
   `Store.EnqueueOnce` yang memeriksa + menyimpan dalam **satu transaksi**
   dengan `pg_advisory_xact_lock(hashtext(order_id), hashtext(jenis))` — pola
   yang sama dengan kirim ulang email F1-16. Baris yang sudah ada (termasuk
   yang `failed`) dianggap cukup: mengantre ulang email yang pernah dibuat
   adalah tindakan admin eksplisit (`ResendOrder`), bukan efek samping worker.
   Bukti: pekerjaan dijalankan dua kali → tetap 3 dokumen + 3 email.
10. **Penerima yang belum berflag bukan kegagalan pekerjaan.**
    `notification.ErrNoRecipient` (mis. belum ada admin ber-flag invoice)
    dilewati dan dilaporkan di `fulfillment.Hasil`, bukan dikembalikan sebagai
    error — mengulang pekerjaan tidak akan memunculkan penerima. Jenis lain
    tetap diproses.
11. **Fail closed bila penjadwal pekerjaan belum terpasang.** Tanpa klien River
    (mis. konfigurasi salah), `MarkVerified` menolak dengan
    `ErrQueueUnavailable` → `503` alih-alih menyelesaikan pesanan tanpa
    dokumen dan email. Sama semangatnya dengan storage/renderer `Disabled`.
12. **Keputusan approve juga didahulukan sebagai gerbang.** `done_at` diisi di
    dalam query `SetOrderDone` (constraint `done_punya_waktu` menuntut status
    `done` selalu punya `done_at`), dan klausa `WHERE status = 'submitted'` di
    query itu adalah penjaga kedua setelah kunci baris.
13. **Catatan internal tetap internal.** Kartu ini tidak menyentuh
    `orders.note_admin`. Ia menambah baris `order_events` baru bertipe admin
    ("Pembayaran diverifikasi …" / "Pembayaran ditandai perlu review …") —
    mengikuti catatan F1-15: **bila kelak ada timeline yang tampil ke cabang,
    query-nya wajib mengecualikan event admin** (catatan admin, keputusan
    verifikasi, revisi invoice). Portal cabang pada kartu ini tidak menerima
    satu pun data verifikasi.

## Bukti (perintah + hasil nyata)

Lingkungan: PostgreSQL 16.15 lokal (`grahita_f118` + `scripts/pg-dev-shim.sql`),
Go 1.27, sqlc v1.31.1, seluruh uji dijalankan pada salinan kerja
`/home/hermes/projects/grahita-website`.

Gerbang (semua dijalankan pada salinan kerja repo, database uji
`grahita_f118` + `scripts/pg-dev-shim.sql` + ekstensi di `public`):

| Perintah | Hasil |
|---|---|
| `gofmt -l .` | kosong (`gofmt_exit=0`) |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | 0 diff (hanya berkas baru kartu ini yang muncul di `git status`) |
| `./scripts/check-secrets.sh` | exit 0 ("Tidak ada rahasia terdeteksi") |
| `npx @redocly/cli@1 lint openapi/openapi.yaml` | valid, 10 warning (baseline) |
| `go test -race -p 1 -count=1 ./...` | **20 paket ok, 0 FAIL, SUITE_EXIT=0** (`f118-gates2.log`; httpapi 392.8s) |

Catatan lingkungan: `go test ./...` tanpa `-p 1` pernah menjalankan
`internal/platform/database` (uji migrasi pada skema `public`) bersamaan dengan
fixture lain; karena itu seluruh gerbang memakai `-p 1` seperti kartu lain.

Uji fokus kartu:

```
go test ./internal/payment/... ./internal/fulfillment/... ./internal/notification/... -count=1
ok  github.com/grahita/portal/api/internal/payment          (11 uji: otorisasi, bentuk id, hasil/alasan, nominal, catatan, metode, audit, error penyimpanan, penyimpan belum siap, label/teks)
ok  github.com/grahita/portal/api/internal/fulfillment      (8 uji: urutan dokumen→email, idempoten, gagal render, dokumen kosong, penerima tak berhak, kegagalan lain, masukan tak lengkap)
ok  github.com/grahita/portal/api/internal/notification     (EnqueueOrderEmailSekaliTidakMenggandakan)

go test ./internal/httpapi/ -run 'TestPGVerifikasi|TestPGCnC|TestPGPekerjaan' -count=1 -v
--- PASS: TestPGVerifikasiSetujuMenghasilkanDokumenDanEmailSekali
--- PASS: TestPGVerifikasiPerluReviewTidakMenyelesaikan
--- PASS: TestPGVerifikasiBentukDataDitolak
--- PASS: TestPGVerifikasiBersamaanTepatSekali
--- PASS: TestPGVerifikasiOtorisasiDanBentukId
--- PASS: TestPGPekerjaanMenungguDokumenSebelumEmail
--- PASS: TestPGVerifikasiTanpaPenjadwalDitolak
--- PASS: TestPGCnCTanpaBuktiMenungguVerifikasi
```

Kriteria kartu → bukti:

| Kriteria | Bukti |
|---|---|
| approve menghasilkan satu set Invoice + Surat Jalan + email sesuai flag | `TestPGVerifikasiSetujuMenghasilkanDokumenDanEmailSekali`: 3 baris `documents` + 3 objek di storage + 3 baris `email_outbox` (`invoice_cabang` → `branches.email`, `invoice_admin`/`surat_jalan_admin` → admin ber-flag) |
| review tidak Done | `TestPGVerifikasiPerluReviewTidakMenyelesaikan`: `submitted/perlu_review`, `done_at` NULL, tanpa dokumen/email/job |
| C&C tanpa bukti boleh submit, baru Done setelah verifikasi | `TestPGCnCTanpaBuktiMenungguVerifikasi`: submit C&C tanpa berkas `200`; jatuh tempo dilewatkan → tetap `submitted` dan muncul di filter `terlambat=true`; sesudah verify → `done` dan keluar dari filter itu |
| dua verify serentak idempoten / tidak duplikasi | `TestPGVerifikasiBersamaanTepatSekali`: 8 permintaan → 1×200, 7×409, 1 log, 1 audit, 1 job, satu set dokumen + email |
| Manager boleh verify | `TestPGVerifikasiOtorisasiDanBentukId` (manager `200`; cabang `403`; id non-UUID `404`; draft `409 ORDER_NOT_SUBMITTED`; verify kedua `409 ORDER_ALREADY_DONE`) |
| worker menunggu dokumen sebelum email | `TestPGPekerjaanMenungguDokumenSebelumEmail` (renderer `Disabled` → job gagal, 0 dokumen, 0 email; sesudah siap → 3 + 3) |
| efek samping tepat sekali | audit/timeline/log masing-masing 1 baris pada kedua uji transisi; pengulangan pekerjaan tidak menambah dokumen/email |
| tidak ada pesanan Done tanpa pekerjaan dokumen | `TestPGVerifikasiTanpaPenjadwalDitolak` (repository tanpa `WithJobQueue` → `ErrQueueUnavailable`, pesanan tetap `submitted/belum`, tanpa log) |

## Batas bukti (tidak diklaim)

- **Gotenberg dan S3/MinIO nyata tidak dijalankan.** Uji integrasi memakai
  renderer uji (tanpa mesin PDF) + `storage.Memory`, sama seperti kartu F1-17.
  Karena itu *isi* PDF nyata dan lampiran email dari storage nyata belum
  dibuktikan di kartu ini (render + lampiran sudah diuji F1-17/F1-16).
- **PG18 asli + `golangci-lint`** tidak tersedia di sesi ini (PG16.15 +
  shim `uuidv7()`, gate lint tidak direproduksi) — sama dengan kartu lain.
- **UI belum dibuat.** Tombol **Verifikasi** (PRD `AP-ORD-07`) di halaman detail
  pesanan admin belum ada; kartu ini menyediakan kontrak API-nya
  (endpoint + skema OpenAPI + angka total/nominal/selisih di respons dan
  `AdminOrderDetail`). Wiring UI adalah kartu halaman pesanan/dashboard.
- **Kirim email end-to-end ke SMTP nyata** tidak dijalankan di sini (SMTP diuji
  di F1-16; lampiran dokumen pada binary sungguhan menunggu storage S3 F1-22).
- **`river_job` pada skema `public`** belum ada di database uji, jadi
  `internal/worker` (uji fondasi F1-01) tetap ter-skip seperti sebelumnya;
  tabel River dibuat per-skema uji oleh fixture F1-18.

## Kewajiban untuk kartu turunan

- **F2-24 (verifikasi AI/OCR)**: panggil `payment.VerificationService.MarkVerified`
  yang sama dengan `MetodeAI`, `TotalTerbaca` diisi dari Σ `payment_proofs.ocr_nominal`,
  dan alasan `kurang_bayar`/`lebih_bayar`/`rekening_beda`/`tidak_terbaca` dari
  aturan PRD §5. Jangan menulis status/`verification_logs` sendiri — seluruh
  efek samping (dokumen + email + audit) sudah ada di jalur ini.
- **F2-25 (revisi invoice)**: setelah `orders.versi` naik, pakai
  `document.EnsureInvoice` + email dengan `OrderEmailInput.Versi`; jangan pakai
  `EnqueueOrderEmailSekali` (jalur itu sengaja "sekali per pesanan per jenis").
- **F1-19/F1-20 (dashboard & detail admin)**: chip **perlu review** membaca
  `orders.verif_status = 'perlu_review'` (indeks `orders_status` sudah ada);
  tombol **Verifikasi** memakai `POST /admin/orders/{id}/verify` dan boleh
  memakai `GET /admin/orders/{id}` (memuat `verifikasi` + total) untuk
  menampilkan total vs nominal terbaca vs selisih.
- **F1-22 (rilis/smoke)**: `GOTENBERG_URL` + implementasi `storage.Store` S3
  harus terpasang di mode `api` **dan** `worker`, dan `--mode=migrate` harus
  sudah menjalankan migrasi River (tabel `river_job`) — tanpa itu, verifikasi
  gagal `503` atau pekerjaan dokumen tidak pernah jalan.
- **T4/T5 (E2E)**: uji end-to-end verifikasi → dokumen → email perlu mengambil
  berkas lewat URL presigned dan memverifikasi isinya, bukan hanya HTTP 200.

## Hotspot

- `openapi/openapi.yaml`, `api/internal/httpapi/router.go`,
  `api/internal/httpapi/errors.go`, `api/internal/notification/notification.go`
  (interface `Store` bertambah `EnqueueOnce` — implementasi lain di luar
  `notification/postgres` harus ikut menyediakannya),
  `api/internal/platform/pgtypes` (`Int8Ptr`),
  `api/internal/httpapi/integration_pg_test.go` (fixture bersama: kini juga
  menyiapkan tabel River + service verifikasi/pemenuhan).
