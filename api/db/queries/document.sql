-- Dokumen pesanan: Invoice PDF dan Surat Jalan XLSX/PDF (kartu F1-17,
-- Database.md §4.6, SystemDesign.md §8).
--
-- Tabel `documents` dimiliki domain dokumen; query di berkas ini adalah
-- SATU-SATUNYA jalur tulis ke tabel itu. Kolom `storage_key` memang dibaca di
-- sini (dipakai membuat presigned URL dan memastikan berkas lama tidak
-- ditimpa) tetapi tidak pernah keluar dari lapisan service ke klien.

-- name: GetOrderForDocument :one
-- Snapshot pesanan untuk generator dokumen: header, identitas cabang, dan
-- versi pesanan (versi dokumen mengikuti versi pesanan saat render).
SELECT o.id,
       COALESCE(o.nomor, '')::text       AS nomor,
       o.status::text                    AS status,
       o.versi,
       o.branch_id::text                 AS branch_id,
       o.is_cnc,
       o.is_tempo,
       o.tempo_due_date,
       o.is_sertifikat,
       o.total,
       COALESCE(o.catatan, '')::text     AS catatan,
       o.tanggal,
       b.nama                            AS cabang_nama,
       COALESCE(b.nama_kepala, '')       AS cabang_kepala,
       COALESCE(b.email::text, ''::text)::text AS cabang_email,
       COALESCE(b.telepon, '')::text     AS cabang_telepon
  FROM orders o
  JOIN branches b ON b.id = o.branch_id
 WHERE o.id = sqlc.arg(id)::uuid;

-- name: ListDocumentsByOrder :many
-- Seluruh dokumen satu pesanan (semua versi), terlama dulu. Dipakai generator
-- memeriksa dokumen yang sudah ada (idempotensi) dan menyajikan daftar.
SELECT d.id,
       d.order_id::text                  AS order_id,
       d.jenis::text                     AS jenis,
       d.versi,
       COALESCE(d.ukuran_byte, 0)::bigint AS ukuran_byte,
       d.storage_key,
       d.created_at
  FROM documents d
 WHERE d.order_id = sqlc.arg(order_id)::uuid
 ORDER BY d.versi, d.jenis;

-- name: GetDocument :one
-- Satu dokumen beserta pemilik pesanannya; dipakai menegakkan isolasi cabang
-- dan menamai berkas unduhan.
SELECT d.id,
       d.order_id::text                  AS order_id,
       d.jenis::text                     AS jenis,
       d.versi,
       COALESCE(d.ukuran_byte, 0)::bigint AS ukuran_byte,
       d.storage_key,
       d.created_at,
       o.branch_id::text                 AS branch_id,
       COALESCE(o.nomor, '')::text       AS order_nomor
  FROM documents d
  JOIN orders o ON o.id = d.order_id
 WHERE d.id = sqlc.arg(id)::uuid;

-- name: InsertDocument :one
-- Menyimpan satu dokumen. `ON CONFLICT DO NOTHING` membuat render ulang
-- idempoten: baris yang sudah ada TIDAK ditimpa, dan pemanggil membacanya
-- kembali lewat GetDocumentByOrderJenisVersi.
INSERT INTO documents (order_id, jenis, versi, storage_key, ukuran_byte)
VALUES (sqlc.arg(order_id)::uuid, sqlc.arg(jenis)::doc_kind, sqlc.arg(versi),
        sqlc.arg(storage_key), sqlc.arg(ukuran_byte))
ON CONFLICT (order_id, jenis, versi) DO NOTHING
RETURNING id,
          order_id::text                  AS order_id,
          jenis::text                     AS jenis,
          versi,
          COALESCE(ukuran_byte, 0)::bigint AS ukuran_byte,
          storage_key,
          created_at;

-- name: GetDocumentByOrderJenisVersi :one
SELECT id,
       order_id::text                  AS order_id,
       jenis::text                     AS jenis,
       versi,
       COALESCE(ukuran_byte, 0)::bigint AS ukuran_byte,
       storage_key,
       created_at
  FROM documents
 WHERE order_id = sqlc.arg(order_id)::uuid
   AND jenis = sqlc.arg(jenis)::doc_kind
   AND versi = sqlc.arg(versi);

-- name: ListInvoicesForBranch :many
-- Riwayat invoice satu cabang (kartu F1-19, PRD §3.8): satu baris per DOKUMEN
-- invoice (jenis invoice_pdf) milik pesanan cabang tersebut, sehingga versi
-- LAMA tetap tampil setelah revisi (versi +1 menghasilkan dokumen baru tanpa
-- menimpa yang lama, F1-17/F2-25). Pesanan ber-nomor yang dokumennya belum
-- dirender juga ikut satu baris dengan dokumen kosong (versi_dokumen -1) —
-- itulah bentuk "invoice belum Done / belum siap" yang tidak bisa diunduh.
--
-- Filter periode memakai `orders.tanggal` (tanggal bisnis WIB, BR-04) dan
-- inklusif di kedua ujungnya. branch_id selalu dari SESI.
SELECT o.id::text                     AS order_id,
       COALESCE(o.nomor, '')::text    AS nomor,
       o.tanggal,
       o.status::text                 AS status,
       o.versi,
       o.is_cnc,
       o.is_tempo,
       o.total,
       COALESCE((SELECT sum(l.qty) FROM order_lines l
                  WHERE l.order_id = o.id AND l.jenis = 'product'), 0)::bigint AS jumlah_item,
       COALESCE(d.id::text, '')::text AS dokumen_id,
       COALESCE(d.versi, -1)          AS versi_dokumen,
       COALESCE(d.ukuran_byte, 0)::bigint AS ukuran_byte,
       d.created_at                   AS dokumen_created_at
  FROM orders o
  LEFT JOIN documents d
         ON d.order_id = o.id
        AND d.jenis = 'invoice_pdf'
 WHERE o.branch_id = sqlc.arg(branch_id)::uuid
   AND o.nomor IS NOT NULL
   AND (sqlc.narg(dari)::date IS NULL OR o.tanggal >= sqlc.narg(dari)::date)
   AND (sqlc.narg(sampai)::date IS NULL OR o.tanggal <= sqlc.narg(sampai)::date)
 ORDER BY o.tanggal DESC, o.nomor DESC, d.versi DESC;
