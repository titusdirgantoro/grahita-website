-- Master cabang (SystemDesign §6, PRD §4.4).

-- name: GetBranch :one
-- Dipakai portal cabang untuk /me/branch (data cabang milik sesi).
SELECT id, nama, nama_kepala, email::text AS email, telepon,
       is_cnc, is_special, pajak_in, is_active
  FROM branches
 WHERE id = $1;

-- ---------- Master cabang: Admin Portal (F1-06) ----------
--
-- Query di bawah dipakai paket internal/branch. Dua kolom terakhir adalah
-- token registrasi yang MASIH berlaku (belum terpakai dan belum kedaluwarsa)
-- untuk ditampilkan di halaman Cabang (PRD §4.4).

-- name: ListBranchDetails :many
-- Filter opsional memakai sqlc.narg: q (pola ILIKE nama/email), aktif, cnc,
-- special. q dikirim sudah berbentuk pola '%kata%'.
SELECT b.id,
       b.nama,
       b.nama_kepala,
       b.email::text                 AS email,
       COALESCE(b.telepon, '')       AS telepon,
       b.is_cnc,
       b.is_special,
       b.pajak_in,
       b.is_active,
       COALESCE(t.token_prefix, '')  AS token_prefix,
       t.expires_at                  AS token_expires_at
  FROM branches b
  LEFT JOIN registration_tokens t
         ON t.branch_id = b.id
        AND t.used_at IS NULL
        AND t.expires_at > now()
 WHERE (sqlc.narg(q)::text IS NULL
        OR b.nama ILIKE sqlc.narg(q)::text
        OR b.email::text ILIKE sqlc.narg(q)::text)
   AND (sqlc.narg(aktif)::boolean IS NULL OR b.is_active = sqlc.narg(aktif)::boolean)
   AND (sqlc.narg(cnc)::boolean IS NULL OR b.is_cnc = sqlc.narg(cnc)::boolean)
   AND (sqlc.narg(special)::boolean IS NULL OR b.is_special = sqlc.narg(special)::boolean)
 ORDER BY b.nama;

-- name: GetBranchDetail :one
SELECT b.id,
       b.nama,
       b.nama_kepala,
       b.email::text                 AS email,
       COALESCE(b.telepon, '')       AS telepon,
       b.is_cnc,
       b.is_special,
       b.pajak_in,
       b.is_active,
       COALESCE(t.token_prefix, '')  AS token_prefix,
       t.expires_at                  AS token_expires_at
  FROM branches b
  LEFT JOIN registration_tokens t
         ON t.branch_id = b.id
        AND t.used_at IS NULL
        AND t.expires_at > now()
 WHERE b.id = $1;

-- name: CreateBranchDetail :one
INSERT INTO branches (nama, nama_kepala, email, telepon, is_cnc, is_special, pajak_in, is_active)
VALUES (
    sqlc.arg(nama),
    sqlc.arg(nama_kepala),
    sqlc.arg(email),
    sqlc.arg(telepon),
    sqlc.arg(is_cnc),
    sqlc.arg(is_special),
    sqlc.arg(pajak_in),
    sqlc.arg(is_active)
)
RETURNING id;

-- name: UpdateBranchDetail :exec
-- Perubahan penuh satu cabang. Email cabang = email login akun cabang, jadi
-- pemanggil wajib ikut menyelaraskan branch_users.email di transaksi yang sama.
UPDATE branches
   SET nama        = sqlc.arg(nama),
       nama_kepala = sqlc.arg(nama_kepala),
       email       = sqlc.arg(email),
       telepon     = sqlc.arg(telepon),
       is_cnc      = sqlc.arg(is_cnc),
       is_special  = sqlc.arg(is_special),
       pajak_in    = sqlc.arg(pajak_in),
       is_active   = sqlc.arg(is_active),
       updated_at  = now()
 WHERE id = sqlc.arg(id);

-- name: UpdateBranchLoginEmail :exec
-- Menyelaraskan email login akun cabang dengan email cabang (Database.md §4.1:
-- email cabang = email login).
UPDATE branch_users
   SET email      = sqlc.arg(email),
       updated_at = now()
 WHERE branch_id = sqlc.arg(branch_id);

-- name: BranchHasOrders :one
SELECT EXISTS (SELECT 1 FROM orders WHERE branch_id = $1)::boolean;

-- name: BranchHasAccount :one
SELECT EXISTS (SELECT 1 FROM branch_users WHERE branch_id = $1)::boolean;

-- name: BranchEmailTaken :one
-- Email cabang unik; except_id dipakai saat mengubah cabang itu sendiri.
SELECT EXISTS (
    SELECT 1 FROM branches
     WHERE email = sqlc.arg(email)
       AND (sqlc.narg(except_id)::uuid IS NULL OR id <> sqlc.narg(except_id)::uuid)
)::boolean;

-- name: AdminEmailTaken :one
-- Login mencari akun admin LEBIH DULU daripada akun cabang, jadi email yang
-- sudah dipakai admin tidak boleh dipakai cabang (kalau tidak, cabang itu
-- tidak akan pernah bisa masuk).
SELECT EXISTS (SELECT 1 FROM admin_users WHERE email = $1)::boolean;

-- name: DeleteBranchSessions :exec
-- Sesi cabang tidak memakai ON DELETE CASCADE, jadi dibersihkan lebih dulu
-- supaya cabang yang benar-benar belum punya pesanan bisa dihapus.
DELETE FROM sessions
 WHERE sessions.branch_id = $1
    OR user_id IN (SELECT id FROM branch_users WHERE branch_users.branch_id = $1);

-- name: DeleteBranchUsers :exec
DELETE FROM branch_users WHERE branch_users.branch_id = $1;

-- name: DeleteBranchRegistrationTokens :exec
-- Seluruh token cabang ikut terhapus saat cabangnya dihapus (relasi sudah
-- ON DELETE CASCADE; eksplisit di sini supaya niatnya terbaca).
DELETE FROM registration_tokens WHERE registration_tokens.branch_id = $1;

-- name: DeleteBranch :exec
DELETE FROM branches WHERE id = $1;

-- name: ReplaceRegistrationToken :exec
-- Token lama yang belum terpakai digantikan token baru (PRD AP-CAB-02);
-- token yang sudah terpakai dibiarkan sebagai jejak.
DELETE FROM registration_tokens WHERE branch_id = $1 AND used_at IS NULL;

-- name: CreateRegistrationToken :one
INSERT INTO registration_tokens (branch_id, token_hash, token_prefix, expires_at, created_by)
VALUES (
    sqlc.arg(branch_id),
    sqlc.arg(token_hash),
    sqlc.arg(token_prefix),
    sqlc.arg(expires_at),
    sqlc.narg(created_by)::uuid
)
RETURNING expires_at;
