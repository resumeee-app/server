package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-core/domain"
)

var (
	// scriptTagRegex matches paired HTML <script>...</script> blocks across multiple lines.
	scriptTagRegex = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	// scriptTagSelfClosing matches standalone or unclosed opening <script ...> tags.
	scriptTagSelfClosing = regexp.MustCompile(`(?is)<script\b[^>]*\/?>`)
)

// StripScriptTags removes HTML script tags (both paired blocks and standalone unclosed tags) from rendered HTML.
//
// Parameters:
//   - html: Raw HTML string potentially containing script elements.
//
// Returns:
//   - Sanitized HTML string with all script tags stripped.
func StripScriptTags(html string) string {
	return stripScriptTags(html)
}

// stripScriptTags performs regex substitution to remove paired and standalone script tags.
func stripScriptTags(html string) string {
	cleaned := scriptTagRegex.ReplaceAllString(html, "")
	return scriptTagSelfClosing.ReplaceAllString(cleaned, "")
}

// SanitizeXMLFence neutralizes closing and parent prompt tags and wraps content in XML boundary tags.
//
// Parameters:
//   - content: Untrusted client or external job description text.
//   - tagName: Target enclosing XML tag name (e.g., 'target_job_description').
//
// Returns:
//   - Escaped content enclosed within opening and closing XML tags.
func SanitizeXMLFence(content, tagName string) string {
	return sanitizeXMLFence(content, tagName)
}

// sanitizeXMLFence applies regex matching against tag variations, whitespace, attributes, and parent enclosure tags.
func sanitizeXMLFence(content, tagName string) string {
	tagRegex := regexp.MustCompile(fmt.Sprintf(`(?i)</?\s*(?:%s|rules_hierarchy|tier_1_global_rules|tier_2_strategy_instructions|tier_3_branch_directives|main_career_profile|target_job_description|custom_instructions|resume_document|extracted_candidate_data|ats_extraction_rules|ats_matching_rules|response_schema)\b[^>]*>`, regexp.QuoteMeta(tagName)))
	safe := tagRegex.ReplaceAllStringFunc(content, func(m string) string {
		escaped := strings.ReplaceAll(m, "<", "&lt;")
		return strings.ReplaceAll(escaped, ">", "&gt;")
	})
	return fmt.Sprintf("<%s>\n%s\n</%s>", tagName, strings.TrimSpace(safe), tagName)
}

// parseArguments extracts and casts the tool arguments map from a CallToolRequest.
func parseArguments(req mcpSDK.CallToolRequest) map[string]any {
	if m, ok := req.Params.Arguments.(map[string]any); ok && m != nil {
		return m
	}
	return make(map[string]any)
}

// jsonToolResult marshals any payload value into an indented JSON tool text result.
func jsonToolResult(v any) (*mcpSDK.CallToolResult, error) {
	bytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return mcpSDK.NewToolResultText(string(bytes)), nil
}

// extractFormatOutput maps canonical domain.OutputFormat constants to the appropriate output string.
func extractFormatOutput(format domain.OutputFormat, html, md, text string) string {
	switch format {
	case domain.OutputFormatHTML:
		return html
	case domain.OutputFormatMarkdown:
		return md
	case domain.OutputFormatPlainText:
		return text
	default:
		return ""
	}
}

