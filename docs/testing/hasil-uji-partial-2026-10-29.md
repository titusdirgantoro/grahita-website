# Hasil uji ulang skenario PARTIAL — 29 Oktober 2026

Pengujian dimulai dari commit `25f1f2fca` dengan baseline kumulatif **95 PASS, 64 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Data dan bukti API/PostgreSQL lama yang dipakai sebagai bukti komposit berasal dari QA terisolasi tanggal 27–28 Oktober; putaran ini tidak membuat transaksi di produksi.

## Skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| AUT-09 | **PASS komposit QA** | UI Profil Cabang 3/3 dan Profil Admin 2/2 lulus. Digabung dengan bukti API/PostgreSQL sebelumnya: password lama wajib, perubahan password berhasil, sesi lain dicabut, nama dapat diubah, serta email/role/data master tetap read-only. |
| CAB-08 | **PASS komposit QA** | UI membuktikan order Tempo lama tetap memakai snapshot setelah izin dicabut dan order baru hanya menawarkan metode yang masih aktif. Bukti browser dan PostgreSQL sebelumnya membuktikan snapshot order Tempo/Deposit lama tidak berubah serta pilihan order baru mengikuti izin terbaru. |
| DEP-07 | **PASS komposit QA** | Uji UI konflik `journal_version` memuat ulang rincian pembayaran dan tidak menampilkan klaim berhasil. Digabung dengan integrasi PostgreSQL sebelumnya: dua alokasi yang berebut saldo tidak membuat saldo negatif dan request dengan versi lama ditolak tanpa debit. |
| ADM-01 | **PASS komposit QA** | UI mencakup daftar akun cabang, status aktif/nonaktif, login terakhir, pengiriman reset, dan CSRF. Digabung dengan bukti API/PostgreSQL sebelumnya untuk nonaktif/login/reset sampai password baru. |
| ADM-02 | **PASS setelah perbaikan UI** | Portal Admin kini menyediakan **Kirim ulang undangan** untuk akun berstatus undangan. Uji UI mencakup resend, role, dan flag penerima dokumen; bukti API/PostgreSQL sebelumnya mencakup undangan, aktivasi, resend idempoten, dan penolakan akun nonaktif. |
| ADM-04 | **PASS komposit QA** | Delapan uji Pengaturan dan lima uji OCR/Gemini lulus untuk setting utama, validasi, RBAC Superadmin, field API key, serta status konfigurasi. Digabung dengan bukti API/PostgreSQL sebelumnya untuk audit dan snapshot transaksi lama. |

## Hasil gerbang

- Portal Cabang penuh: **81/81 PASS**.
- Portal Admin penuh: percobaan pertama **110/111** karena satu query fixture melewati waktu tunggu; test terkait langsung lulus **6/6**, area Profil/Pengguna/Pengaturan lulus **18/18**, lalu suite penuh ulang lulus **111/111**.
- Typecheck Portal Cabang dan Portal Admin: **PASS**.
- Paket `internal/httpapi` dengan seluruh test PostgreSQL baru: **compile PASS**.
- Unit order: **PASS**. Unit payment yang tidak memakai socket lulus pada run agen; run gabungan utama berhenti pada test provider yang membutuhkan `httptest` socket karena sandbox menolak bind.
- `gofmt`, `git diff --check`, pemeriksaan rahasia, dan pemeriksaan migrasi kompatibel: **PASS**.

Log pendukung utama:

- `/opt/temp/grahita-qa-branch-round3-customer-20261029.log`
- `/opt/temp/grahita-qa-branch-round3-admin-profile-20261029.log`
- `/opt/temp/grahita-qa-partial-admin-pengguna-20261029.log`
- `/opt/temp/grahita-qa-partial-backend3-payment-unit-20261029.log`
- `/opt/temp/grahita-qa-partial-backend3-order-unit-20261029.log`

## Tetap PARTIAL

Coverage baru sudah ditambahkan untuk DEP-08 (nominal eksplisit 70/30), ORD-08 (Deposit dengan saldo nol), VER-03, VER-04, dan DOC-02. Seluruhnya berhasil dikompilasi dan sisi UI yang terkait lulus, tetapi bukti PostgreSQL barunya belum dieksekusi karena sandbox putaran ini menolak Docker, TCP, dan Unix socket. Kelima ID tersebut **tidak dinaikkan** hanya berdasarkan kompilasi atau pemeriksaan source.

## Rekap kumulatif

Enam skenario berpindah dari PARTIAL menjadi PASS:

- Seluruh 162 skenario: **101 PASS, 58 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- P0 (124 skenario): **82 PASS, 39 PARTIAL, 0 NOT RUN, 3 BLOCKED**.

Tidak ada status BLOCKED yang diubah dan tidak ada klaim pengujian transaksi produksi pada putaran ini.

---

## Putaran lanjutan: PostgreSQL nyata dan regresi penuh

Putaran lanjutan dimulai dari rekap **101 PASS, 58 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Seluruh kenaikan di bawah didukung eksekusi test, bukan inspeksi source saja. Database yang dipakai adalah PostgreSQL terisolasi; tidak ada transaksi yang dibuat di produksi.

### Skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| DEP-01 | **PASS komposit QA** | UI membuktikan navigasi dan rute Deposit sesuai izin. PostgreSQL membuktikan isolasi data cabang, bukti cabang lain `404`, dan top-up cabang tanpa izin `403`. |
| DEP-08 | **PASS komposit QA** | PostgreSQL membuktikan order Rp100.000 memakai Deposit Rp70.000 dan transfer Rp30.000 tepat, lalu baru menjadi `done` setelah settlement lengkap. |
| DEP-09 | **PASS komposit QA** | PostgreSQL membuktikan cabang dan Manager dapat menambah alokasi pada order `submitted`; debit, saldo, sisa, actor, alasan, versi jurnal, dan idempotensi sesuai. |
| DEP-13 | **PASS komposit QA** | PostgreSQL membuktikan revisi turun wajib memakai reversal plan, penolakan melakukan rollback penuh, dan carry-forward settlement tetap konsisten. |
| DEP-14 | **PASS komposit QA** | Pending/review/verified direkonsiliasi antara ringkasan dan mutasi cabang, antrean/filter/detail/bukti Admin, serta database; retry verifikasi tidak menggandakan transaksi. |
| ORD-08 | **PASS komposit QA** | PostgreSQL membuktikan submit Deposit untuk saldo penuh, kurang, nol, versi stale, dan request paralel; jurnal tetap tepat sekali. |
| VER-03 | **PASS** | Enam bentuk payload salah menghasilkan `400` tanpa perubahan status, verification log, atau ledger; kontrol positif menghasilkan `200`. |
| VER-04 | **PASS** | Delapan verifikasi paralel menghasilkan tepat `1×200` dan `7×409`; hanya satu log, audit, render job, tiga dokumen, dan tiga email. Retry tidak membuat duplikasi. |
| MAS-01 | **PASS komposit QA** | CRUD produk reguler/Sertifikat, urutan, status, hapus, audit, dan aturan produk terpakai lulus pada UI, API, dan PostgreSQL. |
| MAS-02 | **PASS komposit QA** | Rumus HJM + P1 + SPV dan pembulatan lulus; input negatif/desimal kini ditolak UI dan server tanpa mengirim perubahan. |
| MAS-03 | **PASS komposit QA** | Produk tanpa harga hilang dari katalog Cabang dan ditolak saat order; produk aktif dan harga tetap terisolasi per cabang. |
| MAS-04 | **PASS komposit QA** | Salin harga ke satu/semua cabang, edit tujuan, audit, dan snapshot harga order lama lulus pada UI dan PostgreSQL. |
| TMP-24 | **PASS komposit QA** | Tiga charge berumur 10/40/70 hari, pembayaran FIFO Rp50.000, dan bukti pending Rp20.000 menghasilkan angka identik di API Cabang, API Admin, UI, dan DB: piutang/overdue Rp550.000; aging Rp300.000/Rp200.000/Rp50.000; pending Rp20.000. |
| DOC-02 | **PASS** | Revisi tanpa alasan, dari cabang, oleh Manager saat toggle mati, pada draft/submitted, ID invalid, dan request tanpa CSRF semuanya ditolak tanpa revision row atau audit liar. |

