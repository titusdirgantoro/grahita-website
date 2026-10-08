-- Rekening bank tujuan transfer (PRD §4.6, Database.md §4.3).
-- Kolom `bank` bertipe enum bank_code; dikembalikan sebagai teks supaya
-- lapisan repository tidak perlu mengimpor tipe enum hasil generate.

-- name: ListBankAccounts :many
SELECT id, bank::text AS bank, nama_pemilik, nomor, is_active
  FROM bank_accounts
 ORDER BY bank, nomor;

-- name: ListActiveBankAccounts :many
SELECT id, bank::text AS bank, nama_pemilik, nomor, is_active
  FROM bank_accounts
 WHERE is_active
 ORDER BY bank, nomor;

-- name: GetBankAccount :one
SELECT id, bank::text AS bank, nama_pemilik, nomor, is_active
  FROM bank_accounts
 WHERE id = $1;

-- name: CreateBankAccount :one
INSERT INTO bank_accounts (bank, nama_pemilik, nomor, is_active)
VALUES (sqlc.arg(bank)::bank_code, sqlc.arg(nama_pemilik), sqlc.arg(nomor), sqlc.arg(is_active))
RETURNING id, bank::text AS bank, nama_pemilik, nomor, is_active;

-- name: UpdateBankAccount :one
UPDATE bank_accounts
   SET bank         = sqlc.arg(bank)::bank_code,
       nama_pemilik = sqlc.arg(nama_pemilik),
       nomor        = sqlc.arg(nomor),
       is_active    = sqlc.arg(is_active)
 WHERE id = sqlc.arg(id)
RETURNING id, bank::text AS bank, nama_pemilik, nomor, is_active;
