# Tindak lanjut QA Grahita — 27 Oktober 2026

Dokumen ini memperbarui [laporan baseline](hasil-uji-e2e-2026-10-27.md) untuk skenario yang diuji ulang. Status baseline lainnya tetap berlaku. Semua transaksi bisnis di bawah menggunakan PostgreSQL, storage, dan renderer QA terisolasi. Pemeriksaan pada dua portal produksi dilakukan dengan akun `UJI-` yang disediakan operator dan hanya membaca data.

## Perbaikan produk

- **CAB-09:** `/me/branch` sekarang mengirim `has_tempo_history` dari pesanan Tempo yang pernah dikirim. Menu **Tagihan** tetap muncul setelah izin membuat pesanan Tempo dicabut; akses ke riwayat dan pelunasan tidak bergantung pada pilihan pembayaran saat ini. Pesanan Tempo baru tetap ditolak dengan `PAYMENT_MODE_CHANGED`.
- **DOC-08:** Surat Jalan dengan banyak baris dibagi ke beberapa tabel cetak. Halaman akhir selalu membawa rincian terakhir bersama total, catatan, dan tanda tangan. Pada hasil lama, 29 baris memenuhi halaman pertama dan tanda tangan terdorong sendirian ke halaman kedua.
- Tooltip Invoice dan pesan API untuk dokumen/revisi diperbaiki agar sesuai dengan penerbitan invoice Tempo saat `approved_unpaid`.
- Pemeriksaan browser produksi menemukan teks Riwayat Invoice dan Beranda yang masih menyebut invoice baru terbit setelah `Selesai`. Teks itu diperbaiki agar menyebut persetujuan dan terbitnya dokumen. Beranda kini menampilkan status `Disetujui — belum lunas` untuk pesanan Tempo yang telah disetujui, serta menggunakan pesan umum saat pesanan masih menunggu verifikasi.

## Regresi dan bukti

| Skenario | Status lokal | Bukti yang diperoleh |
|---|---|---|
| CAB-09 | **PASS** | Tes komponen UI menemukan Tagihan di sidebar dan navigasi bawah bagi cabang dengan riwayat Tempo; integrasi API/DB membuktikan piutang lama tetap bisa dibaca, bukti dilunasi, order menjadi `done`, dan submit Tempo baru ditolak. |
| DEP-06 | **PASS** | Approval Deposit berulang dengan key dan payload sama mengembalikan replay tanpa menggandakan jurnal/saldo; key sama dengan nominal berbeda ditolak. |
| TMP-14 | **PASS** | Dua keputusan verifikasi bukti pelunasan bersamaan menghasilkan satu sukses, satu konflik, dan satu alokasi Rp90.000; keputusan berikutnya tetap konflik. |
| TMP-16 | **PASS** | Bukti pending Rp40.000 pada tunggakan Rp100.000 tidak melepas hold; bukti berikutnya Rp60.000 memulai grace dan baru saat itu submit Tempo diizinkan. |
| TMP-18 | **PASS** | Hold kembali setelah bukti ditolak atau nominal terverifikasi kurang, lalu hilang setelah sisa piutang nol. |
| TMP-22 | **PASS** | Pengingat H-3, H, H+1, H+7 untuk tanggal bayar 1, 2, 3, 5 terkirim satu kali per tahap. H-3 tanggal 1–3 bersifat umum; pengingat berikutnya memakai sisa terkini setelah bayar sebagian. |
| DOC-08 | **PASS** | Gotenberg nyata menghasilkan Invoice dan Surat Jalan dengan 1, 11, 22, 23, 29, dan 100 baris; seluruh serial ditemukan pada PDF. Nama instansi panjang, Unicode/HTML, catatan panjang, serta Sertifikat + packaging tercetak. Surat Jalan 22 baris tetap 1 halaman, 23 baris menjadi 2 (18+5), 29 baris menjadi 2 (24+5), dan 100 baris menjadi 5 (24+24+24+23+5). Halaman akhir berisi total dan tanda tangan. |

Dengan tujuh perubahan menjadi PASS di atas serta REP-10 dan TMP-08 dari NOT RUN menjadi PARTIAL, hasil **lokal** dari 124 P0 menjadi: **34 PASS, 0 FAIL, 80 PARTIAL, 7 NOT RUN, 3 BLOCKED**. Dari seluruh 162 skenario: **35 PASS, 0 FAIL, 107 PARTIAL, 17 NOT RUN, 3 BLOCKED**. Jumlah ini bukan klaim bahwa 162 skenario atau produksi telah lulus.

