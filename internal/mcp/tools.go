package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/renderer"
)

// defaultTemplateSetID is the fallback built-in template set identifier when none is specified.
const defaultTemplateSetID = "builtin-standard-clean"

// reservedPromptTagRegex matches reserved prompt XML boundary tags that must be escaped inside untrusted input.
var reservedPromptTagRegex = regexp.MustCompile(`(?i)</?\s*(?:rules_hierarchy|tier_1_global_rules|tier_2_strategy_instructions|tier_3_branch_directives|main_career_profile|target_job_description|custom_instructions|resume_document|extracted_candidate_data|ats_extraction_rules|ats_matching_rules|response_schema)\b[^>]*>`)

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

// stripScriptTags delegates script tag sanitization to the renderer package.
func stripScriptTags(html string) string {
	return renderer.StripScriptTags(html)
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

// sanitizeXMLFence escapes conflicting prompt boundary XML tags in untrusted inputs.
func sanitizeXMLFence(content, tagName string) string {
	safe := escapeXMLMatches(content, reservedPromptTagRegex)
	if !isReservedPromptTag(tagName) {
		customTagRegex := regexp.MustCompile(fmt.Sprintf(`(?i)</?\s*%s\b[^>]*>`, regexp.QuoteMeta(tagName)))
		safe = escapeXMLMatches(safe, customTagRegex)
	}
	return fmt.Sprintf("<%s>\n%s\n</%s>", tagName, strings.TrimSpace(safe), tagName)
}

// escapeXMLMatches replaces angle brackets on all regex tag matches with XML entities.
func escapeXMLMatches(content string, tagRegex *regexp.Regexp) string {
	return tagRegex.ReplaceAllStringFunc(content, func(m string) string {
		escaped := strings.ReplaceAll(m, "<", "&lt;")
		return strings.ReplaceAll(escaped, ">", "&gt;")
	})
}

// isReservedPromptTag reports whether tagName is already covered by reservedPromptTagRegex.
func isReservedPromptTag(tagName string) bool {
	switch strings.ToLower(tagName) {
	case "rules_hierarchy",
		"tier_1_global_rules",
		"tier_2_strategy_instructions",
		"tier_3_branch_directives",
		"main_career_profile",
		"target_job_description",
		"custom_instructions",
		"resume_document",
		"extracted_candidate_data",
		"ats_extraction_rules",
		"ats_matching_rules",
		"response_schema":
		return true
	default:
		return false
	}
}

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
