// Package masterimport menangani preview, apply idempoten, dan rollback staging
// master Impact. Preview boleh berjalan di production, sedangkan apply dan
// rollback production selalu ditolak fail-closed.
package masterimport

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/grahita/portal/api/internal/auth"
)

const (
	SourceSystem    = "impact"
	ContractVersion = "impact-master-v1"
	// ProductionPreviewEnvironment adalah provenance khusus preview production.
	// Nilai ini tetap tidak membuka environment "production" untuk apply.
	ProductionPreviewEnvironment = "production-preview"
	SheetName                    = "Master Tarif (Opsi A)"
	ExpectedRows                 = 34
	ExpectedHash                 = "eaf02f20e86b7383bf06342bd26d44e1fc4c714ab59b34657dfc9e0c3c0f91f7"
	MaxWorkbookSize              = 5 << 20
)

var (
	ErrForbidden        = errors.New("master import hanya untuk superadmin")
	ErrProduction       = errors.New("apply dan rollback master import dilarang di production")
	ErrInvalidWorkbook  = errors.New("workbook Impact tidak valid")
	ErrHashMismatch     = errors.New("hash artefak tidak diotorisasi")
	ErrStructure        = errors.New("struktur workbook berubah")
	ErrInvalidValue     = errors.New("nilai workbook tidak valid")
	ErrTooLarge         = errors.New("workbook melebihi batas ukuran")
	ErrAttemptNotFound  = errors.New("attempt impor tidak ditemukan")
	ErrApplyBlocked     = errors.New("apply impor masih mempunyai conflict atau keputusan yang belum sah")
	ErrPreviewStale     = errors.New("preview impor kedaluwarsa karena master berubah")
	ErrAlreadyApplied   = errors.New("attempt impor sudah diterapkan dengan idempotency key berbeda")
	ErrRollbackConflict = errors.New("rollback ditolak karena data berubah sesudah apply")
)

type Status string

const (
	StatusInsert      Status = "insert"
	StatusUpdate      Status = "update"
	StatusSkip        Status = "skip"
	StatusConflict    Status = "conflict"
	StatusNeedsReview Status = "needs_review"
)

type EntityKind string

const (
	EntityBranch  EntityKind = "branch"
	EntityProduct EntityKind = "product"
	EntityVariant EntityKind = "variant"
	EntityTariff  EntityKind = "tariff"
	EntityNote    EntityKind = "note"
)

type Count struct {
	Insert      int `json:"insert"`
	Update      int `json:"update"`
	Skip        int `json:"skip"`
	Conflict    int `json:"conflict"`
	NeedsReview int `json:"needs_review"`
}

type Counts struct {
	Branch  Count `json:"branch"`
	Product Count `json:"product"`
	Variant Count `json:"variant"`
	Tariff  Count `json:"tariff"`
	Note    Count `json:"note"`
	Total   Count `json:"total"`
}

type PreviewItem struct {
	SourceRow           int        `json:"source_row"`
	EntityKind          EntityKind `json:"entity_kind"`
	EntityCode          string     `json:"entity_code"`
	Fingerprint         string     `json:"fingerprint"`
	Status              Status     `json:"status"`
	ReasonCode          string     `json:"reason_code,omitempty"`
	ExistingFingerprint string     `json:"-"`
	PrivateEvidence     string     `json:"-"`
}

type Preview struct {
	AttemptID       string        `json:"attempt_id"`
	ArtifactID      string        `json:"artifact_id"`
	SourceHash      string        `json:"source_hash"`
	PreviewHash     string        `json:"preview_hash"`
	ContractVersion string        `json:"contract_version"`
	Sheet           string        `json:"sheet"`
	Counts          Counts        `json:"counts"`
	Items           []PreviewItem `json:"items"`
	CreatedAt       time.Time     `json:"created_at"`
}

type MoneyComponent struct {
	Available   bool   `json:"available"`
	HJM         int64  `json:"hjm"`
	P1          string `json:"p1"`
	SPV         int64  `json:"spv"`
	FinalItem   int64  `json:"final_item"`
	Fingerprint string `json:"fingerprint"`
}

type ProductDefinition struct {
	Code string
	Name string
}

type VariantDefinition struct {
	Code        string
	ProductCode string
	ParentCode  string
	Name        string
}

type SourceBranch struct {
	Row         int                       `json:"row"`
	SourceKey   string                    `json:"source_key"`
	Fingerprint string                    `json:"fingerprint"`
	Name        string                    `json:"name"`
	Position    string                    `json:"position"`
	HeadName    *string                   `json:"head_name"`
	Tariffs     map[string]MoneyComponent `json:"tariffs"`
	ReviewCells []ReviewCell              `json:"review_cells"`
}

