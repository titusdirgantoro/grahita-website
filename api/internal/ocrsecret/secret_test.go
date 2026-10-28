package ocrsecret

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
)

type memoryRepo struct {
	rec    Record
	exists bool
}

func (m *memoryRepo) Load(context.Context) (Record, error) {
	if !m.exists {
		return Record{}, ErrNotConfigured
	}
	return m.rec, nil
}
func (m *memoryRepo) Save(_ context.Context, _ auth.Actor, rec Record) error {
	if m.exists {
		rec.Version = m.rec.Version + 1
	} else {
		rec.Version = 1
	}
	rec.UpdatedAt = time.Now()
	m.rec = rec
	m.exists = true
	return nil
}
func (m *memoryRepo) MarkVerified(_ context.Context, _ auth.Actor, version int64) error {
	if !m.exists || m.rec.Version != version {
		return ErrStale
	}
	now := time.Now()
	m.rec.VerifiedAt = &now
	return nil
}
func (m *memoryRepo) Delete(context.Context, auth.Actor) error { m.exists = false; return nil }

func TestKunciGeminiTerenkripsiDanRotasiMembatalkanVerifikasi(t *testing.T) {
	ctx := context.Background()
	repo := &memoryRepo{}
	master := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	svc, err := New(repo, master)
	if err != nil {
		t.Fatal(err)
	}
	sa := auth.Actor{UserID: "admin-uji", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	manager := auth.Actor{UserID: "manager-uji", Kind: auth.KindAdmin, Role: auth.RoleManager}
	if err := svc.Save(ctx, manager, "AIza-manager-tidak-boleh"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manager: %v", err)
	}
	key := "AIza-kunci-uji-pertama-123456"
	if err := svc.Save(ctx, sa, key); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(repo.rec.Ciphertext), key) {
		t.Fatal("ciphertext memuat kunci plaintext")
	}
	status, err := svc.Status(ctx)
	if err != nil || !status.Configured || status.Verified {
		t.Fatalf("status awal: %+v %v", status, err)
	}
	if _, err := svc.KeyForRead(ctx); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("worker harus menolak sebelum uji: %v", err)
	}
	got, version, err := svc.KeyForTest(ctx)
	if err != nil || got != key {
		t.Fatalf("kunci uji: %v", err)
	}
	if err := svc.MarkVerified(ctx, sa, version); err != nil {
		t.Fatal(err)
	}
	got, err = svc.KeyForRead(ctx)
	if err != nil || got != key {
		t.Fatalf("kunci worker: %v", err)
	}
	if err := svc.Save(ctx, sa, "AIza-kunci-uji-kedua-1234567"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.KeyForRead(ctx); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("rotasi harus mencabut izin: %v", err)
	}
	if err := svc.MarkVerified(ctx, sa, version); !errors.Is(err, ErrStale) {
		t.Fatalf("versi lama tidak boleh diverifikasi: %v", err)
	}
	if err := svc.Delete(ctx, sa); err != nil {
		t.Fatal(err)
	}
	status, err = svc.Status(ctx)
	if err != nil || status.Configured {
		t.Fatalf("hapus: %+v %v", status, err)
	}
}
