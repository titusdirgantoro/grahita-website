-- Revisi invoice berversi (F2-25; SystemDesign.md §4.5, PRD §4.3 AP-REV-01..05,
-- Database.md §4.6).
--
-- SATU permintaan revisi = SATU transaksi:
--
--   LockOrderForRevise      -> baris pesanan dikunci; keadaan versi lama dibaca
--   ListOrderLinesForRevise -> baris versi lama + jenis produk master
--   DeleteOrderLines        -> seluruh baris diganti (editor mengirim daftar final)
--   InsertOrderLineProductWithSerial / InsertOrderLinePackaging -> baris baru
--   SetOrderVersi            -> versi + 1 (nomor invoice TIDAK berubah)
--   SumOrderLineSubtotals    -> total dihitung server dari barisnya (BR-10)
--   InsertInvoiceRevision    -> snapshot versi lama (jsonb) + delta nilai
--   InsertOrderEvent         -> timeline (aktor, alasan)
--
-- Karena baris pesanan dikunci lebih dahulu, dua revisi yang bersamaan
-- berbaris rapi: yang kedua membaca versi yang sudah dinaikkan yang pertama.
-- Serial number baris lama dipertahankan apa adanya; baris produk yang benar-
-- benar BARU mendapat serial dari tabel counter dengan periode TANGGAL PESANAN
-- ASLI (SystemDesign.md §4.5), jadi nomor tidak bolong dan tetap unik.
--
-- Kolom enum dikembalikan sebagai teks (`status::text`) agar lapisan repository
-- tidak perlu mengimpor tipe enum hasil generate.

-- name: LockOrderForRevise :one
-- Mengunci baris pesanan selama transaksi revisi sekaligus membaca keadaan
-- versi lama (nomor, versi, status, total, tanggal bisnis, penanda sertifikat,
-- catatan) untuk snapshot & audit Before. Status diperiksa di service/
-- repository: hanya pesanan 'done' yang boleh direvisi.
SELECT o.id,
       o.branch_id,
       COALESCE(o.nomor, '')::text        AS nomor,
       o.versi,
       o.status::text                     AS status,
       o.total,
       o.tanggal,
       o.is_sertifikat,
       o.is_cnc,
       COALESCE(o.catatan, '')::text      AS catatan
  FROM orders o
 WHERE o.id = sqlc.arg(id)::uuid
 FOR UPDATE;

-- name: ListOrderLinesForRevise :many
-- Seluruh baris pesanan versi berjalan beserta jenis produk master. `jenis`
-- baris + `produk_jenis` dipakai memastikan aturan Sertifikat tetap terjaga
-- (tidak dicampur produk reguler, baris packaging tetap tunggal), sedangkan
-- `serial_number` baris lama dipertahankan apa adanya.
SELECT l.line_no,
       l.jenis::text                    AS jenis,
       COALESCE(l.product_id::text, '')::text AS product_id,
       l.nama_snapshot,
       l.satuan_snapshot,
       l.qty,
       l.harga_satuan,
       COALESCE(l.serial_number, '')::text AS serial_number,
       COALESCE(p.jenis::text, '')::text   AS produk_jenis,
       COALESCE(p.nama, '')                AS produk_nama,
       COALESCE(p.satuan, '')              AS produk_satuan,
       (p.id IS NOT NULL)::bool            AS produk_ada
  FROM order_lines l
  LEFT JOIN products p ON p.id = l.product_id
 WHERE l.order_id = sqlc.arg(order_id)::uuid
 ORDER BY l.line_no;

-- name: GetProductForRevise :one
-- Keadaan produk master untuk baris BARU: revision memakai nama & satuan
-- master saat itu, tetapi harga tetap angka yang dikirim admin Pusat
-- (koreksi harga adalah inti revisi) kecuali produk Sertifikat yang selalu 0.
SELECT p.id,
       p.nama,
       p.satuan,
       p.jenis::text AS jenis,
       p.is_active
  FROM products p
 WHERE p.id = sqlc.arg(id)::uuid;

-- name: DeleteOrderLines :exec
-- Seluruh baris diganti: revisi mengirim DAFTAR BARIS FINAL, bukan diff —
-- sama seperti editor draft (F1-11). Baris lama yang tidak lagi ada di daftar
-- final memang dihapus, tetapi nilainya tetap terekam di snapshot
-- invoice_revisions.snapshot.
DELETE FROM order_lines WHERE order_id = $1;

-- name: InsertOrderLineProductWithSerial :exec
-- Baris produk saat revisi. `subtotal` TIDAK diisi (kolomnya GENERATED ALWAYS).
-- Baris lama memakai serial lama apa adanya; baris baru memakai serial dari
-- counter dengan periode tanggal pesanan asli.
INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot,
                         satuan_snapshot, qty, harga_satuan, serial_number)
VALUES ($1, $2, 'product', $3, $4, $5, $6, $7, $8);

-- name: SetOrderVersi :execrows
-- Menaikkan versi pesanan SATU langkah (0 -> 1, 1 -> 2, ...); nomor invoice
-- TIDAK disentuh (AP-REV-02). Klausa WHERE memeriksa ulang status 'done'
-- sehingga transisi tidak bisa menembus pesanan yang belum selesai.
UPDATE orders
   SET versi = versi + 1,
       updated_at = now()
 WHERE id = sqlc.arg(id)::uuid
   AND status = 'done';

-- name: InsertInvoiceRevision :one
-- Snapshot versi LAMA (jsonb: nomor, versi, total, baris + serial) beserta
-- alasan, delta nilai, dan pelakunya. Unique (order_id, versi_baru) menjaga
-- agar satu versi tidak pernah tercatat dua kali; berkas versi lama sendiri
-- tetap ada di object storage (kunci per (order, jenis, versi), F1-17).
INSERT INTO invoice_revisions (order_id, versi_lama, versi_baru, alasan, snapshot,
                               delta_total, admin_id)
VALUES (sqlc.arg(order_id)::uuid, sqlc.arg(versi_lama)::int, sqlc.arg(versi_baru)::int,
        sqlc.arg(alasan), sqlc.arg(snapshot)::jsonb, sqlc.arg(delta_total)::bigint,
        sqlc.arg(admin_id)::uuid)
RETURNING id, created_at;
