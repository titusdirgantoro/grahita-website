# F1-03 — Perbandingan visual shell terhadap prototype

Kartu: **t_104d4013 — Sistem desain dan shell responsif dua portal**

Halaman ini menyimpan bukti visual bahwa sistem desain dan shell hasil port
(`web/packages/ui` + `web/apps/customer` + `web/apps/admin`) menampilkan nilai
yang sama dengan prototype (`docs/prototype/customer_v2.html`,
`docs/prototype/admin_v2.html`).

## Berkas snapshot

| Berkas | Viewport | Sumber |
|---|---|---|
| `customer-app-desktop.png` | 1440×900 | `apps/customer` rute `/gaya` (hasil build) |
| `customer-app-mobile.png` | 390×844 | idem |
| `customer-proto-desktop.png` | 1440×900 | `customer_v2.html` rute `#/katalog` |
| `customer-proto-mobile.png` | 390×844 | idem |
| `admin-app-desktop.png` | 1440×900 | `apps/admin` rute `/gaya` (hasil build) |
| `admin-app-mobile.png` | 390×844 | idem |
| `admin-proto-desktop.png` | 1440×900 | `admin_v2.html` rute `#/cabang` |
| `admin-proto-mobile.png` | 390×844 | idem |

## Cara reproduksi

1. `cd web && npm run build` lalu layani hasilnya:
   `npm run preview --workspace @grahita/customer` (4173) dan
   `npm run preview --workspace @grahita/admin` (4174).
2. Prototype: berkas asli di `docs/prototype` memuat font & ikon dari CDN
   (fonts.googleapis.com, cdn.jsdelivr.net). Lingkungan verifikasi ini tidak
   memiliki akses CDN, sehingga dibuat **salinan sementara** di `/tmp` dengan
   tautan CDN ditulis ulang ke aset lokal yang sudah ada di `node_modules`
   (font Plus Jakarta Sans varian variable dan font ikon Phosphor).
   Berkas asli **tidak diubah** — salinan hanya alat verifikasi.
   Rute prototype dijalankan dengan `S.authed = true` + `location.hash` lalu
   `render()`.
3. Snapshot diambil lewat Chrome DevTools Protocol (`Page.captureScreenshot`)
   dengan animasi dimatikan agar frame stabil.
4. Perbedaan piksel dihitung per area dengan PIL (`ImageChops.difference`);
   "beda piksel" = persentase piksel dengan selisih kanal > 15/255.

## Hasil computed style (app vs prototype)

Nilai diambil langsung dari `getComputedStyle` di kedua halaman.

### Customer, desktop 1440×900

| Properti | Prototype | App |
|---|---|---|
| `body` font | `"Plus Jakarta Sans", …` | `"Plus Jakarta Sans Variable", …` (font variable self-host) |
| `body` background | oklch(brand-900) | sama |
| `aside` | 240×900, transparan | sama |
| Tautan nav aktif | bg `rgba(255,255,255,.1)`, `#fff`, radius `999px`, tinggi `44px` | sama |
| Panel utama | brand-50, radius `24px`, margin `12px 12px 12px 0` | sama |
| Padding konten | `28px 28px 48px` | sama |
| `h1` | `28px / 700` | sama |
| Ikon | font `Phosphor` | font `Phosphor` (self-host) |

### Customer, mobile 390×844

| Properti | Prototype | App |
|---|---|---|
| Sidebar | `display:none` | sama |
| Bottom-nav | `display:grid`, 5×`1fr`, bg brand-900, 390×62 | sama |
| Tautan bottom | `11px`, `rgba(255,255,255,.72)`, aktif = "Katalog" | sama |
| Topbar mobile | `display:flex`, `position:sticky`, brand-50, tinggi 59 | sama |
| Panel | radius `0`, margin `0`, `padding-bottom:76px` | sama |
| Padding konten | `16px 16px 24px` | sama |
| `h1` | `24px / 700` | sama |

### Admin, desktop 1440×900

| Properti | Prototype | App |
|---|---|---|
| `body` background | `#EDEDEB` | sama |
| Sidebar | 232×876, `#fff`, radius `24px`, padding `18px 14px`, `position:sticky` | sama |
| Label grup | `12px`, `#6D6D72` | sama |
| Tautan nav aktif | bg `ink`, `#fff`, radius `999px`, tinggi `40px` | sama |
| `main` padding | `4px 4px 32px` | sama |
| Padding shell | `12px` | sama |
| Topbar | tinggi 44, `min-height:44px` | sama |
| Tombol menu | `display:none` (desktop) | sama |
| `h1` | `24px / 500` | sama |

### Admin, mobile 390×844

| Properti | Prototype | App |
|---|---|---|
| Sidebar | `position:fixed`, 232×824, radius `24px`, `translateX(-278px)` (menyembunyikan) | sama |
| Tombol menu | `display:grid` | sama |
| Padding shell | `10px` | sama |
| `h1` | `24px / 500` | sama |

## Perbedaan piksel pada area yang seharusnya identik

| Area | Viewport | Beda piksel | Rata selisih |
|---|---|---|---|
| Customer sidebar (0–240 px) | desktop | **1,32 %** | 0,43/255 |
| Customer bottom-nav (90 px bawah) | mobile | **1,59 %** | 1,72/255 |
| Customer topbar (60 px atas) | mobile | **4,56 %** | 6,19/255 |
| Admin sidebar (0–244 px) | desktop | **0,08 %** | 0,06/255 |
| Admin tombol menu (60×48) | mobile | **1,32 %** | 0,33/255 |
| Admin lonceng+avatar (60×48) | mobile | **0,00 %** | 0,00/255 |

Sisa selisih berasal dari antialias teks/ikon, posisi scroll, dan panjang nama
cabang ("Grahita Bandung" vs "Grahita Bandung" pada prototype memang sama).

## Perbedaan yang disengaja (bukan cacat sistem desain)

- Isi `<main>` berbeda: halaman app yang dibandingkan adalah **galeri komponen
  `/gaya`** (untuk memperlihatkan seluruh kit), sementara prototype menampilkan
  halaman Katalog (customer) dan Cabang (admin). Karena itu perbandingan piksel
  difokuskan pada sisi shell (sidebar, topbar, bottom-nav).
- Pada `/gaya` admin, topbar memuat **sakelar peran Superadmin/Manager** untuk
  memperagakan guard menu; prototype halaman Cabang tidak memiliki kendali itu,
  sehingga baris kedua topbar berbeda tinggi. Baris lain tetap sama
  (lihat dua pemeriksaan 60×48 di tabel).
- Rute `/gaya` sengaja memakai akun contoh dan tidak memanggil API sehingga
  snapshot dapat direproduksi tanpa backend.
