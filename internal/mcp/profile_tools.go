package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-core"
)

// registerProfileTools registers career profile read and mutation tools onto s.
func registerProfileTools(s *serverSDK.MCPServer, cfg ServerConfig) {
	s.AddTool(mcpSDK.NewTool("get_main_profile",
		mcpSDK.WithDescription("Retrieves the canonical main career profile snapshot (experiences, bullet facts, skills, education, certifications) for the authenticated user."),
	), withToolDeps(handleGetMainProfile(cfg), toolDepCheck{
		ok:  cfg.ProfileService != nil || cfg.ProfileStore != nil,
		msg: "profile service and store unavailable",
	}))

	s.AddTool(mcpSDK.NewTool("add_experience",
		mcpSDK.WithDescription("Adds a new employment work experience to the authenticated user's main career profile."),
		mcpSDK.WithString("company", mcpSDK.Required(), mcpSDK.Description("Name of employing organization")),
		mcpSDK.WithString("role_title", mcpSDK.Required(), mcpSDK.Description("Job title / role held")),
		mcpSDK.WithString("start_date", mcpSDK.Required(), mcpSDK.Description("Start date in ISO-8601 YYYY-MM-DD or YYYY-MM format")),
		mcpSDK.WithString("end_date", mcpSDK.Description("End date in ISO-8601 format (omit or null if current)")),
		mcpSDK.WithBoolean("is_current", mcpSDK.Description("Whether this is the candidate's current employment")),
		mcpSDK.WithString("location", mcpSDK.Description("Geographical location (e.g., 'San Francisco, CA' or 'Remote')")),
		mcpSDK.WithString("summary", mcpSDK.Description("Summary of core duties and responsibilities")),
	), withToolDeps(handleAddExperience(cfg), careerStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("add_achievement_bullet",
		mcpSDK.WithDescription("Appends a verifiable achievement or quantifiable bullet fact to an existing work experience, project, or education entity."),
		mcpSDK.WithString("owner_id", mcpSDK.Required(), mcpSDK.Description("ID of the parent entity (work_experience, project, or education)")),
		mcpSDK.WithString("owner_type", mcpSDK.Required(), mcpSDK.Description("Parent entity type: strictly 'work_experience', 'project', or 'education'")),
		mcpSDK.WithString("statement", mcpSDK.Required(), mcpSDK.Description("Verifiable factual achievement or metric statement")),
	), withToolDeps(handleAddAchievementBullet(cfg), careerStoreDep(cfg)))
}

// handleGetMainProfile handles tool 'get_main_profile', returning the authenticated user's profile snapshot.
func handleGetMainProfile(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, _ mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		var snapshot *core.MainProfileSnapshot
		var err error
		switch {
		case cfg.ProfileService != nil:
			snapshot, err = cfg.ProfileService.Snapshot(ctx, userID)
		default:
			snapshot, err = cfg.ProfileStore.Snapshot(ctx, userID)
		}

		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to get profile snapshot", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to retrieve main profile due to an internal store error"), nil
		}

		res, err := jsonToolResult(snapshot)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to marshal profile snapshot", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to serialize main profile snapshot"), nil
		}
		return res, nil
	}
}

// handleAddExperience handles tool 'add_experience', appending employment history to the main profile.
func handleAddExperience(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		args := parseArguments(req)
		company := optionalTrimmedString(args, "company")
		roleTitle := optionalTrimmedString(args, "role_title")
		startDateStr := optionalTrimmedString(args, "start_date")

		if company == "" || roleTitle == "" || startDateStr == "" {
			return mcpSDK.NewToolResultError("company, role_title, and start_date are required fields"), nil
		}

		startDate, err := core.ParseDate(startDateStr)
		if err != nil || startDate.IsZero() {
			return mcpSDK.NewToolResultError("valid start_date is required (format: YYYY-MM-DD)"), nil
		}

		exp := buildWorkExperience(userID, company, roleTitle, startDate, args)
		if err := cfg.CareerStore.CreateExperience(ctx, exp); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create experience", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create work experience due to an internal store error"), nil
		}

		return mcpSDK.NewToolResultText(fmt.Sprintf("Experience created successfully with ID: %s", exp.EffectiveShortID())), nil
	}
}

