package httpapi

// Uji integrasi pembacaan bukti AI/OCR dan antrean review (kartu F2-26) pada
// PostgreSQL nyata.
//
// Yang dibuktikan berkas ini hanya bisa dibuktikan di database dan pada rantai
// antar-domain:
//
//   - hasil baca tersimpan pada kolom `payment_proofs.ocr_*` beserta entri
//     audit raw yang SUDAH dibersihkan dari nilai rahasia;
//   - keputusan "cocok" memakai `payment.MarkVerified` yang sama dengan
//     verifikasi manual (metode `ai`, `admin_id` NULL, timeline oleh sistem,
//     dan pekerjaan dokumen dititipkan) — AI tidak menulis status sendiri;
//   - multi-bukti dijumlahkan sebelum dibandingkan dengan total pesanan;
//   - bukti kabur / rekening salah / tanggal terlalu tua / kurang bayar /
//     lebih bayar jatuh ke `perlu_review` dengan alasan yang tepat, dan pesanan
//     TETAP submitted (AI tidak menolak final);
//   - `ai.aktif=false` tidak memanggil provider sama sekali;
//   - pekerjaan yang diulang (River at-least-once) TIDAK menggandakan log
//     verifikasi, timeline, audit, maupun dokumen — dan bukti BARU tetap
//     memicu penilaian ulang;
//   - submit pesanan (dan unggahan bukti susulan) MENITIPKAN job `ocr_bukti`
//     pada transaksi yang sama ketika AI menyala.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/payment"
	paymentpg "github.com/grahita/portal/api/internal/payment/postgres"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/settings"
	settingspg "github.com/grahita/portal/api/internal/settings/postgres"
)

// setAIAktif menulis setelan `ai` (PRD §4.9) dengan ambang yang dipakai uji.
func setAIAktif(t *testing.T, f *pgFixture, aktif bool) {
	t.Helper()
	nilai := `{"aktif":false,"min_keyakinan":0.9,"tol_kurang":0,"tol_lebih":0,"digit_rek":4,"mundur":1}`
	if aktif {
		nilai = `{"aktif":true,"min_keyakinan":0.9,"tol_kurang":0,"tol_lebih":0,"digit_rek":4,"mundur":1}`
	}
	setSetting(t, f, "ai", nilai)
}

// periodeInvoiceIni mengembalikan periode penomoran bulan berjalan (WIB).
func periodeInvoiceIni(t *testing.T, f *pgFixture) string {
	t.Helper()
	_, periode, _ := wibHariIni(t, f)
	return periode
}

// nomorUji menyusun nomor pesanan yang sah untuk penyemaian langsung: akhiran
// minimal tiga digit diperlukan agar serial baris pesanan memenuhi CHECK
// serial_format (YYYYMMDD + 3+ digit).
func nomorUji(t *testing.T, f *pgFixture, urut int) string {
	t.Helper()
	return fmt.Sprintf("%s-%03d", periodeInvoiceIni(t, f), urut)
}

