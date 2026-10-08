# F1-07b — Halaman daftar token Customer Portal (UI dua langkah)

Kartu: **t_abbacd96 — F1-07b Halaman daftar token Customer Portal (UI dua
langkah)**

Dokumen acuan: PRD §3.1 CP-REG-02..07 + §9(1); SystemDesign §4.1 (`pDaftar`
`/daftar`) dan §6–7 (layout auth terpisah dari shell); Architecture §6
(kata sandi ≥ 8, rate limit di backend); Database §4.1–4.2 (1 cabang = 1 akun,
token sekali pakai); prototype `docs/prototype/customer_v2.html` (`pDaftar`,
`SUBMIT.token`, `SUBMIT.register`); F1-07 `HASIL.md` (permukaan API yang sudah
disetujui QA pada commit `ca47d32`).

Kartu ini **frontend saja**: tidak ada perubahan `api/`, `openapi/`, atau
skema. Permukaan API F1-07 dipakai apa adanya.

## Permukaan API yang dipakai (sudah ada sejak F1-07)

| Method | Path | Body | Dipakai untuk |
|---|---|---|---|
| POST | `/api/v1/auth/registration/validate` | `{token}` | langkah 1: validasi token + data cabang read-only |
| POST | `/api/v1/auth/register` | `{token, telepon, password, password_konfirmasi}` | langkah 2: membuat akun cabang |

Kedua endpoint publik (tanpa sesi) dan dikecualikan dari CSRF, jadi klien
memanggilnya dengan `{ skipCsrf: true }` — tidak ada header `X-CSRF-Token` yang
dikirim. Respons sukses `{data:…}`, gagal `{error:{code,message,fields?}}`
(`@grahita/api-client`).

## Keputusan desain

1. **Halaman pindah dari kerangka F1-03 ke alur nyata.** `HalamanDaftar`
   (`web/apps/customer/src/pages/auth.tsx`) tidak lagi memakai data cabang
   hardcode (`Grahita Bandung` / `Dra. Sri Wahyuni`) dan tidak lagi berpindah
   langkah hanya karena token ≥ 4 karakter. Langkah 1 kini memanggil
   `POST /auth/registration/validate`; langkah 2 baru muncul setelah backend
   menyatakan token sah.
2. **Data cabang 100% dari respons backend.** `nama_cabang`, `nama_kepala`,
   `email` disimpan di state React dari isi `200` validate dan ditampilkan
   sebagai input `readOnly` **tanpa atribut `name`** — biarpun payload langkah 2
   dibangun dari state (bukan `FormData` seluruh form), field itu juga tidak
   bisa "ikut terkirim" karena tidak punya nama. Backend tetap menolak field tak
   dikenal (`additionalProperties: false`), jadi identitas cabang/email tidak
   dapat dipalsukan dari klien.
3. **Token tidak pernah disimpan.** Token hidup hanya di state komponen (tidak
   ada `localStorage`/`sessionStorage`, tidak ada query param). Field token
   hanya ada di langkah 1; setelah lolos validasi, nilainya tidak ditampilkan
   kembali. Normalisasi (huruf besar/`trim`) dan hashing tetap urusan backend —
   klien hanya mengirim bentuk yang diketik pengguna (huruf besar + `trim`).
4. **Pesan spesifik per keadaan (CP-REG-07).** Kode
   `TOKEN_INVALID`/`TOKEN_EXPIRED`/`TOKEN_USED`/`BRANCH_INACTIVE`/
   `BRANCH_HAS_ACCOUNT`/`EMAIL_TAKEN`/`RATE_LIMITED` dipetakan ke pesan
   Indonesia yang menyebut langkah berikutnya (minta token baru / hubungi admin
   Pusat / pakai lupa kata sandi). Kode lain (mis. `UNAVAILABLE`,
   `VALIDATION_FAILED` di luar `fields`) memakai pesan backend apa adanya.
   Kegagalan jaringan (`fetch` menolak) tidak menghasilkan pesan teknis:
   "Tidak dapat menghubungi server. Periksa koneksi internet lalu coba lagi."
