# F1-15 — Riwayat dan detail pesanan admin

Kartu: **t_beb6e7c9 — F1-15 Riwayat dan detail pesanan admin**

Dokumen acuan: PRD §4.2 (AP-ORD-01..05), §2.1 (matriks hak akses), §7;
SystemDesign §2, §5.4, §6, §7.1–7.2; Database.md §4.4–4.6, §7.2.
Prototype acuan UI: `docs/prototype/admin_v2.html` (`pPesanan`, `pDetail`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend — domain `internal/order` (bagian admin, berkas baru `admin.go`):

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/api/v1/admin/orders` | Manager & Superadmin | pencarian + filter + grouping + paginasi server |
| GET | `/api/v1/admin/orders/{id}` | Manager & Superadmin | detail lengkap (header, baris+serial, timeline, dokumen, log verifikasi) |
| POST | `/api/v1/admin/orders/{id}/notes` | Manager & Superadmin | catatan internal (PRD AP-ORD-05) |

Frontend portal admin (`grahita-admin.tiusclaw.com`, endpoint tetap
`/api/v1/admin`):

- `/pesanan` — riwayat seluruh cabang: cari nomor/cabang/serial, filter cabang,
  status, verifikasi, C&C, lewat jatuh tempo, email gagal, rentang tanggal;
  grouping tanpa/per cabang/per hari/per bulan/per tahun; paginasi server;
  seluruh filter di URL search param supaya bisa di-bookmark/di-share.
- `/pesanan/{id}` — header + data cabang, order line + serial, timeline,
  catatan internal (form), dokumen, log verifikasi, bukti transfer dengan
  **zoom**, dan riwayat email + tombol **Kirim ulang**.

## Keputusan desain

1. **Jalur admin dipisah dari jalur cabang, dan tidak dibatasi `branch_id`.**
   `internal/order/admin.go` memakai `AdminService` + `AdminRepository`
   tersendiri. Otorisasinya `auth.CapAdminOrders` — Manager **dan** Superadmin
   (PRD §2.1), sedangkan akun cabang dijawab `403`. Pemilihan cabang hanyalah
   salah satu **filter**, bukan isolasi; itu sebabnya klausa `WHERE`-nya tidak
   pernah menyertakan `branch_id` sesi (bukan kelalaian).
2. **Paginasi server dengan batas keras.** `halaman` (≥1) dan `per_halaman`
   (bawaan 25, dipagari 100) diterjemahkan ke `LIMIT/OFFSET`; `total` adalah
   jumlah SELURUH baris yang cocok filter dari query `count(*)` terpisah.
   Halaman tidak pernah menarik seluruh riwayat ke browser (PRD AP-ORD-03).
   Nilai `per_halaman` > 100 dipagari, bukan ditolak — UI tetap aman.
3. **Grouping = urutan + ringkasan agregat, bukan beberapa permintaan.**
   `grup` (tanpa|cabang|hari|bulan|tahun) mengubah `ORDER BY` daftar dan
   menambah array `ringkasan` (kunci, label, jumlah, Σ nilai) yang dihitung
   `GROUP BY` atas himpunan terfilter yang sama. Artinya angka grup konsisten
   dengan `total` di halaman mana pun, dan biayanya tetap satu query agregat.
   Urutan grup dibuat deterministik di service: per cabang A→Z, per periode
   terbaru dahulu.
4. **Tanggal bisnis selalu Asia/Jakarta.** Grouping hari/bulan/tahun dan
   penanda `terlambat` memakai
   `COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date)` dan
   `now() AT TIME ZONE 'Asia/Jakarta'`, jadi draft (tanpa tanggal) tetap
   tergolong pada hari bisnis yang benar dan C&C yang lewat tempo benar-benar
   terhitung (PRD §7.2, AP-DSH-03).
5. **Urutan daftar stabil.** `ORDER BY` ditutup `o.created_at DESC, o.id DESC`
   sehingga paginasi tidak pernah mengulang/melewatkan baris walau banyak
   pesanan bertanggal sama. Diuji: 3 halaman × 2 baris menutup 5 pesanan tanpa
   duplikat.
6. **Penanda "email gagal" lewat interface, bukan menembus tabel domain lain.**
   `order.SumberEmailGagal` (`PesananEmailGagal(ctx) ([]string, error)`)
   dipenuhi `notification.Service`; id pesanan ber-email `failed` diteruskan ke
   repository sebagai parameter `email_gagal_ids` (`o.id = ANY($n::uuid[])`),
   sehingga filter dan penanda memakai satu sumber kebenaran
   (`email_outbox.status='failed'`, Database.md §7.2) tanpa domain pesanan
   membaca tabel `email_outbox`. Tanpa sumber terpasang hasilnya kosong
   (fail closed), bukan seolah-olah semua pesanan normal.
7. **Catatan internal = kolom tunggal `orders.note_admin` + jejak timeline.**
   Database.md §4.4 hanya mendefinisikan satu kolom dan prototype hanya punya
   satu textarea "Note admin", jadi `POST .../notes` MENGGANTI isi catatan
   (bukan menambah baris baru), dan setiap penyimpanan menulis satu peristiwa
   `order_events` berisi aktor + isi catatan pada transaksi yang sama. Dengan
   begitu riwayat siapa-kapan-apa tetap bisa ditelusuri walau kolomnya tunggal.
   Catatan kosong/whitespace ditolak `400`; batas 2000 karakter.
8. **Catatan internal tidak mungkin bocor ke portal cabang.** Field-nya hidup
   di struct `AdminDetail` saja; `order.Order` (jalur cabang) tidak punya
   fieldnya, jadi `GET /api/v1/orders/{id}` tidak pernah memuatnya. Diuji di
   HTTP (kunci & isi) dan di PostgreSQL nyata untuk kedua portal.
9. **Detail tidak menduplikasi bukti & email.** `GET /admin/orders/{id}`
   mengembalikan bagian pesanan (header, baris+serial, timeline, dokumen, log
   verifikasi), sedangkan bukti transfer dan riwayat email tetap dibaca UI dari
   endpoint F1-13/F1-16 yang sudah ber-otorisasi sama (`CapAdminOrders`).
   Alasannya: satu respons tetap kecil, dan otorisasi bukti tetap di jalur yang
   sudah diuji — bukan direplikasi.
10. **Id bukan UUID dijawab `404`.** Daftar periksa `uuidRe` di service sebelum
    menyentuh database, sehingga `/admin/orders/tidak-ada` tidak berakhir
    `500` (`SQLSTATE 22P02`).
11. **Filter yang tidak dikenal DITOLAK `400`, bukan diabaikan.** Status,
    status verifikasi, mode grup, format tanggal, dan urutan rentang tanggal
    divalidasi service (`order.ErrAdminFilter` → `400 VALIDATION_FAILED` dengan
    kunci `filter`). Parameter boolean/numerik yang salah juga ditolak handler
    (`cnc=mungkin`, `halaman=abc` → `400`), sehingga salah ketik tidak membuat
    filter hilang tanpa terlihat.
12. **Baca-lintas-domain yang disengaja (read-only).** Detail menampilkan
    `documents` (invoice/surat jalan) dan `verification_logs`. Pemilik tabel
    itu — domain dokumen (F1-18/F2-25) dan domain verifikasi (F1-18) — belum
    ada, jadi `internal/order/postgres` membacanya langsung; **tidak ada satu
    pun query di berkas ini yang menulis ke tabel domain lain**. Kunci storage
    dokumen tidak pernah keluar dari repository. Saat domain dokumen lahir,
    pemindahan ke interface adalah pekerjaan sepele (satu query read-only) dan
    dicatat sebagai hotspot di bawah.
13. **Catatan admin tidak masuk `audit_logs`.** SystemDesign §5.4 membatasi
    audit pada mutasi admin atas cabang/harga/verifikasi/revisi/pengaturan;
    catatan internal tercatat di timeline pesanan (sama seperti keputusan
    F1-11 untuk mutasi draft cabang).

## Berkas yang berubah

Backend:

- `api/db/queries/admin_order.sql` (baru) + `api/internal/db/sqlc/admin_order.sql.go` (hasil sqlc)
- `api/internal/order/admin.go` (baru: tipe, `AdminRepository`, `AdminService`)
- `api/internal/order/postgres/admin.go` (baru: implementasi repository)
- `api/internal/notification/notification.go` (+`Store.PesananEmailGagal`, +`Service.PesananEmailGagal`)
- `api/internal/notification/postgres/postgres.go` (implementasi query)
- `api/internal/notification/notification_test.go` (fakeStore ikut kontrak baru)
- `api/internal/httpapi/handlers_admin_orders.go` (baru), `deps.go`, `router.go`, `errors.go`
- `api/cmd/server/main.go` (wiring `order.NewAdminService`)
- `openapi/openapi.yaml` (+3 path, +7 skema)
- Uji: `api/internal/order/admin_test.go`, `api/internal/httpapi/router_admin_orders_test.go`,
  `api/internal/httpapi/integration_admin_orders_pg_test.go` (baru),
  `api/internal/httpapi/integration_pg_test.go` (fixture menyertakan `AdminOrders`)

Frontend:

- `web/apps/admin/src/pages/pesanan.tsx` (baru: halaman daftar + detail)
- `web/apps/admin/src/pages/admin-pages.module.css` (+kelas F1-15)
- `web/apps/admin/src/routes.tsx` (`/pesanan` + validator search param, `/pesanan/$id`)
- `web/apps/admin/src/pages/shell-pages.tsx` (placeholder `pPesanan` F1-03 dihapus)
- `web/apps/admin/src/useApi.ts` (+`muatUlang`)
- `web/apps/admin/src/pesanan.test.tsx` (baru, 9 uji)

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

```
$ gofmt -l .                        -> (kosong)
$ go vet ./...                      -> exit 0
$ go build ./...                    -> exit 0
$ sqlc-v1.31.1 generate             -> tanpa diff (idempoten)
$ ./scripts/check-secrets.sh        -> "Tidak ada rahasia terdeteksi", exit 0
$ npx @redocly/cli@1 lint openapi/openapi.yaml
                                    -> "Your API description is valid. 🎉" (10 warning = baseline lama)
