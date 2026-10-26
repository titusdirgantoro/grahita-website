# Serah terima pembayaran Tempo dan piutang cabang

Tanggal: 26 Oktober 2026. Implementasi ada pada branch `feat/tempo-receivables`.

## Ruang lingkup

- Master cabang dapat mengizinkan satu atau lebih dari Transfer di muka, Deposit Cash & Carry, dan Tempo. Cabang memilih satu cara per pesanan saat submit; pilihan menjadi snapshot pesanan. Deposit boleh ditambah transfer untuk kekurangan saldo. Tempo menyimpan tanggal bayar 1–28 dan toggle tahan Tempo.
- Pesanan Tempo dikirim tanpa bukti transfer. Manager atau Superadmin menyetujui, invoice dan surat jalan diterbitkan, dan pesanan berstatus `approved_unpaid` sampai lunas.
- Pelunasan cabang di menu Tagihan diverifikasi Manager/Superadmin; alokasi FIFO dan kredit kelebihan bayar terpisah dari deposit. Kredit baru membutuhkan Superadmin.
- Bukti memakai `payment_proofs.purpose=tempo_repayment`. Alokasi lintas invoice dicatat pada `tempo_allocations`; `verification_proof_splits` tetap untuk verifikasi satu pesanan dan tidak dapat mewakili pembayaran lintas tagihan tanpa merusak relasi lamanya.
- Surat Tagihan PDF versi per bulan terbit dari job harian; pengingat H-3/H/H+1/H+7. Admin memiliki ringkasan piutang, umur piutang dan ekspor XLSX.
- Flag Special dihapus dari aplikasi dan kontrak API. Kolom `branches.is_special` tetap ada secara dorman untuk kompatibilitas migrasi.

## Aturan operasional

Periode berasal dari tanggal kirim pesanan WIB. Tanggal bayar cabang disalin ke pesanan Tempo saat submit dan tidak mengubah pesanan lama. Tunggakan menahan submit dengan pilihan Tempo hanya jika toggle aktif; Transfer di muka dan Deposit tetap tersedia bila diizinkan. Draft tetap tersedia. Bukti pending yang menutup seluruh tunggakan membuka masa tenggang dua hari kerja tanpa perpanjangan lewat unggah ulang.

Saat rilis pilihan pembayaran campuran, jalankan migrasi 0017 sebelum backend dan kedua portal diperbarui. Cabang lama mempertahankan satu cara pembayaran. Uji satu cabang pilot mencakup pemilihan tiga cara per pesanan, submit saat Tempo ditahan, dan seluruh siklus tagihan. Perubahan opsi cabang saat submit ditolak dengan `PAYMENT_MODE_CHANGED` agar cabang memuat ulang pilihan.

## Pemulihan

Cadangan PostgreSQL memuat seluruh tabel `tempo_*`; objek bukti pelunasan dan Surat Tagihan ikut cadangan objek terenkripsi. PDF Surat Tagihan berversi, tidak ditimpa oleh koreksi, dan versi lama tetap dapat diunduh dari riwayat Tagihan.
