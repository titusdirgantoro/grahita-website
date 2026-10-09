-- +goose Up
-- 0007_cnc_reminders_sent_at.sql — klarifikasi semantik kolom `sent_at` pada
-- tabel `cnc_reminders` (tindak lanjut non-pemblokir QA F2-23, kartu t_9b6a1dbf).
--
-- Nama kolom mengikuti Database.md §4.7 sehingga TIDAK diubah. Yang diperjelas
-- hanyalah artinya: `sent_at` mengambil DEFAULT now() saat baris pengingat
-- LAHIR, yaitu waktu tahap itu DIANTRE (baris cnc_reminders + email_outbox +
-- audit ditulis dalam satu transaksi `AntreReminderCNC`), BUKAN waktu email
-- benar-benar terkirim.
--
-- Kebenaran "email sudah terkirim" ada di `email_outbox.sent_at`: kolom itu baru
-- terisi setelah SMTP menerima pesan dan tetap NULL selama pengiriman masih
-- queued/ditunda/gagal. Karena itu `cnc_reminders.sent_at` tidak boleh dibaca
-- sebagai laporan pengiriman — ia hanya jejak waktu antre sekaligus penanda
-- baris (order_id, tahap) yang melindungi pemindaian ulang dari penggandaan.
--
-- Migrasi ini HANYA menambah komentar katalog: tidak mengubah tipe, nullability,
-- maupun data, sehingga aman dijalankan pada basis data yang sudah berisi
-- (kompatibel mundur; lihat scripts/check-migration-kompatibel.sh).
COMMENT ON COLUMN cnc_reminders.sent_at IS
    'Waktu baris pengingat DIANTRE (DEFAULT now() saat insert), bukan waktu email terkirim; kebenaran pengiriman ada di email_outbox.sent_at.';

-- +goose Down
COMMENT ON COLUMN cnc_reminders.sent_at IS NULL;
