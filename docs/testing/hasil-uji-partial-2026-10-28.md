# Tindak lanjut skenario PARTIAL — 28 Oktober 2026

Pengujian tambahan memakai source sesudah rilis Gemini (`91727a30`) dan data sintetis pada PostgreSQL QA terisolasi. Tidak ada transaksi keuangan produksi yang dibuat. Status **PASS** di bawah berlaku untuk QA lokal dan gabungan bukti yang disebut, bukan klaim bahwa perjalanan finansial produksi sudah diuji.

## Koreksi baseline kumulatif

Ringkasan 28 Oktober di [hasil uji keuangan](hasil-uji-keuangan-2026-10-28.md) menghitung ulang dari [tindak lanjut pertama](hasil-uji-tindak-lanjut-2026-10-27.md), sehingga melewatkan [remediasi](hasil-uji-remediasi-2026-10-27.md) dan [uji paralel](hasil-uji-paralel-2026-10-27.md) yang sudah selesai sebelumnya. Ini kesalahan rekap, bukan regresi produk atau hasil tes yang dibatalkan. Perhitungan dari baseline 162 ID, dengan perubahan status berurutan sesuai empat laporan tersebut, menghasilkan **69 PASS, 90 PARTIAL, 0 NOT RUN, 3 BLOCKED** sebelum putaran ini; untuk 124 P0: **59 PASS, 62 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Semua 162 ID tetap ada. Perubahan status yang pernah dicatat tidak diwarisi sebagai bukti produksi.

## Pengujian tambahan oleh agen utama

| ID | Hasil | Bukti dan batas |
|---|---|---|
| ADM-03 (P0) | **PASS QA** | Enam tes HTTP pada source saat ini lulus: proteksi Superadmin terakhir saat diturunkan/dinonaktifkan, termasuk Superadmin lain yang masih undangan dan dua mutasi serentak, serta penolakan email admin/cabang duplikat. Unit HTTP dan PostgreSQL QA sama-sama dipakai. Log `/opt/temp/grahita-qa-adm03-2026-10-28.log`. |
| TMP-25 (P1) | **PASS QA** | Tes PostgreSQL nyata membandingkan setiap baris XLSX piutang dengan overview Admin/Cabang (periode, due, total, terbayar, sisa, status, umur), membuktikan bukti pending tidak mengurangi piutang, serta menolak endpoint ekspor untuk Manager dan cabang dengan HTTP 403. Pemeriksaan peran ditambahkan ke tes integrasi. Log `/opt/temp/grahita-qa-tmp25-2026-10-28.log`; ekspor XLSX dan layar kedua portal pada data QA yang sama sudah dibuktikan di [hasil uji keuangan](hasil-uji-keuangan-2026-10-28.md), REP-10. |
| TMP-02 (P0) | **PASS QA** | HTTP persetujuan Tempo dengan jam WIB tetap pada 4 November, 5 November pukul 23:59:59, dan 6 November pukul 00:00:00 mengembalikan due 5 November, 5 November, dan 5 Desember. Order dan satu charge pada PostgreSQL memuat due yang sama serta status `approved_unpaid`. Tes fungsi tanggal termasuk pergantian tahun juga lulus. Log `/opt/temp/grahita-qa-tmp02-2026-10-28.log` dan `/opt/temp/grahita-qa-tmp02-unit-2026-10-28.log`. Percobaan build pertama bertepatan dengan edit backend yang belum selesai dan tidak dipakai sebagai hasil aplikasi; run ulang sesudah edit selesai lulus. |
| TMP-05 (P1) | **PASS QA** | Cabang Tempo mengirim order pertama saat tanggal bayar master 5. Superadmin mengubah tanggal bayar melalui API Admin menjadi 10 pada periode yang sama, lalu cabang mengirim order kedua. Snapshot di PostgreSQL masing-masing tetap 5/10; persetujuan via HTTP menghasilkan due 5/10 November pada order serta charge. Log `/opt/temp/grahita-qa-tmp05-2026-10-28.log`. |
| DOC-13 (P0) | **PASS komposit QA** | Perjalanan Transfer R1 pada [hasil uji keuangan](hasil-uji-keuangan-2026-10-28.md) benar-benar mengirim email revisi ke cabang dan admin, keduanya melampirkan PDF R1 yang hash-nya sama. Outbox memuat subjek `revisi v1`, dokumen invoice dan Surat Jalan R1 tersedia, sedangkan email Surat Jalan hanya ada untuk versi awal. Tes `internal/fulfillment` pada source saat ini menegaskan email invoice untuk versi baru, penjaga idempoten, dan dokumen Surat Jalan versi baru. Bukti `/opt/temp/grahita-qa-finance-2026-10-28/e2e06-transfer-email-after-fix-db.log`, `e2e06-transfer-mailpit-after-fix.log`, serta `/opt/temp/grahita-qa-doc13-2026-10-28.log`. |
| TMP-26 (P1) | **PASS komposit QA** | Layar Cabang/Admin pada [hasil uji keuangan](hasil-uji-keuangan-2026-10-28.md) menampilkan Surat Tagihan v2 dan v1 berlabel versi lama dengan tombol unduh; uji TMP-07 mengunduh v1 setelah v3 dan hash PDF lama tetap. Riwayat bukti pembayaran milik cabang dapat dibuka; tes PostgreSQL/HTTP pada source saat ini memberi 200 untuk pemilik Surat Tagihan, 404 dan daftar kosong untuk cabang lain. Bukti `/opt/temp/grahita-qa-finance-2026-10-28/tmp08-statement-versions.log`, `/opt/temp/grahita-qa-branch-2026-10-27/proof-history.log`, dan `/opt/temp/grahita-qa-tmp26-2026-10-28.log`. |

