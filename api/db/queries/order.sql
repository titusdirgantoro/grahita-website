-- Pesanan cabang: draft, editor order line, daftar dan detail (F1-11).
--
-- Setiap query yang menyentuh `orders` SELALU menyertakan branch_id sebagai
-- syarat, dan nilainya datang dari SESI (bukan dari klien) sehingga pesanan
-- cabang lain tidak pernah bisa dibaca, diubah, atau dihapus (§ isolasi data).
-- Status draft diperiksa lagi di klausa WHERE update/delete supaya submit yang
-- berjalan bersamaan (F1-14) tidak bisa ditembus.
--
-- Kolom enum dikembalikan sebagai teks (`status::text`) agar lapisan repository
-- tidak perlu mengimpor tipe enum hasil generate.

-- name: ListOrdersForBranch :many
-- Daftar pesanan satu cabang: nomor, tanggal, jumlah item (Σ qty baris produk),
-- total, status, dan penanda C&C (PRD CP-ORD-01). Filter status opsional;
-- draft punya nomor/tanggal NULL sehingga diurutkan memakai created_at.
SELECT o.id,
       o.nomor,
       o.status::text       AS status,
       o.verif_status::text AS verif_status,
       o.is_cnc,
       o.is_sertifikat,
       o.versi,
       o.tanggal,
       COALESCE(o.catatan, '') AS catatan,
       o.total,
       COALESCE((SELECT sum(l.qty) FROM order_lines l
                  WHERE l.order_id = o.id AND l.jenis = 'product'), 0)::bigint AS jumlah_item,
       o.submitted_at,
       o.created_at,
       o.updated_at
  FROM orders o
 WHERE o.branch_id = sqlc.arg(branch_id)::uuid
   AND (sqlc.narg(status)::order_status IS NULL OR o.status = sqlc.narg(status)::order_status)
 ORDER BY COALESCE(o.tanggal::timestamptz, o.created_at) DESC, o.created_at DESC;

-- name: GetOrderForBranch :one
-- Detail satu pesanan milik cabang tersebut. Pesanan cabang lain tidak
-- ditemukan (0 baris) sehingga tidak membocorkan keberadaannya.
SELECT o.id,
       o.branch_id,
       o.nomor,
       o.status::text       AS status,
       o.verif_status::text AS verif_status,
       o.is_cnc,
       o.is_sertifikat,
       o.versi,
       o.tanggal,
       o.jatuh_tempo,
       COALESCE(o.catatan, '') AS catatan,
       o.total,
       COALESCE((SELECT sum(l.qty) FROM order_lines l
                  WHERE l.order_id = o.id AND l.jenis = 'product'), 0)::bigint AS jumlah_item,
       o.submitted_at,
       o.created_at,
       o.updated_at
  FROM orders o
 WHERE o.id = sqlc.arg(id)::uuid
   AND o.branch_id = sqlc.arg(branch_id)::uuid;

-- name: CreateOrderDraft :one
-- Draft baru: tanpa nomor dan tanpa tanggal (constraint draft_tanpa_nomor).
-- total diisi 0 lalu disinkronkan trigger order_lines_recalc saat barisnya
-- ditulis, jadi angka total tidak pernah datang dari klien (SystemDesign §3).
INSERT INTO orders (branch_id, status, catatan, total, created_by)
VALUES (sqlc.arg(branch_id)::uuid, 'draft', sqlc.narg(catatan), 0, sqlc.narg(created_by)::uuid)
RETURNING id;

-- name: UpdateOrderDraft :one
-- Mengubah catatan draft milik cabang pemanggil. 0 baris berarti pesanan tidak
-- ada, bukan milik cabang itu, atau sudah bukan draft lagi.
UPDATE orders
   SET catatan = sqlc.narg(catatan)
 WHERE id = sqlc.arg(id)::uuid
   AND branch_id = sqlc.arg(branch_id)::uuid
   AND status = 'draft'
RETURNING id;

-- name: DeleteOrderDraft :execrows
-- Hapus draft milik cabang pemanggil; baris order_lines ikut terhapus lewat
-- ON DELETE CASCADE. Pesanan Submitted/Done tidak pernah terhapus di sini.
DELETE FROM orders
 WHERE id = sqlc.arg(id)::uuid
   AND branch_id = sqlc.arg(branch_id)::uuid
   AND status = 'draft';

-- name: ClearOrderLines :exec
-- Baris draft diganti seluruhnya saat draft diubah (editor mengirim daftar
-- baris final, bukan diff).
DELETE FROM order_lines WHERE order_id = $1;

-- name: InsertOrderLineProduct :exec
-- subtotal TIDAK diisi: kolomnya GENERATED ALWAYS (qty * harga_satuan).
-- harga_satuan sudah diambil server dari branch_prices cabang sesi.
INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, satuan_snapshot, qty, harga_satuan)
VALUES ($1, $2, 'product', $3, $4, $5, $6, $7);

-- name: ListOrderLines :many
SELECT id, line_no, jenis::text AS jenis, product_id, nama_snapshot, satuan_snapshot,
       qty, harga_satuan, subtotal, serial_number
  FROM order_lines
 WHERE order_id = $1
 ORDER BY line_no;
