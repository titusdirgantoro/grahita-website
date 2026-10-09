-- +goose Up
-- 0006_tutorial.sql — konten terstruktur halaman Tutorial portal cabang
-- (F2-27, PRD §3.8 dan §10 fase 2).
--
-- Konten tutorial disimpan TERSTRUKTUR (judul, ringkasan, daftar langkah, dan
-- kunci ilustrasi bawaan aplikasi) supaya Superadmin dapat memperbaruinya lewat
-- API dan portal cabang yang di-refresh langsung menampilkan versi terbaru
-- TANPA build/deploy portal.
--
-- Migrasi ini KOMPATIBEL MUNDUR: hanya MENAMBAH tabel baru (tidak ada tabel
-- yang diubah/dihapus) dan mengisinya dengan empat topik awal yang sekarang
-- tampil di portal cabang. Pengisian awal hanya berjalan bila tabel masih
-- kosong, sehingga menjalankan/mengulang migrasi pada basis data yang sudah
-- berisi konten tidak menimpa atau menggandakan apa pun.
--
-- `ikon` adalah KUNCI ilustrasi bawaan (memesan/bukti/sertifikat/cnc/umum),
-- bukan berkas atau markup: ilustrasi tetap di-render aplikasi, jadi tidak ada
-- HTML/SVG arbitrer yang bisa masuk lewat data (keputusan kartu F2-27).

CREATE TABLE tutorial_topics (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    ikon       text NOT NULL,
    judul      text NOT NULL,
    ringkas    text NOT NULL,
    langkah    jsonb NOT NULL,
    urutan     int NOT NULL,
    updated_by uuid REFERENCES admin_users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tutorial_topics_langkah_array CHECK (jsonb_typeof(langkah) = 'array'),
    CONSTRAINT tutorial_topics_langkah_isi   CHECK (jsonb_array_length(langkah) BETWEEN 1 AND 24),
    CONSTRAINT tutorial_topics_judul_isi     CHECK (char_length(judul) BETWEEN 1 AND 120),
    CONSTRAINT tutorial_topics_ringkas_isi   CHECK (char_length(ringkas) BETWEEN 1 AND 300),
    CONSTRAINT tutorial_topics_urutan_isi    CHECK (urutan >= 0)
);
CREATE INDEX tutorial_topics_urutan ON tutorial_topics (urutan, created_at);

-- Empat topik awal (sama dengan halaman tutorial statis F1-19 dan prototype
-- customer_v2.html). Dijaga `WHERE NOT EXISTS` supaya idempoten.
INSERT INTO tutorial_topics (ikon, judul, ringkas, langkah, urutan)
SELECT s.ikon, s.judul, s.ringkas, s.langkah, s.urutan
  FROM (VALUES
    ('memesan', 'Cara memesan',
     'Dari memilih produk sampai pesanan terkirim ke Pusat.',
     '["Buka menu Pesanan lalu pilih “Buat pesanan”.",
       "Pilih produk dan isi qty; harga satuan terisi otomatis dari harga khusus cabang Anda.",
       "Pilih “Simpan draft” bila belum siap dikirim — draft bisa diubah atau dihapus.",
       "Pilih “Kirim pesanan”, lalu transfer sesuai rekening yang tampil.",
       "Unggah bukti transfer (JPG/PNG/PDF) dan tekan “Kirim pesanan”.",
       "Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”."]'::jsonb, 10),
    ('bukti', 'Cara unggah bukti transfer',
     'Bukti yang jelas mempercepat verifikasi Pusat.',
     '["Gunakan foto atau tangkapan layar yang jelas: JPG, PNG, atau PDF.",
       "Satu berkas maksimal 5 MB, dan bisa lebih dari satu berkas untuk pembayaran terpisah.",
       "Non-Cash & Carry wajib menyertakan minimal satu bukti saat mengirim pesanan.",
       "Bukti tambahan bisa diunggah dari detail pesanan selama statusnya masih Menunggu.",
       "Nominal seluruh bukti dijumlahkan saat verifikasi Pusat."]'::jsonb, 20),
    ('sertifikat', 'Aturan produk Sertifikat',
     'Sertifikat selalu dipesan terpisah dari produk reguler.',
     '["Buat pesanan baru khusus untuk produk Sertifikat.",
       "Harga satuan Sertifikat selalu Rp0 berapa pun jumlah lembarnya.",
       "Biaya packaging ditambahkan otomatis satu baris per pesanan dan tidak bisa diubah.",
       "Produk reguler dipesan di pesanan terpisah; satu pesanan Sertifikat = satu invoice."]'::jsonb, 30),
    ('cnc', 'Pesanan Cash & Carry',
     'Bayar sebelum jatuh tempo yang dihitung dari tanggal pesanan.',
     '["Opsi Cash & Carry hanya muncul untuk cabang yang diizinkan Pusat.",
       "Saat mengirim pesanan, aktifkan centang Cash & Carry; bukti transfer boleh menyusul.",
       "Jatuh tempo = tanggal pesanan + jumlah hari C&C yang berlaku (bawaan 7 hari).",
       "Bayar sebelum tanggal jatuh tempo; pesanan baru selesai setelah pembayaran diverifikasi Pusat.",
       "Pengingat dikirim ke email cabang mendekati dan pada hari jatuh tempo."]'::jsonb, 40)
  ) AS s(ikon, judul, ringkas, langkah, urutan)
 WHERE NOT EXISTS (SELECT 1 FROM tutorial_topics);

-- +goose Down
DROP TABLE tutorial_topics;