// registerTools registers all 8 domain tools onto the provided MCPServer instance.
//
// Parameters:
//   - s: Pointer to the MCPServer instance.
//   - cfg: ServerConfig providing dependencies, repositories, and timeouts.
func registerTools(s *serverSDK.MCPServer, cfg ServerConfig) {
	// 1. get_main_profile
	s.AddTool(mcpSDK.NewTool("get_main_profile",
		mcpSDK.WithDescription("Retrieves the canonical main career profile snapshot (experiences, bullet facts, skills, education, certifications) for the authenticated user."),
	), handleGetMainProfile(cfg))

	// 2. list_branches
	s.AddTool(mcpSDK.NewTool("list_branches",
		mcpSDK.WithDescription("Lists all tailored resume branches and their active version status for the authenticated user."),
	), handleListBranches(cfg))

	// 3. render_resume
	s.AddTool(mcpSDK.NewTool("render_resume",
		mcpSDK.WithDescription("Renders a resume branch version into HTML, Markdown, or plain text using the specified template set."),
		mcpSDK.WithString("branch_id", mcpSDK.Required(), mcpSDK.Description("ID of the target resume branch")),
		mcpSDK.WithString("format", mcpSDK.Required(), mcpSDK.Description("Target format: 'html', 'markdown', or 'text'")),
		mcpSDK.WithInteger("version_number", mcpSDK.Description("Sequential version number (e.g., 1, 2). If omitted, renders active version.")),
		mcpSDK.WithString("template_set_id", mcpSDK.Description("Optional template set ID to override branch/version template")),
		mcpSDK.WithString("template_id", mcpSDK.Description("Alias for template_set_id")),
	), handleRenderResume(cfg))

	// 4. add_experience
	s.AddTool(mcpSDK.NewTool("add_experience",
		mcpSDK.WithDescription("Adds a new employment work experience to the authenticated user's main career profile."),
		mcpSDK.WithString("company", mcpSDK.Required(), mcpSDK.Description("Name of employing organization")),
		mcpSDK.WithString("role_title", mcpSDK.Required(), mcpSDK.Description("Job title / role held")),
		mcpSDK.WithString("start_date", mcpSDK.Required(), mcpSDK.Description("Start date in ISO-8601 YYYY-MM-DD or YYYY-MM format")),
		mcpSDK.WithString("end_date", mcpSDK.Description("End date in ISO-8601 format (omit or null if current)")),
		mcpSDK.WithBoolean("is_current", mcpSDK.Description("Whether this is the candidate's current employment")),
		mcpSDK.WithString("location", mcpSDK.Description("Geographical location (e.g., 'San Francisco, CA' or 'Remote')")),
		mcpSDK.WithString("summary", mcpSDK.Description("Summary of core duties and responsibilities")),
	), handleAddExperience(cfg))

	// 5. add_achievement_bullet
	s.AddTool(mcpSDK.NewTool("add_achievement_bullet",
		mcpSDK.WithDescription("Appends a verifiable achievement or quantifiable bullet fact to an existing work experience, project, or education entity."),
		mcpSDK.WithString("owner_id", mcpSDK.Required(), mcpSDK.Description("ID of the parent entity (work_experience, project, or education)")),
		mcpSDK.WithString("owner_type", mcpSDK.Required(), mcpSDK.Description("Parent entity type: strictly 'work_experience', 'project', or 'education'")),
		mcpSDK.WithString("statement", mcpSDK.Required(), mcpSDK.Description("Verifiable factual achievement or metric statement")),
	), handleAddAchievementBullet(cfg))

	// 6. create_branch
	s.AddTool(mcpSDK.NewTool("create_branch",
		mcpSDK.WithDescription("Creates a new targeted resume branch tailored for a specific company, role, tailoring strategy, and template. Presets govern target language (EffectiveLanguage) and prompt instructions."),
		mcpSDK.WithString("name", mcpSDK.Required(), mcpSDK.Description("Descriptive name for the resume branch")),
		mcpSDK.WithString("target_company", mcpSDK.Required(), mcpSDK.Description("Name of the prospective employer")),
		mcpSDK.WithString("target_role", mcpSDK.Required(), mcpSDK.Description("Target job title")),
		mcpSDK.WithString("target_requirements", mcpSDK.Description("Target job description requirements or posting text")),
		mcpSDK.WithString("tailoring_strategy_id", mcpSDK.Description("Optional preset / tailoring strategy ID")),
		mcpSDK.WithString("template_set_id", mcpSDK.Description("Optional template set ID (defaults to 'builtin-standard-clean')")),
		mcpSDK.WithString("provider_override", mcpSDK.Description("Optional AI provider override (e.g., 'gemini', 'anthropic', 'openai')")),
		mcpSDK.WithString("model_override", mcpSDK.Description("Optional LLM model override")),
		mcpSDK.WithString("reasoning_level", mcpSDK.Description("Optional reasoning effort constraint for thinking models: 'none', 'low', 'medium', 'high'")),
	), handleCreateBranch(cfg))

	// 7. update_branch
	s.AddTool(mcpSDK.NewTool("update_branch",
		mcpSDK.WithDescription("Updates an existing targeted resume branch's configuration, target job requirements, prompt directives, template, or LLM model settings."),
		mcpSDK.WithString("id", mcpSDK.Required(), mcpSDK.Description("ID of the target resume branch to update")),
		mcpSDK.WithString("branch_id", mcpSDK.Description("Alias for id")),
		mcpSDK.WithString("name", mcpSDK.Description("Updated descriptive name for the branch")),
		mcpSDK.WithString("target_company", mcpSDK.Description("Updated target company name")),
		mcpSDK.WithString("target_role", mcpSDK.Description("Updated target role title")),
		mcpSDK.WithString("target_requirements", mcpSDK.Description("Updated target job description requirements or posting text")),
		mcpSDK.WithString("custom_directives", mcpSDK.Description("Updated custom candidate prompt directives")),
		mcpSDK.WithString("tailoring_strategy_id", mcpSDK.Description("Updated preset / tailoring strategy ID")),
		mcpSDK.WithString("template_set_id", mcpSDK.Description("Updated template set ID")),
		mcpSDK.WithString("provider_override", mcpSDK.Description("Updated LLM provider override (e.g. 'gemini', 'anthropic', 'openai')")),
		mcpSDK.WithString("model_override", mcpSDK.Description("Updated model identifier override")),
		mcpSDK.WithString("reasoning_level", mcpSDK.Description("Updated reasoning effort constraint: 'none', 'low', 'medium', 'high'")),
	), handleUpdateBranch(cfg))

	// 8. tailor_resume
	s.AddTool(mcpSDK.NewTool("tailor_resume",
		mcpSDK.WithDescription("Initiates an asynchronous tailoring generation pipeline for a branch against job requirements, applying XML boundary fencing."),
		mcpSDK.WithString("branch_id", mcpSDK.Required(), mcpSDK.Description("ID of the target resume branch")),
		mcpSDK.WithString("job_description", mcpSDK.Required(), mcpSDK.Description("Target job description text")),
		mcpSDK.WithString("custom_instructions", mcpSDK.Description("Optional candidate directives or emphasis instructions")),
		mcpSDK.WithString("provider_override", mcpSDK.Description("Optional LLM provider override")),
		mcpSDK.WithString("model_override", mcpSDK.Description("Optional LLM model override")),
		mcpSDK.WithString("reasoning_level", mcpSDK.Description("Optional reasoning effort constraint for thinking models: 'none', 'low', 'medium', 'high'")),
	), handleTailorResume(cfg))

	// 8. run_gap_analysis
	s.AddTool(mcpSDK.NewTool("run_gap_analysis",
		mcpSDK.WithDescription("Executes a decoupled ATS keyword and qualification gap analysis between the candidate's career profile and target job description."),
		mcpSDK.WithString("job_description", mcpSDK.Required(), mcpSDK.Description("Target job description or requirements text")),
		mcpSDK.WithString("branch_id", mcpSDK.Description("Optional branch ID to contextually align analysis against")),
	), handleRunGapAnalysis(cfg))

	// 8b. simulate_ats
	s.AddTool(mcpSDK.NewTool("simulate_ats",
		mcpSDK.WithDescription("Executes on-demand ATS extraction confidence and job matching score simulation for a completed tailored branch version."),
		mcpSDK.WithString("branch_id", mcpSDK.Required(), mcpSDK.Description("ID of the target tailored resume branch")),
		mcpSDK.WithString("version_id", mcpSDK.Description("Optional version ID or sequential version number (defaults to the active version)")),
		mcpSDK.WithString("format", mcpSDK.Description("Optional text resume format to parse: 'markdown', 'html', or 'text' (defaults to 'markdown')")),
	), handleSimulateATS(cfg))

	// 9. list_presets
	s.AddTool(mcpSDK.NewTool("list_presets",
		mcpSDK.WithDescription("Lists all tailoring strategy presets accessible to the authenticated user, including system built-in presets and custom tenant presets."),
		mcpSDK.WithString("language", mcpSDK.Description("Optional filter by target language (e.g., 'English', 'Spanish')")),
	), handleListPresets(cfg))

	// 10. get_preset
	s.AddTool(mcpSDK.NewTool("get_preset",
		mcpSDK.WithDescription("Retrieves the full configuration and system instructions for a specific tailoring strategy preset."),
		mcpSDK.WithString("id", mcpSDK.Required(), mcpSDK.Description("ID of the target tailoring preset")),
	), handleGetPreset(cfg))

	// 11. create_preset
	s.AddTool(mcpSDK.NewTool("create_preset",
		mcpSDK.WithDescription("Creates a new custom tailoring strategy preset for the authenticated user with customizable prompt instructions and language."),
		mcpSDK.WithString("name", mcpSDK.Required(), mcpSDK.Description("Descriptive name for the preset")),
		mcpSDK.WithString("system_instructions", mcpSDK.Required(), mcpSDK.Description("Core system instructions and tailoring prompt strategy guidelines")),
		mcpSDK.WithString("description", mcpSDK.Description("Optional human-readable description of the preset purpose")),
		mcpSDK.WithString("template_set_id", mcpSDK.Description("Optional default template set ID to suggest with this preset")),
		mcpSDK.WithString("suggested_theme_id", mcpSDK.Description("Alias for template_set_id")),
		mcpSDK.WithString("language", mcpSDK.Description("Target output language for contextual translation (e.g. 'English', 'Spanish', 'French'). Defaults to 'English'. RTL languages are rejected.")),
	), handleCreatePreset(cfg))

	// 12. update_preset
	s.AddTool(mcpSDK.NewTool("update_preset",
		mcpSDK.WithDescription("Updates mutable fields on an existing custom tailoring strategy preset. Built-in presets are immutable and cannot be updated."),
		mcpSDK.WithString("id", mcpSDK.Required(), mcpSDK.Description("ID of the target preset to update")),
		mcpSDK.WithString("name", mcpSDK.Description("Updated descriptive name")),
		mcpSDK.WithString("description", mcpSDK.Description("Updated description")),
		mcpSDK.WithString("system_instructions", mcpSDK.Description("Updated system instructions")),
		mcpSDK.WithString("template_set_id", mcpSDK.Description("Updated default template set ID")),
		mcpSDK.WithString("suggested_theme_id", mcpSDK.Description("Alias for template_set_id")),
		mcpSDK.WithString("language", mcpSDK.Description("Updated target output language (validated, RTL languages rejected)")),
	), handleUpdatePreset(cfg))

	// 13. list_supported_languages
	s.AddTool(mcpSDK.NewTool("list_supported_languages",
		mcpSDK.WithDescription("Lists all supported left-to-right output languages and the default language ('English') for resume tailoring and presets."),
	), handleListSupportedLanguages(cfg))

	// 14. list_skills
	s.AddTool(mcpSDK.NewTool("list_skills",
		mcpSDK.WithDescription("Lists skills registered in the candidate career profile and global system catalog, with optional type filtering ('all', 'global', 'custom')."),
		mcpSDK.WithString("type", mcpSDK.Description("Filter by skill origin: 'all', 'global', or 'custom'")),
	), handleListSkills(cfg))

	// 15. add_skill
	s.AddTool(mcpSDK.NewTool("add_skill",
		mcpSDK.WithDescription("Adds a custom skill to the authenticated candidate's career profile."),
		mcpSDK.WithString("name", mcpSDK.Required(), mcpSDK.Description("Name of the skill")),
		mcpSDK.WithString("category", mcpSDK.Description("Category classification (e.g. 'Languages', 'Cloud', 'Frameworks')")),
		mcpSDK.WithString("proficiency", mcpSDK.Description("Skill proficiency level (e.g. 'Familiar', 'Proficient', 'Expert')")),
		mcpSDK.WithNumber("years_experience", mcpSDK.Description("Number of years experience with this skill")),
		mcpSDK.WithString("keywords", mcpSDK.Description("Comma-separated list of searchable skill keywords")),
	), handleAddSkill(cfg))

	// 16. update_skill
	s.AddTool(mcpSDK.NewTool("update_skill",
		mcpSDK.WithDescription("Updates properties of an existing custom skill in the authenticated candidate's profile."),
		mcpSDK.WithString("id", mcpSDK.Required(), mcpSDK.Description("ID of the target skill to update")),
		mcpSDK.WithString("name", mcpSDK.Description("Updated name of the skill")),
		mcpSDK.WithString("category", mcpSDK.Description("Updated category classification")),
		mcpSDK.WithString("proficiency", mcpSDK.Description("Updated skill proficiency level")),
		mcpSDK.WithNumber("years_experience", mcpSDK.Description("Updated number of years experience")),
		mcpSDK.WithString("keywords", mcpSDK.Description("Updated comma-separated list of searchable skill keywords")),
	), handleUpdateSkill(cfg))
}

