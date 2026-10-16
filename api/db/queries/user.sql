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
       COALESCE(bu.is_active AND b.is_active
                AND b.identity_status = 'registered'
                AND b.nama_kepala IS NOT NULL AND b.email IS NOT NULL, false)::boolean AS boleh_login
  FROM branch_users bu
  JOIN branches b ON b.id = bu.branch_id
 WHERE bu.email = $1;

-- name: GetBranchUserByID :one
SELECT bu.id, bu.email::text AS email, bu.password_hash, bu.is_active, bu.branch_id,
       b.nama AS branch_nama,
       COALESCE(bu.is_active AND b.is_active
                AND b.identity_status = 'registered'
                AND b.nama_kepala IS NOT NULL AND b.email IS NOT NULL, false)::boolean AS boleh_login
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

-- name: ActivateAdminUser :exec
-- Aktivasi admin hasil undangan: password_hash DAN activated_at WAJIB terisi
-- bersamaan, karena aturan boleh_login menolak akun bila salah satunya NULL
-- (kewajiban turunan F1-04). activated_at tidak pernah ditimpa bila sudah ada.
UPDATE admin_users
   SET password_hash = $2,
       activated_at  = COALESCE(activated_at, now()),
       updated_at    = now()
 WHERE id = $1;

-- name: UpdateAdminNama :exec
-- Profil sendiri (PRD §4.10): admin mengubah NAMA TAMPILANNYA. Email dan role
-- tidak pernah diubah di sini — keduanya read-only dan hanya Superadmin yang
-- boleh mengubahnya lewat user management (PUT /admin/users/admin/{id}).
UPDATE admin_users
   SET nama = $2,
       updated_at = now()
 WHERE id = $1;

-- ---------- User management (PRD §4.8) ----------

-- name: ListBranchAccountRows :many
-- Satu baris per CABANG (bukan per akun) supaya cabang yang belum registrasi
-- tetap tampil dengan status "belum registrasi" (PRD §4.8).
SELECT b.id::text                                          AS branch_id,
       b.nama,
       COALESCE(b.nama_kepala, '')                          AS nama_kepala,
       COALESCE(b.email::text, ''::text)::text                          AS email,
       b.identity_status::text                              AS identity_status,
       b.is_active                                         AS branch_active,
       COALESCE(bu.id::text, '')::text                      AS user_id,
       COALESCE(bu.telepon, '')                            AS telepon,
       COALESCE(bu.is_active, false)::boolean              AS user_active,
       bu.last_login_at,
       COALESCE(t.token_prefix, '')                        AS token_prefix,
       t.expires_at                                        AS token_expires_at
  FROM branches b
  LEFT JOIN branch_users bu ON bu.branch_id = b.id
  LEFT JOIN registration_tokens t
         ON t.branch_id = b.id
        AND t.used_at IS NULL
       AND t.revoked_at IS NULL
        AND t.expires_at > now()
 ORDER BY b.nama;

-- name: GetBranchAccountRow :one
-- Satu baris akun cabang berdasarkan id branch_users (dipakai setelah mutasi).
SELECT b.id::text                                          AS branch_id,
       b.nama,
       COALESCE(b.nama_kepala, '')                          AS nama_kepala,
       COALESCE(b.email::text, ''::text)::text                          AS email,
       b.identity_status::text                              AS identity_status,
       b.is_active                                         AS branch_active,
       COALESCE(bu.id::text, '')::text                      AS user_id,
       COALESCE(bu.telepon, '')                            AS telepon,
       COALESCE(bu.is_active, false)::boolean              AS user_active,
       bu.last_login_at,
       COALESCE(t.token_prefix, '')                        AS token_prefix,
       t.expires_at                                        AS token_expires_at
  FROM branch_users bu
  JOIN branches b ON b.id = bu.branch_id
  LEFT JOIN registration_tokens t
         ON t.branch_id = b.id
        AND t.used_at IS NULL
       AND t.revoked_at IS NULL
        AND t.expires_at > now()
 WHERE bu.id = $1;

