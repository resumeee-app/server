package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-api/internal/access"
	"github.com/resumeee-app/resumeee-core"
)

// registerSkillTools registers skill catalog query and profile skill mutation tools onto s.
func registerSkillTools(s *serverSDK.MCPServer, cfg ServerConfig) {
	s.AddTool(mcpSDK.NewTool("list_skills",
		mcpSDK.WithDescription("Lists skills registered in the candidate career profile and global system catalog, with optional type filtering ('all', 'global', 'custom')."),
		mcpSDK.WithString("type", mcpSDK.Description("Filter by skill origin: 'all', 'global', or 'custom'")),
	), withToolDeps(handleListSkills(cfg), careerStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("add_skill",
		mcpSDK.WithDescription("Adds a custom skill to the authenticated candidate's career profile."),
		mcpSDK.WithString("name", mcpSDK.Required(), mcpSDK.Description("Name of the skill")),
		mcpSDK.WithString("category", mcpSDK.Description("Category classification (e.g. 'Languages', 'Cloud', 'Frameworks')")),
		mcpSDK.WithString("proficiency", mcpSDK.Description("Skill proficiency level (e.g. 'Familiar', 'Proficient', 'Expert')")),
		mcpSDK.WithNumber("years_experience", mcpSDK.Description("Number of years experience with this skill")),
		mcpSDK.WithString("keywords", mcpSDK.Description("Comma-separated list of searchable skill keywords")),
	), withToolDeps(handleAddSkill(cfg), careerStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("update_skill",
		mcpSDK.WithDescription("Updates properties of an existing custom skill in the authenticated candidate's profile."),
		mcpSDK.WithString("id", mcpSDK.Required(), mcpSDK.Description("ID of the target skill to update")),
		mcpSDK.WithString("name", mcpSDK.Description("Updated name of the skill")),
		mcpSDK.WithString("category", mcpSDK.Description("Updated category classification")),
		mcpSDK.WithString("proficiency", mcpSDK.Description("Updated skill proficiency level")),
		mcpSDK.WithNumber("years_experience", mcpSDK.Description("Updated number of years experience")),
		mcpSDK.WithString("keywords", mcpSDK.Description("Updated comma-separated list of searchable skill keywords")),
	), withToolDeps(handleUpdateSkill(cfg), careerStoreDep(cfg)))
}

// handleListSkills handles tool 'list_skills', returning skills for the user with optional origin filter.
func handleListSkills(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		args := parseArguments(req)
		skillType := strings.ToLower(optionalTrimmedString(args, "type"))

		var skills []core.Skill
		var err error
		switch skillType {
		case "global":
			skills, err = cfg.CareerStore.ListGlobalSkills(ctx)
		case "custom":
			all, lErr := cfg.CareerStore.ListSkills(ctx, userID)
			if lErr == nil {
				skills = access.FilterCustomSkills(all, userID)
			}
			err = lErr
		default:
			skills, err = cfg.CareerStore.ListSkills(ctx, userID)
		}

		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to list skills", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to list skills due to an internal store error"), nil
		}
		if skills == nil {
			skills = []core.Skill{}
		}

		return jsonToolResult(skills)
	}
}

// parseOptionalYearsExperience extracts and casts an optional years of experience value.
func parseOptionalYearsExperience(raw any) (*float64, bool) {
	if raw == nil {
		return nil, false
	}
	switch val := raw.(type) {
	case float64:
		return &val, true
	case int:
		f := float64(val)
		return &f, true
	case string:
		if trimmed := strings.TrimSpace(val); trimmed != "" {
			if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
				return &parsed, true
			}
		}
	}
	return nil, false
}

// parseOptionalKeywords extracts and normalizes an optional slice of string keywords.
func parseOptionalKeywords(raw any) ([]string, bool) {
	if raw == nil {
		return nil, false
	}
	switch val := raw.(type) {
	case string:
		var keywords []string
		for _, p := range strings.Split(val, ",") {
			trimmed := strings.TrimSpace(p)
			if trimmed != "" {
				keywords = append(keywords, trimmed)
			}
		}
		return keywords, true
	case []any:
		var keywords []string
		for _, item := range val {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				keywords = append(keywords, strings.TrimSpace(s))
			}
		}
		return keywords, true
	case []string:
		var keywords []string
		for _, s := range val {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				keywords = append(keywords, trimmed)
			}
		}
		return keywords, true
	}
	return nil, false
}

// handleAddSkill handles tool 'add_skill', persisting a new custom skill for the user profile.
func handleAddSkill(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		args := parseArguments(req)
		name := optionalTrimmedString(args, "name")
		if name == "" {
			return mcpSDK.NewToolResultError("name is required"), nil
		}

		var years float64
		if yPtr, ok := parseOptionalYearsExperience(args["years_experience"]); ok && yPtr != nil {
			years = *yPtr
		}

		var keywords []string
		if kw, ok := parseOptionalKeywords(args["keywords"]); ok {
			keywords = kw
		}

		skill := core.Skill{
			ID:              core.GenerateID("skill"),
			ShortID:         core.GenerateShortID("skill"),
			UserID:          userID,
			Name:            name,
			Category:        optionalTrimmedString(args, "category"),
			Proficiency:     optionalTrimmedString(args, "proficiency"),
			YearsExperience: years,
			Keywords:        keywords,
		}

		if err := cfg.CareerStore.CreateSkill(ctx, &skill); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create skill", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create skill due to an internal store error"), nil
		}

		return jsonToolResult(skill)
	}
}

// handleUpdateSkill handles tool 'update_skill', updating properties of an existing custom skill.
func handleUpdateSkill(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		args := parseArguments(req)
		id := optionalTrimmedString(args, "id")
		if id == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		skill, err := cfg.CareerStore.Skill(ctx, userID, id)
		if err != nil || skill == nil || skill.UserID != userID {
			return mcpSDK.NewToolResultError(fmt.Sprintf("skill with ID %q not found or access denied", id)), nil
		}

		if name, ok := args["name"].(string); ok && strings.TrimSpace(name) != "" {
			skill.Name = strings.TrimSpace(name)
		}
		if cat, ok := args["category"].(string); ok {
			skill.Category = strings.TrimSpace(cat)
		}
		if prof, ok := args["proficiency"].(string); ok {
			skill.Proficiency = strings.TrimSpace(prof)
		}
		if yPtr, ok := parseOptionalYearsExperience(args["years_experience"]); ok && yPtr != nil {
			skill.YearsExperience = *yPtr
		}
		if kw, ok := parseOptionalKeywords(args["keywords"]); ok {
			skill.Keywords = kw
		}

		if err := cfg.CareerStore.UpdateSkill(ctx, skill); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to update skill", "id", id, "err", err)
			return mcpSDK.NewToolResultError("failed to update skill due to an internal store error"), nil
		}

		return jsonToolResult(skill)
	}
}
