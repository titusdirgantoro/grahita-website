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

-- ----------------------------------------------------------------------------
-- Format nomor (F1-14). Definisi periode dan bentuk nomor ada di migrasi 0004
-- sebagai fungsi PostgreSQL supaya tidak terduplikasi di kode Go: transaksi
-- submit memanggil fungsi yang sama dengan yang diuji di Database.md §6,
-- termasuk perilaku "digit melebar" saat urutan melewati digit_invoice.
-- ----------------------------------------------------------------------------

-- name: InvoicePeriode :one
-- Periode counter invoice (YYYY-MM) dari tanggal bisnis WIB.
SELECT invoice_periode(sqlc.arg(tanggal)::date)::text AS periode;

-- name: SerialPeriode :one
-- Periode counter serial (YYYYMMDD) dari tanggal bisnis WIB.
SELECT serial_periode(sqlc.arg(tanggal)::date)::text AS periode;

-- name: FormatInvoiceNumber :one
-- Nomor invoice YYYY-MM-XX; bila urutan melebihi `digit_invoice`, jumlah
-- digit bertambah (tidak pernah gagal).
SELECT format_invoice_number(sqlc.arg(tanggal)::date, sqlc.arg(urut)::int, sqlc.arg(digit)::int)::text AS nomor;

-- name: FormatSerialNumber :one
-- Serial number YYYYMMDDXXX (minimal 3 digit, melebar bila perlu).
SELECT format_serial_number(sqlc.arg(tanggal)::date, sqlc.arg(urut)::int)::text AS nomor;