### Perbaikan yang ditemukan saat pengujian

- Editor tarif sebelumnya mengubah `-1` menjadi `1` dan `1.5` menjadi `15`. Parser tarif sekarang hanya menerima bilangan bulat non-negatif dan menampilkan galat inline.
- Ringkasan Tagihan Admin sebelumnya tidak menghitung total bukti Tempo pending, dan Portal Cabang belum menampilkan tiga kelompok umur piutang. Keduanya sekarang ditampilkan dan direkonsiliasi dengan database.
- Alasan alokasi Deposit oleh Admin sebelumnya divalidasi tetapi tidak disimpan. Alasan sekarang masuk jurnal dan fingerprint idempotensi.

### Bukti eksekusi utama

- PostgreSQL untuk DEP-01/09/13/14: **4/4 PASS**, paket selesai dalam **30,534 detik**.
- PostgreSQL untuk VER-03/04 dan DOC-02: **4/4 PASS**, paket selesai dalam **39,671 detik**. Log: `/opt/temp/grahita-qa-verdoc-pg-round4-20261029.log`, SHA-256 `96b5499de811d016064c31da9a3b397d81a0ad3719f8537350ba7c33b9baf0f8`.
- PostgreSQL untuk katalog: **5/5 PASS**.
- PostgreSQL untuk TMP-24: **1/1 PASS**; UI Admin dan Cabang masing-masing **1/1 PASS**.
- Portal Admin penuh: **16 file, 115/115 PASS**.
- Portal Cabang penuh: **10 file, 83/83 PASS**.
- Typecheck dan build produksi Portal Admin/Cabang: **PASS**.
- Semua paket Go selain `internal/httpapi`: **PASS** pada run penuh. Run pertama `internal/httpapi` gagal tiga test karena volume Docker tidak memuat `openapi/openapi.yaml`; pengulangan dengan seluruh root repo terpasang lulus dalam **58,506 detik**.
- `go vet ./...`, `gofmt`, `git diff --check`, pemeriksaan rahasia, dan pemeriksaan migrasi kompatibel: **PASS**.

### Rekap terbaru

Empat belas skenario berpindah dari PARTIAL menjadi PASS:

- Seluruh 162 skenario: **115 PASS, 44 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- P0 (124 skenario): **94 PASS, 27 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- P1 (38 skenario): **21 PASS, 17 PARTIAL, 0 NOT RUN, 0 BLOCKED**.

Tiga status BLOCKED tidak diubah. Skenario PARTIAL lain juga tidak dinaikkan tanpa bukti lengkap sesuai kanal UI/API/DB yang diminta matriks.

---

## Putaran lanjutan kedua: 44 PARTIAL diuji ulang

Putaran ini dimulai dari **115 PASS, 44 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Daftar 44 ID direkonstruksi dari status baseline dan seluruh perubahan status pada laporan 27–29 Oktober sebelum pengujian dimulai. Seluruh PostgreSQL memakai database terisolasi; produksi tidak diberi transaksi sintetis.

### Empat belas skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| MAS-05 | **PASS komposit QA** | UI Cabang menampilkan snapshot order; HTTP/PostgreSQL membuktikan master berubah setelah draft, submit memakai harga/nama terbaru saat itu, lalu perubahan master kedua tidak mengubah detail API atau baris DB order submitted. |
| MAS-06 | **PASS komposit QA** | UI Admin membuat, mengubah, menonaktifkan, dan menetapkan rekening per cabang. API/PostgreSQL membuktikan hanya rekening aktif/diizinkan muncul pada Cabang dan invoice baru. |
| MAS-07 | **PASS komposit QA** | Tanpa rekening efektif: Transfer, Deposit kurang, dan approval Tempo ditolak; Deposit yang seluruhnya tertutup saldo berhasil tanpa bukti/rekening. Penolakan tidak mendebit saldo atau memakai nomor. |
| MAS-08 | **PASS komposit QA** | Deaktivasi/assignment rekening, unggah, submit, verifikasi, race versi, dan reevaluasi policy diuji pada UI/API/PostgreSQL. Rekening tercabut tidak diterima dan job stale tidak membuat keputusan baru. |
| VER-01 | **PASS komposit QA** | Dua bukti Rp100.000 + Rp150.000 tersimpan sebagai dua split dengan rekening efektif dan snapshot masing-masing; hanya satu log, audit, job, dan set dokumen. |
| VER-02 | **PASS komposit QA** | Kurang bayar, lebih bayar, nominal tidak terbaca, dan rekening berbeda semuanya tetap `submitted/perlu_review` dengan alasan tepat, tanpa dokumen atau email prematur. |
| VER-07 | **PASS komposit QA** | UI preview/zoom, upload Admin, catatan internal, timeline dan log lulus. PostgreSQL membuktikan catatan tidak bocor ke API Cabang dan tautan unduh bukti hanya tersedia melalui endpoint berizin. |
| VER-13 | **PASS komposit QA** | Manager tidak dapat mengkredit lebih bayar; Superadmin dengan idempotency key dapat membagi nilai ke order dan Deposit tepat sekali. Replay aman dan key dengan payload berbeda konflik. |
| DOC-01 | **PASS komposit QA** | Revisi menghapus baris lama dan menambah produk baru melalui UI/HTTP; nomor invoice tetap, versi naik, serial lama bertahan, serial terhapus hilang, dan serial/nama instansi baru benar. |
| DOC-03 | **PASS komposit QA** | Revisi sebelum render ditolak tanpa mutasi liar; dua revisi paralel hanya memberi satu pemenang, retry sah menghasilkan versi berikutnya, dan worker idempoten menjaga PDF/email tiap versi. |
| DOC-15 | **PASS komposit QA** | Chip dan riwayat email gagal tampil; RBAC, klik ganda, audit dan resend diuji. Setelah worker pulih, penerima dan byte lampiran tetap sama. |
| CAB-04 | **PASS komposit QA** | Filter, pencarian, status, detail, kepala cabang, telepon dan PI lulus di UI/API/DB. Audit benar dan flag Special tidak muncul pada UI, respons, atau XLSX. |
| REP-07 | **PASS komposit QA** | UI memantau `queued → proses → selesai`; worker River nyata menghasilkan XLSX dengan filter/baris/instansi/serial/rekap yang benar. Unduh baru tersedia setelah selesai dan RBAC berlaku. |
| ADM-07 | **PASS komposit QA** | Unit audit, API, dan PostgreSQL membuktikan password/hash/token/API key/cookie/CSRF/URL bertanda/storage key disaring sebelum DB dan pada respons. Cabang/Manager ditolak 403. |

