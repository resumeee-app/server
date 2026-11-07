// Package mcp implements Model Context Protocol (MCP) server endpoints, tools, resources, and prompts.
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

	"resumeee/internal/domain"
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
	tagRegex := regexp.MustCompile(fmt.Sprintf(`(?i)</?\s*(?:%s|rules_hierarchy|tier_1_global_rules|tier_2_strategy_instructions|tier_3_branch_directives|master_career_profile|target_job_description|custom_instructions)\b[^>]*>`, regexp.QuoteMeta(tagName)))
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
	// 1. get_master_profile
	s.AddTool(mcpSDK.NewTool("get_master_profile",
		mcpSDK.WithDescription("Retrieves the canonical master career profile snapshot (experiences, bullet facts, skills, education, certifications) for the authenticated user."),
	), handleGetMasterProfile(cfg))

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
		mcpSDK.WithDescription("Adds a new employment work experience to the authenticated user's master career profile."),
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
		mcpSDK.WithDescription("Creates a new targeted resume branch tailored for a specific company, role, tailoring strategy, and template."),
		mcpSDK.WithString("name", mcpSDK.Required(), mcpSDK.Description("Descriptive name for the resume branch")),
		mcpSDK.WithString("target_company", mcpSDK.Required(), mcpSDK.Description("Name of the prospective employer")),
		mcpSDK.WithString("target_role", mcpSDK.Required(), mcpSDK.Description("Target job title")),
		mcpSDK.WithString("target_requirements", mcpSDK.Description("Target job description requirements or posting text")),
		mcpSDK.WithString("tailoring_strategy_id", mcpSDK.Description("Optional preset / tailoring strategy ID")),
		mcpSDK.WithString("template_set_id", mcpSDK.Description("Optional template set ID (defaults to 'builtin-standard-clean')")),
	), handleCreateBranch(cfg))

	// 7. tailor_resume
	s.AddTool(mcpSDK.NewTool("tailor_resume",
		mcpSDK.WithDescription("Initiates an asynchronous tailoring generation pipeline for a branch against job requirements, applying XML boundary fencing."),
		mcpSDK.WithString("branch_id", mcpSDK.Required(), mcpSDK.Description("ID of the target resume branch")),
		mcpSDK.WithString("job_description", mcpSDK.Required(), mcpSDK.Description("Target job description text")),
		mcpSDK.WithString("custom_instructions", mcpSDK.Description("Optional candidate directives or emphasis instructions")),
	), handleTailorResume(cfg))

	// 8. run_gap_analysis
	s.AddTool(mcpSDK.NewTool("run_gap_analysis",
		mcpSDK.WithDescription("Executes a decoupled ATS keyword and qualification gap analysis between the candidate's career profile and target job description."),
		mcpSDK.WithString("job_description", mcpSDK.Required(), mcpSDK.Description("Target job description or requirements text")),
		mcpSDK.WithString("branch_id", mcpSDK.Description("Optional branch ID to contextually align analysis against")),
	), handleRunGapAnalysis(cfg))
}

// handleGetMasterProfile handles tool 'get_master_profile', returning the authenticated user's profile snapshot.
func handleGetMasterProfile(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)

		snapshot, err := cfg.ProfileService.GetSnapshot(ctx, userID)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to get profile snapshot", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to retrieve master profile due to an internal repository error"), nil
		}

		res, err := jsonToolResult(snapshot)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to marshal profile snapshot", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to serialize master profile snapshot"), nil
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
		if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn) {
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

// handleAddExperience handles tool 'add_experience', appending employment history to the master profile.
func handleAddExperience(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		args := parseArguments(req)

		company, _ := args["company"].(string)
		roleTitle, _ := args["role_title"].(string)
		startDate, _ := args["start_date"].(string)

		company = strings.TrimSpace(company)
		roleTitle = strings.TrimSpace(roleTitle)
		startDate = strings.TrimSpace(startDate)

		if company == "" || roleTitle == "" || startDate == "" {
			return mcpSDK.NewToolResultError("company, role_title, and start_date are required fields"), nil
		}

		var endDate *string
		if ed, ok := args["end_date"].(string); ok && strings.TrimSpace(ed) != "" {
			trimmed := strings.TrimSpace(ed)
			endDate = &trimmed
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
		args := parseArguments(req)

		name, _ := args["name"].(string)
		targetCompany, _ := args["target_company"].(string)
		targetRole, _ := args["target_role"].(string)
		targetRequirements, _ := args["target_requirements"].(string)
		strategyID, _ := args["tailoring_strategy_id"].(string)
		templateSetID, _ := args["template_set_id"].(string)

		name = strings.TrimSpace(name)
		targetCompany = strings.TrimSpace(targetCompany)
		targetRole = strings.TrimSpace(targetRole)
		strategyID = strings.TrimSpace(strategyID)
		templateSetID = strings.TrimSpace(templateSetID)

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
			if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn() && !strings.HasPrefix(preset.ID, "builtin-")) {
				return mcpSDK.NewToolResultError(fmt.Sprintf("tailoring strategy %q not found or access denied", strategyID)), nil
			}
			stratPtr = &strategyID
		}

		// Verify multi-tenant access to template_set_id
		if templateSetID == "" {
			templateSetID = "builtin-standard-clean"
		}
		tmpl, err := cfg.TemplateRepo.GetTemplate(ctx, userID, templateSetID)
		if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn) {
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

// handleTailorResume handles tool 'tailor_resume', launching the asynchronous resume tailoring generation pipeline.
func handleTailorResume(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
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
