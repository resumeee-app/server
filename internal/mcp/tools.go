package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/core"
)

// defaultTemplateSetID is the fallback built-in template set identifier when none is specified.
const defaultTemplateSetID = core.TemplateStandardCleanID

// parseArguments extracts and casts the tool arguments map from a CallToolRequest.
func parseArguments(req mcpSDK.CallToolRequest) map[string]any {
	if m, ok := req.Params.Arguments.(map[string]any); ok && m != nil {
		return m
	}
	return make(map[string]any)
}

// optionalTrimmedString returns the trimmed string value of the first non-empty matching key in args.
func optionalTrimmedString(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := args[key].(string); ok {
			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// jsonToolResult marshals any payload value into an indented JSON tool text result.
func jsonToolResult(v any) (*mcpSDK.CallToolResult, error) {
	bytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return mcpSDK.NewToolResultText(string(bytes)), nil
}

// extractFormatOutput maps canonical core.OutputFormat constants to the appropriate output string.
func extractFormatOutput(format core.OutputFormat, html, md, text string) string {
	switch format {
	case core.OutputFormatHTML:
		return html
	case core.OutputFormatMarkdown:
		return md
	case core.OutputFormatPlainText:
		return text
	default:
		return ""
	}
}

// toolDepCheck pairs a dependency availability condition with the MCP tool error returned when missing.
type toolDepCheck struct {
	ok  bool
	msg string
}

// withToolDeps wraps handler with precondition checks returning an MCP tool error if a dependency is nil.
func withToolDeps(handler serverSDK.ToolHandlerFunc, checks ...toolDepCheck) serverSDK.ToolHandlerFunc {
	return func(ctx context.Context, req mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
		for _, c := range checks {
			if !c.ok {
				return mcpSDK.NewToolResultError(c.msg), nil
			}
		}
		return handler(ctx, req)
	}
}

// careerStoreDep verifies CareerStore is configured.
func careerStoreDep(cfg ServerConfig) toolDepCheck {
	return toolDepCheck{ok: cfg.CareerStore != nil, msg: "career store unavailable"}
}

// branchStoreDep verifies BranchStore is configured.
func branchStoreDep(cfg ServerConfig) toolDepCheck {
	return toolDepCheck{ok: cfg.BranchStore != nil, msg: "branch store unavailable"}
}

// branchServiceDep verifies BranchService is configured.
func branchServiceDep(cfg ServerConfig) toolDepCheck {
	return toolDepCheck{ok: cfg.BranchService != nil, msg: "branch service unavailable"}
}

// templateStoreDep verifies TemplateStore is configured.
func templateStoreDep(cfg ServerConfig) toolDepCheck {
	return toolDepCheck{ok: cfg.TemplateStore != nil, msg: "template store unavailable"}
}

// presetStoreDep verifies PresetStore is configured.
func presetStoreDep(cfg ServerConfig) toolDepCheck {
	return toolDepCheck{ok: cfg.PresetStore != nil, msg: "preset store unavailable"}
}

// mcpDomainErrorMessage maps known domain sentinels to client-safe error text while hiding internal/upstream errors.
func mcpDomainErrorMessage(action string, err error) string {
	switch {
	case errors.Is(err, core.ErrValidation):
		return fmt.Sprintf("invalid request to %s", action)
	case errors.Is(err, core.ErrDefaultBranchImmutable):
		return "default branch cannot be modified (immutable)"
	case errors.Is(err, core.ErrConflict):
		return fmt.Sprintf("conflict while trying to %s", action)
	case errors.Is(err, core.ErrNotFound):
		return fmt.Sprintf("target resource not found to %s", action)
	case errors.Is(err, core.ErrForbidden):
		return fmt.Sprintf("access denied to %s", action)
	case errors.Is(err, core.ErrQuotaExceeded):
		return fmt.Sprintf("quota exceeded while trying to %s", action)
	case errors.Is(err, core.ErrProvenance):
		return fmt.Sprintf("provenance validation failed while trying to %s", action)
	case errors.Is(err, core.ErrTemplateSyntax):
		return fmt.Sprintf("template syntax error while trying to %s", action)
	default:
		return fmt.Sprintf("failed to %s due to an internal error", action)
	}
}

// registerTools registers all 18 domain tools onto the provided MCPServer instance.
//
// Parameters:
//   - s: Pointer to the MCPServer instance.
//   - cfg: ServerConfig providing dependencies, stores, and timeouts.
func registerTools(s *serverSDK.MCPServer, cfg ServerConfig) {
	registerProfileTools(s, cfg)
	registerBranchTools(s, cfg)
	registerPresetTools(s, cfg)
	registerSkillTools(s, cfg)
}