### Skenario yang sengaja tetap PARTIAL

- **VER-11:** enqueue dan stale fingerprint/version sudah lulus, tetapi perjalanan job baru setelah bukti/policy berubah belum dibuktikan sampai verification log dan audit akhir.
- **REP-03:** chip menunggu, review, dan email gagal lulus, tetapi chip overdue belum dibandingkan dengan daftar detail memakai dataset yang sama.
- Lima ID Tempo yang diuji ulang oleh salah satu jalur sudah berstatus PASS pada putaran sebelumnya. Hasil ulangnya valid, tetapi tidak dihitung sebagai kenaikan baru.

### Gerbang akhir

- PostgreSQL MAS-05–08: **8/8 PASS**, paket **41,660 detik**. Log `/opt/temp/grahita-qa-mas-round5-20261029.log`.
- PostgreSQL VER-01/02/13 dan bukti pendukung: **6/6 PASS**, paket **25,440 detik**. Log `/opt/temp/grahita-qa-orders-round5-20261029.log`, SHA-256 `93da67b4893e04f669cd199f93f3beda440e1474ce62c1e16755565bf11a120a`.
- PostgreSQL DOC-01/03/15: **6/6 PASS**, paket **26,672 detik**, ditambah penguatan email **1/1 PASS**. Log `/opt/temp/grahita-qa-doc-r5-pg-20261029.log`.
- PostgreSQL CAB-04/REP-07/ADM-07: **8/8 PASS**, paket **57,933 detik**.
- Portal Admin penuh: **16 file, 121/121 PASS**. Portal Cabang penuh: **10 file, 83/83 PASS**.
- Semua paket Go, `go vet`, typecheck, build produksi kedua portal, pemindaian rahasia, migrasi kompatibel, format, dan `git diff --check`: **PASS**.
- Satu test Admin yang tidak terkait target sempat melewati batas waktu ketika tiga suite dijalankan bersamaan. Target terkait lulus pada run terarah dan suite Admin penuh kemudian lulus 121/121.

### Rekap terbaru

- Seluruh 162 skenario: **129 PASS, 30 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- P0 (124 skenario): **106 PASS, 15 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- Kelompok selain P0 (38 skenario): **23 PASS, 15 PARTIAL, 0 NOT RUN, 0 BLOCKED**.

PARTIAL P0 yang tersisa: `AUT-07`, `CAB-06`, `VER-06`, `TMP-01`, `TMP-19`, `TMP-28`, `DOC-04`, `DOC-10`, `REP-05`, `ADM-06`, `OPS-01`, `OPS-02`, `OPS-03`, `OPS-04`, dan `OPS-05`.

PARTIAL selain P0 yang tersisa: `CAB-10`, `ORD-14`, `ORD-16`, `ORD-18`, `VER-11`, `VER-12`, `REP-03`, `REP-08`, `ADM-05`, `ADM-08`, `OPS-06`, `OPS-07`, `OPS-08`, `OPS-10`, dan `OPS-12`.

---

## Putaran lanjutan ketiga: 30 PARTIAL diuji ulang

Putaran ini dimulai dari **129 PASS, 30 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Sebanyak 16 skenario dinaikkan hanya setelah jalur yang disyaratkan matriks benar-benar dieksekusi. PostgreSQL 18 terisolasi dipakai untuk pengujian transaksi; tidak ada data sintetis yang dibuat di produksi.

### Enam belas skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| AUT-07 | **PASS komposit QA** | Respons lupa kata sandi untuk akun ada/tidak ada identik, TTL mengikuti setting 7 menit, outbox dan worker mengirim email, token kedaluwarsa ditolak, token sah hanya dapat dipakai sekali, dan password baru dapat dipakai masuk. |
| CAB-06 | **PASS komposit QA** | Cabang yang memiliki order dan tiga dokumen historis dapat dinonaktifkan; sesi lama ditolak saat submit, login baru ditolak, sedangkan order, line, total, dokumen, dan akses histori Admin tetap utuh. |
| VER-06 | **PASS komposit QA** | Rekening milik cabang lain, rekening yang dinonaktifkan, dan assignment yang dicabut dievaluasi ulang saat keputusan. Jalur `cocok` ditolak `409`, jalur review tetap tersedia, dan snapshot hasil tujuan tersimpan di PostgreSQL. |
| TMP-01 | **PASS komposit QA** | Dengan jam bisnis tetap di batas 31 Oktober, persetujuan 2 November menghasilkan periode dan jatuh tempo yang benar di database; browser tidak mengirim tanggal/periode buatan klien. |
| TMP-19 | **PASS komposit QA** | Hold tunggakan, pelepasan melalui toggle Admin, audit aktor/waktu/before-after, dan submit langsung setelah pelepasan terbukti pada UI, API, serta PostgreSQL. |
| TMP-28 | **PASS** | Enam order bernilai besar membentuk piutang Rp300 miliar dan Rp600 miliar tanpa plafon tersembunyi. Due date dan toggle hold tetap menjadi satu-satunya aturan penahanan yang diuji. |
| DOC-04 | **PASS komposit QA** | Jalur Deposit nyata mencapai `done`, menitipkan satu River job, menghasilkan tiga dokumen DB/storage, dan retry worker tidak menggandakan dokumen atau email. Jalur Transfer/Tempo, kegagalan dependency, pemulihan, dan respons renderer `503` juga lulus. |
| DOC-10 | **PASS komposit QA** | Tautan unduh bertanda memiliki TTL 300 detik, tunduk pada RBAC, membuka objek storage yang benar, dan versi dokumen lama maupun baru tetap dapat diunduh melalui UI/API. |
| REP-05 | **PASS komposit QA** | Order `done` direvisi melalui API menjadi tiga line versi 1. Laporan hanya membaca versi berjalan, menjaga nama instansi dan serial, menghitung subtotal Rp250.000, serta menerapkan filter tanggal/cabang/status. |
| ADM-06 | **PASS komposit QA** | Sepuluh mutasi nyata mencakup cabang, produk/harga, rekening, setting, user, verifikasi, revisi, top-up, dan Tempo. Audit API/DB membuktikan aktor, waktu, before-after, filter, urutan, serta pagination. |
| OPS-01 | **PASS** | Router produksi membuktikan endpoint publik terbatas, endpoint bisnis meminta sesi, CSRF dan role ditolak tepat, origin asing tidak mendapat CORS, header keamanan lengkap, dan sesi lama tidak berlaku setelah logout. |
| CAB-10 | **PASS komposit QA** | Toggle hold dapat diubah melalui UI/API; PostgreSQL dan audit menyimpan aktor, waktu, serta perubahan `true` ke `false`, lalu submit yang sebelumnya tertahan berhasil. |
| ORD-14 | **PASS komposit QA** | Daftar, filter, detail, serial, nama instansi per line, label cara bayar, timeline, dan navigasi kembali terbukti di kedua portal. |
| ORD-16 | **PASS setelah perbaikan UI** | Portal Admin kini menyediakan unggah bukti multipart atas nama cabang. API/PostgreSQL membuktikan metadata pengunggah Admin, daftar, unduh bertanda, dan timeline aktor/nama. |
| ORD-18 | **PASS komposit QA** | Hitungan dan label `draft/submitted/approved_unpaid/done`, banner periode/sisa/hold, serta isolasi data berdasarkan sesi cabang terbukti tanpa parameter `branch_id` dari browser. |
| OPS-10 | **PASS komposit QA** | Empat pengujian PostgreSQL membuktikan settlement Transfer, Deposit 70/30, revisi total tetap/naik/turun, Tempo FIFO, pemisahan kredit dari Deposit, tanpa debit ganda, dan seluruh invariant non-negatif. |

