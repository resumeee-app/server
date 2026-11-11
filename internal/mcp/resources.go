package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-core/domain"
)

// jsonResourceContents serializes any data structure into indented JSON text resource contents.
//
// Parameters:
//   - uri: Canonical URI of the resource being represented.
//   - v: Payload value to serialize into JSON.
//
// Returns:
//   - Slice of ResourceContents containing a single TextResourceContents entry, or an error.
func jsonResourceContents(uri string, v any) ([]mcpSDK.ResourceContents, error) {
	bytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return []mcpSDK.ResourceContents{
		mcpSDK.TextResourceContents{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(bytes),
		},
	}, nil
}

// registerResources binds all static and templated MCP resources onto the provided server.
//
// Parameters:
//   - s: Pointer to the MCPServer instance.
//   - cfg: ServerConfig specifying dependencies and timeouts.
func registerResources(s *serverSDK.MCPServer, cfg ServerConfig) {
	// 1. resumeee://profile/main
	s.AddResource(
		mcpSDK.NewResource("resumeee://profile/main", "Main Profile",
			mcpSDK.WithResourceDescription("Verified canonical main career profile snapshot for the authenticated user"),
			mcpSDK.WithMIMEType("application/json"),
		),
		handleResourceMainProfile(cfg),
	)

	// 2. resumeee://languages
	s.AddResource(
		mcpSDK.NewResource("resumeee://languages", "Supported Languages",
			mcpSDK.WithResourceDescription("Supported Left-to-Right languages and canonical default language for resume tailoring"),
			mcpSDK.WithMIMEType("application/json"),
		),
		handleResourceSupportedLanguages(cfg),
	)

	// 3. resumeee://branches/{id}
	s.AddResourceTemplate(
		mcpSDK.NewResourceTemplate("resumeee://branches/{id}", "Branch Details",
			mcpSDK.WithTemplateDescription("Resume branch metadata, target requirements, and active tailored version content"),
			mcpSDK.WithTemplateMIMEType("application/json"),
		),
		handleResourceBranchDetails(cfg),
	)

	// 4. resumeee://templates/{id}
	s.AddResourceTemplate(
		mcpSDK.NewResourceTemplate("resumeee://templates/{id}", "Template Set",
			mcpSDK.WithTemplateDescription("Multi-format HTML, Markdown, and text resume template definition"),
			mcpSDK.WithTemplateMIMEType("application/json"),
		),
		handleResourceTemplateSet(cfg),
	)

	// 5. resumeee://presets/{id}
	s.AddResourceTemplate(
		mcpSDK.NewResourceTemplate("resumeee://presets/{id}", "Tailoring Preset",
			mcpSDK.WithTemplateDescription("Tailoring strategy preset details, instructions, language, and model configuration"),
			mcpSDK.WithTemplateMIMEType("application/json"),
		),
		handleResourcePresetDetails(cfg),
	)
}

// handleResourceMainProfile constructs a ResourceHandlerFunc that retrieves the authenticated user's
// canonical main career profile snapshot and returns it formatted as JSON.
//
// Parameters:
//   - cfg: ServerConfig providing profile service and REST timeout bounds.
//
// Returns:
//   - ResourceHandlerFunc compatible with the MCP SDK.
func handleResourceMainProfile(cfg ServerConfig) serverSDK.ResourceHandlerFunc {
	return func(ctx context.Context, req mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
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
			return nil, errors.New("profile service and repository unavailable")
		}

		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to read main profile resource", "user_id", userID, "err", err)
			return nil, errors.New("failed to retrieve profile due to an internal repository error")
		}

		contents, err := jsonResourceContents("resumeee://profile/main", snapshot)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to serialize profile snapshot", "user_id", userID, "err", err)
			return nil, errors.New("failed to serialize profile snapshot")
		}

		return contents, nil
	}
}

// handleResourceSupportedLanguages returns the supported and default languages for resume tailoring.
func handleResourceSupportedLanguages(_ ServerConfig) serverSDK.ResourceHandlerFunc {
	return func(ctx context.Context, req mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
		payload := map[string]any{
			"default_language":    domain.DefaultLanguage,
			"supported_languages": domain.SupportedLanguages,
		}
		return jsonResourceContents("resumeee://languages", payload)
	}
}

