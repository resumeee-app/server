package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-core/services/branch"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/services/renderer"
	"github.com/resumeee-app/resumeee-core/memory"
	"github.com/resumeee-app/resumeee-core/validator"
)

func TestBranchPreview_DynamicTemplateRendering(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "user-dynamic-preview"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	profileRepo := store
	presetRepo := store
	templateRepo := store
	branchRepo := store
	rend := renderer.NewTemplateRenderer()

	branchSvc := branch.NewService(branch.Config{
		BranchRepo:   branchRepo,
		ProfileRepo:  profileRepo,
		PresetRepo:   presetRepo,
		TemplateRepo: templateRepo,
		Validator:    validator.New(),
		Renderer:     rend,
	})

	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:   profileRepo,
		PresetRepo:    presetRepo,
		TemplateRepo:  templateRepo,
		BranchRepo:    branchRepo,
		BranchService: branchSvc,
		Renderer:      rend,
		DefaultUserID: defaultUserID,
		Admin:         true,
	})

	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()
	baseURL := server.URL

	// 1. Create two distinct custom templates
	now := time.Now().UTC()
	classicTemplate := &core.TemplateSet{
		ID:               "tmpl-classic-theme",
		UserID:           defaultUserID,
		Name:             "Classic Theme",
		IsBuiltIn:        false,
		HTMLTemplate:     `<div class="classic-theme">{{ .FullName }} - Classic</div>`,
		MarkdownTemplate: `# {{ .FullName }} - Classic`,
		TextTemplate:     `{{ .FullName }} - Classic`,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := templateRepo.CreateTemplate(ctx, classicTemplate); err != nil {
		t.Fatalf("failed to create classic template: %v", err)
	}

	modernTemplate := &core.TemplateSet{
		ID:               "tmpl-modern-theme",
		UserID:           defaultUserID,
		Name:             "Modern Theme",
		IsBuiltIn:        false,
		HTMLTemplate:     `<div class="modern-theme">{{ .FullName }} - Modern</div>`,
		MarkdownTemplate: `# {{ .FullName }} - Modern`,
		TextTemplate:     `{{ .FullName }} - Modern`,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := templateRepo.CreateTemplate(ctx, modernTemplate); err != nil {
		t.Fatalf("failed to create modern template: %v", err)
	}

	// 2. Create branch with classic template
	targetBranch := &core.TargetResume{
		ID:            "branch-test-dyn",
		UserID:        defaultUserID,
		Name:          "Frontend Architect",
		TemplateSetID: "tmpl-classic-theme",
	}
	if err := branchRepo.CreateBranch(ctx, targetBranch); err != nil {
		t.Fatalf("failed to create target branch: %v", err)
	}

	// 3. Create completed version with classic template cached output
	v1 := &core.ResumeVersion{
		ID:                 "ver-dyn-1",
		ResumeID:           targetBranch.ID,
		BranchID:           targetBranch.ID,
		VersionNumber:      1,
		Status:             core.VersionStatusCompleted,
		TemplateSetIDUsed:  "tmpl-classic-theme",
		SourceType:         core.SourceTypeLLMGeneration,
		HTMLOutput:         `<div class="classic-theme">Taylor Candidate - Classic</div>`,
		MarkdownOutput:     `# Taylor Candidate - Classic`,
		PlainTextOutput:    `Taylor Candidate - Classic`,
		TailoredContent:    &core.TailoredContent{Summary: "Lead developer"},
		CreatedAt:          now,
	}
	if err := branchRepo.CreateVersion(ctx, v1); err != nil {
		t.Fatalf("failed to create version: %v", err)
	}

	// 4. Initial preview should return classic template output
	url := baseURL + "/api/v1/branches/" + targetBranch.ID + "/versions/1/preview"
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-User-ID", defaultUserID)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("initial preview request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var previewRes core.RenderOutput
	if err := json.NewDecoder(resp.Body).Decode(&previewRes); err != nil {
		t.Fatalf("failed to decode preview: %v", err)
	}
	if !strings.Contains(previewRes.HTML, "classic-theme") {
		t.Errorf("expected classic theme initially, got: %s", previewRes.HTML)
	}

	// 5. Update branch to modern template
	updateBody := map[string]any{
		"template_set_id": "tmpl-modern-theme",
	}
	rawUpdate, _ := json.Marshal(updateBody)
	putReq, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/branches/"+targetBranch.ID, bytes.NewReader(rawUpdate))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("X-User-ID", defaultUserID)
	putResp, err := client.Do(putReq)
	if err != nil {
		t.Fatalf("update branch request failed: %v", err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on update branch, got: %d", putResp.StatusCode)
	}

	// 6. Preview GET /branches/{id}/versions/1/preview must dynamically render with modern template
	getReq, _ := http.NewRequest(http.MethodGet, url, nil)
	getReq.Header.Set("X-User-ID", defaultUserID)
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("preview request after branch update failed: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()

	var dynPreview core.RenderOutput
	if err := json.NewDecoder(getResp.Body).Decode(&dynPreview); err != nil {
		t.Fatalf("failed to decode dynamic preview: %v", err)
	}
	if !strings.Contains(dynPreview.HTML, "modern-theme") {
		t.Errorf("expected dynamically rendered HTML to contain 'modern-theme' matching updated branch setting, got: %s", dynPreview.HTML)
	}
	if !strings.Contains(dynPreview.Markdown, "Modern") {
		t.Errorf("expected dynamically rendered Markdown to contain 'Modern', got: %s", dynPreview.Markdown)
	}
	if !strings.Contains(dynPreview.PlainText, "Modern") {
		t.Errorf("expected dynamically rendered PlainText to contain 'Modern', got: %s", dynPreview.PlainText)
	}

	// 7. Preview with query parameter override ?template_set_id=tmpl-classic-theme
	queryReq, _ := http.NewRequest(http.MethodGet, url+"?template_set_id=tmpl-classic-theme", nil)
	queryReq.Header.Set("X-User-ID", defaultUserID)
	queryResp, err := client.Do(queryReq)
	if err != nil {
		t.Fatalf("query preview request failed: %v", err)
	}
	defer func() { _ = queryResp.Body.Close() }()

	var queryPreview core.RenderOutput
	if err := json.NewDecoder(queryResp.Body).Decode(&queryPreview); err != nil {
		t.Fatalf("failed to decode query preview: %v", err)
	}
	if !strings.Contains(queryPreview.HTML, "classic-theme") {
		t.Errorf("expected query param override to render 'classic-theme', got: %s", queryPreview.HTML)
	}
	if !strings.Contains(queryPreview.Markdown, "Classic") {
		t.Errorf("expected query param override Markdown to contain 'Classic', got: %s", queryPreview.Markdown)
	}
	if !strings.Contains(queryPreview.PlainText, "Classic") {
		t.Errorf("expected query param override PlainText to contain 'Classic', got: %s", queryPreview.PlainText)
	}

	// 8. Verify database version immutability
	dbVer, err := branchRepo.GetVersion(ctx, targetBranch.ID, v1.ID)
	if err != nil {
		t.Fatalf("failed to fetch version from repo: %v", err)
	}
	if dbVer.TemplateSetIDUsed != "tmpl-classic-theme" {
		t.Errorf("version TemplateSetIDUsed mutated in DB: expected 'tmpl-classic-theme', got '%s'", dbVer.TemplateSetIDUsed)
	}
	if dbVer.HTMLOutput != `<div class="classic-theme">Taylor Candidate - Classic</div>` {
		t.Errorf("version HTMLOutput mutated in DB: got '%s'", dbVer.HTMLOutput)
	}
}