// unggahBuktiHTTP mengirim satu berkas bukti ke endpoint multipart (submit
// maupun /proofs) dan mengembalikan statusnya. Badan tidak diurai karena
// endpoint /proofs menjawab larik, bukan objek.
func unggahBuktiHTTP(t *testing.T, url string, cookies []*http.Cookie, csrf string, berkas map[string][]byte) int {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for nama, isi := range berkas {
		fw, err := w.CreateFormFile(payment.FieldBerkas, nama)
		if err != nil {
			t.Fatalf("buat bagian multipart: %v", err)
		}
		if _, err := fw.Write(isi); err != nil {
			t.Fatalf("tulis isi berkas: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tutup multipart: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("buat request unggah: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if csrf != "" {
		req.Header.Set(httpx.CSRFHeader, csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim unggah: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// ocrPipeline menyusun layanan pembacaan bukti di atas repository pembayaran
// nyata (peran worker di produksi).
func ocrPipeline(t *testing.T, f *pgFixture, reader payment.PaymentReader) *payment.OCRService {
	t.Helper()
	return payment.NewOCRService(f.paymentsRepo, reader, f.objek,
		f.bank, settings.NewService(settingspg.New(f.pool)),
		payment.NewVerificationService(f.paymentsRepo))
}

// seedBuktiOCR menyemai satu bukti transfer: isinya masuk object storage
// in-memory dan metadatanya ke `payment_proofs`.
func seedBuktiOCR(t *testing.T, f *pgFixture, orderID, nama string, isi []byte) string {
	t.Helper()
	ctx := context.Background()
	kunci := "proofs/" + orderID + "/" + nama
	if err := f.objek.Put(ctx, kunci, bytes.NewReader(isi), int64(len(isi)), payment.MimeJPEG); err != nil {
		t.Fatalf("simpan objek bukti: %v", err)
	}
	sum := sha256.Sum256(isi)
	var id string
	err := f.pool.QueryRow(ctx,
		`INSERT INTO payment_proofs (purpose,branch_id,order_id,storage_key,nama_file,mime_type,ukuran_byte,sha256,uploaded_by_kind)
		 VALUES ('order_payment',(SELECT branch_id FROM orders WHERE id=$1),$1,$2,$3,'image/jpeg',$4,$5,'branch') RETURNING id`,
		orderID, kunci, nama, len(isi), sum[:]).Scan(&id)
	if err != nil {
		t.Fatalf("seed bukti %s: %v", nama, err)
	}
	return id
}

// tanggalPesananDB membaca tanggal bisnis pesanan (dipakai sebagai tanggal
// transfer pada hasil baca yang cocok).
func tanggalPesananDB(t *testing.T, f *pgFixture, orderID string) time.Time {
	t.Helper()
	var tgl time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT tanggal FROM orders WHERE id = $1`, orderID).Scan(&tgl); err != nil {
		t.Fatalf("baca tanggal pesanan: %v", err)
	}
	return tgl
}

// hitungKueri menghitung baris dengan satu query ber-argumen.
func hitungKuery(t *testing.T, f *pgFixture, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

// jobOCR menghitung job pembacaan bukti milik satu pesanan.
func jobOCR(t *testing.T, f *pgFixture, orderID string) int {
	t.Helper()
	return hitungKuery(t, f, `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'order_id' = $2`,
		jobs.KindOCRBukti, orderID)
}

// hasilBacaCocok menyusun bacaan yang cocok: nominal penuh, rekening tujuan
// aktif, dan tanggal transfer = tanggal pesanan.
func hasilBacaCocok(total int64, tanggalPesanan time.Time) payment.ReadResult {
	t := tanggalPesanan.UTC()
	raw, _ := json.Marshal(map[string]any{
		"nominal":   total,
		"tanggal":   t.Format("2006-01-02"),
		"bank":      "BCA",
		"rek_akhir": "9999000011",
		"keyakinan": 0.97,
		// Kunci palsu untuk membuktikan penyensoran sebelum disimpan.
		"debug_api_key": "sk-f2-26-rahasia",
	})
	return payment.ReadResult{
		Nominal: &total, Tanggal: &t, Bank: "BCA", RekAkhir: "9999000011",
		Confidence: 0.97, Raw: raw,
	}
}

// TestPGOCRCocokMenyetujuiLewatJalurVerifikasiYangSama menjalankan alur utama:
// satu bukti cocok -> Done lewat MarkVerified (metode ai), hasil baca tersimpan,
// audit raw bersih, dan pekerjaan dokumen dititipkan.
func TestPGOCRCocokMenyetujuiLewatJalurVerifikasiYangSama(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	id, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 1))
	setAIAktif(t, f, true)

	isi := berkasJPEGUji("f2-26-cocok")
	seedBuktiOCR(t, f, id, "bukti.jpg", isi)
	reader := payment.NewMockReader(map[string]payment.ReadResult{
		payment.MockKey(isi): hasilBacaCocok(total, tanggalPesananDB(t, f, id)),
	})
	svc := ocrPipeline(t, f, reader)

	hasil, err := svc.Baca(ctx, id)
	if err != nil {
		t.Fatalf("Baca gagal: %v", err)
	}
	if hasil.Dilewati || hasil.Keputusan == nil || hasil.Keputusan.Hasil != payment.VerifCocok {
		t.Fatalf("harus menyetujui, dapat %+v", hasil)
	}
	if reader.JumlahPanggilan() != 1 {
		t.Fatalf("provider harus dipanggil sekali, dapat %d", reader.JumlahPanggilan())
	}

	status, verif, alasan, doneAt := statusPesanan(t, f, id)
	if status != "done" || verif != "cocok" || alasan != "" {
		t.Errorf("pesanan harus done/cocok tanpa alasan, dapat %s/%s/%q", status, verif, alasan)
	}
	if doneAt == nil {
		t.Error("done_at harus terisi untuk pesanan done")
	}

	// Log verifikasi: SATU baris, metode ai, tanpa pemeriksa manusia.
	var metode, hasilLog, catatan string
	var totalTerbaca *int64
	var adminID *string
	if err := f.pool.QueryRow(ctx,
		`SELECT metode::text, hasil::text, COALESCE(total_terbaca, -1), admin_id::text, COALESCE(catatan,'')
		   FROM verification_logs WHERE order_id = $1`, id).Scan(&metode, &hasilLog, &totalTerbaca, &adminID, &catatan); err != nil {
		t.Fatalf("baca log verifikasi: %v", err)
	}
	if metode != "ai" || hasilLog != "cocok" {
		t.Errorf("log harus ai/cocok, dapat %s/%s", metode, hasilLog)
	}
	if totalTerbaca == nil || *totalTerbaca != total {
		t.Errorf("total terbaca harus %d, dapat %v", total, totalTerbaca)
	}
	if adminID != nil {
		t.Errorf("verifikasi AI tidak boleh punya pemeriksa manusia (admin_id NULL), dapat %v", *adminID)
	}
	if !strings.Contains(catatan, "AI/OCR") {
		t.Errorf("catatan log harus menjelaskan pembacaan AI: %q", catatan)
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id = $1`, id); n != 1 {
		t.Errorf("harus tepat 1 log verifikasi, dapat %d", n)
	}

	// Hasil baca tersimpan lengkap; jawaban mentah disensor.
	var nominal *int64
	var bank, rek, raw string
	var keyakinan float64
	var ocrAt *string
	if err := f.pool.QueryRow(ctx,
		`SELECT ocr_nominal, COALESCE(ocr_bank,''), COALESCE(ocr_rek_akhir,''), COALESCE(ocr_confidence,0),
		        COALESCE(ocr_raw::text,''), ocr_at::text
		   FROM payment_proofs WHERE order_id = $1`, id).Scan(&nominal, &bank, &rek, &keyakinan, &raw, &ocrAt); err != nil {
		t.Fatalf("baca hasil OCR: %v", err)
	}
	if nominal == nil || *nominal != total {
		t.Errorf("ocr_nominal harus %d, dapat %v", total, nominal)
	}
	if bank != "BCA" || rek != "9999000011" || keyakinan < 0.9 {
		t.Errorf("hasil baca tidak lengkap: bank=%q rek=%q keyakinan=%v", bank, rek, keyakinan)
	}
	if ocrAt == nil {
		t.Error("ocr_at harus terisi supaya bukti tidak dibaca ulang")
	}
	if !strings.Contains(raw, `"nominal"`) {
		t.Errorf("ocr_raw harus menyimpan jawaban mentah: %s", raw)
	}
	if strings.Contains(raw, "sk-f2-26-rahasia") {
		t.Errorf("ocr_raw tidak boleh memuat nilai rahasia: %s", raw)
	}

	// Timeline: oleh SISTEM, bukan mengaku admin.
	var actorKind, teks string
	if err := f.pool.QueryRow(ctx,
		`SELECT actor_kind::text, teks FROM order_events WHERE order_id = $1 AND icon = 'selesai'`, id).
		Scan(&actorKind, &teks); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if actorKind != "system" || !strings.Contains(teks, "sistem (AI)") {
		t.Errorf("timeline harus mencatat pelaku sistem, dapat %s/%q", actorKind, teks)
	}

	// Audit raw tanpa kebocoran: entri pembacaan + entri verifikasi.
	if n := hitungKuery(t, f,
		`SELECT count(*) FROM audit_logs WHERE entity = 'order' AND entity_id = $1 AND action = 'baca_bukti'`, id); n != 1 {
		t.Errorf("harus 1 entri audit pembacaan, dapat %d", n)
	}
	var auditActor, auditAfter string
	if err := f.pool.QueryRow(ctx,
		`SELECT actor_kind::text, COALESCE(after::text,'') FROM audit_logs
		  WHERE entity = 'order' AND entity_id = $1 AND action = 'baca_bukti'`, id).Scan(&auditActor, &auditAfter); err != nil {
		t.Fatalf("baca audit pembacaan: %v", err)
	}
	if auditActor != "system" {
		t.Errorf("audit pembacaan harus oleh sistem, dapat %q", auditActor)
	}
	if !strings.Contains(auditAfter, `"raw"`) {
		t.Errorf("audit pembacaan harus memuat jawaban mentah (raw): %s", auditAfter)
	}
	if strings.Contains(auditAfter, "sk-f2-26-rahasia") {
		t.Errorf("audit tidak boleh membocorkan nilai rahasia: %s", auditAfter)
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM audit_logs WHERE entity = 'order' AND entity_id = $1 AND action = 'verify'`, id); n != 1 {
		t.Errorf("harus 1 entri audit verifikasi, dapat %d", n)
	}

	// Pekerjaan dokumen dititipkan oleh jalur verifikasi yang sama.
	if n := pekerjaanDokumen(t, f, id); n != 1 {
		t.Errorf("harus 1 job render dokumen, dapat %d", n)
	}
	if n := jobOCR(t, f, id); n != 0 {
		t.Errorf("pembacaan tidak boleh menitipkan job OCR lagi, dapat %d", n)
	}
	repo := paymentpg.New(f.pool)
	if current, err := repo.SudahDinilaiAI(ctx, id); err != nil || !current {
		t.Fatalf("attempt AI harus current sebelum policy berubah: current=%v err=%v", current, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET bank_allowlist_version=bank_allowlist_version+1 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if current, err := repo.SudahDinilaiAI(ctx, id); err != nil || current {
		t.Fatalf("attempt AI harus stale setelah versi allowlist berubah: current=%v err=%v", current, err)
	}

	// PENGULANGAN job River: tidak ada efek samping kedua.
	ulang, err := svc.Baca(ctx, id)
	if err != nil {
		t.Fatalf("Baca ulang gagal: %v", err)
	}
	if !ulang.Dilewati {
		t.Error("pengulangan harus dilewati")
	}
	if reader.JumlahPanggilan() != 1 {
		t.Errorf("pengulangan tidak boleh memanggil provider lagi, dapat %d", reader.JumlahPanggilan())
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id = $1`, id); n != 1 {
		t.Errorf("pengulangan tidak boleh menambah log verifikasi, dapat %d", n)
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM audit_logs WHERE entity_id = $1`, id); n != 2 {
		t.Errorf("pengulangan tidak boleh menambah audit, dapat %d", n)
	}
	if n := pekerjaanDokumen(t, f, id); n != 1 {
		t.Errorf("pengulangan tidak boleh menambah job dokumen, dapat %d", n)
	}
}

// TestPGOCRBuktiBaruMenilaiUlang himpunan bukti yang bertambah: bukti lama
// tidak dibaca ulang, bukti baru dibaca, dan keputusan diambil ulang atas
// penjumlahan keduanya (menutup celah "sudah dinilai" yang membeku).
func TestPGOCRBuktiBaruMenilaiUlang(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	id, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 1))
	setAIAktif(t, f, true)
	tgl := tanggalPesananDB(t, f, id)

	// Bukti pertama hanya separuh: perlu review (kurang bayar).
	isi1 := berkasJPEGUji("f2-26-bukti-1")
	seedBuktiOCR(t, f, id, "a.jpg", isi1)
	separuh := total / 2
	reader := payment.NewMockReader(map[string]payment.ReadResult{
		payment.MockKey(isi1): {
			Nominal: &separuh, Tanggal: &tgl, Bank: "BCA", RekAkhir: "9999000011", Confidence: 0.96,
		},
	})
	svc := ocrPipeline(t, f, reader)

	if _, err := svc.Baca(ctx, id); err != nil {
		t.Fatalf("Baca pertama gagal: %v", err)
	}
	status, verif, alasan, doneAt := statusPesanan(t, f, id)
	if status != "submitted" || verif != "perlu_review" || alasan != payment.AlasanKurangBayar {
		t.Fatalf("harus perlu_review/kurang_bayar, dapat %s/%s/%q", status, verif, alasan)
	}
	if doneAt != nil {
		t.Error("perlu review tidak boleh mengisi done_at")
	}
	if n := pekerjaanDokumen(t, f, id); n != 0 {
		t.Errorf("perlu review tidak boleh menitipkan job dokumen, dapat %d", n)
	}

	// Bukti kedua melengkapi nominal -> pembacaan ulang menyetujui.
	isi2 := berkasJPEGUji("f2-26-bukti-2")
	seedBuktiOCR(t, f, id, "b.jpg", isi2)
	sisa := total - separuh
	reader.Hasil[payment.MockKey(isi2)] = payment.ReadResult{
		Nominal: &sisa, Tanggal: &tgl, Bank: "BCA", RekAkhir: "9999000011", Confidence: 0.96,
	}

	hasil, err := svc.Baca(ctx, id)
	if err != nil {
		t.Fatalf("Baca kedua gagal: %v", err)
	}
	if hasil.Dibaca != 1 {
		t.Errorf("hanya bukti baru yang boleh dipanggil provider, dapat %d", hasil.Dibaca)
	}
	if hasil.Keputusan == nil || hasil.Keputusan.Hasil != payment.VerifCocok {
		t.Fatalf("penjumlahan bukti harus menyetujui, dapat %+v", hasil.Keputusan)
	}
	status, verif, _, _ = statusPesanan(t, f, id)
	if status != "done" || verif != "cocok" {
		t.Errorf("pesanan harus done setelah bukti lengkap, dapat %s/%s", status, verif)
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id = $1`, id); n != 2 {
		t.Errorf("harus 2 log (perlu_review lalu cocok), dapat %d", n)
	}
	if n := hitungKuery(t, f,
		`SELECT count(*) FROM verification_logs WHERE order_id = $1 AND metode = 'ai' AND admin_id IS NULL`, id); n != 2 {
		t.Errorf("kedua log harus dari AI tanpa pemeriksa, dapat %d", n)
	}
}

// TestPGOCRAlasanReviewSesuaiAturanPRD menjalankan tiap penyebab jatuh ke
// perlu review: bukti kabur, rekening salah, tanggal terlalu tua, nominal
// kurang, dan nominal lebih.
func TestPGOCRAlasanReviewSesuaiAturanPRD(t *testing.T) {
	kasus := []struct {
		nama         string
		mauAlasan    string
		mauDestinasi string
		ubah         func(r *payment.ReadResult, total int64, tgl time.Time)
	}{
		{
			nama: "bukti kabur (keyakinan rendah)", mauAlasan: payment.AlasanTidakTerbaca,
			mauDestinasi: "allowed", ubah: func(r *payment.ReadResult, _ int64, _ time.Time) { r.Confidence = 0.42 },
		},
		{
			nama: "rekening tujuan tidak cocok", mauAlasan: payment.AlasanRekeningBeda,
			mauDestinasi: "unknown", ubah: func(r *payment.ReadResult, _ int64, _ time.Time) { r.RekAkhir = "0000000000" },
		},
		{
			nama: "tanggal transfer terlalu tua", mauAlasan: payment.AlasanLainnya, mauDestinasi: "allowed",
			ubah: func(r *payment.ReadResult, _ int64, tgl time.Time) {
				tua := tgl.AddDate(0, 0, -5)
				r.Tanggal = &tua
			},
		},
		{
			nama: "kurang bayar", mauAlasan: payment.AlasanKurangBayar, mauDestinasi: "allowed",
			ubah: func(r *payment.ReadResult, total int64, _ time.Time) { kurang := total - 1; r.Nominal = &kurang },
		},
		{
			nama: "lebih bayar", mauAlasan: payment.AlasanLebihBayar, mauDestinasi: "allowed",
			ubah: func(r *payment.ReadResult, total int64, _ time.Time) { lebih := total + 1; r.Nominal = &lebih },
		},
	}

	// SATU fixture untuk seluruh sub-uji: setiap kasus memakai pesanannya
	// sendiri, sehingga pengujian tetap terisolasi tanpa biaya menyiapkan
	// skema/migrasi berulang (suite integrasi sudah padat).
	f := newPGFixture(t)
	ctx := context.Background()
	setAIAktif(t, f, true)

	for i, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			id, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, i+1))
			tgl := tanggalPesananDB(t, f, id)

			hasil := hasilBacaCocok(total, tgl)
			k.ubah(&hasil, total, tgl)
			isi := berkasJPEGUji("f2-26-" + k.nama)
			hasil.Raw = nil
			seedBuktiOCR(t, f, id, "bukti.jpg", isi)
			svc := ocrPipeline(t, f, payment.NewMockReader(map[string]payment.ReadResult{
				payment.MockKey(isi): hasil,
			}))

			out, err := svc.Baca(ctx, id)
			if err != nil {
				t.Fatalf("Baca gagal: %v", err)
			}
			if out.Keputusan == nil || out.Keputusan.Alasan != k.mauAlasan {
				t.Fatalf("harus %s, dapat %+v", k.mauAlasan, out.Keputusan)
			}
			status, verif, alasan, doneAt := statusPesanan(t, f, id)
			if status != "submitted" || verif != "perlu_review" || alasan != k.mauAlasan {
				t.Errorf("pesanan harus tetap submitted/perlu_review/%s, dapat %s/%s/%q",
					k.mauAlasan, status, verif, alasan)
			}
			if doneAt != nil {
				t.Error("perlu review tidak boleh mengisi done_at")
			}
			if n := pekerjaanDokumen(t, f, id); n != 0 {
				t.Errorf("perlu review tidak boleh melahirkan dokumen, dapat %d job", n)
			}
			// Bukti yang tidak terbaca disimpan tanpa nominal (NULL), bukan 0.
			var totalTerbaca *int64
			if err := f.pool.QueryRow(ctx,
				`SELECT total_terbaca FROM verification_logs WHERE order_id = $1`, id).Scan(&totalTerbaca); err != nil {
				t.Fatalf("baca log: %v", err)
			}
			if k.mauAlasan == payment.AlasanTidakTerbaca && totalTerbaca != nil {
				t.Errorf("nominal yang tidak terbaca harus tersimpan NULL, dapat %v", *totalTerbaca)
			}
			if k.mauAlasan != payment.AlasanTidakTerbaca && totalTerbaca == nil {
				t.Error("nominal yang terbaca harus tersimpan pada log")
			}
			var destinationJSON []byte
			if err := f.pool.QueryRow(ctx, `SELECT destination_results FROM verification_logs WHERE order_id=$1`, id).Scan(&destinationJSON); err != nil {
				t.Fatalf("baca destination_results: %v", err)
			}
			var destinations []payment.DestinationResult
			if err := json.Unmarshal(destinationJSON, &destinations); err != nil {
				t.Fatalf("urai destination_results: %v", err)
			}
			if len(destinations) != 1 || destinations[0].Status != k.mauDestinasi {
				t.Fatalf("attempt review harus punya satu hasil tujuan %s: %+v", k.mauDestinasi, destinations)
			}
		})
	}
}

