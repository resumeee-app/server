package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-api/internal/access"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/renderer"
)

// tailorResumePayload serializes the asynchronous tailoring initiation response returned by tailor_resume.
type tailorResumePayload struct {
	Status    string              `json:"status"`
	BranchID  string              `json:"branch_id"`
	VersionID string              `json:"version_id"`
	Version   *core.BranchVersion `json:"version"`
}

// registerBranchTools registers resume branch, rendering, tailoring, gap analysis, and ATS simulation tools onto s.
func registerBranchTools(s *serverSDK.MCPServer, cfg ServerConfig) {
	s.AddTool(mcpSDK.NewTool("list_branches",
		mcpSDK.WithDescription("Lists all tailored resume branches and their active version status for the authenticated user."),
	), withToolDeps(handleListBranches(cfg), branchStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("render_resume",
		mcpSDK.WithDescription("Renders a resume branch version into HTML, Markdown, or plain text using the specified template set."),
		mcpSDK.WithString("branch_id", mcpSDK.Required(), mcpSDK.Description("ID of the target resume branch")),
		mcpSDK.WithString("format", mcpSDK.Required(), mcpSDK.Description("Target format: 'html', 'markdown', or 'text'")),
		mcpSDK.WithInteger("version_number", mcpSDK.Description("Sequential version number (e.g., 1, 2). If omitted, renders active version.")),
		mcpSDK.WithString("template_set_id", mcpSDK.Description("Optional template set ID to override branch/version template")),
		mcpSDK.WithString("template_id", mcpSDK.Description("Alias for template_set_id")),
	), withToolDeps(handleRenderResume(cfg), branchStoreDep(cfg), templateStoreDep(cfg)))

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
	), withToolDeps(handleCreateBranch(cfg), branchServiceDep(cfg), templateStoreDep(cfg)))

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
	), withToolDeps(handleUpdateBranch(cfg), branchStoreDep(cfg), branchServiceDep(cfg)))

	s.AddTool(mcpSDK.NewTool("tailor_resume",
		mcpSDK.WithDescription("Initiates an asynchronous tailoring generation pipeline for a branch against job requirements, applying XML boundary fencing."),
		mcpSDK.WithString("branch_id", mcpSDK.Required(), mcpSDK.Description("ID of the target resume branch")),
		mcpSDK.WithString("job_description", mcpSDK.Required(), mcpSDK.Description("Target job description text")),
		mcpSDK.WithString("custom_instructions", mcpSDK.Description("Optional candidate directives or emphasis instructions")),
		mcpSDK.WithString("provider_override", mcpSDK.Description("Optional LLM provider override")),
		mcpSDK.WithString("model_override", mcpSDK.Description("Optional LLM model override")),
		mcpSDK.WithString("reasoning_level", mcpSDK.Description("Optional reasoning effort constraint for thinking models: 'none', 'low', 'medium', 'high'")),
	), withToolDeps(handleTailorResume(cfg), branchStoreDep(cfg), branchServiceDep(cfg)))

	s.AddTool(mcpSDK.NewTool("run_gap_analysis",
		mcpSDK.WithDescription("Executes a decoupled ATS keyword and qualification gap analysis between the candidate's career profile and target job description."),
		mcpSDK.WithString("job_description", mcpSDK.Required(), mcpSDK.Description("Target job description or requirements text")),
		mcpSDK.WithString("branch_id", mcpSDK.Description("Optional branch ID to contextually align analysis against")),
	), withToolDeps(handleRunGapAnalysis(cfg), branchStoreDep(cfg), branchServiceDep(cfg)))

	s.AddTool(mcpSDK.NewTool("simulate_ats",
		mcpSDK.WithDescription("Executes on-demand ATS extraction confidence and job matching score evaluation for a completed tailored branch version."),
		mcpSDK.WithString("branch_id", mcpSDK.Required(), mcpSDK.Description("ID of the target tailored resume branch")),
		mcpSDK.WithString("version_id", mcpSDK.Description("Optional version ID or sequential version number (defaults to the active version)")),
		mcpSDK.WithString("format", mcpSDK.Description("Optional text resume format to parse: 'markdown', 'html', or 'text' (defaults to 'markdown')")),
	), withToolDeps(handleSimulateATS(cfg), branchServiceDep(cfg)))
}