// handleResourceBranchDetails constructs a ResourceTemplateHandlerFunc that reads a targeted resume branch
// by identifier, checks multi-tenant user ownership, attaches active version details, and returns JSON.
//
// Parameters:
//   - cfg: ServerConfig providing branch repository access and REST timeout bounds.
//
// Returns:
//   - ResourceTemplateHandlerFunc compatible with the MCP SDK.
func handleResourceBranchDetails(cfg ServerConfig) serverSDK.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		branchID := strings.TrimPrefix(req.Params.URI, "resumeee://branches/")
		branchID = strings.TrimSpace(branchID)
		if branchID == "" {
			return nil, errors.New("invalid branch URI: missing branch ID")
		}

		branch, err := cfg.BranchRepo.GetBranch(ctx, userID, branchID)
		if err != nil || branch == nil {
			cfg.Logger.WarnContext(ctx, "branch resource not found or access denied", "branch_id", branchID, "user_id", userID)
			return nil, errors.New("branch not found or access denied")
		}

		var activeVer any
		if branch.ActiveVersionID != nil && *branch.ActiveVersionID != "" {
			if v, vErr := cfg.BranchRepo.GetVersion(ctx, branch.ID, *branch.ActiveVersionID); vErr == nil && v != nil {
				activeVer = v
			}
		}

		payload := map[string]any{
			"branch":         branch,
			"active_version": activeVer,
		}

		contents, err := jsonResourceContents(req.Params.URI, payload)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to serialize branch resource", "branch_id", branchID, "err", err)
			return nil, errors.New("failed to serialize branch resource")
		}

		return contents, nil
	}
}

// handleResourceTemplateSet constructs a ResourceTemplateHandlerFunc that retrieves a template set
// definition by identifier, checking multi-tenant ownership and built-in visibility.
//
// Parameters:
//   - cfg: ServerConfig providing template repository access and REST timeout bounds.
//
// Returns:
//   - ResourceTemplateHandlerFunc compatible with the MCP SDK.
func handleResourceTemplateSet(cfg ServerConfig) serverSDK.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		templateID := strings.TrimPrefix(req.Params.URI, "resumeee://templates/")
		templateID = strings.TrimSpace(templateID)
		if templateID == "" {
			return nil, errors.New("invalid template URI: missing template ID")
		}

		tmpl, err := cfg.TemplateRepo.GetTemplate(ctx, userID, templateID)
		if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn && tmpl.UserID != domain.SystemUserID) {
			cfg.Logger.WarnContext(ctx, "template resource not found or access denied", "template_id", templateID, "user_id", userID)
			return nil, errors.New("template not found or access denied")
		}

		contents, err := jsonResourceContents(req.Params.URI, tmpl)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to serialize template resource", "template_id", templateID, "err", err)
			return nil, errors.New("failed to serialize template resource")
		}

		return contents, nil
	}
}

// handleResourcePresetDetails retrieves a tailoring strategy preset by identifier.
func handleResourcePresetDetails(cfg ServerConfig) serverSDK.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		presetID := strings.TrimPrefix(req.Params.URI, "resumeee://presets/")
		presetID = strings.TrimSpace(presetID)
		if presetID == "" {
			return nil, errors.New("invalid preset URI: missing preset ID")
		}

		if cfg.PresetRepo == nil {
			return nil, errors.New("preset repository unavailable")
		}

		preset, err := cfg.PresetRepo.GetPreset(ctx, userID, presetID)
		if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn() && preset.UserID != domain.SystemUserID) {
			cfg.Logger.WarnContext(ctx, "preset resource not found or access denied", "preset_id", presetID, "user_id", userID)
			return nil, errors.New("preset not found or access denied")
		}

		contents, err := jsonResourceContents(req.Params.URI, preset)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to serialize preset resource", "preset_id", presetID, "err", err)
			return nil, errors.New("failed to serialize preset resource")
		}

		return contents, nil
	}
}
