# F1-06b — Halaman Cabang Admin Portal (UI CRUD + token registrasi)

Kartu: **t_d919f518 — F1-06b Halaman Cabang Admin Portal (UI CRUD + token
registrasi)**

Dokumen acuan: PRD §4.4 (field, tombol, AP-CAB-01..05), §2.1 (Manager boleh
master cabang); prototype `docs/prototype/admin_v2.html`
(`pCabang`/`openCabang`/`paintCabang`/`saveCabang`); F1-06 `HASIL.md`
(permukaan API yang sudah disetujui QA).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode. Kartu ini **frontend saja**: tidak ada perubahan
`api/`, `openapi/`, atau skema.

## Permukaan API yang dipakai (semua sudah ada sejak F1-06/F1-05)

| Method | Path | Dipakai untuk |
|---|---|---|
| GET | `/api/v1/admin/branches?q=&aktif=&cnc=&special=` | daftar + filter tab & pencarian |
| POST | `/api/v1/admin/branches` | tambah cabang |
| PUT | `/api/v1/admin/branches/{id}` | ubah cabang, aktif/nonaktifkan |
| DELETE | `/api/v1/admin/branches/{id}` | hapus (409 `BRANCH_HAS_ORDERS` → jalur nonaktifkan) |
| POST | `/api/v1/admin/branches/{id}/token` | token registrasi (tampil sekali) |
| GET | `/api/v1/admin/users/branch` | kolom "Akun" + aturan token (F1-05) |

Semua mutasi membawa header `X-CSRF-Token` dari cookie `grahita_csrf` (klien
`@grahita/api-client`, sama seperti kartu UI lain).

## Keputusan desain

1. **Halaman pindah dari kerangka F1-03 ke `pages/cabang.tsx`.** `CabangPage`
   read-only di `pages/shell-pages.tsx` dihapus; tipe `Cabang` tetap di
   `shell-pages.tsx` (dipakai `DashboardPage`) dan kini diekspor, ditambah
   field `pajak_in`, `token_prefix`, `token_expires_at` sesuai `Branch` di
   OpenAPI. Rute `/cabang` di `routes.tsx` menunjuk berkas baru.
2. **Filter tab & pencarian dijalankan SERVER**, bukan disaring di memori
   seperti prototype: tab Semua/C&C/Special/Nonaktif dan kotak cari memetakan
   diri ke `?q=`, `?cnc=true`, `?special=true`, `?aktif=false` — persis filter
   yang disediakan F1-06, sehingga daftar tidak perlu dimuat seluruhnya.
   Konsekuensinya subjudul menampilkan jumlah hasil filter (`N cabang cocok
   dengan filter`) ketika filter aktif, dan `N cabang · M akun aktif` ketika
   tidak (angka akun aktif berasal dari daftar akun yang memang tidak
   tersaring).
3. **`has_account` diambil dari `GET /admin/users/branch` (F1-05).**
   `/admin/branches` sengaja tidak mengembalikannya (F1-06 memutuskan di
   service lewat `HasAccount`). Dua permintaan itu dijalankan bersamaan dan
   **kegagalan daftar akun tidak menghalangi master cabang**: kolom "Akun"
   menampilkan `—` plus `Notice` peringatan, dan blok token berhenti menebak —
   tombol generate tetap ada dan jawaban server (409 `BRANCH_HAS_ACCOUNT`)
   yang menentukan. Server tetap otoritas akhir.
4. **Token sekali tampil.** Nilai asli token hanya dipakai dari respons
   `POST /admin/branches/{id}/token`, ditampilkan pada input read-only +
   tombol **Salin** (`navigator.clipboard`, dengan pesan manual bila papan
   klip tidak tersedia). Setelah dialog ditutup dan dibuka lagi, yang tampil
   hanya `token_prefix` + masa berlaku (`Token aktif GRH-XXXX… berlaku sampai
   …`), sesuai aturan hash-only di F1-06.
