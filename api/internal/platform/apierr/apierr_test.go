package apierr

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestBentukErrorJSON mengunci kontrak publik: klien frontend bergantung pada
// bentuk {"error":{code,message,fields}} (SystemDesign.md §6).
func TestBentukErrorJSON(t *testing.T) {
	err := Validation("Data yang dikirim belum lengkap atau tidak valid.", map[string]string{"qty": "harus > 0"})

	raw, marshalErr := json.Marshal(err.JSON())
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}

	var luar map[string]json.RawMessage
	if uerr := json.Unmarshal(raw, &luar); uerr != nil {
		t.Fatalf("bentuk JSON tidak valid: %v", uerr)
	}
	if len(luar) != 1 || luar["error"] == nil {
		t.Fatalf("objek teratas harus hanya berisi \"error\": %s", raw)
	}

	var dalam map[string]json.RawMessage
	if uerr := json.Unmarshal(luar["error"], &dalam); uerr != nil {
		t.Fatalf("objek error tidak valid: %v", uerr)
	}
	if len(dalam) != 3 {
		t.Fatalf("objek error harus berisi code, message, fields: %s", raw)
	}

	assertString(t, dalam, "code", "VALIDATION_FAILED")
	assertString(t, dalam, "message", "Data yang dikirim belum lengkap atau tidak valid.")

	var fields map[string]string
	if uerr := json.Unmarshal(dalam["fields"], &fields); uerr != nil {
		t.Fatalf("fields tidak valid: %v", uerr)
	}
	if fields["qty"] != "harus > 0" {
		t.Errorf("fields tidak sesuai: %v", fields)
	}
}

// TestFieldsDihilangkanBilaKosong memastikan "fields" tidak muncul sebagai
// null pada error tanpa detail per-field.
func TestFieldsDihilangkanBilaKosong(t *testing.T) {
	err := NotFound("Endpoint tidak ditemukan.")
	raw, marshalErr := json.Marshal(err.JSON())
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}

	var luar map[string]json.RawMessage
	if uerr := json.Unmarshal(raw, &luar); uerr != nil {
		t.Fatalf("bentuk JSON tidak valid: %v", uerr)
	}
	var dalam map[string]json.RawMessage
	if uerr := json.Unmarshal(luar["error"], &dalam); uerr != nil {
		t.Fatalf("objek error tidak valid: %v", uerr)
	}
	if _, ada := dalam["fields"]; ada {
		t.Errorf("fields tidak boleh muncul saat kosong: %s", raw)
	}
	assertString(t, dalam, "code", "NOT_FOUND")
	assertString(t, dalam, "message", "Endpoint tidak ditemukan.")
}

func TestStatusDanKodeStandar(t *testing.T) {
	kasus := []struct {
		err    *Error
		status int
		code   string
	}{
		{Unauthenticated("x"), http.StatusUnauthorized, CodeUnauthenticated},
		{Forbidden("x"), http.StatusForbidden, CodeForbidden},
		{NotFound("x"), http.StatusNotFound, CodeNotFound},
		{Conflict("DUPLIKAT", "x"), http.StatusConflict, "DUPLIKAT"},
		{RateLimited("x"), http.StatusTooManyRequests, CodeRateLimited},
		{Internal(context.Canceled), http.StatusInternalServerError, CodeInternal},
	}
	for _, k := range kasus {
		if k.err.Status() != k.status {
			t.Errorf("%s: status %d, mau %d", k.code, k.err.Status(), k.status)
		}
		if k.err.Code() != k.code {
			t.Errorf("%s: kode %q, mau %q", k.code, k.err.Code(), k.code)
		}
	}
}

// TestFromMemetakanBatasWaktu memastikan error context diterjemahkan ke 504.
func TestFromMemetakanBatasWaktu(t *testing.T) {
	err := From(context.DeadlineExceeded)
	if err.Status() != http.StatusGatewayTimeout || err.Code() != CodeTimeout {
		t.Fatalf("context deadline harus jadi TIMEOUT 504, dapat %d %s", err.Status(), err.Code())
	}
}

// TestFromTidakMembocorkanPesanInternal memastikan error tak dikenal menjadi
// INTERNAL dengan pesan generik, sementara penyebabnya tetap tersimpan.
func TestFromTidakMembocorkanPesanInternal(t *testing.T) {
	penyebab := rahasiaErr{}
	tidakDikenal := From(penyebab)

	if tidakDikenal.Code() != CodeInternal {
		t.Fatalf("error tak dikenal harus INTERNAL, dapat %s", tidakDikenal.Code())
	}
	if tidakDikenal.Message() == penyebab.Error() {
		t.Fatalf("pesan internal bocor ke klien: %q", tidakDikenal.Message())
	}
	if tidakDikenal.Unwrap() == nil {
		t.Fatal("penyebab asli harus tetap tersimpan untuk log")
	}
}

// TestFromMeneruskanErrorBertipe memastikan handler boleh mengembalikan error
// dari service tanpa membungkus ulang.
func TestFromMeneruskanErrorBertipe(t *testing.T) {
	asli := Forbidden("tidak boleh")
	if got := From(asli); got != asli {
		t.Fatal("error bertipe harus diteruskan apa adanya")
	}
	if got := From(nil); got != nil {
		t.Fatal("nil harus tetap nil")
	}
}

func assertString(t *testing.T, m map[string]json.RawMessage, key, mau string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(m[key], &got); err != nil {
		t.Fatalf("%s bukan string: %v", key, err)
	}
	if got != mau {
		t.Errorf("%s = %q, mau %q", key, got, mau)
	}
}

type rahasiaErr struct{}

func (rahasiaErr) Error() string { return "koneksi ke 10.0.0.5 ditolak" }
