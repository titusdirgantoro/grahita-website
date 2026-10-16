# F1-12 — Aturan Sertifikat, packaging dan Cash & Carry

Kartu: **t_d5636b31 — F1-12 Aturan Sertifikat, packaging dan Cash & Carry**

Dokumen acuan: PRD §3.6–3.7, §7, §4.4 (CP-KAT-04); SystemDesign.md §3–4.2;
Database.md §4.4, §6, §8. Prototype acuan UI: `docs/prototype/customer_v2.html`
(`pEditor` baris `.line.pack`, `pDetail` penanda C&C + jatuh tempo).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend:

| Bagian | Perubahan |
|---|---|
| `internal/order` | harga Sertifikat dipaksa 0; tepat satu baris `packaging` dari setelan; campuran ditolak; fungsi aturan C&C (`ValidateCNC`, `TanggalWIB`, `JatuhTempo`) |
| `internal/order/postgres` | query `InsertOrderLinePackaging` (tanpa produk, qty 1, tanpa serial) |
| `internal/settings` | `Int(ctx, key)` — pembacaan bilangan bulat yang berlaku (nilai tersimpan atau bawaan) untuk domain lain |
| `internal/branch` | `IsCNC(ctx, branchID)` — pembacaan lintas-domain flag cabang (bahan aturan C&C) |
| `internal/catalog` | `BranchProduct.BiayaPackaging` (catatan biaya packaging produk Sertifikat, CP-KAT-04) |
| `internal/httpapi` | katalog cabang menempelkan `biaya_packaging` dari setelan; pemetaan `ErrCNCNotAllowed → 400 VALIDATION_FAILED` |
| `internal/seed` | produk Sertifikat ikut dijual ke semua cabang (harga 0) supaya alurnya bisa dipakai di lingkungan contoh |

Frontend portal cabang: baris biaya packaging terkunci di editor (ikut total),
penjelasan konflik + nominal biaya, penanda C&C dan jatuh tempo di detail,
catatan biaya packaging di katalog.

**Batas dengan F1-14 (t_5b21b48b):** transaksi submit (nomor/serial, bukti,
status Draft→Submitted, `is_cnc` + `jatuh_tempo` akhir) tetap milik F1-14.
Kartu ini menyediakan aturannya; ia tidak mengklaim alur submit.

## Keputusan desain

1. **Harga Sertifikat dipaksa 0 di server, bukan hanya di data.** Domain katalog
   sudah menolak harga cabang Sertifikat ≠ 0 (F1-08), tetapi `order.buildLines`
   tetap menghitung `harga = 0` untuk setiap produk `jenis = 'sertifikat'`
   (CP-SER-01, BR-06). Dengan begitu data harga yang menyimpang pun tidak bisa
   membuat sertifikat berbayar — ada uji khusus dengan katalog palsu berharga
   7500. Subtotal otomatis 0 karena kolomnya `GENERATED ALWAYS` (`qty × harga`).
2. **Baris biaya packaging dibuat server, tepat satu, dari setelan.** Saat draft
   disimpan, `buildLines` menambahkan satu baris `jenis = 'packaging'`:
   `nama = "Biaya packaging"`, `qty = 1`, tanpa `product_id`,
   tanpa `serial_number`, `harga_satuan = settings.Int("biaya_packaging")`
   (CP-SER-02/05). Editor hanya mengirim `product_id` + `qty`, jadi klien tidak
   punya bentuk permintaan apa pun untuk menambah, mengubah, menghapus, atau
   menduplikasi baris itu: **setiap simpan draft menyusunnya ulang dari nol**.
   Permintaan yang mencoba menyisipkannya tetap ditolak `400` (baris tanpa
   produk / id karangan / field `jenis` & `harga_satuan` pada body).
3. **Nama baris packaging adalah snapshot teks.** Dibaca tidak berubah setelah
   tersimpan (`"Biaya packaging"`). Produk maupun baris packaging tidak lagi
   menyimpan satuan; `qty = 1` tetap menyatakan satu biaya per pesanan.