// handleGetMainProfile handles tool 'get_main_profile', returning the authenticated user's profile snapshot.
func handleGetMainProfile(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		var snapshot *domain.MainProfileSnapshot
		var err error
		if cfg.ProfileService != nil {
			snapshot, err = cfg.ProfileService.GetSnapshot(ctx, userID)
		} else if cfg.ProfileRepo != nil {
			snapshot, err = cfg.ProfileRepo.GetSnapshot(ctx, userID)
		} else {
			return mcpSDK.NewToolResultError("profile service and repository unavailable"), nil
		}

		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to get profile snapshot", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to retrieve main profile due to an internal repository error"), nil
		}

		res, err := jsonToolResult(snapshot)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to marshal profile snapshot", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to serialize main profile snapshot"), nil
		}
		return res, nil
	}
}

// handleListBranches handles tool 'list_branches', enumerating all resume branches for the authenticated user.
func handleListBranches(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		branches, err := cfg.BranchRepo.ListBranches(ctx, userID)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to list branches", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to list branches due to an internal repository error"), nil
		}

		res, err := jsonToolResult(branches)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to marshal branches", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to serialize branch list"), nil
		}
		return res, nil
	}
}

// handleRenderResume handles tool 'render_resume', compiling and rendering a resume branch version into the requested format.
func handleRenderResume(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		branchID, _ := args["branch_id"].(string)
		if strings.TrimSpace(branchID) == "" {
			return mcpSDK.NewToolResultError("branch_id is required"), nil
		}

		formatRaw, _ := args["format"].(string)
		var outputFormat domain.OutputFormat
		switch strings.ToLower(strings.TrimSpace(formatRaw)) {
		case "html":
			outputFormat = domain.OutputFormatHTML
		case "markdown":
			outputFormat = domain.OutputFormatMarkdown
		case "text", "plain_text":
			outputFormat = domain.OutputFormatPlainText
		default:
			return mcpSDK.NewToolResultError("format must be 'html', 'markdown', or 'text'"), nil
		}

		branch, err := cfg.BranchRepo.GetBranch(ctx, userID, branchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", branchID)), nil
		}

		var versionNum int
		if v, ok := args["version_number"]; ok {
			switch val := v.(type) {
			case float64:
				versionNum = int(val)
			case int:
				versionNum = val
			case string:
				parsed, _ := strconv.Atoi(val)
				versionNum = parsed
			}
		}

		var ver *domain.BranchVersion
		if versionNum > 0 {
			v, vErr := cfg.BranchRepo.GetVersionByNumber(ctx, branchID, versionNum)
			if vErr != nil || v == nil {
				return mcpSDK.NewToolResultError(fmt.Sprintf("version number %d not found for branch %q", versionNum, branchID)), nil
			}
			ver = v
		} else if branch.ActiveVersionID != nil && *branch.ActiveVersionID != "" {
			v, vErr := cfg.BranchRepo.GetVersion(ctx, branchID, *branch.ActiveVersionID)
			if vErr == nil && v != nil {
				ver = v
			}
		}

		if ver == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("no completed version found for branch %q", branchID)), nil
		}

		templateSetID, _ := args["template_set_id"].(string)
		if templateSetID == "" {
			templateSetID, _ = args["template_id"].(string)
		}
		if templateSetID == "" {
			templateSetID = ver.TemplateSetIDUsed
		}
		if templateSetID == "" {
			templateSetID = branch.TemplateSetID
		}
		if templateSetID == "" {
			templateSetID = "builtin-standard-clean"
		}

		tmpl, err := cfg.TemplateRepo.GetTemplate(ctx, userID, templateSetID)
		if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != domain.SystemUserID) {
			return mcpSDK.NewToolResultError(fmt.Sprintf("template set %q not found or access denied", templateSetID)), nil
		}

		var output string
		prof, _ := cfg.ProfileRepo.GetProfile(ctx, userID)
		if cfg.TemplateRenderer != nil && prof != nil && ver.TailoredContent != nil {
			rendered, rErr := cfg.TemplateRenderer.Render(ctx, tmpl, prof, ver.TailoredContent)
			if rErr == nil && rendered != nil {
				output = extractFormatOutput(outputFormat, rendered.HTML, rendered.Markdown, rendered.PlainText)
			}
		}

		if output == "" {
			output = extractFormatOutput(outputFormat, ver.HTMLOutput, ver.MarkdownOutput, ver.PlainTextOutput)
		}

		if outputFormat == domain.OutputFormatHTML {
			output = stripScriptTags(output)
		}

		return mcpSDK.NewToolResultText(output), nil
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

		company, _ := args["company"].(string)
		roleTitle, _ := args["role_title"].(string)
		startDateStr, _ := args["start_date"].(string)

		company = strings.TrimSpace(company)
		roleTitle = strings.TrimSpace(roleTitle)
		startDateStr = strings.TrimSpace(startDateStr)

		if company == "" || roleTitle == "" || startDateStr == "" {
			return mcpSDK.NewToolResultError("company, role_title, and start_date are required fields"), nil
		}

		startDate, err := domain.ParseDate(startDateStr)
		if err != nil || startDate.IsZero() {
			return mcpSDK.NewToolResultError("valid start_date is required (format: YYYY-MM-DD)"), nil
		}

		var endDate *domain.Date
		if ed, ok := args["end_date"].(string); ok && strings.TrimSpace(ed) != "" {
			if parsed, err := domain.ParseDate(ed); err == nil && !parsed.IsZero() {
				endDate = &parsed
			}
		}

		var isCurrent bool
		if ic, ok := args["is_current"].(bool); ok {
			isCurrent = ic
		}

		var loc domain.Location
		if l, ok := args["location"].(string); ok && l != "" {
			loc.Address = l
		}

		var summary string
		if s, ok := args["summary"].(string); ok {
			summary = s
		}

		exp := &domain.WorkExperience{
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

		if err := cfg.ProfileRepo.CreateExperience(ctx, exp); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create experience", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create work experience due to an internal repository error"), nil
		}

		return mcpSDK.NewToolResultText(fmt.Sprintf("Experience created successfully with ID: %s", exp.EffectiveShortID())), nil
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

		ownerID, _ := args["owner_id"].(string)
		ownerID = strings.TrimSpace(ownerID)
		if ownerID == "" {
			return mcpSDK.NewToolResultError("owner_id is required"), nil
		}

		rawOwnerType, _ := args["owner_type"].(string)
		rawOwnerType = strings.TrimSpace(strings.ToLower(rawOwnerType))
		ownerType := domain.OwnerType(rawOwnerType)
		if ownerType != domain.OwnerTypeWorkExperience &&
			ownerType != domain.OwnerTypeProject &&
			ownerType != domain.OwnerTypeEducation {
			return mcpSDK.NewToolResultError("invalid owner_type: must be 'work_experience', 'project', or 'education'"), nil
		}

		statement, _ := args["statement"].(string)
		statement = strings.TrimSpace(statement)
		if statement == "" {
			return mcpSDK.NewToolResultError("statement cannot be empty"), nil
		}

		// Verify target owner entity exists and belongs to the authenticated user
		var exists bool
		switch ownerType {
		case domain.OwnerTypeWorkExperience:
			exp, err := cfg.ProfileRepo.GetExperience(ctx, userID, ownerID)
			exists = err == nil && exp != nil && exp.UserID == userID
		case domain.OwnerTypeProject:
			proj, err := cfg.ProfileRepo.GetProject(ctx, userID, ownerID)
			exists = err == nil && proj != nil && proj.UserID == userID
		case domain.OwnerTypeEducation:
			edu, err := cfg.ProfileRepo.GetEducation(ctx, userID, ownerID)
			exists = err == nil && edu != nil && edu.UserID == userID
		}

		if !exists {
			return mcpSDK.NewToolResultError(fmt.Sprintf("%s with ID %q not found or access denied", ownerType, ownerID)), nil
		}

		fact := &domain.Fact{
			ID:        "fact-" + uuid.New().String(),
			UserID:    userID,
			OwnerID:   ownerID,
			OwnerType: ownerType,
			Statement: statement,
		}

		if err := cfg.ProfileRepo.CreateFact(ctx, fact); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create achievement fact", "user_id", userID, "owner_id", ownerID, "err", err)
			return mcpSDK.NewToolResultError("failed to create achievement fact due to an internal repository error"), nil
		}

		return mcpSDK.NewToolResultText(fmt.Sprintf("Achievement bullet created successfully with ID: %s", fact.EffectiveShortID())), nil
	}
}