Bukti tambahan yang belum menutup seluruh skenario: FIFO lintas bulan dan kredit Tempo (TMP-09 tetap PARTIAL karena perjalanan browser lintas periode belum lengkap); tanggal submit WIB dan dua hari kerja grace diuji pada unit (TMP-01/TMP-17 tetap belum penuh); konservasi nominal split verifikasi serta validasi nama instansi Unicode 160 karakter, spasi, dan qty melewati batas (VER-05/ORD-02 tetap PARTIAL); job penerbitan surat tanggal 1 idempoten dan email cabang/admin lulus integrasi (TMP-06/TMP-07 tetap PARTIAL pada bukti PDF/tagihan dan perjalanan menyeluruh).

REP-10 sekarang **PARTIAL**: API Tagihan cabang, API Tagihan admin, buku besar, dan setiap baris ekspor XLSX cocok untuk dua periode setelah bayar sebagian; bukti pending Rp20.000 tetap tercatat sebagai pending dan tidak mengurangi sisa Rp250.000. Perbandingan pada layar kedua portal belum dilakukan dengan dataset yang sama.

TMP-08 sekarang **PARTIAL**: status `berjalan`, `menunggu_bayar` pada H-1/H, `lewat_jatuh_tempo` pada H+1, serta `lunas` setelah pembayaran diverifikasi telah diuji terhadap PostgreSQL dan zona WIB. Perubahan status di layar browser dengan waktu terkendali belum dijalankan.

Fixture backup/restore kini menyertakan `branch_id` yang diwajibkan database dan tes restore PostgreSQL lulus; OPS-05 tetap PARTIAL karena backup objek serta operasi terjadwal belum dibuktikan. Dua tes dokumen yang semula membaca serial XLSX dari kolom lama kini lulus, termasuk jalur Chromium nyata. Perbaikan ini hanya mengubah asersi/fixture tes; posisi kolom file sebenarnya memang sudah benar setelah penambahan nama instansi.

## Batas yang masih terbuka

- Tiga P0 OCR provider nyata (VER-08/09/10) tetap **BLOCKED** karena tidak ada `ANTHROPIC_API_KEY` QA. Provider tiruan dan keamanan alur OCR diuji, tetapi itu tidak membuktikan respons provider nyata.
- Produksi saat diperiksa belum mempunyai cabang dengan riwayat pesanan Tempo. Kasus izin Tempo dicabut dibuktikan di fixture lokal; tidak dibuat piutang sintetis baru di produksi.
- Skenario P0 yang masih PARTIAL atau NOT RUN pada laporan baseline tidak dinaikkan menjadi PASS hanya karena suite unit/integrasi lulus. Beberapa memerlukan pengamatan UI, penjadwalan tanggal, pemulihan worker, atau bukti operasional tersendiri.

## Gerbang teknis sebelum rilis

- Seluruh paket Go lulus `go test -p 1 ./...`; `go vet ./...` bersih.
- Integrasi PostgreSQL QA lulus berurutan dengan `go test -p 1 -count=1 -timeout 60m ./internal/platform/database/... ./internal/worker/... ./internal/httpapi/...` (paket HTTP API 1.437,9 detik). Dua tes rekonsiliasi piutang dan batas status yang ditambahkan setelah suite dimulai lulus terarah pada PostgreSQL yang sama.
- Frontend lulus 249 tes (9 API client, 65 UI, 100 admin, 75 cabang), typecheck, dan build kedua portal.
- Pemeriksaan migrasi kompatibel mundur, pemindaian rahasia, dan `git diff --check` lulus. Renderer Gotenberg QA menghasilkan 12 PDF untuk enam variasi jumlah baris.
- Gerbang teknis ini memverifikasi perubahan rilis; status 162 skenario end to end tetap sesuai tabel di atas.

## Artefak QA

- PDF hasil renderer: `/opt/temp/grahita-qa-evidence-2026-10-27/pdf-banyak/`.
- Screenshot smoke browser sebelum rilis: `/opt/temp/grahita-qa-evidence-2026-10-27/prod-before/`.
- Log tes terarah: `/tmp/grahita-qa-tempo-target-20261027.log`, `/tmp/grahita-qa-deposit-idempotency-20261027.log`, `/tmp/grahita-qa-settlement-conservation-20261027.log`, `/tmp/grahita-qa-document-chromium-fixed-20261027.log`.
- Rekonsiliasi API/ekspor piutang: `/tmp/grahita-qa-receivable-reconciliation-20261027.log`.
- Batas status Tagihan: `/tmp/grahita-qa-tempo-status-boundaries-20261027.log`.
- Empat tes integrasi terakhir yang melampaui timeout awal: `/tmp/grahita-qa-integration-final-four-20261027.log` (semuanya PASS).
- Suite integrasi penuh: `/tmp/grahita-qa-integration-60m-20261027.log`.
