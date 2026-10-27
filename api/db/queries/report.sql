-- Laporan order line + pekerjaan ekspor XLSX (kartu F2-24, PRD §4.7).
--
-- Satu baris laporan = SATU order line (AP-REP-01), termasuk baris packaging
-- bila ada; pesanan berstatus draft DIKECUALIKAN (constraint `draft_tanpa_nomor`
-- membuat tanggalnya NULL sehingga tidak mungkin ikut rentang tanggal).
--
-- Query daftar (paginasi) dan query ekspor memakai WHERE yang SAMA, sehingga
-- "jumlah baris export == jumlah baris query terfilter" bukan kebetulan
-- melainkan konsekuensi satu definisi filter (Database.md §7.3).
--
-- PI adalah atribut cabang yang tampil dan bisa difilter pada laporan.

-- name: ListReportOrderLines :many
-- Satu batch baris laporan (AP-REP-01/02/03), diurutkan STABIL menurut
-- (tanggal, nomor, line_no) sehingga ekspor bisa berjalan dengan kursor
-- bertahap tanpa melewatkan/mengulang baris (Database.md §7.3, cursor/FETCH).
-- `nomor` unik global (constraint nomor_unik) dan selalu terisi untuk pesanan
-- non-draft, jadi kunci urut ini tidak pernah punya seri kembar.
--
-- Tanggal filter dan kursor dikirim sebagai TEKS `YYYY-MM-DD` (bukan
-- timestamptz) lalu dicor ke `date`: konversi teks→tanggal tidak bergantung
-- zona waktu sesi database, sehingga batas rentang selalu berarti tanggal
-- BISNIS WIB yang tersimpan di kolom `orders.tanggal` (PRD §7.2). Mengirim
-- time.Time justru dikodekan sebagai timestamptz dan `::date`-nya mengikuti
-- TimeZone sesi — persis sumber pergeseran satu hari.
--
-- `setelah_*` adalah kursor: diisi bersama-sama dengan baris terakhir batch
-- sebelumnya, atau kosong untuk mengambil batch pertama.
SELECT o.tanggal,
       COALESCE(o.nomor, '')                                        AS nomor,
       o.versi,
       o.branch_id,
       b.nama                                                       AS cabang,
       COALESCE(b.nama_kepala, '')                                  AS nama_kepala,
       o.is_cnc,
       b.pajak_in,
       l.line_no,
       l.nama_snapshot                                              AS produk,
       l.nama_instansi                                              AS nama_instansi,
       l.jenis::text                                                AS jenis,
       COALESCE(l.serial_number, '')                                AS serial_number,
       l.qty,
       l.harga_satuan,
       COALESCE(l.subtotal, 0)::bigint                              AS subtotal,
       o.status::text                                               AS status,
       o.done_at,
       COALESCE((SELECT vl.metode::text
                   FROM verification_logs vl
                  WHERE vl.order_id = o.id
                  ORDER BY vl.created_at DESC, vl.id DESC
                  LIMIT 1), ''::text)::text                        AS metode_verifikasi
  FROM order_lines l
  JOIN orders   o ON o.id = l.order_id
  JOIN branches b ON b.id = o.branch_id
 WHERE o.status <> 'draft'
   AND (sqlc.arg(dari)::text   = '' OR o.tanggal >= sqlc.arg(dari)::date)
   AND (sqlc.arg(sampai)::text = '' OR o.tanggal <= sqlc.arg(sampai)::date)
   AND (cardinality(sqlc.arg(cabang_ids)::uuid[]) = 0
        OR o.branch_id = ANY(sqlc.arg(cabang_ids)::uuid[]))
   AND (sqlc.arg(status)::text = '' OR o.status::text = sqlc.arg(status)::text)
   AND (sqlc.arg(jenis)::text  = '' OR l.jenis::text  = sqlc.arg(jenis)::text)
   AND (sqlc.narg(is_cnc)::bool     IS NULL OR o.is_cnc     = sqlc.narg(is_cnc)::bool)
   AND (sqlc.narg(pajak_in)::bool   IS NULL OR b.pajak_in   = sqlc.narg(pajak_in)::bool)
   AND (sqlc.arg(setelah_tanggal)::text = ''
        OR (o.tanggal, COALESCE(o.nomor, ''), l.line_no)
           > (sqlc.arg(setelah_tanggal)::date, sqlc.arg(setelah_nomor)::text, sqlc.arg(setelah_line_no)::int))
 ORDER BY o.tanggal, o.nomor, l.line_no
 LIMIT sqlc.arg(batch)::int OFFSET sqlc.arg(offset_n)::int;

