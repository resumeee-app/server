package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/server/internal/access"
)

// registerPresetTools registers tailoring strategy preset and language discovery tools onto s.
func registerPresetTools(s *serverSDK.MCPServer, cfg ServerConfig) {
	s.AddTool(mcpSDK.NewTool("list_presets",
		mcpSDK.WithDescription("Lists all tailoring strategy presets accessible to the authenticated user, including system built-in presets and custom tenant presets."),
		mcpSDK.WithString("language", mcpSDK.Description("Optional filter by target language (e.g., 'English', 'Spanish')")),
	), withToolDeps(handleListPresets(cfg), presetStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("get_preset",
		mcpSDK.WithDescription("Retrieves the full configuration and system instructions for a specific tailoring strategy preset."),
		mcpSDK.WithString("id", mcpSDK.Required(), mcpSDK.Description("ID of the target tailoring preset")),
	), withToolDeps(handleGetPreset(cfg), presetStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("create_preset",
		mcpSDK.WithDescription("Creates a new custom tailoring strategy preset for the authenticated user with customizable prompt instructions and language."),
		mcpSDK.WithString("name", mcpSDK.Required(), mcpSDK.Description("Descriptive name for the preset")),
		mcpSDK.WithString("system_instructions", mcpSDK.Required(), mcpSDK.Description("Core system instructions and tailoring prompt strategy guidelines")),
		mcpSDK.WithString("description", mcpSDK.Description("Optional human-readable description of the preset purpose")),
		mcpSDK.WithString("suggested_theme_id", mcpSDK.Description("Optional suggested visual theme / template set ID")),
		mcpSDK.WithString("language", mcpSDK.Description("Target output language for contextual translation (e.g. 'English', 'Spanish', 'French'). Defaults to 'English'. RTL languages are rejected.")),
	), withToolDeps(handleCreatePreset(cfg), presetStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("update_preset",
		mcpSDK.WithDescription("Updates mutable fields on an existing custom tailoring strategy preset. Built-in presets are immutable and cannot be updated."),
		mcpSDK.WithString("id", mcpSDK.Required(), mcpSDK.Description("ID of the target preset to update")),
		mcpSDK.WithString("name", mcpSDK.Description("Updated descriptive name")),
		mcpSDK.WithString("description", mcpSDK.Description("Updated description")),
		mcpSDK.WithString("system_instructions", mcpSDK.Description("Updated system instructions")),
		mcpSDK.WithString("suggested_theme_id", mcpSDK.Description("Updated suggested visual theme / template set ID")),
		mcpSDK.WithString("language", mcpSDK.Description("Updated target output language (validated, RTL languages rejected)")),
	), withToolDeps(handleUpdatePreset(cfg), presetStoreDep(cfg)))

	s.AddTool(mcpSDK.NewTool("list_supported_languages",
		mcpSDK.WithDescription("Lists all supported left-to-right output languages and the default language ('English') for resume tailoring and presets."),
	), handleListSupportedLanguages(cfg))
}

// handleListPresets handles tool 'list_presets', enumerating all presets visible to the user.
func handleListPresets(cfg ServerConfig) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RESTTimeout)
		defer cancel()

		userID := UserIDFromContext(ctx, cfg.DefaultUserID)
		_ = cfg.EnsureTenant(ctx, userID)

		presets, err := cfg.PresetStore.ListPresets(ctx, userID)
		if err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to list presets", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to list presets due to an internal store error"), nil
		}
		if presets == nil {
			presets = []core.Preset{}
		}

		args := parseArguments(req)
		if targetLang := optionalTrimmedString(args, "language"); targetLang != "" {
			filtered := make([]core.Preset, 0, len(presets))
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

		args := parseArguments(req)
		presetID := optionalTrimmedString(args, "id")
		if presetID == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		preset, err := access.Preset(ctx, cfg.PresetStore, userID, cfg.DefaultUserID, presetID)
		if err != nil {
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

		args := parseArguments(req)
		name := optionalTrimmedString(args, "name")
		instructions := optionalTrimmedString(args, "system_instructions")
		if name == "" || instructions == "" {
			return mcpSDK.NewToolResultError("name and system_instructions are required fields"), nil
		}

		desc, _ := args["description"].(string)
		suggestedThemeID := optionalTrimmedString(args, "suggested_theme_id")
		if suggestedThemeID != "" {
			if _, err := access.Template(ctx, cfg.TemplateStore, userID, suggestedThemeID); err != nil {
				if msg, ok := access.StoreUnavailableMessage(err); ok {
					return mcpSDK.NewToolResultError(msg), nil
				}
				return mcpSDK.NewToolResultError(fmt.Sprintf("cannot access foreign or nonexistent template %q", suggestedThemeID)), nil
			}
		}

		langRaw, _ := args["language"].(string)
		validatedLang, err := core.ValidateLanguage(langRaw)
		if err != nil {
			return mcpSDK.NewToolResultError(err.Error()), nil
		}

		var suggestedTheme *string
		if suggestedThemeID != "" {
			suggestedTheme = &suggestedThemeID
		}

		preset := core.Preset{
			ID:                 "preset-" + uuid.New().String(),
			UserID:             userID,
			Name:               name,
			Description:        desc,
			SystemInstructions: instructions,
			SuggestedThemeID:   suggestedTheme,
			Language:           validatedLang,
		}

		if err := cfg.PresetStore.CreatePreset(ctx, &preset); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to create preset", "user_id", userID, "err", err)
			return mcpSDK.NewToolResultError("failed to create preset due to an internal store error"), nil
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

		args := parseArguments(req)
		presetID := optionalTrimmedString(args, "id")
		if presetID == "" {
			return mcpSDK.NewToolResultError("id is required"), nil
		}

		preset, err := access.Preset(ctx, cfg.PresetStore, userID, cfg.DefaultUserID, presetID)
		if err != nil {
			return mcpSDK.NewToolResultError(fmt.Sprintf("preset with ID %q not found or access denied", presetID)), nil
		}

		if access.IsSystemPreset(preset) {
			return mcpSDK.NewToolResultError("built-in presets cannot be modified (immutable)"), nil
		}

		if errMsg := applyPresetUpdates(ctx, cfg, userID, preset, args); errMsg != "" {
			return mcpSDK.NewToolResultError(errMsg), nil
		}

		if err := cfg.PresetStore.UpdatePreset(ctx, preset); err != nil {
			cfg.Logger.ErrorContext(ctx, "failed to update preset", "preset_id", presetID, "err", err)
			return mcpSDK.NewToolResultError("failed to update preset due to an internal store error"), nil
		}

		return jsonToolResult(preset)
	}
}

