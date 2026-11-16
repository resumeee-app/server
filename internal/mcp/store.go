package mcp

import (
	"context"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/seed"
	"github.com/resumeee-app/core/store"
	"github.com/resumeee-app/server/internal/access"
)

// ProfileStore defines tenant profile persistence operations required by MCP handlers and tenant provisioning.
type ProfileStore interface {
	// Profile retrieves the base profile metadata for userID.
	Profile(ctx context.Context, userID string) (*core.Profile, error)
	// UpsertProfile creates or updates a tenant profile record.
	UpsertProfile(ctx context.Context, profile *core.Profile) error
	// Snapshot returns the aggregated main career profile snapshot for userID.
	Snapshot(ctx context.Context, userID string) (*core.MainProfileSnapshot, error)
}

// CareerStore defines career entity and skill persistence operations consumed by MCP profile and skill tools.
type CareerStore interface {
	// ListSkills returns user-owned custom skills belonging to userID.
	ListSkills(ctx context.Context, userID string) ([]core.Skill, error)
	// ListGlobalSkills returns curated system-wide skills available to all tenants.
	ListGlobalSkills(ctx context.Context) ([]core.Skill, error)
	// Skill fetches a skill accessible to userID by ID.
	Skill(ctx context.Context, userID, id string) (*core.Skill, error)
	// CreateSkill persists a new custom skill record.
	CreateSkill(ctx context.Context, skill *core.Skill) error
	// UpdateSkill updates an existing tenant-owned skill record.
	UpdateSkill(ctx context.Context, skill *core.Skill) error
	// Experience fetches a single work experience belonging to userID by ID.
	Experience(ctx context.Context, userID, id string) (*core.WorkExperience, error)
	// CreateExperience persists a new work experience record.
	CreateExperience(ctx context.Context, exp *core.WorkExperience) error
	// Project fetches a single project belonging to userID by ID.
	Project(ctx context.Context, userID, id string) (*core.Project, error)
	// Education fetches a single education record belonging to userID by ID.
	Education(ctx context.Context, userID, id string) (*core.Education, error)
	// CreateFact persists a new verifiable achievement fact record.
	CreateFact(ctx context.Context, fact *core.Fact) error
}

// PresetStore defines tailoring preset persistence operations consumed by MCP preset tools and resources.
type PresetStore interface {
	// ListPresets returns system and tenant-owned presets visible to userID.
	ListPresets(ctx context.Context, userID string) ([]core.Preset, error)
	// Preset fetches a tailoring preset visible to userID by ID.
	Preset(ctx context.Context, userID, id string) (*core.Preset, error)
	// CreatePreset persists a new custom tenant preset.
	CreatePreset(ctx context.Context, preset *core.Preset) error
	// UpdatePreset updates an existing custom tenant preset.
	UpdatePreset(ctx context.Context, preset *core.Preset) error
}

// TemplateStore defines template set persistence operations consumed by MCP rendering, validation, and provisioning.
type TemplateStore interface {
	// Template fetches a template set visible to userID by ID.
	Template(ctx context.Context, userID, id string) (*core.TemplateSet, error)
	// CreateTemplate persists a new tenant template set.
	CreateTemplate(ctx context.Context, tmpl *core.TemplateSet) error
}

// BranchStore defines resume branch and version persistence operations consumed by MCP tools and resources.
type BranchStore interface {
	// ListBranches returns all resume branches belonging to userID.
	ListBranches(ctx context.Context, userID string) ([]core.Branch, error)
	// Branch fetches a single resume branch belonging to userID by ID.
	Branch(ctx context.Context, userID, id string) (*core.Branch, error)
	// CreateBranch persists a new resume branch.
	CreateBranch(ctx context.Context, branch *core.Branch) error
	// UpdateBranch updates an existing resume branch.
	UpdateBranch(ctx context.Context, branch *core.Branch) error
	// Version fetches a branch version by branchID and versionID.
	Version(ctx context.Context, branchID, versionID string) (*core.BranchVersion, error)
	// VersionByNumber fetches a branch version by branchID and sequential versionNumber.
	VersionByNumber(ctx context.Context, branchID string, versionNumber int) (*core.BranchVersion, error)
	// SaveCompletedVersion persists a completed branch version record.
	SaveCompletedVersion(ctx context.Context, version *core.BranchVersion) error
}

// UserStore defines authenticated tenant user persistence operations consumed by MCP lazy provisioning.
type UserStore interface {
	// User fetches a user record by ID.
	User(ctx context.Context, id string) (*core.User, error)
	// UpsertUser creates or updates a user record.
	UpsertUser(ctx context.Context, user *core.User) error
}

// Compile-time checks verify MCP consumer interfaces satisfy shared helper and seeder contracts
// and are implemented by core domain sub-stores.
var (
	_ seed.ProfileSeeder    = (ProfileStore)(nil)
	_ seed.BranchSeeder     = (BranchStore)(nil)
	_ seed.TemplateSeeder   = (TemplateStore)(nil)
	_ seed.UserSeeder       = (UserStore)(nil)
	_ access.PresetReader   = (PresetStore)(nil)
	_ access.TemplateReader = (TemplateStore)(nil)
	_ ProfileStore          = (*store.Profiles)(nil)
	_ CareerStore           = (*store.Career)(nil)
	_ PresetStore           = (*store.Presets)(nil)
	_ TemplateStore         = (*store.Templates)(nil)
	_ BranchStore           = (*store.Branches)(nil)
	_ UserStore             = (*store.Users)(nil)
)