5. **Aturan blok "Akun & token registrasi"** (semuanya juga ditegakkan server):
   - cabang baru (belum disimpan) → "Simpan cabang terlebih dahulu…";
   - cabang **sudah punya akun** → token tidak diperlukan, pemulihan lewat
     lupa kata sandi (tombol generate tidak dirender);
   - cabang **nonaktif** → "Cabang sedang nonaktif; aktifkan dulu sebelum
     membuat token registrasi." (tombol generate tidak dirender; server tetap
     menjawab 409 `BRANCH_INACTIVE` bila dipaksa);
   - cabang aktif tanpa akun → tombol **Buat token** / **Buat token baru**.
6. **Hapus selalu lewat konfirmasi** (modal terpisah). Bila server menjawab
   409 `BRANCH_HAS_ORDERS`, pesannya ditampilkan dan tombol **Hapus cabang**
   digantikan **Nonaktifkan cabang** (`PUT {is_active:false}`) — jalur
   AP-CAB-04, tanpa mengulang permintaan hapus yang pasti gagal.
7. **Konfirmasi "Ya, ubah email login".** Bila email cabang yang sudah punya
   akun diubah, penyimpanan pertama hanya menampilkan peringatan + tombol
   berubah label menjadi "Ya, ubah email login" (perilaku prototype
   `saveCabang`); `PUT` baru dikirim setelah dikonfirmasi. `PUT` selalu
   memuat seluruh field form (bukan hanya yang berubah) supaya payload
   deterministik.
8. **Validasi form di klien + pemetaan galat server.** Klien memeriksa nama
   ≥ 2 karakter, nama kepala ≥ 2 karakter, dan format email; galat server
   ditempelkan ke field yang tepat (`EMAIL_TAKEN` → input email, karena
   pesannya dikirim tanpa `fields`), `VALIDATION_FAILED`/lain-lain → `Notice`
   di atas dialog. Dialog tetap terbuka agar pengguna bisa membetulkan.
