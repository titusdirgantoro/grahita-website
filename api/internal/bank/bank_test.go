package bank

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
)

func superadmin() auth.Actor {
	return auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
}

func manager() auth.Actor {
	return auth.Actor{UserID: "adm-2", Kind: auth.KindAdmin, Role: auth.RoleManager}
}

func branchUser() auth.Actor {
	return auth.Actor{UserID: "usr-1", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-1"}
}

func TestCreateValidasi(t *testing.T) {
	cases := []struct {
		nama string
		in   NewAccount
	}{
		{"bank tak dikenal", NewAccount{Bank: "BNI", NamaPemilik: "PT Grahita", Nomor: "1234567890"}},
		{"nama terlalu pendek", NewAccount{Bank: BankBCA, NamaPemilik: "A", Nomor: "1234567890"}},
		{"nomor bukan angka", NewAccount{Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "12345abcde"}},
		{"nomor terlalu pendek", NewAccount{Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "12345"}},
	}
	for _, c := range cases {
		t.Run(c.nama, func(t *testing.T) {
			svc := NewService(NewMemory())
			if _, err := svc.Create(context.Background(), superadmin(), c.in); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("harus ErrInvalidData, dapat %v", err)
			}
		})
	}
}

func TestCreateNormalisasiDanAudit(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo)

	saved, err := svc.Create(context.Background(), superadmin(), NewAccount{
		Bank: "bca", NamaPemilik: "  PT Grahita Indonesia  ", Nomor: "1234 567 890",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if saved.Bank != BankBCA || saved.NamaPemilik != "PT Grahita Indonesia" || saved.Nomor != "1234567890" {
		t.Fatalf("normalisasi tidak sesuai: %+v", saved)
	}
	if !saved.IsActive {
		t.Error("rekening baru harus aktif")
	}
	entries := repo.AuditEntries()
	if len(entries) != 1 {
		t.Fatalf("harus 1 entri audit, dapat %d", len(entries))
	}
	if entries[0].Entity != "bank_account" || entries[0].Action != "create" || entries[0].EntityID != saved.ID {
		t.Errorf("entri audit tidak sesuai: %+v", entries[0])
	}
	raw, err := json.Marshal(entries[0].After)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), saved.Nomor) || !strings.Contains(string(raw), "••••7890") {
		t.Fatalf("audit harus masked tanpa nomor lengkap: %s", raw)
	}
}

func TestCreateNonaktifSejakAwal(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo)
	nonaktif := false
	saved, err := svc.Create(context.Background(), superadmin(), NewAccount{
		Bank: BankBRI, NamaPemilik: "PT Grahita", Nomor: "0987654321", IsActive: &nonaktif,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if saved.IsActive {
		t.Error("is_active=false harus dihormati")
	}
}

func TestCreateDuplikat(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo)
	if _, err := svc.Create(context.Background(), superadmin(), NewAccount{
		Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "111222333",
	}); err != nil {
		t.Fatalf("Create pertama: %v", err)
	}
	_, err := svc.Create(context.Background(), superadmin(), NewAccount{
		Bank: BankBCA, NamaPemilik: "PT Grahita Lain", Nomor: "111222333",
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplikat harus ErrDuplicate, dapat %v", err)
	}
}

func TestOtorisasiHanyaSuperadmin(t *testing.T) {
	repo := NewMemory()
	repo.Put(Account{ID: "bk-1", Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "1234567890", IsActive: true})
	svc := NewService(repo)
	ctx := context.Background()

	for _, aktor := range []struct {
		nama  string
		actor auth.Actor
	}{{"manager", manager()}, {"cabang", branchUser()}} {
		if _, err := svc.List(ctx, aktor.actor); !errors.Is(err, ErrForbidden) {
			t.Errorf("List oleh %s harus ErrForbidden, dapat %v", aktor.nama, err)
		}
		if _, err := svc.Create(ctx, aktor.actor, NewAccount{Bank: BankBCA, NamaPemilik: "PT", Nomor: "999888777"}); !errors.Is(err, ErrForbidden) {
			t.Errorf("Create oleh %s harus ErrForbidden, dapat %v", aktor.nama, err)
		}
		if _, err := svc.Update(ctx, aktor.actor, "bk-1", Patch{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("Update oleh %s harus ErrForbidden, dapat %v", aktor.nama, err)
		}
	}
	if _, err := svc.List(ctx, superadmin()); err != nil {
		t.Errorf("superadmin harus boleh List: %v", err)
	}
}

func TestActiveMenyembunyikanNonaktif(t *testing.T) {
	repo := NewMemory()
	repo.Put(Account{ID: "bk-1", Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "111111111", IsActive: true})
	repo.Put(Account{ID: "bk-2", Bank: BankBRI, NamaPemilik: "PT Grahita", Nomor: "222222222", IsActive: false})
	svc := NewService(repo)

	aktif, err := svc.Active(context.Background())
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if len(aktif) != 1 || aktif[0].ID != "bk-1" {
		t.Fatalf("hanya rekening aktif yang boleh tampil, dapat %+v", aktif)
	}

	semua, err := svc.List(context.Background(), superadmin())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(semua) != 2 {
		t.Fatalf("List harus memuat aktif & nonaktif, dapat %d", len(semua))
	}
}

func TestUpdateAktifNonaktifDanAudit(t *testing.T) {
	repo := NewMemory()
	repo.Put(Account{ID: "bk-1", Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "1234567890", IsActive: true})
	svc := NewService(repo)

	nonaktif := false
	saved, err := svc.Update(context.Background(), superadmin(), "bk-1", Patch{IsActive: &nonaktif})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if saved.IsActive {
		t.Error("rekening harus menjadi nonaktif")
	}
	aktif, _ := svc.Active(context.Background())
	if len(aktif) != 0 {
		t.Errorf("rekening nonaktif tidak boleh tampil, dapat %+v", aktif)
	}
	entries := repo.AuditEntries()
	if len(entries) != 1 || entries[0].Action != "update" || entries[0].EntityID != "bk-1" {
		t.Errorf("audit update tidak sesuai: %+v", entries)
	}
	if saved.IsActive {
		t.Error("audit after harus mencerminkan perubahan")
	}
}

func TestUpdateTidakDitemukanDanDuplikat(t *testing.T) {
	repo := NewMemory()
	repo.Put(Account{ID: "bk-1", Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "111111111", IsActive: true})
	repo.Put(Account{ID: "bk-2", Bank: BankBCA, NamaPemilik: "PT Grahita", Nomor: "222222222", IsActive: true})
	svc := NewService(repo)

	if _, err := svc.Update(context.Background(), superadmin(), "", Patch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("id kosong harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.Update(context.Background(), superadmin(), "tidak-ada", Patch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("id tak dikenal harus ErrNotFound, dapat %v", err)
	}
	nomor := "222222222"
	if _, err := svc.Update(context.Background(), superadmin(), "bk-1", Patch{Nomor: &nomor}); !errors.Is(err, ErrIdentityImmutable) {
		t.Errorf("nomor rekening historis tidak boleh diubah, dapat %v", err)
	}
}
