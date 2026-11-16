package api

import (
	"context"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/branch"
	"github.com/resumeee-app/core/profile"
	"github.com/resumeee-app/core/store"
	"github.com/resumeee-app/core/worker"
)

// ProfileStore defines tenant profile persistence operations required by API handlers,
// the profile service, and branch operations.
type ProfileStore interface {
	profile.Store
}

// CareerStore defines CRUD persistence operations for career entities belonging to a tenant.
type CareerStore interface {
	// ListSkills returns custom skills belonging to userID.
	ListSkills(ctx context.Context, userID string) ([]core.Skill, error)
	// ListGlobalSkills returns curated system-wide skills available to all tenants.
	ListGlobalSkills(ctx context.Context) ([]core.Skill, error)
	// Skill fetches a skill accessible to userID by ID.
	Skill(ctx context.Context, userID, id string) (*core.Skill, error)
	// CreateSkill persists a new skill record.
	CreateSkill(ctx context.Context, skill *core.Skill) error
	// UpdateSkill updates an existing tenant-owned skill record.
	UpdateSkill(ctx context.Context, skill *core.Skill) error
	// DeleteSkill deletes a tenant-owned skill by ID.
	DeleteSkill(ctx context.Context, userID, id string) error

	// ListExperiences returns all work experiences belonging to userID.
	ListExperiences(ctx context.Context, userID string) ([]core.WorkExperience, error)
	// Experience fetches a single work experience belonging to userID by ID.
	Experience(ctx context.Context, userID, id string) (*core.WorkExperience, error)
	// CreateExperience persists a new work experience record.
	CreateExperience(ctx context.Context, exp *core.WorkExperience) error
	// UpdateExperience updates an existing work experience record.
	UpdateExperience(ctx context.Context, exp *core.WorkExperience) error
	// DeleteExperience deletes a work experience belonging to userID by ID.
	DeleteExperience(ctx context.Context, userID, id string) error

	// ListProjects returns all projects belonging to userID.
	ListProjects(ctx context.Context, userID string) ([]core.Project, error)
	// Project fetches a single project belonging to userID by ID.
	Project(ctx context.Context, userID, id string) (*core.Project, error)
	// CreateProject persists a new project record.
	CreateProject(ctx context.Context, proj *core.Project) error
	// UpdateProject updates an existing project record.
	UpdateProject(ctx context.Context, proj *core.Project) error
	// DeleteProject deletes a project belonging to userID by ID.
	DeleteProject(ctx context.Context, userID, id string) error

	// ListEducation returns all education records belonging to userID.
	ListEducation(ctx context.Context, userID string) ([]core.Education, error)
	// Education fetches a single education record belonging to userID by ID.
	Education(ctx context.Context, userID, id string) (*core.Education, error)
	// CreateEducation persists a new education record.
	CreateEducation(ctx context.Context, edu *core.Education) error
	// UpdateEducation updates an existing education record.
	UpdateEducation(ctx context.Context, edu *core.Education) error
	// DeleteEducation deletes an education record belonging to userID by ID.
	DeleteEducation(ctx context.Context, userID, id string) error

	// ListCertifications returns all certifications belonging to userID.
	ListCertifications(ctx context.Context, userID string) ([]core.Certification, error)
	// Certification fetches a single certification belonging to userID by ID.
	Certification(ctx context.Context, userID, id string) (*core.Certification, error)
	// CreateCertification persists a new certification record.
	CreateCertification(ctx context.Context, cert *core.Certification) error
	// UpdateCertification updates an existing certification record.
	UpdateCertification(ctx context.Context, cert *core.Certification) error
	// DeleteCertification deletes a certification belonging to userID by ID.
	DeleteCertification(ctx context.Context, userID, id string) error

	// ListFactsByOwner returns facts owned by userID associated with ownerType and ownerID.
	ListFactsByOwner(ctx context.Context, userID, ownerType, ownerID string) ([]core.Fact, error)
	// Fact fetches a single fact belonging to userID by ID.
	Fact(ctx context.Context, userID, id string) (*core.Fact, error)
	// CreateFact persists a new fact record.
	CreateFact(ctx context.Context, fact *core.Fact) error
	// UpdateFact updates an existing fact record.
	UpdateFact(ctx context.Context, fact *core.Fact) error
	// DeleteFact deletes a fact belonging to userID by ID.
	DeleteFact(ctx context.Context, userID, id string) error
}

