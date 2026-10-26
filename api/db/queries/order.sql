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
-- total, status, dan penanda C&C (PRD CP-ORD-01).
-- Filter status opsional; draft punya nomor/tanggal NULL sehingga diurutkan
-- memakai created_at.
SELECT o.id,
       o.nomor,
       o.status::text       AS status,
       o.verif_status::text AS verif_status,
       o.is_cnc,
       o.is_tempo,
       o.tempo_due_date,
       o.tempo_paid_amount,
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
       o.is_tempo,
       o.tempo_due_date,
       o.tempo_paid_amount,
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
INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan)
VALUES ($1, $2, 'product', $3, $4, $5, $6);

-- name: InsertOrderLinePackaging :exec
-- Baris biaya packaging pesanan sertifikat (PRD CP-SER-02): tanpa product_id,
-- qty SELALU 1, dan tanpa serial number (constraint packaging_tanpa_serial).
-- Harga packaging adalah snapshot `biaya_packaging` yang dibaca server dari
-- domain pengaturan — tidak pernah datang dari klien. Indeks unik parsial
-- order_lines_satu_packaging menjaga agar baris ini tetap tunggal per pesanan.
INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan)
VALUES ($1, $2, 'packaging', NULL, $3, 1, $4);

-- name: ListOrderLines :many
SELECT id, line_no, jenis::text AS jenis, product_id, nama_snapshot,
       qty, harga_satuan, subtotal, serial_number
  FROM order_lines
 WHERE order_id = $1
 ORDER BY line_no;

-- ============================================================================
-- Submit pesanan (F1-14): draft -> submitted dan penomoran transaksional
-- (SystemDesign.md §4.2, Database.md §4.4/§6).
--
-- Seluruh query di bawah ini dipanggil DI DALAM SATU transaksi repository
-- Submit, dengan urutan: kunci baris pesanan → baca baris + master + harga
-- cabang → validasi ulang → ambil nomor invoice/serial → tulis snapshot,
-- serial, status, tanggal, total, debit deposit C&C, dan timeline. Karena penomoran
-- memakai tabel counter (bukan SEQUENCE) dan berada di transaksi yang sama,
-- submit yang gagal/rollback tidak memakai nomor (BR-09) dan submit
-- bersamaan tidak bisa mendapat nomor yang sama.
-- ============================================================================

-- name: LockOrderForSubmit :one
-- Mengunci baris pesanan selama transaksi submit: cegah double submit dan
-- berpindah status di tengah jalan (SystemDesign §5.3). Baris milik cabang
-- lain tidak ditemukan (0 baris) sehingga keberadaannya tidak bocor.
SELECT o.status::text AS status
  FROM orders o
 WHERE o.id = sqlc.arg(id)::uuid
   AND o.branch_id = sqlc.arg(branch_id)::uuid
 FOR UPDATE;

-- name: ListOrderLinesForSubmit :many
-- Baris draft beserta keadaan master DAN harga cabang saat submit: produk
-- aktif, harga dari branch_prices cabang sesi (bukan harga_default, bukan
-- angka klien). Dipakai memvalidasi ulang dan menyegarkan snapshot
-- nama/harga (BR-05) DI DALAM transaksi submit, sehingga perubahan
-- master sesudah draft disimpan ikut terpakai.
SELECT l.line_no,
       l.jenis::text          AS jenis,
       l.nama_snapshot,

       l.qty,
       (p.id IS NOT NULL)::bool     AS produk_ada,
       COALESCE(p.is_active, false) AS produk_aktif,
       COALESCE(p.nama, '')         AS produk_nama,

       COALESCE(p.jenis::text, '')::text AS produk_jenis,
       COALESCE(bp.harga, 0)::bigint AS harga_cabang,
       (bp.product_id IS NOT NULL)::bool AS ada_harga_cabang
  FROM order_lines l
  LEFT JOIN products p ON p.id = l.product_id
  LEFT JOIN branch_prices bp
         ON bp.product_id = l.product_id
        AND bp.branch_id = sqlc.arg(branch_id)::uuid
 WHERE l.order_id = sqlc.arg(order_id)::uuid
 ORDER BY l.line_no;

-- name: UpdateOrderLineSnapshot :exec
-- Menyegarkan snapshot satu baris saat submit. subtotal ikut berubah karena
-- kolomnya GENERATED ALWAYS (qty * harga_satuan), dan trigger
-- order_lines_recalc menyinkronkan orders.total.
UPDATE order_lines
   SET nama_snapshot = sqlc.arg(nama_snapshot),
       harga_satuan  = sqlc.arg(harga_satuan)
 WHERE order_id = sqlc.arg(order_id)::uuid
   AND line_no = sqlc.arg(line_no)::int;

-- name: SetOrderLineSerial :exec
-- Serial number resmi satu baris produk (BR-02): YYYYMMDDXXX. Baris packaging
-- tidak pernah melewati query ini (constraint packaging_tanpa_serial).
UPDATE order_lines
   SET serial_number = sqlc.arg(serial_number)
 WHERE order_id = sqlc.arg(order_id)::uuid
   AND line_no = sqlc.arg(line_no)::int;

-- name: SumOrderLineSubtotals :one
-- Total pesanan dihitung ULANG di server dari barisnya (BR-10); angka klien
-- tidak pernah dipakai. Dibaca setelah snapshot harga disegarkan.
SELECT COALESCE(sum(subtotal), 0)::bigint AS total
  FROM order_lines
 WHERE order_id = sqlc.arg(order_id)::uuid;

-- name: SetOrderSubmitted :execrows
-- Transisi draft -> submitted: nomor, tanggal bisnis WIB, penanda C&C
-- (cabang berdeposit), total, dan waktu submit (UTC). Status 'draft'
-- diperiksa ulang di klausa WHERE supaya submit bersamaan tidak bisa
-- menembus dua kali; trigger orders_guard_submit menegakkan invariant isi
-- baris (serial lengkap, aturan sertifikat/packaging, total cocok).
UPDATE orders
   SET status       = 'submitted',
       nomor        = sqlc.arg(nomor),
       tanggal      = sqlc.arg(tanggal)::date,
       is_cnc       = sqlc.arg(is_cnc),
       total        = sqlc.arg(total),
       submitted_at = now()
 WHERE id = sqlc.arg(id)::uuid
   AND branch_id = sqlc.arg(branch_id)::uuid
   AND status = 'draft';

-- name: InsertOrderEvent :exec
-- Timeline pesanan (PRD CP-ORD-08): "Pesanan dikirim ...". Aktornya dicatat
-- apa adanya (aktor cabang sesi) dan ditulis di transaksi submit yang sama.
INSERT INTO order_events (order_id, icon, teks, actor_kind, actor_id)
VALUES (sqlc.arg(order_id)::uuid, sqlc.arg(icon), sqlc.arg(teks),
        sqlc.arg(actor_kind)::actor_kind, sqlc.narg(actor_id)::uuid);
