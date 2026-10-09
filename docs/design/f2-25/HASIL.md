# F2-25 — Revisi invoice berversi dan audit perubahan

Kartu: **t_9a6d5b98 — F2-25 Revisi invoice berversi dan audit perubahan**

Dokumen acuan: `docs/PRD.md` §4.3 (AP-REV-01..05), §3.8 (riwayat invoice), §7
(BR-05/07/09/10, state machine §7.1), §10 (Fase 2); `docs/SystemDesign.md` §4.5
(alur revisi), §5.4 (audit), §6 (permukaan API), §8 (kunci dokumen);
`docs/Database.md` §4.6 (`invoice_revisions`, `documents`), §6 (penomoran),
§8 (integritas).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend (tanpa UI — antarmuka admin dipegang kartu lain):

| Method | Path | Peran | Catatan |
|---|---|---|---|
| POST | `/api/v1/admin/orders/{id}/revise` | Superadmin; Manager bila `manager_boleh_revisi` aktif | revisi invoice pesanan Done |

Perubahan berkas (repo `/home/hermes/projects/grahita-website`):

| Berkas | Isi |
|---|---|
| `api/db/queries/revise.sql` + `api/internal/db/sqlc/revise.sql.go` | query SQL + hasil `sqlc generate` |
| `api/internal/order/revise.go` | domain: `RevisionService`, `ReviseInput`, `ReviseOutcome`, sentinel error, validasi bentuk baris |
| `api/internal/order/postgres/revise.go` | transaksi revisi (kunci baris, snapshot, ganti baris, versi +1, total, timeline, audit, job) |
| `api/internal/order/postgres/postgres.go` | `Repo.jobs` + `WithJobQueue` |
| `api/internal/order/revise_test.go` | uji unit aturan revisi |
| `api/internal/httpapi/handlers_revise.go`, `router.go`, `errors.go`, `deps.go` | handler, rute `CapAdminRevise`, pemetaan error, `Deps.Revise` |
| `api/internal/platform/jobs/jobs.go` | kind job `revisi_invoice` |
| `api/internal/fulfillment/fulfillment.go` (+ `_test.go`) | `PesananDirevisi`: invoice saja + email invoice per versi |
| `api/internal/worker/worker.go` | `RevisiInvoiceWorker` + registrasi |
| `api/internal/notification/notification.go`, `notification/postgres/postgres.go` (+ `_test.go`) | `EnqueueOnceVersi` / `EnqueueOrderEmailSekaliVersi` (penjaga per (pesanan, jenis, versi)) |
| `api/internal/settings/settings.go` | `Service.Bool` (baca `manager_boleh_revisi`) |
| `api/internal/audit/audit.go` | `ActionRevise` |
| `api/cmd/server/main.go` | wiring `RevisionService` + job queue |
| `api/internal/httpapi/integration_pg_test.go` | fixture: repo pesanan + revisi |
| `api/internal/httpapi/integration_revise_pg_test.go` | 7 uji integrasi PostgreSQL nyata |
| `openapi/openapi.yaml` | endpoint + skema `ReviseLineInput`, `ReviseResult`, `RevisionRow` |

## Keputusan desain

1. **Kontrak input = daftar baris FINAL, bukan diff.** Satu permintaan berisi
   `alasan` (wajib), `catatan` (opsional), dan `lines` (final). Baris dengan
   `line_no > 0` adalah baris LAMA yang dipertahankan (qty/harga boleh berubah;
   serial, jenis, dan produknya TIDAK berubah). Baris tanpa `line_no` adalah
   baris produk BARU (`product_id` wajib). Baris lama yang tidak lagi disebut
   memang dihapus, tetapi nilainya tetap terekam di
   `invoice_revisions.snapshot`. Ini pola yang sama dengan editor draft F1-11
   ("daftar baris final, bukan diff") dan dipilih karena revisi harus
   menghasilkan versi yang utuh, bukan tambalan parsial.