4. **Tepat satu per pesanan, bukan per baris Sertifikat.** Pesanan dengan dua
   produk Sertifikat berbeda tetap mendapat satu baris packaging (CP-SER-02:
   "per pesanan"). Ditegakkan juga di database lewat indeks unik parsial
   `order_lines_satu_packaging` — percobaan `INSERT` baris packaging kedua
   ditolak `23505` (dibuktikan di probe).
5. **Snapshot biaya = setelan yang berlaku saat baris disusun (BR-05).** Setiap
   simpan draft membaca `biaya_packaging` yang sedang berlaku, sedangkan
   transaksi submit (F1-14) wajib membaca ulang saat submit — persis seperti
   prototype (`l.harga = S.settings.packaging` pada `submitOrder`) dan
   `settings.go` ("harga/biaya di-snapshot ke order line saat submit").
   Konsekuensinya: **mengubah setelan tidak menyentuh baris yang sudah
   tersimpan** (probe: pesanan lama tetap 3000 setelah setelan diubah 5000),
   sementara pesanan berikutnya memakai 5000. Perilaku ini disengaja dan
   dibuktikan dua arah oleh uji + probe; draft sengaja bukan invoice.
6. **C&C hanya sah bila cabang SESI berflag `cash_and_carry` (CP-CNC-01).**
   Kartu ini menyediakan satu-satunya sumber aturannya:
   `ValidateCNC(branchIsCNC, requested)`, `TanggalWIB(t)`, `JatuhTempo(tanggal, cncHari)`.
   Flag dibaca server lewat `branch.Service.IsCNC` (pembacaan lintas-domain,
   bukan permukaan HTTP); F1-14 wajib memanggil `ValidateCNC` + `JatuhTempo`
   di transaksi submit, dan `ErrCNCNotAllowed` sudah dipetakan ke
   `400 VALIDATION_FAILED` ("Cash & Carry tidak tersedia untuk cabang ini.").
   Di permukaan draft **tidak ada field C&C sama sekali** (prototype menaruh
   centang C&C di modal kirim, bukan editor), jadi cabang tanpa flag tidak
   punya cara memalsukan C&C pada kartu ini: body `{"is_cnc":true,…}` maupun
   `{"cnc":true}` pada baris ditolak `400` (field tak dikenal), dan
   `/me/branch` melaporkan flag cabang sesi apa adanya (Bekasi `is_cnc:false`).
7. **Tanggal bisnis memakai WIB, bukan UTC.** `TanggalWIB` membulatkan ke
   tengah malam `Asia/Jakarta` (UTC+7, tanpa DST) sehingga submit 00:30 WIB
   tercatat pada hari yang benar (CP-ORD-06, BR-04). Uji menempuh 17:00 UTC
   (= besok di WIB), 16:59:59 UTC, 23:30 WIB, dan lintas bulan.
   `JatuhTempo(tanggal, cncHari)` = `tanggal + cnc_hari` hari (CP-CNC-04);
   `cnc_hari` juga datang dari setelan server (`settings.Int`).
8. **Snapshot biaya dibaca domain pesanan lewat `settings.Service.Int`, bukan
   `GET /api/v1/settings`.** Endpoint lintas-role itu sudah dihapus pada F1-09
   (404 untuk semua role). `Int` membaca nilai yang berlaku (tersimpan atau
   bawaan PRD §4.9: `biaya_packaging` 3000, `cnc_hari` 7) dan tidak membuka
   akses tulis apa pun — pengaturan tetap hanya milik Superadmin.
9. **Catatan biaya packaging untuk portal cabang (CP-KAT-04) diambil di lapisan
   transport.** Handler `/catalog` menempelkan `biaya_packaging` ke produk
   Sertifikat memakai setelan yang berlaku; katalog admin (daftar produk master)
   tidak berubah. Editor memakainya untuk menampilkan baris terkunci dan total
   sebelum disimpan, dan halaman `/katalog` menampilkan catatannya. Angka final
   tetap dihitung server saat simpan/submit.