// handleCreateBranch handles tool 'create_branch', creating a new targeted resume branch with XML boundary fencing.
func handleCreateBranch(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		name, _ := args["name"].(string)
		targetCompany, _ := args["target_company"].(string)
		targetRole, _ := args["target_role"].(string)
		targetRequirements, _ := args["target_requirements"].(string)
		strategyID, _ := args["tailoring_strategy_id"].(string)
		templateSetID, _ := args["template_set_id"].(string)
		providerOverride, _ := args["provider_override"].(string)
		modelOverride, _ := args["model_override"].(string)
		reasoningLevel, _ := args["reasoning_level"].(string)

		name = strings.TrimSpace(name)
		targetCompany = strings.TrimSpace(targetCompany)
		targetRole = strings.TrimSpace(targetRole)
		strategyID = strings.TrimSpace(strategyID)
		templateSetID = strings.TrimSpace(templateSetID)
		providerOverride = strings.TrimSpace(providerOverride)
		modelOverride = strings.TrimSpace(modelOverride)
		reasoningLevel = strings.TrimSpace(strings.ToLower(reasoningLevel))
		switch reasoningLevel {
		case "", "none", "low", "medium", "high":
		default:
			return mcpSDK.NewToolResultError("reasoning_level must be one of: none, low, medium, high"), nil
		}

		isAdmin := cfg.Admin || domain.IsAdminFromContext(ctx)
		if !isAdmin && (providerOverride != "" || modelOverride != "" || reasoningLevel != "") {
			return mcpSDK.NewToolResultError("model and reasoning selection are restricted to administrators"), nil
		}

		if name == "" || targetCompany == "" || targetRole == "" {
			return mcpSDK.NewToolResultError("name, target_company, and target_role are required"), nil
		}

		// Verify multi-tenant access to tailoring_strategy_id if provided
		var stratPtr *string
		if strategyID != "" {
			preset, err := cfg.PresetRepo.GetPreset(ctx, userID, strategyID)
			if err != nil && (strings.HasPrefix(strategyID, "builtin-") || cfg.DefaultUserID != "") {
				if p, pErr := cfg.PresetRepo.GetPreset(ctx, cfg.DefaultUserID, strategyID); pErr == nil && p != nil {
					preset = p
					err = nil
				}
			}
			if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn() && preset.UserID != domain.SystemUserID) {
				return mcpSDK.NewToolResultError(fmt.Sprintf("tailoring strategy %q not found or access denied", strategyID)), nil
			}
			stratPtr = &strategyID
		}

		// Verify multi-tenant access to template_set_id
		if templateSetID == "" {
			templateSetID = "builtin-standard-clean"
		}
		tmpl, err := cfg.TemplateRepo.GetTemplate(ctx, userID, templateSetID)
		if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != domain.SystemUserID) {
			return mcpSDK.NewToolResultError(fmt.Sprintf("template set %q not found or access denied", templateSetID)), nil
		}

		// Fencing target requirements against indirect prompt injection
		fencedRequirements := targetRequirements
		if strings.TrimSpace(targetRequirements) != "" {
			fencedRequirements = sanitizeXMLFence(targetRequirements, "target_job_description")
		}

		branch := &domain.Branch{
			ID:                  "branch-" + uuid.New().String(),
			UserID:              userID,
			Name:                name,
			TargetCompany:       targetCompany,
			TargetRole:          targetRole,
			TargetRequirements:  fencedRequirements,
			TailoringStrategyID: stratPtr,
			PresetID:            stratPtr,
			TemplateSetID:       templateSetID,
			ProviderOverride:    providerOverride,
			ModelOverride:       modelOverride,
			ReasoningLevel:      reasoningLevel,
		}

		createdBranch, err := cfg.BranchService.CreateBranch(ctx, branch)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create branch", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create branch due to an internal repository error"), nil
		}

		res, err := jsonToolResult(createdBranch)
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize created branch"), nil
		}
		return res, nil
	}
}