-- name: CountReportOrderLines :one
-- Jumlah baris + jumlah total rupiah untuk filter yang sama dengan
-- ListReportOrderLines. Dipakai endpoint daftar (AP-REP-03) dan sebagai
-- pembanding angka ekspor pada pengujian.
SELECT count(*)::bigint            AS jumlah_baris,
       COALESCE(sum(l.subtotal), 0)::bigint AS total_subtotal
  FROM order_lines l
  JOIN orders   o ON o.id = l.order_id
  JOIN branches b ON b.id = o.branch_id
 WHERE o.status <> 'draft'
   AND (sqlc.arg(dari)::text   = '' OR o.tanggal >= sqlc.arg(dari)::date)
   AND (sqlc.arg(sampai)::text = '' OR o.tanggal <= sqlc.arg(sampai)::date)
   AND (cardinality(sqlc.arg(cabang_ids)::uuid[]) = 0
        OR o.branch_id = ANY(sqlc.arg(cabang_ids)::uuid[]))
   AND (sqlc.arg(status)::text = '' OR o.status::text = sqlc.arg(status)::text)
   AND (sqlc.arg(jenis)::text  = '' OR l.jenis::text  = sqlc.arg(jenis)::text)
   AND (sqlc.narg(is_cnc)::bool     IS NULL OR o.is_cnc     = sqlc.narg(is_cnc)::bool)
   AND (sqlc.narg(pajak_in)::bool   IS NULL OR b.pajak_in   = sqlc.narg(pajak_in)::bool);

-- name: InsertReportJob :one
-- Membuat baris pekerjaan ekspor. Diminta DI DALAM transaksi yang juga
-- menitipkan job River-nya.
INSERT INTO report_jobs (jenis, diminta_oleh, diminta_nama, filter, status)
VALUES (sqlc.arg(jenis)::text,
        sqlc.narg(diminta_oleh)::uuid,
        sqlc.arg(diminta_nama)::text,
        sqlc.arg(filter)::jsonb,
        'queued')
RETURNING id, jenis, diminta_nama, filter, status::text AS status,
          nama_file, jumlah_baris, pesan, created_at, started_at, finished_at;

-- name: GetReportJob :one
SELECT id, jenis, diminta_nama, filter, status::text AS status,
       storage_key, nama_file, jumlah_baris, pesan,
       created_at, started_at, finished_at
  FROM report_jobs
 WHERE id = sqlc.arg(id)::uuid;

-- name: MarkReportJobProses :execrows
UPDATE report_jobs
   SET status     = 'proses',
       started_at = COALESCE(started_at, now()),
       pesan      = NULL,
       updated_at = now()
 WHERE id = sqlc.arg(id)::uuid
   AND status <> 'selesai';

-- name: MarkReportJobSelesai :execrows
UPDATE report_jobs
   SET status       = 'selesai',
       storage_key  = sqlc.arg(storage_key)::text,
       nama_file    = sqlc.arg(nama_file)::text,
       jumlah_baris = sqlc.arg(jumlah_baris)::bigint,
       pesan        = NULL,
       finished_at  = now(),
       updated_at   = now()
 WHERE id = sqlc.arg(id)::uuid;

-- name: MarkReportJobGagal :execrows
UPDATE report_jobs
   SET status      = 'gagal',
       pesan       = sqlc.arg(pesan)::text,
       finished_at = now(),
       updated_at  = now()
 WHERE id = sqlc.arg(id)::uuid
   AND status <> 'selesai';
