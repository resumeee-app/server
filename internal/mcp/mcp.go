// Package mcp implements the Model Context Protocol (MCP) server integration for Resumeee,
// exposing career profile tools, resources, and guided prompts over stdio and HTTP SSE transports.
//
// It enables desktop AI assistants (such as Claude Desktop, Cursor, and Antigravity)
// to inspect candidate master profiles, trigger targeted resume tailoring runs,
// retrieve resume versions, and execute anti-hallucination validation checks.
//
// Transports:
//   - Stdio: Command-line pipe transport used by cmd/mcp
//   - HTTP SSE: Server-Sent Events transport mounted on the server daemon
package mcp
