-- Akun cabang & admin (PRD §2). Password selalu Argon2id.

-- name: GetAdminUserByEmail :one
SELECT id, nama, email::text AS email, password_hash, role::text AS role, is_active
  FROM admin_users
 WHERE email = $1;

-- name: GetAdminUserByID :one
SELECT id, nama, email::text AS email, password_hash, role::text AS role, is_active
  FROM admin_users
 WHERE id = $1;

-- name: GetBranchUserByEmail :one
SELECT bu.id, bu.email::text AS email, bu.password_hash, bu.is_active, bu.branch_id,
       b.nama AS branch_nama
  FROM branch_users bu
  JOIN branches b ON b.id = bu.branch_id
 WHERE bu.email = $1;

-- name: GetBranchUserByID :one
SELECT bu.id, bu.email::text AS email, bu.password_hash, bu.is_active, bu.branch_id,
       b.nama AS branch_nama
  FROM branch_users bu
  JOIN branches b ON b.id = bu.branch_id
 WHERE bu.id = $1;

-- name: TouchAdminLogin :exec
UPDATE admin_users SET last_login_at = now() WHERE id = $1;

-- name: TouchBranchLogin :exec
UPDATE branch_users SET last_login_at = now() WHERE id = $1;
