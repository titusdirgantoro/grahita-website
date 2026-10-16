package bank

import (
	"context"
	"errors"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
)

func superadminAllowlist() auth.Actor {
	return auth.Actor{UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
}

func TestAllowlistFailClosedDanIsolasiCabang(t *testing.T) {
	repo := NewMemory()
	repo.PutBranch("branch-a", true)
	repo.PutBranch("branch-b", true)
	repo.Put(Account{ID: "account-a", Bank: BankBCA, NamaPemilik: "PT A", Nomor: "1234567890", IsActive: true})
	repo.Put(Account{ID: "account-b", Bank: BankBRI, NamaPemilik: "PT B", Nomor: "0987654321", IsActive: true})
	repo.Assign("branch-a", "account-a")
	repo.Assign("branch-b", "account-b")
	svc := NewService(repo)

	a, err := svc.EffectiveForBranch(context.Background(), "branch-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Accounts) != 1 || a.Accounts[0].ID != "account-a" {
		t.Fatalf("branch A harus hanya melihat account-a: %+v", a.Accounts)
	}
	zero, err := svc.EffectiveForBranch(context.Background(), "branch-kosong")
	if !errors.Is(err, ErrBranchNotFound) || len(zero.Accounts) != 0 {
		t.Fatalf("branch tak dikenal harus not found, dapat %+v %v", zero, err)
	}
	repo.PutBranch("branch-kosong", true)
	zero, err = svc.EffectiveForBranch(context.Background(), "branch-kosong")
	if err != nil || len(zero.Accounts) != 0 {
		t.Fatalf("assignment nol harus slice kosong tanpa fallback: %+v %v", zero, err)
	}
}

func TestReplacementDormantVersiDanKonflik(t *testing.T) {
	repo := NewMemory()
	repo.PutBranch("branch-a", true)
	repo.Put(Account{ID: "aktif-a", Bank: BankBCA, NamaPemilik: "PT A", Nomor: "1111111111", IsActive: true})
	repo.Put(Account{ID: "dormant-b", Bank: BankBRI, NamaPemilik: "PT B", Nomor: "2222222222", IsActive: false})
	repo.Assign("branch-a", "dormant-b")
	svc := NewService(repo)

	out, err := svc.ReplaceAssignments(context.Background(), superadminAllowlist(), "branch-a", ReplaceInput{
		ExpectedVersion: 0, BankAccountIDs: []string{"dormant-b", "aktif-a"},
	})
	if err != nil {
		t.Fatalf("mempertahankan dormant sambil tambah aktif harus sah: %v", err)
	}
	if out.Version != 1 || !out.PaymentAvailable || !out.Changed {
		t.Fatalf("hasil replacement salah: %+v", out)
	}
	if _, err := svc.ReplaceAssignments(context.Background(), superadminAllowlist(), "branch-a", ReplaceInput{
		ExpectedVersion: 0, BankAccountIDs: []string{"aktif-a"},
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("versi basi harus konflik, dapat %v", err)
	}

	out, err = svc.ReplaceAssignments(context.Background(), superadminAllowlist(), "branch-a", ReplaceInput{
		ExpectedVersion: 1, BankAccountIDs: []string{"aktif-a"},
	})
	if err != nil || out.Version != 2 {
		t.Fatalf("cabut dormant harus naik versi: %+v %v", out, err)
	}
	if _, err := svc.ReplaceAssignments(context.Background(), superadminAllowlist(), "branch-a", ReplaceInput{
		ExpectedVersion: 2, BankAccountIDs: []string{"aktif-a", "dormant-b"},
	}); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("rekening nonaktif yang sudah dicabut tak boleh diaktifkan ulang: %v", err)
	}
}

func TestIdentitasRekeningImmutable(t *testing.T) {
	repo := NewMemory()
	repo.Put(Account{ID: "account-a", Bank: BankBCA, NamaPemilik: "PT A", Nomor: "1234567890", IsActive: true})
	svc := NewService(repo)
	other := BankBRI
	if _, err := svc.Update(context.Background(), superadminAllowlist(), "account-a", Patch{Bank: &other}); !errors.Is(err, ErrIdentityImmutable) {
		t.Fatalf("bank historis harus immutable: %v", err)
	}
	nomor := "9999999999"
	if _, err := svc.Update(context.Background(), superadminAllowlist(), "account-a", Patch{Nomor: &nomor}); !errors.Is(err, ErrIdentityImmutable) {
		t.Fatalf("nomor historis harus immutable: %v", err)
	}
}