```

### Uji backend (PostgreSQL 16.15 nyata + `scripts/pg-dev-shim.sql`)

```
$ go test -race -p 1 -count=1 ./internal/order/... ./internal/notification/...
ok  github.com/grahita/portal/api/internal/order         1.709s
ok  github.com/grahita/portal/api/internal/notification  9.152s

$ go test -race -p 1 -count=1 ./internal/httpapi/
ok  github.com/grahita/portal/api/internal/httpapi       124.175s

$ go test -race -p 1 -count=1 ./...
(seluruh paket ok, 0 FAIL)
```

Uji yang ditambahkan kartu ini (12):

| Berkas | Jumlah | Cakupan |
|---|---|---|
| `internal/order/admin_test.go` | 6 | otorisasi (cabang 403, Manager & Superadmin boleh), normalisasi filter/paginasi/pagarnya, `email_gagal_ids` dari sumber (+fail closed & error), urutan ringkasan grup, id bukan UUID tanpa menyentuh DB, aturan catatan internal (kosong/terlalu panjang/aktor) |
| `internal/httpapi/router_admin_orders_test.go` | 3 | daftar + seluruh filter diteruskan handler, filter tidak valid → `400`, detail + catatan internal + CSRF + field tak dikenal ditolak + catatan tidak ada di respons cabang |
| `internal/httpapi/integration_admin_orders_pg_test.go` | 1 (besar) | di PostgreSQL nyata: seluruh cabang terlihat, paginasi 3 halaman tanpa duplikat, 13 kombinasi filter, grouping bulan & cabang (`ringkasan` konsisten dengan `total`), detail (baris+serial, timeline, dokumen, log verifikasi ber-nama admin), catatan internal tersimpan di `note_admin` + 1 entri timeline, **catatan tidak bocor ke portal cabang**, cabang 403 di daftar/detail/bukti, Superadmin boleh, id bukan UUID 404 |

### Uji & build frontend

```
$ npm run typecheck   -> exit 0 (api-client, ui, admin, customer)
$ npm test            -> admin 49 (9 di antaranya pesanan.test.tsx), api-client 8,
                         ui 64, customer 44 => 165 lulus, 0 gagal
