package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/resumeee-api/internal/access"
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
	return func(ctx context.Context, _ mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		snapshot, err := fetchProfileSnapshot(ctx, cfg, userID)
		if err != nil {
			if msg, ok := access.StoreUnavailableMessage(err); ok {
				return nil, errors.New(msg)
			}
			cfg.Logger.ErrorContext(ctx, "failed to read main profile resource", "user_id", userID, "err", err)
			return nil, errors.New("failed to retrieve profile due to an internal store error")
		}

		contents, err := jsonResourceContents("resumeee://profile/main", snapshot)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to serialize profile snapshot", "user_id", userID, "err", err)
			return nil, errors.New("failed to serialize profile snapshot")
		}

		return contents, nil
	}
}

// supportedLanguagesPayload serializes the default and supported tailoring languages.
type supportedLanguagesPayload struct {
	DefaultLanguage    string   `json:"default_language"`
	SupportedLanguages []string `json:"supported_languages"`
}

// branchDetailsPayload serializes a branch alongside its active version when present.
type branchDetailsPayload struct {
	Branch        *core.Branch        `json:"branch"`
	ActiveVersion *core.BranchVersion `json:"active_version"`
}

// handleResourceSupportedLanguages returns the supported and default languages for resume tailoring.
func handleResourceSupportedLanguages(_ ServerConfig) serverSDK.ResourceHandlerFunc {
	return func(_ context.Context, _ mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
		payload := supportedLanguagesPayload{
			DefaultLanguage:    core.DefaultLanguage,
			SupportedLanguages: core.SupportedLanguages,
		}
		return jsonResourceContents("resumeee://languages", payload)
	}
}

// handleResourceBranchDetails constructs a ResourceTemplateHandlerFunc that reads a targeted resume branch
// by identifier, checks multi-tenant user ownership, attaches active version details, and returns JSON.
//
// Parameters:
//   - cfg: ServerConfig providing branch store access and REST timeout bounds.
//
// Returns:
//   - ResourceTemplateHandlerFunc compatible with the MCP SDK.
func handleResourceBranchDetails(cfg ServerConfig) serverSDK.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcpSDK.ReadResourceRequest) ([]mcpSDK.ResourceContents, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		if cfg.BranchStore == nil {
			return nil, errors.New("branch store unavailable")
		}

		branchID := strings.TrimPrefix(req.Params.URI, "resumeee://branches/")
		branchID = strings.TrimSpace(branchID)
		if branchID == "" {
			return nil, errors.New("invalid branch URI: missing branch ID")
		}

		branch, err := cfg.BranchStore.Branch(ctx, userID, branchID)
		if err != nil || branch == nil {
			cfg.Logger.WarnContext(ctx, "branch resource not found or access denied", "branch_id", branchID, "user_id", userID)
			return nil, errors.New("branch not found or access denied")
		}

		var activeVer *core.BranchVersion
		if branch.ActiveVersionID != nil && *branch.ActiveVersionID != "" {
			if v, vErr := cfg.BranchStore.Version(ctx, branch.ID, *branch.ActiveVersionID); vErr == nil && v != nil {
				activeVer = v
			}
		}

		payload := branchDetailsPayload{
			Branch:        branch,
			ActiveVersion: activeVer,
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
//   - cfg: ServerConfig providing template store access and REST timeout bounds.
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

		tmpl, err := access.Template(ctx, cfg.TemplateStore, userID, templateID)
		if err != nil {
			if msg, ok := access.StoreUnavailableMessage(err); ok {
				return nil, errors.New(msg)
			}
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

		preset, err := access.Preset(ctx, cfg.PresetStore, userID, cfg.DefaultUserID, presetID)
		if err != nil {
			if msg, ok := access.StoreUnavailableMessage(err); ok {
				return nil, errors.New(msg)
			}
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