// handleListBranches handles tool 'list_branches', enumerating all resume branches for the authenticated user.
func handleListBranches(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, _ mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		branches, err := cfg.BranchStore.ListBranches(ctx, userID)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to list branches", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to list branches due to an internal store error"), nil
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

		branchID := optionalTrimmedString(args, "branch_id")
		if branchID == "" {
			return mcpSDK.NewToolResultError("branch_id is required"), nil
		}

		outputFormat, ok := parseRenderOutputFormat(optionalTrimmedString(args, "format"))
		if !ok {
			return mcpSDK.NewToolResultError("format must be 'html', 'markdown', or 'text'"), nil
		}

		branch, err := cfg.BranchStore.Branch(ctx, userID, branchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", branchID)), nil
		}

		ver, errMsg := resolveRenderBranchVersion(ctx, cfg, branch, args)
		if errMsg != "" {
			return mcpSDK.NewToolResultError(errMsg), nil
		}

		templateSetID := resolveRenderTemplateID(args, branch, ver)
		tmpl, err := access.Template(ctx, cfg.TemplateStore, userID, templateSetID)
		if err != nil {
			if msg, ok := access.StoreUnavailableMessage(err); ok {
				return mcpSDK.NewToolResultError(msg), nil
			}
			return mcpSDK.NewToolResultError(fmt.Sprintf("template set %q not found or access denied", templateSetID)), nil
		}

		output := renderBranchOutput(ctx, cfg, userID, tmpl, ver, outputFormat)
		return mcpSDK.NewToolResultText(output), nil
	}
}

// parseRenderOutputFormat maps an input format string to its canonical core.OutputFormat constant.
func parseRenderOutputFormat(raw string) (core.OutputFormat, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "html":
		return core.OutputFormatHTML, true
	case "markdown":
		return core.OutputFormatMarkdown, true
	case "text", "plain_text":
		return core.OutputFormatPlainText, true
	default:
		return "", false
	}
}

// resolveRenderBranchVersion looks up the requested version number or active version of branch.
func resolveRenderBranchVersion(ctx context.Context, cfg ServerConfig, branch *core.Branch, args map[string]any) (*core.BranchVersion, string) {
	versionNum := parseVersionNumberArg(args["version_number"])
	if versionNum > 0 {
		ver, err := cfg.BranchStore.VersionByNumber(ctx, branch.ID, versionNum)
		if err != nil || ver == nil {
			return nil, fmt.Sprintf("version number %d not found for branch %q", versionNum, branch.ID)
		}
		return ver, ""
	}

	if branch.ActiveVersionID != nil && *branch.ActiveVersionID != "" {
		ver, err := cfg.BranchStore.Version(ctx, branch.ID, *branch.ActiveVersionID)
		if err == nil && ver != nil {
			return ver, ""
		}
	}

	return nil, fmt.Sprintf("no completed version found for branch %q", branch.ID)
}

// parseVersionNumberArg converts a JSON number or string version_number argument to an int.
func parseVersionNumberArg(raw any) int {
	switch val := raw.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case string:
		parsed, _ := strconv.Atoi(val)
		return parsed
	default:
		return 0
	}
}

// resolveRenderTemplateID selects the template set ID across request overrides, version defaults, branch defaults, and the built-in fallback.
func resolveRenderTemplateID(args map[string]any, branch *core.Branch, ver *core.BranchVersion) string {
	for _, candidate := range []string{
		optionalTrimmedString(args, "template_set_id", "template_id"),
		ver.TemplateSetIDUsed,
		branch.TemplateSetID,
	} {
		if candidate != "" {
			return candidate
		}
	}
	return defaultTemplateSetID
}

