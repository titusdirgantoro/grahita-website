-- Sesi login (Architecture.md §6: cookie sesi host-only, disimpan di DB).

-- name: CreateSession :one
INSERT INTO sessions (actor_kind, user_id, branch_id, token_hash, ip, user_agent, expires_at)
VALUES (
    sqlc.arg(actor_kind)::actor_kind,
    sqlc.arg(user_id),
    sqlc.arg(branch_id),
    sqlc.arg(token_hash),
    NULLIF(sqlc.arg(ip)::text, '')::inet,
    NULLIF(sqlc.arg(user_agent)::text, ''),
    sqlc.arg(expires_at)
)
RETURNING id, expires_at;

-- name: GetSessionByToken :one
SELECT id, actor_kind::text AS actor_kind, user_id, branch_id, expires_at, last_seen_at
  FROM sessions
 WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE id = $1;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1;

-- name: RevokeSessionsForUser :exec
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;