### Bukti eksekusi utama

- PostgreSQL AUT-07/CAB-06/VER-06/TMP-01/TMP-19/TMP-28: **6/6 PASS**, paket **59,450 detik**. Log `/opt/temp/grahita-qa-business-r6-20261029.log`, SHA-256 `0fd1fa55bc4b7edc9ea211978c3b081fac1f6eb1f6b77d13bff1d47799c430a1`.
- PostgreSQL DOC-04/REP-05: **2/2 PASS**, paket **15,005 detik**. Log `/opt/temp/grahita-qa-doc04-rep05-round6-final-20261029.log`, SHA-256 `f40cb262bb0281e54661bff1d7a57e4bc72f602d83925d66946a8623d8db2c81`.
- PostgreSQL dependency DOC-04/DOC-10: **4/4 PASS**, paket **25,564 detik**. Log `/opt/temp/grahita-qa-doc04-doc10-round6-20261029.log`, SHA-256 `7af8a7ae1ba820e98a7f958bbb6d11c918ea2fb261575cc5c74fc8de9e136ba2`.
- PostgreSQL ADM-06: **10/10 PASS**, paket **74,111 detik**. Log `/opt/temp/grahita-qa-adm06-round6-20261029.log`, SHA-256 `3e50c22a0cf06fcc33a9c4c5f7bcb10c2bca688882ca270758a3526862441de3`.
- PostgreSQL CAB-10/ORD-16: **2/2 PASS**. Log `/opt/temp/grahita-qa-r6-uiops-pg.log`, SHA-256 `8d58d1222b267d8d2fdd25673e26517139583d5a9122d1dfbb65e4ae7bdaf925`.
- OPS-01: **PASS**. Log `/opt/temp/grahita-qa-ops01-round6-20261029.log`, SHA-256 `402cce2cbbeeb01e039d26088d5269959ff25726db7a990a88209d39f35ead52`.
- OPS-10: **4/4 PASS**. Log `/opt/temp/grahita-qa-ops10-round6-20261029.log`, SHA-256 `ab08610d68ef5efd219643a2395e10b9b140e9df7cb257a7cacab6b1910a6080`.
- Portal Admin penuh: run paralel pertama mengalami enam timeout pada tes lama saat mesin penuh; pengulangan tunggal dengan satu worker lulus **16 file, 123/123 PASS**.
- Portal Cabang penuh: **10 file, 85/85 PASS**.
- Seluruh paket Go, `go vet`, typecheck, build produksi kedua portal, pemindaian rahasia, migrasi kompatibel, format, dan `git diff --check`: **PASS**.

Percobaan Chromium tambahan untuk dokumen tidak dihitung karena image QA tidak memiliki `libglib-2.0.so.0`. Kegagalan awal pada beberapa test baru juga tidak dihitung; semuanya berasal dari fixture atau ekspektasi test dan telah dikoreksi sebelum run bersih di atas.

### Rekap terbaru

- Seluruh 162 skenario: **145 PASS, 14 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- P0 (124 skenario): **117 PASS, 4 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- Kelompok selain P0 (38 skenario): **28 PASS, 10 PARTIAL, 0 NOT RUN, 0 BLOCKED**.

PARTIAL P0 yang tersisa: `OPS-02`, `OPS-03`, `OPS-04`, dan `OPS-05`.

PARTIAL selain P0 yang tersisa: `VER-11`, `VER-12`, `REP-03`, `REP-08`, `ADM-05`, `ADM-08`, `OPS-06`, `OPS-07`, `OPS-08`, dan `OPS-12`.

Status BLOCKED `VER-08`, `VER-09`, dan `VER-10` tidak diubah. Tidak ada skenario tersisa yang dinaikkan berdasarkan inspeksi source atau asumsi.

---

## Putaran lanjutan keempat: 14 PARTIAL diuji ulang

Putaran ini dimulai dari **145 PASS, 14 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Sepuluh skenario dinaikkan setelah jalur penerimaan yang relevan benar-benar dieksekusi. PostgreSQL 18 dan layanan QA terisolasi dipakai untuk transaksi dan pengujian gangguan; pemeriksaan produksi bersifat baca saja.