// renderBranchOutput renders ver using tmpl when structured content is available, falling back to stored outputs and sanitizing HTML.
func renderBranchOutput(ctx context.Context, cfg ServerConfig, userID string, tmpl *core.TemplateSet, ver *core.BranchVersion, outputFormat core.OutputFormat) string {
	var output string
	var prof *core.Profile
	if cfg.ProfileStore != nil {
		prof, _ = cfg.ProfileStore.Profile(ctx, userID)
	}
	if cfg.TemplateRenderer != nil && prof != nil && ver.TailoredContent != nil {
		rendered, rErr := cfg.TemplateRenderer.Render(ctx, tmpl, prof, ver.TailoredContent)
		if rErr == nil && rendered != nil {
			output = extractFormatOutput(outputFormat, rendered.HTML, rendered.Markdown, rendered.PlainText)
		}
	}
	if output == "" {
		output = extractFormatOutput(outputFormat, ver.HTMLOutput, ver.MarkdownOutput, ver.PlainTextOutput)
	}
	if outputFormat == core.OutputFormatHTML {
		output = renderer.StripScriptTags(output)
	}
	return output
}

// handleCreateBranch handles tool 'create_branch', creating a new targeted resume branch with XML boundary fencing.
func handleCreateBranch(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		name := optionalTrimmedString(args, "name")
		targetCompany := optionalTrimmedString(args, "target_company")
		targetRole := optionalTrimmedString(args, "target_role")
		targetRequirements, _ := args["target_requirements"].(string)
		strategyID := optionalTrimmedString(args, "tailoring_strategy_id")
		templateSetID := optionalTrimmedString(args, "template_set_id")
		providerOverride := optionalTrimmedString(args, "provider_override")
		modelOverride := optionalTrimmedString(args, "model_override")
		reasoningLevel := strings.ToLower(optionalTrimmedString(args, "reasoning_level"))

		if err := core.ValidateReasoningLevel(reasoningLevel); err != nil {
			return mcpSDK.NewToolResultError("reasoning_level must be one of: none, low, medium, high"), nil
		}

		isAdmin := cfg.Admin || core.IsAdminFromContext(ctx)
		if !isAdmin && (providerOverride != "" || modelOverride != "" || reasoningLevel != "") {
			return mcpSDK.NewToolResultError("model and reasoning selection are restricted to administrators"), nil
		}

		if name == "" || targetCompany == "" || targetRole == "" {
			return mcpSDK.NewToolResultError("name, target_company, and target_role are required"), nil
		}

		strategyPtr, errMsg := validateBranchPresetAccess(ctx, cfg, userID, strategyID)
		if errMsg != "" {
			return mcpSDK.NewToolResultError(errMsg), nil
		}

		if templateSetID == "" {
			templateSetID = defaultTemplateSetID
		}
		if _, err := access.Template(ctx, cfg.TemplateStore, userID, templateSetID); err != nil {
			if msg, ok := access.StoreUnavailableMessage(err); ok {
				return mcpSDK.NewToolResultError(msg), nil
			}
			return mcpSDK.NewToolResultError(fmt.Sprintf("template set %q not found or access denied", templateSetID)), nil
		}

		fencedRequirements := targetRequirements
		if strings.TrimSpace(targetRequirements) != "" {
			fencedRequirements = SanitizeXMLFence(targetRequirements, "target_job_description")
		}

		branch := &core.Branch{
			ID:                  "branch-" + uuid.New().String(),
			UserID:              userID,
			Name:                name,
			TargetCompany:       targetCompany,
			TargetRole:          targetRole,
			TargetRequirements:  fencedRequirements,
			TailoringStrategyID: strategyPtr,
			PresetID:            strategyPtr,
			TemplateSetID:       templateSetID,
			ProviderOverride:    providerOverride,
			ModelOverride:       modelOverride,
			ReasoningLevel:      reasoningLevel,
		}

		createdBranch, err := cfg.BranchService.CreateBranch(ctx, branch)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create branch", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create branch due to an internal store error"), nil
		}

		res, err := jsonToolResult(createdBranch)
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize created branch"), nil
		}
		return res, nil
	}
}

// validateBranchPresetAccess verifies that an optional tailoring strategy preset is accessible to userID.
func validateBranchPresetAccess(ctx context.Context, cfg ServerConfig, userID, strategyID string) (*string, string) {
	if strategyID == "" {
		return nil, ""
	}
	if _, err := access.Preset(ctx, cfg.PresetStore, userID, cfg.DefaultUserID, strategyID); err != nil {
		if msg, ok := access.StoreUnavailableMessage(err); ok {
			return nil, msg
		}
		return nil, fmt.Sprintf("tailoring strategy %q not found or access denied", strategyID)
	}
	return &strategyID, ""
}