## Pengujian paralel portal cabang

| ID | Hasil | Bukti dan batas |
|---|---|---|
| MAS-09 (P1) | **PASS QA** | Katalog browser menampilkan produk aktif yang memiliki harga cabang, termasuk nama, deskripsi, harga dan penanda Sertifikat; produk tanpa harga untuk cabang tersebut serta produk nonaktif tidak bocor. Pencarian nama/deskripsi, jumlah hasil, hapus pencarian, kondisi tanpa hasil dan layout tanpa overflow diuji pada lebar 1366, 390 dan 320 piksel. Log `/opt/temp/grahita-qa-branch-2026-10-28/catalog-retest.log`, `catalog-visibility.log`, dan `explore.log`. |
| ORD-03 (P0) | **PASS QA** | Browser mencegah draft kosong; API menolak draft kosong dan campuran Sertifikat/reguler dengan HTTP 400 serta tidak membuat order. Produk nonaktif dan produk tanpa harga cabang tidak tersedia di UI dan percobaan API langsung ditolak 400. Log `/opt/temp/grahita-qa-branch-2026-10-28/orders-browser.log` dan `orders-unavailable.log`. |
| ORD-04 (P0) | **PASS QA** | Dua baris Sertifikat dengan nama instansi berbeda disimpan bernilai Rp0, hanya memiliki satu baris packaging, dan baris packaging tidak dapat dihapus. Detail API mempertahankan kedua nama instansi dan kuantitas. Log `/opt/temp/grahita-qa-branch-2026-10-28/orders-browser.log`. |
| ORD-05 (P0) | **PASS QA** | Packaging Rp3.000 pada draft berubah menjadi Rp5.000 saat submit setelah master diubah; perubahan master berikutnya ke Rp7.000 tidak mengubah order submitted dan hanya berlaku pada draft baru. Log `/opt/temp/grahita-qa-branch-2026-10-28/orders-browser.log`. |
| ORD-07 (P0) | **PASS QA** | Submit Transfer tanpa bukti ditolak dan order tetap draft tanpa nomor. Submit ulang melalui UI dengan dua bukti sah berhasil menjadi `submitted`, memperoleh nomor, tanggal WIB dan serial, serta kedua bukti tersimpan dengan hash berbeda. Log `/opt/temp/grahita-qa-branch-2026-10-28/ord07-transfer.log`. |
| ORD-13 (P0) | **PASS QA** | Tombol ubah/hapus tidak tersedia setelah submit, URL ubah memberi penjelasan terkunci, dan request PUT/DELETE langsung ditolak 409. Seluruh line, nama instansi, serial, qty, harga dan total tetap sama. Log `/opt/temp/grahita-qa-branch-2026-10-28/orders-browser.log`. |
| DOC-11 (P1) | **PASS komposit QA** | Browser membuktikan filter periode inklusif, invoice Tempo `approved_unpaid`, unduh v0/v1, label versi, reset filter, dan PDF kedua versi memiliki hash berbeda. Tes PostgreSQL/HTTP setelah perbaikan membuktikan daftar versi 0/1/2 memakai total dan jumlah item dari snapshot masing-masing, bukan total order terbaru. Log `/opt/temp/grahita-qa-branch-2026-10-28/doc11-browser.log` dan `/opt/temp/grahita-qa-partial-combined-2026-10-28.log`. |