// TestPGOCRMatiTidakMemanggilProvider menegakkan PRD §5 dan SystemDesign §4.3:
// dengan `ai.aktif=false` tidak ada panggilan provider dan tidak ada perubahan
// keadaan — pesanan menunggu verifikasi manual.
func TestPGOCRMatiTidakMemanggilProvider(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	id, _ := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 1))
	setAIAktif(t, f, false)

	isi := berkasJPEGUji("f2-26-mati")
	seedBuktiOCR(t, f, id, "bukti.jpg", isi)
	reader := payment.NewMockReader(map[string]payment.ReadResult{
		payment.MockKey(isi): hasilBacaCocok(1, tanggalPesananDB(t, f, id)),
	})
	svc := ocrPipeline(t, f, reader)

	hasil, err := svc.Baca(ctx, id)
	if err != nil {
		t.Fatalf("Baca gagal: %v", err)
	}
	if !hasil.Dilewati || reader.JumlahPanggilan() != 0 {
		t.Fatalf("AI mati harus dilewati tanpa panggilan provider, dapat %+v (%d panggilan)",
			hasil, reader.JumlahPanggilan())
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id = $1`, id); n != 0 {
		t.Errorf("tidak boleh ada log verifikasi, dapat %d", n)
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM audit_logs WHERE entity_id = $1`, id); n != 0 {
		t.Errorf("tidak boleh ada entri audit, dapat %d", n)
	}
	var ocrAt *string
	if err := f.pool.QueryRow(ctx, `SELECT ocr_at::text FROM payment_proofs WHERE order_id = $1`, id).Scan(&ocrAt); err != nil {
		t.Fatalf("baca bukti: %v", err)
	}
	if ocrAt != nil {
		t.Error("bukti tidak boleh ditandai sudah dibaca")
	}
	status, verif, _, _ := statusPesanan(t, f, id)
	if status != "submitted" || verif != "belum" {
		t.Errorf("keadaan pesanan tidak boleh berubah, dapat %s/%s", status, verif)
	}
}

