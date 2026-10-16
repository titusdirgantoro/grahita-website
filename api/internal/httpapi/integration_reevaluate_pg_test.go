package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/grahita/portal/api/internal/payment"
)

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