type ReviewCell struct {
	Column      string `json:"column"`
	Fingerprint string `json:"fingerprint"`
	Value       string `json:"value"`
}

type ParsedWorkbook struct {
	SourceHash string         `json:"source_hash"`
	Branches   []SourceBranch `json:"branches"`
}

type BranchState struct {
	ID               string
	SourceKey        string
	Name             string
	Position         string
	HeadName         *string
	EmailPresent     bool
	IdentityStatus   string
	AccessUsable     bool
	IdentityComplete bool
	AccessConsistent bool
	MappingConflict  bool
	Ambiguous        bool
	Fingerprint      string
}

type ProductState struct {
	ID          string
	Code        string
	Name        string
	Active      bool
	Fingerprint string
}

type VariantState struct {
	ID          string
	Code        string
	ProductCode string
	ParentCode  string
	Active      bool
	Fingerprint string
}

type TariffState struct {
	BranchID          string
	VariantCode       string
	Available         bool
	ComponentHash     string
	VersionID         string
	ProjectionPresent bool
	ProjectionPrice   int64
	Fingerprint       string
}

type Snapshot struct {
	Branches         map[string]BranchState
	BranchCandidates map[string][]BranchState
	Products         map[string]ProductState
	Variants         map[string]VariantState
	Tariffs          map[string]TariffState
}

type SavePreviewInput struct {
	Environment string
	ActorID     string
	SourceHash  string
	PreviewHash string
	Items       []PreviewItem
	Counts      Counts
	Parsed      ParsedWorkbook
}

type SavedPreview struct {
	AttemptID  string
	ArtifactID string
	CreatedAt  time.Time
}

type EvidenceItem struct {
	SourceRow  int        `json:"source_row"`
	EntityKind EntityKind `json:"entity_kind"`
	EntityCode string     `json:"entity_code"`
	Value      string     `json:"value"`
}

type Decision struct {
	SourceRow  int        `json:"source_row"`
	EntityKind EntityKind `json:"entity_kind"`
	EntityCode string     `json:"entity_code"`
	Decision   string     `json:"decision"`
	Reason     string     `json:"reason"`
}

type ApplyRequest struct {
	AttemptID      string     `json:"attempt_id"`
	PreviewHash    string     `json:"preview_hash"`
	IdempotencyKey string     `json:"idempotency_key"`
	Decisions      []Decision `json:"decisions"`
}

type MutationCounts struct {
	Branch  int `json:"branch"`
	Product int `json:"product"`
	Variant int `json:"variant"`
	Tariff  int `json:"tariff"`
	Revoked int `json:"identity_revocation"`
}

type ApplyResult struct {
	ApplyRunID   string         `json:"apply_run_id"`
	AttemptID    string         `json:"attempt_id"`
	Status       string         `json:"status"`
	Mutations    MutationCounts `json:"mutations"`
	Idempotent   bool           `json:"idempotent"`
	AppliedAt    time.Time      `json:"applied_at"`
	RolledBackAt *time.Time     `json:"rolled_back_at,omitempty"`
}

type RollbackResult struct {
	ApplyRunID   string    `json:"apply_run_id"`
	Status       string    `json:"status"`
	Restored     int       `json:"restored"`
	Irreversible int       `json:"identity_revocation_not_reversed"`
	RolledBackAt time.Time `json:"rolled_back_at"`
}

type Repository interface {
	Snapshot(context.Context) (Snapshot, error)
	SavePreview(context.Context, SavePreviewInput) (SavedPreview, error)
	LoadEvidence(context.Context, string) ([]EvidenceItem, error)
	Apply(context.Context, auth.Actor, ApplyRequest) (ApplyResult, error)
	Rollback(context.Context, auth.Actor, string) (RollbackResult, error)
}

type Parser interface {
	Parse(io.Reader, int64) (ParsedWorkbook, error)
}

type Service struct {
	repo        Repository
	parser      Parser
	environment string
}

func NewService(repo Repository, parser Parser, environment string) *Service {
	return &Service{repo: repo, parser: parser, environment: environment}
}

func (s *Service) Preview(ctx context.Context, actor auth.Actor, r io.Reader, size int64) (Preview, error) {
	return s.preview(ctx, actor, r, size)
}

func (s *Service) Evidence(ctx context.Context, actor auth.Actor, attemptID string) ([]EvidenceItem, error) {
	if !actor.IsSuperadmin() {
		return nil, ErrForbidden
	}
	return s.repo.LoadEvidence(ctx, attemptID)
}