// handleUpdateBranch handles tool 'update_branch', updating an existing targeted resume branch.
func handleUpdateBranch(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		branchID, _ := args["id"].(string)
		if branchID == "" {
			branchID, _ = args["branch_id"].(string)
		}
		branchID = strings.TrimSpace(branchID)
		if branchID == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		branch, err := cfg.BranchRepo.GetBranch(ctx, userID, branchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", branchID)), nil
		}

		if name, ok := args["name"].(string); ok && strings.TrimSpace(name) != "" {
			branch.Name = strings.TrimSpace(name)
		}
		if company, ok := args["target_company"].(string); ok {
			branch.TargetCompany = strings.TrimSpace(company)
		}
		if role, ok := args["target_role"].(string); ok {
			branch.TargetRole = strings.TrimSpace(role)
		}
		if reqs, ok := args["target_requirements"].(string); ok {
			reqs = strings.TrimSpace(reqs)
			if reqs != "" {
				branch.TargetRequirements = sanitizeXMLFence(reqs, "target_job_description")
			} else {
				branch.TargetRequirements = ""
			}
		}
		if dirs, ok := args["custom_directives"].(string); ok {
			dirs = strings.TrimSpace(dirs)
			if dirs != "" {
				fenced := sanitizeXMLFence(dirs, "custom_instructions")
				branch.CustomDirectives = fenced
				branch.BranchInstructions = fenced
			} else {
				branch.CustomDirectives = ""
				branch.BranchInstructions = ""
			}
		}
		if strat, ok := args["tailoring_strategy_id"].(string); ok {
			strat = strings.TrimSpace(strat)
			if strat != "" {
				preset, err := cfg.PresetRepo.GetPreset(ctx, userID, strat)
				if err != nil && (strings.HasPrefix(strat, "builtin-") || cfg.DefaultUserID != "") {
					if p, pErr := cfg.PresetRepo.GetPreset(ctx, cfg.DefaultUserID, strat); pErr == nil && p != nil {
						preset = p
						err = nil
					}
				}
				if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn() && preset.UserID != domain.SystemUserID) {
					return mcpSDK.NewToolResultError(fmt.Sprintf("tailoring strategy %q not found or access denied", strat)), nil
				}
				branch.TailoringStrategyID = &strat
				branch.PresetID = &strat
			} else {
				branch.TailoringStrategyID = nil
				branch.PresetID = nil
			}
		}
		if tmplID, ok := args["template_set_id"].(string); ok {
			tmplID = strings.TrimSpace(tmplID)
			if tmplID != "" {
				tmpl, err := cfg.TemplateRepo.GetTemplate(ctx, userID, tmplID)
				if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != domain.SystemUserID) {
					return mcpSDK.NewToolResultError(fmt.Sprintf("template set %q not found or access denied", tmplID)), nil
				}
				branch.TemplateSetID = tmplID
			}
		}
		isAdmin := cfg.Admin || domain.IsAdminFromContext(ctx)
		if !isAdmin && (args["provider_override"] != nil || args["model_override"] != nil || args["reasoning_level"] != nil) {
			return mcpSDK.NewToolResultError("model and reasoning selection are restricted to administrators"), nil
		}

		if prov, ok := args["provider_override"].(string); ok {
			branch.ProviderOverride = strings.TrimSpace(prov)
		}
		if mod, ok := args["model_override"].(string); ok {
			branch.ModelOverride = strings.TrimSpace(mod)
		}
		if rLvl, ok := args["reasoning_level"].(string); ok {
			rLvl = strings.TrimSpace(strings.ToLower(rLvl))
			switch rLvl {
			case "", "none", "low", "medium", "high":
				branch.ReasoningLevel = rLvl
			default:
				return mcpSDK.NewToolResultError("reasoning_level must be one of: none, low, medium, high"), nil
			}
		}

		updatedBranch, err := cfg.BranchService.UpdateBranch(ctx, branch)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to update branch", "branch_id", branchID, "err", err)
			return mcpSDK.NewToolResultError(fmt.Sprintf("failed to update branch: %v", err)), nil
		}

		res, err := jsonToolResult(updatedBranch)
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize updated branch"), nil
		}
		return res, nil
	}
}

