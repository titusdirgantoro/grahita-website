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

-- name: ProductHasHistory :one
-- Produk yang sudah dipakai order line, atau sudah punya varian dengan riwayat
-- tarif berversi (impor maupun edit matriks), tidak boleh dihapus — keduanya
-- memakai ON DELETE RESTRICT; nonaktifkan saja produknya.
SELECT (EXISTS (SELECT 1 FROM order_lines ol WHERE ol.product_id = sqlc.arg(product_id)::uuid)
     OR EXISTS (SELECT 1 FROM product_variants pv WHERE pv.product_id = sqlc.arg(product_id)::uuid))::boolean;

-- ---------- Matriks harga per cabang ----------

-- name: ListBranchPrices :many
-- Sel matriks yang BERISI harga. Sel kosong tidak punya baris dan berarti
-- produk tidak dijual ke cabang tersebut (PRD AP-KAT-03).
--
-- Komponen HJM/P1/SPV diambil dari versi tarif aktif varian utama produk,
-- hanya bila tarif finalnya sama dengan harga sel (komponen benar-benar
-- menjelaskan harga). Sel harga lama tanpa versi yang cocok mengembalikan
-- komponen NULL dan source_kind kosong.
SELECT bp.branch_id, bp.product_id, bp.harga,
       t.hjm_rupiah, COALESCE(t.p1_ratio, '')::text AS p1_ratio, t.spv_rupiah,
       COALESCE(t.source_kind, '')::text AS source_kind
  FROM branch_prices bp
  LEFT JOIN LATERAL (
        SELECT tv.hjm_rupiah, tv.p1_ratio::text AS p1_ratio, tv.spv_rupiah, tv.source_kind
          FROM product_variants v
          JOIN branch_tariff_versions tv ON tv.variant_id = v.id
         WHERE v.product_id = bp.product_id
           AND v.tariff_parent_variant_id IS NULL
           AND tv.branch_id = bp.branch_id
           AND tv.superseded_at IS NULL
           AND tv.is_available
           AND tv.final_item_rupiah = bp.harga
         ORDER BY v.created_at, v.id
         LIMIT 1
       ) t ON true
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR bp.branch_id = sqlc.narg(branch_id)::uuid)
   AND (sqlc.narg(product_id)::uuid IS NULL OR bp.product_id = sqlc.narg(product_id)::uuid)
 ORDER BY bp.branch_id, bp.product_id;

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

-- ---------- Versi tarif manual dari matriks harga ----------
--
-- Edit matriks menulis komponen HJM/P1/SPV sebagai versi tarif append-only
-- (source_kind 'manual'), lalu memproyeksikan tarif finalnya ke branch_prices
-- yang dibaca katalog dan order (docs/design/master-impact-import.md §5.3).

-- name: PrimaryVariantOfProduct :one
-- Varian utama (bukan turunan) tempat tarif produk disimpan.
SELECT id
  FROM product_variants
 WHERE product_id = $1 AND tariff_parent_variant_id IS NULL
 ORDER BY created_at, id
 LIMIT 1;

-- name: CreatePrimaryVariant :one
-- Varian utama untuk produk yang belum pernah bertarif berversi (produk buatan
-- admin, bukan hasil impor). Kode diturunkan dari id produk supaya pasti unik
-- dan tidak bertabrakan dengan kode immutable varian impor.
INSERT INTO product_variants (product_id, code, nama)
SELECT p.id, 'produk-' || p.id::text, p.nama
  FROM products p
 WHERE p.id = $1
RETURNING id;

-- name: ActiveTariffVersion :one
SELECT id, is_available
  FROM branch_tariff_versions
 WHERE branch_id = $1 AND variant_id = $2 AND superseded_at IS NULL
 FOR UPDATE;

-- name: SupersedeTariffVersion :exec
UPDATE branch_tariff_versions SET superseded_at = now() WHERE id = $1;

-- name: InsertManualTariffVersion :exec
INSERT INTO branch_tariff_versions
       (branch_id, variant_id, hjm_rupiah, p1_ratio, spv_rupiah, final_item_rupiah,
        is_available, source_kind, created_by, source_component_hash)
VALUES (sqlc.arg(branch_id), sqlc.arg(variant_id), sqlc.arg(hjm_rupiah),
        (sqlc.arg(p1_ratio)::text)::numeric, sqlc.arg(spv_rupiah), sqlc.arg(final_item_rupiah),
        true, 'manual', sqlc.arg(created_by), sqlc.arg(source_component_hash));

-- name: InsertManualTariffTombstone :exec
-- Penanda "tidak dijual lagi" untuk pasangan yang sebelumnya bertarif aktif.
INSERT INTO branch_tariff_versions
       (branch_id, variant_id, is_available, source_kind, created_by, source_component_hash)
VALUES (sqlc.arg(branch_id), sqlc.arg(variant_id), false, 'manual',
        sqlc.arg(created_by), sqlc.arg(source_component_hash));
