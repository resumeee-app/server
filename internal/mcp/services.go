package mcp

import (
	"context"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/branch"
	"github.com/resumeee-app/core/profile"
)

// ProfileService defines the main career profile operations consumed by MCP handlers.
type ProfileService interface {
	Snapshot(ctx context.Context, userID string) (*core.MainProfileSnapshot, error)
}

// BranchService defines the targeted resume branch operations consumed by MCP handlers.
type BranchService interface {
	CreateBranch(ctx context.Context, b *core.Branch) (*core.Branch, error)
	UpdateBranch(ctx context.Context, b *core.Branch) (*core.Branch, error)
	GenerateVersion(ctx context.Context, userID, branchID string) (*core.BranchVersion, error)
	AnalyzeGaps(ctx context.Context, userID, branchID string) (*core.GapReport, error)
	SimulateATS(ctx context.Context, userID, branchID string, opts *core.ATSSimulationOptions) (*core.ATSScore, error)
}

// Compile-time assertions verify domain service implementations satisfy consumer contracts.
var (
	_ ProfileService = (*profile.Service)(nil)
	_ BranchService  = (*branch.Service)(nil)
)