### Sepuluh skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| VER-11 | **PASS** | Perubahan policy dan perubahan bukti sama-sama menghasilkan versi/fingerprint baru, keputusan akhir, dua verification log historis, dua audit verifikasi, dan status order `done`. Seluruh 2 subtest PostgreSQL lulus. |
| VER-12 | **PASS komposit QA** | Riwayat dan detail Admin mencakup nomor, cabang, serial, hasil verifikasi, C&C, kegagalan email, tanggal, kombinasi filter, grouping, pagination, detail, dan RBAC. API/PostgreSQL serta 53 uji UI dashboard/pesanan lulus. |
| REP-03 | **PASS komposit QA** | Jumlah dan nilai overdue dibandingkan pada dataset yang sama antara PostgreSQL, API, chip dashboard, dan daftar detail. Perilaku chip saat nol, tampil, dan diklik juga lulus di UI. |
| REP-08 | **PASS komposit QA** | Export besar dan kegagalan backend sudah dibuktikan pada worker; UI membuktikan job gagal tidak menyediakan unduhan, tombol ulang membuat job baru, dan job baru selesai. |
| ADM-05 | **PASS setelah perbaikan UI** | Pengaturan tutorial telah tersedia untuk Superadmin. Sanitasi, RBAC, penyimpanan, pembacaan terbaru, editor, dan tampilan tutorial diuji; tiga file UI terkait lulus **17/17**. |
| ADM-08 | **PASS** | Preview, evidence, apply paralel/idempoten, stale/tombstone, rollback/retry, konflik, audit, guard produksi, dan mutasi setelah preview yang fail closed lulus pada HTTP/PostgreSQL. Parser juga menolak hash, header, keputusan, cipher, dan key yang tidak sah. |
| OPS-02 | **PASS** | Delapan uji PostgreSQL membuktikan retry/race submit, verifikasi tepat sekali, replay top-up, alokasi paralel, replay reversal, retry revisi identik tanpa duplikasi, retry worker, dan pelunasan Tempo tepat sekali. |
| OPS-03 | **PASS** | Sembilan uji gangguan membuktikan rollback DB, konflik terlambat, queue fail closed, serta pembersihan objek storage sementara/duplikat tanpa state, audit, job, nomor, atau objek liar. |
| OPS-06 | **PASS** | Health/readiness produksi lulus secara baca saja. Prometheus dan Alertmanager QA dengan rule produksi nyata mengaktifkan alarm API down, worker down, antrean email, dan backup gagal sesuai durasi, serta mengirim empat firing alert ke webhook. |
| OPS-12 | **PASS** | Daftar Admin p95 116,469 ms untuk 25 baris dan 66,961 ms untuk 100 baris; 100 submit paralel menghasilkan 100 nomor unik tanpa gap; export 50 ribu baris selesai 33,445 detik dengan batch maksimum 500; invoice dan surat jalan nyata dirender Gotenberg dengan p95 3,046 detik dan 2,583 detik. |

### Tetap PARTIAL berdasarkan bukti yang belum lengkap

| ID | Alasan tetap PARTIAL |
|---|---|
| OPS-04 | Dependency, cancel, retry, held email, serta berbagai job dokumen/tagihan/export sudah lulus. Belum ada bukti proses worker benar-benar dihentikan dengan `SIGKILL` lalu River merebut kembali **setiap jenis job** yang sedang berjalan. Probe awal tidak mencapai job karena fixture schema River telah terhapus, sehingga tidak dihitung. |
| OPS-05 | Backup dan restore QA lulus end-to-end, termasuk `pg_dump`, dua objek, manifest, restore byte-identik, anonymize, pencatatan, alarm gagal, catch-up jadwal, dan UI Settings. Produksi belum mengonfigurasi target/age/restore dan `backup_runs` produksi masih kosong, padahal penerimaan mensyaratkan backup harian produksi tercatat. |
| OPS-07 | Ditemukan font Cyrillic ter-inline sebagai `data:font` dan diblokir CSP produksi. Konfigurasi build kedua portal sudah diperbaiki agar menghasilkan berkas font self-hosted dan lulus di Caddy dengan CSP produksi. Status tetap PARTIAL sampai modal submit/revisi, preview/unduh PDF, serta seluruh state loading/error/empty diuji pada build final di semua viewport yang disyaratkan. |
| OPS-08 | Navigasi keyboard, fokus, kontras, dan pemuatan font dengan CSP telah diuji. Status tetap PARTIAL karena belum ada eksekusi dengan screen reader nyata pada alur yang disyaratkan. |

### Bukti eksekusi utama

- VER-11 PostgreSQL: **2/2 PASS**. Log `/opt/temp/grahita-qa-r7-ver11.log`, SHA-256 `1ed61554bbcf06d2f0c6fded06ae79b108b5d4927c3ce7741bccb19ed8ecff3`.
- REP-03/VER-12 PostgreSQL: **2/2 PASS**; UI dashboard/pesanan: **53/53 PASS**. Log PostgreSQL `/opt/temp/grahita-qa-r7-rep03-ver12-pg.log`, SHA-256 `c6e850f0a8c9d94fe1274f5ebdb9dba50d97fce58524877fd013d17a152f5ec5`; log UI `/opt/temp/grahita-qa-r7-rep03-ver12-ui.log`, SHA-256 `494e619473066344046a29749f5b6dfe991bdc8517ae147de2794dd54c73e98e`.
- ADM-08 PostgreSQL/HTTP: **PASS** dalam 124,927 detik. Log `/opt/temp/grahita-qa-r7-adm08-pg.log`, SHA-256 `ca775d1f06e459af6192977d7d78cd0e74bb3b3291517cd4f465130951f0cf3f`; unit master import `/opt/temp/grahita-qa-r7-adm08-masterimport.log`, SHA-256 `8f1d155a083edd10a35172b7045f8c8f3b5ee02f108358e54b9d6164d6cfa4e0`.
- OPS-02 PostgreSQL: **8/8 PASS**. Log `/opt/temp/grahita-qa-ops02-round7.log`, SHA-256 `d667fa91aa7a69afcf29b82fec03c20fc28c01fb3fc2d5bda9722f29bd619518`.
- OPS-03: **9/9 PASS**. Log inti `/opt/temp/grahita-qa-ops03-04-round7-clean.log`, SHA-256 `6bb3af612a01b6cb9e57967227a0a8957325a2150eddfd340fa0362045e4dd2a`; queue `af53268d04085f0c07d93df3d1967555d94d7225d18c282e6a9e82ca270282d9`; storage cleanup `0a60506f4a9228806e618273725596c3b7f38086ca8487a370294a4b3fa1d3bc`.
- OPS-05 QA backup/restore: **PASS**. Log `/opt/temp/grahita-qa-ops05-round7.log`, SHA-256 `b4f09306861f066190122a2576a5330b544c084a9ea7af5d2bbbc85e29b015d0`. Status tidak dinaikkan karena syarat produksi di atas belum terpenuhi.
- OPS-06 dan OPS-12: artefak serta checksum lengkap tersimpan pada `/opt/temp/grahita-qa-round7-checksums.sha256`.
- Portal Admin: run pertama **124/126** karena dua tes lama Cabang tertahan di loading saat mesin penuh; test Cabang terarah kemudian **16/16 PASS** dan suite penuh satu worker **17 file, 126/126 PASS**.
- Portal Cabang penuh: **10 file, 85/85 PASS**. Typecheck dan build produksi seluruh workspace web: **PASS**.
- Seluruh paket Go: **PASS** pada run penuh sebelumnya dalam putaran ini. `go vet ./...` dengan Go 1.27, `gofmt`, `git diff --check`, pemindaian rahasia, dan migrasi kompatibel: **PASS**.

Build backend melalui tahap Docker resmi selesai mengompilasi, tetapi export image pemeriksaan pertama gagal karena disk host penuh. Cache build yang tidak dipakai dibersihkan, lalu `go vet` dijalankan langsung dengan image Go 1.27 dan exit 0. Kegagalan export tidak dihitung sebagai kegagalan aplikasi.

### Rekap terbaru

- Seluruh 162 skenario: **155 PASS, 4 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- P0 (124 skenario): **119 PASS, 2 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- Kelompok selain P0 (38 skenario): **36 PASS, 2 PARTIAL, 0 NOT RUN, 0 BLOCKED**.

