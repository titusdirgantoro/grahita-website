-- Master cabang (SystemDesign §6, PRD §4).

-- name: ListBranches :many
SELECT id, nama, nama_kepala, email::text AS email, telepon,
       is_cnc, is_special, pajak_in, is_active
  FROM branches
 ORDER BY nama;

-- name: GetBranch :one
SELECT id, nama, nama_kepala, email::text AS email, telepon,
       is_cnc, is_special, pajak_in, is_active
  FROM branches
 WHERE id = $1;

-- name: CreateBranch :one
INSERT INTO branches (nama, nama_kepala, email, telepon, is_cnc, is_special, pajak_in)
VALUES (
    sqlc.arg(nama),
    sqlc.arg(nama_kepala),
    sqlc.arg(email),
    sqlc.arg(telepon),
    sqlc.arg(is_cnc),
    sqlc.arg(is_special),
    sqlc.arg(pajak_in)
)
RETURNING id;

-- name: UpdateBranchFlags :exec
UPDATE branches
   SET is_cnc     = $2,
       is_special = $3,
       pajak_in   = $4,
       is_active  = $5
 WHERE id = $1;