10. **Seed lingkungan contoh menyertakan Sertifikat.** Sebelumnya seed hanya
    mengisi `branch_prices` untuk produk `reguler`, sehingga produk Sertifikat
    tidak punya baris harga cabang → tidak tampil di katalog dan tidak bisa
    dipesan sama sekali di lingkungan contoh. Kini semua produk (termasuk
    Sertifikat, `harga_default` 0 dijamin constraint `sertifikat_gratis`)
    dijual ke semua cabang.
11. **`is_sertifikat` tetap dihitung dari isi baris.** Di PostgreSQL trigger
    `order_lines_recalc` menetapkannya dari `products.jenis`; repository
    in-memory menirunya dari keberadaan baris `packaging` (baris packaging hanya
    lahir untuk pesanan Sertifikat). Tidak ada pengisian manual dari klien.
12. **Batas PRD yang belum diambil:** pesanan C&C lewat jatuh tempo tetap
    `Submitted` sampai admin memverifikasi (CP-CNC-07 — tidak ada jalur
    auto-`Done`; status `Done` hanya lewat verifikasi admin di F1-17/F1-19),
    bukti transfer opsional untuk C&C saat submit, dan penolakan pembatalan
    pesanan `Submitted` oleh cabang — ketiganya dijalur submit/verifikasi
    (F1-14, F1-13) dan tidak bisa diuji pada kartu ini; aturan yang bisa
    dipegang sekarang (harga 0, satu packaging, campuran, flag cabang, tenggat
    WIB) sudah ditegakkan dan diuji di sini.

## Berkas yang berubah

Backend:

- `api/internal/order/order.go` (aturan Sertifikat/packaging + fungsi C&C),
  `memory.go` (is_sertifikat dari baris), `postgres/postgres.go` (baris packaging)
- `api/db/queries/order.sql` + `api/internal/db/sqlc/order.sql.go` (hasil sqlc)
- `api/internal/settings/settings.go` (+`Int`), `api/internal/branch/branch.go` (+`IsCNC`)
- `api/internal/catalog/catalog.go` (+`BranchProduct.BiayaPackaging`)
- `api/internal/httpapi/handlers_catalog.go`, `errors.go`
- `api/internal/seed/seed.go` (Sertifikat ikut dijual)
- `api/cmd/server/main.go` (wiring `settings.Service` ke domain pesanan & katalog)
- `openapi/openapi.yaml` (OrderLine `jenis`/`product_id`/`harga_satuan`/`serial`,
  `BranchProduct.biaya_packaging`, deskripsi POST/PUT draft)
- Uji: `api/internal/order/order_rules_test.go` (baru), `order_test.go`,
  `settings/settings_test.go`, `branch/branch_test.go`,
  `httpapi/router_orders_test.go`, `httpapi/integration_orders_pg_test.go`,
  `httpapi/integration_pg_test.go`

Frontend:

- `web/apps/customer/src/pages/pesanan.tsx` (+`jatuh_tempo`, baris packaging
  terkunci, notice bernominal, `tanggalWIB`), `pesanan.module.css` (`.linePack`)
- `web/apps/customer/src/pages/shell-pages.tsx` (catatan biaya di katalog)
- Uji: `web/apps/customer/src/pesanan.test.tsx` (+3), `App.test.tsx`

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

```
$ gofmt -l .                       -> (kosong)
$ go vet ./...                     -> exit 0
$ go build ./...                   -> exit 0
$ sqlc-v1.31.1 generate            -> tidak ada diff tambahan (hanya query baru)
$ ./scripts/check-secrets.sh       -> "Tidak ada rahasia terdeteksi", exit 0
$ redocly lint openapi/openapi.yaml-> "Your API description is valid. 🎉 You have 10 warnings."
                                      (10 warning = baseline gaya lama, sama seperti F1-09)
```