2. **Nomor tetap, versi +1 dilakukan di SQL** (`UPDATE orders SET versi = versi
   + 1 … WHERE status='done'`) di dalam transaksi yang mengunci baris pesanan
   (`SELECT … FOR UPDATE`). Dua revisi bersamaan karena itu berbaris rapi:
   yang kedua membaca versi yang sudah dinaikkan. `UNIQUE (order_id,
   versi_baru)` di `invoice_revisions` menjadi benteng terakhir anti-duplikat.
3. **Serial lama tidak pernah dihitung ulang; serial baris baru memakai
   TANGGAL PESANAN ASLI.** Baris yang dipertahankan membawa serialnya apa
   adanya. Untuk baris baru, service memanggil tabel counter
   (`next_numbers('serial', <YYYYMMDD pesanan asli>, n)`) — bukan `SEQUENCE` —
   sehingga rollback tidak membuang nomor (BR-09) dan tidak ada serial/versi
   "palsu" yang tertinggal (SystemDesign.md §4.5).
4. **Total dihitung server** dari baris final (BR-10); `delta_total = total
   baru − total lama` disimpan di `invoice_revisions` dan di audit After.
5. **Aturan Sertifikat tetap terjaga:** pesanan Sertifikat tidak boleh
   dicampur produk reguler (dan sebaliknya), harga produk Sertifikat dipaksa 0
   (CP-SER-01), dan baris packaging disusun ulang server dengan biaya dari
   setelan `biaya_packaging` yang berlaku (CP-SER-02/05).
6. **Izin Manager dibaca server** lewat `settings.Bool("manager_boleh_revisi")`;
   Superadmin tidak pernah bergantung pada toggle. Mematikan setelan benar-benar
   menutup API (403), bukan sekadar menyembunyikan tombol. Guard rute memakai
   kapabilitas `admin.revise` yang sama dengan penjaga domain.
7. **Efek samping dititipkan pada transaksi yang sama** (pola F1-18): job River
   `revisi_invoice` ⇒ worker memastikan **hanya Invoice versi baru** dirender
   (`document.EnsureInvoice`) lalu mengantre **hanya** email
   `invoice_cabang` + `invoice_admin` (Surat Jalan TIDAK dikirim ulang,
   PRD AP-REV-04/§6.2). Bila penjadwal belum terpasang, revisi DITOLAK (503) —
   bukan menaikkan versi tanpa invoice baru.
8. **Idempotensi email per versi.** `email_outbox` tidak punya kolom versi
   (Database.md §4.7), jadi penjaga `EnqueueOnceVersi` memakai
   `COALESCE((payload->>'versi')::int, 0)` dengan advisory lock per (pesanan,
   jenis). Akibatnya revisi berikutnya (versi berbeda) tetap boleh melahirkan
   email baru, sementara pekerjaan worker yang dijalankan ulang (River
   at-least-once) untuk versi yang sama tidak menggandakannya. Penjaga lama
   `EnqueueOnce` (F1-18) tidak diubah supaya semantik transisi Done tetap sama.
9. **Audit + timeline.** `audit_logs` mencatat `entity='order'`,
   `action='revise'`, Before = snapshot versi lama, After = versi baru + total
   + `delta_total` + `alasan` (aktor/IP dari `audit.NewEntry`). `order_events`
   mencatat kalimat "Invoice direvisi oleh <aktor> — alasan: <alasan> (versi
   -R<n>)". Pembaca audit (UI Superadmin) adalah lingkup kartu F2-28.
10. **Berkas versi lama tidak pernah ditimpa.** Kunci storage
    `documents/{order}/{jenis}-{versi}.{ext}` memuat versi (F1-17), dan jalur
    render tidak pernah menghapus objek; revisi hanya menambah kunci versi
    baru. Uji integrasi membaca isi objek versi 0 sebelum/sesudah dan
    membuktikannya byte-identik.

## Bukti verifikasi (dijalankan di repo target)

Gerbang:

| Perintah | Hasil |
|---|---|
| `gofmt -l .` | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | 0 diff pada berkas generate lama (hanya `revise.sql.go` baru) |
| `./scripts/check-secrets.sh` | exit 0 — tidak ada rahasia |
| `redocly lint openapi/openapi.yaml` | valid, 10 warning (baseline sama dengan kartu sebelumnya) |
| `go test -race -p 1 -count=1 ./...` (TEST_DATABASE_URL) | semua paket ok, 0 FAIL |

