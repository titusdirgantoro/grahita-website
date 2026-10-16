package payment

import (
	"context"
	"errors"
	"strings"

	"github.com/grahita/portal/api/internal/auth"
)

var (
	ErrAIDisabled           = errors.New("penilaian AI sedang nonaktif")
	ErrOCRResultMissing     = errors.New("hasil OCR belum tersedia untuk seluruh bukti")
	ErrPaymentProofRequired = errors.New("pesanan belum mempunyai bukti pembayaran")
)

type ReevaluateResult struct {
	Queued    bool `json:"queued"`
	Duplicate bool `json:"duplicate"`
}

type ReevaluateRepository interface {
	EnqueueDestinationReevaluation(ctx context.Context, orderID string) (ReevaluateResult, error)
}

type ReevaluateService struct {
	repo ReevaluateRepository
	ai   AISettingsSource
}

func NewReevaluateService(repo ReevaluateRepository, ai AISettingsSource) *ReevaluateService {
	return &ReevaluateService{repo: repo, ai: ai}
}

func (s *ReevaluateService) Enqueue(ctx context.Context, actor auth.Actor, orderID string) (ReevaluateResult, error) {
	if !actor.Can(auth.CapAdminVerify) {
		return ReevaluateResult{}, ErrForbidden
	}
	orderID = strings.TrimSpace(orderID)
	if !uuidRe.MatchString(orderID) {
		return ReevaluateResult{}, ErrNotFound
	}
	if s == nil || s.repo == nil || s.ai == nil {
		return ReevaluateResult{}, ErrQueueUnavailable
	}
	rules, err := s.ai.AISettings(ctx)
	if err != nil {
		return ReevaluateResult{}, err
	}
	if !rules.Aktif {
		return ReevaluateResult{}, ErrAIDisabled
	}
	return s.repo.EnqueueDestinationReevaluation(ctx, orderID)
}
