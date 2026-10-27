# Remediasi dan uji lanjutan Grahita — 27 Oktober 2026

Dokumen ini melanjutkan [baseline 162 skenario](hasil-uji-e2e-2026-10-27.md) dan [tindak lanjut pertama](hasil-uji-tindak-lanjut-2026-10-27.md). Status hanya dinaikkan ketika variasi yang tertulis pada skenario diuji; hasil unit, integrasi, browser, PDF, dan produksi dibedakan. Transaksi dalam dokumen ini memakai database dan storage QA terisolasi.

## Temuan yang diperbaiki

- **Pembayaran Tempo langsung pada pesanan:** detail pesanan Tempo yang sudah dikirim semula menampilkan unggah bukti transfer reguler. Bukti itu dapat tersimpan, tetapi verifikasi reguler gagal 500 karena constraint snapshot Tempo. Portal cabang kini mengarahkan pelunasan ke **Tagihan**. API menolak unggah bukti reguler untuk Tempo dengan 409, tetap mengembalikan 404 untuk pesanan cabang lain, dan mencegah draft dengan bukti reguler dikirim sebagai Tempo. Verifikasi reguler terhadap bukti lama pada Tempo juga ditolak 409 sebelum perubahan ekonomi.
- **Batas instansi Unicode:** batas 160 karakter kini dihitung sebagai karakter Unicode, sehingga emoji/simbol non-BMP tidak terpotong pada karakter ke-80 oleh batas UTF-16 bawaan HTML. API tetap menjadi batas akhir.
- **Akun dan aksesibilitas:** pendaftaran menolak telepon berhuruf di klien dan server. Pesan galat/hint dari komponen `Field` ditautkan ke kontrol melalui `aria-describedby` tanpa menghapus deskripsi yang sudah ada.
- **Data lama dan nominal nol:** instansi kosong di XLSX Surat Jalan/laporan tampil `—`; approval Sertifikat Tempo Rp0 tercatat `done` di audit sesuai status sebenarnya.

## Skenario yang ditutup pada QA

| Skenario | Status baru | Bukti utama |
|---|---|---|
| AUT-05 (P0) | **PASS** | Token salah/kedaluwarsa/terpakai, cabang nonaktif/berakun ditolak; regenerasi melalui API admin mencabut token lama, validasi lama 400 dan token baru 200, tanpa akun ganda. |
| AUT-12 (P0) | **PASS** | Isolasi order, bukti, invoice dan deposit pada tes sebelumnya dilengkapi Surat Tagihan: pemilik mendapat tautan 200, cabang lain 404, daftarnya kosong. |
| ORD-02 (P0) | **PASS** | Browser + API: kosong/spasi/161 karakter dan qty 0/negatif/terlalu besar ditolak; 160 karakter Unicode diterima; teks mirip HTML ditampilkan sebagai teks. |
| ORD-12 (P0) | **PASS** | HTTP + PostgreSQL dengan jam WIB terkendali: 23:59:59, 00:00:00, dan pergantian tahun menghasilkan tanggal, serial, nomor dan reset counter yang benar. |
| VER-05 (P0) | **PASS** | PostgreSQL: split nominal tak seimbang dan bukti yang dipakai ulang pada order lain ditolak; tepat satu keputusan ekonomi tersimpan. |
| TMP-01 (P0) | **PARTIAL** | Submit/approve pada batas WIB, periode Oktober dan due snapshot benar di HTTP + DB; interaksi browser tepat pada batas waktu belum dijalankan. |
| TMP-17 (P0) | **PASS** | Jumat mulai grace, Senin/Selasa diizinkan, Rabu kembali tertahan; bukti kedua tidak memperpanjang grace. |
| REP-01 (P0) | **PASS** | Browser + API: hari/minggu/bulan/tahun, filter cabang, angka, dan URL setelah reload cocok pada data QA. |
| OPS-04 (P0) | **PARTIAL** | Interupsi saat unggah dokumen, Surat Tagihan dan ekspor lalu pemrosesan ulang lulus; klaim email yatim pulih. Belum ada kill proses worker nyata untuk tiap jenis. Email tetap bersemantik *at least once* bila SMTP menerima sebelum status tersimpan. |
| AUT-06 (P1) | **PASS** | UI + API/DB: sandi pendek, konfirmasi beda, telepon tidak sah/terlalu panjang/kosong, field wajib kosong ditolak tanpa akun baru atau token terpakai. |
| MAS-10 (P1) | **PASS** | Produk lama diganti nama/nonaktif; snapshot tetap pada API pesanan, laporan, XLSX dan PDF. |
| ORD-19 (P1) | **PASS** | Instansi historis kosong tampil `—` di detail cabang, laporan admin, XLSX dan PDF Chromium/Gotenberg nyata; unduhan PDF sah. |
| TMP-23 (P1) | **PASS** | H-3 Rp100.000, koreksi sebelum H membuat pengingat H Rp70.000, setelah lunas H+1/H+7 tidak dikirim; job berulang tidak menggandakan outbox. |
| TMP-27 (P1) | **PASS** | Sertifikat + packaging Rp0 disetujui langsung `done`; sisa piutang nol, dokumen tetap terbit dan audit sesuai status. |
| DOC-17 (P1) | **PASS** | HTML/script, kutip dan `&` ter-escape pada template; Invoice dan Surat Jalan menjadi PDF nyata, tersimpan, dan endpoint unduh mengembalikan tautan ke objek yang benar. |
| REP-04 (P1) | **PASS** | Grafik/peringkat/komposisi dan CSV browser dibandingkan dengan API, termasuk filter Bandung tanpa data Bekasi. |
| REP-06 (P1) | **PASS** | API/DB: multi-cabang, jenis, C&C, PI, tanggal, status, tiga halaman paginasi tanpa duplikat, dan filter invalid 400. |
| OPS-08 (P1) | **PARTIAL** | Browser memakai keyboard: fokus terlihat, urutan login, editor, mode bayar, bukti dan keputusan admin berlabel; error form terhubung ke input. Pembaca layar nyata dan audit kontras menyeluruh belum dilakukan. |
| OPS-11 (P2) | **PASS** | Browser + API: putus jaringan saat simpan tidak mengaku sukses, retry pulih, edit tab lama setelah tab lain mengirim pesanan ditolak, dan refresh saat GET tertunda pulih. |

