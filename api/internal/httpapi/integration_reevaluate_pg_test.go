package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/grahita/portal/api/internal/payment"
)

func jalankanReevaluateTerbaru(t *testing.T, f *pgFixture, orderID string) payment.OCRHasil {
	t.Helper()
	hasilAntre, err := f.paymentsRepo.EnqueueDestinationReevaluation(t.Context(), orderID)
	if err != nil {
		t.Fatalf("antre reevaluate %s: %v", orderID, err)
	}
	if !hasilAntre.Queued || hasilAntre.Duplicate {
		t.Fatalf("snapshot baru harus menghasilkan job baru: %+v", hasilAntre)
	}
	var version int64
	var fingerprint string
	if err := f.pool.QueryRow(t.Context(), `SELECT (args->>'allowlist_version')::bigint,args->>'proof_set_fingerprint'
		FROM river_job WHERE kind='ocr_bukti' AND args->>'order_id'=$1 ORDER BY id DESC LIMIT 1`, orderID).
		Scan(&version, &fingerprint); err != nil {
		t.Fatalf("baca snapshot job reevaluate: %v", err)
	}
	reader := payment.NewMockReader(nil)
	hasil, err := ocrPipeline(t, f, reader).BacaUlang(t.Context(), orderID, version, fingerprint)
	if err != nil {
		t.Fatalf("jalankan reevaluate: %v", err)
	}
	if hasil.Dilewati || hasil.Keputusan == nil || reader.JumlahPanggilan() != 0 {
		t.Fatalf("reevaluate harus memakai OCR tersimpan tanpa provider: hasil=%+v calls=%d", hasil, reader.JumlahPanggilan())
	}
	return hasil
}

