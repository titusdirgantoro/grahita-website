-- Katalog produk & matriks harga per cabang (SystemDesign §6).

-- name: ListProducts :many
SELECT id, kode, nama, jenis::text AS jenis, satuan,
       COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active
  FROM products
 ORDER BY urutan, nama;

-- name: ListActiveProducts :many
SELECT id, kode, nama, jenis::text AS jenis, satuan,
       COALESCE(deskripsi, '') AS deskripsi, harga_default, urutan, is_active
  FROM products
 WHERE is_active
 ORDER BY urutan, nama;

-- name: ListProductsForBranch :many
-- Hanya produk yang benar-benar dijual ke cabang tersebut (punya baris harga).
SELECT p.id, p.nama, p.jenis::text AS jenis, p.satuan,
       COALESCE(p.deskripsi, '') AS deskripsi, bp.harga, p.urutan
  FROM products p
  JOIN branch_prices bp ON bp.product_id = p.id
 WHERE bp.branch_id = $1 AND p.is_active
 ORDER BY p.urutan, p.nama;

-- name: CreateProduct :one
INSERT INTO products (kode, nama, jenis, satuan, deskripsi, harga_default, urutan)
VALUES (
    sqlc.arg(kode),
    sqlc.arg(nama),
    sqlc.arg(jenis)::product_kind,
    sqlc.arg(satuan),
    sqlc.arg(deskripsi),
    sqlc.arg(harga_default),
    sqlc.arg(urutan)
)
RETURNING id;

-- name: UpsertBranchPrice :exec
INSERT INTO branch_prices (branch_id, product_id, harga, updated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (branch_id, product_id) DO UPDATE
   SET harga      = EXCLUDED.harga,
       updated_by = EXCLUDED.updated_by,
       updated_at = now();