5. **Indikator kekuatan kata sandi (CP-REG-05).** Komponen baru
   `PasswordStrength` + helper `kekuatanKataSandi` di `@grahita/ui`
   (`packages/ui/src/password.ts`, `components/PasswordStrength.tsx`). Skor 0–4
   dari panjang ≥ 8 (wajib), panjang ≥ 12, variasi huruf besar/kecil, angka, dan
   simbol; label "Sangat lemah/Lemah/Cukup/Kuat/Sangat kuat". Meter memakai
   `role="meter"` + `aria-valuenow`/`aria-valuetext` dan teks label
   `aria-live="polite"`. Indikator hanya muncul setelah pengguna mulai
   mengetik (tidak menambah kebisingan di samping `hint`).
   **Aturan mengikat tetap backend** (`auth.CheckPasswordStrength`): klien hanya
   umpan balik cepat, dan pesan `fields` dari server selalu menimpa.
6. **Validasi klien memakai sumber bersama.** Syarat kata sandi (≥ 8, huruf dan
   angka, tanpa spasi tepi) diperiksa lewat `periksaKataSandiBaru` yang sama
   dengan alur ganti kata sandi; ditambah parameter opsional `pesanKonfirmasi`
   supaya pesan konfirmasi pendaftaran berbunyi "Konfirmasi kata sandi belum
   sama." (tanpa kata "baru"). Telepon: wajib + maks 40 karakter (sama dengan
   `validate:"required,max=40"` di backend), tanpa batasan format angka.
   Konfirmasi beda / kata sandi lemah / telepon kosong ditolak di klien tanpa
   memanggil API; galat `fields` dari server (`password`,
   `password_konfirmasi`, `telepon`) ditempelkan ke field yang tepat.
7. **Cegah submit ganda.** Setiap handler memeriksa `if (sibuk) return;` dan
   tombol submit `disabled` selama permintaan berjalan (label berubah menjadi
   "Memeriksa token…" / "Membuat akun…").
8. **Sukses → arahkan ke login, bukan login otomatis.** Setelah `201`, halaman
   menavigasi ke `/masuk?daftar=sukses`; `masRoute` memvalidasi search param itu
   dan `HalamanMasuk` menampilkan `Notice` sukses. Tidak ada token/sesi yang
   dititipkan ke klien (SystemDesign §4.1 berakhir di `201`).
9. **Token yang mati saat langkah 2 tetap bisa dipulihkan.** Bila `register`
   menjawab `TOKEN_INVALID`/`TOKEN_EXPIRED`/`TOKEN_USED`/`BRANCH_INACTIVE`/
   `BRANCH_HAS_ACCOUNT` (mis. token dipakai tab lain lebih dulu), pesan
   spesifiknya ditampilkan dan muncul tombol **Gunakan token lain** yang
   mengosongkan state dan kembali ke langkah 1.
10. **Layout & aksesibilitas mengikuti F1-03.** `AuthLayout` (indikator 2
    langkah, tanpa shell/sidebar) dan `Form`/`Field`/`Input`/`Notice`/`Button`
    yang ada; label terhubung `htmlFor`+`id`, pesan galat `role="alert"`,
    `aria-invalid` otomatis lewat `Field`, `autoComplete` (`tel`,
    `new-password`), `inputMode="tel"`, dan `maxLength` telepon.

## Berkas yang berubah

| Berkas | Perubahan |
|---|---|
| `web/apps/customer/src/pages/auth.tsx` | `HalamanDaftar` dua langkah nyata; `HalamanMasuk` menerima `terdaftar` |
| `web/apps/customer/src/routes.tsx` | `masRoute` `validateSearch` `?daftar=sukses` + wrapper |
| `web/packages/ui/src/password.ts` | `kekuatanKataSandi`, tipe `SkorKekuatan`/`KekuatanKataSandi`, `pesanKonfirmasi` |
| `web/packages/ui/src/components/PasswordStrength.tsx` (+`.module.css`) | komponen indikator kekuatan |
| `web/packages/ui/src/index.ts` | ekspor baru |
| `web/apps/customer/src/daftar.test.tsx` | 12 tes alur `/daftar` |
| `web/packages/ui/src/components/password-strength.test.tsx` | 8 tes indikator + `pesanKonfirmasi` |

## Verifikasi

Gerbang otomatis (repo `/home/hermes/projects/grahita-website`, `web/`):

| Perintah | Hasil |
|---|---|
| `npm run typecheck` (api-client, ui, admin, customer) | exit 0, tanpa error TS |
| `npm run test` (akar, 4 workspace) | api-client 8/8 · ui **64/64** · admin 40/40 · customer **40/40** |
| `npm run test --workspace @grahita/customer` | 5 berkas, 40 tes lulus (12 di antaranya `daftar.test.tsx`) |
| `npm run build` | kedua SPA hijau (customer 169 modul) |

Tidak ada berkas `api/`, `openapi/`, atau migrasi yang disentuh, sehingga suite
Go tidak terpengaruh (`git status` hanya memuat berkas di tabel atas).