PARTIAL yang tersisa: `OPS-04`, `OPS-05`, `OPS-07`, dan `OPS-08`.

Status BLOCKED `VER-08`, `VER-09`, dan `VER-10` tidak diubah. Tidak ada kenaikan status berdasarkan inspeksi source, asumsi, atau test yang belum selesai.

---

## Putaran lanjutan kelima: 4 PARTIAL diuji ulang

Putaran ini dimulai dari **155 PASS, 4 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Hanya OPS-04 yang dinaikkan setelah eksekusi `SIGKILL` proses worker nyata, recovery River, dan regresi implementasi job selesai. Produksi hanya diperiksa secara baca saja.

### Skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| OPS-04 | **PASS komposit QA** | Subprocess worker dibunuh dengan `SIGKILL` saat empat kind produksi berstatus `running`: dokumen, tempo daily/statement, ekspor, dan pemindaian email. Client River baru melakukan rescue/reclaim; seluruh job selesai pada attempt ke-2 dengan `river:rescue_count=1`, dua attempt row, tepat satu result row per kind, dan checksum hasil cocok. Regresi domain nyata dokumen/statement/export **4/4 PASS**, notification lease/claim/cancel **3/3 PASS**, dan package worker PASS. |

Email tetap memiliki semantik *at least once*: proses yang mati setelah SMTP menerima pesan tetapi sebelum `MarkSent` dapat mengirim ulang. Hasil OPS-04 membuktikan restart pada titik yang diuji menghasilkan satu versi/keputusan konsisten; hasil ini tidak menyatakan SMTP *exactly once* universal.

### Skenario yang tetap PARTIAL

| ID | Bukti valid dan batas yang masih ada |
|---|---|
| OPS-05 | E2E QA PostgreSQL 18 diulang bersih **2/2 PASS** untuk `pg_dump → age`, manifest bukti+dokumen, restore DB/objek byte-identik, anonimisasi, `backup_runs`, alarm gagal, dan guard nama DB. Produksi masih memiliki konfigurasi target/rclone/age/restore/WAL kosong, `backup_runs=0`, serta tidak memiliki file age, manifest, atau laporan restore. Timer lama hanya membuat `pg_dump -Fc` lokal plaintext dengan magic `PGDMP`, tanpa objek atau offsite. |
| OPS-07 | Build final kedua portal memakai CSP produksi dan font self-hosted tanpa pelanggaran. Login/fokus kedua portal, editor order line, serta modal submit tiga mode diuji pada 320/390/768/1366 px; upload PDF dan submit aktual berhasil, dan loading nyata terlihat. Status tetap PARTIAL karena matriks loading/error/empty seluruh viewport serta modal keputusan, revisi, dan preview/unduh PDF belum selesai dalam run final. Beberapa run berhenti pada timing harness; itu tidak dinilai sebagai kegagalan aplikasi. |
| OPS-08 | Orca 46.1, AT-SPI, Chrome headed, Xvfb, dan journey login→editor→cara bayar/upload→keputusan Admin benar-benar dijalankan. Namun speech log hanya berisi aktivasi screen reader, frame Chrome, dan dialog Translate; tidak ada label kontrol aplikasi. Retry kedua juga tidak menghasilkan speech label. Karena pembaca layar belum terbukti membaca alur aplikasi, status tidak dinaikkan. |

### Bukti eksekusi

- OPS-04 SIGKILL: `/opt/temp/grahita-ops04-sigkill-final.log`, SHA-256 `7d906a18fc333fa2aae8d91883dc66bbf9e9e20f12bc73c2eb1cbe8aae018f09`.
- OPS-04 domain: `/opt/temp/grahita-ops04-http-regression.log`, SHA-256 `4f32a6949d05a2652ed9492091fd7159b2251c8a68135766f8950be31a55f5b1`; notification `98c23dbb84a8f8b1793d393b430778d08a7a3316c4631270009a101e935cef95`; worker `22d973e71fefb94025bcc4ec410629cfbc4e4757d0a76a539c62a17c5c1e1f81`.
- OPS-05 QA: `/opt/temp/grahita-qa-r8-ops05-backup-restore-clean.log`, SHA-256 `d41760abc3ca810f596aec0332b3798463066a047867354408b9ef5a27319184`.
- OPS-05 produksi baca saja: `/opt/temp/grahita-qa-r8-ops05-production-config-readonly.log`, SHA-256 `b605deb20a73131fc6e6dab16d54b12d2fc6c3bb5b21f81e9c5b38a0ec170f47`.
- OPS-07/08: artefak dan checksum tersimpan di `/opt/temp/grahita-qa-round8-ops0708/checksums.sha256`. Log Orca pertama SHA-256 `5fd427a42a57620a846abcdf0f09d532ec04c1dee0b6ebf5090b0a6ca915d545`; retry `b6297f1e375d1fbc4c1e23afa541583970f142ec4e6587227f5a0131d75cb02e`.

Run pertama harness OPS-04 menyelesaikan recovery tetapi gagal pada assertion test karena JOIN menggandakan satu result terhadap dua attempt; assertion diperbaiki memakai `count(DISTINCT ...)` dan rerun bersih PASS. Run regresi gabungan pertama juga membuat dua package berebut `CREATE EXTENSION citext`; notification lulus, sedangkan HTTP gagal saat setup. HTTP kemudian dijalankan sendiri dan **4/4 PASS**. Kegagalan harness/setup tersebut tidak dihitung sebagai kegagalan produk.

### Rekap terbaru

- Seluruh 162 skenario: **156 PASS, 3 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- P0 (124 skenario): **120 PASS, 1 PARTIAL, 0 NOT RUN, 3 BLOCKED**.
- Kelompok selain P0 (38 skenario): **36 PASS, 2 PARTIAL, 0 NOT RUN, 0 BLOCKED**.

PARTIAL yang tersisa: `OPS-05`, `OPS-07`, dan `OPS-08`.

Status BLOCKED `VER-08`, `VER-09`, dan `VER-10` tidak diubah.

---

## Putaran lanjutan keenam: 3 PARTIAL dan 3 BLOCKED diuji ulang

Putaran ini dimulai dari **156 PASS, 3 PARTIAL, 0 NOT RUN, 3 BLOCKED**. Seluruh bukti Gemini memakai gambar transfer sintetis; tidak ada bukti pembayaran atau transaksi pelanggan yang dikirim ke provider. Database produksi hanya dipakai untuk membuka kunci terverifikasi secara baca saja. Keputusan dan mutasi pengujian dijalankan pada PostgreSQL 18 QA terisolasi.

### Skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| VER-08 | **PASS** | Gemini `gemini-2.5-flash-lite` nyata membaca bukti cocok sebagai Rp250.000, tanggal 29 Oktober 2026, BCA, rekening `9999000011`, keyakinan 1.0. Run kontinu provider nyata → object storage QA → OCR service → PostgreSQL QA menghasilkan `done/cocok`, satu log metode AI, hasil OCR, waktu baca, dan audit. |
| VER-09 | **PASS** | Provider nyata dan pipeline QA diuji untuk bukti buram, rekening/tanggal/nominal salah, nominal kurang/lebih, serta himpunan multi-bukti berbeda. Bukti buram dan bukti salah tetap `submitted/perlu_review`; tidak ada penolakan final. Regresi PostgreSQL membuktikan semua alasan review dan keputusan Admin sesudah review tetap tersedia. |
| VER-10 | **PASS** | Tiga gangguan dijalankan pada Job+DB QA: tabel scheduler River dibuat tidak tersedia saat submit, reader dibuat gagal, dan object storage dibuat gagal. Submit scheduler menjawab 500 tetapi pesanan tetap draft tanpa nomor/event, lalu retry setelah pemulihan menjawab 200 dengan tepat satu job. Gangguan reader/storage tidak menyimpan `ocr_at` atau log keputusan; retry setelah pulih selesai sukses. |

Status BLOCKED lama berasal dari Gemini HTTP 402 `RESOURCE_EXHAUSTED` saat kredit prabayar habis. Pada putaran ini seluruh panggilan provider nyata berhasil sehingga hambatan eksternal itu sudah tidak ada. Kunci produksi berstatus terverifikasi, tetapi setelan `ai.aktif` produksi masih `false`; OCR otomatis baru berjalan untuk transaksi produksi setelah Superadmin mengaktifkannya.

### Skenario yang tetap PARTIAL

| ID | Bukti valid dan batas yang masih ada |
|---|---|
| OPS-05 | Audit produksi baca saja mengonfirmasi timer lama berhasil membuat dump PostgreSQL lokal yang valid (`PGDMP`, 436 entri), tetapi konfigurasi target/rclone/age/restore/WAL kosong, `backup_runs=0`, tidak ada backup objek, `.age`, manifest, salinan offsite, atau restore drill produksi. Satu laporan restore yang ditemukan adalah fixture QA lama, bukan hasil produksi. |
| OPS-07 | Draft, submit transfer, upload PDF, build produksi+CSP, serta matriks dua portal × empat viewport × loading/empty/error **24/24 PASS**. Tes menemukan bug nyata: modal keputusan selebar 338 px pada viewport 320 px. `Modal.module.css` diperbaiki dengan batas lebar dan `min-width: 0`; build kedua portal PASS dan CSS hasil build memuat perbaikan. Status tetap PARTIAL karena modal revisi serta preview/unduh PDF pada build setelah perbaikan belum selesai; run Chrome terakhir crash/timeout karena tekanan resource host. |
| OPS-08 | Orca 46.1 + AT-SPI2 + Chrome headed 124 + input keyboard native benar-benar mengucapkan label login, produk baris, nama instansi, pilihan Transfer/Deposit/Tempo beserta state, unggah bukti, dan tombol Kirim pesanan. Status tetap PARTIAL karena kontrol keputusan Admin, announcement kuantitas/error, urutan fokus penuh, dan kontras belum tervalidasi tuntas. Ucapan dialog Google yang sempat tertangkap dikeluarkan dari bukti aplikasi. |

### Bukti eksekusi

- Provider Gemini nyata: `/opt/temp/grahita-gemini-live-fixtures/provider-results.jsonl`, SHA-256 `d6e5a3e11205fdd2c388ef46b1a285a9825a83af9f9064b660615318064891a8`.
- Provider nyata sampai Job+DB: `/opt/temp/grahita-gemini-live-fixtures/live-provider-job-db.log`, SHA-256 `e7041c0897627b8f1f0c8842f0172b90fa9168f95b66545dcc52ecb5d55b2f13`.
- Regresi OCR PostgreSQL: `/opt/temp/grahita-gemini-live-fixtures/httpapi-ocr-integration.log`, SHA-256 `22108872e245535ed6251d0d777c20d6f27601017aa5787767ace6e81171bb76`.
- Pemulihan scheduler/reader/storage: `/opt/temp/grahita-gemini-live-fixtures/failure-recovery-job-db.log`, SHA-256 `d436dff220680da72359320e208a0fcec3495393affaab8c3e695a41a07ed378`.
- Regresi domain kegagalan: `/opt/temp/grahita-gemini-live-fixtures/domain-failure-regression.log`, SHA-256 `789924c4bac5403f64abe2fc38c3284e0b278bd5ebf3fc6bc6f35d2d95ce458d`.
- OPS-05 produksi baca saja: `/opt/temp/grahita-qa-r9-ops05-production-readonly.log`, SHA-256 `c42705be9677ba16b43864d663bb3ee6a24df148b9dc26ac4b69c477f95aac60`.
- OPS-07: `/opt/temp/grahita-qa-round9b-ops07/checksums.sha256`, SHA-256 `931c0353dde87c1586da029d898d8b43ace31e81887a4944a2448d8351347438`.
- OPS-08: `/opt/temp/grahita-qa-round9-ops08/summary.log`, SHA-256 `7f9fe348013cd6d3af64aab164f29f12a8b09411ec9be2f211a6297bf184c4ae`; manifest SHA-256 `f112d333d6083a4d12633fb0b228a4f7cc9d7edb2f5bd8795d5458ffc6cda9b1`.

Harness provider nyata dan fixture QA sementara sudah dihapus setelah tes. Tidak ada data produksi yang dimutasi dan tidak ada API key pada log atau artefak.

### Rekap terbaru

- Seluruh 162 skenario: **159 PASS, 3 PARTIAL, 0 NOT RUN, 0 BLOCKED**.
- P0 (124 skenario): **123 PASS, 1 PARTIAL, 0 NOT RUN, 0 BLOCKED**.
- Kelompok selain P0 (38 skenario): **36 PASS, 2 PARTIAL, 0 NOT RUN, 0 BLOCKED**.

PARTIAL yang tersisa: `OPS-05`, `OPS-07`, dan `OPS-08`. Tidak ada skenario yang masih BLOCKED.

---

## Putaran lanjutan ketujuh: OPS-07 dan OPS-08 diuji ulang sampai final

Putaran ini dimulai dari **159 PASS, 3 PARTIAL, 0 NOT RUN, 0 BLOCKED**. Pengujian memakai build produksi kedua portal di belakang Caddy dengan CSP produksi, PostgreSQL 18 dan object storage QA terisolasi, Chrome for Testing 124 headed/headless, Orca 46.1, AT-SPI2, serta axe-core 4.10.3. Tidak ada data produksi yang dimutasi.

### Skenario yang selesai