// TestPGSubmitDanUnggahMenitipJobOCR membuktikan pemicu pekerjaan: submit
// pesanan (SystemDesign.md §4.2) dan unggahan bukti susulan menitipkan job
// `ocr_bukti` pada transaksi yang sama — tetapi HANYA ketika AI menyala.
func TestPGSubmitDanUnggahMenitipJobOCR(t *testing.T) {
	f := newPGFixture(t)
	cookies, csrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	setAIAktif(t, f, true)

	// Submit dengan AI menyala -> job pembacaan bukti tertitip.
	draft := buatDraftHTTP(t, f, cookies, csrf, produk, 1)
	id, _ := draft["id"].(string)
	if status := unggahBuktiHTTP(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookies, csrf,
		map[string][]byte{"bukti.jpg": berkasJPEGUji("f2-26-submit")}); status != http.StatusOK {
		t.Fatalf("submit harus 200, dapat %d", status)
	}
	if n := jobOCR(t, f, id); n != 1 {
		t.Fatalf("submit dengan AI menyala harus menitipkan 1 job OCR, dapat %d", n)
	}

	// Unggahan bukti susulan pada pesanan submitted -> job susulan.
	if status := unggahBuktiHTTP(t, f.srv.URL+"/api/v1/orders/"+id+"/proofs", cookies, csrf,
		map[string][]byte{"tambahan.jpg": berkasJPEGUji("f2-26-susulan")}); status != http.StatusCreated {
		t.Fatalf("unggah bukti susulan harus 201, dapat %d", status)
	}
	if n := jobOCR(t, f, id); n != 2 {
		t.Fatalf("unggahan bukti pada pesanan submitted harus menitipkan job OCR, dapat %d", n)
	}

	// AI dimatikan -> submit berikutnya tidak menitipkan job.
	setAIAktif(t, f, false)
	draft2 := buatDraftHTTP(t, f, cookies, csrf, produk, 1)
	id2, _ := draft2["id"].(string)
	if status := unggahBuktiHTTP(t, f.srv.URL+"/api/v1/orders/"+id2+"/submit", cookies, csrf,
		map[string][]byte{"bukti.jpg": berkasJPEGUji("f2-26-mati-submit")}); status != http.StatusOK {
		t.Fatalf("submit dengan AI mati harus 200, dapat %d", status)
	}
	if n := jobOCR(t, f, id2); n != 0 {
		t.Errorf("submit dengan AI mati tidak boleh menitipkan job OCR, dapat %d", n)
	}
	// Pesanannya tetap tersimpan utuh.
	status, _, _, _ := statusPesanan(t, f, id2)
	if status != "submitted" {
		t.Errorf("pesanan harus tetap tersimpan submitted, dapat %s", status)
	}
}

