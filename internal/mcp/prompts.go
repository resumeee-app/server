// Package mcp implements Model Context Protocol (MCP) server endpoints, tools, resources, and prompts.
package mcp

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"text/template"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"
)

//go:embed templates/*
var promptTemplateFS embed.FS

var (
	prepareJobApplicationTmpl = template.Must(template.ParseFS(promptTemplateFS, "templates/prepare_job_application.tmpl"))
	mockInterviewPrepTmpl     = template.Must(template.ParseFS(promptTemplateFS, "templates/mock_interview_prep.tmpl"))
)

// prepareJobApplicationPromptContext defines the data passed to prepare_job_application.tmpl.
type prepareJobApplicationPromptContext struct {
	JobDescription string
	BranchName     string
}

// mockInterviewPrepPromptContext defines the data passed to mock_interview_prep.tmpl.
type mockInterviewPrepPromptContext struct {
	BranchID  string
	FocusArea string
}

// registerPrompts registers all supported conversational guidance prompts onto the provided MCP server.
//
// Parameters:
//   - s: Pointer to the active MCPServer instance.
//   - cfg: ServerConfig providing configuration and dependencies.
func registerPrompts(s *serverSDK.MCPServer, cfg ServerConfig) {
	// 1. prepare_job_application
	s.AddPrompt(
		mcpSDK.NewPrompt("prepare_job_application",
			mcpSDK.WithPromptDescription("Guides the AI assistant to analyze job requirements against candidate master profile, run gap analysis, create a targeted branch, and tailor the resume."),
			mcpSDK.WithArgument("job_description", mcpSDK.ArgumentDescription("Target job description or requirements text"), mcpSDK.RequiredArgument()),
			mcpSDK.WithArgument("branch_name", mcpSDK.ArgumentDescription("Optional descriptive name for the targeted resume branch")),
		),
		handlePromptPrepareJobApplication(cfg),
	)

	// 2. mock_interview_prep
	s.AddPrompt(
		mcpSDK.NewPrompt("mock_interview_prep",
			mcpSDK.WithPromptDescription("Generates tailored behavioral STAR questions and technical interview scenarios based on candidate bullet facts and branch role requirements."),
			mcpSDK.WithArgument("branch_id", mcpSDK.ArgumentDescription("ID of the target resume branch"), mcpSDK.RequiredArgument()),
			mcpSDK.WithArgument("focus_area", mcpSDK.ArgumentDescription("Optional focus area (e.g. 'system design', 'leadership', 'technical')")),
		),
		handlePromptMockInterviewPrep(cfg),
	)
}

// handlePromptPrepareJobApplication returns a prompt handler that executes prepare_job_application.tmpl
// to construct guided instructions for an AI client to prepare a targeted job application.
//
// Parameters:
//   - cfg: ServerConfig containing server dependencies and options.
//
// Returns:
//   - PromptHandlerFunc compatible with the MCP server SDK.
func handlePromptPrepareJobApplication(_ ServerConfig) serverSDK.PromptHandlerFunc {
	return func(ctx context.Context, req mcpSDK.GetPromptRequest) (*mcpSDK.GetPromptResult, error) {
		jobDescription := strings.TrimSpace(req.Params.Arguments["job_description"])
		branchName := strings.TrimSpace(req.Params.Arguments["branch_name"])
		if branchName == "" {
			branchName = "Target Role Branch"
		}

		promptCtx := prepareJobApplicationPromptContext{
			JobDescription: jobDescription,
			BranchName:     branchName,
		}

		var sb strings.Builder
		if err := prepareJobApplicationTmpl.Execute(&sb, promptCtx); err != nil {
			return nil, fmt.Errorf("rendering prepare_job_application prompt template: %w", err)
		}

		return mcpSDK.NewGetPromptResult(
			"Job Application Preparation Guidance",
			[]mcpSDK.PromptMessage{
				mcpSDK.NewPromptMessage(mcpSDK.RoleUser, mcpSDK.NewTextContent(strings.TrimSpace(sb.String()))),
			},
		), nil
	}
}

// handlePromptMockInterviewPrep returns a prompt handler that executes mock_interview_prep.tmpl
// to construct guided interview prep instructions with behavioral and technical scenarios.
//
// Parameters:
//   - cfg: ServerConfig containing server dependencies and options.
//
// Returns:
//   - PromptHandlerFunc compatible with the MCP server SDK.
func handlePromptMockInterviewPrep(_ ServerConfig) serverSDK.PromptHandlerFunc {
	return func(ctx context.Context, req mcpSDK.GetPromptRequest) (*mcpSDK.GetPromptResult, error) {
		branchID := strings.TrimSpace(req.Params.Arguments["branch_id"])
		focusArea := strings.TrimSpace(req.Params.Arguments["focus_area"])
		if focusArea == "" {
			focusArea = "General Behavioral and Technical Alignment"
		}

		promptCtx := mockInterviewPrepPromptContext{
			BranchID:  branchID,
			FocusArea: focusArea,
		}

		var sb strings.Builder
		if err := mockInterviewPrepTmpl.Execute(&sb, promptCtx); err != nil {
			return nil, fmt.Errorf("rendering mock_interview_prep prompt template: %w", err)
		}

		return mcpSDK.NewGetPromptResult(
			"Mock Interview Preparation Guidance",
			[]mcpSDK.PromptMessage{
				mcpSDK.NewPromptMessage(mcpSDK.RoleUser, mcpSDK.NewTextContent(strings.TrimSpace(sb.String()))),
			},
		), nil
	}
}