// handleTailorResume handles tool 'tailor_resume', launching the asynchronous resume tailoring generation pipeline.
func handleTailorResume(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		branchID, _ := args["branch_id"].(string)
		jobDescription, _ := args["job_description"].(string)
		customInstructions, _ := args["custom_instructions"].(string)

		branchID = strings.TrimSpace(branchID)
		jobDescription = strings.TrimSpace(jobDescription)
		customInstructions = strings.TrimSpace(customInstructions)

		if branchID == "" || jobDescription == "" {
			return mcpSDK.NewToolResultError("branch_id and job_description are required"), nil
		}

		branch, err := cfg.BranchRepo.GetBranch(ctx, userID, branchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", branchID)), nil
		}

		// Apply XML boundary fencing to untrusted client inputs
		branch.TargetRequirements = sanitizeXMLFence(jobDescription, "target_job_description")
		if customInstructions != "" {
			branch.CustomDirectives = sanitizeXMLFence(customInstructions, "custom_instructions")
			branch.BranchInstructions = branch.CustomDirectives
		}

		isAdmin := cfg.Admin || domain.IsAdminFromContext(ctx)
		if !isAdmin && (args["provider_override"] != nil || args["model_override"] != nil || args["reasoning_level"] != nil) {
			return mcpSDK.NewToolResultError("model and reasoning selection are restricted to administrators"), nil
		}

		if prov, ok := args["provider_override"].(string); ok && strings.TrimSpace(prov) != "" {
			branch.ProviderOverride = strings.TrimSpace(prov)
		}
		if mod, ok := args["model_override"].(string); ok && strings.TrimSpace(mod) != "" {
			branch.ModelOverride = strings.TrimSpace(mod)
		}
		if rLvl, ok := args["reasoning_level"].(string); ok && strings.TrimSpace(rLvl) != "" {
			rLvl = strings.TrimSpace(strings.ToLower(rLvl))
			switch rLvl {
			case "none", "low", "medium", "high":
				branch.ReasoningLevel = rLvl
			default:
				return mcpSDK.NewToolResultError("reasoning_level must be one of: none, low, medium, high"), nil
			}
		}

		if err := cfg.BranchRepo.UpdateBranch(ctx, branch); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to update branch with fenced requirements", "branch_id", branchID, "err", err)
			return mcpSDK.NewToolResultError("failed to update branch requirements"), nil
		}

		tailorCtx, cancel := context.WithTimeout(ctx, cfg.TailoringTimeout)
		defer cancel()

		ver, err := cfg.BranchService.GenerateVersion(tailorCtx, userID, branchID)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to trigger tailoring generation", "branch_id", branchID, "err", err)
			return mcpSDK.NewToolResultError("failed to trigger tailoring generation"), nil
		}

		res, err := jsonToolResult(map[string]any{
			"status":     "tailoring_initiated",
			"branch_id":  branchID,
			"version_id": ver.ID,
			"version":    ver,
		})
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize tailoring response"), nil
		}
		return res, nil
	}
}