$ npm run build       -> dua SPA sukses (dist/index.html + font & ikon lokal)
```

`pesanan.test.tsx` memverifikasi: daftar memakai paginasi server
(`per_halaman=25&halaman=1`), chip **C&C** mengirim `cnc=true`, form cari
mengirim `cari=bio`, tombol **Berikutnya** meminta `halaman=2`, grouping per
bulan menampilkan **Ringkasan grup**, detail menampilkan header + baris +
serial + timeline + catatan internal + bukti + riwayat email, **Simpan
catatan** mengirim `POST /admin/orders/{id}/notes` dengan body
`{catatan:"…"}` lalu memuat ulang detail, tombol **Zoom** mengambil tautan
unduh berumur pendek dan menampilkan pratinjau, dan **Kirim ulang** memanggil
`POST /admin/orders/{id}/emails/{jenis}/resend`.

### Probe binary sungguhan + PostgreSQL nyata

`api` hasil `go build --mode=api` di database bersih (migrate + seed), log
`cache/scratch/f115-probe.log`, skrip `cache/scratch/f115-probe.sh`:

| Permintaan | Hasil |
|---|---|
| `GET /healthz` | `200` |
| login Manager (`budi@…`) & cabang (`bandung@…`) | `200` / `200` |
| `GET /admin/orders` (Manager) | `200`, `total:1`, `nomor:"2026-10-01"`, `terlambat:true` |
| `GET /admin/orders?grup=bulan&per_halaman=1` | `200`, `ringkasan:[{"kunci":"2026-10","label":"","jumlah":1,"total":150000}]` |
| `GET /admin/orders?grup=kuartal` | `400 VALIDATION_FAILED` |
| `GET /admin/orders?status=batal` | `400 VALIDATION_FAILED` |
| `GET /admin/orders?email_gagal=true` | `200`, `total:0` (tidak ada email gagal) |
| `GET /admin/orders/{id}` (Manager) | `200`, `note_admin:""`, `serial_number:"20261001001"` |
| `POST /admin/orders/{id}/notes` `{"catatan":"internal pusat probe"}` | `200`, `note_admin` terisi; `SELECT note_admin` = `internal pusat probe` |
| `GET /orders/{id}` (cabang) | `200`, **tanpa** `note_admin` dan tanpa isi catatan |
| cabang → `/admin/orders`, `/admin/orders/{id}`, `.../proofs` | `403` / `403` / `403` |
| Manager → `/admin/orders/{id}/proofs` | `200` |
| `GET /admin/orders/bukan-uuid` | `404` (bukan `500`) |

## Batas kartu (bukan kelalaian)

- **Unduh/cetak Invoice & Surat Jalan dari detail (AP-ORD-09)** belum ada:
  dokumennya belum pernah ada sebelum verifikasi (F1-18) dan unduhannya milik
  domain dokumen. Detail sudah menampilkan daftar dokumen (siap diisi F1-18),
  sehingga tombol unduh menyusul bersama endpoint dokumen.
- **Verifikasi (`POST /admin/orders/{id}/verify`)** dan pengisian
  `verification_logs` adalah F1-18; kartu ini hanya MEMBACA lognya.
- **Revisi invoice (AP-REV-…)** milik F2-25.
- **Unggah bukti atas nama cabang dari halaman detail** memakai endpoint
  F1-13 yang sudah ada; kartu ini menampilkan tautan Zoom/unduh, bukan form
  unggah baru (AP-ORD-06 diselesaikan F1-13).
- **Nilai rupiah untuk Manager**: PRD §2.1 hanya membatasi nilai rupiah pada
  **dashboard** (F1-20). Riwayat pemesanan menampilkan total kepada Manager
  maupun Superadmin, persis prototype; pembatasan per-peran bila diminta kelak
  adalah perubahan tersendiri.

## Sisa risiko

1. `documents` dan `verification_logs` dibaca langsung dari
   `internal/order/postgres` selama domain pemiliknya belum ada (keputusan 12).
   Bacaannya read-only, tetapi bila F1-18 mengubah bentuk kolom, query ini ikut
   harus ditinjau.
2. Ringkasan grup dihitung ulang setiap permintaan. Untuk ratusan ribu pesanan
   ia agregat `GROUP BY` yang tetap terindeks lewat `orders_tanggal`, tetapi
   belum diuji pada volume 50.000 baris (beban ada di kartu F1-21/k6).
3. Pencarian `ILIKE '%…%'` pada nama cabang memakai indeks trigram
   (`branches_nama_trgm`); pencarian serial memakai `order_lines_serial` via
   `EXISTS`, tetapi pencarian nomor `ILIKE '%…%'` belum memakai indeks trigram
   `orders_nomor_trgm` (pola berawalan `%` tidak memakai btree) — untuk skala
   saat ini masih cepat, optimasi menyusul bila terbukti lambat.
4. Uji dijalankan pada PostgreSQL 16.15 + shim, bukan PG18 asli/CI
   `postgres:18-alpine` — sama seperti kartu F1-02..F1-14.
5. `golangci-lint` tidak tersedia di sesi ini (gate lint tidak direproduksi).

## Kewajiban untuk kartu turunan

- **F1-18 (verifikasi → Done)**: menulis `verification_logs` dan `documents`;
  detail F1-15 sudah menampilkannya apa adanya (`metode`, `total_pesanan`,
  `total_terbaca`, `selisih`, `hasil`, `alasan`, `catatan`, nama admin). Bila
  domain dokumen lahir, pindahkan pembacaan `documents`/`verification_logs`
  dari `internal/order/postgres/admin.go` ke interface domain itu.
- **F1-20 (dashboard & chip)**: chip "email gagal" sudah punya padanan filter
  `?email_gagal=true` di riwayat, dan chip "C&C lewat jatuh tempo" padanan
  `?terlambat=true`; klik chip dashboard sebaiknya menavigasi ke
  `/pesanan?…` dengan parameter itu agar angka chip dan daftar konsisten.
- **F2-28 (audit log)**: catatan internal sengaja TIDAK menulis `audit_logs`
  (keputusan 13); bila kebijakan berubah, tambahkan di transaksi `SetNoteAdmin`.
- **F1-22 (rilis)**: halaman `/pesanan` adalah permukaan admin pertama yang
  memanggil tiga endpoint sekaligus (detail + bukti + email) — perlu dicek pada
  smoke E2E bersama Caddy host admin.

## Hotspot

- `openapi/openapi.yaml`, `api/internal/order/postgres/`, dan
  `api/internal/httpapi/router.go` dipakai bersama kartu F1-18 (verify),
  F1-20 (dashboard), F2-25 (revisi), dan F1-16 (email); perubahan di sini
  aditif tetapi tetap perlu rebase hati-hati.
- `api/internal/notification/notification.go` (`Store` bertambah satu metode)
  menyentuh kontrak yang juga dipakai F1-16/F2-23 — implementasi baru
  (fakeStore, repository lain) wajib menambahkan `PesananEmailGagal`.