Uji fokus:

- unit `./internal/order/` — aturan revisi (alasan wajib, otorisasi, toggle
  Manager, validasi baris, Superadmin mengabaikan toggle) PASS.
- unit `./internal/fulfillment/` — `PesananDirevisi` hanya invoice + email
  invoice, idempoten per versi, menahan email saat render gagal PASS.
- unit `./internal/notification/` — `EnqueueOrderEmailSekaliVersi` mengizinkan
  versi baru dan tidak menggandakan versi yang sama PASS.
- integrasi `./internal/httpapi/ -run TestPGRevisi` (PostgreSQL nyata) — 8 uji
  PASS:
  1. `TestPGRevisiMenaikkanVersiSimpanSnapshotDanInvoiceBaru` — versi 0→1,
     nomor tetap, total/delta benar di DB, snapshot + alasan + delta di
     `invoice_revisions`, serial lama tetap, serial baris baru bertanggal
     pesanan asli, timeline + audit `revise`, job `revisi_invoice` tertitip,
     invoice v1 memuat "Revisi v1", berkas v0 byte-identik, surat jalan v1 tidak
     dibuat, email invoice cabang+admin v1 tepat satu, surat jalan tidak
     dikirim ulang, pekerjaan ulang idempoten.
  2. `TestPGRevisiUnduhVersiLamaDanBaru` — daftar dokumen cabang memuat DUA
     versi invoice dan tautan unduh (presigned) keduanya 200.
  3. `TestPGRevisiManagerDitolakSaatToggleMati` — `manager_boleh_revisi=false`
     ⇒ Manager 403 (versi & `invoice_revisions` tak berubah); `true` ⇒ 200.
  4. `TestPGRevisiConcurrentBerurutan` — dua revisi bersamaan ⇒ keduanya 200,
     versi akhir 2, dua baris `invoice_revisions`.
  5. `TestPGRevisiRollbackTidakMenyisakanSerialVersiDanJob` — kegagalan di akhir
     transaksi (bentrok snapshot) ⇒ versi 0, total & baris & serial utuh,
     counter serial tidak terpakai, dokumen v0 utuh, tanpa job, tanpa timeline
     revisi.
  6. `TestPGRevisiMenolakPesananBelumDoneDanIdTakSah` — pesanan Submitted ⇒ 409
     `ORDER_NOT_DONE`; id bukan UUID ⇒ 404; tanpa CSRF ⇒ 403; akun cabang ⇒ 403.
  7. `TestPGRevisiSertifikatPertahankanPackagingDanHargaNol` — harga Sertifikat
     dipaksa 0, baris packaging tetap satu senilai setelan, pencampuran
     Sertifikat+reguler ⇒ 400 tanpa menaikkan versi.
  8. `TestPGRevisiPDFNyataVersiLamaDanBaru` — PDF SUNGGUHAN via tiruan protokol
     Gotenberg di atas Chromium lokal: invoice v0 byte-identik sesudah revisi,
     invoice v1 berupa PDF A4 sah (`%PDF-`, `/Type /Page`, `%%EOF`) yang
     BERBEDA dari v0, dan kedua versi tampil di daftar dokumen cabang.

## Batas bukti & risiko tersisa (non-pemblokir)

- Mesin PDF nyata diuji lewat tiruan protokol Gotenberg di atas Chromium lokal
  (uji 8); Gotenberg/MinIO/SMTP asli, PG18 asli, dan `golangci-lint` belum
  tersedia di sesi ini (sama seperti kartu sebelumnya). Sebagian uji integrasi
  lain memakai `rendererUji` + `storage.Memory`.
- Email dokumen diantre lewat worker River; pemicu sungguhan di produksi
  bergantung pada worker yang berjalan (`renders` idempoten).
- Perubahan `orderpg.New` menambah opsi `WithJobQueue`; pemanggil lama tanpa opsi
  tetap berperilaku sama (revisi menolak jalan tanpa penjadwal).
