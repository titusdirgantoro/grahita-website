-- Token atur ulang kata sandi (Database.md §4.2, PRD §3.2).
--
-- Yang disimpan hanya SHA-256 token; nilai aslinya hanya dikirim lewat email.
-- Karena itu kolom uniknya token_hash, bukan nilai token.

-- name: DeleteActivePasswordResetTokens :exec
-- Minta tautan baru membatalkan tautan lama yang belum dipakai supaya hanya
-- satu tautan berlaku pada satu waktu.
DELETE FROM password_reset_tokens WHERE user_id = $1 AND used_at IS NULL;

-- name: CreatePasswordResetToken :exec
INSERT INTO password_reset_tokens (actor_kind, user_id, token_hash, expires_at)
VALUES (sqlc.arg(actor_kind)::actor_kind, sqlc.arg(user_id), sqlc.arg(token_hash), sqlc.arg(expires_at));

-- name: ConsumePasswordResetToken :one
-- Satu token hanya boleh terpakai sekali: UPDATE ... RETURNING bersifat
-- atomik, jadi dua permintaan bersamaan hanya menghasilkan satu baris.
UPDATE password_reset_tokens
   SET used_at = now()
 WHERE token_hash = $1
   AND used_at IS NULL
   AND expires_at > $2
RETURNING id, actor_kind::text AS actor_kind, user_id, expires_at;
