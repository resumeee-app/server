package mcp

import (
	serverSDK "github.com/mark3labs/mcp-go/server"
)

// NewServer initializes and configures a new Model Context Protocol (MCP) server instance with full
// tool, resource, and prompt capabilities for Resumeee. It binds all domain handlers for profile management,
// resume branch creation, tailoring, ATS gap analysis, and template rendering.
//
// Parameters:
//   - cfg: ServerConfig specifying service dependencies, repositories, timeouts, and logger.
//
// Returns:
//   - Configured *serverSDK.MCPServer ready to serve over stdio or HTTP SSE.
func NewServer(cfg ServerConfig) *serverSDK.MCPServer {
	cfg.setDefaults()

	mcpServer := serverSDK.NewMCPServer(
		"Resumeee Career & Resume Engine",
		"1.0.0",
		serverSDK.WithToolCapabilities(true),
		serverSDK.WithResourceCapabilities(true, false),
		serverSDK.WithPromptCapabilities(true),
	)

	registerTools(mcpServer, cfg)
	registerResources(mcpServer, cfg)
	registerPrompts(mcpServer, cfg)

	return mcpServer
}