// PresetStore defines tailoring preset persistence operations required by API handlers and branch generation.
type PresetStore interface {
	// ListPresets returns system and tenant-owned presets visible to userID.
	ListPresets(ctx context.Context, userID string) ([]core.Preset, error)
	// Preset fetches a preset visible to userID by ID.
	Preset(ctx context.Context, userID, id string) (*core.Preset, error)
	// CreatePreset persists a new tenant preset.
	CreatePreset(ctx context.Context, preset *core.Preset) error
	// UpdatePreset updates an existing tenant-owned preset.
	UpdatePreset(ctx context.Context, preset *core.Preset) error
	// DeletePreset deletes a tenant-owned preset by ID.
	DeletePreset(ctx context.Context, userID, id string) error
}

// TemplateStore defines template set persistence operations required by API handlers and branch rendering.
type TemplateStore interface {
	// ListTemplates returns system and tenant-owned template sets visible to userID.
	ListTemplates(ctx context.Context, userID string) ([]core.TemplateSet, error)
	// Template fetches a template set visible to userID by ID.
	Template(ctx context.Context, userID, id string) (*core.TemplateSet, error)
	// CreateTemplate persists a new tenant template set.
	CreateTemplate(ctx context.Context, tmpl *core.TemplateSet) error
	// UpdateTemplate updates an existing tenant-owned template set.
	UpdateTemplate(ctx context.Context, tmpl *core.TemplateSet) error
	// DeleteTemplate deletes a tenant-owned template set by ID.
	DeleteTemplate(ctx context.Context, userID, id string) error
}

// BranchStore defines branch and branch version persistence operations.
type BranchStore interface {
	branch.Store
}

// JobStore defines persistent background job submission, status lookup, and cancellation operations.
type JobStore interface {
	branch.JobStore
	worker.JobStore
}

// QuotaStore defines LLM quota inspection, reservation, commit, and release operations.
type QuotaStore interface {
	// Quota returns the tenant's quota snapshot, initializing default limits when needed.
	Quota(ctx context.Context, userID string) (*core.TenantQuota, error)
	// ReserveQuota reserves estimated LLM tokens and request capacity prior to enqueuing work.
	ReserveQuota(ctx context.Context, userID string, operation core.LLMOperationType, estimatedTokens, dailyRequestLimit, monthlyTokenLimit int) (*core.LLMUsageRecord, error)
	// CommitUsage finalizes a quota reservation with observed LLM telemetry.
	CommitUsage(ctx context.Context, reservationID string, telemetry core.LLMTelemetry) error
	// ReleaseQuota releases a failed or cancelled quota reservation and records failure telemetry.
	ReleaseQuota(ctx context.Context, reservationID, errMsg string, telemetry core.LLMTelemetry) error
}

// UserStore defines authenticated tenant user persistence operations.
type UserStore interface {
	// User fetches a user record by ID.
	User(ctx context.Context, id string) (*core.User, error)
	// UpsertUser creates or updates a user record.
	UpsertUser(ctx context.Context, user *core.User) error
}

// Compile-time assertions verify core sub-store implementations satisfy API consumer store contracts.
var (
	_ ProfileStore  = (*store.Profiles)(nil)
	_ CareerStore   = (*store.Career)(nil)
	_ PresetStore   = (*store.Presets)(nil)
	_ TemplateStore = (*store.Templates)(nil)
	_ BranchStore   = (*store.Branches)(nil)
	_ JobStore      = (*store.Jobs)(nil)
	_ QuotaStore    = (*store.Quotas)(nil)
	_ UserStore     = (*store.Users)(nil)
)