// applyPresetUpdates validates and applies mutable preset field updates onto preset.
func applyPresetUpdates(ctx context.Context, cfg ServerConfig, userID string, preset *core.Preset, args map[string]any) string {
	if name, ok := args["name"].(string); ok && strings.TrimSpace(name) != "" {
		preset.Name = strings.TrimSpace(name)
	}
	if desc, ok := args["description"].(string); ok {
		preset.Description = strings.TrimSpace(desc)
	}
	if sys, ok := args["system_instructions"].(string); ok && strings.TrimSpace(sys) != "" {
		preset.SystemInstructions = strings.TrimSpace(sys)
	}
	if rawThemeID, ok := args["suggested_theme_id"].(string); ok {
		themeID := strings.TrimSpace(rawThemeID)
		if themeID == "" {
			preset.SuggestedThemeID = nil
		} else {
			if _, err := access.Template(ctx, cfg.TemplateStore, userID, themeID); err != nil {
				if msg, ok := access.StoreUnavailableMessage(err); ok {
					return msg
				}
				return fmt.Sprintf("cannot access foreign or nonexistent template %q", themeID)
			}
			preset.SuggestedThemeID = &themeID
		}
	}
	if langRaw, ok := args["language"].(string); ok {
		validatedLang, err := core.ValidateLanguage(langRaw)
		if err != nil {
			return err.Error()
		}
		preset.Language = validatedLang
	}
	return ""
}

// handleListSupportedLanguages handles tool 'list_supported_languages', returning supported Left-to-Right languages.
func handleListSupportedLanguages(_ ServerConfig) serverSDK.ToolHandlerFunc {
	return func(_ context.Context, _ mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		return jsonToolResult(supportedLanguagesPayload{
			DefaultLanguage:    core.DefaultLanguage,
			SupportedLanguages: core.SupportedLanguages,
		})
	}
}
