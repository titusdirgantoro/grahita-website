-- Penomoran invoice & serial (Database.md §6, SystemDesign §5.2).
--
-- next_numbers() mengambil rentang nomor secara atomik dengan
-- UPDATE ... RETURNING pada baris counter, jadi panggilan ini WAJIB berada di
-- dalam transaksi submit: baris counter terkunci sampai COMMIT sehingga submit
-- bersamaan tidak mungkin mendapat nomor yang sama, dan bila transaksi
-- rollback nomor tidak terbuang (berbeda dari SEQUENCE).

-- name: NextNumbers :one
SELECT next_numbers(sqlc.arg(scope)::text, sqlc.arg(periode)::text, sqlc.arg(jumlah)::int)::int AS nomor_pertama;

-- name: GetCounter :one
SELECT last_value
  FROM numbering_counters
 WHERE scope = $1 AND periode = $2;
