-- Konten tutorial portal cabang (F2-27, PRD §3.8).
--
-- Konten disimpan terstruktur: judul, ringkasan, daftar langkah (jsonb array
-- of text), urutan tampil, dan `ikon` — KUNCI ilustrasi bawaan aplikasi, bukan
-- markup. Teks sudah disanitasi domain sebelum sampai ke sini.

-- name: ListTutorialTopics :many
SELECT id, ikon, judul, ringkas, langkah, urutan, updated_at
  FROM tutorial_topics
 ORDER BY urutan, created_at;

-- name: InsertTutorialTopic :one
INSERT INTO tutorial_topics (ikon, judul, ringkas, langkah, urutan, updated_by)
VALUES (sqlc.arg(ikon), sqlc.arg(judul), sqlc.arg(ringkas), sqlc.arg(langkah)::jsonb,
        sqlc.arg(urutan), sqlc.arg(updated_by))
RETURNING id, ikon, judul, ringkas, langkah, urutan, updated_at;

-- name: UpdateTutorialTopic :execrows
UPDATE tutorial_topics
   SET ikon       = sqlc.arg(ikon),
       judul      = sqlc.arg(judul),
       ringkas    = sqlc.arg(ringkas),
       langkah    = sqlc.arg(langkah)::jsonb,
       urutan     = sqlc.arg(urutan),
       updated_by = sqlc.arg(updated_by),
       updated_at = now()
 WHERE id = sqlc.arg(id);

-- name: DeleteTutorialTopic :execrows
DELETE FROM tutorial_topics
 WHERE id = sqlc.arg(id);