// handleRunGapAnalysis handles tool 'run_gap_analysis', executing a qualification and ATS keyword gap analysis.
func handleRunGapAnalysis(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		jobDescription, _ := args["job_description"].(string)
		branchID, _ := args["branch_id"].(string)

		jobDescription = strings.TrimSpace(jobDescription)
		branchID = strings.TrimSpace(branchID)

		if jobDescription == "" {
			return mcpSDK.NewToolResultError("job_description is required"), nil
		}

		targetBranchID := branchID
		if targetBranchID == "" {
			branches, err := cfg.BranchRepo.ListBranches(ctx, userID)
			if err == nil {
				for _, b := range branches {
					if b.IsDefault {
						targetBranchID = b.ID
						break
					}
				}
				if targetBranchID == "" && len(branches) > 0 {
					targetBranchID = branches[0].ID
				}
			}
		}

		if targetBranchID == "" {
			return mcpSDK.NewToolResultError("no target branch available for gap analysis; please create a branch first"), nil
		}

		branch, err := cfg.BranchRepo.GetBranch(ctx, userID, targetBranchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", targetBranchID)), nil
		}

		// Apply XML boundary fencing
		branch.TargetRequirements = sanitizeXMLFence(jobDescription, "target_job_description")
		_ = cfg.BranchRepo.UpdateBranch(ctx, branch)

		report, err := cfg.BranchService.AnalyzeGaps(ctx, userID, targetBranchID)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to run gap analysis", "branch_id", targetBranchID, "err", err)
			return mcpSDK.NewToolResultError("failed to execute gap analysis"), nil
		}

		res, err := jsonToolResult(report)
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize gap analysis report"), nil
		}
		return res, nil
	}
}

// handleSimulateATS handles tool 'simulate_ats', executing on-demand ATS extraction and job matching score evaluation for a tailored branch version.
func handleSimulateATS(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		branchID, _ := args["branch_id"].(string)
		versionID, _ := args["version_id"].(string)
		format, _ := args["format"].(string)

		branchID = strings.TrimSpace(branchID)
		if branchID == "" {
			return mcpSDK.NewToolResultError("branch_id is required"), nil
		}

		score, err := cfg.BranchService.SimulateATS(ctx, userID, branchID, &domain.ATSSimulationOptions{
			VersionID: strings.TrimSpace(versionID),
			Format:    strings.TrimSpace(format),
		})
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to simulate ATS score", "branch_id", branchID, "err", err)
			return mcpSDK.NewToolResultError(fmt.Sprintf("failed to simulate ATS score: %v", err)), nil
		}

		res, err := jsonToolResult(score)
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize ATS score"), nil
		}
		return res, nil
	}
}

// handleListPresets handles tool 'list_presets', enumerating all presets visible to the user.
func handleListPresets(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		if cfg.PresetRepo == nil {
			return mcpSDK.NewToolResultError("preset repository unavailable"), nil
		}

		presets, err := cfg.PresetRepo.ListPresets(ctx, userID)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to list presets", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to list presets due to an internal repository error"), nil
		}
		if presets == nil {
			presets = []domain.Preset{}
		}

		args := parseArguments(req)
		if langRaw, ok := args["language"].(string); ok && strings.TrimSpace(langRaw) != "" {
			targetLang := strings.TrimSpace(langRaw)
			filtered := make([]domain.Preset, 0, len(presets))
			for _, p := range presets {
				if strings.EqualFold(p.EffectiveLanguage(), targetLang) {
					filtered = append(filtered, p)
				}
			}
			presets = filtered
		}

		return jsonToolResult(presets)
	}
}

// handleGetPreset handles tool 'get_preset', retrieving a specific preset by ID.
func handleGetPreset(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		if cfg.PresetRepo == nil {
			return mcpSDK.NewToolResultError("preset repository unavailable"), nil
		}

		args := parseArguments(req)
		presetID, _ := args["id"].(string)
		presetID = strings.TrimSpace(presetID)
		if presetID == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		preset, err := cfg.PresetRepo.GetPreset(ctx, userID, presetID)
		if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn() && preset.UserID != domain.SystemUserID) {
			return mcpSDK.NewToolResultError(fmt.Sprintf("preset with ID %q not found or access denied", presetID)), nil
		}

		return jsonToolResult(preset)
	}
}

// handleCreatePreset handles tool 'create_preset', validating and storing a new custom preset.
func handleCreatePreset(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		if cfg.PresetRepo == nil {
			return mcpSDK.NewToolResultError("preset repository unavailable"), nil
		}

		args := parseArguments(req)
		name, _ := args["name"].(string)
		name = strings.TrimSpace(name)
		instructions, _ := args["system_instructions"].(string)
		instructions = strings.TrimSpace(instructions)
		if name == "" || instructions == "" {
			return mcpSDK.NewToolResultError("name and system_instructions are required fields"), nil
		}

		desc, _ := args["description"].(string)
		templateSetID, _ := args["template_set_id"].(string)
		if templateSetID == "" {
			templateSetID, _ = args["suggested_theme_id"].(string)
		}
		templateSetID = strings.TrimSpace(templateSetID)

		if templateSetID != "" && cfg.TemplateRepo != nil {
			tmpl, err := cfg.TemplateRepo.GetTemplate(ctx, userID, templateSetID)
			if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != domain.SystemUserID) {
				return mcpSDK.NewToolResultError(fmt.Sprintf("cannot access foreign or nonexistent template %q", templateSetID)), nil
			}
		}

		langRaw, _ := args["language"].(string)
		validatedLang, err := domain.ValidateLanguage(langRaw)
		if err != nil {
			return mcpSDK.NewToolResultError(err.Error()), nil
		}

		var suggestedTheme *string
		if templateSetID != "" {
			suggestedTheme = &templateSetID
		}

		preset := domain.Preset{
			ID:                 "preset-" + uuid.New().String(),
			UserID:             userID,
			Name:               name,
			Description:        desc,
			SystemInstructions: instructions,
			SuggestedThemeID:   suggestedTheme,
			TemplateSetID:      templateSetID,
			Language:           validatedLang,
		}

		if err := cfg.PresetRepo.CreatePreset(ctx, &preset); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create preset", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create preset due to an internal repository error"), nil
		}

		return jsonToolResult(preset)
	}
}

