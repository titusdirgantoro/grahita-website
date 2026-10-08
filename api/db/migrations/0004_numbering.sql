-- +goose Up
-- 0004_numbering.sql — tanggal bisnis WIB dan format nomor (Database.md §1, §6).
--
-- Service menghitung periode dari tanggal submit memakai fungsi ini agar
-- definisi "tanggal bisnis WIB" dan format nomor tidak terduplikasi di kode Go
-- dan di SQL. Pengambilan nomor itu sendiri memakai next_numbers() (0002) di
-- dalam transaksi submit.

-- Tanggal bisnis hari ini menurut Asia/Jakarta (bukan UTC).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION wib_today() RETURNS date AS $$
    SELECT (now() AT TIME ZONE 'Asia/Jakarta')::date;
$$ LANGUAGE sql STABLE;
-- +goose StatementEnd

-- Periode counter invoice: YYYY-MM (reset tiap bulan, global lintas cabang).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION invoice_periode(p_tanggal date) RETURNS text AS $$
    SELECT to_char(p_tanggal, 'YYYY-MM');
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- Periode counter serial: YYYYMMDD (reset tiap hari).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION serial_periode(p_tanggal date) RETURNS text AS $$
    SELECT to_char(p_tanggal, 'YYYYMMDD');
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- Nomor invoice: YYYY-MM-XX. Bila urutan melebihi jumlah digit yang diminta,
-- nomor tidak gagal melainkan bertambah digit (Database.md §6).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION format_invoice_number(p_tanggal date, p_urut int, p_digit int)
RETURNS text AS $$
    SELECT to_char(p_tanggal, 'YYYY-MM') || '-' ||
           lpad(p_urut::text, greatest(p_digit, length(p_urut::text)), '0');
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- Serial number: YYYYMMDDXXX (minimal 3 digit, bertambah digit bila perlu).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION format_serial_number(p_tanggal date, p_urut int)
RETURNS text AS $$
    SELECT to_char(p_tanggal, 'YYYYMMDD') ||
           lpad(p_urut::text, greatest(3, length(p_urut::text)), '0');
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS format_serial_number(date, int);
DROP FUNCTION IF EXISTS format_invoice_number(date, int, int);
DROP FUNCTION IF EXISTS serial_periode(date);
DROP FUNCTION IF EXISTS invoice_periode(date);
DROP FUNCTION IF EXISTS wib_today();
