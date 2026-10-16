package payment

import (
	"context"
	"errors"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/settings"
)

type reevaluateRepoFake struct {
	result ReevaluateResult
	err    error
	calls  int
}

func (r *reevaluateRepoFake) EnqueueDestinationReevaluation(context.Context, string) (ReevaluateResult, error) {
	r.calls++
	return r.result, r.err
}

type reevaluateAIFake struct {
	settings settings.AISettings
	err      error
}

func (a reevaluateAIFake) AISettings(context.Context) (settings.AISettings, error) {
	return a.settings, a.err
}

func TestReevaluateServiceGuardDanIdempotensi(t *testing.T) {
	manager := auth.Actor{UserID: "00000000-0000-0000-0000-000000000001", Kind: auth.KindAdmin, Role: auth.RoleManager}
	branch := auth.Actor{UserID: "00000000-0000-0000-0000-000000000002", Kind: auth.KindBranch}
	const orderID = "00000000-0000-0000-0000-000000000010"

	t.Run("manager boleh dan outcome duplicate diteruskan", func(t *testing.T) {
		repo := &reevaluateRepoFake{result: ReevaluateResult{Duplicate: true}}
		svc := NewReevaluateService(repo, reevaluateAIFake{settings: settings.AISettings{Aktif: true}})
		got, err := svc.Enqueue(context.Background(), manager, orderID)
		if err != nil || !got.Duplicate || got.Queued || repo.calls != 1 {
			t.Fatalf("outcome tidak sesuai: %+v err=%v calls=%d", got, err, repo.calls)
		}
	})

	t.Run("ai mati fail closed sebelum repository", func(t *testing.T) {
		repo := &reevaluateRepoFake{}
		svc := NewReevaluateService(repo, reevaluateAIFake{})
		_, err := svc.Enqueue(context.Background(), manager, orderID)
		if !errors.Is(err, ErrAIDisabled) || repo.calls != 0 {
			t.Fatalf("harus AI_DISABLED tanpa enqueue, err=%v calls=%d", err, repo.calls)
		}
	})

	t.Run("akun cabang ditolak", func(t *testing.T) {
		repo := &reevaluateRepoFake{}
		svc := NewReevaluateService(repo, reevaluateAIFake{settings: settings.AISettings{Aktif: true}})
		_, err := svc.Enqueue(context.Background(), branch, orderID)
		if !errors.Is(err, ErrForbidden) || repo.calls != 0 {
			t.Fatalf("harus forbidden tanpa enqueue, err=%v calls=%d", err, repo.calls)
		}
	})
}
