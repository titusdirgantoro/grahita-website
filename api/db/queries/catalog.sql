-- Katalog produk & matriks harga per cabang (SystemDesign §6, PRD §3.4/§4.5).
--
-- Kolom `jenis` bertipe enum product_kind; dikembalikan sebagai teks supaya
-- lapisan repository tidak perlu mengimpor tipe enum hasil generate.

-- ---------- Pembacaan katalog ----------

-- name: ListProducts :many
SELECT id, kode, nama, jenis::text AS jenis,
       COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active
  FROM products
 ORDER BY urutan, nama;

-- name: ListActiveProducts :many
SELECT id, kode, nama, jenis::text AS jenis,
       COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active
  FROM products
 WHERE is_active
 ORDER BY urutan, nama;

-- name: ListProductsForBranch :many
-- Hanya produk yang benar-benar dijual ke cabang tersebut (punya baris harga).
-- Harga default TIDAK dipakai sebagai cadangan: sel kosong = tidak dijual
-- (PRD CP-KAT-01/02).
SELECT p.id, p.nama, p.jenis::text AS jenis,
       COALESCE(p.deskripsi, '') AS deskripsi, bp.harga, p.urutan
  FROM products p
  JOIN branch_prices bp ON bp.product_id = p.id
 WHERE bp.branch_id = $1 AND p.is_active
 ORDER BY p.urutan, p.nama;

-- ---------- Master produk: Admin Portal (F1-08) ----------

-- name: ListProductsForAdmin :many
-- Daftar produk untuk halaman katalog admin: TERMASUK produk nonaktif supaya
-- bisa diaktifkan kembali. Filter opsional: q (pola ILIKE nama/kode, sudah
-- berbentuk '%kata%'), jenis, aktif.
SELECT id, kode, nama, jenis::text AS jenis,
       COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active
  FROM products
 WHERE (sqlc.narg(q)::text IS NULL
        OR nama ILIKE sqlc.narg(q)::text
        OR COALESCE(kode, '') ILIKE sqlc.narg(q)::text)
   AND (sqlc.narg(jenis)::product_kind IS NULL OR jenis = sqlc.narg(jenis)::product_kind)
   AND (sqlc.narg(aktif)::boolean IS NULL OR is_active = sqlc.narg(aktif)::boolean)
 ORDER BY urutan, nama;

-- name: GetProductForAdmin :one
SELECT id, kode, nama, jenis::text AS jenis,
       COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active
  FROM products
 WHERE id = $1;

-- name: CreateProduct :one
INSERT INTO products (kode, nama, jenis, deskripsi, harga_default, urutan, is_active)
VALUES (
    sqlc.narg(kode),
    sqlc.arg(nama),
    sqlc.arg(jenis)::product_kind,

    sqlc.narg(deskripsi),
    sqlc.arg(harga_default),
    sqlc.arg(urutan),
    sqlc.arg(is_active)
)
RETURNING id, kode, nama, jenis::text AS jenis,
          COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active;

-- name: UpdateProduct :one
-- Perubahan penuh satu produk. Harga default bukan cadangan katalog: ia hanya
-- dipakai sebagai nilai awal saat mengisi matriks harga.
UPDATE products
   SET kode          = sqlc.narg(kode),
       nama          = sqlc.arg(nama),
       jenis         = sqlc.arg(jenis)::product_kind,

       deskripsi     = sqlc.narg(deskripsi),
       harga_default = sqlc.arg(harga_default),
       urutan        = sqlc.arg(urutan),
       is_active     = sqlc.arg(is_active),
       updated_at    = now()
 WHERE id = sqlc.arg(id)
RETURNING id, kode, nama, jenis::text AS jenis,
          COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active;

-- name: DeleteProduct :exec
DELETE FROM products WHERE id = $1;

-- name: ProductKodeTaken :one
-- Kode produk unik (NULL boleh berulang). except_id dipakai saat mengubah
-- produk itu sendiri.
SELECT EXISTS (
    SELECT 1 FROM products
     WHERE kode = sqlc.arg(kode)
       AND (sqlc.narg(except_id)::uuid IS NULL OR id <> sqlc.narg(except_id)::uuid)
)::boolean;

-- name: ProductHasOrderLines :one
-- Produk yang sudah dipakai order line tidak boleh dihapus (order_lines
-- memakai ON DELETE RESTRICT); nonaktifkan saja produknya.
SELECT EXISTS (SELECT 1 FROM order_lines WHERE product_id = $1)::boolean;

-- ---------- Matriks harga per cabang ----------

-- name: ListBranchPrices :many
-- Sel matriks yang BERISI harga. Sel kosong tidak punya baris dan berarti
-- produk tidak dijual ke cabang tersebut (PRD AP-KAT-03).
SELECT branch_id, product_id, harga
  FROM branch_prices
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR branch_id = sqlc.narg(branch_id)::uuid)
   AND (sqlc.narg(product_id)::uuid IS NULL OR product_id = sqlc.narg(product_id)::uuid)
 ORDER BY branch_id, product_id;

-- name: UpsertBranchPrice :exec
INSERT INTO branch_prices (branch_id, product_id, harga, updated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (branch_id, product_id) DO UPDATE
   SET harga      = EXCLUDED.harga,
       updated_by = EXCLUDED.updated_by,
       updated_at = now();

-- name: DeleteBranchPrice :exec
-- Menghapus sel = produk tidak lagi dijual ke cabang itu (PRD AP-KAT-03).
DELETE FROM branch_prices WHERE branch_id = $1 AND product_id = $2;

-- name: DeleteBranchPricesOfBranch :exec
-- Dipakai aksi "salin harga": matriks cabang tujuan digantikan seluruhnya oleh
-- matriks cabang sumber.
DELETE FROM branch_prices WHERE branch_id = $1;