### Uji backend (PostgreSQL 16.15 nyata + `scripts/pg-dev-shim.sql`)

```
$ TEST_DATABASE_URL=postgres://…:5455/grahita_test?sslmode=disable \
  go test -race -p 1 -count=1 ./...
ok  github.com/grahita/portal/api/internal/order     1.810s
ok  github.com/grahita/portal/api/internal/branch    1.727s
ok  github.com/grahita/portal/api/internal/settings  1.813s
ok  github.com/grahita/portal/api/internal/httpapi  93.659s
ok  ... (seluruh paket lain ok; EXIT 0, 0 FAIL, 0 SKIP)
```

Uji baru kartu ini:

| Berkas | Uji | Cakupan |
|---|---|---|
| `internal/order/order_rules_test.go` | 8 | tepat satu packaging qty 1 harga 3000 + tanpa produk/serial, harga Sertifikat selalu 0 (walau data harga menyimpang), snapshot biaya & perubahan setelan dua arah, klien tak bisa menyisipkan baris packaging, satu packaging untuk beberapa produk Sertifikat, `ValidateCNC` 4 kasus, `TanggalWIB` lintas tengah malam/bulan + `JatuhTempo` |
| `internal/httpapi/router_orders_test.go` | 3 | lewat HTTP: draft Sertifikat 2 baris (produk 0 + packaging 3000), ubah draft tak menggandakan packaging, pesanan reguler tanpa packaging; body penyisipan packaging/harga 400 + 0 pesanan tercipta; `/catalog` membawa `biaya_packaging` 3000 hanya untuk Sertifikat |
| `internal/httpapi/integration_orders_pg_test.go` | 1 | di PostgreSQL: baris Sertifikat harga 0 tanpa serial, baris packaging tanpa `product_id`/serial, indeks unik + trigger menolak packaging kedua & pencampuran, setelan 3000→5000 tidak mengubah pesanan lama, pesanan baru 5000, campuran lewat HTTP 400 |
| `internal/settings/settings_test.go` | 1 | `Int`: nilai tersimpan menang, bawaan PRD dipakai bila belum disimpan, kunci non-bulat ditolak |
| `internal/branch/branch_test.go` | 1 | `IsCNC` per cabang + cabang tak dikenal/`branch_id` kosong `ErrNotFound` |

### Uji & build frontend

```
$ npm run typecheck   -> exit 0 (api-client, ui, admin, customer)
$ npm test            -> admin 29, api-client 7, customer 23, ui 56 => 115 lulus
```

Uji baru: editor menampilkan `data-testid="baris-packaging"` tanpa tombol/hapus
saat Sertifikat dipilih dan totalnya ikut 3.000; pesanan reguler tanpa baris itu;
detail C&C menampilkan penanda "Cash & Carry", "Sertifikat", "Jatuh tempo C&C"
15 Okt 2026 dan baris packaging; katalog menampilkan "Biaya packaging Rp3.000
ditambahkan otomatis per pesanan.".

### Probe HTTP pada binary sungguhan + PostgreSQL (bukti eksekusi)

Binary `--mode=api` (DB baru `grahita_f112probe`, migrasi 0001..0004 + seed,
port 8097, login cabang Bandung (C&C) dan Superadmin), hasil nyata:

