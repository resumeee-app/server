// Package mcp implements the Model Context Protocol (MCP) server integration for Resumeee,
// exposing career profile tools, resources, and guided prompts over stdio and HTTP SSE transports.
//
// It enables desktop AI assistants (such as Claude Desktop, Cursor, and Antigravity)
// to inspect candidate main profiles, trigger targeted resume tailoring runs,
// retrieve resume versions, and execute anti-hallucination validation checks.
//
// Transports:
//   - Stdio: Command-line pipe transport used by cmd/mcp
//   - HTTP SSE: Server-Sent Events transport mounted on the server daemon
package mcp

import (
	serverSDK "github.com/mark3labs/mcp-go/server"
)

// NewServer initializes and configures a new Model Context Protocol (MCP) server instance with full
// tool, resource, and prompt capabilities for Resumeee. It binds all domain handlers for profile management,
// resume branch creation, tailoring, ATS gap analysis, and template rendering.
//
// Parameters:
//   - cfg: ServerConfig specifying service dependencies, stores, timeouts, and logger.
//
// Returns:
//   - Configured *serverSDK.MCPServer ready to serve over stdio or HTTP SSE.
func NewServer(cfg ServerConfig) *serverSDK.MCPServer {
	cfg.setDefaults()

	mcpServer := serverSDK.NewMCPServer(
		"Resumeee Career & Resume Engine",
		"1.0.0",
		serverSDK.WithRecovery(),
		serverSDK.WithToolCapabilities(true),
		serverSDK.WithResourceCapabilities(true, false),
		serverSDK.WithPromptCapabilities(true),
	)

	registerTools(mcpServer, cfg)
	registerResources(mcpServer, cfg)
	registerPrompts(mcpServer, cfg)

	return mcpServer
}