// VER-11: perubahan policy dan himpunan bukti masing-masing harus membuat
// snapshot reevaluate baru. Keputusan, log verifikasi, dan audit akhirnya
// harus mengacu pada versi/fingerprint baru, bukan job lama.
func TestPGReevaluateSetelahPolicyDanBuktiBerubahMenulisJejakAkhir(t *testing.T) {
	t.Run("policy rekening berubah", func(t *testing.T) {
		f := newPGFixture(t)
		setAIAktif(t, f, true)
		orderID, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 911))
		proofID := seedBuktiOCR(t, f, orderID, "policy.jpg", berkasJPEGUji("policy-baru"))
		tanggal := tanggalPesananDB(t, f, orderID)
		if _, err := f.pool.Exec(t.Context(), `UPDATE payment_proofs SET ocr_at=clock_timestamp(),ocr_nominal=$2,
			ocr_tanggal=$3,ocr_bank='BRI',ocr_rek_akhir='77777',ocr_confidence=0.99 WHERE id=$1`, proofID, total, tanggal); err != nil {
			t.Fatal(err)
		}

		pertama := jalankanReevaluateTerbaru(t, f, orderID)
		if pertama.Keputusan.Hasil != payment.VerifPerluReview || pertama.Keputusan.Alasan != payment.AlasanRekeningBeda {
			t.Fatalf("policy lama harus review rekening berbeda: %+v", pertama.Keputusan)
		}
		var versiLama int64
		var sidikLama []byte
		if err := f.pool.QueryRow(t.Context(), `SELECT branch_allowlist_version,proof_set_fingerprint FROM verification_logs
			WHERE order_id=$1 ORDER BY created_at LIMIT 1`, orderID).Scan(&versiLama, &sidikLama); err != nil {
			t.Fatal(err)
		}

		var rekeningBaru string
		if err := f.pool.QueryRow(t.Context(), `INSERT INTO bank_accounts(bank,nama_pemilik,nomor,is_active)
			VALUES ('BRI','Rekening Policy Baru','9900077777',true) RETURNING id`).Scan(&rekeningBaru); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO branch_bank_accounts(branch_id,bank_account_id,assigned_by)
			VALUES ($1,$2,$3)`, f.branchA, rekeningBaru, f.adminSA); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET bank_allowlist_version=bank_allowlist_version+1 WHERE id=$1`, f.branchA); err != nil {
			t.Fatal(err)
		}

		kedua := jalankanReevaluateTerbaru(t, f, orderID)
		if kedua.Keputusan.Hasil != payment.VerifCocok || kedua.Outcome == nil || kedua.Outcome.Order.Status != payment.StatusDone {
			t.Fatalf("policy baru yang mengizinkan rekening harus menyelesaikan order: %+v", kedua)
		}
		var versiBaru int64
		var sidikBaru []byte
		if err := f.pool.QueryRow(t.Context(), `SELECT branch_allowlist_version,proof_set_fingerprint FROM verification_logs
			WHERE order_id=$1 ORDER BY created_at DESC LIMIT 1`, orderID).Scan(&versiBaru, &sidikBaru); err != nil {
			t.Fatal(err)
		}
		if versiBaru <= versiLama || string(sidikBaru) != string(sidikLama) {
			t.Fatalf("policy harus berganti versi dengan fingerprint bukti tetap: version %d→%d fingerprint sama=%v", versiLama, versiBaru, string(sidikBaru) == string(sidikLama))
		}
		if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id=$1`, orderID); n != 2 {
			t.Fatalf("harus ada dua log keputusan historis, dapat %d", n)
		}
		if n := hitungKuery(t, f, `SELECT count(*) FROM audit_logs WHERE entity='order' AND entity_id=$1 AND action='verify'`, orderID); n != 2 {
			t.Fatalf("kedua keputusan harus punya audit, dapat %d", n)
		}
	})

	t.Run("himpunan bukti berubah", func(t *testing.T) {
		f := newPGFixture(t)
		setAIAktif(t, f, true)
		orderID, total := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 912))
		tanggal := tanggalPesananDB(t, f, orderID)
		setengah := total / 2
		proofA := seedBuktiOCR(t, f, orderID, "a.jpg", berkasJPEGUji("bukti-a"))
		if _, err := f.pool.Exec(t.Context(), `UPDATE payment_proofs SET ocr_at=clock_timestamp(),ocr_nominal=$2,
			ocr_tanggal=$3,ocr_bank='BCA',ocr_rek_akhir='00011',ocr_confidence=0.99 WHERE id=$1`, proofA, setengah, tanggal); err != nil {
			t.Fatal(err)
		}
		pertama := jalankanReevaluateTerbaru(t, f, orderID)
		if pertama.Keputusan.Hasil != payment.VerifPerluReview || pertama.Keputusan.Alasan != payment.AlasanKurangBayar {
			t.Fatalf("bukti pertama harus kurang bayar: %+v", pertama.Keputusan)
		}
		var sidikLama []byte
		if err := f.pool.QueryRow(t.Context(), `SELECT proof_set_fingerprint FROM verification_logs WHERE order_id=$1`, orderID).Scan(&sidikLama); err != nil {
			t.Fatal(err)
		}

		proofB := seedBuktiOCR(t, f, orderID, "b.jpg", berkasJPEGUji("bukti-b"))
		if _, err := f.pool.Exec(t.Context(), `UPDATE payment_proofs SET ocr_at=clock_timestamp(),ocr_nominal=$2,
			ocr_tanggal=$3,ocr_bank='BCA',ocr_rek_akhir='00011',ocr_confidence=0.99 WHERE id=$1`, proofB, total-setengah, tanggal); err != nil {
			t.Fatal(err)
		}
		kedua := jalankanReevaluateTerbaru(t, f, orderID)
		if kedua.Keputusan.Hasil != payment.VerifCocok || kedua.Outcome == nil || kedua.Outcome.Order.Status != payment.StatusDone {
			t.Fatalf("dua bukti terbaru harus melunasi order: %+v", kedua)
		}
		var sidikBaru []byte
		if err := f.pool.QueryRow(t.Context(), `SELECT proof_set_fingerprint FROM verification_logs
			WHERE order_id=$1 ORDER BY created_at DESC LIMIT 1`, orderID).Scan(&sidikBaru); err != nil {
			t.Fatal(err)
		}
		if string(sidikBaru) == string(sidikLama) {
			t.Fatal("penambahan bukti harus menghasilkan fingerprint snapshot baru")
		}
		if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id=$1`, orderID); n != 2 {
			t.Fatalf("harus ada log sebelum dan sesudah bukti berubah, dapat %d", n)
		}
		if n := hitungKuery(t, f, `SELECT count(*) FROM audit_logs WHERE entity='order' AND entity_id=$1 AND action='verify'`, orderID); n != 2 {
			t.Fatalf("kedua keputusan harus punya audit, dapat %d", n)
		}
	})
}

