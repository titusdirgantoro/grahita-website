-- Revisi invoice berversi (F2-25; SystemDesign.md §4.5, PRD §4.3 AP-REV-01..05,
-- Database.md §4.6).
--
-- SATU permintaan revisi = SATU transaksi:
--
--   LockOrderForRevise      -> baris pesanan dikunci; keadaan versi lama dibaca
--   InvoiceVersiAda         -> invoice VERSI BERJALAN wajib sudah dirender
--                              (fail closed; tanpa ini versi bisa dilewati)
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

-- name: InvoiceVersiAda :one
-- Memeriksa invoice VERSI BERJALAN pesanan sudah dirender (fail closed,
-- temuan QA putaran 1). Pekerjaan lanjutan revisi merender invoice versi
-- pesanan SAAT ITU, jadi revisi hanya boleh menaikkan versi bila versi
-- berjalan sudah punya baris invoice di `documents` — bila tidak, versi itu
-- bisa dilewati selamanya (tidak ada baris maupun berkasnya) padahal API
-- sudah menjawab 200 (melanggar AP-REV-03/AP-REV-04).
-- Invarian F1-17 berlaku: bila barisnya ada, berkasnya ada.
SELECT EXISTS (
    SELECT 1
      FROM documents d
     WHERE d.order_id = sqlc.arg(order_id)::uuid
       AND d.jenis = 'invoice_pdf'
       AND d.versi = sqlc.arg(versi)::int
)::bool AS ada;

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
       o.is_tempo,
       o.tempo_paid_amount,
       o.tanggal_bayar_snapshot,
       o.tempo_due_date,
       COALESCE(o.catatan, '')::text      AS catatan,
       o.done_verification_log_id,
       o.done_order_version,
       o.done_deposit_amount,
       o.done_transfer_amount,
       o.done_at
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
       l.qty,
       l.harga_satuan,
       COALESCE(l.serial_number, '')::text AS serial_number,
       COALESCE(p.jenis::text, '')::text   AS produk_jenis,
       COALESCE(p.nama, '')                AS produk_nama,
       (p.id IS NOT NULL)::bool            AS produk_ada
  FROM order_lines l
  LEFT JOIN products p ON p.id = l.product_id
 WHERE l.order_id = sqlc.arg(order_id)::uuid
 ORDER BY l.line_no;

-- name: GetProductForRevise :one
-- Keadaan produk master untuk baris BARU: revision memakai nama
-- master saat itu, tetapi harga tetap angka yang dikirim admin Pusat
-- (koreksi harga adalah inti revisi) kecuali produk Sertifikat yang selalu 0.
SELECT p.id,
       p.nama,
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
                         qty, harga_satuan, serial_number)
VALUES ($1, $2, 'product', $3, $4, $5, $6, $7);

-- name: SetOrderVersi :execrows
-- Menaikkan versi pesanan SATU langkah (0 -> 1, 1 -> 2, ...); nomor invoice
-- TIDAK disentuh (AP-REV-02). Klausa WHERE memeriksa ulang status 'done'
-- sehingga transisi tidak bisa menembus pesanan yang belum selesai.
UPDATE orders
   SET versi = versi + 1,
       status = 'submitted',
       revision_payment_state = 'revision_underpayment',
       revision_payment_started_at = now(),
       verif_status = 'perlu_review',
       alasan_review = 'kurang_bayar',
       done_at = NULL,
       done_verification_log_id = NULL,
       done_order_version = NULL,
       done_deposit_amount = NULL,
       done_transfer_amount = NULL,
       updated_at = now()
 WHERE id = sqlc.arg(id)::uuid
   AND status = 'done';

-- name: FinalizeOrderRevisionSettlement :execrows
UPDATE orders
   SET status = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                     THEN 'done'::order_status ELSE 'submitted'::order_status END,
       revision_payment_state = CASE sqlc.arg(payment_transition)::revision_payment_transition
         WHEN 'underpayment' THEN 'revision_underpayment'::revision_payment_state
         WHEN 'overpayment' THEN 'revision_overpayment_pending'::revision_payment_state
         ELSE NULL END,
       revision_payment_started_at = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                                          THEN NULL ELSE now() END,
       verif_status = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                           THEN 'cocok'::verif_status ELSE 'perlu_review'::verif_status END,
       alasan_review = CASE sqlc.arg(payment_transition)::revision_payment_transition
         WHEN 'underpayment' THEN 'kurang_bayar'::review_reason
         WHEN 'overpayment' THEN 'lebih_bayar'::review_reason
         ELSE NULL END,
       done_at = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                      THEN sqlc.arg(previous_done_at)::timestamptz ELSE NULL END,
       done_verification_log_id = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                                       THEN sqlc.arg(previous_done_verification_log_id)::uuid ELSE NULL END,
       done_order_version = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                                 THEN sqlc.arg(previous_done_order_version)::int ELSE NULL END,
       done_deposit_amount = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                                  THEN sqlc.arg(previous_done_deposit_amount)::bigint ELSE NULL END,
       done_transfer_amount = CASE WHEN sqlc.arg(payment_transition)::revision_payment_transition = 'total_unchanged'
                                   THEN sqlc.arg(previous_done_transfer_amount)::bigint ELSE NULL END,
       updated_at = now()
 WHERE id = sqlc.arg(order_id)::uuid
   AND status = 'submitted'
   AND revision_payment_state = 'revision_underpayment';

-- name: InsertInvoiceRevision :one
-- Snapshot versi LAMA (jsonb: nomor, versi, total, baris + serial) beserta
-- alasan, delta nilai, dan pelakunya. Unique (order_id, versi_baru) menjaga
-- agar satu versi tidak pernah tercatat dua kali; berkas versi lama sendiri
-- tetap ada di object storage (kunci per (order, jenis, versi), F1-17).
INSERT INTO invoice_revisions (order_id, versi_lama, versi_baru, alasan, snapshot,
                               delta_total, admin_id, payment_transition,
                               previous_done_verification_log_id, previous_done_order_version,
                               previous_done_total, previous_done_deposit_amount,
                               previous_done_transfer_amount, previous_done_at)
VALUES (sqlc.arg(order_id)::uuid, sqlc.arg(versi_lama)::int, sqlc.arg(versi_baru)::int,
        sqlc.arg(alasan), sqlc.arg(snapshot)::jsonb, sqlc.arg(delta_total)::bigint,
        sqlc.arg(admin_id)::uuid, sqlc.arg(payment_transition)::revision_payment_transition,
        sqlc.arg(previous_done_verification_log_id)::uuid,
        sqlc.arg(previous_done_order_version)::int,
        sqlc.arg(previous_done_total)::bigint,
        sqlc.arg(previous_done_deposit_amount)::bigint,
        sqlc.arg(previous_done_transfer_amount)::bigint,
        sqlc.arg(previous_done_at)::timestamptz)
RETURNING id, created_at;
