package openapi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAPISpecificationIntegrity(t *testing.T) {
	specPath := filepath.Join(".", "openapi.yaml")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("failed to read OpenAPI spec at %s: %v", specPath, err)
	}

	content := string(data)
	if len(content) == 0 {
		t.Fatalf("OpenAPI spec is empty")
	}

	// Verify OpenAPI 3.1.0 version header
	if !strings.Contains(content, "openapi: 3.1.0") {
		t.Errorf("spec does not specify openapi: 3.1.0")
	}

	// Verify server prefix
	if !strings.Contains(content, "url: /api/v1") {
		t.Errorf("spec does not specify /api/v1 server url")
	}

	// Verify key endpoints discovered from design doc Section 5
	expectedEndpoints := []string{
		"/health:",
		"/models:",
		"/profile:",
		"/profile/full:",
		"/profile/jsonresume/import:",
		"/profile/jsonresume/export:",
		"/profile/pdf/parse:",
		"/profile/pdf/commit:",
		"/skills:",
		"/skills/{id}:",
		"/experiences:",
		"/experiences/{id}:",
		"/projects:",
		"/projects/{id}:",
		"/education:",
		"/education/{id}:",
		"/certifications:",
		"/certifications/{id}:",
		"/facts:",
		"/facts/{id}:",
		"/presets:",
		"/presets/{id}:",
		"/tailoring-strategies:",
		"/tailoring-strategies/{id}:",
		"/templates:",
		"/templates/{id}:",
		"/branches:",
		"/branches/{id}:",
		"/branches/{id}/generate:",
		"/branches/{id}/prompt-preview:",
		"/branches/sync:",
		"/branches/{id}/versions:",
		"/branches/{id}/versions/{v}:",
		"/branches/{id}/versions/{v}/export:",
		"/branches/{id}/versions/{v}/edit:",
		"/branches/{id}/versions/{v}/render-preview:",
		"/resumes/{id}/versions/{v}/render-preview:",
		"/branches/{id}/ats-score:",
		"/branches/{id}/versions/{v}/ats-score:",
		"/resumes/{id}/ats-score:",
		"/resumes/{id}/versions/{v}/ats-score:",
	}

	for _, endpoint := range expectedEndpoints {
		if !strings.Contains(content, endpoint) {
			t.Errorf("spec is missing documented endpoint: %s", endpoint)
		}
	}

	// Verify key schemas and error envelopes
	expectedSchemas := []string{
		"ErrorResponse:",
		"ErrorDetail:",
		"ModelOption:",
		"Profile:",
		"Skill:",
		"WorkExperience:",
		"Project:",
		"Education:",
		"Certification:",
		"Fact:",
		"Preset:",
		"TailoringStrategy:",
		"CreateStrategyRequest:",
		"UpdateStrategyRequest:",
		"TemplateSet:",
		"Branch:",
		"BranchVersion:",
		"AsyncJobEnqueued:",
		"SyncBranchesResponse:",
		"EditVersionRequest:",
		"PromptPreviewResponse:",
		"RenderPreviewRequest:",
		"ATSScore:",
		"ATSExtractedCandidate:",
		"ATSSimulationRequest:",
	}

	for _, schema := range expectedSchemas {
		if !strings.Contains(content, schema) {
			t.Errorf("spec is missing required component schema: %s", schema)
		}
	}
}
