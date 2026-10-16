-- Pendaftaran akun cabang lewat token registrasi (PRD §3.1, SystemDesign.md §4.1).
--
-- Token disimpan sebagai SHA-256 (registration_tokens.token_hash); pencarian
-- selalu lewat hash, tidak pernah lewat nilai mentah. Dua query pertama
-- mengembalikan keadaan token BESERTA cabangnya sekaligus sehingga service
-- bisa memberi pesan yang spesifik (tidak valid / kedaluwarsa / sudah dipakai
-- / cabang nonaktif / cabang sudah punya akun) tanpa membaca tabel lain.

-- name: LookupRegistrationToken :one
-- Dipakai langkah 1 (validasi token) dan pemeriksaan awal langkah 2: tanpa
-- kunci baris, hanya membaca keadaan sekarang.
SELECT t.id            AS token_id,
       t.branch_id,
       t.expires_at,
       t.used_at,
       b.nama,
       COALESCE(b.nama_kepala, '') AS nama_kepala,
       COALESCE(b.email::text, ''::text)::text AS email,
       b.identity_status::text AS identity_status,
       b.is_active     AS branch_active,
       EXISTS (SELECT 1 FROM branch_users bu WHERE bu.branch_id = b.id)::boolean AS branch_has_account
  FROM registration_tokens t
  JOIN branches b ON b.id = t.branch_id
 WHERE t.token_hash = $1
   AND t.revoked_at IS NULL;

-- name: LockRegistrationToken :one
-- Sama dengan LookupRegistrationToken, tetapi MENGUNCI baris token
-- (FOR UPDATE OF t) supaya dua permintaan register bersamaan tidak bisa
-- sama-sama lolos: yang kedua menunggu commit yang pertama, lalu membaca
-- ulang used_at yang sudah terisi (SystemDesign.md §4.1).
SELECT t.id            AS token_id,
       t.branch_id,
       t.expires_at,
       t.used_at,
       b.nama,
       COALESCE(b.nama_kepala, '') AS nama_kepala,
       COALESCE(b.email::text, ''::text)::text AS email,
       b.identity_status::text AS identity_status,
       b.is_active     AS branch_active,
       EXISTS (SELECT 1 FROM branch_users bu WHERE bu.branch_id = b.id)::boolean AS branch_has_account
  FROM registration_tokens t
  JOIN branches b ON b.id = t.branch_id
 WHERE t.token_hash = $1
   AND t.revoked_at IS NULL
   FOR UPDATE OF t;

-- name: CreateBranchAccount :one
-- Akun cabang baru. Email akun = email cabang (email login, Database.md §4.1);
-- email TIDAK diambil dari payload klien. branch_id UNIQUE menjaga "1 cabang
-- = 1 akun" walau ada jalur yang melewatkan pemeriksaan service (PRD CP-REG-06).
INSERT INTO branch_users (branch_id, email, password_hash, telepon, is_active)
VALUES (
    sqlc.arg(branch_id),
    sqlc.arg(email),
    sqlc.arg(password_hash),
    sqlc.arg(telepon),
    true
)
RETURNING id, email::text AS email;

-- name: MarkRegistrationTokenUsed :execrows
-- Menandai token terpakai SEKALI saja. Syarat used_at IS NULL membuat
-- pemakaian kedua tidak pernah bisa menimpa jejak pemakaian pertama; jumlah
-- baris yang berubah dipakai pemanggil untuk memastikan token benar-benar
-- baru saja terpakai.
UPDATE registration_tokens
   SET used_at = now(),
       used_by = sqlc.narg(used_by)::uuid
 WHERE id = sqlc.arg(id)
   AND used_at IS NULL;
