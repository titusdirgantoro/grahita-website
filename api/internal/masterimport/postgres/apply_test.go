package postgres

import (
	"bytes"
	"errors"
	"testing"

	"github.com/grahita/portal/api/internal/masterimport"
)

func TestValidateDecisionsMatriksIdentitas(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		decision string
		wantErr  bool
	}{
		{"blank preserve", "existing_access_identity_blank", "preserve_existing_verified_identity", false},
		{"blank revoke", "existing_access_identity_blank", "revoke_access_and_mark_incomplete", false},
		{"inconsistent revoke", "inconsistent_existing_access", "revoke_inconsistent_access", false},
		{"inconsistent preserve ditolak", "inconsistent_existing_access", "preserve_existing_verified_identity", true},
		{"remap tidak boleh diputus importer", "identity_remap_required", "revoke_inconsistent_access", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := storedPreviewItem{SourceRow: 3, EntityKind: masterimport.EntityBranch, EntityCode: "masked", Status: masterimport.StatusConflict, ReasonCode: tt.reason}
			_, err := validateDecisions([]storedPreviewItem{item}, []masterimport.Decision{{
				SourceRow: 3, EntityKind: masterimport.EntityBranch, EntityCode: "masked",
				Decision: tt.decision, Reason: "keputusan eksplisit",
			}})
			if tt.wantErr != errors.Is(err, masterimport.ErrApplyBlocked) {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDecisionsNeedsReviewPerItem(t *testing.T) {
	items := []storedPreviewItem{
		{SourceRow: 3, EntityKind: masterimport.EntityNote, EntityCode: "AR", Status: masterimport.StatusNeedsReview},
		{SourceRow: 3, EntityKind: masterimport.EntityNote, EntityCode: "AU", Status: masterimport.StatusNeedsReview},
	}
	one := []masterimport.Decision{{SourceRow: 3, EntityKind: masterimport.EntityNote, EntityCode: "AR", Decision: "ignored_with_reason", Reason: "bukan aturan bisnis"}}
	if _, err := validateDecisions(items, one); !errors.Is(err, masterimport.ErrApplyBlocked) {
		t.Fatalf("keputusan agregat/parsial harus ditolak, err=%v", err)
	}
	all := append(one, masterimport.Decision{SourceRow: 3, EntityKind: masterimport.EntityNote, EntityCode: "AU", Decision: "ignored_with_reason", Reason: "bukan aturan bisnis"})
	if _, err := validateDecisions(items, all); err != nil {
		t.Fatalf("semua item sudah diputus: %v", err)
	}
}

func TestIdentityRevocationAuditReasonHanyaKodeKebijakan(t *testing.T) {
	tests := []struct {
		decision string
		want     string
		wantErr  bool
	}{
		{"revoke_access_and_mark_incomplete", "source_identity_blank_access_revoked", false},
		{"revoke_inconsistent_access", "inconsistent_existing_access_revoked", false},
		{"keputusan-tidak-dikenal", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.decision, func(t *testing.T) {
			got, err := identityRevocationAuditReason(tt.decision)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("reason=%q err=%v, ingin reason=%q wantErr=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestRatioNumericEksakUntukRasioDesimal(t *testing.T) {
	if got := ratioNumeric("1/10"); got != "0.1" {
		t.Fatalf("ratioNumeric=%q", got)
	}
	if got := ratioNumeric("0"); got != "0" {
		t.Fatalf("ratioNumeric nol=%q", got)
	}
}

func TestSnapshotCipherRoundTripDanAAD(t *testing.T) {
	cipher := newSnapshotCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	plaintext := []byte(`{"before":{"name":"PII privat"}}`)
	ciphertext, keyID, err := cipher.encrypt(plaintext, "run:1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("PII privat")) {
		t.Fatal("ciphertext menyimpan PII plaintext")
	}
	decoded, err := cipher.decrypt(ciphertext, keyID, "run:1")
	if err != nil || !bytes.Equal(decoded, plaintext) {
		t.Fatalf("round-trip=%q err=%v", decoded, err)
	}
	if _, err := cipher.decrypt(ciphertext, keyID, "run:2"); err == nil {
		t.Fatal("AAD yang berbeda harus menolak dekripsi")
	}
}

func TestSnapshotCipherMenolakKunciTidakValid(t *testing.T) {
	if _, _, err := newSnapshotCipher("").encrypt([]byte("x"), "run:1"); !errors.Is(err, errSnapshotKeyInvalid) {
		t.Fatalf("err=%v", err)
	}
}
