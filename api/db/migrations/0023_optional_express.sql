-- +goose Up
-- Pilihan Expres disimpan per baris draft agar perubahan tarif sebelum submit
-- tetap mengikuti pilihan cabang. Baris historis mempertahankan biayanya.
ALTER TABLE order_lines ADD COLUMN use_express boolean NOT NULL DEFAULT false;
UPDATE order_lines SET use_express = true WHERE express_per_pcs > 0;

UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{1}', to_jsonb('Isi Nama Instansi, jumlah pcs, dan produk di tiap baris. Centang Expres hanya pada baris yang membutuhkannya; total diperbarui otomatis.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>1 = 'Pilih produk dan isi qty; harga satuan terisi otomatis dari harga khusus cabang Anda.';

-- +goose Down
UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{1}', to_jsonb('Pilih produk dan isi qty; harga satuan terisi otomatis dari harga khusus cabang Anda.'::text)),
       updated_at = now()
 WHERE ikon = 'memesan' AND judul = 'Cara memesan'
   AND langkah->>1 = 'Isi Nama Instansi, jumlah pcs, dan produk di tiap baris. Centang Expres hanya pada baris yang membutuhkannya; total diperbarui otomatis.';
ALTER TABLE order_lines DROP COLUMN use_express;
