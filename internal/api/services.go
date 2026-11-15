package api

import (
	"context"

	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
	"github.com/resumeee-app/resumeee-core/profile"
)

// BranchService defines the branch and resume lifecycle contract consumed by HTTP handlers.
type BranchService interface {
	ListBranches(ctx context.Context, userID string, staleOnly bool) ([]core.Branch, error)
	Branch(ctx context.Context, userID, id string) (*core.Branch, error)
	CreateBranch(ctx context.Context, b *core.Branch) (*core.Branch, error)
	UpdateBranch(ctx context.Context, b *core.Branch) (*core.Branch, error)
	DeleteBranch(ctx context.Context, userID, id string) error
	SyncBranches(ctx context.Context, userID string, branchIDs []string) ([]string, error)
	GenerateVersion(ctx context.Context, userID, branchID string) (*core.BranchVersion, error)
	ListVersions(ctx context.Context, branchID string) ([]core.BranchVersion, error)
	Version(ctx context.Context, branchID, versionNumOrID string) (*core.BranchVersion, error)
	EditVersion(ctx context.Context, userID, branchID, parentVersionNumOrID string, editedContent core.TailoredContent) (*core.BranchVersion, error)
	AnalyzeGaps(ctx context.Context, userID, branchID string) (*core.GapReport, error)
	SimulateATS(ctx context.Context, userID, branchID string, opts *core.ATSSimulationOptions) (*core.ATSScore, error)
	PromptPreview(ctx context.Context, userID, branchID string, opts *core.PromptPreviewOptions) (*core.PromptPreview, error)
}

// ProfileService defines the JSON Resume import and export contract consumed by HTTP handlers.
type ProfileService interface {
	ImportJSONResume(ctx context.Context, userID string, payload []byte) (*core.MainProfileSnapshot, error)
	ExportJSONResume(ctx context.Context, userID string, versionID *string) (*core.JSONResume, error)
}

// Compile-time assertions verify domain service implementations satisfy consumer contracts.
var (
	_ BranchService  = (*branch.Service)(nil)
	_ ProfileService = (*profile.Service)(nil)
)