func TestPGReevaluateDestinationEksplisitIdempotenDanGuard(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	setAIAktif(t, f, true)

	orderID, _ := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 901))
	proofID := seedBuktiOCR(t, f, orderID, "reevaluate.jpg", berkasJPEGUji("reevaluate"))
	if _, err := f.pool.Exec(ctx, `UPDATE payment_proofs SET ocr_at=now(),ocr_bank='BCA',ocr_rek_akhir='00011',ocr_confidence=0.99 WHERE id=$1`, proofID); err != nil {
		t.Fatal(err)
	}

	cookies, csrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	url := f.srv.URL + "/api/v1/admin/orders/" + orderID + "/verification/reevaluate-destination"
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, url, `{}`, cookies, csrf)
	if status != http.StatusAccepted {
		t.Fatalf("manager harus boleh reevaluate: %d %v", status, body)
	}
	data := body["data"].(map[string]any)
	if data["queued"] != true || data["duplicate"] != false {
		t.Fatalf("request pertama harus queued: %v", data)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, url, `{}`, cookies, csrf)
	if status != http.StatusAccepted {
		t.Fatalf("duplikat harus tetap 202: %d %v", status, body)
	}
	data = body["data"].(map[string]any)
	if data["queued"] != false || data["duplicate"] != true {
		t.Fatalf("request kedua harus duplicate: %v", data)
	}
	if n := jobOCR(t, f, orderID); n != 1 {
		t.Fatalf("duplikasi request harus satu job, dapat %d", n)
	}

	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, url, `{}`, cookies, "")
	if status != http.StatusForbidden {
		t.Fatalf("tanpa CSRF harus 403, dapat %d", status)
	}

	missingID, _ := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, 902))
	seedBuktiOCR(t, f, missingID, "missing.jpg", berkasJPEGUji("missing"))
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+missingID+"/verification/reevaluate-destination", `{}`, cookies, csrf)
	if status != http.StatusConflict || kodeErrorVerifikasi(body) != "OCR_RESULT_MISSING" {
		t.Fatalf("ocr_at NULL harus OCR_RESULT_MISSING: %d %v", status, body)
	}

	setAIAktif(t, f, false)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, url, `{}`, cookies, csrf)
	if status != http.StatusConflict || kodeErrorVerifikasi(body) != "AI_DISABLED" {
		t.Fatalf("AI mati harus AI_DISABLED: %d %v", status, body)
	}
}

func TestPGReevaluateJobTertundaTidakMembacaBuktiAtauPolicyBaru(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	setAIAktif(t, f, true)

	jalankan := func(t *testing.T, suffix int, ubah func(orderID string)) {
		orderID, _ := seedPesananSubmitted(t, f, f.branchA, nomorUji(t, f, suffix))
		proofID := seedBuktiOCR(t, f, orderID, "lama.jpg", berkasJPEGUji(orderID))
		if _, err := f.pool.Exec(ctx, `UPDATE payment_proofs SET ocr_at=now(),ocr_bank='BCA',ocr_rek_akhir='00011',ocr_confidence=0.99 WHERE id=$1`, proofID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.paymentsRepo.EnqueueDestinationReevaluation(ctx, orderID); err != nil {
			t.Fatal(err)
		}
		var version int64
		var fingerprint string
		if err := f.pool.QueryRow(ctx, `SELECT (args->>'allowlist_version')::bigint,args->>'proof_set_fingerprint'
			FROM river_job WHERE kind='ocr_bukti' AND args->>'order_id'=$1`, orderID).Scan(&version, &fingerprint); err != nil {
			t.Fatal(err)
		}
		ubah(orderID)
		reader := payment.NewMockReader(nil)
		hasil, err := ocrPipeline(t, f, reader).BacaUlang(ctx, orderID, version, fingerprint)
		if err != nil || !hasil.Dilewati || reader.JumlahPanggilan() != 0 {
			t.Fatalf("job lama harus skip tanpa provider: hasil=%+v calls=%d err=%v", hasil, reader.JumlahPanggilan(), err)
		}
		if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id=$1`, orderID); n != 0 {
			t.Fatalf("job stale tidak boleh menulis attempt, dapat %d", n)
		}
	}

	t.Run("bukti baru", func(t *testing.T) {
		jalankan(t, 904, func(orderID string) {
			seedBuktiOCR(t, f, orderID, "baru.jpg", berkasJPEGUji("baru"))
		})
	})
	t.Run("policy berubah", func(t *testing.T) {
		jalankan(t, 905, func(string) {
			if _, err := f.pool.Exec(ctx, `UPDATE branches SET bank_allowlist_version=bank_allowlist_version+1 WHERE id=$1`, f.branchA); err != nil {
				t.Fatal(err)
			}
		})
	})
}
