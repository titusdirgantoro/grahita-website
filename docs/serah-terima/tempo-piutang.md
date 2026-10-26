# Serah terima pembayaran Tempo dan piutang cabang

Tanggal: 26 Oktober 2026. Implementasi ada pada branch `feat/tempo-receivables`.

## Ruang lingkup

- Master cabang memilih Transfer di muka, Deposit Cash & Carry, atau Tempo; Tempo menyimpan tanggal bayar 1–28 dan toggle tahan pesanan.
- Pesanan Tempo dikirim tanpa bukti transfer. Manager atau Superadmin menyetujui, invoice dan surat jalan diterbitkan, dan pesanan berstatus `approved_unpaid` sampai lunas.
- Pelunasan cabang di menu Tagihan diverifikasi Manager/Superadmin; alokasi FIFO dan kredit kelebihan bayar terpisah dari deposit. Kredit baru membutuhkan Superadmin.
- Bukti memakai `payment_proofs.purpose=tempo_repayment`. Alokasi lintas invoice dicatat pada `tempo_allocations`; `verification_proof_splits` tetap untuk verifikasi satu pesanan dan tidak dapat mewakili pembayaran lintas tagihan tanpa merusak relasi lamanya.
- Surat Tagihan PDF versi per bulan terbit dari job harian; pengingat H-3/H/H+1/H+7. Admin memiliki ringkasan piutang, umur piutang dan ekspor XLSX.
- Flag Special dihapus dari aplikasi dan kontrak API. Kolom `branches.is_special` tetap ada secara dorman untuk kompatibilitas migrasi.

## Aturan operasional

Periode berasal dari tanggal kirim pesanan WIB. Tanggal bayar cabang disalin ke pesanan saat submit dan tidak mengubah pesanan lama. Tunggakan menahan submit hanya jika toggle aktif; draft tetap tersedia. Bukti pending yang menutup seluruh tunggakan membuka masa tenggang dua hari kerja tanpa perpanjangan lewat unggah ulang.

Saat rilis, jalankan migrasi 0015 dan 0016 bersama backend, worker, serta kedua portal sebelum mengaktifkan satu cabang sebagai Tempo. Uji satu cabang pilot mencakup submit, approval, dokumen/email, pembayaran sebagian, pembayaran penuh, lebih bayar, revisi, Surat Tagihan, pengingat, dan tahan pesanan. Jangan mengubah mode cabang selama pesanan sedang dikirim tanpa memeriksa respons `PAYMENT_MODE_CHANGED`.

## Pemulihan

Cadangan PostgreSQL memuat seluruh tabel `tempo_*`; objek bukti pelunasan dan Surat Tagihan ikut cadangan objek terenkripsi. PDF Surat Tagihan berversi, tidak ditimpa oleh koreksi, dan versi lama tetap dapat diunduh dari riwayat Tagihan.
