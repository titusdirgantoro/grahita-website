-- +goose Up
-- Pesanan lama tidak mempunyai nama instansi. Nilai kosong mempertahankan
-- sejarahnya; API mewajibkan nama instansi pada baris produk baru.
ALTER TABLE order_lines
    ADD COLUMN nama_instansi text NOT NULL DEFAULT '';

ALTER TABLE order_lines
    ADD CONSTRAINT order_lines_nama_instansi_length
    CHECK (char_length(nama_instansi) <= 160);

-- +goose Down
ALTER TABLE order_lines DROP CONSTRAINT order_lines_nama_instansi_length;
ALTER TABLE order_lines DROP COLUMN nama_instansi;