// handleUpdatePreset handles tool 'update_preset', modifying an existing user preset and enforcing immutability on built-ins.
func handleUpdatePreset(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		if cfg.PresetRepo == nil {
			return mcpSDK.NewToolResultError("preset repository unavailable"), nil
		}

		args := parseArguments(req)
		presetID, _ := args["id"].(string)
		presetID = strings.TrimSpace(presetID)
		if presetID == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		preset, err := cfg.PresetRepo.GetPreset(ctx, userID, presetID)
		if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn() && preset.UserID != domain.SystemUserID) {
			return mcpSDK.NewToolResultError(fmt.Sprintf("preset with ID %q not found or access denied", presetID)), nil
		}

		if preset.IsBuiltIn() || strings.HasPrefix(preset.ID, "builtin-") || preset.UserID == domain.SystemUserID {
			return mcpSDK.NewToolResultError("built-in presets cannot be modified (immutable)"), nil
		}

		if name, ok := args["name"].(string); ok && strings.TrimSpace(name) != "" {
			preset.Name = strings.TrimSpace(name)
		}
		if desc, ok := args["description"].(string); ok {
			preset.Description = strings.TrimSpace(desc)
		}
		if sys, ok := args["system_instructions"].(string); ok && strings.TrimSpace(sys) != "" {
			preset.SystemInstructions = strings.TrimSpace(sys)
		}
		var rawTemplateID string
		if tmplID, ok := args["template_set_id"].(string); ok {
			rawTemplateID = strings.TrimSpace(tmplID)
		} else if themeID, ok := args["suggested_theme_id"].(string); ok {
			rawTemplateID = strings.TrimSpace(themeID)
		}
		if rawTemplateID != "" {
			if cfg.TemplateRepo != nil {
				tmpl, tErr := cfg.TemplateRepo.GetTemplate(ctx, userID, rawTemplateID)
				if tErr != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != domain.SystemUserID) {
					return mcpSDK.NewToolResultError(fmt.Sprintf("cannot access foreign or nonexistent template %q", rawTemplateID)), nil
				}
			}
			preset.TemplateSetID = rawTemplateID
			preset.SuggestedThemeID = &rawTemplateID
		}
		if langRaw, ok := args["language"].(string); ok {
			validatedLang, err := domain.ValidateLanguage(langRaw)
			if err != nil {
				return mcpSDK.NewToolResultError(err.Error()), nil
			}
			preset.Language = validatedLang
		}

		if err := cfg.PresetRepo.UpdatePreset(ctx, preset); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to update preset", "preset_id", presetID, "err", err)
			return mcpSDK.NewToolResultError("failed to update preset due to an internal repository error"), nil
		}

		return jsonToolResult(preset)
	}
}

// handleListSupportedLanguages handles tool 'list_supported_languages', returning supported Left-to-Right languages.
func handleListSupportedLanguages(_ ServerConfig) serverSDK.ToolHandlerFunc {
	return func(_ context.Context, _ mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		return jsonToolResult(map[string]any{
			"default_language":    domain.DefaultLanguage,
			"supported_languages": domain.SupportedLanguages,
		})
	}
}

// handleListSkills handles tool 'list_skills', returning skills for the user with optional origin filter.
func handleListSkills(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		if cfg.ProfileRepo == nil {
			return mcpSDK.NewToolResultError("profile repository unavailable"), nil
		}

		args := parseArguments(req)
		skillType, _ := args["type"].(string)
		skillType = strings.ToLower(strings.TrimSpace(skillType))

		var skills []domain.Skill
		var err error
		switch skillType {
		case "global":
			skills, err = cfg.ProfileRepo.ListGlobalSkills(ctx)
		case "custom":
			all, lErr := cfg.ProfileRepo.ListSkills(ctx, userID)
			if lErr == nil {
				skills = make([]domain.Skill, 0, len(all))
				for _, s := range all {
					if !s.IsGlobal && s.UserID == userID {
						skills = append(skills, s)
					}
				}
			}
			err = lErr
		default:
			skills, err = cfg.ProfileRepo.ListSkills(ctx, userID)
		}

		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to list skills", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to list skills due to an internal repository error"), nil
		}
		if skills == nil {
			skills = []domain.Skill{}
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

		if cfg.ProfileRepo == nil {
			return mcpSDK.NewToolResultError("profile repository unavailable"), nil
		}

		args := parseArguments(req)
		name, _ := args["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return mcpSDK.NewToolResultError("name is required"), nil
		}

		category, _ := args["category"].(string)
		proficiency, _ := args["proficiency"].(string)

		var years float64
		if yPtr, ok := parseOptionalYearsExperience(args["years_experience"]); ok && yPtr != nil {
			years = *yPtr
		}

		var keywords []string
		if kw, ok := parseOptionalKeywords(args["keywords"]); ok {
			keywords = kw
		}

		skill := domain.Skill{
			ID:              domain.GenerateID("skill"),
			ShortID:         domain.GenerateShortID("skill"),
			UserID:          userID,
			Name:            name,
			Category:        strings.TrimSpace(category),
			Proficiency:     strings.TrimSpace(proficiency),
			YearsExperience: years,
			Keywords:        keywords,
		}

		if err := cfg.ProfileRepo.CreateSkill(ctx, &skill); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create skill", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create skill due to an internal repository error"), nil
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

		if cfg.ProfileRepo == nil {
			return mcpSDK.NewToolResultError("profile repository unavailable"), nil
		}

		args := parseArguments(req)
		id, _ := args["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		skill, err := cfg.ProfileRepo.GetSkill(ctx, userID, id)
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

		if err := cfg.ProfileRepo.UpdateSkill(ctx, skill); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to update skill", "id", id, "err", err)
			return mcpSDK.NewToolResultError("failed to update skill due to an internal repository error"), nil
		}

		return jsonToolResult(skill)
	}
}
