package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"

	"github.com/resumeee-app/resumeee-core/domain"
	resumeeeMCP "github.com/resumeee-app/resumeee-api/internal/mcp"
	"github.com/resumeee-app/resumeee-core/memory"
)

func toolReq(name string, args map[string]any) mcpSDK.CallToolRequest {
	return mcpSDK.CallToolRequest{
		Params: mcpSDK.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

func resReq(uri string) mcpSDK.ReadResourceRequest {
	return mcpSDK.ReadResourceRequest{
		Params: mcpSDK.ReadResourceParams{
			URI: uri,
		},
	}
}

func TestMCP_TenantProvisioning_LazyOnboarding(t *testing.T) {
	ctx := context.Background()
	store, err := memory.NewSeeded(ctx, "user-1")
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	cfg := resumeeeMCP.ServerConfig{
		ProfileRepo:   store,
		BranchRepo:    store,
		TemplateRepo:  store,
		PresetRepo:    store,
		UserRepo:      store,
		DefaultUserID: "user-1",
		RESTTimeout:   5 * time.Second,
	}

	server := resumeeeMCP.NewServer(cfg)
	newTenantID := "tenant-lazy-123"

	// Verify tenant is initially unprovisioned in the repository
	initialProf, _ := store.GetProfile(ctx, newTenantID)
	if initialProf != nil {
		t.Fatalf("expected new tenant to be unprovisioned initially")
	}

	// Invoke get_main_profile tool for the unprovisioned tenant
	tenantCtx := resumeeeMCP.WithUserID(ctx, newTenantID)
	tool := server.GetTool("get_main_profile")
	if tool == nil {
		t.Fatal("tool get_main_profile not found")
	}

	res, err := tool.Handler(tenantCtx, mcpSDK.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected successful execution with lazy provisioning, got error: %+v", res)
	}

	// Verify that profile and default branch were created for the tenant
	provProf, err := store.GetProfile(ctx, newTenantID)
	if err != nil || provProf == nil {
		t.Fatalf("expected tenant profile to be provisioned, err: %v", err)
	}
	if provProf.FullName != "Default User" {
		t.Errorf("expected FullName 'Default User', got %q", provProf.FullName)
	}

	branches, err := store.ListBranches(ctx, newTenantID)
	if err != nil || len(branches) == 0 {
		t.Fatalf("expected default branch to be provisioned for tenant, branches: %+v, err: %v", branches, err)
	}
	if !branches[0].IsDefault {
		t.Errorf("expected branch to be marked default")
	}
}

func TestMCP_PresetTools_LanguageSupport(t *testing.T) {
	ctx := context.Background()
	store, err := memory.NewSeeded(ctx, "user-1")
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	cfg := resumeeeMCP.ServerConfig{
		ProfileRepo:   store,
		BranchRepo:    store,
		TemplateRepo:  store,
		PresetRepo:    store,
		UserRepo:      store,
		DefaultUserID: "user-1",
		RESTTimeout:   5 * time.Second,
	}

	server := resumeeeMCP.NewServer(cfg)
	userCtx := resumeeeMCP.WithUserID(ctx, "user-1")

	// 1. Verify create_preset tool exists
	createTool := server.GetTool("create_preset")
	if createTool == nil {
		t.Fatal("tool create_preset not registered")
	}

	// 2. create_preset with explicit valid language
	validReq := toolReq("create_preset", map[string]any{
		"name":                "Spanish SWE Strategy",
		"system_instructions": "Focus on high-scale backend engineering in Spanish.",
		"language":            "Spanish",
	})

	validRes, err := createTool.Handler(userCtx, validReq)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if validRes.IsError {
		t.Fatalf("expected successful creation, got: %+v", validRes)
	}

	var createdPreset domain.Preset
	createdText := validRes.Content[0].(mcpSDK.TextContent).Text
	if err := json.Unmarshal([]byte(createdText), &createdPreset); err != nil {
		t.Fatalf("failed to unmarshal created preset: %v, raw: %s", err, createdText)
	}
	if createdPreset.Language != "Spanish" {
		t.Errorf("expected language 'Spanish', got %q", createdPreset.Language)
	}
	if createdPreset.Name != "Spanish SWE Strategy" {
		t.Errorf("unexpected name: %q", createdPreset.Name)
	}

	persistedPreset, err := store.GetPreset(ctx, "user-1", createdPreset.ID)
	if err != nil || persistedPreset == nil {
		t.Fatalf("expected preset %q to be in store: %v", createdPreset.ID, err)
	}
	if persistedPreset.Name != "Spanish SWE Strategy" {
		t.Errorf("expected persisted Name 'Spanish SWE Strategy', got %q", persistedPreset.Name)
	}
	if persistedPreset.Language != "Spanish" {
		t.Errorf("expected persisted Language 'Spanish', got %q", persistedPreset.Language)
	}
	if persistedPreset.SystemInstructions != "Focus on high-scale backend engineering in Spanish." {
		t.Errorf("expected persisted SystemInstructions, got %q", persistedPreset.SystemInstructions)
	}

	// 3. create_preset with empty language defaults to English
	emptyLangReq := toolReq("create_preset", map[string]any{
		"name":                "Default Lang Strategy",
		"system_instructions": "General tech instructions.",
	})
	emptyLangRes, err := createTool.Handler(userCtx, emptyLangReq)
	if err != nil || emptyLangRes.IsError {
		t.Fatalf("expected successful default language preset creation, res: %+v, err: %v", emptyLangRes, err)
	}
	var defaultLangPreset domain.Preset
	defaultText := emptyLangRes.Content[0].(mcpSDK.TextContent).Text
	if err := json.Unmarshal([]byte(defaultText), &defaultLangPreset); err != nil {
		t.Fatalf("failed to unmarshal preset: %v", err)
	}
	if defaultLangPreset.Language != domain.DefaultLanguage {
		t.Errorf("expected default language %q, got %q", domain.DefaultLanguage, defaultLangPreset.Language)
	}

	persistedDefaultPreset, err := store.GetPreset(ctx, "user-1", defaultLangPreset.ID)
	if err != nil || persistedDefaultPreset == nil {
		t.Fatalf("expected default preset %q to be in store: %v", defaultLangPreset.ID, err)
	}
	if persistedDefaultPreset.Language != domain.DefaultLanguage {
		t.Errorf("expected persisted default language %q, got %q", domain.DefaultLanguage, persistedDefaultPreset.Language)
	}

	// 4. create_preset with RTL language rejected
	rtlReq := toolReq("create_preset", map[string]any{
		"name":                "Arabic Strategy",
		"system_instructions": "Instructions.",
		"language":            "Arabic",
	})
	rtlRes, err := createTool.Handler(userCtx, rtlReq)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if !rtlRes.IsError {
		t.Fatalf("expected RTL language to be rejected with error")
	}
	rtlErrText := rtlRes.Content[0].(mcpSDK.TextContent).Text
	if !strings.Contains(strings.ToLower(rtlErrText), "right-to-left") {
		t.Errorf("expected error message to mention right-to-left, got: %s", rtlErrText)
	}

	// 5. create_preset with unsupported language rejected
	unsupportedReq := toolReq("create_preset", map[string]any{
		"name":                "Klingon Strategy",
		"system_instructions": "Instructions.",
		"language":            "Klingon",
	})
	unsupportedRes, err := createTool.Handler(userCtx, unsupportedReq)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if !unsupportedRes.IsError {
		t.Fatalf("expected unsupported language to be rejected")
	}

	// 6. update_preset language to French
	updateTool := server.GetTool("update_preset")
	if updateTool == nil {
		t.Fatal("tool update_preset not registered")
	}

	updateReq := toolReq("update_preset", map[string]any{
		"id":       createdPreset.ID,
		"language": "French",
	})
	updateRes, err := updateTool.Handler(userCtx, updateReq)
	if err != nil || updateRes.IsError {
		t.Fatalf("expected update to succeed, got: %+v, err: %v", updateRes, err)
	}
	var updatedPreset domain.Preset
	updText := updateRes.Content[0].(mcpSDK.TextContent).Text
	if err := json.Unmarshal([]byte(updText), &updatedPreset); err != nil {
		t.Fatalf("failed to unmarshal updated preset: %v", err)
	}
	if updatedPreset.Language != "French" {
		t.Errorf("expected language 'French', got %q", updatedPreset.Language)
	}

	persistedUpdatedPreset, err := store.GetPreset(ctx, "user-1", createdPreset.ID)
	if err != nil || persistedUpdatedPreset == nil {
		t.Fatalf("expected updated preset to be in store: %v", err)
	}
	if persistedUpdatedPreset.Language != "French" {
		t.Errorf("expected persisted updated Language 'French', got %q", persistedUpdatedPreset.Language)
	}

	// 7. update_preset rejecting built-in preset
	builtInUpdateReq := toolReq("update_preset", map[string]any{
		"id":       domain.Preset1PageTechResumeID,
		"language": "German",
	})
	builtInRes, err := updateTool.Handler(userCtx, builtInUpdateReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !builtInRes.IsError {
		t.Fatalf("expected error updating built-in preset")
	}
	builtInErrText := builtInRes.Content[0].(mcpSDK.TextContent).Text
	if !strings.Contains(strings.ToLower(builtInErrText), "built-in") && !strings.Contains(strings.ToLower(builtInErrText), "immutable") {
		t.Errorf("expected built-in immutability error message, got: %s", builtInErrText)
	}

	// 8. list_presets returns both user preset and built-in presets
	listTool := server.GetTool("list_presets")
	if listTool == nil {
		t.Fatal("tool list_presets not registered")
	}
	listRes, err := listTool.Handler(userCtx, mcpSDK.CallToolRequest{})
	if err != nil || listRes.IsError {
		t.Fatalf("expected list_presets success, got: %+v, err: %v", listRes, err)
	}
	var presetList []domain.Preset
	if err := json.Unmarshal([]byte(listRes.Content[0].(mcpSDK.TextContent).Text), &presetList); err != nil {
		t.Fatalf("failed to unmarshal preset list: %v", err)
	}
	if len(presetList) < 2 {
		t.Errorf("expected at least 2 presets, got %d", len(presetList))
	}

	// 9. get_preset retrieves custom and built-in presets
	getTool := server.GetTool("get_preset")
	if getTool == nil {
		t.Fatal("tool get_preset not registered")
	}
	getReq := toolReq("get_preset", map[string]any{
		"id": createdPreset.ID,
	})
	getRes, err := getTool.Handler(userCtx, getReq)
	if err != nil || getRes.IsError {
		t.Fatalf("expected get_preset success, got: %+v, err: %v", getRes, err)
	}

	// 10. list_supported_languages tool
	langTool := server.GetTool("list_supported_languages")
	if langTool == nil {
		t.Fatal("tool list_supported_languages not registered")
	}
	langRes, err := langTool.Handler(userCtx, mcpSDK.CallToolRequest{})
	if err != nil || langRes.IsError {
		t.Fatalf("expected list_supported_languages success, got: %+v, err: %v", langRes, err)
	}
	var langPayload struct {
		DefaultLanguage    string   `json:"default_language"`
		SupportedLanguages []string `json:"supported_languages"`
	}
	if err := json.Unmarshal([]byte(langRes.Content[0].(mcpSDK.TextContent).Text), &langPayload); err != nil {
		t.Fatalf("failed to unmarshal language list: %v", err)
	}
	if langPayload.DefaultLanguage != domain.DefaultLanguage {
		t.Errorf("expected default language %q, got %q", domain.DefaultLanguage, langPayload.DefaultLanguage)
	}
	if len(langPayload.SupportedLanguages) != len(domain.SupportedLanguages) {
		t.Errorf("expected %d supported languages, got %d", len(domain.SupportedLanguages), len(langPayload.SupportedLanguages))
	}
}

func TestMCP_Resources_LanguagesAndPresets(t *testing.T) {
	ctx := context.Background()
	store, err := memory.NewSeeded(ctx, "user-1")
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	cfg := resumeeeMCP.ServerConfig{
		ProfileRepo:   store,
		BranchRepo:    store,
		TemplateRepo:  store,
		PresetRepo:    store,
		UserRepo:      store,
		DefaultUserID: "user-1",
		RESTTimeout:   5 * time.Second,
	}

	server := resumeeeMCP.NewServer(cfg)
	userCtx := resumeeeMCP.WithUserID(ctx, "user-1")

	// 1. Read resumeee://languages
	resources := server.ListResources()
	langResource, ok := resources["resumeee://languages"]
	if !ok {
		t.Fatal("resource resumeee://languages not found")
	}
	contents, err := langResource.Handler(userCtx, resReq("resumeee://languages"))
	if err != nil || len(contents) == 0 {
		t.Fatalf("expected reading resumeee://languages to succeed, got: %v", err)
	}
	textContent, ok := mcpSDK.AsTextResourceContents(contents[0])
	if !ok {
		t.Fatal("expected TextResourceContents")
	}
	if !strings.Contains(textContent.Text, "English") || !strings.Contains(textContent.Text, "Spanish") {
		t.Errorf("expected languages JSON to contain English and Spanish, got: %s", textContent.Text)
	}

	// 2. Read resumeee://presets/{id} via JSON-RPC resources/read
	reqBytes, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "resources/read",
		"params":  map[string]any{"uri": "resumeee://presets/" + domain.Preset1PageTechResumeID},
	})
	resp := server.HandleMessage(userCtx, reqBytes)
	respBytes, _ := json.Marshal(resp)

	var rpcResp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  *struct {
			Contents []struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"contents"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &rpcResp); err != nil {
		t.Fatalf("failed to decode JSON-RPC response: %v, raw: %s", err, string(respBytes))
	}
	if rpcResp.Error != nil {
		t.Fatalf("expected no RPC error, got: %+v", rpcResp.Error)
	}
	if rpcResp.Result == nil || len(rpcResp.Result.Contents) == 0 {
		t.Fatalf("expected result contents in response, got: %+v", rpcResp.Result)
	}
	pText := rpcResp.Result.Contents[0].Text
	if !strings.Contains(pText, domain.Preset1PageTechResumeID) {
		t.Errorf("expected preset resource to contain ID, got: %s", pText)
	}
}

func TestMCP_SkillsTools(t *testing.T) {
	ctx := context.Background()
	store, err := memory.NewSeeded(ctx, "user-1")
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	cfg := resumeeeMCP.ServerConfig{
		ProfileRepo:   store,
		BranchRepo:    store,
		TemplateRepo:  store,
		PresetRepo:    store,
		UserRepo:      store,
		DefaultUserID: "user-1",
		RESTTimeout:   5 * time.Second,
	}

	server := resumeeeMCP.NewServer(cfg)
	userCtx := resumeeeMCP.WithUserID(ctx, "user-1")

	// 1. add_skill
	addSkillTool := server.GetTool("add_skill")
	if addSkillTool == nil {
		t.Fatal("tool add_skill not registered")
	}

	addReq := toolReq("add_skill", map[string]any{
		"name":             "Rust",
		"category":         "Languages",
		"proficiency":      "Proficient",
		"years_experience": 3.5,
		"keywords":         "systems, memory-safety, async",
	})

	addRes, err := addSkillTool.Handler(userCtx, addReq)
	if err != nil || addRes.IsError {
		t.Fatalf("expected add_skill success, got: %+v, err: %v", addRes, err)
	}

	var addedSkill domain.Skill
	addText := addRes.Content[0].(mcpSDK.TextContent).Text
	if err := json.Unmarshal([]byte(addText), &addedSkill); err != nil {
		t.Fatalf("failed to unmarshal added skill: %v, raw: %s", err, addText)
	}
	if addedSkill.Name != "Rust" || addedSkill.Category != "Languages" {
		t.Errorf("unexpected added skill fields: %+v", addedSkill)
	}

	persistedSkill, err := store.GetSkill(ctx, "user-1", addedSkill.ID)
	if err != nil || persistedSkill == nil {
		t.Fatalf("expected skill %q to be in store: %v", addedSkill.ID, err)
	}
	if persistedSkill.Name != "Rust" {
		t.Errorf("expected persisted Name 'Rust', got %q", persistedSkill.Name)
	}
	if persistedSkill.Category != "Languages" {
		t.Errorf("expected persisted Category 'Languages', got %q", persistedSkill.Category)
	}
	if persistedSkill.Proficiency != "Proficient" {
		t.Errorf("expected persisted Proficiency 'Proficient', got %q", persistedSkill.Proficiency)
	}
	if persistedSkill.YearsExperience != 3.5 {
		t.Errorf("expected persisted YearsExperience 3.5, got %f", persistedSkill.YearsExperience)
	}
	if len(persistedSkill.Keywords) != 3 {
		t.Errorf("expected 3 persisted keywords, got: %+v", persistedSkill.Keywords)
	}

	// 2. list_skills
	listSkillTool := server.GetTool("list_skills")
	if listSkillTool == nil {
		t.Fatal("tool list_skills not registered")
	}

	// List custom skills
	listCustomReq := toolReq("list_skills", map[string]any{
		"type": "custom",
	})
	listCustomRes, err := listSkillTool.Handler(userCtx, listCustomReq)
	if err != nil || listCustomRes.IsError {
		t.Fatalf("expected list_skills success, got: %+v, err: %v", listCustomRes, err)
	}
	var customSkills []domain.Skill
	if err := json.Unmarshal([]byte(listCustomRes.Content[0].(mcpSDK.TextContent).Text), &customSkills); err != nil {
		t.Fatalf("failed to unmarshal custom skills: %v", err)
	}
	if len(customSkills) == 0 {
		t.Fatal("expected at least one custom skill")
	}

	// 3. update_skill
	updateSkillTool := server.GetTool("update_skill")
	if updateSkillTool == nil {
		t.Fatal("tool update_skill not registered")
	}

	updReq := toolReq("update_skill", map[string]any{
		"id":          addedSkill.ID,
		"proficiency": "Expert",
	})
	updRes, err := updateSkillTool.Handler(userCtx, updReq)
	if err != nil || updRes.IsError {
		t.Fatalf("expected update_skill success, got: %+v, err: %v", updRes, err)
	}
	var updatedSkill domain.Skill
	if err := json.Unmarshal([]byte(updRes.Content[0].(mcpSDK.TextContent).Text), &updatedSkill); err != nil {
		t.Fatalf("failed to unmarshal updated skill: %v", err)
	}
	if updatedSkill.Proficiency != "Expert" {
		t.Errorf("expected proficiency 'Expert', got %q", updatedSkill.Proficiency)
	}

	persistedUpdSkill, err := store.GetSkill(ctx, "user-1", addedSkill.ID)
	if err != nil || persistedUpdSkill == nil {
		t.Fatalf("expected updated skill in store: %v", err)
	}
	if persistedUpdSkill.Proficiency != "Expert" {
		t.Errorf("expected persisted Proficiency 'Expert', got %q", persistedUpdSkill.Proficiency)
	}
}

func TestMCP_HTTP_LazyTenantProvisioning(t *testing.T) {
	ctx := context.Background()
	store, err := memory.NewSeeded(ctx, "user-1")
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	cfg := resumeeeMCP.ServerConfig{
		ProfileRepo:           store,
		BranchRepo:            store,
		TemplateRepo:          store,
		PresetRepo:            store,
		UserRepo:              store,
		DefaultUserID:         "user-1",
		RESTTimeout:           5 * time.Second,
		MaxConnectionsPerUser: 5,
		MaxTotalConnections:   100,
		AuthTokens: map[string]string{
			"token-new-tenant": "tenant-http-456",
		},
	}

	mcpServer := resumeeeMCP.NewServer(cfg)
	handler := resumeeeMCP.NewHTTPHandler(mcpServer, cfg)

	// Verify tenant is initially unprovisioned
	if p, _ := store.GetProfile(ctx, "tenant-http-456"); p != nil {
		t.Fatal("expected tenant to be initially unprovisioned")
	}

	// Make an HTTP request with the tenant's Bearer token
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil).WithContext(reqCtx)
	req.Header.Set("Authorization", "Bearer token-new-tenant")
	rec := httptest.NewRecorder()

	// Run handler in background since SSE holds open connection
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	// Wait briefly for authentication and lazy provisioning to run
	time.Sleep(50 * time.Millisecond)

	// Verify tenant has been provisioned
	provProf, err := store.GetProfile(ctx, "tenant-http-456")
	if err != nil || provProf == nil {
		t.Fatalf("expected tenant profile to be provisioned via HTTP auth, err: %v", err)
	}
	if provProf.FullName != "Default User" {
		t.Errorf("expected FullName 'Default User', got %q", provProf.FullName)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for handler to terminate after cancellation")
	}
}