// buildWorkExperience constructs a populated WorkExperience model from parsed MCP tool arguments.
func buildWorkExperience(userID, company, roleTitle string, startDate core.Date, args map[string]any) *core.WorkExperience {
	var endDate *core.Date
	if ed, ok := args["end_date"].(string); ok && strings.TrimSpace(ed) != "" {
		if parsed, err := core.ParseDate(ed); err == nil && !parsed.IsZero() {
			endDate = &parsed
		}
	}

	var isCurrent bool
	if ic, ok := args["is_current"].(bool); ok {
		isCurrent = ic
	}

	var loc core.Location
	if l, ok := args["location"].(string); ok && l != "" {
		loc.Address = l
	}

	var summary string
	if s, ok := args["summary"].(string); ok {
		summary = s
	}

	return &core.WorkExperience{
		ID:            "exp-" + uuid.New().String(),
		UserID:        userID,
		Company:       company,
		RoleTitle:     roleTitle,
		StartDate:     startDate,
		EndDate:       endDate,
		IsCurrent:     isCurrent,
		Location:      loc,
		DutiesSummary: summary,
	}
}

// handleAddAchievementBullet handles tool 'add_achievement_bullet', appending a verifiable achievement fact.
func handleAddAchievementBullet(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		args := parseArguments(req)
		ownerID := optionalTrimmedString(args, "owner_id")
		if ownerID == "" {
			return mcpSDK.NewToolResultError("owner_id is required"), nil
		}

		ownerType := core.OwnerType(strings.ToLower(optionalTrimmedString(args, "owner_type")))
		if ownerType != core.OwnerTypeWorkExperience &&
			ownerType != core.OwnerTypeProject &&
			ownerType != core.OwnerTypeEducation {
			return mcpSDK.NewToolResultError("invalid owner_type: must be 'work_experience', 'project', or 'education'"), nil
		}

		statement := optionalTrimmedString(args, "statement")
		if statement == "" {
			return mcpSDK.NewToolResultError("statement cannot be empty"), nil
		}

		if !verifyFactOwner(ctx, cfg, userID, ownerID, ownerType) {
			return mcpSDK.NewToolResultError(fmt.Sprintf("%s with ID %q not found or access denied", ownerType, ownerID)), nil
		}

		fact := &core.Fact{
			ID:        "fact-" + uuid.New().String(),
			UserID:    userID,
			OwnerID:   ownerID,
			OwnerType: ownerType,
			Statement: statement,
		}

		if err := cfg.CareerStore.CreateFact(ctx, fact); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create achievement fact", "user_id", userID, "owner_id", ownerID, "err", err)
			return mcpSDK.NewToolResultError("failed to create achievement fact due to an internal store error"), nil
		}

		return mcpSDK.NewToolResultText(fmt.Sprintf("Achievement bullet created successfully with ID: %s", fact.EffectiveShortID())), nil
	}
}

// verifyFactOwner reports whether the target entity exists and belongs to userID.
func verifyFactOwner(ctx context.Context, cfg ServerConfig, userID, ownerID string, ownerType core.OwnerType) bool {
	switch ownerType {
	case core.OwnerTypeWorkExperience:
		exp, err := cfg.CareerStore.Experience(ctx, userID, ownerID)
		return err == nil && exp != nil && exp.UserID == userID
	case core.OwnerTypeProject:
		proj, err := cfg.CareerStore.Project(ctx, userID, ownerID)
		return err == nil && proj != nil && proj.UserID == userID
	case core.OwnerTypeEducation:
		edu, err := cfg.CareerStore.Education(ctx, userID, ownerID)
		return err == nil && edu != nil && edu.UserID == userID
	default:
		return false
	}
}
