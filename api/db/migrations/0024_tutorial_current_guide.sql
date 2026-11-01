-- +goose Up
-- Koreksi batas berkas lama. Hanya teks bawaan yang tepat sama diperbarui;
-- suntingan Superadmin tetap utuh.
UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{1}', to_jsonb('Batas ukuran dan jumlah berkas mengikuti validasi yang ditampilkan sistem; beberapa bukti dapat dilampirkan bila pembayaran terpisah.'::text)),
       updated_at = now()
 WHERE ikon = 'bukti' AND judul = 'Cara unggah bukti transfer'
   AND langkah->>1 = 'Satu berkas maksimal 5 MB, dan bisa lebih dari satu berkas untuk pembayaran terpisah.';

-- +goose Down
UPDATE tutorial_topics
   SET langkah = jsonb_set(langkah, '{1}', to_jsonb('Satu berkas maksimal 5 MB, dan bisa lebih dari satu berkas untuk pembayaran terpisah.'::text)),
       updated_at = now()
 WHERE ikon = 'bukti' AND judul = 'Cara unggah bukti transfer'
   AND langkah->>1 = 'Batas ukuran dan jumlah berkas mengikuti validasi yang ditampilkan sistem; beberapa bukti dapat dilampirkan bila pembayaran terpisah.';