// handleUpdateBranch handles tool 'update_branch', updating an existing targeted resume branch.
func handleUpdateBranch(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		branchID := optionalTrimmedString(args, "id", "branch_id")
		if branchID == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		branch, err := cfg.BranchStore.Branch(ctx, userID, branchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", branchID)), nil
		}

		applyBranchTextUpdates(branch, args)
		if errMsg := applyBranchReferenceUpdates(ctx, cfg, userID, branch, args); errMsg != "" {
			return mcpSDK.NewToolResultError(errMsg), nil
		}
		if errMsg := applyBranchModelUpdates(ctx, cfg, branch, args, true); errMsg != "" {
			return mcpSDK.NewToolResultError(errMsg), nil
		}

		updatedBranch, err := cfg.BranchService.UpdateBranch(ctx, branch)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to update branch", "branch_id", branchID, "err", err)
			return mcpSDK.NewToolResultError(mcpDomainErrorMessage("update branch", err)), nil
		}

		res, err := jsonToolResult(updatedBranch)
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize updated branch"), nil
		}
		return res, nil
	}
}

// applyBranchTextUpdates applies mutable name, target, and fenced directive text fields onto branch.
func applyBranchTextUpdates(branch *core.Branch, args map[string]any) {
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
			branch.TargetRequirements = SanitizeXMLFence(reqs, "target_job_description")
		} else {
			branch.TargetRequirements = ""
		}
	}
	if dirs, ok := args["custom_directives"].(string); ok {
		dirs = strings.TrimSpace(dirs)
		if dirs != "" {
			fenced := SanitizeXMLFence(dirs, "custom_instructions")
			branch.CustomDirectives = fenced
			branch.BranchInstructions = fenced
		} else {
			branch.CustomDirectives = ""
			branch.BranchInstructions = ""
		}
	}
}

// applyBranchReferenceUpdates validates and applies preset and template set reference updates onto branch.
func applyBranchReferenceUpdates(ctx context.Context, cfg ServerConfig, userID string, branch *core.Branch, args map[string]any) string {
	if rawStrategy, ok := args["tailoring_strategy_id"].(string); ok {
		strategyID := strings.TrimSpace(rawStrategy)
		if strategyID != "" {
			strategyPtr, errMsg := validateBranchPresetAccess(ctx, cfg, userID, strategyID)
			if errMsg != "" {
				return errMsg
			}
			branch.TailoringStrategyID = strategyPtr
			branch.PresetID = strategyPtr
		} else {
			branch.TailoringStrategyID = nil
			branch.PresetID = nil
		}
	}
	if tmplID, ok := args["template_set_id"].(string); ok {
		tmplID = strings.TrimSpace(tmplID)
		if tmplID != "" {
			if _, err := access.Template(ctx, cfg.TemplateStore, userID, tmplID); err != nil {
				if msg, ok := access.StoreUnavailableMessage(err); ok {
					return msg
				}
				return fmt.Sprintf("template set %q not found or access denied", tmplID)
			}
			branch.TemplateSetID = tmplID
		}
	}
	return ""
}

// applyBranchModelUpdates enforces administrator permissions and applies LLM override arguments onto branch.
func applyBranchModelUpdates(ctx context.Context, cfg ServerConfig, branch *core.Branch, args map[string]any, allowEmpty bool) string {
	isAdmin := cfg.Admin || core.IsAdminFromContext(ctx)
	if !isAdmin && (args["provider_override"] != nil || args["model_override"] != nil || args["reasoning_level"] != nil) {
		return "model and reasoning selection are restricted to administrators"
	}

	if prov, ok := args["provider_override"].(string); ok {
		trimmed := strings.TrimSpace(prov)
		if allowEmpty || trimmed != "" {
			branch.ProviderOverride = trimmed
		}
	}
	if mod, ok := args["model_override"].(string); ok {
		trimmed := strings.TrimSpace(mod)
		if allowEmpty || trimmed != "" {
			branch.ModelOverride = trimmed
		}
	}
	if rLvl, ok := args["reasoning_level"].(string); ok {
		trimmed := strings.TrimSpace(strings.ToLower(rLvl))
		if allowEmpty || trimmed != "" {
			if err := core.ValidateReasoningLevel(trimmed); err != nil {
				return "reasoning_level must be one of: none, low, medium, high"
			}
			branch.ReasoningLevel = trimmed
		}
	}
	return ""
}

