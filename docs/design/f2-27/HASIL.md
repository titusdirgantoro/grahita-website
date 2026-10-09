# F2-27 — Tutorial dinamis tanpa deploy

Kartu: **t_0856261e — F2-27 Tutorial dinamis tanpa deploy**

Dokumen acuan: `docs/PRD.md` §3.8 (Tutorial: "Konten dikelola sebagai halaman
statis yang bisa diperbarui tanpa deploy ulang") dan §10 (Fase 2 — "halaman
tutorial dinamis"); `docs/SystemDesign.md` §2 (struktur kode backend) dan §7
(frontend: peta layar `pTutorial` + aturan implementasi UI);
`docs/Database.md` §1 (konvensi: uuid PK, snake_case, created_at/updated_at,
migrasi goose satu perubahan per berkas, selalu kompatibel mundur) dan §4.7
(tabel pengaturan/audit).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

1. **Penyimpanan konten terstruktur** `tutorial_topics` lewat migrasi baru
   `0006_tutorial.sql` yang HANYA menambah tabel + mengisi empat topik awal
   (kompatibel mundur, idempoten).
2. **Domain `internal/tutorial`**: validasi + sanitasi konten, badging
   otorisasi, dan penulisan audit per topik dalam satu transaksi.
3. **Repository `internal/tutorial/postgres`** di atas PostgreSQL (sqlc) —
   selaras pola `settings`/`bank` (service → repository → db).
4. **API**: `GET /api/v1/tutorial` (portal cabang) dan pasangan admin
   `GET`/`PUT /api/v1/admin/tutorial` (Superadmin), plus dokumentasi OpenAPI.
5. **Portal cabang**: halaman `/tutorial` membaca konten dari API, bukan lagi
   dari bundel — perubahan konten langsung terlihat saat halaman di-refresh.

Di luar lingkup kartu (tidak dikerjakan): **editor konten di portal admin**
(UI). Kartu hanya meminta API baca cabang + tulis Superadmin; jalur tulisnya
sudah lengkap dan dapat dipakai UI mana pun (termasuk `curl`/panel admin di
kartu lanjutan).

## Keputusan desain

1. **Konten = teks biasa, bukan HTML.** Kartu melarang "HTML arbitrer".
   Karena itu `judul`, `ringkas`, dan setiap butir `langkah` melewati
   `tutorial.Sanitize` sebelum divalidasi panjangnya: komentar HTML dan blok
   `<script>`/`<style>` (beserta isinya) dibuang, seluruh tag `<…>` dibuang
   berulang sampai stabil, sisa karakter `<`/`>` juga dibuang, karakter
   kendali dijadikan spasi, dan spasi diciutkan. Hasilnya: konten yang
   tersimpan **tidak pernah memuat rangkaian markup** (invarian ini diuji),
   jadi tidak ada jalur HTML/JS yang bisa masuk lewat data — bahkan bila klien
   lain kelak salah merender sebagai markup.
   *Trade-off yang disadari:* karakter `<`/`>` yang dipakai sebagai
   perbandingan ("harga < 5000") ikut hilang; tulisannya harus "kurang dari".
   Ini dipilih supaya invariannya sederhana dan bisa diperiksa mesin.
2. **Ilustrasi = KUNCI bawaan aplikasi (`ikon`), bukan berkas/markup.**
   Server hanya menerima `memesan | bukti | sertifikat | cnc | umum`; nilai
   lain ditolak `400 VALIDATION_FAILED`. Keempat gambar lama (design prototype
   `customer_v2.html`) tetap persis seperti F1-19, ditambah satu ilustrasi
   netral `umum` untuk topik baru; semuanya SVG inline milik portal, sehingga
   "jangan memberi HTML arbitrer" berlaku juga untuk bagian bergambar. Kunci
   tak dikenal di sisi klien (mis. data lama) jatuh ke ilustrasi `umum`.
3. **Batas konten ditegakkan dua lapis.** Domain: maksimal 12 topik, judul
   ≤ 120, ringkasan ≤ 300, satu langkah ≤ 500 karakter (dihitung per *rune*,
   bukan byte), 1–24 langkah per topik. Database: CHECK `jsonb_typeof(langkah)
   = 'array'`, `jsonb_array_length(langkah) BETWEEN 1 AND 24`,
   `char_length(judul)/ringkas` dalam rentang yang sama, `urutan >= 0`.
4. **Bentuk tulis: "ganti seluruh isi" (`PUT`).** Urutan array = urutan
   tampil (server menomori `10, 20, 30, …` sehingga klien tidak bisa mengirim
   urutan bertabrakan), `id` kosong = topik baru, dan topik yang tidak
   disertakan DIHAPUS. Seluruh validasi/sanitasi dijalankan lebih dahulu —
   bila ada satu bagian tidak valid, tidak ada yang tersimpan (all-or-nothing,
   `400` dengan pesan per bagian `topik[i].<bagian>`). Alasan memilih
   "ganti seluruh isi": satu permintaan = satu transaksi = satu perubahan
   halaman, tanpa urutan panggilan antar-topik yang bisa setengah jalan.
5. **Hanya yang berubah yang ditulis & diaudit.** Service membandingkan
   kiriman dengan isi yang berlaku (ikon/judul/ringkasan/langkah/urutan);
   topik identik dilewati, dan bila tidak ada tambah/ubah/hapus sama sekali
   tidak ada baris yang ditulis dan **tidak ada entri audit** (pola yang sudah
   dipakai F1-09 untuk pengaturan). Snapshot audit berisi konten saja
   (ikon/judul/ringkas/langkah/urutan) — tidak ada data sensitif.
6. **Hak akses: kapabilitas baru `admin.tutorial` hanya untuk Superadmin.**
   Router menegakkan kapabilitas (`requireCap`) dan layer service memeriksa
   ulang (`Replace` menolak bila `!actor.Can(CapAdminTutorial)`), jadi Manager
   dan akun cabang dijawab `403 FORBIDDEN` di dua tempat — bukan hanya
   disembunyikan di UI. Pembacaan `GET /api/v1/tutorial` cukup sesi yang sah
   (halaman informasi, tidak ada data per-cabang), dan endpoint baca tidak
   punya metode tulis (`PUT/POST/DELETE /api/v1/tutorial` → `405`).
7. **Migrasi kompatibel mundur & idempoten.** Tidak ada tabel/kolom lama yang
   diubah atau dihapus. Pengisian empat topik awal dijaga
   `WHERE NOT EXISTS (SELECT 1 FROM tutorial_topics)`, jadi menjalankan ulang
   migrasi pada basis data yang sudah berisi konten tidak menimpa dan tidak
   menggandakan.
8. **Portal cabang selalu punya panduan.** Halaman `/tutorial` menampilkan
   konten server segera setelah `GET /api/v1/tutorial` selesai, tetapi tetap
   menampilkan empat topik bawaan (teks yang sama, kini hanya sebagai jaring
   pengaman) selama permintaan berjalan atau bila permintaan gagal — panduan
   tidak pernah kosong walaupun API/DB sedang bermasalah. Tidak ada build
   ulang yang dibutuhkan: isi halaman adalah hasil `fetch` saat render.

## Perubahan berkas (repo `/home/hermes/projects/grahita-website`)

| Berkas | Isi |
|---|---|
| `api/db/migrations/0006_tutorial.sql` | tabel `tutorial_topics` + indeks urutan + CHECK konten + seed empat topik awal (idempoten) |
| `api/db/queries/tutorial.sql` | `ListTutorialTopics`, `InsertTutorialTopic`, `UpdateTutorialTopic`, `DeleteTutorialTopic` |
| `api/internal/db/sqlc/tutorial.sql.go`, `models.go` | hasil `sqlc generate` (pin v1.31.1) |
| `api/internal/tutorial/tutorial.go` | domain: `Topic`/`Input`, `Sanitize`, `Check`, `Service.List`/`Replace`, `ValidationError`, sentinel error, helper jsonb `LangkahJSON`/`LangkahDariJSON` |
| `api/internal/tutorial/tutorial_test.go` | uji unit sanitasi, validasi, otorisasi, tambah/ubah/hapus + audit, no-op, bentuk kanonik |
| `api/internal/tutorial/postgres/postgres.go` | repository PostgreSQL: `List` + `Apply` (satu transaksi untuk tambah/ubah/hapus + audit) |
| `api/internal/auth/actor.go` | kapabilitas `admin.tutorial` (hanya Superadmin) |
| `api/internal/audit/audit.go` | entitas audit `tutorial` |
| `api/internal/httpapi/deps.go`, `handlers_tutorial.go`, `errors.go`, `router.go` | layanan di Deps, handler baca/tulis, pemetaan error, dua grup rute (cabang & Superadmin) |
| `api/internal/httpapi/router_tutorial_test.go` | uji transport: otorisasi, pembacaan cabang, sanitasi lewat HTTP, validasi all-or-nothing, 405 |
| `api/internal/httpapi/integration_tutorial_pg_test.go` | uji integrasi PostgreSQL nyata: konten dinamis + audit + urutan + sanitasi DB + hak akses |
| `api/internal/platform/database/schema_test.go` | `tutorial_topics` masuk daftar tabel inti; `TestSeedTutorialPRD` (konten awal tersedia + migrasi ulang idempoten) |
| `api/internal/auth/auth_test.go` | matriks kapabilitas: Manager/cabang TIDAK boleh `admin.tutorial` |
| `api/cmd/server/main.go` | wiring `tutorial.NewService(tutorialpg.New(pool))` di mode `api` |
| `openapi/openapi.yaml` | path `/api/v1/tutorial` + `/api/v1/admin/tutorial`, skema `TutorialTopic`/`TutorialContent`/`TutorialTopicInput`/`TutorialUpdateRequest` |
| `web/apps/customer/src/pages/tutorial.tsx` | halaman membaca `GET /api/v1/tutorial`; pemetaan `ikon` → ilustrasi bawaan; fallback empat topik |
| `web/apps/customer/src/tutorial.test.tsx` | uji konten dari server, uji teks berbahaya tidak menjadi markup, uji fallback |

## Bukti verifikasi

Semua perintah dijalankan di repo target. Database uji dibangun ulang
(`DROP/CREATE DATABASE grahita_f227` + `scripts/pg-dev-shim.sql` + ekstensi
`citext`/`pg_trgm`/`pgcrypto`) pada PostgreSQL 16.15 lokal.

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | 0 diff (`git status --porcelain -- db internal/db/sqlc` hanya memuat berkas baru F2-27) |
| `./scripts/check-secrets.sh` | "Tidak ada rahasia terdeteksi pada berkas yang dilacak." (exit 0) |
| `redocly lint openapi/openapi.yaml` | "Your API description is valid." — 10 warning (baseline lama) |
| `go test ./internal/tutorial/...` | ok (uji unit) |
| `go test ./internal/httpapi -run 'TestApiTutorial.*'` | ok (uji transport, in-memory) |
| `TEST_DATABASE_URL=…  go test ./internal/httpapi -run 'TestPGKontenTutorial\|TestPGTutorial'` | ok (2 uji integrasi PostgreSQL nyata) |
| `TEST_DATABASE_URL=… go test ./internal/platform/database -run 'TestSeedTutorialPRD\|TestSkemaIntiLengkap\|TestMigrasiBersih'` | ok |
| `npm run typecheck` (4 workspace) | exit 0 |
| `npm test --workspace @grahita/customer -- src/tutorial.test.tsx src/App.test.tsx` | 9 lulus / 9 |
| `go test -race -p 1 -count=1 -timeout 30m ./...` | **SUITE_EXIT=0** dalam 778s; seluruh paket `ok` (termasuk `internal/httpapi` 612,7s dan `internal/tutorial` 1,6s) — bukti mentah di `docs/design/f2-27/BUKTI-SUITE.txt` |
| **Bukti hidup**: biner di-build dari ISI COMMIT (`git checkout-index -a`), dijalankan di PG 16.15 dengan DB baru (`--mode=migrate` → `--mode=seed` → `--mode=api`), lalu probe HTTP nyata | `PROBE_OK`: portal cabang membaca 4 topik awal, `PUT` Superadmin → sesi cabang BARU langsung melihat versi barunya, Manager/cabang 403 (baca & tulis), `PUT/POST/DELETE /tutorial` 405, isi database tersanitasi, audit `update` = 1 — bukti mentah di `docs/design/f2-27/BUKTI-LIVE.txt` |

Uji integrasi PostgreSQL membuktikan secara langsung kriteria kartu:

- **Konten berubah tanpa deploy**: `PUT /api/v1/admin/tutorial` → `GET
  /api/v1/tutorial` (permintaan baru, sesi cabang) segera mengembalikan konten
  baru, lengkap dengan urutan baru dan topik yang dihapus; nilai di tabel
  `tutorial_topics` sama dengan yang dijawab API.
- **HTML berbahaya disanitasi**: judul `Cara <b>memesan</b><script>alert(1)
  </script>`, ringkasan ber-`<img … onerror>`, dan langkah ber-`javascript:`
  tersimpan sebagai `Cara memesan` / `Ringkasan bersih.` / `Klik di sini` —
  diperiksa di DATABASE (`judul`, `ringkas`, `langkah::text` tidak memuat
  `<`, `>`, `script`, `onerror`, `javascript`), bukan hanya di respons.
- **Hak akses API diuji**: cabang & Manager → `403 FORBIDDEN` pada baca dan
  tulis `/api/v1/admin/tutorial`; isi tabel dan jumlah entri audit TIDAK
  berubah setelah penolakan; `PUT/POST/DELETE /api/v1/tutorial` → `405`.
- **Konten awal tetap tersedia saat migrasi**: `TestSeedTutorialPRD`
  memastikan empat topik awal (ikon/judul/urutan ≥ 10/20/30/40 + langkah)
  ada setelah migrasi dan jumlahnya tidak berubah setelah migrasi dijalankan
  ulang.
- **Audit**: entri `entity='tutorial'` dengan `actor_id` Superadmin, satu
  `create` per topik baru (tanpa `before`), `update` dengan snapshot
  before/after 5 kunci konten, dan `delete`; mengirim ulang konten yang sama
  tidak menambah entri.

## Risiko & catatan yang tersisa

1. **TOCTOU halus pada bentuk "ganti seluruh isi"**: diff dihitung dari
   pembacaan sebelum transaksi. Dua `PUT` bersamaan bisa sama-sama menyimpulkan
   perubahan; `Apply` menolak `Update`/`Delete` yang kehilangan baris
   (`ErrNotFound` → rollback), dan `id` yang tidak dikenal dijawab `400`.
   Dampaknya kecil (halaman konten disunting satu orang), sama dengan catatan
   non-pemblokir F1-09 pada pengaturan.
2. **Belum ada editor konten di portal admin.** Sesuai lingkup kartu, jalur
   tulis hanya tersedia lewat API. Bila diinginkan, UI-nya bisa menyusul di
   kartu terpisah tanpa perubahan backend.
3. **Konten bukan lagi bagian bundel** — portal cabang menampilkan empat topik
   bawaan bila API gagal. Ini disengaja (panduan tidak pernah kosong), tetapi
   artinya tampilan bisa berbeda dari database saat API bermasalah; tidak ada
   indikator galat di halaman (sengaja, agar tidak berisik bagi cabang).
4. **`updated_by` per topik** dicatat di database, tetapi tidak ditampilkan;
   riwayat lengkap perubahan ada di `audit_logs` (dipakai kartu F2-28 audit
   UI).
5. Bukti dijalankan pada PostgreSQL 16.15 + `scripts/pg-dev-shim.sql` (bukan
   PG 18) dan tanpa `golangci-lint`, sama seperti kartu-kartu lain pada
   gelombang ini.
