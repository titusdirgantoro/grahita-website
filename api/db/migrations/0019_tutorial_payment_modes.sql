-- +goose Up
-- Koreksi petunjuk lama yang menganggap semua pesanan memerlukan bukti transfer.
-- Setiap perubahan hanya menyentuh langkah yang masih berisi teks bawaan lama;
-- langkah lain yang pernah disunting Superadmin tetap dipertahankan.

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{3}', to_jsonb('Pilih cara pembayaran yang tersedia untuk cabang Anda: Transfer di muka, Deposit (Cash & Carry), atau Tempo.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>3 = 'Pilih “Kirim pesanan”, lalu transfer sesuai rekening yang tampil.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{4}', to_jsonb('Untuk Transfer, unggah bukti pembayaran. Untuk Deposit, gunakan saldo dan unggah bukti bila saldo kurang. Tempo tidak perlu bukti di muka.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>4 = 'Unggah bukti transfer (JPG/PNG/PDF) dan tekan “Kirim pesanan”.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{5}', to_jsonb('Tekan “Kirim pesanan”. Invoice tersedia setelah pesanan disetujui dan dokumen terbit.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>5 = 'Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{2}', to_jsonb('Bukti saat mengirim pesanan wajib untuk Transfer di muka. Deposit memerlukan bukti bila saldo kurang; Tempo tidak memerlukan bukti di muka.'::text)),
       updated_at = now()
 WHERE ikon = 'bukti' AND judul = 'Cara unggah bukti transfer'
   AND langkah->>2 = 'Non-Cash & Carry wajib menyertakan minimal satu bukti saat mengirim pesanan.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{3}', to_jsonb('Untuk Transfer atau Deposit yang masih menunggu verifikasi, bukti tambahan dapat diunggah dari detail pesanan.'::text)),
       updated_at = now()
 WHERE ikon = 'bukti' AND judul = 'Cara unggah bukti transfer'
   AND langkah->>3 = 'Bukti tambahan bisa diunggah dari detail pesanan selama statusnya masih Menunggu.';

-- +goose Down
-- Hanya kembalikan langkah yang masih persis hasil migrasi; suntingan baru
-- oleh Superadmin setelah rilis tidak akan tertimpa.

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{3}', to_jsonb('Pilih “Kirim pesanan”, lalu transfer sesuai rekening yang tampil.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>3 = 'Pilih cara pembayaran yang tersedia untuk cabang Anda: Transfer di muka, Deposit (Cash & Carry), atau Tempo.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{4}', to_jsonb('Unggah bukti transfer (JPG/PNG/PDF) dan tekan “Kirim pesanan”.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>4 = 'Untuk Transfer, unggah bukti pembayaran. Untuk Deposit, gunakan saldo dan unggah bukti bila saldo kurang. Tempo tidak perlu bukti di muka.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{5}', to_jsonb('Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>5 = 'Tekan “Kirim pesanan”. Invoice tersedia setelah pesanan disetujui dan dokumen terbit.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{2}', to_jsonb('Non-Cash & Carry wajib menyertakan minimal satu bukti saat mengirim pesanan.'::text)),
       updated_at = now()
 WHERE ikon = 'bukti' AND judul = 'Cara unggah bukti transfer'
   AND langkah->>2 = 'Bukti saat mengirim pesanan wajib untuk Transfer di muka. Deposit memerlukan bukti bila saldo kurang; Tempo tidak memerlukan bukti di muka.';

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{3}', to_jsonb('Bukti tambahan bisa diunggah dari detail pesanan selama statusnya masih Menunggu.'::text)),
       updated_at = now()
 WHERE ikon = 'bukti' AND judul = 'Cara unggah bukti transfer'
   AND langkah->>3 = 'Untuk Transfer atau Deposit yang masih menunggu verifikasi, bukti tambahan dapat diunggah dari detail pesanan.';
