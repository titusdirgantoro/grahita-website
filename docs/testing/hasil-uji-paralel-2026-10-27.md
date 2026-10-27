# Pengujian paralel dan remediasi lanjutan Grahita — 27 Oktober 2026

Laporan ini melanjutkan [hasil remediasi sebelumnya](hasil-uji-remediasi-2026-10-27.md). Dua agen memakai PostgreSQL dan storage QA yang terpisah; pemeriksaan produksi oleh agen utama hanya membaca data akun UJI. Status PASS di bawah berlaku pada QA lokal, bukan klaim transaksi finansial produksi.

## Hasil 162 skenario

| Status | Semua 162 | P0 dari 124 |
|---|---:|---:|
| PASS | **63** | **53** |
| FAIL | **0** | **0** |
| PARTIAL | **96** | **68** |
| NOT RUN | **0** | **0** |
| BLOCKED | **3** | **3** |

Dua belas skenario P0 naik dari PARTIAL menjadi PASS pada putaran ini. Tiga BLOCKED tetap VER-08/09/10 karena kunci Anthropic QA untuk menguji provider OCR nyata belum tersedia. Skenario PARTIAL tidak dinyatakan lulus hanya karena ada tes unit atau integrasi untuk sebagian variasinya.

## Temuan dan perbaikan produk

- **ORD-10 (P0):** API sebelumnya menerima dua `payment_mode` pada multipart yang sama dan diam-diam memakai nilai pertama. Permintaan `tempo` + `transfer` benar-benar menghasilkan order Tempo HTTP 200 di QA sebelum perbaikan. Parser sekarang menolak nilai ganda, termasuk dua nilai sama, dan menolak mode lewat query string. Empat variasi ambigu menghasilkan HTTP 400 sebelum order, nomor, serial, bukti, audit ekonomi, atau piutang berubah; satu mode sah tetap HTTP 200.
- **OPS-07 (P1, tetap PARTIAL):** portal Cabang `/tagihan` produksi melebar menjadi 419 px pada viewport 320/390 px karena pilihan rekening panjang memakai `<select>` tanpa batas lebar. Komponen `Select` bersama menggantikannya. Browser pada build baru menunjukkan `scrollWidth = clientWidth` di 320, 390, 768, dan 1366 px; pilihan rekening dan fokus tombol kirim bekerja. Fixture dengan dua periode dan empat bukti juga pas di 320/390 px. Skenario OPS-07 tetap PARTIAL karena modal submit/revisi, pratinjau PDF, serta seluruh keadaan loading/error belum ditinjau pada semua ukuran.

## Skenario yang naik menjadi PASS lokal

| ID | Bukti utama |
|---|---|
| E2E-05 (P0) | Tiga pilihan bayar dikirim pada order berbeda, snapshot lama bertahan setelah izin berubah; saat overdue, hanya Tempo tertahan, Transfer dan Deposit tetap dapat dikirim. |
| E2E-07 (P0) | FIFO lintas September–Oktober; lebih bayar diputuskan Superadmin menjadi kredit Tempo Rp50.000, lalu kredit melunasi order berikutnya tanpa menambah Deposit. |
| ORD-01 (P0) | Draft tiga baris multi-instansi: total live Rp147.500, simpan/muat ulang cocok, hapus satu baris menjadi Rp62.500. |
| ORD-06 (P0) | Simpan, ubah, muat ulang, dan hapus draft lain lewat UI; order lama tetap ada. |
| ORD-10 (P0) | Mode kosong/tidak diizinkan dan payload mode ganda ditolak; snapshot satu mode sah tersimpan. Regresi HTTP/PostgreSQL membuktikan penolakan sebelum mutasi. |
| TMP-06 (P0) | Job 31 Oktober belum menerbitkan, 1 November menerbitkan satu PDF dengan invoice/due/total tepat; email cabang dan admin ber-flag benar-benar terkirim pada SMTP QA. |
| TMP-07 (P0) | Approval terlambat dan koreksi membuat v1/v2/v3; job ulang tidak menduplikasi versi/email; unduh v1 tetap 200 dan byte PDF lama tidak berubah. |
| TMP-09 (P0) | Bukti Rp150.000 melunasi O1 Rp100.000 dan mengurangi O2 Rp50.000 lintas periode, cocok pada UI/API/DB. |
| TMP-10 (P0) | Dua bukti di BCA/BRI tetap pending tanpa mengurangi piutang; bukti BRI yang ditolak tercatat dengan alasan dan tanpa alokasi. |
| TMP-15 (P0) | Draft tetap bisa dibuat saat hold; submit Tempo disabled di UI dan ditolak API 409, sedangkan Transfer dan Deposit berhasil. |
| DOC-14 (P0) | Retry email 1m/5m/30m/2j/6j, SMTP gagal lalu pulih, lease `sending`, serta kemungkinan kirim ulang *at least once* terbukti pada simulasi QA. |
| REP-09 (P0) | Manager/cabang mendapat 403 saat meminta laporan, ekspor, status dan unduh dengan ID job ekspor Superadmin yang nyata; URL bertanda tangan tidak bocor. |