| Permintaan | Hasil |
|---|---|
| `GET /me/branch` (Bandung) | `is_cnc: true` |
| `GET /me/branch` (Bekasi) | `is_cnc: false` |
| `GET /catalog` | produk `Sertifikat Psikotes` `harga: 0` + `biaya_packaging: 3000` |
| `POST /orders` Sertifikat qty 25 | `201`: `is_sertifikat true`, `total 3000`, `jumlah_item 25`, 2 baris (produk 0/0, packaging 1×3000) |
| DB `order_lines` | produk: `harga 0`, subtotal 0, `serial_number NULL`; packaging: `harga 3000`, `product_id NULL`, `serial NULL` |
| `POST /orders` Sertifikat + reguler | `400 VALIDATION_FAILED` |
| `POST /orders` baris `product_id:""` / `"jenis":"packaging"` / `"harga_satuan"` | `400` (tiga-tiganya) |
| `POST /orders` body `{"is_cnc":true,…}` & baris `{"cnc":true}` (cabang non-C&C) | `400` (tidak ada permukaan C&C di draft) |
| SQL: `INSERT` baris packaging kedua | ditolak `duplicate key … order_lines_satu_packaging` |
| SQL: `INSERT` baris produk reguler ke pesanan Sertifikat | ditolak `pesanan sertifikat tidak boleh dicampur produk reguler (CP-SER-03)` |
| `PUT /admin/settings` `biaya_packaging 3000→5000` (Superadmin) | `200` |
| `GET /orders/{id}` pesanan lama | `total 3000`, packaging tetap `3000` (DB juga 3000) |
| `POST /orders` Sertifikat berikutnya | `total 5000`, packaging `5000` |
| `PUT /orders/{id}` (editor kirim ulang daftar baris) | tetap **2** baris, `total 5000`, packaging 1×5000 (= setelan yang berlaku saat simpan) |
| `GET /orders` | `is_sertifikat true` untuk pesanan Sertifikat |

## Catatan & risiko sisa (non-pemblokir)

- **Snapshot draft vs submit.** Baris packaging draft di-snapshot ulang setiap
  kali draft disimpan (keputusan 5). QA F1-14 harus membaca ulang
  `biaya_packaging` + `cnc_hari` **di dalam transaksi submit** memakai
  `settings.Int`, dan memanggil `ValidateCNC`/`TanggalWIB`/`JatuhTempo` supaya
  nilai final pesanan benar.
- **`cnc_hari` sudah bisa dibaca server** (`settings.Service.Int`, teruji) tetapi
  belum dipakai jalur HTTP mana pun di kartu ini — pemakaiannya di submit
  (F1-14). Demikian pula `ErrCNCNotAllowed` baru terpakai begitu F1-14
  memanggil `ValidateCNC`.
- **Bukti transfer opsional C&C, overdue tetap `Submitted`, dan cabang tidak
  bisa membatalkan `Submitted`** tidak punya permukaan pada kartu ini (submit &
  verifikasi = F1-14/F1-13/F1-17). Yang dipegang sekarang: status enum tanpa
  `batal`, penguncian non-draft di service (F1-11), dan tidak ada jalur
  auto-`Done`.
- **`biaya_packaging` di `/catalog` hanya untuk akun cabang** (katalog admin
  memakai daftar produk master). Bila kelak admin butuh catatan itu, pakai
  `/admin/settings`, bukan katalog.
- **Uji dijalankan pada PG16.15 + shim**, bukan PG18/CI `postgres:18-alpine`
  (sama seperti F1-02..F1-11). CI "migrasi" tetap sumber kebenaran versi asli.
- **Probe UI di browser sungguhan belum dijalankan** untuk kartu ini (editor
  dengan API+PG nyata); bukti UI berasal dari uji DOM vitest + probe HTTP.
  Jika QA ingin bukti browser, jalankan `npm run dev:customer` dengan API pada
  `:8088` (proxy dev) dan pilih produk Sertifikat di `/pesanan/baru`.

## Sisa untuk kartu lain (tidak dikerjakan di sini)

- F1-14 (t_5b21b48b): transaksi submit, `is_cnc` + `jatuh_tempo` (memakai
  `ValidateCNC`/`TanggalWIB`/`JatuhTempo` + `settings.Int`), bukti ≥1 kecuali
  C&C, penomoran/serial, snapshot harga terbaru, model UI modal kirim + centang
  C&C hanya bila cabang berflag.
- F1-13/F1-15/F1-19: daftar/dashboard admin (penanda C&C, chip lewat jatuh
  tempo), riwayat & invoice.
