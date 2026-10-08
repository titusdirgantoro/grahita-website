-- Akun cabang & admin (PRD §2). Password selalu Argon2id.
--
-- `boleh_login` adalah satu-satunya sumber kebenaran untuk "akun ini boleh
-- masuk": akun aktif DAN (untuk cabang) cabangnya masih aktif, DAN (untuk
-- admin) sudah diaktivasi (password_hash + activated_at terisi). Menyimpan
-- aturannya di query membuat handler/service tidak pernah menebak-nebak.

-- name: GetAdminUserByEmail :one
SELECT id, nama, email::text AS email, password_hash, role::text AS role, is_active,
       COALESCE(is_active AND password_hash IS NOT NULL AND activated_at IS NOT NULL, false)::boolean AS boleh_login
  FROM admin_users
 WHERE email = $1;

-- name: GetAdminUserByID :one
SELECT id, nama, email::text AS email, password_hash, role::text AS role, is_active,
       COALESCE(is_active AND password_hash IS NOT NULL AND activated_at IS NOT NULL, false)::boolean AS boleh_login
  FROM admin_users
 WHERE id = $1;

-- name: GetBranchUserByEmail :one
SELECT bu.id, bu.email::text AS email, bu.password_hash, bu.is_active, bu.branch_id,
       b.nama AS branch_nama,
       COALESCE(bu.is_active AND b.is_active, false)::boolean AS boleh_login
  FROM branch_users bu
  JOIN branches b ON b.id = bu.branch_id
 WHERE bu.email = $1;

-- name: GetBranchUserByID :one
SELECT bu.id, bu.email::text AS email, bu.password_hash, bu.is_active, bu.branch_id,
       b.nama AS branch_nama,
       COALESCE(bu.is_active AND b.is_active, false)::boolean AS boleh_login
  FROM branch_users bu
  JOIN branches b ON b.id = bu.branch_id
 WHERE bu.id = $1;

-- name: TouchAdminLogin :exec
UPDATE admin_users SET last_login_at = now() WHERE id = $1;

-- name: TouchBranchLogin :exec
UPDATE branch_users SET last_login_at = now() WHERE id = $1;

-- name: UpdateAdminPassword :exec
UPDATE admin_users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: UpdateBranchPassword :exec
UPDATE branch_users SET password_hash = $2, updated_at = now() WHERE id = $1;