Periode lama pada E2E-07/TMP-09 dibuat dengan tanggal QA terkendali. DOC-14 menguji pemulihan lease tanpa membunuh proses worker OS nyata; OPS-04 tetap PARTIAL untuk uji `SIGKILL` lintas jenis pekerjaan.

## Bukti tambahan yang tetap PARTIAL

- **CAB-04:** pada produksi baca saja, filter Admin cocok dengan API: 35 cabang total, 1 C&C, 0 Tempo, 0 nonaktif; pencarian akun UJI menemukan satu cabang, detail nama/kepala/telepon/email cocok, dan label Special tidak terlihat. Mutasi profil/PI serta seluruh ekspor belum diuji pada data yang sama.
- **OPS-05:** backup PostgreSQL dan dua objek terenkripsi age, manifest, restore byte-identik, anonimisasi, dan alarm kegagalan lulus QA. Jadwal harian serta tampilan Pengaturan belum ditelusuri dalam satu perjalanan.
- **OPS-06:** `/healthz` tetap 200 dan `/readyz` menjadi 503 saat DB/storage/renderer gagal. Metrik dan 12 aturan alert lulus tes/promtool; pengiriman alarm melalui stack live belum diuji.
- **OPS-07:** 44 kombinasi halaman/ukuran pada kedua portal produksi dibuka tanpa galat JavaScript/HTTP 5xx; dua overflow Tagihan ditemukan dan diperbaiki pada build QA. Modal detail cabang Admin tetap dalam viewport pada 320/390/768 px. Cakupan keadaan lain masih terbatas seperti di atas.

## Gerbang dan batas

- Customer Portal: 77/77 tes lulus dengan `NODE_ENV=test`, typecheck dan build lulus. Percobaan awal dengan `NODE_ENV=production` dari shell host menghasilkan kegagalan `React.act is not a function`; pengulangan pada lingkungan tes yang benar lulus seluruhnya. Browser pada build produksi lokal lulus empat ukuran viewport.
- Tes Go terarah untuk ORD-10, TMP-06/07, DOC-14, REP-09, readiness, serta fixture outbox lulus pada PostgreSQL QA. Percobaan awal tes outbox gabungan sempat gagal karena clock fixture ditetapkan terlalu dekat dengan waktu INSERT; setelah clock ditetapkan sesudah enqueue, tes terkait lulus. Ini tidak diklaim sebagai satu run `go test ./...` yang lulus.
- Rekonsiliasi QA Cabang akhir: charge Tempo Rp350.000, terbayar Rp350.000, sisa Rp0; kredit Tempo Rp50.000 dibuat dan habis terpakai; saldo/jurnal Deposit tetap 0.
- Produksi belum mempunyai cabang Tempo aktif. Tidak ada transaksi finansial sintetis dibuat di produksi pada putaran ini.

Rincian per langkah dan log: [laporan QA cabang](/opt/temp/grahita-qa-branch-2026-10-27/hasil-uji-cabang-final.md), [laporan QA backend](/opt/temp/grahita-qa-backend-2026-10-27/hasil-backend.md), serta artefak responsif di `/opt/temp/grahita-qa-admin-2026-10-27/`.
