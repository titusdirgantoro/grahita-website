# F1-13 — Penyimpanan dan unggah bukti transfer

Kartu: **t_f2244a1b — F1-13 Penyimpanan dan unggah bukti transfer**

Dokumen acuan: PRD §3.5 (CP-ORD-04/07/08), §4.2 (AP-ORD-04/06), §4.9 (setelan
`max_file`/`max_mb`), §7.2 (unggah: pratinjau/zoom di klien), §8 no.8 (retensi);
Architecture.md §1 (object storage S3-compatible), §5.1 (MinIO), §6 (keamanan:
presigned URL, isolasi data cabang); SystemDesign.md §5.3 (idempotensi unggah),
§6 (permukaan API), §8 (kunci dokumen + presigned URL); Database.md §4.5
(`payment_proofs`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend:

| Bagian | Perubahan |
|---|---|
| `internal/payment` (baru) | domain bukti transfer: validasi magic bytes + ekstensi, batas jumlah/ukuran dari setelan, kunci acak, SHA-256 unik per pesanan, otorisasi cabang/admin, tautan unduh berumur pendek, pencatatan timeline, repository in-memory untuk pengujian |
| `internal/payment/postgres` (baru) | repository nyata (metadata bukti) sekaligus `OrderSource` (konteks pemilik+status pesanan) dan `Timeline` (order_events) |
| `internal/platform/storage/s3.go` (baru) | implementasi `Store` S3-compatible dengan aws-sdk-go-v2: Put/Get/Delete/Health + presigned GET dan presigned *download* (Content-Disposition attachment) |
| `internal/platform/storage/storage.go` | `Memory.PresignDownload` (kontrak unduh untuk pengujian/mode lokal) |
| `internal/httpapi` | 6 endpoint bukti (cabang & admin), pembatas laju unggah, pemetaan error domain → HTTP (`PROOF_DUPLICATE`, `PROOF_LIMIT`, `PAYLOAD_TOO_LARGE`, `ORDER_LOCKED`, `UNAVAILABLE`) |
| `internal/platform/apierr` | `WithFields` untuk tabel keputusan status/kode di lapisan transport |
| `cmd/server` | `buildStorage` (S3 dari environment, gagal terang-terangan bila konfigurasi setengah), wiring `payment.Service` + `ProofLimiter` |
| `db/queries/payment.sql` + `internal/db/sqlc` | 5 query (hitung, insert, list, get, konteks pesanan, event timeline) — hasil `sqlc-v1.31.1` |
| `openapi/openapi.yaml` | 6 path baru + skema `PaymentProof`/`PaymentProofDownload` + respons `PayloadTooLarge`/`Unavailable` + parameter `OrderIdPath` |

Frontend portal cabang:

| Bagian | Perubahan |
|---|---|
| `packages/api-client` | `postForm` (multipart) — Content-Type TIDAK disetel manual supaya boundary sah; CSRF tetap dikirim |
| `apps/customer/src/pages/pesanan.tsx` | kartu **Bukti transfer**: daftar bukti + penanda pengunggah, pratinjau/zoom lewat tautan unduh berumur pendek, form unggah multi-berkas untuk pesanan `submitted` |
| `apps/customer/src/pages/pesanan.module.css` | gaya unggah + pratinjau/zoom |

**Batas dengan kartu lain:** transaksi submit (`POST /orders/{id}/submit`,
nomor/serial, status Draft→Submitted, bukti ≥1 kecuali C&C) tetap milik F1-14;
verifikasi pembayaran (Setujui → Done, alasan review) milik F1-17/F1-19; OCR
Claude vision + antrean review milik F2-26. Kartu ini menyediakan penyimpanan,
validasi, ACL, dan tautan unduh yang dipakai keduanya, dan SENGAJA tidak
mengubah status pesanan.

## Keputusan desain

1. **Isi berkas tidak pernah masuk database.** Yang tersimpan hanya
   `storage_key`, `nama_file`, `mime_type`, `ukuran_byte`, `sha256`, dan
   pengunggah (Database.md §4.5). Isinya masuk object storage dengan kunci
   **acak**: `proofs/{order_id}/{16 byte acak}.{ext}`. Nama berkas kiriman
   klien hanya menjadi metadata tampilan (dibersihkan dari komponen direktori
   dan karakter kendali, maks 120 rune) — sehingga tidak ada cara menebak atau
   mengiterasi berkas, dan nama berbahaya tidak bisa menyuntik header.
2. **Jenis berkas ditentukan MAGIC BYTES, bukan nama/Content-Type klien.**
   Hanya JPEG (`FF D8 FF`), PNG (`89 50 4E 47 0D 0A 1A 0A`), dan PDF (`%PDF-`)
   yang diterima (CP-ORD-04). Ekstensi nama berkas WAJIB konsisten dengan isi:
   `.pdf` berisi JPEG, `.jpg` berisi PDF, `.jpg` berisi skrip, atau ekstensi di
   luar daftar semuanya ditolak `400` dengan field `file`. Ini yang dimaksud
   "ekstensi palsu ditolak" pada kartu.
3. **Batas jumlah/ukuran datang dari SETELAN SERVER** (`settings.Int`), bukan
   dari klien maupun konstanta: `max_file` (default 5 berkas per pesanan) dan
   `max_mb` (default 5 MB per berkas). Badan permintaan multipart dibatasi
   `max_file × max_mb + 1 MB` di lapisan transport supaya tidak ada unggahan
   raksasa yang menahan memori/disk sebelum validasi. Batas laju unggah 30
   permintaan / 15 menit **per akun** (`429 RATE_LIMITED`).
4. **Satu permintaan unggah = satu unit kerja.** Seluruh berkas divalidasi
   lebih dulu dan dipra-cek terhadap sidik jari yang sudah tersimpan; isinya
   baru ditulis ke object storage setelah semuanya lolos, lalu seluruh metadata
   **dan peristiwa timeline** masuk dalam SATU transaksi database (dengan baris
   pesanan dikunci `FOR UPDATE`) — bukan lagi satu tulisan per berkas. Bila ada
   satu berkas ditolak, tidak ada baris/peristiwa yang tersisa dan **semua**
   objek yang sempat ditulis dihapus lagi (bukan hanya yang terakhir). Ini
   menutup temuan QA putaran 1: dulu permintaan berisi `[berkas baru, berkas
   duplikat]` dijawab `409` tetapi berkas baru itu tetap tersimpan permanen.
5. **SHA-256 unik per pesanan.** Duplikat isi pada satu pesanan ditolak `409
   PROOF_DUPLICATE`, dicek tiga lapis: (a) di dalam permintaan itu sendiri,
   (b) terhadap seluruh sidik jari pesanan SEBELUM satu byte pun masuk storage,
   dan (c) oleh indeks unik `(order_id, sha256)` di dalam transaksi commit —
   benteng terakhir bila service dilewati (SystemDesign.md §5.3). Isi yang sama
   pada pesanan BERBEDA tetap sah (cek per pesanan, bukan global).
   Batas `max_file` juga diperiksa di dalam transaksi commit yang sama, setelah
   baris pesanan dikunci, sehingga unggahan **paralel** tidak bisa
   melampaui batas (temuan QA putaran 1 #2: dulu 12 unggahan paralel dengan
   `max_file=5` menyisakan 9 baris; sekarang tepat 5).
6. **Otorisasi di layer service, bukan di UI.** Akun cabang hanya boleh
   menyentuh pesanan milik cabang SESI-nya; pesanan cabang lain dijawab `404`
   (bukan `403`) supaya keberadaannya tidak bocor, dan id yang bukan UUID juga
   `404` (bukan `500`). Admin memakai kapabilitas `admin.orders` (Manager dan
   Superadmin — PRD §2.1) dan boleh menyentuh pesanan cabang mana pun.
7. **Status pesanan menentukan siapa yang boleh menambah bukti.** Cabang boleh
   menambah bukti pada pesanan `draft` (dibutuhkan modal submit F1-14 yang
   mengunggah berkas sebelum transisi status) dan `submitted` (pembayaran
   cicilan/terpisah, CP-ORD-07); pesanan `done` ditolak `409 ORDER_LOCKED`
   (BR-07). Admin boleh pada status apa pun, termasuk `done`, karena
   koreksi/revisi adalah hak Pusat. Membaca tetap boleh untuk cabang pada
   pesanan `done`.
8. **Timeline menandai pengunggah.** Setiap unggahan pada pesanan yang sudah
   dikirim menulis satu baris `order_events`: `branch | "Bukti transfer
   diunggah oleh cabang"` atau `admin | "Bukti transfer diunggah oleh <nama>
   (admin Pusat)"` (PRD CP-ORD-08, AP-ORD-06). Draft belum punya timeline
   (keputusan F1-11/F1-14), jadi unggahan pada draft tidak menulis event.
   Penulisan `order_events` dilakukan repository paket bukti — preseden yang
   sama dipakai domain email saat membaca `orders` (SystemDesign.md §2 aturan
   antar-domain dijaga lewat query terpisah, bukan ketergantungan paket).
9. **Unduhan selalu presigned URL berumur pendek** (default 300 detik) dengan
   `Content-Disposition: attachment; filename="<nama asli>"`, sehingga browser
   menyimpan dengan nama yang benar dan berkas tidak pernah mengalir lewat API.
   `storage.Store` tidak diubah; paket bukti memakai interface opsional
   `DownloadSigner` yang dipenuhi S3 maupun Memory. Kunci storage hanya muncul
   di dalam URL bertanda tangan yang kedaluwarsa.
10. **Implementasi S3 nyata memakai aws-sdk-go-v2** (Architecture.md §1) dengan
    endpoint path-style untuk MinIO. Konfigurasi setengah (endpoint ada,
    kredensial tidak) **menghentikan start** — lebih baik gagal terang-terangan
    daripada kehilangan berkas pengguna. Storage belum dikonfigurasi berarti
    unggah dijawab `503 UNAVAILABLE`; tidak ada fallback ke penyimpanan memori
    yang isinya hilang saat proses berhenti. `/readyz` melaporkan `storage: ok`
    hanya bila bucket benar-benar bisa diakses.
11. **Endpoint yang ditambahkan di luar daftar ringkas SystemDesign §6** (yang
    hanya menyebut `POST .../proofs` untuk kedua portal): `GET .../proofs`
    (daftar), dan `GET .../proofs/{proofId}/download` (tautan unduh) — keduanya
    untuk cabang dan admin. Tanpa endpoint baca, kriteria "pratinjau/zoom
    sesuai izin" dan "multi-bukti tercatat dengan pengunggah" tidak bisa
    dipenuhi; semuanya dicatat di `openapi/openapi.yaml` dan diuji kontrak.
12. **Batas tidak diduplikasi di klien.** Nilai `max_file`/`max_mb` hanya ada di
    setelan Superadmin (`/admin/settings`) dan tidak ada permukaan pengaturan
    lintas-role (keputusan F1-09). UI karena itu tidak menampilkan angka batas,
    melainkan pesan kesalahan yang dikembalikan server per field `file`.
    Pilihan ini disengaja: menulis ulang angka batas di klien akan membuat UI
    dan server bisa berbeda setelah Superadmin mengubah setelan.
13. **Retensi tanpa batas sesuai keputusan user** (override Database.md §9):
    tidak ada kolom kedaluwarsa, tidak ada job pembersih, dan penghapusan hanya
    terjadi bila pesanan draft dihapus (cascade baris) atau objek yatim
    dibersihkan saat metadata gagal dicatat.

## Berkas yang berubah

Backend:

- `api/internal/payment/payment.go`, `filetype.go`, `memory.go` (baru)
- `api/internal/payment/postgres/postgres.go` (baru)
- `api/internal/platform/storage/s3.go` (baru), `storage.go` (`PresignDownload`)
- `api/internal/httpapi/handlers_proofs.go` (baru), `router.go`, `deps.go`,
  `errors.go`, `integration_pg_test.go` (fixture menambah Payments)
- `api/internal/platform/apierr/apierr.go` (`WithFields`)
- `api/cmd/server/main.go` (`buildStorage`, wiring payment + ProofLimiter)
- `api/db/queries/payment.sql` + `api/internal/db/sqlc/payment.sql.go`
- `api/go.mod` / `api/go.sum` (aws-sdk-go-v2 core/credentials/service-s3)
- `openapi/openapi.yaml`
- Uji: `api/internal/payment/payment_test.go`,
  `api/internal/platform/storage/s3_test.go`,
  `api/internal/httpapi/router_proofs_test.go`,
  `api/internal/httpapi/integration_proofs_pg_test.go`

Frontend:

- `web/packages/api-client/src/index.ts` (+`postForm`, FormData tak di-stringify),
  `client.test.ts`
- `web/apps/customer/src/pages/pesanan.tsx` (+`BuktiTransfer`, `KartuBukti`),
  `pesanan.module.css`
- Uji: `web/apps/customer/src/pesanan.test.tsx` (+5)

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

```
$ gofmt -l .                       -> (kosong)
$ go vet ./...                     -> exit 0
$ go build ./...                   -> exit 0
$ sqlc-v1.31.1 generate            -> tidak ada diff tambahan (query baru saja)
$ ./scripts/check-secrets.sh       -> "Tidak ada rahasia terdeteksi", exit 0
$ redocly lint openapi/openapi.yaml-> "Your API description is valid. 🎉"
$ go mod tidy                      -> go.mod/go.sum konsisten (diff hanya 12+24 baris aws-sdk)
```

### Uji backend (PostgreSQL 16.15 nyata + `scripts/pg-dev-shim.sql`)

```
$ TEST_DATABASE_URL=postgres://…:5455/grahita_test?sslmode=disable \
  go test -race -p 1 -count=1 ./...
ok  github.com/grahita/portal/api/internal/payment     1.843s
ok  github.com/grahita/portal/api/internal/platform/storage 1.058s
ok  github.com/grahita/portal/api/internal/httpapi   111.547s
ok  ... (seluruh paket lain ok; EXIT 0, 0 FAIL, 0 SKIP)
```

Uji baru kartu ini:

| Berkas | Uji | Cakupan |
|---|---|---|
| `internal/payment/payment_test.go` | 18 | magic bytes 11 kasus (JPG/PNG/PDF sah, 3 ekstensi palsu, skrip berkedok `.jpg`, ekstensi asing, tanpa ekstensi, kosong), kunci acak + nama kiriman tidak bocor, sha256 tersimpan & isi bisa dibaca ulang, duplikat (antar permintaan, dalam satu permintaan, DB) + tidak menyisakan bukti, batas jumlah dari setelan, batas ukuran tepat di ambang, isolasi cabang (`ErrNotFound`), penguncian `done` (cabang vs admin), penanda nama admin + timeline hanya untuk non-draft, tautan unduh berumur pendek + `ErrNotFound` lintas cabang/pesanan, `MaxRequestBytes`, storage belum siap tidak menyisakan baris, pembersihan nama berkas, 200 kunci acak unik, TTL dapat diubah, **permintaan gagal tidak menyisakan baris/objek/timeline**, **kegagalan storage di tengah membersihkan semua objek**, **commit batal membersihkan semua objek**, **2×max_file unggahan paralel menyisakan tepat max_file** |
| `internal/platform/storage/s3_test.go` | 5 | Put/Get/Delete/Health terhadap server S3 in-process (membuktikan klien mengirim PUT/GET/DELETE dengan path & Content-Type benar), presign GET memuat `X-Amz-Signature`/`X-Amz-Expires=300`/`X-Amz-Credential`, presign unduh menyetel `response-content-disposition` attachment + sanitasi nama berbahaya, konfigurasi setengah ditolak, Memory memenuhi kontrak presign |
| `internal/httpapi/router_proofs_test.go` | 8 | lewat HTTP: unggah multi-berkas 201 + metadata tanpa `storage_key` + daftar + tautan unduh 300 detik; isolasi cabang 404 (unggah/daftar/unduh) + id non-UUID 404 + `done` 409 + tanpa sesi 403 CSRF/401 GET; ekstensi palsu & skrip 400 dengan field `file`, tanpa berkas 400, bukan multipart 400; duplikat 409 `PROOF_DUPLICATE` + `PROOF_LIMIT` 409; `max_mb` → 413; pembatas laju 429; admin unggah atas nama cabang (kind `admin` + nama) + boleh pada `done` + melihat pesanan cabang lain; storage belum siap 503 |
| `internal/httpapi/integration_proofs_pg_test.go` | 7 | di PostgreSQL: baris `payment_proofs` lengkap (kind dari sesi, `uploaded_by_nama` NULL untuk cabang, sha256 32 byte, `storage_key` berawalan `proofs/`), 2 event timeline, isi objek benar-benar tersimpan; duplikat 409 + INSERT duplikat ditolak database (23505); isolasi cabang tidak menyisakan baris; admin pada `done` 201 dengan nama di baris + timeline (`admin`), cabang 409 `ORDER_LOCKED`; `max_file` dari setelan DB (PUT `/admin/settings`) menolak unggahan kedua 409 `PROOF_LIMIT`; **permintaan `[berkas baru + duplikat]` 409 tanpa menambah baris/timeline/objek**; **2×max_file (10) unggahan paralel menyisakan tepat 5 baris + 5 event + 5 objek** |

### Uji & build frontend

```
$ npm run typecheck   -> exit 0 (api-client, ui, admin, customer)
$ npm test            -> api-client 8, ui 56, admin 29, customer 28 => 121 lulus
```

Uji baru: daftar bukti menampilkan nama berkas, ukuran, dan penanda pengunggah
("Cabang" vs "Admin Budi"); tombol Lihat memanggil `.../proofs/pf1/download` dan
menampilkan gambar dari URL bertanda tangan, klik gambar menyalakan zoom; unggah
mengirim `FormData` (field `file`) ke `.../orders/o2/proofs` **tanpa** header
Content-Type manual; pesanan `done` tidak menampilkan form unggah; draft tidak
memiliki bagian bukti sama sekali (dan tidak memanggil endpoint bukti).

### Probe HTTP pada binary sungguhan + PostgreSQL + object storage S3

Binary `--mode=api` (DB baru `grahita_f113probe`, migrasi 0001..0004 + 8 migrasi
River + seed, port 8091) dengan `S3_ENDPOINT` menunjuk server S3-compatible
lokal (`s3stub`, berkas scratch — bukan bagian repo), lalu seluruh alur
dijalankan lewat API nyata:

| Permintaan | Hasil |
|---|---|
| `/readyz` | `200` `{"storage":"ok","database":"ok","migrations":"ok (versi 4)"}` — bucket benar-benar di-HeadBucket |
| login cabang Bandung (seed) | `200` |
| `POST /orders` (draft) via F1-11 | `201`, order_id `01a11c49-…` |
| `POST /orders/{id}/proofs` (JPG 3.004 B + PDF 1.217 B) | `201`, dua bukti: `mime_type` dari isi, `uploaded_by_kind=branch`, `uploaded_by_nama=null`, `sha256` sama dengan `sha256sum` berkas |
| `GET /orders/{id}/proofs` | `200`, dua baris |
| `GET /orders/{id}/proofs/{pid}/download` | `200`, `url` memuat `X-Amz-Algorithm=AWS4-HMAC-SHA256` + `X-Amz-Signature` + `X-Amz-Expires=300` + `response-content-disposition=attachment; filename="bukti-1.jpg"`, `expires_in=300` |
| `GET` presigned URL (langsung ke object storage) | `200`, `Content-Disposition: attachment; filename="bukti-1.jpg"`, `Content-Type: image/jpeg`, **sha256 isi unduhan = sha256 berkas yang diunggah** |
| `POST .../proofs` PDF berkedok `.jpg` | `400` `{"file":"isi berkas tidak cocok dengan ekstensinya: ekstensi \".jpg\" bukan PDF"}` |
| `POST .../proofs` berkas acak 300 KB berkedok `.jpg` | `400` `{"file":"jenis berkas tidak didukung: hanya JPG, PNG, atau PDF"}` |
| `POST .../proofs` tanpa berkas | `400` `{"file":"tidak ada berkas bukti yang dikirim"}` |
| `POST .../proofs` isi identik (nama beda) | `409 PROOF_DUPLICATE`; jumlah baris DB tetap 2 |
| cabang Bekasi: daftar/unduh/unggah bukti pesanan Bandung | ketiganya `404 NOT_FOUND` (tanpa membocorkan keberadaan) |
| pesanan dijadikan `submitted` (serial + nomor diisi di DB) lalu unggah bukti cicilan (PNG) | `201`; DB `order_events`: `branch \| Bukti transfer diunggah oleh cabang` |
| admin (Superadmin) `POST /admin/orders/{id}/proofs` | `201`, `uploaded_by_kind=admin`, `uploaded_by_nama=Rina Kartika`; timeline bertambah `admin \| Bukti transfer diunggah oleh Rina Kartika (admin Pusat)` |
| `GET /admin/orders/{id}/proofs` (Superadmin) | `200` |
| cabang → `GET /admin/orders/{id}/proofs` | `403 FORBIDDEN` |
| `max_file` diubah 5 → 1 lewat `PUT /admin/settings`, lalu unggah kedua | `409 PROOF_LIMIT`; setelan dikembalikan ke 5 |
| unggah berkas 6.291.004 B (melebihi `max_mb` 5) | `413 PAYLOAD_TOO_LARGE` `{"file":"ukuran berkas melebihi batas"}` |
| cabang menambah bukti pada pesanan `done` | `409 ORDER_LOCKED`; admin pada pesanan yang sama `201` + timeline menandai nama |
| ringkasan DB | 4 bukti pesanan pertama: kind `branch`/`admin`, nama `-`/`Rina Kartika`, `kunci` berawalan `proofs/{order_id}/` |
| log object storage | PUT untuk setiap berkas sah, GET ber-`X-Amz-Signature` untuk unduhan, dan **PUT diikuti DELETE** tepat pada percobaan yang ditolak (objek yatim dibersihkan) |
| retensi | 9 baris bukti tersimpan; tidak ada kolom `expire*`/`deleted*` dan tidak ada job pembersih |
| regresi | `GET /orders/{id}` tetap `200` dengan baris utuh |

## Perbaikan putaran 2 (temuan QA putaran 1)

QA mereproduksi dua cacat pada commit `1435cd5` di salinan bersih + PG16; dua-duanya
diperbaiki di commit ini:

1. **Commit multi-berkas tidak atomik.** Dulu `Service.Upload` menulis objek +
   baris **per berkas di dalam loop**, sehingga satu permintaan berisi
   `[B baru, A duplikat]` dijawab `409 PROOF_DUPLICATE` tetapi B tetap tersimpan
   permanen (dan responsnya membuat UI mengulang batch yang sama selamanya).
   Sekarang: (a) seluruh berkas divalidasi, (b) sidik jari pesanan dipra-cek
   (`Repository.Sha256ByOrder`) sehingga duplikat — termasuk terhadap baris lama
   — gagal **sebelum** satu byte pun masuk storage, (c) seluruh metadata +
   peristiwa timeline ditulis dalam **satu transaksi**
   (`Repository.CommitProofs`), dan (d) bila penyimpanan atau transaksi gagal,
   `hapusObjek` membuang **semua** kunci yang sempat ditulis, bukan hanya yang
   terakhir.
2. **Batas `max_file` tidak aman-balapan.** Dulu `CountByOrder` lalu `Add` tanpa
   kunci apa pun: 12 unggahan paralel dengan `max_file=5` menyisakan 9 baris.
   Sekarang `CommitProofs` mengunci baris pesanan lebih dulu
   (`SELECT id FROM orders WHERE id=$1 FOR UPDATE`, query baru
   `LockOrderForProof`) lalu menghitung dan menyisipkan di transaksi yang sama,
   sehingga batas tidak bisa dilampaui unggahan serentak. Uji regresi paralel
   ditambahkan di dua level: unit in-memory `2×max_file` dan integrasi PG
   (10 unggahan paralel → tepat 5 baris + 5 event + 5 objek).

Perubahan kontrak internal (bukan HTTP): `Repository` tidak lagi menyediakan
`CountByOrder`/`Add`; penulisan hanya lewat `CommitProofs(CommitRequest)`, dan
interface `Timeline` yang terpisah dihapus karena peristiwa timeline kini
bagian dari unit kerja yang sama (`Event`) — `main.go` dan fixture uji tidak lagi
memakai `payment.WithTimeline`. Endpoint, kode status, dan badan respons tidak
berubah, jadi klien/frontend tidak terpengaruh.

Verifikasi putaran ini (PG 16.15 + shim, DB baru `grahita_f113r2`):

```
$ gofmt -l .                       -> (kosong)
$ go build ./... ; go vet ./...    -> exit 0
$ ./scripts/check-secrets.sh       -> exit 0
$ TEST_DATABASE_URL=…/grahita_f113r2 go test -race -p 1 -count=1 ./...
ok  internal/payment 1.8s | ok  internal/httpapi 99.7s | … seluruh paket ok (EXIT 0, 0 FAIL/0 SKIP)
$ go test -race -count=3 -run 'TestPGPermintaanGagalTidakMenyisakanBaris|TestPGBatasJumlahAmanBalapan' ./internal/httpapi/
ok  internal/httpapi 15.6s
$ go test -race -count=5 ./internal/payment/...
ok  internal/payment 2.3s
```

Uji integrasi baru benar-benar dieksekusi (bukan skip):

```
--- PASS: TestPGPermintaanGagalTidakMenyisakanBaris (2.23s)
--- PASS: TestPGBatasJumlahAmanBalapan (2.22s)
```

## Catatan & risiko sisa (non-pemblokir)

- **Duplikat dicek per pesanan, bukan global.** Isi berkas yang sama boleh
  diunggah pada pesanan berbeda (kebutuhan nyata: bukti transfer yang sama
  dipakai untuk dua pesanan sekaligus). Ini sesuai bunyi "sha256 unik per
  order" pada kartu. Bila kelak ingin dedup global (hemat storage), itu
  perubahan kebijakan yang perlu keputusan user.
- **Berkas divalidasi dengan membaca seluruh isi ke memori** (≤ `max_file ×
  max_mb` = 25 MB dengan default) supaya sidik jari dan penyimpanan memakai
  byte yang sama. Untuk batas jauh lebih besar, alurnya perlu streaming +
  penulisan ke berkas sementara — belum diperlukan sekarang (batas PRD kecil).
- **Objek storage yatim bila pesanan draft dihapus.** Cascade DB hanya menghapus
  baris `payment_proofs`; objeknya tetap ada di bucket (tidak ada kunci
  penghapusan per-pesanan di kartu ini). Dengan retensi tanpa batas ini tidak
  menghilangkan data pengguna, tetapi menyisakan sampah storage — pekerjaan
  pembersihan/backup adalah kewajiban **F1-21 (backup)** dan/atau kartu dokumen
  F1-18, bukan kartu ini. Jalur kegagalan yang bisa dikendalikan kartu ini
  (permintaan ditolak / transaksi batal) sudah membersihkan objeknya.
- **Unggahan F1-14 belum atomik dengan transisi status.** F1-14 dapat memanggil
  `POST /orders/{id}/proofs` sebelum transisi Draft→Submitted (draft boleh
  diunggah — keputusan 7). Bila transisi gagal, bukti tetap menempel pada draft
  (tidak ada uang/nomor yang terbuang; draft bisa dihapus) — bukan kehilangan
  data, tetapi bukan satu transaksi. Bila F1-14 menuntut atomisitas penuh, ia
  harus menulis baris `payment_proofs` di transaksi submitnya lewat repository
  paket ini (`payment/postgres`) dan menyimpan berkasnya lebih dulu.
- **`nama_file` tidak diverifikasi terhadap isi** selain kecocokan ekstensi
  (mis. PDF dengan nama `struk.jpg` tidak ada karena ekstensi wajib cocok).
  Nama tetap ditampilkan apa adanya (sudah dibersihkan) di UI.
- **Presigned URL bergantung pada konfigurasi S3 yang benar** (endpoint publik
  yang bisa dijangkau browser). Di lingkungan lokal, `S3_ENDPOINT` harus
  menunjuk alamat yang bisa diakses browser (MinIO di docker-compose sudah
  begitu); kalau tidak, unduhan gagal di klien meski API benar.
- **Uji dijalankan pada PG16.15 + shim `uuidv7()`**, bukan PG18 asli/CI
  `postgres:18-alpine` (sama seperti F1-02..F1-12). Object storage diuji
  terhadap server S3-compatible lokal (bukan MinIO sungguhan); verifikasi tanda
  tangan SigV4 oleh server tidak diuji karena `s3stub` tidak memverifikasi tanda
  tangan — bagian `Pre sign` diuji pada struktur URL + SDK resmi yang menandatangani.
- **Probe UI di browser sungguhan belum dijalankan** untuk kartu ini (kartu
  bukti + zoom). Bukti UI berasal dari uji DOM vitest dan probe HTTP; untuk
  bukti browser jalankan `npm run dev:customer` dengan API pada `:8088` dan
  buka `/pesanan/{id}` pesanan `submitted`.

## Sisa untuk kartu lain (tidak dikerjakan di sini)

- **F1-14 (t_5b21b48b)**: modal kirim + multipart bukti pada `POST
  /orders/{id}/submit`, wajib ≥1 bukti kecuali C&C, dan keputusan apakah bukti
  ditulis di transaksi submit yang sama (lihat catatan risiko).
- **F1-15/F1-19**: halaman detail pesanan admin (pratinjau bukti + tombol
  unggah atas nama cabang) memakai endpoint `GET/POST /admin/orders/{id}/proofs`
  dan `GET /admin/orders/{id}/proofs/{proofId}/download`; chip "perlu review"
  dan verifikasi tetap milik kartu tersebut.
- **F1-18 (dokumen)**: `documents` sebaiknya memakai `storage.S3` dan kontrak
  presign yang sudah ada di kartu ini (`PresignGet`/`PresignDownload`).
- **F2-26**: kolom `ocr_*` pada `payment_proofs` sengaja belum diisi; pembacaan
  AI mengisinya, dan `PaymentReader` membaca berkas lewat `storage.Store.Get`.
- **F1-21 (backup)**: sinkronisasi inkremental object storage harus menyertakan
  prefix `proofs/` dan tidak menghapus apa pun (retensi tanpa batas).