func TestPGOCRRaceAttemptAIIdentikMengembalikanOutcomeFinal(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	orderID, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 903))
	proofID := seedBuktiOCR(t, f, orderID, "race-ai.jpg", berkasJPEGUji("race-ai"))
	if _, err := f.pool.Exec(ctx, `UPDATE payment_proofs
 SET ocr_at=now(),ocr_confidence=0 WHERE id=$1`, proofID); err != nil {
		t.Fatal(err)
	}

	verify := payment.NewVerificationService(f.paymentsRepo)
	const workers = 8
	outcomes := make([]payment.VerifyOutcome, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i], errs[i] = verify.MarkVerified(ctx, payment.SystemActor(), orderID, payment.VerifyInput{
				Hasil: payment.VerifPerluReview, Metode: payment.MetodeAI,
				TotalTerbaca: &total, Alasan: payment.AlasanTidakTerbaca,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	winnerID := outcomes[0].Log.ID
	if winnerID == "" {
		t.Fatalf("outcome pertama tidak memiliki verification log: %+v err=%v", outcomes[0], errs[0])
	}
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d harus membaca outcome final, dapat error %v", i, errs[i])
		}
		if outcomes[i].Log.ID != winnerID {
			t.Fatalf("worker %d mendapat log %q, ingin outcome final %q", i, outcomes[i].Log.ID, winnerID)
		}
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id=$1 AND metode='ai'`, orderID); n != 1 {
		t.Fatalf("race attempt AI identik harus menyimpan tepat satu log, dapat %d", n)
	}
}

func TestPGOCRFinalRevalidationMenurunkanKeReview(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	setAIAktif(t, f, true)

	t.Run("rekening menjadi ambigu", func(t *testing.T) {
		orderID, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 906))
		proofID := seedBuktiOCR(t, f, orderID, "ambigu.jpg", berkasJPEGUji("ambigu"))
		if _, err := f.pool.Exec(ctx, `UPDATE payment_proofs SET ocr_at=now(),ocr_bank='BCA',ocr_rek_akhir='0011',ocr_confidence=0.99 WHERE id=$1`, proofID); err != nil {
			t.Fatal(err)
		}
		set, err := f.bank.EffectiveForBranch(ctx, f.branchA)
		if err != nil || len(set.Accounts) == 0 {
			t.Fatalf("rekening awal: %+v %v", set, err)
		}
		var account2 string
		if err := f.pool.QueryRow(ctx, `INSERT INTO bank_accounts(bank,nama_pemilik,nomor,is_active)
			VALUES('BCA','Pemilik Ambigu','8888880011',true) RETURNING id`).Scan(&account2); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO branch_bank_accounts(branch_id,bank_account_id,is_allowed,assigned_by)
			VALUES($1,$2,true,$3)`, f.branchA, account2, f.adminSA); err != nil {
			t.Fatal(err)
		}
		out, err := payment.NewVerificationService(f.paymentsRepo).MarkVerified(ctx, payment.SystemActor(), orderID, payment.VerifyInput{
			Hasil: payment.VerifCocok, Metode: payment.MetodeAI, TotalTerbaca: &total,
			ProofDestinations: []payment.ProofDestination{{PaymentProofID: proofID, BankAccountID: set.Accounts[0].ID}},
		})
		if err != nil {
			t.Fatalf("ambigu harus menjadi review, bukan error retry: %v", err)
		}
		if out.Order.Status != payment.StatusSubmitted || out.Log.Hasil != payment.VerifPerluReview || out.Log.Alasan != payment.AlasanRekeningBeda {
			t.Fatalf("hasil ambigu salah: %+v", out)
		}
		if len(out.Log.DestinationResults) != 1 || out.Log.DestinationResults[0].Status != "ambiguous" {
			t.Fatalf("snapshot ambigu salah: %+v", out.Log.DestinationResults)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE branch_bank_accounts SET is_allowed=false,revoked_by=$3,revoked_at=now()
			WHERE branch_id=$1 AND bank_account_id=$2`, f.branchA, account2, f.adminSA); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("digit lebih pendek dari kebijakan", func(t *testing.T) {
		orderID, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 907))
		proofID := seedBuktiOCR(t, f, orderID, "pendek.jpg", berkasJPEGUji("pendek"))
		if _, err := f.pool.Exec(ctx, `UPDATE payment_proofs SET ocr_at=now(),ocr_bank='BCA',ocr_rek_akhir='1',ocr_confidence=0.99 WHERE id=$1`, proofID); err != nil {
			t.Fatal(err)
		}
		out, err := payment.NewVerificationService(f.paymentsRepo).MarkVerified(ctx, payment.SystemActor(), orderID, payment.VerifyInput{
			Hasil: payment.VerifPerluReview, Metode: payment.MetodeAI, TotalTerbaca: &total,
			Alasan: payment.AlasanRekeningBeda,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Log.DestinationResults) != 1 || out.Log.DestinationResults[0].Status != "unreadable" {
			t.Fatalf("digit pendek tidak boleh allowed: %+v", out.Log.DestinationResults)
		}
	})

	t.Run("assignment dicabut sebelum commit final", func(t *testing.T) {
		orderID, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 908))
		proofID := seedBuktiOCR(t, f, orderID, "cabut.jpg", berkasJPEGUji("cabut"))
		if _, err := f.pool.Exec(ctx, `UPDATE payment_proofs SET ocr_at=now(),ocr_bank='BCA',ocr_rek_akhir='0011',ocr_confidence=0.99 WHERE id=$1`, proofID); err != nil {
			t.Fatal(err)
		}
		set, err := f.bank.EffectiveForBranch(ctx, f.branchA)
		if err != nil || len(set.Accounts) == 0 {
			t.Fatalf("rekening awal: %+v %v", set, err)
		}
		oldAccount := set.Accounts[0].ID
		if _, err := f.pool.Exec(ctx, `UPDATE branch_bank_accounts SET is_allowed=false,revoked_by=$3,revoked_at=now()
			WHERE branch_id=$1 AND bank_account_id=$2`, f.branchA, oldAccount, f.adminSA); err != nil {
			t.Fatal(err)
		}
		out, err := payment.NewVerificationService(f.paymentsRepo).MarkVerified(ctx, payment.SystemActor(), orderID, payment.VerifyInput{
			Hasil: payment.VerifCocok, Metode: payment.MetodeAI, TotalTerbaca: &total,
			ProofDestinations: []payment.ProofDestination{{PaymentProofID: proofID, BankAccountID: oldAccount}},
		})
		if err != nil {
			t.Fatalf("rekening dicabut harus review, bukan error retry: %v", err)
		}
		if out.Log.Hasil != payment.VerifPerluReview || len(out.Log.DestinationResults) != 1 || out.Log.DestinationResults[0].Status != "not_allowed" {
			t.Fatalf("hasil rekening dicabut salah: %+v", out)
		}
	})

	t.Run("key reevaluate diverifikasi lagi di transaksi final", func(t *testing.T) {
		orderID, total := seedPesananSubmitted(t, f, f.branchB, nomorUji(t, f, 909))
		var account string
		if err := f.pool.QueryRow(ctx, `INSERT INTO bank_accounts(bank,nama_pemilik,nomor,is_active)
			VALUES('BRI','Cabang B','77770022',true) RETURNING id`).Scan(&account); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO branch_bank_accounts(branch_id,bank_account_id,is_allowed,assigned_by)
			VALUES($1,$2,true,$3)`, f.branchB, account, f.adminSA); err != nil {
			t.Fatal(err)
		}
		proofID := seedBuktiOCR(t, f, orderID, "stale-final.jpg", berkasJPEGUji("stale-final"))
		if _, err := f.pool.Exec(ctx, `UPDATE payment_proofs SET ocr_at=now(),ocr_bank='BRI',ocr_rek_akhir='0022',ocr_confidence=0.99 WHERE id=$1`, proofID); err != nil {
			t.Fatal(err)
		}
		_, err := payment.NewVerificationService(f.paymentsRepo).MarkVerified(ctx, payment.SystemActor(), orderID, payment.VerifyInput{
			Hasil: payment.VerifCocok, Metode: payment.MetodeAI, TotalTerbaca: &total,
			ProofDestinations:        []payment.ProofDestination{{PaymentProofID: proofID, BankAccountID: account}},
			ExpectedAllowlistVersion: 999, ExpectedProofFingerprint: "00",
		})
		if !errors.Is(err, payment.ErrOCRSnapshotStale) {
			t.Fatalf("key stale harus ditolak di transaksi final, dapat %v", err)
		}
		if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id=$1`, orderID); n != 0 {
			t.Fatalf("key stale tidak boleh menulis attempt, dapat %d", n)
		}
	})
}