Setelah 19 skenario di atas diperbarui, hasil **QA lokal** menjadi **51 PASS, 0 FAIL, 108 PARTIAL, 0 NOT RUN, 3 BLOCKED** dari 162 skenario. Untuk 124 P0: **41 PASS, 0 FAIL, 80 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Angka ini tidak berarti seluruh alur produksi lulus.

## Gerbang teknis dan keterbatasan

- Tes PostgreSQL terarah untuk guard Tempo, registrasi, batas WIB, pengingat, laporan, koreksi, pemulihan job, dan PDF/unduh lulus. Backup terenkripsi database + objek dan restore berhasil pada PostgreSQL QA; kebijakan backup produksi tetap mengikuti operator.
- PDF nyata Invoice dan Surat Jalan untuk 1, 11, 22, 23, 29 dan 100 baris lulus pada Gotenberg QA. Kasus 11 baris mencakup instansi historis kosong; kasus 1 baris mencakup teks mirip HTML/script.
- Tes frontend 9 API client, 66 komponen UI, 100 admin, dan 77 cabang lulus. Run gabungan saat beban QA tinggi sempat gagal lima asersi asinkron admin; keempat berkas gagal itu lulus saat diulang, lalu suite admin penuh lulus dengan satu worker. Typecheck dan build kedua portal lulus.
- `go vet`, kontrak OpenAPI, pemeriksaan migrasi kompatibel, pemindaian rahasia, dan `git diff --check` lulus. Run `go test -p 1 -count=1 -timeout 60m ./...` dengan PostgreSQL 18 QA **keluar 1**: paket HTTP API lulus dalam 1.712 detik dan backup lulus, tetapi tiga tes `internal/platform/database` gagal karena database dasar QA yang dipakai ulang sudah berisi bukti Tempo tanpa `order_id`. Paket database diulang pada database PostgreSQL 18 yang kosong dan **lulus seluruhnya** dalam 22 detik. Jadi semua paket telah lulus dengan fixture yang sesuai, tetapi satu perintah gabungan tadi tidak lulus; kegagalan itu tidak disembunyikan.
- Status rilis produksi dicatat pada laporan deploy operator sesudah rilis. Perubahan ini tidak menambah migrasi.
- OCR provider nyata tetap **BLOCKED** pada VER-08/09/10 karena kunci Anthropic QA belum tersedia. Produksi belum mempunyai cabang Tempo aktif, sehingga perjalanan finansial produksi tidak dibuat dengan transaksi sintetis.

## Artefak QA

Log dan screenshot berada di `/opt/temp/grahita-qa-followup-2026-10-27/`, termasuk `full-go-pg18-real.log`, `migration-pg18-clean.log`, `tempo-proof-guard-go.log`, `aut05-token-rotation.log`, `aut12-statement.log`, `aut06-api.log`, `aut06-ui.log`, `wib-period-http-go.log`, `tmp23-go.log`, `doc17-real-api-rerun.log`, `rep04-dashboard-filter.png`, `ops08-register-error.png`, `ops11-stale-draft.png`, dan `pdf-real/`.