## Pengujian paralel backend dan operasional

| ID | Hasil | Bukti dan batas |
|---|---|---|
| DOC-16 (P1) | **PASS QA** | Renderer dan storage dibuat gagal untuk order Transfer `done` serta Tempo `approved_unpaid`. Status bisnis/piutang bertahan, tidak ada metadata dokumen atau email prematur; retry setelah dependency pulih menghasilkan tiga dokumen dan tiga email per order tanpa duplikasi pada retry berikutnya. Log `/opt/temp/grahita-qa-partial-combined-2026-10-28.log`. |
| DOC-18 (P1) | **PASS QA** | Invoice v0 dibuat dengan renderer bertanda template lama, lalu v1 dengan template baru. Byte v0 tetap identik, URL unduh masing-masing menunjuk objek yang benar, kedua berkas dapat diambil penuh dan tetap berawalan `%PDF-`. Log `/opt/temp/grahita-qa-doc18-2026-10-28.log`. |
| OPS-09 (P1) | **PASS QA** | Tanggal/UUID/angka salah bentuk, status HTML, teks pencarian 201 karakter, catatan melebihi 2.000 karakter dan ID non-UUID mengembalikan 4xx terstruktur. Teks mirip SQL/HTML tersimpan sebagai data tanpa mengubah tabel. Halaman sangat besar yang sebelumnya memicu PostgreSQL `OFFSET must not be negative` kini ditolak 400 pada pesanan dan laporan. Validasi tipe berkas bukti juga lulus. Log `/opt/temp/grahita-qa-backend-ops-2026-10-28/ops09-full.log`. |

## Rekap setelah putaran ini

Sebanyak **16 skenario** berubah dari PARTIAL menjadi PASS. Rekap seluruh 162 ID menjadi **85 PASS, 74 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Untuk 124 skenario P0 menjadi **67 PASS, 54 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Tidak ada skenario yang diturunkan statusnya.

Skenario OCR provider nyata VER-08/09/10 tetap **BLOCKED** sampai key Gemini diisi oleh Superadmin dan bukti uji yang sesuai diproses melalui provider nyata. OPS-05 tetap **PARTIAL** selama cadangan terenkripsi offsite dan jadwal produksi belum dikonfigurasi serta diuji pulih.

## Quality gate perubahan

- Seluruh paket Go: **PASS** (`/opt/temp/grahita-go-test-all-2026-10-28.log`).
- Delapan tes integrasi PostgreSQL yang mencakup perbaikan dan skenario Tempo terkait: **PASS** (`/opt/temp/grahita-qa-partial-combined-2026-10-28.log`).
- `go vet`, build server, pemeriksaan rahasia, dan pemeriksaan migrasi kompatibel: **PASS**.
- Typecheck kedua portal, 9+14 berkas tes portal (77+106 tes), paket UI/API client, dan build produksi kedua portal: **PASS** dengan `NODE_ENV` tes yang benar (`/opt/temp/grahita-web-gates-2026-10-28.log`).
- OpenAPI 3.1: **valid** setelah `nullable` lama pada status kunci OCR disesuaikan ke union `string|null`; 10 warning dokumentasi lama tetap tidak memblokir (`/opt/temp/grahita-openapi-lint-2026-10-28.log`).