Probe browser nyata — binary `--mode=api` pada DB PG baru `grahita_f107b_live`
(seed `ProbeF107b!23`) + `npm run dev:customer` (proxy `/api` → `:8088`),
cabang + token registrasi dibuat lewat API admin F1-06 sebagai Superadmin:

| Keadaan | Hasil yang teramati |
|---|---|
| langkah 1 → 2 | `POST /auth/registration/validate` terkirim `{token}` (tanpa header CSRF, tanpa field lain); halaman berubah ke "Lengkapi akun" |
| data read-only | `Nama cabang`/`Nama kepala cabang`/`Email cabang` = nilai dari backend, `readOnly=true`, `name=null` |
| indikator kekuatan | kata sandi lemah → `aria-valuenow=0` + "Sangat lemah"; kuat → `aria-valuenow=4` + "Sangat kuat" |
| konfirmasi beda | pesan "Konfirmasi kata sandi belum sama." + `aria-invalid=true`, **0** panggilan `/auth/register` |
| submit ganda | satu klik + satu `submit` programatik → **1** panggilan validate, tombol berubah "Memeriksa token…" |
| sukses | `POST /auth/register` **tepat 1 kali**, lalu URL `/masuk?daftar=sukses` + notice "Akun cabang berhasil dibuat…" |
| login akun baru | masuk dengan email cabang + kata sandi baru → shell "Beranda" memuat nama cabang baru |
| token sudah dipakai | "Token registrasi sudah dipakai. Satu cabang hanya punya satu akun — gunakan lupa kata sandi bila perlu masuk kembali." (tetap di langkah 1) |
| token tidak dikenal | "Token registrasi tidak dikenali. Periksa kembali penulisannya, atau minta token baru ke admin Pusat." |
| token kosong | "Token registrasi wajib diisi." tanpa panggilan API |
| anti-pemalsuan | nilai input read-only diubah jadi `palsu@jahat.test`/`Cabang Palsu` **dan** diberi atribut `name` sebelum submit → body yang benar-benar terkirim hanya `{token}` (validate) dan `{token,telepon,password,password_konfirmasi}` (register); pendaftaran tetap `201` |
| penyimpanan klien | `localStorage`/`sessionStorage` kosong; cookie yang ada hanya `grahita_csrf` (bukan token) |
| responsif | desktop 1280×900 dan mobile 390×844: `scrollWidth == innerWidth` (tanpa overflow horizontal), kontrol 40px tinggi, tanpa sidebar pada layout auth; langkah 1 dikirim lewat tombol Enter (keyboard) |

Bukti mentah (tanpa nilai token/kata sandi): lampiran
`BUKTI-browser-f107b.log` pada kartu ini.

## Catatan / sisa risiko (non-pemblokir)

- **429 tidak diuji ulang lewat browser.** Limiter registrasi (10 per 15 menit
  per IP) sudah dibuktikan QA F1-07 pada level API, dan jalur UI-nya diuji unit
  test (`RATE_LIMITED` → "Terlalu banyak percobaan. Tunggu beberapa saat lalu
  coba lagi."). Menguji 429 di browser akan menghabiskan kuota per-IP yang sama
  dengan probe alur normal.
- **`TOKEN_EXPIRED`/`BRANCH_INACTIVE`/`BRANCH_HAS_ACCOUNT` tidak dibuat hidup
  di probe browser** (butuh memundurkan `expires_at`, menonaktifkan cabang, atau
  membuat akun lebih dulu); ketiganya diuji unit test dengan kode error yang
  sama seperti yang dihasilkan backend (`mapDomainErr`) dan sudah dibuktikan
  hidup pada level API oleh QA F1-07. `TOKEN_USED` dan `TOKEN_INVALID` diuji
  hidup di browser.
- **Indikator kekuatan adalah heuristik klien**, bukan aturan. Dua kata sandi
  dengan `skor` berbeda bisa sama-sama diterima backend selagi memenuhi ≥ 8,
  huruf+angka; sebaliknya skor tinggi tidak menjamin lolos bila server
  menambah aturan (pesan server selalu menimpa).
- **Token terlihat di layar pada langkah 1** (pengguna mengetiknya). Tidak ada
  penyimpanan, log, atau pengiriman ulang nilai itu setelah validasi sukses.
- **Endpoint pendaftaran dikecualikan CSRF oleh backend** (F1-07, keputusan
  terdokumentasi); klien tidak dapat memperketatnya sendiri.
