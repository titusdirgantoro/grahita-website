# F1-20 — Dashboard admin dan chip "perlu tindakan"

Kartu: **t_dc32ba2a — F1-20 Dashboard admin dan chip perlu tindakan**

Dokumen acuan: `docs/PRD.md` §4.1 (AP-DSH-01..06), §2.1 (matriks hak akses),
§7; `docs/SystemDesign.md` §6 (daftar endpoint), §7.1–7.2 (peta layar &
aturan UI); `docs/Database.md` §7.2 (query chip "perlu tindakan").
Prototype acuan UI: `docs/prototype/admin_v2.html` (`pDashboard`, `range()`,
`perluk()`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend — domain baru `internal/dashboard` (+ `internal/dashboard/postgres`):

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/api/v1/admin/dashboard` | Manager & Superadmin | metrik periode + chip "perlu tindakan" + tren + peringkat cabang |

Frontend portal admin (`grahita-admin.tiusclaw.com`, endpoint tetap
`/api/v1/admin`):

- `/` (dashboard) — filter cabang (semua/satu) & periode (hari/minggu/bulan/
  tahun) di URL search param; chip "perlu tindakan" yang bisa diklik menuju
  daftar terfilter dan bisa disembunyikan; metrik berbasis role; grafik SVG
  tren 12 bulan & harian bulan berjalan; ringkasan status pembayaran;
  peringkat cabang.

## Keputusan desain

1. **Domain tersendiri, bukan menumpang domain pesanan.** Dashboard adalah
   *read model* lintas tabel: ia membaca `orders`/`branches` (domain pesanan)
   dan daftar id pesanan ber-email gagal (domain `notification` lewat interface
   `dashboard.SumberEmailGagal`, sama seperti `order.SumberEmailGagal` F1-15).
   Karena itu ia punya service + repository sendiri
   (`internal/dashboard`, `internal/dashboard/postgres`) dan **tidak ada satu
   pun query tulis** di dalamnya. `httpapi` tetap hanya mengimpor paket service.
   Isi interface diserahkan oleh pemanggil: saat tidak ada sumber terpasang,
   chip email gagal = 0 dan filter `email_gagal=true` kosong (fail closed,
   bukan seolah-olah semua pesanan normal).
2. **Nilai rupiah dihilangkan untuk Manager, bukan dikirim nol.** Tipe respons
   `Metrik{Jumlah int64; Nilai *int64}` memakai `omitempty`, dan service hanya
   mengisi `Nilai` bila aktor memegang `admin.dashboard.value` (Superadmin).
   Manager menerima `{"jumlah": N}` **tanpa kunci `nilai` sama sekali**
   (AP-DSH-02). Ini diuji di dua lapis: unit (JSON Marshal tidak memuat
   `"nilai"`) dan integrasi (badan mentah HTTP Manager/Superadmin).
3. **Otorisasi kapabilitas, bukan hostname.** Rute memakai
   `requireCap(auth.CapAdminDashboard)` (Manager + Superadmin, PRD §2.1) dan
   service menegakkan hal yang sama (`dashboardActor`); akun cabang dijawab
   `403`. Pembatasan nilai rupiah ditegakkan di *service*, bukan router, karena
   Manager memang boleh membuka dashboard.
4. **Tanggal bisnis Asia/Jakarta dari jam aplikasi, bukan UTC.** "Hari ini"
   diambil dari `clock.TodayWIB(clk)` (Asia/Jakarta), dan rentang periode
   mengikuti perilaku prototype: **minggu mulai Senin**, bulan & tahun berjalan
   **berakhir HARI INI** (bukan akhir bulan/tahun). Query SQL memakai ekspresi
   tanggal bisnis yang **persis sama** dengan riwayat pemesanan admin
   (`COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date)`),
   termasuk penggolongan draft ke hari bisnis `created_at`-nya.
5. **Chip tidak dibatasi periode (Database.md §7.2).** Chip adalah keadaan yang
   menunggu penanganan *sekarang*: yang membatasinya hanya filter cabang. Tiap
   angka chip dihitung dengan klausa yang **sama** dengan filter daftar riwayat
   (F1-15) sehingga padanannya tepat:
   `menunggu` → `status=submitted&verif=belum`, `perlu_review` →
   `verif=perlu_review`, `lewat_jatuh_tempo` → `terlambat=true`,
   `email_gagal` → `email_gagal=true`.
   Konsistensi ini bukan asumsi: uji integrasi membandingkan **setiap** angka
   chip dengan `total` daftar terfilternya, untuk semua cabang dan untuk dua
   cabang terpisah (pembanding per cabang dihitung ulang lewat SQL di uji).
6. **C&C lewat jatuh tempo tidak mengubah status.** Sistem tidak pernah
   memindahkan pesanan karena tenggat terlewat; chip hanya menghitung
   `is_cnc AND status <> 'done' AND jatuh tempo < hari ini` (PRD CP-CNC-06,
   §7.1). Uji menegaskan status pesanan tetap `submitted` setelah dashboard
   dibuka.
7. **Empat chip selalu dikirim; UI yang memilih menampilkan.** Server
   mengembalikan empat chip beserta angkanya (termasuk 0) supaya klien bisa
   memutuskan; UI meniru prototype — chip bernilai 0 tidak digambar, dan chip
   yang disembunyikan user hanya state tampilan (tidak dikirim ke server).
8. **Tren 12 bulan = Januari–Desember tahun bisnis berjalan**, mengikuti
   `range()` prototype (`months = Jan..Des tahun berjalan`); bulan tanpa pesanan
   diisi nol oleh service supaya grafik selalu 12 batang. **Harian** selalu
   bulan berjalan, hari 1 sampai hari ini, terlepas dari periode terpilih
   (AP-DSH-04). Bulan/tahun berjalan yang "berakhir hari ini" tidak mengubah
   pengisian nol ini.
9. **Peringkat cabang dibatasi 5 teratas di database** (`LIMIT`), diurutkan
   deterministik: jumlah DESC, lalu nilai DESC, lalu nama cabang A→Z
   (AP-DSH-06) supaya urutan tidak berubah-ubah saat angkanya seri.
10. **Filter yang tidak dikenal ditolak `400`.** `periode` di luar
    hari/minggu/bulan/tahun dan `cabang` yang bukan UUID dijawab
    `400 VALIDATION_FAILED` (`filter`), bukan diam-diam diabaikan; kosong berarti
    bawaan (`bulan`, semua cabang).
11. **Grafik SVG buatan sendiri, aksesibel, dan responsif.** Tidak ada library
    chart (Architecture.md §3.5/§7.2). SVG memakai `viewBox` + lebar 100%
    (responsif), `role="img"`, `aria-label`, `<title>`/`<desc>`, dan `<title>`
    per batang berisi label + angka (terbaca pembaca layar dan muncul sebagai
    tooltip bawaan browser).
12. **Pemilih periode di topbar shell dihapus.** Sebelumnya shell menampilkan
    `Segmented` periode untuk `/` dan `/pesanan` yang **tidak berpengaruh apa
    pun**. Sekarang kendali periode hidup di dashboard dan disimpan di URL
    search param halaman itu (SystemDesign §7.2) — satu pemilih, berfungsi, dan
    bisa di-bookmark. Tombol "+" di topbar dibiarkan seperti semula.

## Berkas yang berubah

Backend:

- `api/db/queries/dashboard.sql` (baru) + `api/internal/db/sqlc/dashboard.sql.go` (hasil sqlc)
- `api/internal/dashboard/dashboard.go` (baru: tipe, `Repository`, `Service`, normalisasi filter, penjagaan role)
- `api/internal/dashboard/dashboard_test.go` (baru: 11 uji unit)
- `api/internal/dashboard/postgres/postgres.go` (baru: implementasi repository, read-only)
- `api/internal/httpapi/handlers_dashboard.go` (baru), `deps.go`, `router.go`, `errors.go`
- `api/internal/httpapi/integration_dashboard_pg_test.go` (baru: 2 uji integrasi PostgreSQL)
- `api/internal/httpapi/integration_pg_test.go` (fixture: `Deps.Dashboard` dipasang)
- `api/cmd/server/main.go` (wiring service + sumber email gagal + clock)

Kontrak & UI:

- `openapi/openapi.yaml` (`/api/v1/admin/dashboard` + 7 skema `AdminDashboard*`)
- `web/apps/admin/src/pages/dashboard.tsx` (baru: `pDashboard`)
- `web/apps/admin/src/pages/shell-pages.tsx` (pDashboard lama & `MiniBars` placeholder dihapus)
- `web/apps/admin/src/pages/admin-pages.module.css` (kelas grafik & peringkat)
- `web/apps/admin/src/routes.tsx` (rute `/` + `validateSearch`, pemilih periode topbar dihapus)
- `web/apps/admin/src/dashboard.test.tsx` (baru: 10 uji vitest)

## Verifikasi (perintah + hasil nyata)

Dijalankan pada PostgreSQL 16.15 di `127.0.0.1:5455`, database khusus uji
`grahita_f120` (ekstensi `citext`, `pg_trgm`, `pgcrypto` di skema `public`;
setiap uji membuat skema sendiri + shim `uuidv7`).

| Gerbang | Perintah | Hasil |
|---|---|---|
| Format | `gofmt -l .` (di `api/`) | kosong |
| Analisis | `go vet ./...` | exit 0 |
| Build | `go build ./...` | exit 0 |
| sqlc | `sqlc-v1.31.1 generate` | 0 diff (md5 `dashboard.sql.go` identik) |
| Rahasia | `scripts/check-secrets.sh` | bersih (exit 0) |
| Kontrak | `redocly lint openapi/openapi.yaml` | valid, 10 warning (baseline) |
| Uji unit dashboard | `go test ./internal/dashboard/...` | ok (11 uji) |
| Uji integrasi dashboard | `go test -race -p 1 -count=1 -run TestPGDashboard ./internal/httpapi` | ok (2 uji, 7–8 s) |
| Suite penuh Go | `go test -race -p 1 -count=1 -timeout 30m ./...` (TEST_DATABASE_URL) | **22 paket ok, 0 FAIL** (httpapi 570,3 s; dashboard 1,7 s); log `cache/scratch/f120-full-suite3.log` |
| Web typecheck | `npm run typecheck` | exit 0 (4 workspace) |
| Web test | `npm test` | 195 lulus (api-client 8, ui 64, admin 70, customer 53) |
| Web build | `npm run build` | exit 0 |
| Uji fokus dashboard | `npx vitest run src/dashboard.test.tsx` (di `apps/admin`) | 10/10 PASS |

Uji yang membuktikan kriteria kartu:

- **Manager tak menerima rupiah dari API** — unit: JSON Marshal respons Manager
  tidak memuat kunci `nilai` (Superadmin memuat); integrasi: badan mentah HTTP
  Manager diperiksa dengan penyisir kunci JSON, ditambah `nilai` = tidak ada
  pada ringkasan/pembayaran/tren/peringkat.
- **Filter/timezone & chip konsisten dengan daftar** — chip vs `total`
  `/admin/orders` untuk keempat padanan filter × tiga lingkup cabang (semua,
  cabang A, cabang B); batas hari WIB diuji di unit (16:59:59Z vs 17:00:00Z).
- **Email gagal setelah retry muncul** — jalur produksi `notification.Service` +
  repository PostgreSQL dengan pengirim yang selalu gagal dan `MaxAttempts=2`:
  percobaan 1 → `Ditunda` (reschedule), percobaan 2 → `Menyerah` (`failed`,
  `attempts=2`), lalu chip `email_gagal` = 1 dan daftar `?email_gagal=true`
  total = 1.
- **C&C overdue tetap Submitted** — pesanan C&C ber-`jatuh_tempo` 7 hari lalu
  dihitung chip `lewat_jatuh_tempo` dan statusnya di database tetap
  `submitted` setelah dashboard dibuka.
- **Grafik SVG aksesibel responsif** — uji UI: `role="img"` + `aria-label`,
  `viewBox` ada, 12 batang bulanan & 9 batang harian; SVG lebar 100%
  (`max-width: 100%`, `height: auto`).

## Risiko tersisa / batas bukti

- Uji dijalankan pada **PostgreSQL 16.15 + shim `uuidv7`**, bukan PG18 asli;
  `golangci-lint` tidak tersedia (sama seperti kartu-kartu sebelumnya).
  Catatan lingkungan: database uji baru perlu ekstensi `citext`/`pg_trgm`/
  `pgcrypto` di skema `public` **dan** `scripts/pg-dev-shim.sql` diterapkan
  sebelum suite; tanpa shim, `internal/platform/database` gagal
  `function uuidv7() does not exist` (lingkungan, bukan kode — dibuktikan dengan
  menerapkan shim lalu paket itu ok).
- Batas "hari ini" untuk chip diambil dari **jam aplikasi** (service), sedangkan
  daftar riwayat F1-15 memakai `now()` PostgreSQL. Di produksi keduanya satu
  host sehingga identik; secara teori keduanya bisa berbeda tepat di detik
  pergantian hari WIB (17:00 UTC). Konsistensi chip↔daftar diuji pada satu
  database/clock, jadi risiko ini tidak hilang, hanya tidak praktis.
- Ringkasan periode **ikut menghitung draft** selama tanggal bisnisnya di dalam
  rentang, karena query-nya memakai ekspresi tanggal yang sama dengan daftar
  riwayat (agar angka dashboard dan daftar tidak pernah berbeda). Bila kelak
  draft harus dikecualikan, perubahan harus dilakukan di kedua tempat.
- Tren 12 bulan mengikuti prototype (Jan–Des tahun berjalan) — bukan jendela
  bergulir 12 bulan. Bila maksud PRD "12 bulan berjalan" adalah jendela
  bergulir, ini titik yang perlu diputuskan ulang.
- Peringkat cabang dibatasi 5 teratas (prototype & AP-DSH-06 "Could").
- `cabang` pada filter dashboard diambil dari master cabang (`/admin/branches`),
  jadi daftar pilihannya mengikuti F1-06; cabang nonaktif tetap bisa dipilih
  (datanya historis, bukan kesalahan).

## Hotspot

- `web/apps/admin/src/routes.tsx` — rute `/` kini menyimpan `validateSearch`
  filter dashboard; kartu lain yang ingin menambah search param di `/` harus
  memperluas `FilterDashboard` di `pages/dashboard.tsx`.
- `openapi/openapi.yaml` — satu berkas besar yang disentuh hampir semua kartu;
  uji kontrak `TestKontrakOpenAPIIkutRouter` menjaga agar rute & kontrak tetap
  sinkron.
