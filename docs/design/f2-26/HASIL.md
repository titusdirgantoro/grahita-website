# F2-26 — Pembacaan bukti AI/OCR dan antrean review

Kartu: **t_6fce9576 — F2-26 Pembacaan bukti AI/OCR dan antrean review**

Dokumen acuan: `docs/PRD.md` §5 (verifikasi bukti transfer + aturan AI), §4.9
(setelan `ai.*`), §10 (Fase 2); `docs/Architecture.md` §1 dan §11 (OCR/AI di
balik interface `PaymentReader`, mode mock, risiko bukti buram);
`docs/SystemDesign.md` §4.3 (alur verifikasi) dan §9 (pengujian);
`docs/Database.md` §4.5 (`payment_proofs.ocr_*`, `verification_logs`) dan §7.4
(Σ nominal + keyakinan terendah satu pesanan).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend saja (tanpa UI baru — tombol Verifikasi manual dan tampilan "perlu
review" sudah milik F1-18/F1-19/F1-20):

1. **Provider pembaca** di balik interface `payment.PaymentReader`:
   `payment.ClaudeReader` (Claude vision, Messages API) dan
   `payment.MockReader` (deterministik, untuk lokal/pengujian).
2. **Layanan pembacaan + penilaian** `payment.OCRService.Baca(orderID)`:
   membaca setiap bukti yang belum pernah dibaca, menyimpan hasilnya ke kolom
   `payment_proofs.ocr_*`, menjumlahkan nominal multi-bukti, membandingkan
   dengan pesanan + rekening aktif + setelan `ai`, lalu menyerahkan keputusan
   ke `payment.VerificationService.MarkVerified` (metode `ai`).
3. **Pekerjaan latar** `jobs.OCRBuktiArgs` (kind `ocr_bukti`) +
   `worker.OCRBuktiWorker`, dititipkan DI DALAM transaksi submit pesanan
   (`SystemDesign.md` §4.2) dan transaksi unggahan bukti susulan.

Perubahan berkas (repo `/home/hermes/projects/grahita-website`):

| Berkas | Isi |
|---|---|
| `api/internal/payment/reader.go` | interface `PaymentReader`, `ReadInput`/`ReadResult`, sentinel error pembaca |
| `api/internal/payment/reader_mock.go` | `MockReader` deterministik (kunci = SHA-256 isi), bawaan "tidak terbaca" |
| `api/internal/payment/reader_claude.go` | `ClaudeReader` (Messages API, blok gambar/dokumen, penjepitan nilai, tanpa kebocoran kredensial) |
| `api/internal/payment/ocr.go` | `OCRService`, `Nilai` (aturan PRD §5), `SanitizeRaw`, tipe konteks/bukti, `AISettingsSource`/`RekeningSource` |
| `api/internal/payment/verify.go` | aktor sistem (`SystemActor`, konstanta identitas mesin), `verifikatorSah` (sistem hanya metode `ai`), teks timeline sistem |
| `api/internal/payment/payment.go` | `CommitRequest.EnqueueOCR`, opsi `WithAISettings`, `perluBacaBukti` |
| `api/internal/payment/postgres/ocr.go` | repository pembacaan (`OrderForOCR`, `ListProofsForOCR`, `SimpanBacaan`, `SudahDinilaiAI`) |
| `api/internal/payment/postgres/postgres.go` | `CommitProofs` menitipkan job `ocr_bukti` (transaksi yang sama) |
| `api/internal/payment/postgres/verify.go` | timeline `actor_kind='system'` untuk AI, `admin_id` NULL untuk metode `ai` |
| `api/db/queries/payment.sql` + `api/internal/db/sqlc/payment.sql.go` | 4 query F2-26 + hasil `sqlc generate` |
| `api/internal/settings/settings.go` | `AISettings` terurai + `Service.AISettings` |
| `api/internal/platform/jobs/jobs.go` | kind job `ocr_bukti` |
| `api/internal/platform/pgtypes/pgtypes.go` | `Numeric` (kolom `ocr_confidence`) |
| `api/internal/platform/config/config.go` | `OCR_PROVIDER`, `ANTHROPIC_*`, `OCR_TIMEOUT` |
| `api/internal/order/submit.go`, `order/order.go`, `order/postgres/postgres.go` | `SubmitRequest.EnqueueOCR` + `WithAISettings`, job OCR pada transaksi submit |
| `api/internal/worker/worker.go` | `OCRBuktiWorker` + opsi `DenganPembacaBukti` |
| `api/cmd/server/main.go` | wiring pembaca, layanan OCR, penjadwal di mode `api` dan `worker` |
| `.env.example` | variabel OCR/AI |
| `api/internal/payment/ocr_test.go` | uji unit penilaian, sensor, orkestrasi, aktor sistem |
| `api/internal/payment/reader_claude_test.go` | uji pembaca Claude dengan server HTTP tiruan |
| `api/internal/settings/settings_test.go` | uji `AISettings` |
| `api/internal/httpapi/integration_ocr_pg_test.go` | 4 uji integrasi PostgreSQL nyata (alur utama, bukti tambahan, 5 alasan review, AI mati, pemicu job) |
| `api/internal/httpapi/integration_pg_test.go` | fixture: sumber setelan AI dipasang pada pesanan + bukti (meniru `cmd/server`) |

## Keputusan desain

1. **Provider di balik interface, bukan di dalam domain.** `PaymentReader`
   hanya mengubah satu berkas menjadi bacaan terstruktur
   (`nominal/tanggal/bank/rek_akhir/keyakinan` + jawaban mentah). Ia TIDAK
   pernah memutuskan apa pun; keputusan tetap milik `Nilai` +
   `MarkVerified`. Mengganti Claude ke penyedia lain (atau mock) tidak
   menyentuh domain verifikasi (`Architecture.md` §1/§11).
2. **`MarkVerified` tetap satu-satunya pintu transisi Done** (kewajiban yang
   ditulis F1-18 untuk kartu ini). Karena itu satu-satunya perubahan otorisasi
   adalah `verifikatorSah`: aktor ber-`Kind = system` diterima **hanya** untuk
   metode `ai`. Aktor sistem TIDAK bisa memakai metode `manual`, dan manusia
   ber-`admin.verify` tetap satu-satunya yang boleh menimpa keputusan AI.
3. **Identitas mesin konstan** (`00000000-0000-0000-0000-000000000000`,
   nama `verifikasi-ai`) mengikuti pola pekerjaan latar lain (backup,
   pengingat C&C). `verification_logs.admin_id` karena itu ditulis **NULL**
   untuk metode `ai` (Database.md §4.5: NULL = AI) dan timeline mencatat
   `actor_kind='system'` dengan kalimat "…oleh sistem (AI)" — tidak ada log
   yang mengaku dikerjakan admin.
4. **`ai.aktif` diperiksa di dua tempat.** Saat enqueue (submit/unggah) supaya
   tidak ada job yang terbuang ketika AI mati, dan SEKALI LAGI saat pekerjaan
   berjalan supaya mematikan AI menghentikan pemanggilan provider walaupun job
   sudah telanjur terjadwal. Dengan AI mati tidak ada panggilan provider dan
   tidak ada perubahan keadaan sama sekali (`SystemDesign.md` §4.3: "antre
   verifikasi manual").
5. **Penilaian berurutan dan seluruhnya dari setelan server.** Keterbacaan
   (nominal ada + keyakinan ≥ `min_keyakinan`) → rekening tujuan cocok dengan
   rekening AKTIF (`digit_rek` digit terakhir) → tanggal transfer ≥ tanggal
   pesanan − `mundur` hari → Σ nominal vs total pesanan dengan
   `tol_kurang`/`tol_lebih`. Urutan ini disengaja: memeriksa rekening/tanggal
   atas nominal yang tidak terbaca tidak bermakna. Semua ambang dibaca dari
   `settings.Service.AISettings` (bawaan PRD §4.9: 0.90 / 0 / 0 / 4 / 1).
6. **Multi-bukti dijumlahkan sebelum dibandingkan** (PRD §5, Database.md
   §7.4). Setiap bukti tetap diperiksa sendiri untuk keterbacaan, rekening, dan
   tanggal; hanya NOMINAL yang dijumlahkan.
7. **`digit_rek` fail closed.** Hasil baca yang digitnya lebih pendek dari
   `digit_rek` tidak pernah dianggap cocok (mencocokkan lebih sedikit digit
   akan meloloskan rekening yang salah) → perlu review `rekening_beda`.
8. **Nominal yang tidak terbaca disimpan NULL, bukan 0.**
   `verification_logs.total_terbaca` dikirim `nil` ketika keputusannya
   "tidak terbaca" supaya kolom generated `selisih` tidak melaporkan
   "kurang bayar −total" untuk bukti yang sekadar buram.
9. **Idempotensi berlapis (River at-least-once).**
   (a) bukti yang sudah dibaca (`ocr_at` terisi) tidak dipanggil ulang ke
   provider — hasil bacanya dipakai kembali dari database; (b) kegagalan
   provider tidak menyimpan apa pun, jadi pengulangan mulai dari keadaan
   bersih; (c) keputusan untuk satu himpunan bukti tidak ditulis dua kali:
   query `OrderSudahDinilaiAI` menjawab "sudah ada log verifikasi metode `ai`
   yang dibuat SESUDAH bukti terakhir dibaca". Karena bukti BARU mendapat
   `ocr_at` yang lebih muda, ia selalu memicu penilaian ulang — sedangkan
   pengulangan job tanpa bukti baru dilewati; (d) pesanan yang sudah Done
   (mis. admin menimpa lebih dulu) dijawab `ErrAlreadyDone` dan diperlakukan
   sebagai "tidak ada yang perlu dilakukan", bukan kegagalan.
10. **Job dititipkan DI DALAM transaksi, bukan sesudahnya.** Submit pesanan
    menulis baris `river_job` pada transaksi submit yang sama
    (`SystemDesign.md` §4.2) dan unggahan bukti pada transaksi `CommitProofs`
    yang sama. Tanpa penjadwal, kedua jalur itu DITOLAK (`ErrQueueUnavailable`)
    alih-alih menyimpan keadaan yang kehilangan pembacaannya.
11. **Audit raw tanpa kebocoran.** Jawaban mentah provider lewat `SanitizeRaw`
    (rekursif; kunci yang tampak rahasia → `[disensor]`) sebelum masuk
    `payment_proofs.ocr_raw` maupun entri audit `baca_bukti`. Pembaca Claude
    tidak pernah menyalin header permintaan ke pesan galat, dan galat provider
    hanya meneruskan `error.message` (bukan badan jawaban).
12. **Mode mock aman secara bawaan.** `OCR_PROVIDER=mock` (lokal/pengujian)
    memakai peta sidik jari → bacaan; tanpa peta, bawaannya "tidak terbaca"
    sehingga mode lokal TIDAK pernah menyetujui pembayaran atas dasar angka
    karangan. Untuk produksi, `OCR_PROVIDER=claude` dengan `ANTHROPIC_API_KEY`;
    kunci kosong berarti pekerjaan GAGAL dan diulang (bukan menyetujui), dan
    admin tetap bisa memverifikasi manual.

## Bukti verifikasi (dijalankan di repo target)

Perkakas: `go1.27.0`, PostgreSQL 16.15 di `127.0.0.1:5455` (database uji
`grahita_f226` + `scripts/pg-dev-shim.sql` + ekstensi di `public`),
`sqlc-v1.31.1` (pin CI).

```
$ cd api && sqlc-v1.31.1 generate        # tanpa diff setelah dijalankan ulang
$ gofmt -l .                             # kosong
$ go vet ./...                           # bersih
$ go build ./...                         # bersih
$ go test -run 'TestPGOCR|TestPGSubmitDanUnggahMenitipJobOCR' -count=1 ./internal/httpapi/
ok  github.com/grahita/portal/api/internal/httpapi  15.147s
$ go test -race -timeout 30m -p 1 -count=1 ./...   # SUITE_EXIT=0 (lihat bawah)
```

Catatan batas waktu: pada host ini paket `internal/httpapi` butuh **606.6 s**
di bawah `-race` (didominasi `TestPGReportEkspor50kBarisTidakOOM` 178.7 s),
sehingga batas bawaan `go test` 10 menit terlewati. Karena itu suite dijalankan
dengan `-timeout 30m`; tambahan uji kartu ini sendiri hanya ~8 s
(2.04 + 2.10 + 2.14 + 2.03 s).

Hasil suite penuh (log: `f226-suite30m.log`):

```
ok  github.com/grahita/portal/api/internal/httpapi   662.199s
ok  github.com/grahita/portal/api/internal/{order,payment,settings,worker,...}
... 23 paket "ok", 0 FAIL
SUITE_EXIT=0
```

Tambahan gerbang: `sqlc-v1.31.1 generate` idempoten (md5 `payment.sql.go`
identik sebelum/sesudah), `./scripts/check-secrets.sh` EXIT 0,
`go build ./...` bersih, `gofmt -l .` kosong, `go vet ./...` bersih.
`openapi/openapi.yaml` TIDAK disentuh (kartu ini tidak menambah permukaan
HTTP: kolom `ocr_*` sengaja belum diekspos lewat API).

Uji yang menyentuh kartu ini:

| Uji | Membuktikan |
|---|---|
| `TestNilaiMenutupSeluruhAturanPRD` (11 kasus) | cocok, multi-bukti dijumlah, bukti kabur, nominal/rekening/tanggal tidak terbaca, rekening beda, tanggal terlalu tua, batas mundur tepat, kurang/lebih bayar, tanpa bukti |
| `TestNilaiMenghormatiToleransiSetelan`, `TestNilaiMenghormatiDigitRekeningSetelan`, `TestRekeningCocokDanDigitSaja` | seluruh ambang dibaca dari setelan; digit pendek fail closed; pemisah diabaikan |
| `TestSanitizeRawMembuangNilaiRahasia` | `api_key`/`Authorization`/`password`/`token` (termasuk bersarang) disensor; nilai non-rahasia utuh; JSON rusak tidak disimpan |
| `TestMockReaderDeterministikDanBawaanTidakTerbaca` | determinisme per isi; bawaan "tidak terbaca"; galat provider diteruskan |
| `TestOCSKipTidakMemanggilProviderDanTidakMenulis` (unit) + `TestPGOCRMatiTidakMemanggilProvider` (PG) | `ai.aktif=false` ⇒ 0 panggilan provider, 0 log/audit, `ocr_at` tetap NULL, status pesanan tidak berubah |
| `TestOCRCocokMemverifikasiDenganMetodeAI`, `TestOCRPerluReviewKarenaBuktiKaburTidakMenyetujui` | keputusan masuk lewat `MarkVerified` metode `ai`; total terbaca NULL saat tidak terbaca; audit `baca_bukti` oleh aktor sistem |
| `TestOCRIdempotenSaatDiulang`, `TestOCRBuktiBaruMemicuPenilaianUlang`, `TestOCRGagalProviderTidakMenulisApaPun` | pengulangan tidak menggandakan panggilan/keputusan; bukti baru dinilai ulang; kegagalan provider tidak menyimpan apa pun |
| `TestAktorSistemHanyaUntukMetodeAI`, `TestAktorSistemKonstannyaBukanAkunManusia`, `TestTeksTimelineSistemTidakMengakuAdmin` | aktor sistem hanya untuk `ai`; tanpa kapabilitas manusia; timeline tidak mengaku admin |
| `TestClaude*` (9 uji, server HTTP tiruan) | bentuk permintaan (`/v1/messages`, header versi + kunci, blok gambar/dokumen base64), penguraian JSON berpagar kode, galat provider/jaringan TIDAK membocorkan kunci, penjepitan nominal/tanggal/keyakinan |
| `TestOCRBuktiWorker*` (worker) | pekerja meneruskan pesanan ke layanan, galat diteruskan supaya River mengulang, tanpa layanan gagal terang-terangan |
| `TestAISettingsTerurai` (settings) | setelan tersimpan dipakai apa adanya, bawaan PRD §4.9, nilai rusak ditolak |
| `TestPGOCRCocokMenyetujuiLewatJalurVerifikasiYangSama` (PG) | alur utama: `ocr_*` tersimpan (termasuk `ocr_raw` bersih), `verification_logs` 1 baris `ai` tanpa `admin_id`, timeline `system`, audit `baca_bukti` + `verify`, job dokumen dititipkan; pengulangan job TIDAK menambah log/audit/job |
| `TestPGOCRBuktiBaruMenilaiUlang` (PG) | bukti lama tidak dibaca ulang, bukti baru dibaca, 2 log AI (kurang bayar lalu cocok), pesanan jadi Done |
| `TestPGOCRAlasanReviewSesuaiAturanPRD` (PG, 5 sub-uji) | kelima alasan review tepat dan pesanan TETAP submitted tanpa `done_at` dan tanpa dokumen |
| `TestPGSubmitDanUnggahMenitipJobOCR` (PG) | submit & unggahan bukti susulan (HTTP nyata, CSRF) menitipkan job `ocr_bukti` saat AI menyala; AI mati ⇒ 0 job sementara pesanan tetap tersimpan |

## Batas bukti & risiko tersisa (non-pemblokir)

- **Claude API nyata tidak dipanggil**: `ClaudeReader` diuji dengan server HTTP
  tiruan (bentuk permintaan + penguraian jawaban). Panggilan ke
  `api.anthropic.com` dengan kunci sungguhan adalah tugas smoke T4/T5
  (butuh kredensial).
- **PostgreSQL 16.15 + shim, bukan PG18/CI**; `golangci-lint` tidak tersedia di
  sesi ini. Sama seperti kartu F1-xx/F2-xx lain.
- **Bukti PDF** dikirim ke provider sebagai blok `document`; kualitas pembacaan
  PDF nyata belum diuji dengan provider sungguhan.
- **Model vision bawaan** (`claude-sonnet-4-5`) dapat ditimpa lewat
  `ANTHROPIC_MODEL`; nama model tidak diuji terhadap API sungguhan.
- **Objektif "screenshot terpotong"** tetap bisa salah baca: mitigasinya adalah
  ambang keyakinan + selalu jatuh ke review manual (`Architecture.md` §11), dan
  itu sudah ditegakkan (bukti kabur → `tidak_terbaca`).
- **Unggahan bukti susulan pada pesanan Done oleh ADMIN** tidak menitipkan job
  OCR (hanya `submitted`); koreksi setelah Done tetap lewat verifikasi manual.

## Kewajiban untuk kartu turunan

- **T7 (t_bd58477b)**: memakai `ai.aktif=true` + `OCR_PROVIDER=mock` untuk uji
  end-to-end pembacaan; override manual tetap `POST /admin/orders/{id}/verify`.
- **F1-19/F1-20 (detail & chip admin)**: sumber "perlu review" tetap
  `orders.verif_status = 'perlu_review'`; hasil baca AI sudah tersedia di
  `payment_proofs.ocr_*` bila suatu saat perlu ditampilkan (kolom sengaja TIDAK
  diekspos lewat API pada kartu ini).
- **F1-22/T4/T5 (rilis)**: `ANTHROPIC_API_KEY` + `OCR_PROVIDER` harus diisi di
  mode `worker`; tanpa kunci, job `ocr_bukti` gagal dan diulang (terlihat di
  metrik job) sementara admin tetap bisa memverifikasi manual.