-- name: GetBranchUserBrief :one
SELECT id, branch_id, email::text AS email, COALESCE(telepon, '') AS telepon, is_active
  FROM branch_users
 WHERE id = $1;

-- name: SetBranchUserActive :exec
UPDATE branch_users SET is_active = $2, updated_at = now() WHERE id = $1;

-- name: ListAdminUsers :many
SELECT id, nama, email::text AS email, role::text AS role,
       notif_invoice, notif_surat_jalan, is_active, invited_at, activated_at, last_login_at
  FROM admin_users
 ORDER BY nama;

-- name: GetAdminUserRow :one
SELECT id, nama, email::text AS email, role::text AS role,
       notif_invoice, notif_surat_jalan, is_active, invited_at, activated_at, last_login_at
  FROM admin_users
 WHERE id = $1;

-- name: GetAdminUserRowByEmail :one
SELECT id, nama, email::text AS email, role::text AS role,
       notif_invoice, notif_surat_jalan, is_active, invited_at, activated_at, last_login_at
  FROM admin_users
 WHERE email = $1;

-- name: EmailTakenByBranch :one
-- Email sudah dipakai cabang: baik email login akun cabang (branch_users)
-- maupun email cabang itu sendiri (branches, termasuk cabang yang belum
-- registrasi). Login mencari akun admin lebih dulu, jadi email yang dipegang
-- cabang tidak boleh diundang sebagai admin — kalau tidak, akun cabang itu
-- tidak akan pernah bisa masuk. Cermin dari branch.AdminEmailTaken (F1-06);
-- tanpa keduanya, salah satu arah saja yang aman.
SELECT (EXISTS (SELECT 1 FROM branch_users bu WHERE bu.email = $1)
     OR EXISTS (SELECT 1 FROM branches b WHERE b.email = $1 AND b.email IS NOT NULL))::boolean;

-- name: CreateAdminInvitation :one
-- Admin undangan: password_hash dan activated_at dibiarkan NULL sehingga tidak
-- bisa login dan tidak menerima email dokumen (PRD AP-USR-02).
INSERT INTO admin_users (nama, email, role, notif_invoice, notif_surat_jalan, is_active, invited_at)
VALUES (sqlc.arg(nama), sqlc.arg(email), sqlc.arg(role)::admin_role,
        sqlc.arg(notif_invoice), sqlc.arg(notif_surat_jalan), true, now())
RETURNING id, nama, email::text AS email, role::text AS role,
          notif_invoice, notif_surat_jalan, is_active, invited_at, activated_at, last_login_at;

-- name: UpdateAdminUserRow :one
UPDATE admin_users
   SET nama              = sqlc.arg(nama),
       role              = sqlc.arg(role)::admin_role,
       notif_invoice     = sqlc.arg(notif_invoice),
       notif_surat_jalan = sqlc.arg(notif_surat_jalan),
       is_active         = sqlc.arg(is_active),
       updated_at        = now()
 WHERE id = sqlc.arg(id)
RETURNING id, nama, email::text AS email, role::text AS role,
          notif_invoice, notif_surat_jalan, is_active, invited_at, activated_at, last_login_at;

-- name: ListInvoiceRecipients :many
-- Penerima salinan invoice: HANYA admin aktif, sudah diaktivasi, dan
-- flag-nya menyala (PRD AP-USR-01/AP-USR-02). Alamat tidak pernah di-hardcode.
SELECT email::text AS email
  FROM admin_users
 WHERE is_active
   AND password_hash IS NOT NULL
   AND activated_at IS NOT NULL
   AND notif_invoice
 ORDER BY email;

-- name: ListSuratJalanRecipients :many
SELECT email::text AS email
  FROM admin_users
 WHERE is_active
   AND password_hash IS NOT NULL
   AND activated_at IS NOT NULL
   AND notif_surat_jalan
 ORDER BY email;
