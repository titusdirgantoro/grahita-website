# Kandidat production B2

Kartu: `t_14ccfa4d`.

Dokumen ini mencatat komposisi kandidat rilis B2 yang dibangun ulang dari basis
production terakhir yang terbukti, tanpa melakukan deployment.

## Basis

Basis adalah `62ae8805c807a53c20308a83275a94c7e9f80dd7`. Kartu
`t_2eba0a6f` mencatat deployment sukses melalui skrip resmi, status service sehat,
dan pemeriksaan QA yang mencocokkan hash aset kedua portal production dengan build
lokal commit tersebut.

## Delta yang diintegrasikan

Delta berikut diambil ulang secara berurutan dari commit yang telah disetujui QA:

1. `4bfd226200a95733908ae27a026f637e328d7ba4` — implementasi rekening cabang dan verifikasi OCR B2;
2. `6c8992c092ef12770bdc57d95c4b41f86c58804e` — status rekening efektif cabang;
3. `42a1d606e0acb633144f29f584a664fc055d8700` — penutupan celah UI rekening dan OCR;
4. `12acf6c9cb2b05e8fde5b388b6f625938c7735d9` — invalidasi rekening customer yang fail-closed;
5. `f06dcaa39c66dc0b37cf6caeefc73b9aad8175b9` — fixture integrasi dokumen Done yang memenuhi invariant pointer verifikasi.

Implementasi B2 sampai `12acf6c` disetujui QA pada `t_dea8f24c`. Perbaikan fixture
PDF `f06dcaa` disetujui QA pada `t_3216a632`. Cherry-pick menghasilkan hash baru,
sehingga QA wajib meninjau seluruh ancestry kandidat ini sebelum deployment.

## Delta yang sengaja tidak diintegrasikan

Kandidat tidak memuat commit setelah basis yang berkaitan dengan:

- smoke atau dokumentasi operasi production;
- preview/apply Impact production;
- desain atau implementasi deposit C1/D3;
- route sesi browser lokal;
- PostgreSQL/storage S3 sintetis lokal.

VA tetap OFF. Assignment rekening hanya dapat dimutasi Superadmin. Manager dan
Superadmin dapat melakukan verifikasi manual hanya terhadap rekening efektif. OCR
stale atau rekening tidak efektif tetap fail-closed.

## Batas operasional

Kandidat ini hanya artefak review dan tidak dideploy oleh kartu ini. Deployment
harus memakai SHA persis yang disetujui QA melalui `/srv/grahita-ops/deploy.sh`,
diikuti verifikasi status resmi dan E2E pada dua portal production.
