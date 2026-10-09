-- Pembacaan audit log (kartu F2-28, SystemDesign.md §5.4 / §6, PRD §4.10 fase 2).
--
-- Tabel audit_logs bersifat append-only: satu baris = satu mutasi admin yang
-- berhasil. Query di bawah hanya MEMBACA; tidak ada jalur tulis di sini
-- (penulisan dilakukan produsennya masing-masing di dalam transaksinya).
--
-- Batas rentang waktu memakai tanggal BISNIS WIB. Tanggal dikirim sebagai TEKS
-- `YYYY-MM-DD` lalu dicor ke timestamptz memakai `AT TIME ZONE 'Asia/Jakarta'`
-- sehingga batasnya tidak pernah bergeser mengikuti zona waktu sesi database
-- (pola yang sama dengan report.sql).
--
-- Urutan `at DESC, id DESC` STABIL: baris dengan `at` identik (mis. beberapa
-- entri dalam satu milidetik) tidak pernah bertukar posisi antar halaman,
-- sehingga tidak ada baris yang terlewat atau tampil dua kali saat menelusuri
-- halaman berikutnya.

-- name: ListAuditLogs :many
SELECT id,
       at,
       actor_kind::text              AS actor_kind,
       COALESCE(actor_id::text, '')::text  AS actor_id,
       COALESCE(actor_nama, '')::text      AS actor_nama,
       COALESCE(host(ip), '')::text        AS ip,
       entity,
       COALESCE(entity_id::text, '')::text AS entity_id,
       action,
       before,
       after
  FROM audit_logs
 WHERE (cardinality(sqlc.arg(entitas)::text[]) = 0
        OR entity = ANY(sqlc.arg(entitas)::text[]))
   AND (sqlc.arg(aksi)::text = '' OR action = sqlc.arg(aksi)::text)
   AND (sqlc.arg(actor_id)::text = ''
        OR actor_id = sqlc.arg(actor_id)::uuid)
   AND (sqlc.arg(actor_nama)::text = ''
        OR actor_nama ILIKE '%' || sqlc.arg(actor_nama)::text || '%')
   AND (sqlc.arg(dari)::text = ''
        OR at >= (sqlc.arg(dari)::text::date::timestamp AT TIME ZONE 'Asia/Jakarta'))
   AND (sqlc.arg(sampai)::text = ''
        OR at < ((sqlc.arg(sampai)::text::date + 1)::timestamp AT TIME ZONE 'Asia/Jakarta'))
 ORDER BY at DESC, id DESC
 LIMIT sqlc.arg(limit_rows) OFFSET sqlc.arg(offset_rows);

-- name: CountAuditLogs :one
-- COUNT memakai WHERE yang SAMA PERSIS dengan ListAuditLogs supaya "total" pada
-- respons tidak pernah menyimpang dari jumlah baris yang bisa ditelusuri.
SELECT count(*)::bigint AS total
  FROM audit_logs
 WHERE (cardinality(sqlc.arg(entitas)::text[]) = 0
        OR entity = ANY(sqlc.arg(entitas)::text[]))
   AND (sqlc.arg(aksi)::text = '' OR action = sqlc.arg(aksi)::text)
   AND (sqlc.arg(actor_id)::text = ''
        OR actor_id = sqlc.arg(actor_id)::uuid)
   AND (sqlc.arg(actor_nama)::text = ''
        OR actor_nama ILIKE '%' || sqlc.arg(actor_nama)::text || '%')
   AND (sqlc.arg(dari)::text = ''
        OR at >= (sqlc.arg(dari)::text::date::timestamp AT TIME ZONE 'Asia/Jakarta'))
   AND (sqlc.arg(sampai)::text = ''
        OR at < ((sqlc.arg(sampai)::text::date + 1)::timestamp AT TIME ZONE 'Asia/Jakarta'));