| ID | Status baru | Bukti penutup |
|---|---|---|
| OPS-07 | **PASS setelah perbaikan UI** | Build final melewati **24/24** pemeriksaan state loading/empty/error untuk Portal Cabang dan Admin pada 320/390/768/1366 px. Modal kirim pesanan, keputusan Admin, preview/unduh PDF, dan revisi invoice melewati **16/16** pemeriksaan pada empat viewport yang sama tanpa overflow halaman, dialog, atau kontrol. Tautan PDF final menjawab HTTP 200 dengan `Content-Type: application/pdf`. Perbaikan batas lebar modal dari putaran sebelumnya tetap aktif. |
| OPS-08 | **PASS komposit QA setelah perbaikan kontras** | Audit axe WCAG A/AA pada editor cabang, modal kirim, detail Admin, dan modal keputusan menghasilkan **0 pelanggaran**. Sebanyak **26** titik fokus bernama berada di viewport; error `Qty setiap baris minimal 1.` hadir sebagai live alert. AT-SPI nyata mengenali dan mengaktifkan kontrol produk/Qty, tiga radio cara bayar, unggah bukti, rekening tujuan, Keputusan, nominal, alasan, catatan, Batal, dan `Tandai perlu review` dengan role yang tepat. Orca 46.1 mengumumkan `Verifikasi pembayaran dialog`; bukti Orca putaran sebelumnya tetap membuktikan ucapan label kontrol cabang. Dua masalah kontras nyata diperbaiki: hint 12 px dari rasio 2,73:1 dan subjudul Admin 4,39:1 menjadi warna yang melewati ambang AA, lalu seluruh audit diulang bersih. |

### Perbaikan yang diterapkan

- Hint komponen `Field` kini memakai `--text-muted`, bukan `--text-subtle` yang terlalu pucat pada permukaan putih.
- Token `--text-muted` digelapkan dari `#6d6d72` menjadi `#68686d`; rasionya terhadap canvas `#ededeb` naik dari 4,39:1 menjadi sekitar 4,73:1.
- Tidak ada perubahan alur transaksi. Fixture keputusan Admin ditutup melalui `Batal`; pengujian final tidak mengirim persetujuan atau keputusan review.

### Bukti eksekusi

- State responsif **24/24 PASS**: `/opt/temp/grahita-qa-ops0708-r10/02_states-results.json`, SHA-256 `8cb8936eb3694734ff1bd924f9da2f0dc1a5eb99cd7a98b3cd37d622c52dd12d`.
- Modal dan PDF **16/16 PASS**: `/opt/temp/grahita-qa-ops0708-r10/03_modals-results.json`, SHA-256 `85bdaafe69bf3654614a4c218324673a7c8e19a3f4a0b1b4b303c548314a380f`.
- Fokus, live alert, dan axe: `/opt/temp/grahita-qa-ops0708-r10/04_accessibility-results.json`, SHA-256 `69f6e422406bbf34f1089d5592d8ab82014923862f98b969232cad0b1c5adead`.
- AT-SPI alur error cabang: SHA-256 `843bd055514911e17171fdd92871bfe618df805809f5ddbf115738bfc96a53ab`; modal kirim: `0cacc033d1d13e946ef2e04b594c59781fdb31885b73f627e2105fae8558f076`; keputusan Admin: `a043c06f274d3d3dcb1b2ee24fe5bea68b8e9611b86e6963984d2c2a18ef8326`.
- Ucapan Orca untuk dialog keputusan Admin: `/opt/temp/grahita-qa-ops0708-r10/ops08-speech-admin-struct-lines.log`, SHA-256 `0d5c646af96a4168501fb91e2f18540c212f7f1fd37b53352a0501b0eb73ca42`.
- Shared UI **66/66 PASS** dan build produksi kedua portal PASS. Manifest seluruh bukti: `/opt/temp/grahita-qa-ops0708-r10/checksums.sha256`.
- Perbaikan kontras dirilis ke produksi pada commit `fab12904f`. Kedua portal, API meta, image layanan, restart count, token CSS final, dan log error setelah deploy diverifikasi di `/opt/temp/grahita-qa-ops0708-r10/production-postdeploy.log`, SHA-256 `f4506963769707ac1d4ec261be28b0a26f540655115e769839940860ce5dbcde`.

Percobaan unit pertama dijalankan dengan `NODE_ENV=production` dari lingkungan fixture sehingga React production tidak menyediakan `act`; run yang sah diulang dengan `NODE_ENV=test` dan lulus **66/66**. Percobaan dua sesi AT-SPI paralel juga dibuang karena registry aksesibilitas host bersifat tunggal; seluruh bukti final di atas berasal dari run sekuensial bersih.

### Rekap terbaru

- Seluruh 162 skenario: **161 PASS, 1 PARTIAL, 0 NOT RUN, 0 BLOCKED**.
- P0 (124 skenario): **123 PASS, 1 PARTIAL, 0 NOT RUN, 0 BLOCKED**.
- Kelompok selain P0 (38 skenario): **38 PASS, 0 PARTIAL, 0 NOT RUN, 0 BLOCKED**.

Satu-satunya PARTIAL yang tersisa adalah `OPS-05`, karena backup produksi yang terenkripsi, offsite, mencakup object storage, tercatat di `backup_runs`, dan pernah direstore masih belum tersedia. Tidak ada skenario yang masih BLOCKED.

## Putaran lanjutan kedelapan: OPS-05 dijalankan di produksi

Produksi kini menjalankan jalur backup aplikasi pada worker dengan target persisten dan enkripsi age. Run pertama mencatat `postgres`, `storage`, dan `wal` di `backup_runs`; dump PostgreSQL berukuran 927.326 byte terenkripsi dan sinkronisasi storage mencadangkan 12 dokumen berukuran total 224.967 byte. Seluruh 13 berkas payload memiliki header age yang valid, tidak ada objek storage polos di target, dan manifest memuat 12 objek.

Restore drill ke PostgreSQL 18 terisolasi memulihkan database serta **12/12 objek**, memakai potret jumlah baris pada saat dump, menghasilkan nol perbedaan tabel dan nol masalah checksum. Drill pertama menemukan bug nyata: `branch_users.email` belum disamarkan walaupun laporan lama menyatakan bersih. Implementasi dan uji regresi diperbaiki pada commit produksi `18c696d3`; drill kedua membuktikan email/telepon/waktu login pengguna cabang telah disamarkan, sesi dan token terhapus, IP audit kosong, dan seluruh pemeriksaan anonimisasi bernilai nol. Laporan final memiliki SHA-256 `415ed724735b192d035bff910c86a8fdf32c9fc83923e79ce85ad0679f502f2e`.

Timer `pg_dump` polos lama sudah dinonaktifkan. Worker terenkripsi aktif dengan jadwal harian 02:00 WIB. Kedua portal dan API merespons HTTP 200, API sehat, restart count nol, serta tidak ada panic/fatal/error setelah rilis.

Status OPS-05 tetap **PARTIAL secara jujur** karena target `/srv/grahita-encrypted-backups` masih berada pada filesystem VPS yang sama. Server belum memiliki konfigurasi atau kredensial rclone untuk Google Drive/S3 eksternal, sehingga salinan offsite belum dapat dibuktikan. Bukti produksi dan manifest checksum berada di `/opt/temp/grahita-qa-ops05-prod-r11/`.

Rekap tetap **161 PASS, 1 PARTIAL, 0 NOT RUN, 0 BLOCKED**. Satu-satunya kekurangan yang tersisa adalah salinan offsite OPS-05.
