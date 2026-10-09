package auth

import (
	"context"
	"testing"
	"time"
)

// TestCreateResetTokenMenyimpanAuditBersamaToken membuktikan fakta audit yang
// menyertai penerbitan tautan diteruskan sampai ke penyimpanan dan disimpan
// bersama tokennya (F2-28). Pada implementasi PostgreSQL keduanya berada dalam
// satu transaksi; di memori keduanya selalu "satu langkah".
//
// Alur publik (lupa kata sandi) TIDAK menitipkan fakta audit: ia bukan aksi
// admin yang perlu dicatat, dan harus tetap tidak menambah catatan apa pun.
func TestCreateResetTokenMenyimpanAuditBersamaToken(t *testing.T) {
	f := newFixtureAktivasi(t)
	ctx := context.Background()

	rec := &ResetAudit{
		ActorKind: KindAdmin,
		ActorID:   "adm-sa",
		ActorNama: "Rina",
		IP:        "203.0.113.7",
		Entity:    "branch_user",
		EntityID:  "u-1",
		Action:    "reset",
		After:     map[string]any{"user_id": "u-1"},
	}
	if err := f.svc.SendPasswordResetFor(ctx, KindBranch, "u-1", time.Hour, rec); err != nil {
		t.Fatalf("kirim tautan reset: %v", err)
	}

	audits := f.store.ResetAudits()
	if len(audits) != 1 {
		t.Fatalf("fakta audit harus tersimpan bersama token, dapat %d", len(audits))
	}
	got := audits[0]
	if got.ActorKind != KindAdmin || got.ActorID != "adm-sa" || got.ActorNama != "Rina" || got.IP != "203.0.113.7" {
		t.Errorf("aktor/IP fakta audit tidak utuh: %+v", got)
	}
	if got.Entity != "branch_user" || got.EntityID != "u-1" || got.Action != "reset" {
		t.Errorf("entitas/aksi fakta audit tidak utuh: %+v", got)
	}

	// Alur publik: token tetap dibuat, tetapi tidak ada catatan audit baru.
	if err := f.svc.ForgotPassword(ctx, "bandung@example.test", time.Hour, RequestMeta{}); err != nil {
		t.Fatalf("lupa kata sandi: %v", err)
	}
	if n := len(f.store.ResetAudits()); n != 1 {
		t.Errorf("lupa kata sandi (alur publik) tidak boleh menambah catatan audit: %d", n)
	}
}