9. **Keadaan memuat/galat/kosong memakai komponen `@grahita/ui` yang ada:**
   `SkeletonList`, `Notice` (danger untuk galat daftar, warning untuk galat
   daftar akun), `EmptyState` (kosong tanpa filter vs "Tidak ada cabang yang
   cocok."), `Table mobileCards`, `Segmented`, `SwitchRow`, `Badge`, `Modal`.
10. **Penyimpangan sadar dari prototype** (semua demi aksesibilitas/API nyata):
    baris tabel tidak dibuat `click` — aksi lewat tombol **Ubah** sehingga
    keyboard/pembaca layar punya target eksplisit; tab "Belum registrasi"
    prototype tidak dibuat karena kartu meminta tab Semua/C&C/Special/Nonaktif
    (keadaan "belum registrasi" tampil sebagai badge di kolom Akun); pilihan
    cabang pada topbar prototype tidak dirender di halaman ini (bukan bagian
    kriteria kartu, sudah ada di Dashboard).

## Berkas yang berubah

- `web/apps/admin/src/pages/cabang.tsx` (baru) — halaman `/cabang`: tabel,
  tab filter, pencarian, dialog tambah/ubah, blok token, dialog hapus.
- `web/apps/admin/src/cabang.test.tsx` (baru) — 11 uji vitest.
- `web/apps/admin/src/pages/shell-pages.tsx` — `CabangPage` read-only dihapus,
  tipe `Cabang` diekspor + `pajak_in`/`token_prefix`/`token_expires_at`.
- `web/apps/admin/src/routes.tsx` — impor `CabangPage` dari `./pages/cabang`.
- `web/apps/admin/src/pages/admin-pages.module.css` — `.tokenBox`, `.aksi`,
  `.aksiSel` (kartu mobile).
- `web/apps/admin/src/App.test.tsx` — stub fetch uji shell ditambah
  `/admin/users/branch` (halaman `/cabang` kini memanggil dua endpoint).
- `docs/design/f1-06b/HASIL.md` + 3 PNG (lihat bawah) — berkas ini.

## Uji otomatis (vitest, uji perilaku lewat router + fetch tiruan)

`web/apps/admin/src/cabang.test.tsx` — 11 uji:

| # | Yang diuji |
|---|---|
| 1 | tabel: badge C&C/Special/PI, kolom Akun (Aktif/Belum registrasi), tab filter, ringkasan header |
| 2 | tab & pencarian dikirim ke server: `?cnc=true`, `?q=bandung`, `?aktif=false` |
| 3 | tambah cabang: `POST` + header `X-CSRF-Token`, body lengkap, daftar dimuat ulang |
| 4 | `EMAIL_TAKEN` (409) tampil di input email (`aria-invalid=true`), dialog tetap terbuka |
| 5 | validasi klien menahan email salah format **tanpa** memanggil server |
| 6 | token: cabang belum berakun → `POST .../token`, nilai asli tampil, tombol Salin memanggil papan klip |
| 7 | cabang berakun: blok token menjelaskan lupa kata sandi, tidak ada tombol generate |
| 8 | cabang nonaktif: pesan "aktifkan dulu", tidak ada tombol generate |
| 9 | hapus: dialog konfirmasi, 409 `BRANCH_HAS_ORDERS` → tombol "Nonaktifkan cabang" → `PUT {is_active:false}` |
| 10 | ubah email cabang berakun: minta konfirmasi, `PUT` baru terkirim setelah "Ya, ubah email login" |
| 11 | galat daftar (503) ditampilkan lewat `Notice`, tabel tidak dirender |

Perintah + hasil nyata (dijalankan di `web/`):

| Perintah | Hasil |
|---|---|
| `npm run typecheck --workspace @grahita/admin` | exit 0 |
| `npm run test --workspace @grahita/admin` | **7 berkas, 40 tes lulus** (sebelum kartu: 6 berkas, 29 tes) |
| `npm run typecheck` (4 workspace) | exit 0 |
| `npm run build --workspace @grahita/admin` | `✓ built in 1.07s` (`dist/assets/index-BBfvyodW.js` 373,96 kB) |
| `npm run build` (kedua SPA) | admin + customer terbangun (`✓ built in 1.07s` / `910ms`) |
| `npm run test` (semua workspace) | ui 8/8, admin 40/40, customer **1 gagal** — lihat "Catatan" (F1-13, di luar kartu ini) |

### Kontrol negatif (membuktikan uji benar-benar mengikat)

Dua penjaga dikembalikan ke bentuk salah, uji dijalankan, lalu dipulihkan:

| Perubahan sementara | Hasil |
|---|---|
| penjaga "cabang nonaktif" pada blok token dimatikan | `× cabang nonaktif tidak boleh membuat token registrasi` (9 uji lain lulus) |
| cabang berakun: pemanggilan ke server saat email berubah dimatikan | `× mengubah email cabang berakun minta konfirmasi 'Ya, ubah email login'` (9 uji lain lulus) |
| keduanya dipulihkan | 11/11 lulus |

## Verifikasi hidup (browser nyata + API nyata + PostgreSQL)

Rangkaian: DB baru `grahita_f106b` (PG 16.15 + `scripts/pg-dev-shim.sql`) →
`--mode=migrate` → `--mode=seed` → binary `--mode=api` di `:8088` → `npm run
dev:admin` (Vite) di `http://127.0.0.1:5174` dengan proxy `/api` → `:8088`.
Sesi Manager `budi@grahitaindonesia.com` dipasang langsung di tabel `sessions`
(tidak memakai kata sandi), cookie `grahita_session` + `grahita_csrf` disetel
lewat CDP. Data: 4 cabang seed (semua berakun) + `Grahita Semarang` (aktif, C&C,
PI, **belum** berakun) dan `Grahita Palembang` (Special, **nonaktif**,
belum berakun) yang dibuat lewat `POST /api/v1/admin/branches` sungguhan.

Hasil nyata (teks DOM + tangkapan layar):

| Pemeriksaan | Hasil |
|---|---|
| halaman `/cabang` sebagai Manager | "6 cabang · 4 akun aktif", tabel 6 baris, badge C&C/Special/PI, kolom Akun & Status benar |
| menu superadmin | Rekening bank/Reporting/Pengaturan tidak ada (Manager) |
| overflow | desktop `innerWidth 1440 = scrollWidth 1440`; mobile `390 = 390` |
| dialog "Detail cabang" (Semarang) | blok "Akun & token registrasi · Belum registrasi" → "Belum ada token aktif." + tombol **Buat token** |
| klik **Buat token** | `POST /admin/branches/{id}/token` 200; nilai asli `GRH-39JT-…` tampil dengan tombol Salin, "Berlaku sampai 22 Okt 2026, 23.28" (+14 hari), peringatan "Salin token sekarang" |
| tutup lalu buka ulang dialog | hanya `Token aktif GRH-39JT… berlaku sampai 22 Okt 2026, 23.28. Nilai lengkapnya hanya tampil saat token dibuat.` — nilai asli **tidak** bisa dibaca lagi |
| dialog cabang nonaktif (Palembang) | "Cabang sedang nonaktif; aktifkan dulu sebelum membuat token registrasi." tanpa tombol generate |

### Tangkapan layar

| Berkas | Viewport | Isi |
|---|---|---|
| `admin-cabang-desktop.png` | 1440×900 | daftar `6 cabang · 4 akun aktif`, tab Semua/C&C/Special/Nonaktif, kotak cari, badge flag/akun/status, tombol Ubah/Nonaktifkan/Hapus, catatan AP-CAB-04 |
| `admin-cabang-mobile.png` | 390×844 | tampilan kartu (`mobileCards`): judul, tombol Tambah cabang, tab membungkus dua baris, tiga tombol aksi rata kanan, `scrollWidth = 390` |
| `admin-cabang-dialog-token.png` | 1440×900 | dialog "Detail cabang" + blok token dengan nilai asli, tombol Salin, masa berlaku, peringatan sekali tampil, tombol "Buat token baru" |
| `admin-cabang-dialog-nonaktif.png` | 1440×900 | dialog cabang nonaktif: blok token menolak generate |

Token pada tangkapan layar berasal dari DB sekali-pakai `grahita_f106b` yang
sudah dibuang setelah probe, sehingga nilainya tidak berlaku lagi.

## Catatan & risiko sisa

- **`npm run test` di akar repo masih merah, bukan karena kartu ini.**
  `web/apps/customer/src/pesanan.test.tsx` (F1-13, commit 1435cd5) gagal pada
  `menampilkan bukti beserta penanda pengunggah cabang/admin`: assertion
  memakai `getByText` (sinkron) tepat setelah `findByRole` sementara daftar
  bukti masih dimuat asinkron. Berkas customer tidak disentuh kartu ini
  (`git status` hanya menyentuh `web/apps/admin/**`); mengganti satu assertion
  itu menjadi `findByText` membuat uji tersebut lulus (dibuktikan sementara,
  lalu dikembalikan). F1-13 sedang di kartu QA `t_f2244a1b` — perbaikan
  sebaiknya lewat kartu itu, bukan kartu ini.
- Dua permintaan untuk satu halaman (`/admin/branches` + `/admin/users/branch`)
  diterima: filter tab/pencarian milik cabang, kolom Akun milik user
  management. Bila kelak `Branch` memuat `has_account`, panggilan kedua bisa
  dihapus.
- Tidak ada debounce pada kotak cari: setiap ketikan memicu satu permintaan
  (pola yang sama dengan `katalog.tsx` TabProduk). Daftar cabang kecil
  (puluhan baris), jadi belum perlu.
- Tanpa uji end-to-end browser otomatis (Playwright tidak dipakai di repo ini);
  lapisan browser diverifikasi manual dengan tangkapan layar di atas, sesuai
  pola F1-03/F1-05/F1-10.