// handleTailorResume handles tool 'tailor_resume', launching the asynchronous resume tailoring generation pipeline.
func handleTailorResume(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		branchID := optionalTrimmedString(args, "branch_id")
		jobDescription := optionalTrimmedString(args, "job_description")
		customInstructions := optionalTrimmedString(args, "custom_instructions")

		if branchID == "" || jobDescription == "" {
			return mcpSDK.NewToolResultError("branch_id and job_description are required"), nil
		}

		branch, err := cfg.BranchStore.Branch(ctx, userID, branchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", branchID)), nil
		}

		// Apply XML boundary fencing to untrusted client inputs before persisting and queuing tailoring.
		branch.TargetRequirements = SanitizeXMLFence(jobDescription, "target_job_description")
		if customInstructions != "" {
			branch.CustomDirectives = SanitizeXMLFence(customInstructions, "custom_instructions")
			branch.BranchInstructions = branch.CustomDirectives
		}

		if errMsg := applyBranchModelUpdates(ctx, cfg, branch, args, false); errMsg != "" {
			return mcpSDK.NewToolResultError(errMsg), nil
		}

		if err := cfg.BranchStore.UpdateBranch(ctx, branch); err != nil {
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

		res, err := jsonToolResult(tailorResumePayload{
			Status:    "tailoring_initiated",
			BranchID:  branchID,
			VersionID: ver.ID,
			Version:   ver,
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

		jobDescription := optionalTrimmedString(args, "job_description")
		if jobDescription == "" {
			return mcpSDK.NewToolResultError("job_description is required"), nil
		}

		targetBranchID := resolveGapAnalysisBranchID(ctx, cfg, userID, optionalTrimmedString(args, "branch_id"))
		if targetBranchID == "" {
			return mcpSDK.NewToolResultError("no target branch available for gap analysis; please create a branch first"), nil
		}

		branch, err := cfg.BranchStore.Branch(ctx, userID, targetBranchID)
		if err != nil || branch == nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("branch with ID %q not found or access denied", targetBranchID)), nil
		}

		branch.TargetRequirements = SanitizeXMLFence(jobDescription, "target_job_description")
		_ = cfg.BranchStore.UpdateBranch(ctx, branch)

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

// resolveGapAnalysisBranchID picks branchID when specified, otherwise falling back to the user's default or first branch.
func resolveGapAnalysisBranchID(ctx context.Context, cfg ServerConfig, userID, branchID string) string {
	if branchID != "" {
		return branchID
	}
	branches, err := cfg.BranchStore.ListBranches(ctx, userID)
	if err != nil {
		return ""
	}
	for _, b := range branches {
		if b.IsDefault {
			return b.ID
		}
	}
	if len(branches) > 0 {
		return branches[0].ID
	}
	return ""
}

// handleSimulateATS handles tool 'simulate_ats', executing on-demand ATS extraction and job matching score evaluation for a tailored branch version.
func handleSimulateATS(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)
		args := parseArguments(req)

		branchID := optionalTrimmedString(args, "branch_id")
		if branchID == "" {
			return mcpSDK.NewToolResultError("branch_id is required"), nil
		}

		score, err := cfg.BranchService.SimulateATS(ctx, userID, branchID, &core.ATSSimulationOptions{
			VersionID: optionalTrimmedString(args, "version_id"),
			Format:    optionalTrimmedString(args, "format"),
		})
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to simulate ATS score", "branch_id", branchID, "err", err)
			return mcpSDK.NewToolResultError(mcpDomainErrorMessage("simulate ATS score", err)), nil
		}

		res, err := jsonToolResult(score)
		if err != nil {
			return mcpSDK.NewToolResultError("failed to serialize ATS score"), nil
		}
		return res, nil
	}
}
