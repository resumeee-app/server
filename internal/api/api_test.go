package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-core/domain"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/pdf"
	"github.com/resumeee-app/resumeee-core/services/profile"
	"github.com/resumeee-app/resumeee-core/services/renderer"
	"github.com/resumeee-app/resumeee-core/memory"
	"github.com/resumeee-app/resumeee-core/validator"
	"github.com/resumeee-app/resumeee-core/services/worker"
)

type testEnv struct {
	server       *httptest.Server
	db           any
	profileRepo  domain.ProfileRepository
	presetRepo   domain.PresetRepository
	templateRepo domain.TemplateRepository
	branchRepo   domain.BranchRepository
	profileSvc   profile.Service
	enqueuer     *api.ChannelJobEnqueuer
	pdfJobRepo   domain.PDFParseJobRepository
	pdfEnqueuer  *api.ChannelPDFJobEnqueuer
}

func setupTestServer(t *testing.T, queueCap int) *testEnv {
	t.Helper()

	ctx := context.Background()
	defaultUserID := "default"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	profileRepo := store
	presetRepo := store
	templateRepo := store
	branchRepo := store
	profileSvc := profile.NewService(profileRepo, branchRepo)
	templateRenderer := renderer.NewTemplateRenderer()
	provValidator := validator.New()

	if queueCap <= 0 {
		queueCap = 100
	}
	enqueuer := api.NewChannelJobEnqueuer(queueCap)

	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:    profileRepo,
		PresetRepo:     presetRepo,
		TemplateRepo:   templateRepo,
		BranchRepo:     branchRepo,
		QuotaRepo:      store,
		ProfileService: profileSvc,
		Renderer:       templateRenderer,
		Validator:      provValidator,
		JobEnqueuer:    enqueuer,
		DefaultUserID:  defaultUserID,
		Admin:          true,
	})

	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
	})

	return &testEnv{
		server:       server,
		db:           store,
		profileRepo:  profileRepo,
		presetRepo:   presetRepo,
		templateRepo: templateRepo,
		branchRepo:   branchRepo,
		profileSvc:   profileSvc,
		enqueuer:     enqueuer,
	}
}

func doRequest(t *testing.T, client *http.Client, method, url string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			reader = bytes.NewReader(v)
		case string:
			reader = strings.NewReader(v)
		default:
			data, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
			reader = bytes.NewReader(data)
		}
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to execute request %s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	return resp, respBody
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var dst T
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("failed to unmarshal JSON into %T: %v (raw: %s)", dst, err, string(data))
	}
	return dst
}

// TestFullUserJourney verifies the complete end-to-end lifecycle across all 28 OpenAPI endpoints.
func TestFullUserJourney(t *testing.T) {
	env := setupTestServer(t, 100)
	client := env.server.Client()
	baseURL := env.server.URL

	// 1. GET /api/v1/profile -> Verify default seeded user initialized with main_revision: 1
	resp, body := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /profile failed: %d, body: %s", resp.StatusCode, string(body))
	}
	prof := decodeJSON[domain.Profile](t, body)
	if prof.MainRevision != 1 {
		t.Fatalf("expected initial main_revision: 1, got: %d", prof.MainRevision)
	}

	// 2. POST /api/v1/skills -> Create Go skill
	skillGoReq := api.CreateSkillRequest{
		Name:            "Go",
		Category:        "Languages",
		Proficiency:     "Advanced",
		YearsExperience: 6.0,
		Keywords:        []string{"Go", "Concurrency", "gRPC"},
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/skills", skillGoReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /skills failed: %d, body: %s", resp.StatusCode, string(body))
	}
	skillGo := decodeJSON[domain.Skill](t, body)
	if skillGo.Name != "Go" || skillGo.ID == "" {
		t.Fatalf("invalid skill created: %+v", skillGo)
	}

	// 3. GET /api/v1/skills -> List skills
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/skills", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /skills failed: %d", resp.StatusCode)
	}
	skills := decodeJSON[[]domain.Skill](t, body)
	if len(skills) < 1 {
		t.Fatalf("expected at least 1 skill, got %d", len(skills))
	}

	// 4. GET /api/v1/skills/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/skills/"+skillGo.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /skills/{id} failed: %d", resp.StatusCode)
	}

	// 5. PUT /api/v1/skills/{id}
	newProficiency := "Expert"
	updateSkillReq := api.UpdateSkillRequest{
		Proficiency: &newProficiency,
	}
	resp, body = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/skills/"+skillGo.ID, updateSkillReq, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /skills/{id} failed: %d", resp.StatusCode)
	}
	skillGo = decodeJSON[domain.Skill](t, body)
	if skillGo.Proficiency != "Expert" {
		t.Fatalf("expected proficiency Expert, got %s", skillGo.Proficiency)
	}

	// 6. POST /api/v1/experiences -> Create Work Experience
	expReq := api.CreateExperienceRequest{
		Company:        "Acme Corp",
		RoleTitle:      "Senior Backend Engineer",
		EmploymentType: domain.EmploymentTypeFullTime,
		StartDate:      domain.NewDate(2021, 1, 1),
		Location:       domain.Location{City: "San Francisco", Region: "CA", CountryCode: "US"},
		SkillIDs:       []string{skillGo.ID},
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/experiences", expReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /experiences failed: %d, body: %s", resp.StatusCode, string(body))
	}
	exp := decodeJSON[domain.WorkExperience](t, body)
	if exp.Company != "Acme Corp" || exp.ID == "" {
		t.Fatalf("invalid experience created: %+v", exp)
	}

	// 7. GET /api/v1/experiences
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/experiences", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /experiences failed: %d", resp.StatusCode)
	}

	// 8. GET /api/v1/experiences/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/experiences/"+exp.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /experiences/{id} failed: %d", resp.StatusCode)
	}

	// 9. PUT /api/v1/experiences/{id}
	newRole := "Staff Backend Engineer"
	updateExpReq := api.UpdateExperienceRequest{
		RoleTitle: &newRole,
	}
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/experiences/"+exp.ID, updateExpReq, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /experiences/{id} failed: %d", resp.StatusCode)
	}

	// 10. POST /api/v1/facts -> Create quantifiable achievement fact
	factReq := api.CreateFactRequest{
		OwnerID:   exp.ID,
		OwnerType: domain.OwnerTypeWorkExperience,
		Statement: "Architected distributed queue processing 50k events/sec.",
		Impact:    "Reduced latency by 40%",
		SkillIDs:  []string{skillGo.ID},
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/facts", factReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /facts failed: %d, body: %s", resp.StatusCode, string(body))
	}
	fact := decodeJSON[domain.Fact](t, body)
	if fact.ID == "" || fact.OwnerID != exp.ID {
		t.Fatalf("invalid fact created: %+v", fact)
	}

	// 11. GET /api/v1/facts by owner
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/facts?owner_type=work_experience&owner_id="+exp.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /facts failed: %d", resp.StatusCode)
	}
	facts := decodeJSON[[]domain.Fact](t, body)
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	// 12. GET /api/v1/facts/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/facts/"+fact.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /facts/{id} failed: %d", resp.StatusCode)
	}

	// 13. PUT /api/v1/facts/{id}
	updatedImpact := "Reduced latency by 50%"
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/facts/"+fact.ID, api.UpdateFactRequest{Impact: &updatedImpact}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /facts/{id} failed: %d", resp.StatusCode)
	}

	// 14. POST /api/v1/projects
	projReq := api.CreateProjectRequest{
		Name:      "Distributed Store",
		Role:      "Author",
		StartDate: domain.DatePtr(domain.NewDate(2022, 1, 1)),
		Summary:   "Raft-based key-value store in Go.",
		SkillIDs:  []string{skillGo.ID},
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/projects", projReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /projects failed: %d", resp.StatusCode)
	}
	proj := decodeJSON[domain.Project](t, body)

	// 15. GET /api/v1/projects and GET /api/v1/projects/{id} and PUT /api/v1/projects/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/projects", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /projects failed: %d", resp.StatusCode)
	}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/projects/"+proj.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /projects/{id} failed: %d", resp.StatusCode)
	}
	updatedRole := "Lead Maintainer"
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/projects/"+proj.ID, api.UpdateProjectRequest{Role: &updatedRole}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /projects/{id} failed: %d", resp.StatusCode)
	}

	// 16. POST /api/v1/education
	eduReq := api.CreateEducationRequest{
		Institution: "MIT",
		Degree:      "B.S.",
		Field:       "Computer Science",
		StartDate:   domain.DatePtr(domain.NewDate(2016, 9, 1)),
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/education", eduReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /education failed: %d", resp.StatusCode)
	}
	edu := decodeJSON[domain.Education](t, body)

	// 17. GET /api/v1/education and GET /api/v1/education/{id} and PUT /api/v1/education/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/education", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /education failed: %d", resp.StatusCode)
	}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/education/"+edu.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /education/{id} failed: %d", resp.StatusCode)
	}
	score := "3.9 GPA"
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/education/"+edu.ID, api.UpdateEducationRequest{Score: &score}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /education/{id} failed: %d", resp.StatusCode)
	}

	// 18. POST /api/v1/certifications
	certReq := api.CreateCertificationRequest{
		Name:   "CKA",
		Issuer: "CNCF",
		Date:   domain.DatePtr(domain.NewDate(2023, 1, 1)),
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/certifications", certReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /certifications failed: %d", resp.StatusCode)
	}
	cert := decodeJSON[domain.Certification](t, body)

	// 19. GET /api/v1/certifications and GET /api/v1/certifications/{id} and PUT /api/v1/certifications/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/certifications", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /certifications failed: %d", resp.StatusCode)
	}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/certifications/"+cert.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /certifications/{id} failed: %d", resp.StatusCode)
	}
	certSummary := "Certified Kubernetes Administrator"
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/certifications/"+cert.ID, api.UpdateCertificationRequest{Summary: &certSummary}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /certifications/{id} failed: %d", resp.StatusCode)
	}

	// 20. GET /api/v1/profile/full -> Verify main profile snapshot contains career graph
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile/full", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /profile/full failed: %d", resp.StatusCode)
	}
	snapshot := decodeJSON[domain.MainProfileSnapshot](t, body)
	if len(snapshot.Skills) < 1 || len(snapshot.Experiences) < 1 || len(snapshot.Facts) < 1 {
		t.Fatalf("snapshot missing entities: %+v", snapshot)
	}

	// 21. POST /api/v1/presets
	presetReq := api.CreatePresetRequest{
		Name:               "Custom SWE",
		SystemInstructions: "Focus on distributed systems and backend engineering.",
		TemplateSetID:      renderer.TemplateStandardCleanID,
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/presets", presetReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /presets failed: %d, body: %s", resp.StatusCode, string(body))
	}
	preset := decodeJSON[domain.Preset](t, body)

	// 22. GET /api/v1/presets and GET /api/v1/presets/{id} and PUT /api/v1/presets/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/presets", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /presets failed: %d", resp.StatusCode)
	}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/presets/"+preset.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /presets/{id} failed: %d", resp.StatusCode)
	}
	updatedDesc := "Updated preset description"
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/presets/"+preset.ID, api.UpdatePresetRequest{Description: &updatedDesc}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /presets/{id} failed: %d", resp.StatusCode)
	}

	// 23. POST /api/v1/templates -> Create valid template set with syntax dry-run validation
	tmplReq := api.CreateTemplateRequest{
		Name:             "Clean Minimal",
		HTMLTemplate:     "<h1>{{.FullName}}</h1><p>{{.Summary}}</p>",
		MarkdownTemplate: "# {{.FullName}}\n{{.Summary}}",
		TextTemplate:     "{{.FullName}}\n{{.Summary}}",
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates", tmplReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /templates failed: %d, body: %s", resp.StatusCode, string(body))
	}
	tmpl := decodeJSON[domain.TemplateSet](t, body)

	// 24. GET /api/v1/templates and GET /api/v1/templates/{id} and PUT /api/v1/templates/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /templates failed: %d", resp.StatusCode)
	}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+tmpl.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /templates/{id} failed: %d", resp.StatusCode)
	}
	newTmplName := "Clean Minimal v2"
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/templates/"+tmpl.ID, api.UpdateTemplateRequest{Name: &newTmplName}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /templates/{id} failed: %d", resp.StatusCode)
	}

	// 25. POST /api/v1/branches -> Create branch ("Staff Backend - Stripe"). is_stale is false
	branchReq := api.CreateBranchRequest{
		Name:               "Staff Backend - Stripe",
		TargetRole:         "Staff Software Engineer",
		TargetCompany:      "Stripe",
		TargetRequirements: "Distributed systems, Go, idempotency, high availability",
		TemplateSetID:      tmpl.ID,
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches", branchReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /branches failed: %d, body: %s", resp.StatusCode, string(body))
	}
	branch := decodeJSON[domain.Branch](t, body)
	if branch.IsStale {
		t.Fatalf("expected new branch to have is_stale=false, got true")
	}

	// 26. GET /api/v1/branches and GET /api/v1/branches/{id} and PUT /api/v1/branches/{id}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /branches failed: %d", resp.StatusCode)
	}
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /branches/{id} failed: %d", resp.StatusCode)
	}

	// 27. PUT /api/v1/profile -> Update profile summary. Verify GET /api/v1/branches?stale=true returns branch as stale
	newSummary := "Principal Systems Architect specializing in fault-tolerant backends."
	resp, _ = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/profile", api.UpdateProfileRequest{Summary: &newSummary}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /profile failed: %d", resp.StatusCode)
	}

	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches?stale=true", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /branches?stale=true failed: %d", resp.StatusCode)
	}
	staleBranches := decodeJSON[[]domain.Branch](t, body)
	if len(staleBranches) == 0 {
		t.Fatalf("expected branch %s to be stale after profile update, got none", branch.ID)
	}
	if !staleBranches[0].IsStale {
		t.Fatalf("expected branch.IsStale to be true")
	}

	// 28. POST /api/v1/branches/{id}/generate -> Returns 202 Accepted with AsyncJobEnqueued
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/generate", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /generate failed: %d, body: %s", resp.StatusCode, string(body))
	}
	jobEnqueued := decodeJSON[api.AsyncJobEnqueued](t, body)
	if jobEnqueued.VersionID == "" || jobEnqueued.Status != domain.VersionStatusPending {
		t.Fatalf("invalid AsyncJobEnqueued response: %+v", jobEnqueued)
	}

	// Simulate completed generation of version 1 for testing version endpoints
	tailoredContent := &domain.TailoredContent{
		Summary: newSummary,
		SkillGroups: []domain.TailoredSkillGroup{
			{
				Category:       "Languages",
				Skills:         []string{"Go"},
				SourceSkillIDs: []string{skillGo.ID},
			},
		},
		Experiences: []domain.TailoredExperience{
			{
				SourceEntityID: exp.ID,
				Company:        exp.Company,
				RoleTitle:      exp.RoleTitle,
				StartDate:      exp.StartDate,
				Location:       exp.Location,
				Bullets: []domain.TailoredBullet{
					{
						Text:          "Architected distributed queue processing 50k events/sec with Go.",
						SourceFactIDs: []string{fact.ID},
					},
				},
			},
		},
	}
	v1, err := env.branchRepo.GetVersion(context.Background(), branch.ID, jobEnqueued.VersionID)
	if err != nil {
		t.Fatalf("failed to fetch created version: %v", err)
	}
	v1.TailoredContent = tailoredContent
	v1.HTMLOutput = "<h1>Default User</h1><p>" + newSummary + "</p>"
	v1.MarkdownOutput = "# Default User\n" + newSummary
	v1.PlainTextOutput = "Default User\n" + newSummary
	v1.Status = domain.VersionStatusCompleted
	if err := env.branchRepo.CompleteVersion(context.Background(), v1); err != nil {
		t.Fatalf("failed to complete version 1: %v", err)
	}

	// 29. GET /api/v1/branches/{id}/versions
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /branches/{id}/versions failed: %d", resp.StatusCode)
	}
	versions := decodeJSON[[]domain.BranchVersion](t, body)
	if len(versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(versions))
	}

	// 30. GET /api/v1/branches/{id}/versions/1 (by sequential number)
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /versions/1 failed: %d", resp.StatusCode)
	}
	verNum := decodeJSON[domain.BranchVersion](t, body)
	if verNum.VersionNumber != 1 {
		t.Fatalf("expected version_number 1, got %d", verNum.VersionNumber)
	}

	// 31. GET /api/v1/branches/{id}/versions/{v} (by UUID)
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/"+v1.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /versions/{uuid} failed: %d", resp.StatusCode)
	}

	// 32. GET /api/v1/branches/{id}/versions/1/export across formats
	// format=markdown
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/export?format=markdown", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export markdown failed: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Fatalf("expected Content-Type text/markdown, got: %s", ct)
	}
	if !strings.Contains(string(body), newSummary) {
		t.Fatalf("expected markdown body to contain summary, got: %s", string(body))
	}

	// format=html
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/export?format=html", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export html failed: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected Content-Type text/html, got: %s", ct)
	}

	// format=text
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/export?format=text", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export text failed: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected Content-Type text/plain, got: %s", ct)
	}

	// format=jsonresume
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/export?format=jsonresume", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export jsonresume failed: %d, body: %s", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected Content-Type application/json, got: %s", ct)
	}

	// 32b. GET /api/v1/branches/{id}/versions/1/export with dynamic template_set_id override
	customTmplReq := api.CreateTemplateRequest{
		Name:             "Export Override Template",
		HTMLTemplate:     `<div class="export-custom"><h1>{{.FullName}}</h1><p>{{.Summary}}</p></div>`,
		MarkdownTemplate: `### CUSTOM_EXPORT: {{.FullName}}` + "\n" + `{{.Summary}}`,
		TextTemplate:     `CUSTOM_EXPORT: {{.FullName}}` + "\n" + `{{.Summary}}`,
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates", customTmplReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /templates for export override failed: %d", resp.StatusCode)
	}
	customTmpl := decodeJSON[domain.TemplateSet](t, body)

	// Export HTML with template_set_id
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/export?format=html&template_set_id="+customTmpl.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export html with template_set_id failed: %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `class="export-custom"`) {
		t.Fatalf("expected exported html to contain class export-custom, got: %s", string(body))
	}

	// Export Markdown with template_set_id
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/export?format=markdown&template_set_id="+customTmpl.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export markdown with template_set_id failed: %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `### CUSTOM_EXPORT:`) {
		t.Fatalf("expected exported markdown to contain ### CUSTOM_EXPORT:, got: %s", string(body))
	}

	// Export Text with template_set_id
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/export?format=text&template_set_id="+customTmpl.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export text with template_set_id failed: %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `CUSTOM_EXPORT:`) {
		t.Fatalf("expected exported text to contain CUSTOM_EXPORT:, got: %s", string(body))
	}

	// 33. GET /api/v1/profile/jsonresume/export (Main Profile export)
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile/jsonresume/export", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export main jsonresume failed: %d", resp.StatusCode)
	}

	// 34. GET /api/v1/profile/jsonresume/export?version_id=...
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile/jsonresume/export?version_id="+v1.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export version jsonresume via /profile failed: %d", resp.StatusCode)
	}

	// 35. POST /api/v1/profile/jsonresume/import
	jsonResumeSample := `{
		"basics": {
			"name": "Jane Doe",
			"label": "Staff Engineer",
			"email": "jane@example.com"
		},
		"skills": [
			{"name": "Rust", "level": "Intermediate", "keywords": ["Systems", "Async"]}
		]
	}`
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/profile/jsonresume/import", jsonResumeSample, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /profile/jsonresume/import failed: %d, body: %s", resp.StatusCode, string(body))
	}

	// Re-fetch snapshot to get new IDs after import
	_, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile/full", nil, nil)
	freshSnap := decodeJSON[domain.MainProfileSnapshot](t, body)

	// 36. POST /api/v1/branches/{id}/versions/1/edit -> Manual edit creating v2 with 201 Created
	var validSkillID, validExpID, validFactID string
	if len(freshSnap.Skills) > 0 {
		validSkillID = freshSnap.Skills[0].ID
	}
	if len(freshSnap.Experiences) > 0 {
		validExpID = freshSnap.Experiences[0].ID
		if len(freshSnap.Experiences[0].Facts) > 0 {
			validFactID = freshSnap.Experiences[0].Facts[0].ID
		}
	}

	manualTailored := &domain.TailoredContent{
		Summary: "Edited summary for Staff Role",
	}
	if validSkillID != "" {
		manualTailored.SkillGroups = []domain.TailoredSkillGroup{
			{
				Category:       "Core Skills",
				Skills:         []string{freshSnap.Skills[0].Name},
				SourceSkillIDs: []string{validSkillID},
			},
		}
	}
	if validExpID != "" && validFactID != "" {
		manualTailored.Experiences = []domain.TailoredExperience{
			{
				SourceEntityID: validExpID,
				Company:        freshSnap.Experiences[0].Company,
				RoleTitle:      freshSnap.Experiences[0].RoleTitle,
				StartDate:      freshSnap.Experiences[0].StartDate,
				Location:       freshSnap.Experiences[0].Location,
				Bullets: []domain.TailoredBullet{
					{
						Text:          "Refactored memory allocator.",
						SourceFactIDs: []string{validFactID},
					},
				},
			},
		}
	}

	editReq := api.EditVersionRequest{
		TailoredContent: manualTailored,
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/edit", editReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edit failed: %d, body: %s", resp.StatusCode, string(body))
	}
	v2 := decodeJSON[domain.BranchVersion](t, body)
	if v2.VersionNumber != 2 || v2.SourceType != domain.SourceTypeManualEdit {
		t.Fatalf("invalid v2 version from manual edit: %+v", v2)
	}

	// 37. POST /api/v1/branches/sync -> Batch sync
	syncReq := api.SyncBranchesRequest{
		BranchIDs: []string{branch.ID},
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/sync", syncReq, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /branches/sync failed: %d, body: %s", resp.StatusCode, string(body))
	}
	syncResp := decodeJSON[api.SyncBranchesResponse](t, body)
	if syncResp.EnqueuedCount != 1 {
		t.Fatalf("expected enqueued_count 1, got %d", syncResp.EnqueuedCount)
	}

	// 38. Cleanup Deletions
	deletions := []string{
		baseURL + "/api/v1/branches/" + branch.ID,
		baseURL + "/api/v1/presets/" + preset.ID,
		baseURL + "/api/v1/templates/" + tmpl.ID,
	}
	for _, delURL := range deletions {
		resp, _ = doRequest(t, client, http.MethodDelete, delURL, nil, nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DELETE %s failed: %d", delURL, resp.StatusCode)
		}
	}
}

// TestErrorEnvelopes verifies error codes and envelopes for 400, 404, 422, 500, and 503.
func TestErrorEnvelopes(t *testing.T) {
	env := setupTestServer(t, 1) // buffer capacity 1 to easily test 503 QUEUE_FULL
	client := env.server.Client()
	baseURL := env.server.URL

	t.Run("400 INVALID_REQUEST on malformed JSON", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/skills", "{not-valid-json", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400, got: %d", resp.StatusCode)
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected code %s, got: %s", api.CodeInvalidRequest, errResp.Error.Code)
		}
	})

	t.Run("400 INVALID_REQUEST on missing required fields", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/skills", map[string]string{}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400, got: %d", resp.StatusCode)
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected code %s, got: %s", api.CodeInvalidRequest, errResp.Error.Code)
		}
	})

	t.Run("400 INVALID_REQUEST on facts without query params", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/facts", nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400, got: %d", resp.StatusCode)
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected code %s, got: %s", api.CodeInvalidRequest, errResp.Error.Code)
		}
	})

	t.Run("400 INVALID_REQUEST on invalid Content-Type", func(t *testing.T) {
		headers := map[string]string{"Content-Type": "text/plain"}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/skills", `{"name":"Go"}`, headers)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400, got: %d", resp.StatusCode)
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected code %s, got: %s", api.CodeInvalidRequest, errResp.Error.Code)
		}
	})

	t.Run("413 PAYLOAD_TOO_LARGE on oversized JSON body", func(t *testing.T) {
		oversized := fmt.Sprintf(`{"name": "%s"}`, strings.Repeat("A", 11<<20))
		headers := map[string]string{"Content-Type": "application/json"}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/skills", oversized, headers)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 Payload Too Large, got: %d", resp.StatusCode)
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodePayloadTooLarge {
			t.Errorf("expected code %s, got: %s", api.CodePayloadTooLarge, errResp.Error.Code)
		}
	})

	t.Run("404 NOT_FOUND on non-existent resource", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/non-existent-id", nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404, got: %d", resp.StatusCode)
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeNotFound {
			t.Errorf("expected code %s, got: %s", api.CodeNotFound, errResp.Error.Code)
		}
	})

	t.Run("422 TEMPLATE_VALIDATION_ERROR on syntax error in template", func(t *testing.T) {
		tmplReq := api.CreateTemplateRequest{
			Name:             "Broken Template",
			HTMLTemplate:     "<h1>{{ .Unclosed",
			MarkdownTemplate: "# Ok",
			TextTemplate:     "Ok",
		}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates", tmplReq, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got: %d", resp.StatusCode)
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeTemplateValidationError {
			t.Errorf("expected code %s, got: %s", api.CodeTemplateValidationError, errResp.Error.Code)
		}
		if !strings.Contains(errResp.Error.Details, "unclosed") && !strings.Contains(errResp.Error.Details, "syntax") {
			t.Errorf("expected syntax details, got: %s", errResp.Error.Details)
		}
	})

	t.Run("422 PROVENANCE_ERROR on fabricated source fact ID", func(t *testing.T) {
		// First create a branch
		branchReq := api.CreateBranchRequest{
			Name:          "Branch For Provenance Test",
			TemplateSetID: renderer.TemplateStandardCleanID,
		}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches", branchReq, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create branch failed: %d", resp.StatusCode)
		}
		branch := decodeJSON[domain.Branch](t, body)

		// Create dummy version 1 in DB
		v1 := &domain.BranchVersion{
			ID:                 "ver-prov-test",
			BranchID:           branch.ID,
			Status:             domain.VersionStatusCompleted,
			MainRevisionUsed:   1,
			TemplateSetIDUsed:  renderer.TemplateStandardCleanID,
			SourceType:         domain.SourceTypeLLMGeneration,
		}
		if err := env.branchRepo.CreateVersion(context.Background(), v1); err != nil {
			t.Fatalf("failed to create v1: %v", err)
		}

		// Attempt manual edit with fabricated source fact ID
		fabricatedEdit := api.EditVersionRequest{
			TailoredContent: &domain.TailoredContent{
				Experiences: []domain.TailoredExperience{
					{
						SourceEntityID: "exp-fabricated",
						Company:        "Fake Corp",
						RoleTitle:      "Hacker",
						StartDate:      domain.NewDate(2020, 1, 1),
						Bullets: []domain.TailoredBullet{
							{
								Text:          "Fabricated bullet.",
								SourceFactIDs: []string{"fact-fabricated-999"},
							},
						},
					},
				},
			},
		}
		resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/versions/1/edit", fabricatedEdit, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got: %d, body: %s", resp.StatusCode, string(body))
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeProvenanceError {
			t.Errorf("expected code %s, got: %s", api.CodeProvenanceError, errResp.Error.Code)
		}
	})

	t.Run("503 QUEUE_FULL when worker pool channel is saturated", func(t *testing.T) {
		// Setup branch
		branchReq := api.CreateBranchRequest{
			Name:          "Branch Queue Full",
			TemplateSetID: renderer.TemplateStandardCleanID,
		}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches", branchReq, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create branch failed: %d", resp.StatusCode)
		}
		branch := decodeJSON[domain.Branch](t, body)

		// First generate should succeed (queue capacity 1)
		resp, _ = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/generate", nil, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 on first enqueue, got: %d", resp.StatusCode)
		}

		// Second generate should saturate queue and return 503 QUEUE_FULL
		resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/generate", nil, nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 on saturated queue, got: %d, body: %s", resp.StatusCode, string(body))
		}
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeQueueFull {
			t.Errorf("expected code %s, got: %s", api.CodeQueueFull, errResp.Error.Code)
		}
	})
}

// TestCORSAndMiddleware verifies CORS pre-flight, headers, and panic recovery.
func TestCORSAndMiddleware(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	t.Run("OPTIONS preflight returns 204 with CORS headers", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodOptions, baseURL+"/api/v1/branches", nil)
		if err != nil {
			t.Fatalf("failed to create options request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("options request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("expected 204 No Content for OPTIONS, got: %d", resp.StatusCode)
		}
		if allowOrigin := resp.Header.Get("Access-Control-Allow-Origin"); allowOrigin != "*" {
			t.Errorf("expected Access-Control-Allow-Origin: *, got: %s", allowOrigin)
		}
		if allowMethods := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(allowMethods, "POST") {
			t.Errorf("expected Access-Control-Allow-Methods to contain POST, got: %s", allowMethods)
		}
	})

	t.Run("GET response contains CORS header", func(t *testing.T) {
		resp, _ := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches", nil, nil)
		if allowOrigin := resp.Header.Get("Access-Control-Allow-Origin"); allowOrigin != "*" {
			t.Errorf("expected Access-Control-Allow-Origin: *, got: %s", allowOrigin)
		}
	})

	t.Run("Panic in handler recovers with 500 INTERNAL_ERROR", func(t *testing.T) {
		// Create a separate router with a panicking route wrapped by RecoveryMiddleware
		panickingMux := http.NewServeMux()
		panickingMux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) {
			panic("intentional test panic")
		})
		handler := api.RecoveryMiddleware(nil)(panickingMux)
		panicServer := httptest.NewServer(handler)
		defer panicServer.Close()

		resp, err := client.Get(panicServer.URL + "/panic")
		if err != nil {
			t.Fatalf("request to panic server failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected 500 on panic, got: %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		errResp := decodeJSON[api.ErrorResponse](t, body)
		if errResp.Error.Code != api.CodeInternalError {
			t.Errorf("expected code %s, got: %s", api.CodeInternalError, errResp.Error.Code)
		}
	})
}

// TestHealthEndpoints verifies that /api/v1/health, /healthz, and /health return 200 OK with status ok.
func TestHealthEndpoints(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	endpoints := []string{
		"/api/v1/health",
		"/healthz",
		"/health",
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			resp, body := doRequest(t, client, http.MethodGet, baseURL+endpoint, nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 OK for %s, got %d", endpoint, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Errorf("expected application/json, got %s", ct)
			}
			res := decodeJSON[map[string]string](t, body)
			if res["status"] != "ok" {
				t.Errorf("expected status 'ok', got %q", res["status"])
			}
		})
	}
}

func TestFactHandlers_OwnerValidation(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	// 1. Invalid OwnerType
	badTypeReq := api.CreateFactRequest{
		OwnerID:   "exp-123",
		OwnerType: domain.OwnerType("department"),
		Statement: "Led quarterly OKR reviews.",
	}
	resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/facts", badTypeReq, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid owner_type, got %d (body: %s)", resp.StatusCode, string(body))
	}

	// 2. Non-existent OwnerID
	nonExistentOwnerReq := api.CreateFactRequest{
		OwnerID:   "exp-non-existent-999",
		OwnerType: domain.OwnerTypeWorkExperience,
		Statement: "Led quarterly OKR reviews.",
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/facts", nonExistentOwnerReq, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for non-existent owner_id, got %d (body: %s)", resp.StatusCode, string(body))
	}
}

func buildTestPDF(text string) []byte {
	stream := fmt.Sprintf("BT\n/F1 12 Tf\n100 700 Td\n(%s) Tj\nET", text)
	body := fmt.Sprintf("%%PDF-1.4\n"+
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"+
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n"+
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n"+
		"4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n"+
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
		len(stream), stream)

	off1 := 9
	off2 := off1 + len("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off3 := off2 + len("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off4 := off3 + len("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	off5 := off4 + len(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream))
	startXref := len(body)

	xrefAndTrailer := fmt.Sprintf("xref\n0 6\n0000000000 65535 f \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF",
		off1, off2, off3, off4, off5, startXref)

	return []byte(body + xrefAndTrailer)
}

type mockModelEnumeratingExtractor struct {
	completer func(ctx context.Context, sys, user, prov, model string) (string, error)
	models    []domain.ModelOption
}

func (m *mockModelEnumeratingExtractor) ExtractResume(ctx context.Context, text string, provider string, model string) (*profile.JSONResume, error) {
	if m.completer != nil {
		_, err := m.completer(ctx, "", text, provider, model)
		if err != nil {
			return nil, err
		}
	}
	return &profile.JSONResume{
		Basics: profile.JSONResumeBasics{Name: "Morgan BrandNew"},
	}, nil
}

func (m *mockModelEnumeratingExtractor) AvailableModels() []domain.ModelOption {
	return m.models
}

func setupTestServerWithExtractor(t *testing.T, resumeExtractor llm.ResumeExtractor) *testEnv {
	return setupTestServerWithExtractorAndLogger(t, resumeExtractor, nil)
}

func setupTestServerWithExtractorAndLogger(t *testing.T, resumeExtractor llm.ResumeExtractor, logger *slog.Logger) *testEnv {
	return setupTestServerWithExtractorLoggerAndPDFEnqueuer(t, resumeExtractor, logger, nil)
}

func setupTestServerWithPDFEnqueuer(t *testing.T, resumeExtractor llm.ResumeExtractor, pdfEnqueuer *api.ChannelPDFJobEnqueuer) *testEnv {
	return setupTestServerWithExtractorLoggerAndPDFEnqueuer(t, resumeExtractor, nil, pdfEnqueuer)
}

func setupTestServerWithExtractorLoggerAndPDFEnqueuer(t *testing.T, resumeExtractor llm.ResumeExtractor, logger *slog.Logger, pdfEnqueuer *api.ChannelPDFJobEnqueuer) *testEnv {
	t.Helper()

	ctx := context.Background()
	defaultUserID := "default"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	profileRepo := store
	presetRepo := store
	templateRepo := store
	branchRepo := store
	pdfJobRepo := store
	profileSvc := profile.NewService(profileRepo, branchRepo)
	templateRenderer := renderer.NewTemplateRenderer()
	provValidator := validator.New()
	enqueuer := api.NewChannelJobEnqueuer(100)

	var enq api.PDFJobEnqueuer
	if pdfEnqueuer != nil {
		enq = pdfEnqueuer
	}

	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:     profileRepo,
		PresetRepo:      presetRepo,
		TemplateRepo:    templateRepo,
		BranchRepo:      branchRepo,
		ProfileService:  profileSvc,
		PDFJobRepo:      pdfJobRepo,
		PDFJobEnqueuer:  enq,
		PDFExtractor:    pdf.NewExtractor(),
		ResumeExtractor: resumeExtractor,
		Renderer:        templateRenderer,
		Validator:       provValidator,
		JobEnqueuer:     enqueuer,
		DefaultUserID:   defaultUserID,
		Admin:           true,
		QuotaRepo:       store,
		Logger:          logger,
	})

	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
	})

	return &testEnv{
		server:       server,
		db:           store,
		profileRepo:  profileRepo,
		presetRepo:   presetRepo,
		templateRepo: templateRepo,
		branchRepo:   branchRepo,
		profileSvc:   profileSvc,
		enqueuer:     enqueuer,
		pdfJobRepo:   pdfJobRepo,
		pdfEnqueuer:  pdfEnqueuer,
	}
}

func TestProfilePDF_Parse(t *testing.T) {
	mockResume := &profile.JSONResume{
		Basics: profile.JSONResumeBasics{
			Name:    "Morgan BrandNew",
			Email:   "morgan@brandnew.dev",
			Summary: "Full-Stack Engineer with 5 years experience.",
		},
		Skills: []profile.JSONResumeSkill{
			{Name: "Go", Level: "Expert"},
			{Name: "PostgreSQL", Level: "Senior"},
		},
		Work: []profile.JSONResumeWork{
			{
				Name:       "BrandNew Tech",
				Position:   "Senior Developer",
				StartDate:  "2022-01-01",
				Summary:    "Built cloud platforms.",
				Highlights: []string{"Reduced latency by 50%"},
			},
		},
	}

	mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
		data, _ := json.Marshal(mockResume)
		return string(data), nil
	}))

	env := setupTestServerWithExtractor(t, mockExtractor)
	client := env.server.Client()
	baseURL := env.server.URL

	t.Run("Missing file field returns 400 Bad Request", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("provider", "mock")
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Empty file returns 400 Bad Request", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, _ := mw.CreateFormFile("file", "empty.pdf")
		_, _ = part.Write([]byte{})
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Corrupted PDF returns 400 Bad Request", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, _ := mw.CreateFormFile("file", "corrupt.pdf")
		_, _ = part.Write([]byte("not a valid pdf file"))
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Valid PDF parses and returns JSONResume draft", func(t *testing.T) {
		pdfBytes := buildTestPDF("Morgan BrandNew Full-Stack Engineer")

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("create form file failed: %v", err)
		}
		_, _ = part.Write(pdfBytes)
		_ = mw.WriteField("provider", "mock")
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected status 200, got %d (body: %s)", resp.StatusCode, string(body))
		}

		var parsed profile.JSONResume
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if parsed.Basics.Name != "Morgan BrandNew" {
			t.Errorf("expected name 'Morgan BrandNew', got: %q", parsed.Basics.Name)
		}
		if len(parsed.Skills) != 2 {
			t.Errorf("expected 2 skills, got: %d", len(parsed.Skills))
		}
		if len(parsed.Work) != 1 || parsed.Work[0].GetCompany() != "BrandNew Tech" {
			t.Errorf("expected 1 work at BrandNew Tech, got: %+v", parsed.Work)
		}
	})

	t.Run("Context deadline is enforced to 10 minutes", func(t *testing.T) {
		pdfBytes := buildTestPDF("Morgan BrandNew Full-Stack Engineer")
		var capturedDeadline time.Time
		var hasDeadline bool

		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			capturedDeadline, hasDeadline = ctx.Deadline()
			return `{"basics":{"name":"Morgan BrandNew"}}`, nil
		}))

		env := setupTestServerWithExtractor(t, mockExtractor)
		client := env.server.Client()
		baseURL := env.server.URL

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("create form file failed: %v", err)
		}
		_, _ = part.Write(pdfBytes)
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		if !hasDeadline {
			t.Fatalf("expected extractor context to have a deadline")
		}

		remaining := time.Until(capturedDeadline)
		if remaining < 9*time.Minute+50*time.Second || remaining > 10*time.Minute {
			t.Fatalf("expected deadline around 10 minutes, got %v", remaining)
		}
	})
}

func TestProfilePDF_Parse_AsyncLifecycle(t *testing.T) {
	pdfBytes := buildTestPDF("Morgan BrandNew Full-Stack Engineer")

	t.Run("Enqueue job, verify 202 Accepted, and poll status through completion", func(t *testing.T) {
		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			return `{"basics":{"name":"Morgan BrandNew"}}`, nil
		}))

		enqueuer := api.NewChannelPDFJobEnqueuer(10)
		env := setupTestServerWithPDFEnqueuer(t, mockExtractor, enqueuer)
		client := env.server.Client()
		baseURL := env.server.URL

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("create form file failed: %v", err)
		}
		_, _ = part.Write(pdfBytes)
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 202 Accepted, got %d (body: %s)", resp.StatusCode, string(body))
		}

		var accepted api.AsyncPDFJobResponse
		if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
			t.Fatalf("failed to decode accepted response: %v", err)
		}
		if accepted.JobID == "" {
			t.Fatalf("expected non-empty job ID")
		}
		if accepted.Status != domain.PDFParseJobStatusPending {
			t.Errorf("expected pending status, got %s", accepted.Status)
		}

		// Verify task was queued into enqueuer
		select {
		case task := <-enqueuer.Channel():
			if task.JobID != accepted.JobID {
				t.Errorf("expected queued job ID %s, got %s", accepted.JobID, task.JobID)
			}
		default:
			t.Fatalf("expected task in enqueuer channel")
		}

		// Query job status via GET /api/v1/profile/pdf/jobs/{id}
		getReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/profile/pdf/jobs/"+accepted.JobID, nil)
		getResp, err := client.Do(getReq)
		if err != nil {
			t.Fatalf("get job failed: %v", err)
		}
		defer func() { _ = getResp.Body.Close() }()

		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", getResp.StatusCode)
		}
		var job domain.PDFParseJob
		if err := json.NewDecoder(getResp.Body).Decode(&job); err != nil {
			t.Fatalf("failed to decode job: %v", err)
		}
		if job.ID != accepted.JobID || job.Status != domain.PDFParseJobStatusPending {
			t.Errorf("expected pending job %s, got %+v", accepted.JobID, job)
		}

		// Cross-user isolation: user B should receive 404
		isoReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/profile/pdf/jobs/"+accepted.JobID, nil)
		isoReq.Header.Set("X-User-ID", "other-user")
		isoResp, err := client.Do(isoReq)
		if err != nil {
			t.Fatalf("isolated request failed: %v", err)
		}
		_ = isoResp.Body.Close()
		if isoResp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 for cross-user job access, got %d", isoResp.StatusCode)
		}

		// Simulate worker completing the job
		extractedResume := &profile.JSONResume{
			Basics: profile.JSONResumeBasics{Name: "Morgan BrandNew"},
		}
		if err := env.pdfJobRepo.UpdateJobStatus(context.Background(), accepted.JobID, domain.PDFParseJobStatusCompleted, extractedResume, ""); err != nil {
			t.Fatalf("failed to update job status: %v", err)
		}

		// Poll again and verify completion with structured result
		pollReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/profile/pdf/jobs/"+accepted.JobID, nil)
		pollResp, err := client.Do(pollReq)
		if err != nil {
			t.Fatalf("poll job failed: %v", err)
		}
		defer func() { _ = pollResp.Body.Close() }()

		var completedJob domain.PDFParseJob
		if err := json.NewDecoder(pollResp.Body).Decode(&completedJob); err != nil {
			t.Fatalf("failed to decode completed job: %v", err)
		}
		if completedJob.Status != domain.PDFParseJobStatusCompleted {
			t.Errorf("expected completed status, got %s", completedJob.Status)
		}
		if completedJob.Result == nil {
			t.Fatalf("expected non-nil completed job result")
		}
		resMap, ok := completedJob.Result.(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any result, got %T", completedJob.Result)
		}
		basics, ok := resMap["basics"].(map[string]any)
		if !ok || basics["name"] != "Morgan BrandNew" {
			t.Errorf("expected result with name Morgan BrandNew, got %+v", resMap)
		}
	})

	t.Run("Queue full returns 503 SERVICE_UNAVAILABLE", func(t *testing.T) {
		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			return `{}`, nil
		}))

		// Zero capacity enqueuer to instantly trigger ErrQueueFull
		enqueuer := api.NewChannelPDFJobEnqueuer(1)
		// Pre-fill buffer
		_ = enqueuer.EnqueuePDFJob(worker.PDFParseJobTask{JobID: "fill"})

		env := setupTestServerWithPDFEnqueuer(t, mockExtractor, enqueuer)
		client := env.server.Client()
		baseURL := env.server.URL

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, _ := mw.CreateFormFile("file", "resume.pdf")
		_, _ = part.Write(pdfBytes)
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d", resp.StatusCode)
		}
	})

	t.Run("Non-existent job returns 404 NOT_FOUND", func(t *testing.T) {
		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			return `{}`, nil
		}))
		env := setupTestServerWithExtractor(t, mockExtractor)
		client := env.server.Client()
		baseURL := env.server.URL

		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/profile/pdf/jobs/non-existent-id", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", resp.StatusCode)
		}
	})
}

func TestProfilePDF_Parse_ErrorHandling(t *testing.T) {
	pdfBytes := buildTestPDF("Morgan BrandNew Full-Stack Engineer")

	t.Run("Oversized payload returns 413 Payload Too Large", func(t *testing.T) {
		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			return `{}`, nil
		}))
		env := setupTestServerWithExtractor(t, mockExtractor)
		client := env.server.Client()
		baseURL := env.server.URL

		// Create 11MB body
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "large.pdf")
		if err != nil {
			t.Fatalf("failed to create form file: %v", err)
		}
		// Write 11 MB of zeros
		zeros := make([]byte, 11<<20)
		if _, err := part.Write(zeros); err != nil {
			t.Fatalf("failed to write zeros: %v", err)
		}
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected status 413, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodePayloadTooLarge {
			t.Errorf("expected code %s, got %s", api.CodePayloadTooLarge, errResp.Error.Code)
		}
	})

	tests := []struct {
		name           string
		extractorErr   error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "ErrNoProviders returns 503 SERVICE_UNAVAILABLE",
			extractorErr:   llm.ErrNoProviders,
			expectedStatus: http.StatusServiceUnavailable,
			expectedCode:   api.CodeServiceUnavailable,
		},
		{
			name:           "ErrUnauthorized returns 502 UPSTREAM_ERROR",
			extractorErr:   llm.ErrUnauthorized,
			expectedStatus: http.StatusBadGateway,
			expectedCode:   api.CodeUpstreamError,
		},
		{
			name:           "ErrRateLimited returns 429 UPSTREAM_ERROR",
			extractorErr:   llm.ErrRateLimited,
			expectedStatus: http.StatusTooManyRequests,
			expectedCode:   api.CodeUpstreamError,
		},
		{
			name:           "ErrUpstream returns 502 UPSTREAM_ERROR",
			extractorErr:   llm.ErrUpstream,
			expectedStatus: http.StatusBadGateway,
			expectedCode:   api.CodeUpstreamError,
		},
		{
			name:           "ErrInvalidResponse returns 502 UPSTREAM_ERROR",
			extractorErr:   llm.ErrInvalidResponse,
			expectedStatus: http.StatusBadGateway,
			expectedCode:   api.CodeUpstreamError,
		},
		{
			name:           "ErrProviderNotFound returns 400 INVALID_REQUEST",
			extractorErr:   llm.ErrProviderNotFound,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   api.CodeInvalidRequest,
		},
		{
			name:           "context.DeadlineExceeded returns 504 UPSTREAM_ERROR",
			extractorErr:   context.DeadlineExceeded,
			expectedStatus: http.StatusGatewayTimeout,
			expectedCode:   api.CodeUpstreamError,
		},
		{
			name:           "domain.ErrValidation returns 400 INVALID_REQUEST",
			extractorErr:   domain.ErrValidation,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   api.CodeInvalidRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
				return "", tc.extractorErr
			}))

			env := setupTestServerWithExtractor(t, mockExtractor)
			client := env.server.Client()
			baseURL := env.server.URL

			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			part, err := mw.CreateFormFile("file", "resume.pdf")
			if err != nil {
				t.Fatalf("create form file failed: %v", err)
			}
			_, _ = part.Write(pdfBytes)
			_ = mw.Close()

			req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
			req.Header.Set("Content-Type", mw.FormDataContentType())

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tc.expectedStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected status %d, got %d (body: %s)", tc.expectedStatus, resp.StatusCode, string(body))
			}

			var errResp api.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}
			if errResp.Error.Code != tc.expectedCode {
				t.Errorf("expected code %s, got %s", tc.expectedCode, errResp.Error.Code)
			}
		})
	}

	t.Run("Unexpected extractor error logs structured ERROR and returns 500", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			return "", errors.New("unexpected catastrophic failure")
		}))

		env := setupTestServerWithExtractorAndLogger(t, mockExtractor, logger)
		client := env.server.Client()
		baseURL := env.server.URL

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("create form file failed: %v", err)
		}
		_, _ = part.Write(pdfBytes)
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected status 500, got %d", resp.StatusCode)
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "ERROR") {
			t.Errorf("expected logs to contain ERROR level, got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "unexpected catastrophic failure") {
			t.Errorf("expected logs to contain error details, got:\n%s", logOutput)
		}
	})

	t.Run("Client error like ErrProviderNotFound logs structured WARN and returns 400", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			return "", llm.ErrProviderNotFound
		}))

		env := setupTestServerWithExtractorAndLogger(t, mockExtractor, logger)
		client := env.server.Client()
		baseURL := env.server.URL

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("create form file failed: %v", err)
		}
		_, _ = part.Write(pdfBytes)
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", resp.StatusCode)
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "WARN") {
			t.Errorf("expected logs to contain WARN level, got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "requested LLM provider not configured") {
			t.Errorf("expected logs to contain warning message, got:\n%s", logOutput)
		}
	})

	t.Run("Omitting provider and model resolves from user main profile", func(t *testing.T) {
		pdfBytes := buildTestPDF("Morgan BrandNew Full-Stack Engineer")
		var capturedProvider, capturedModel string

		mockExtractor := llm.NewResumeExtractor(llm.CompleterFunc(func(ctx context.Context, sys, user, prov, model string) (string, error) {
			capturedProvider = prov
			capturedModel = model
			return `{"basics":{"name":"Morgan BrandNew"}}`, nil
		}))

		env := setupTestServerWithExtractor(t, mockExtractor)
		client := env.server.Client()
		baseURL := env.server.URL

		// Set default provider and model on user's profile
		prof, err := env.profileRepo.GetProfile(context.Background(), "default")
		if err != nil {
			t.Fatalf("failed to get profile: %v", err)
		}
		prof.DefaultProvider = "custom-provider"
		prof.DefaultModel = "custom-model"
		if err := env.profileRepo.UpsertProfile(context.Background(), prof); err != nil {
			t.Fatalf("failed to update profile: %v", err)
		}

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("create form file failed: %v", err)
		}
		_, _ = part.Write(pdfBytes)
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		if capturedProvider != "custom-provider" {
			t.Errorf("expected provider 'custom-provider', got %q", capturedProvider)
		}
		if capturedModel != "custom-model" {
			t.Errorf("expected model 'custom-model', got %q", capturedModel)
		}
	})

	t.Run("ModelEnumerator validates profile defaults and handles aliases", func(t *testing.T) {
		pdfBytes := buildTestPDF("Morgan BrandNew Full-Stack Engineer")

		testCases := []struct {
			name             string
			profileProvider  string
			profileModel     string
			reqProvider      string
			reqModel         string
			availableModels  []domain.ModelOption
			expectedProvider string
			expectedModel    string
		}{
			{
				name:            "profile provider matching available model is selected",
				profileProvider: "openai",
				profileModel:    "gpt-4o",
				availableModels: []domain.ModelOption{
					{Provider: "openai", Model: "gpt-4o"},
					{Provider: "gemini", Model: "gemini-2.5-flash"},
				},
				expectedProvider: "openai",
				expectedModel:    "gpt-4o",
			},
			{
				name:            "profile provider unavailable gracefully defaults to empty",
				profileProvider: "unsupported-cloud",
				profileModel:    "unknown-model",
				availableModels: []domain.ModelOption{
					{Provider: "openai", Model: "gpt-4o"},
				},
				expectedProvider: "",
				expectedModel:    "",
			},
			{
				name:            "provider alias matching for openai-compat and compat",
				profileProvider: "openai-compat",
				profileModel:    "llama-3.3-70b",
				availableModels: []domain.ModelOption{
					{Provider: "compat", Model: "llama-3.3-70b"},
				},
				expectedProvider: "openai-compat",
				expectedModel:    "llama-3.3-70b",
			},
			{
				name:             "reqProvider supplied with empty model resolves model from profile default",
				profileProvider:  "openai",
				profileModel:     "gpt-4o",
				reqProvider:      "openai",
				reqModel:         "",
				availableModels:  []domain.ModelOption{{Provider: "openai", Model: "gpt-4o"}},
				expectedProvider: "openai",
				expectedModel:    "gpt-4o",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				var capturedProvider, capturedModel string

				mockExt := &mockModelEnumeratingExtractor{
					completer: func(ctx context.Context, sys, user, prov, model string) (string, error) {
						capturedProvider = prov
						capturedModel = model
						return `{"basics":{"name":"Morgan BrandNew"}}`, nil
					},
					models: tc.availableModels,
				}

				env := setupTestServerWithExtractor(t, mockExt)
				client := env.server.Client()
				baseURL := env.server.URL

				prof, err := env.profileRepo.GetProfile(context.Background(), "default")
				if err != nil {
					t.Fatalf("failed to get profile: %v", err)
				}
				prof.DefaultProvider = tc.profileProvider
				prof.DefaultModel = tc.profileModel
				if err := env.profileRepo.UpsertProfile(context.Background(), prof); err != nil {
					t.Fatalf("failed to update profile: %v", err)
				}

				var buf bytes.Buffer
				mw := multipart.NewWriter(&buf)
				part, err := mw.CreateFormFile("file", "resume.pdf")
				if err != nil {
					t.Fatalf("create form file failed: %v", err)
				}
				_, _ = part.Write(pdfBytes)
				if tc.reqProvider != "" {
					_ = mw.WriteField("provider", tc.reqProvider)
				}
				if tc.reqModel != "" {
					_ = mw.WriteField("model", tc.reqModel)
				}
				_ = mw.Close()

				req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
				req.Header.Set("Content-Type", mw.FormDataContentType())

				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("request failed: %v", err)
				}
				defer func() { _ = resp.Body.Close() }()

				if resp.StatusCode != http.StatusOK {
					t.Fatalf("expected status 200, got %d", resp.StatusCode)
				}

				if capturedProvider != tc.expectedProvider {
					t.Errorf("expected provider %q, got %q", tc.expectedProvider, capturedProvider)
				}
				if capturedModel != tc.expectedModel {
					t.Errorf("expected model %q, got %q", tc.expectedModel, capturedModel)
				}
			})
		}
	})

	t.Run("PDF upload writes prompt and output markdown files to data/llm", func(t *testing.T) {
		pdfBytes := buildTestPDF("Morgan BrandNew Full-Stack Engineer")
		debugDir := t.TempDir()

		// Mock upstream HTTP server returning valid OpenAI chat completion
		upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]any{
							"role":    "assistant",
							"content": `{"basics":{"name":"Morgan BrandNew","email":"morgan@brandnew.dev"}}`,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer upstreamServer.Close()

		// Configure LLM registry with prompt debugging enabled targeting debugDir
		registry := llm.NewRegistry(llm.WithDebugPrompts(true, debugDir))
		llm.RegisterClients(registry, llm.ProviderConfig{
			CompatBaseURL: upstreamServer.URL + "/v1",
			CompatAPIKey:  "test-key",
		})
		extractor := llm.NewDefaultResumeExtractor(registry)

		env := setupTestServerWithExtractor(t, extractor)
		client := env.server.Client()
		baseURL := env.server.URL

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("create form file failed: %v", err)
		}
		_, _ = part.Write(pdfBytes)
		_ = mw.Close()

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/profile/pdf/parse", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected status 200, got %d (body: %s)", resp.StatusCode, string(body))
		}

		// Verify files written to debugDir (partitioned by tenant)
		targetDir := filepath.Join(debugDir, "default")
		entries, err := os.ReadDir(targetDir)
		if err != nil {
			t.Fatalf("failed to read debugDir: %v", err)
		}

		var promptFiles, outputFiles []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), "_output.md") {
				outputFiles = append(outputFiles, e.Name())
			} else if strings.HasSuffix(e.Name(), ".md") {
				promptFiles = append(promptFiles, e.Name())
			}
		}

		if len(promptFiles) != 1 {
			t.Fatalf("expected exactly 1 prompt file, got %d (%v)", len(promptFiles), promptFiles)
		}
		if len(outputFiles) != 1 {
			t.Fatalf("expected exactly 1 output file, got %d (%v)", len(outputFiles), outputFiles)
		}

		promptContent, err := os.ReadFile(filepath.Join(targetDir, promptFiles[0]))
		if err != nil {
			t.Fatalf("failed to read prompt file: %v", err)
		}
		if !strings.Contains(string(promptContent), "# LLM Prompt: extract") {
			t.Errorf("expected prompt file to contain '# LLM Prompt: extract', got:\n%s", string(promptContent))
		}
		if !strings.Contains(string(promptContent), "Morgan BrandNew Full-Stack Engineer") {
			t.Errorf("expected prompt file to contain extracted resume text, got:\n%s", string(promptContent))
		}

		outputContent, err := os.ReadFile(filepath.Join(targetDir, outputFiles[0]))
		if err != nil {
			t.Fatalf("failed to read output file: %v", err)
		}
		if !strings.Contains(string(outputContent), "# LLM Response: extract") {
			t.Errorf("expected output file to contain '# LLM Response: extract', got:\n%s", string(outputContent))
		}
		if !strings.Contains(string(outputContent), "Morgan BrandNew") {
			t.Errorf("expected output file to contain raw response json, got:\n%s", string(outputContent))
		}
	})
}

func TestProfilePDF_Commit(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	candidateJSON := `{
		"basics": {
			"name": "Morgan BrandNew",
			"email": "morgan@brandnew.dev",
			"phone": "+1-555-4321",
			"summary": "Full-Stack Engineer with 5 years experience.",
			"location": {
				"city": "Austin",
				"region": "TX",
				"countryCode": "US"
			}
		},
		"skills": [
			{"name": "Go", "level": "Expert"},
			{"name": "PostgreSQL", "level": "Senior"}
		],
		"work": [
			{
				"name": "BrandNew Tech",
				"position": "Senior Developer",
				"startDate": "2022-01-01",
				"summary": "Built cloud platforms.",
				"highlights": ["Reduced latency by 50%"]
			}
		],
		"education": [
			{
				"institution": "UT Austin",
				"area": "Computer Science",
				"studyType": "B.S.",
				"startDate": "2016-09-01",
				"endDate": "2020-05-01"
			}
		],
		"projects": [
			{
				"name": "CloudEngine",
				"description": "Distributed task queue in Go",
				"highlights": ["Zero dependencies"]
			}
		]
	}`

	t.Run("Empty payload returns 400 Bad Request", func(t *testing.T) {
		resp, _ := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/profile/pdf/commit", []byte{}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Malformed JSON returns 400 Bad Request", func(t *testing.T) {
		resp, _ := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/profile/pdf/commit", []byte("{not json"), nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Valid commit for existing user updates profile and increments revision", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/profile/pdf/commit", []byte(candidateJSON), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d (body: %s)", resp.StatusCode, string(body))
		}

		var snap domain.MainProfileSnapshot
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatalf("failed to decode snapshot: %v", err)
		}

		if snap.Profile.FullName != "Morgan BrandNew" {
			t.Errorf("expected FullName 'Morgan BrandNew', got: %q", snap.Profile.FullName)
		}
		if snap.MainRevision < 2 {
			t.Errorf("expected MainRevision >= 2, got: %d", snap.MainRevision)
		}
		if len(snap.Skills) != 2 {
			t.Errorf("expected 2 skills, got: %d", len(snap.Skills))
		}
		if len(snap.Experiences) != 1 {
			t.Errorf("expected 1 experience, got: %d", len(snap.Experiences))
		}
	})

	t.Run("Valid commit for NEW user who has never filled anything initializes main resume", func(t *testing.T) {
		newUserID := "new-user-fresh-9999"
		headers := map[string]string{
			"X-User-ID": newUserID,
		}

		// 1. Commit draft to brand new user
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/profile/pdf/commit", []byte(candidateJSON), headers)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 for new user commit, got %d (body: %s)", resp.StatusCode, string(body))
		}

		var snap domain.MainProfileSnapshot
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatalf("failed to decode snapshot: %v", err)
		}

		if snap.Profile.FullName != "Morgan BrandNew" {
			t.Errorf("expected FullName 'Morgan BrandNew', got: %q", snap.Profile.FullName)
		}
		if snap.Profile.Email != "morgan@brandnew.dev" {
			t.Errorf("expected Email 'morgan@brandnew.dev', got: %q", snap.Profile.Email)
		}
		if snap.MainRevision != 1 {
			t.Errorf("expected MainRevision 1 for fresh user, got: %d", snap.MainRevision)
		}
		if len(snap.Skills) != 2 {
			t.Errorf("expected 2 skills, got: %d", len(snap.Skills))
		}
		if len(snap.Experiences) != 1 {
			t.Errorf("expected 1 experience, got: %d", len(snap.Experiences))
		}
		if len(snap.Education) != 1 {
			t.Errorf("expected 1 education, got: %d", len(snap.Education))
		}
		if len(snap.Projects) != 1 {
			t.Errorf("expected 1 project, got: %d", len(snap.Projects))
		}

		// 2. Query GET /api/v1/profile for this new user to ensure main profile is fully persisted
		getResp, getBody := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile", nil, headers)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for GET /profile on new user, got %d (body: %s)", getResp.StatusCode, string(getBody))
		}
		var prof domain.Profile
		if err := json.Unmarshal(getBody, &prof); err != nil {
			t.Fatalf("failed to decode profile: %v", err)
		}
		if prof.FullName != "Morgan BrandNew" {
			t.Errorf("expected FullName 'Morgan BrandNew', got: %q", prof.FullName)
		}
		if prof.MainRevision != 1 {
			t.Errorf("expected MainRevision 1, got: %d", prof.MainRevision)
		}
	})
}

type mockModelEnumerator struct {
	models []domain.ModelOption
}

func (m *mockModelEnumerator) AvailableModels() []domain.ModelOption {
	return m.models
}

func TestModelsHandler_ListAvailableModels(t *testing.T) {
	mockModels := []domain.ModelOption{
		{
			Provider:    "gemini",
			Model:       "gemini-2.5-pro",
			DisplayName: "Google Gemini — gemini-2.5-pro",
			IsDefault:   true,
		},
		{
			Provider:    "openai",
			Model:       "gpt-4o",
			DisplayName: "OpenAI — gpt-4o",
			IsDefault:   false,
		},
	}

	router := api.NewRouter(api.RouterConfig{
		ModelEnumerator: &mockModelEnumerator{models: mockModels},
	})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, body := doRequest(t, server.Client(), http.MethodGet, server.URL+"/api/v1/models", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	var models []domain.ModelOption
	if err := json.Unmarshal(body, &models); err != nil {
		t.Fatalf("failed to decode models: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Model != "gemini-2.5-pro" || !models[0].IsDefault {
		t.Errorf("unexpected model[0]: %+v", models[0])
	}

	// Security audit: check that no secret credentials appear in raw body
	raw := string(body)
	for _, forbidden := range []string{"api_key", "apiKey", "secret", "token"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("forbidden credential field %q found in /api/v1/models response: %s", forbidden, raw)
		}
	}
}

func TestDefaultBranchAPI(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL
	defaultBranchID := domain.DefaultBranchID("default")

	t.Run("DELETE /api/v1/branches/{id} on default branch returns 400 Bad Request", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodDelete, baseURL+"/api/v1/branches/"+defaultBranchID, nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", resp.StatusCode, string(body))
		}
		errEnv := decodeJSON[api.ErrorResponse](t, body)
		if errEnv.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected code %s, got %s", api.CodeInvalidRequest, errEnv.Error.Code)
		}
	})

	t.Run("POST /api/v1/branches with is_default=true returns 400 Bad Request", func(t *testing.T) {
		isDef := true
		req := api.CreateBranchRequest{
			Name:          "Duplicate Default",
			TemplateSetID: renderer.TemplateStandardCleanID,
			IsDefault:     &isDef,
		}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches", req, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("PUT /api/v1/branches/{id} updates template_set_id and re-renders", func(t *testing.T) {
		newTmpl := renderer.TemplateDetailedImmigrationCVID
		req := api.UpdateBranchRequest{
			TemplateSetID: &newTmpl,
		}
		resp, body := doRequest(t, client, http.MethodPut, baseURL+"/api/v1/branches/"+defaultBranchID, req, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
		}
		updated := decodeJSON[domain.Branch](t, body)
		if updated.TemplateSetID != newTmpl {
			t.Errorf("expected template %s, got %s", newTmpl, updated.TemplateSetID)
		}
	})

	t.Run("POST /api/v1/branches/{id}/generate executes direct render with status completed", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+defaultBranchID+"/generate", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
		}
		jobResp := decodeJSON[api.AsyncJobEnqueued](t, body)
		if jobResp.Status != domain.VersionStatusCompleted {
			t.Errorf("expected status completed, got %s", jobResp.Status)
		}
	})
}

func TestBranch_GapAnalysisEndpoint(t *testing.T) {
	env := setupTestServer(t, 100)
	client := env.server.Client()
	baseURL := env.server.URL

	// 1. Unknown branch returns 404
	resp, _ := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/non-existent-id/gap-analysis", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 NotFound for unknown branch, got %d", resp.StatusCode)
	}

	// 2. Create a targeted branch
	createReq := map[string]any{
		"name":                "Staff SRE - Stripe",
		"target_role":         "Staff Site Reliability Engineer",
		"target_company":      "Stripe",
		"target_requirements": "Distributed Systems, Raft, Kubernetes Operators",
		"template_set_id":     renderer.TemplateStandardCleanID,
	}
	createResp, createBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches", createReq, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", createResp.StatusCode, string(createBody))
	}
	branch := decodeJSON[domain.Branch](t, createBody)

	// 3. Request decoupled fast gap analysis
	gapResp, gapBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/gap-analysis", nil, nil)
	if gapResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from gap analysis endpoint, got %d: %s", gapResp.StatusCode, string(gapBody))
	}

	report := decodeJSON[domain.GapReport](t, gapBody)
	if report.MatchScore == nil {
		t.Errorf("expected non-nil MatchScore from gap analysis endpoint")
	}
	if report.OverallMatchSummary == "" {
		t.Errorf("expected non-empty OverallMatchSummary")
	}
}

// TestEntityCreation_ShortID verifies that REST API endpoints for experiences, skills, and facts
// auto-populate short_id with compact alphanumeric strings, respect custom user-supplied short_id values,
// and correctly resolve parent entity short IDs for child facts.
func TestEntityCreation_ShortID(t *testing.T) {
	env := setupTestServer(t, 100)
	client := env.server.Client()
	baseURL := env.server.URL

	t.Run("CreateExperience_AutoShortID", func(t *testing.T) {
		req := map[string]any{
			"company":    "Google",
			"role_title": "Software Engineer",
			"start_date": "2022-01-01",
		}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/experiences", req, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, string(body))
		}
		var exp domain.WorkExperience
		if err := json.Unmarshal(body, &exp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !strings.HasPrefix(exp.ID, "exp-") || len(exp.ID) < 30 {
			t.Errorf("expected long UUID id for experience, got %q", exp.ID)
		}
		if !strings.HasPrefix(exp.ShortID, "exp-") || len(exp.ShortID) > 15 {
			t.Errorf("expected compact short_id for experience, got %q", exp.ShortID)
		}
	})

	t.Run("CreateExperience_CustomShortID", func(t *testing.T) {
		req := map[string]any{
			"company":    "Google",
			"role_title": "Senior Engineer",
			"start_date": "2022-01-01",
			"short_id":   "exp-google",
		}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/experiences", req, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, string(body))
		}
		var exp domain.WorkExperience
		if err := json.Unmarshal(body, &exp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if exp.ShortID != "exp-google" {
			t.Errorf("expected short_id == 'exp-google', got %q", exp.ShortID)
		}
	})

	t.Run("CreateFact_AutoShortID", func(t *testing.T) {
		// First create an experience with custom short ID
		expReq := map[string]any{
			"company":    "Stripe",
			"role_title": "Staff Engineer",
			"start_date": "2021-01-01",
			"short_id":   "exp-stripe",
		}
		respExp, bodyExp := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/experiences", expReq, nil)
		if respExp.StatusCode != http.StatusCreated {
			t.Fatalf("failed to create experience: %d: %s", respExp.StatusCode, string(bodyExp))
		}
		var exp domain.WorkExperience
		_ = json.Unmarshal(bodyExp, &exp)

		// Create fact referencing the experience by short_id "exp-stripe"
		factReq := map[string]any{
			"owner_id":   "exp-stripe",
			"owner_type": "work_experience",
			"statement":  "Architected distributed idempotency engine",
		}
		respFact, bodyFact := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/facts", factReq, nil)
		if respFact.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d: %s", respFact.StatusCode, string(bodyFact))
		}
		var fact domain.Fact
		if err := json.Unmarshal(bodyFact, &fact); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !strings.HasPrefix(fact.ID, "fact-") || len(fact.ID) < 30 {
			t.Errorf("expected long UUID for fact id, got %q", fact.ID)
		}
		if !strings.HasPrefix(fact.ShortID, "fact-") || len(fact.ShortID) > 15 {
			t.Errorf("expected compact short_id for fact, got %q", fact.ShortID)
		}
		// Fact OwnerID should resolve to canonical exp.ID
		if fact.OwnerID != exp.ID {
			t.Errorf("expected fact OwnerID to resolve to canonical exp.ID %q, got %q", exp.ID, fact.OwnerID)
		}
	})

	t.Run("CreateSkill_AutoShortID", func(t *testing.T) {
		req := map[string]any{
			"name":     "Go",
			"category": "Backend",
		}
		resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/skills", req, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, string(body))
		}
		var skill domain.Skill
		if err := json.Unmarshal(body, &skill); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !strings.HasPrefix(skill.ID, "skill-") || len(skill.ID) < 30 {
			t.Errorf("expected long UUID for skill id, got %q", skill.ID)
		}
		if !strings.HasPrefix(skill.ShortID, "skill-") || len(skill.ShortID) > 15 {
			t.Errorf("expected compact short_id for skill, got %q", skill.ShortID)
		}
	})
}

func TestUpdateExperience_ClearingExtendedFields(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	// 1. Create a skill
	skillReq := map[string]any{
		"name":        "TypeScript",
		"category":    "Languages",
		"proficiency": "Expert",
	}
	resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/skills", skillReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create skill: %d: %s", resp.StatusCode, string(body))
	}
	var skill domain.Skill
	_ = json.Unmarshal(body, &skill)

	// 2. Create work experience with full extended details
	hours := 40
	empType := domain.EmploymentTypeFullTime
	createReq := api.CreateExperienceRequest{
		Company:        "Initial Corp",
		RoleTitle:      "Architect",
		EmploymentType: empType,
		StartDate:      domain.NewDate(2022, 1, 1),
		HoursPerWeek:   &hours,
		CompanyContact: &domain.ContactDetail{
			Type:  domain.ContactTypeEmail,
			Value: "hr@initial.corp",
		},
		SupervisorName: "Sarah Connor",
		SkillIDs:       []string{skill.ID},
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/experiences", createReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create experience: %d: %s", resp.StatusCode, string(body))
	}
	var exp domain.WorkExperience
	_ = json.Unmarshal(body, &exp)
	if exp.CompanyContact == nil || exp.HoursPerWeek == nil || len(exp.Skills) == 0 {
		t.Fatalf("expected initial experience to have contact, hours, and skills: %+v", exp)
	}

	// 3. Clear extended fields on PUT: empty contact value, hours = 0, skill_ids = []
	zeroHours := 0
	emptySupervisor := ""
	updateReq := api.UpdateExperienceRequest{
		CompanyContact: &domain.ContactDetail{
			Type:  domain.ContactTypeEmail,
			Value: "",
		},
		HoursPerWeek:   &zeroHours,
		SupervisorName: &emptySupervisor,
		SkillIDs:       []string{},
	}
	resp, body = doRequest(t, client, http.MethodPut, baseURL+"/api/v1/experiences/"+exp.ID, updateReq, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to update experience: %d: %s", resp.StatusCode, string(body))
	}
	var updatedExp domain.WorkExperience
	_ = json.Unmarshal(body, &updatedExp)

	if updatedExp.CompanyContact != nil {
		t.Errorf("expected CompanyContact to be nil after clearing, got %+v", updatedExp.CompanyContact)
	}
	if updatedExp.HoursPerWeek != nil {
		t.Errorf("expected HoursPerWeek to be nil after clearing, got %v", *updatedExp.HoursPerWeek)
	}
	if updatedExp.SupervisorName != "" {
		t.Errorf("expected SupervisorName to be empty string, got %q", updatedExp.SupervisorName)
	}
	if len(updatedExp.Skills) != 0 {
		t.Errorf("expected Skills to be empty after clearing, got %d skills", len(updatedExp.Skills))
	}

	// 4. Verify via GET
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/experiences/"+exp.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to get experience: %d: %s", resp.StatusCode, string(body))
	}
	var fetchedExp domain.WorkExperience
	_ = json.Unmarshal(body, &fetchedExp)
	if fetchedExp.CompanyContact != nil {
		t.Errorf("expected fetched CompanyContact to be nil, got %+v", fetchedExp.CompanyContact)
	}
	if fetchedExp.HoursPerWeek != nil {
		t.Errorf("expected fetched HoursPerWeek to be nil, got %v", *fetchedExp.HoursPerWeek)
	}
	if len(fetchedExp.Skills) != 0 {
		t.Errorf("expected fetched Skills to be empty, got %d", len(fetchedExp.Skills))
	}
}

func TestAPI_Skills_CreationAndConsolidationViaExperienceAndProject(t *testing.T) {
	env := setupTestServer(t, 10)
	defer env.server.Close()

	client := env.server.Client()
	baseURL := env.server.URL

	// 1. Create Experience with skill names
	expReq := api.CreateExperienceRequest{
		Company:   "Stripe",
		RoleTitle: "Software Engineer",
		StartDate: domain.NewDate(2023, 1, 1),
		SkillIDs:  []string{"Golang", "Kubernetes"},
	}
	resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/experiences", expReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create experience: %d: %s", resp.StatusCode, string(body))
	}

	var createdExp domain.WorkExperience
	if err := json.Unmarshal(body, &createdExp); err != nil {
		t.Fatalf("failed to unmarshal experience: %v", err)
	}
	if len(createdExp.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(createdExp.Skills))
	}
	for _, s := range createdExp.Skills {
		if s.Name != "Golang" && s.Name != "Kubernetes" {
			t.Errorf("unexpected skill name: %q", s.Name)
		}
		if s.UserID != "default" && (!s.IsGlobal || s.UserID != "system") {
			t.Errorf("expected skill UserID to be default or global system, got %q", s.UserID)
		}
	}

	// 2. Verify skills appear in GET /api/v1/skills
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/skills", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to list skills: %d: %s", resp.StatusCode, string(body))
	}
	var skills []domain.Skill
	if err := json.Unmarshal(body, &skills); err != nil {
		t.Fatalf("failed to unmarshal skills: %v", err)
	}
	skillNameMap := make(map[string]domain.Skill)
	for _, s := range skills {
		skillNameMap[s.Name] = s
	}
	if _, ok := skillNameMap["Golang"]; !ok {
		t.Errorf("expected 'Golang' in list of skills, got: %+v", skills)
	}
	if _, ok := skillNameMap["Kubernetes"]; !ok {
		t.Errorf("expected 'Kubernetes' in list of skills, got: %+v", skills)
	}

	// 3. Create Project reusing "golang" (case-insensitive) and introducing "AWS"
	projReq := api.CreateProjectRequest{
		Name:     "Cloud Observability",
		SkillIDs: []string{"golang", "AWS"},
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/projects", projReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create project: %d: %s", resp.StatusCode, string(body))
	}
	var createdProj domain.Project
	if err := json.Unmarshal(body, &createdProj); err != nil {
		t.Fatalf("failed to unmarshal project: %v", err)
	}
	if len(createdProj.Skills) != 2 {
		t.Fatalf("expected 2 skills on project, got %d", len(createdProj.Skills))
	}

	// 4. Verify snapshot consolidates all skills
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile/full", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to get full profile: %d: %s", resp.StatusCode, string(body))
	}
	var snapshot domain.MainProfileSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatalf("failed to unmarshal profile snapshot: %v", err)
	}

	snapSkillNames := make(map[string]bool)
	for _, s := range snapshot.Skills {
		snapSkillNames[s.Name] = true
	}
	if !snapSkillNames["Golang"] || !snapSkillNames["Kubernetes"] || !snapSkillNames["AWS"] {
		t.Errorf("expected Golang, Kubernetes, and AWS in snapshot skills, got: %+v", snapshot.Skills)
	}
}

// TestTemplateAndBranchPreview_GoServerRendering verifies server-side rendering
// of templates and branch version outputs using Go's template engine and server values.
func TestTemplateAndBranchPreview_GoServerRendering(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	// 1. Create a custom template set
	tmplReq := api.CreateTemplateRequest{
		Name:             "Go Render Test Template",
		HTMLTemplate:     `<div class="resume"><h1>{{.FullName}}</h1><p class="role">{{.Headline}}</p><p>{{.Summary}}</p></div>`,
		CSSStyles:        `.resume { font-family: sans-serif; }`,
		MarkdownTemplate: `# {{.FullName}} - {{.Headline}}` + "\n\n" + `{{.Summary}}`,
		TextTemplate:     `{{.FullName}} | {{.Headline}}` + "\n" + `{{.Summary}}`,
	}
	resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates", tmplReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create template: %d: %s", resp.StatusCode, string(body))
	}
	tmpl := decodeJSON[domain.TemplateSet](t, body)

	// 2. Test GET /api/v1/templates/{id}/preview (default JSON)
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+tmpl.ID+"/preview", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from GET /templates/{id}/preview, got %d: %s", resp.StatusCode, string(body))
	}
	var previewOut domain.RenderOutput
	if err := json.Unmarshal(body, &previewOut); err != nil {
		t.Fatalf("failed to parse preview JSON: %v, body: %s", err, string(body))
	}
	if !strings.Contains(previewOut.HTML, "Jane Doe") && !strings.Contains(previewOut.HTML, "Alex Morgan") {
		t.Errorf("expected server-rendered FullName in HTML output, got: %s", previewOut.HTML)
	}
	if !strings.Contains(previewOut.Markdown, "# ") {
		t.Errorf("expected markdown header in markdown output, got: %s", previewOut.Markdown)
	}
	if !strings.Contains(previewOut.PlainText, "|") {
		t.Errorf("expected plain text content in plain_text output, got: %s", previewOut.PlainText)
	}

	// 3. Test GET /api/v1/templates/{id}/preview with format query params
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+tmpl.ID+"/preview?format=html", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for ?format=html, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content-type, got %s", ct)
	}
	if !strings.Contains(string(body), "class=\"resume\"") {
		t.Errorf("expected resume html in body, got: %s", string(body))
	}

	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+tmpl.ID+"/preview?format=markdown", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for ?format=markdown, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("expected text/markdown content-type, got %s", ct)
	}
	if !strings.Contains(string(body), "# ") || (!strings.Contains(string(body), "Jane Doe") && !strings.Contains(string(body), "Alex Morgan")) {
		t.Errorf("expected rendered markdown content with full name, got: %s", string(body))
	}

	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+tmpl.ID+"/preview?format=text", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for ?format=text, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content-type, got %s", ct)
	}
	if !strings.Contains(string(body), "|") || (!strings.Contains(string(body), "Jane Doe") && !strings.Contains(string(body), "Alex Morgan")) {
		t.Errorf("expected rendered plain text content with full name, got: %s", string(body))
	}

	// 4. Test GET /api/v1/templates/{id}/preview 404
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/nonexistent-id/preview", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent template preview, got %d", resp.StatusCode)
	}

	// 5. Test POST /api/v1/templates/preview (live draft preview for editor)
	draftReq := map[string]string{
		"html_template":     `<h1>Draft {{.FullName}}</h1><p>{{upper .Headline}}</p>`,
		"css_styles":        `h1 { color: red; }`,
		"markdown_template": `## Draft {{.FullName}}`,
		"text_template":     `Draft {{.FullName}}`,
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates/preview", draftReq, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from POST /templates/preview, got %d: %s", resp.StatusCode, string(body))
	}
	var draftOut domain.RenderOutput
	if err := json.Unmarshal(body, &draftOut); err != nil {
		t.Fatalf("failed to decode draft preview: %v", err)
	}
	if !strings.Contains(draftOut.HTML, "Draft") {
		t.Errorf("expected 'Draft' in HTML output, got: %s", draftOut.HTML)
	}
	// Verify SafeFuncMap was evaluated on server (upper)
	if !strings.Contains(draftOut.HTML, "STAFF SOFTWARE ENGINEER") {
		t.Errorf("expected upper func applied by Go template engine, got: %s", draftOut.HTML)
	}

	// 6. Test POST /api/v1/templates/preview with syntax error -> 422
	badDraftReq := map[string]string{
		"html_template": `<h1>{{.Unclosed`,
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates/preview", badDraftReq, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for syntax error in POST /templates/preview, got %d: %s", resp.StatusCode, string(body))
	}
	var errResp api.ErrorResponse
	_ = json.Unmarshal(body, &errResp)
	if errResp.Error.Code != api.CodeTemplateValidationError {
		t.Errorf("expected TEMPLATE_VALIDATION_ERROR code, got %s", errResp.Error.Code)
	}

	// 7. Test GET /api/v1/branches/{id}/versions/{v}/preview
	// Create a branch and render default branch version
	branchReq := api.CreateBranchRequest{
		Name:          "Preview Test Branch",
		TemplateSetID: tmpl.ID,
	}
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches", branchReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create branch: %d: %s", resp.StatusCode, string(body))
	}
	createdBranch := decodeJSON[domain.Branch](t, body)

	// Fetch versions
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+createdBranch.ID+"/versions", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to list branch versions: %d", resp.StatusCode)
	}
	var versions []domain.BranchVersion
	_ = json.Unmarshal(body, &versions)
	if len(versions) == 0 {
		// Generate default version
		genResp, gBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+createdBranch.ID+"/generate", nil, nil)
		if genResp.StatusCode != http.StatusOK && genResp.StatusCode != http.StatusAccepted {
			t.Fatalf("failed to generate version: %d: %s", genResp.StatusCode, string(gBody))
		}
		resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+createdBranch.ID+"/versions", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("failed to list branch versions after generate: %d", resp.StatusCode)
		}
		_ = json.Unmarshal(body, &versions)
	}
	if len(versions) == 0 {
		t.Fatalf("expected branch versions to be populated")
	}

	vNum := fmt.Sprintf("%d", versions[0].VersionNumber)

	// JSON format (default)
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+createdBranch.ID+"/versions/"+vNum+"/preview", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for branch version preview JSON, got %d: %s", resp.StatusCode, string(body))
	}
	var branchOut domain.RenderOutput
	if err := json.Unmarshal(body, &branchOut); err != nil {
		t.Fatalf("failed to decode branch preview JSON: %v", err)
	}
	if branchOut.HTML == "" {
		t.Errorf("expected non-empty HTML in branch preview JSON")
	}

	// HTML format
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+createdBranch.ID+"/versions/"+vNum+"/preview?format=html", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for branch version preview ?format=html, got %d: %s", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html for branch version preview ?format=html, got %s", ct)
	}
	if len(body) == 0 {
		t.Errorf("expected non-empty html in branch preview")
	}

	// Markdown format
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+createdBranch.ID+"/versions/"+vNum+"/preview?format=markdown", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for branch version preview ?format=markdown, got %d: %s", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("expected text/markdown for branch version preview ?format=markdown, got %s", ct)
	}
	if len(body) == 0 {
		t.Errorf("expected non-empty markdown in branch preview")
	}

	// Text format
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+createdBranch.ID+"/versions/"+vNum+"/preview?format=text", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for branch version preview ?format=text, got %d: %s", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain for branch version preview ?format=text, got %s", ct)
	}
	if len(body) == 0 {
		t.Errorf("expected non-empty text in branch preview")
	}
}

func TestAPI_MultiTenancyAndErrorSentinels(t *testing.T) {
	ctx := context.Background()
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL
	memStore := env.db.(*memory.Store)

	t.Run("built-in template update rejected with 403 Forbidden", func(t *testing.T) {
		updateBody := map[string]any{
			"name": "Hacked Builtin Template",
		}
		resp, body := doRequest(t, client, http.MethodPut, baseURL+"/api/v1/templates/builtin-standard-clean", updateBody, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for built-in template update, got %d: %s", resp.StatusCode, string(body))
		}
		var errResp api.ErrorResponse
		if err := json.Unmarshal(body, &errResp); err != nil {
			t.Fatalf("failed to unmarshal error response: %v", err)
		}
		if errResp.Error.Code != api.CodeForbidden {
			t.Errorf("expected error code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	t.Run("built-in template delete rejected with 403 Forbidden", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodDelete, baseURL+"/api/v1/templates/builtin-standard-clean", nil, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for built-in template delete, got %d: %s", resp.StatusCode, string(body))
		}
		var errResp api.ErrorResponse
		if err := json.Unmarshal(body, &errResp); err != nil {
			t.Fatalf("failed to unmarshal error response: %v", err)
		}
		if errResp.Error.Code != api.CodeForbidden {
			t.Errorf("expected error code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	t.Run("global skill delete rejected with 403 Forbidden", func(t *testing.T) {
		globalSkill := &domain.Skill{
			ID:       "global-test-lang",
			UserID:   "system",
			Name:     "Rust",
			Slug:     "rust",
			IsGlobal: true,
		}
		if err := memStore.CreateSkill(ctx, globalSkill); err != nil {
			t.Fatalf("failed to create global skill: %v", err)
		}

		resp, body := doRequest(t, client, http.MethodDelete, baseURL+"/api/v1/skills/global-test-lang", nil, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for global skill deletion, got %d: %s", resp.StatusCode, string(body))
		}
		var errResp api.ErrorResponse
		if err := json.Unmarshal(body, &errResp); err != nil {
			t.Fatalf("failed to unmarshal error response: %v", err)
		}
		if errResp.Error.Code != api.CodeForbidden {
			t.Errorf("expected error code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	t.Run("quota exceeded returns 429 Too Many Requests with QUOTA_EXCEEDED code", func(t *testing.T) {
		pdfEnq := api.NewChannelPDFJobEnqueuer(10)
		pdfEnv := setupTestServerWithExtractorLoggerAndPDFEnqueuer(t, nil, nil, pdfEnq)
		pdfClient := pdfEnv.server.Client()
		pdfMem := pdfEnv.db.(*memory.Store)

		// Exhaust user daily quota (50 requests limit)
		for i := 0; i < 50; i++ {
			_, err := pdfMem.ReserveQuota(ctx, "default", domain.OperationResumeTailoring, 100, 50, 1000000)
			if err != nil {
				t.Fatalf("failed to seed reservations: %v", err)
			}
		}

		// Attempt to upload PDF parse job
		pdfBytes := buildTestPDF("Sample Resume Content")
		bodyBuf := &bytes.Buffer{}
		writer := multipart.NewWriter(bodyBuf)
		part, err := writer.CreateFormFile("file", "resume.pdf")
		if err != nil {
			t.Fatalf("failed to create form file: %v", err)
		}
		if _, err := part.Write(pdfBytes); err != nil {
			t.Fatalf("failed to write pdf bytes: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("failed to close writer: %v", err)
		}

		req, err := http.NewRequest(http.MethodPost, pdfEnv.server.URL+"/api/v1/profile/pdf/parse", bodyBuf)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, err := pdfClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusTooManyRequests {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 429 Too Many Requests, got %d: %s", resp.StatusCode, string(respBody))
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeQuotaExceeded {
			t.Errorf("expected error code QUOTA_EXCEEDED, got %q", errResp.Error.Code)
		}
	})

	t.Run("unauthenticated request returns 401 Unauthorized in production mode", func(t *testing.T) {
		prodRouter := api.NewRouter(api.RouterConfig{
			ProfileRepo: memStore,
			Admin:       false, // production mode with verifier == nil
		})
		prodServer := httptest.NewServer(prodRouter)
		defer prodServer.Close()

		req, _ := http.NewRequest(http.MethodGet, prodServer.URL+"/api/v1/profile", nil)
		req.Header.Set("X-User-ID", "attacker")
		resp, err := prodServer.Client().Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}
		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeUnauthorized {
			t.Errorf("expected error code UNAUTHORIZED, got %q", errResp.Error.Code)
		}
	})
}

func TestAPI_ListSkills_TypeFilter(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:   memStore,
		Admin:         true,
		DefaultUserID: "default",
	})
	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()
	baseURL := server.URL

	// Seed tenant custom skills for "default"
	customSkill1 := &domain.Skill{
		ID:       "custom-skill-go",
		UserID:   "default",
		Name:     "Go",
		IsGlobal: false,
	}
	customSkill2 := &domain.Skill{
		ID:       "custom-skill-k8s",
		UserID:   "default",
		Name:     "Kubernetes",
		IsGlobal: false,
	}
	if err := memStore.CreateSkill(ctx, customSkill1); err != nil {
		t.Fatalf("failed to create custom skill 1: %v", err)
	}
	if err := memStore.CreateSkill(ctx, customSkill2); err != nil {
		t.Fatalf("failed to create custom skill 2: %v", err)
	}

	// Seed global skill (system-owned)
	globalSkill := &domain.Skill{
		ID:       "global-skill-rust",
		UserID:   "system",
		Name:     "Rust",
		IsGlobal: true,
	}
	if err := memStore.CreateSkill(ctx, globalSkill); err != nil {
		t.Fatalf("failed to create global skill: %v", err)
	}

	// Seed custom skill for another tenant to verify isolation
	otherTenantSkill := &domain.Skill{
		ID:       "other-tenant-skill-python",
		UserID:   "tenant-other",
		Name:     "Python",
		IsGlobal: false,
	}
	if err := memStore.CreateSkill(ctx, otherTenantSkill); err != nil {
		t.Fatalf("failed to create other tenant skill: %v", err)
	}

	assertSkillNamesAndIDs := func(t *testing.T, skills []domain.Skill, expectedMap map[string]string) {
		t.Helper()
		if len(skills) != len(expectedMap) {
			t.Fatalf("expected %d skills, got %d: %+v", len(expectedMap), len(skills), skills)
		}
		found := make(map[string]string)
		for _, s := range skills {
			found[s.ID] = s.Name
			if s.ID == "other-tenant-skill-python" || s.Name == "Python" {
				t.Errorf("cross-tenant leakage: other tenant's skill found in response: %+v", s)
			}
		}
		for expID, expName := range expectedMap {
			actualName, ok := found[expID]
			if !ok {
				t.Errorf("missing expected skill ID %q in response: %+v", expID, found)
			} else if actualName != expName {
				t.Errorf("expected skill %q to have name %q, got %q", expID, expName, actualName)
			}
		}
	}

	tests := []struct {
		name           string
		query          string
		expectedStatus int
		verify         func(t *testing.T, resp *http.Response, body []byte)
	}{
		{
			name:           "type omitted defaults to all",
			query:          "",
			expectedStatus: http.StatusOK,
			verify: func(t *testing.T, resp *http.Response, body []byte) {
				skills := decodeJSON[[]domain.Skill](t, body)
				assertSkillNamesAndIDs(t, skills, map[string]string{
					"custom-skill-go":   "Go",
					"custom-skill-k8s":  "Kubernetes",
					"global-skill-rust": "Rust",
				})
			},
		},
		{
			name:           "type=all returns union of custom and global skills",
			query:          "?type=all",
			expectedStatus: http.StatusOK,
			verify: func(t *testing.T, resp *http.Response, body []byte) {
				skills := decodeJSON[[]domain.Skill](t, body)
				assertSkillNamesAndIDs(t, skills, map[string]string{
					"custom-skill-go":   "Go",
					"custom-skill-k8s":  "Kubernetes",
					"global-skill-rust": "Rust",
				})
			},
		},
		{
			name:           "type=ALL case-insensitive handling",
			query:          "?type=ALL",
			expectedStatus: http.StatusOK,
			verify: func(t *testing.T, resp *http.Response, body []byte) {
				skills := decodeJSON[[]domain.Skill](t, body)
				assertSkillNamesAndIDs(t, skills, map[string]string{
					"custom-skill-go":   "Go",
					"custom-skill-k8s":  "Kubernetes",
					"global-skill-rust": "Rust",
				})
			},
		},
		{
			name:           "type=global returns only system-seeded global skills",
			query:          "?type=global",
			expectedStatus: http.StatusOK,
			verify: func(t *testing.T, resp *http.Response, body []byte) {
				skills := decodeJSON[[]domain.Skill](t, body)
				assertSkillNamesAndIDs(t, skills, map[string]string{
					"global-skill-rust": "Rust",
				})
				for _, s := range skills {
					if !s.IsGlobal {
						t.Errorf("expected is_global == true, got false for skill: %+v", s)
					}
					if s.UserID != "system" {
						t.Errorf("expected user_id == system, got %s", s.UserID)
					}
				}
			},
		},
		{
			name:           "type=custom returns only tenant-owned custom skills",
			query:          "?type=custom",
			expectedStatus: http.StatusOK,
			verify: func(t *testing.T, resp *http.Response, body []byte) {
				skills := decodeJSON[[]domain.Skill](t, body)
				assertSkillNamesAndIDs(t, skills, map[string]string{
					"custom-skill-go":  "Go",
					"custom-skill-k8s": "Kubernetes",
				})
				for _, s := range skills {
					if s.IsGlobal {
						t.Errorf("expected is_global == false for custom skill, got true for %s", s.Name)
					}
					if s.UserID != "default" {
						t.Errorf("expected user_id == default, got %s", s.UserID)
					}
				}
			},
		},
		{
			name:           "type=invalid returns 400 Bad Request",
			query:          "?type=invalid",
			expectedStatus: http.StatusBadRequest,
			verify: func(t *testing.T, resp *http.Response, body []byte) {
				var errResp api.ErrorResponse
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Fatalf("failed to decode error response: %v", err)
				}
				if errResp.Error.Code != api.CodeInvalidRequest {
					t.Errorf("expected error code %q, got %q", api.CodeInvalidRequest, errResp.Error.Code)
				}
				if !strings.Contains(errResp.Error.Message, "invalid skill type filter") {
					t.Errorf("expected message containing 'invalid skill type filter', got %q", errResp.Error.Message)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/skills"+tc.query, nil, nil)
			if resp.StatusCode != tc.expectedStatus {
				t.Fatalf("expected status %d, got %d: %s", tc.expectedStatus, resp.StatusCode, string(body))
			}
			tc.verify(t, resp, body)
		})
	}
}

type testMockTokenVerifier struct {
	tokens map[string]*domain.TokenInfo
}

func (m *testMockTokenVerifier) VerifyIDToken(_ context.Context, token string) (*domain.TokenInfo, error) {
	if info, ok := m.tokens[token]; ok {
		return info, nil
	}
	return nil, domain.ErrUnauthorized
}

func TestMultiTenant_FirstLoginLazyProvisioning(t *testing.T) {
	ctx := context.Background()
	store, err := memory.NewSeeded(ctx, "default")
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	verifier := &testMockTokenVerifier{
		tokens: map[string]*domain.TokenInfo{
			"token-firebase-new-123": {
				UID:   "firebase-new-user-123",
				Email: "newuser@example.com",
				Role:  "user",
			},
		},
	}

	profileSvc := profile.NewService(store, store)
	enqueuer := api.NewChannelJobEnqueuer(10)
	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:    store,
		PresetRepo:     store,
		TemplateRepo:   store,
		BranchRepo:     store,
		QuotaRepo:      store,
		UserRepo:       store,
		Store:          store,
		ProfileService: profileSvc,
		Renderer:       renderer.NewTemplateRenderer(),
		Validator:      validator.New(),
		JobEnqueuer:    enqueuer,
		Admin:          false,
		TokenVerifier:  verifier,
	})

	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()

	headers := map[string]string{
		"Authorization": "Bearer token-firebase-new-123",
	}

	// 1. GET /api/v1/profile for the new user should return 200 OK (lazy provisioned)
	resp, body := doRequest(t, client, http.MethodGet, server.URL+"/api/v1/profile", nil, headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for GET /profile on first login, got %d: %s", resp.StatusCode, string(body))
	}
	var prof domain.Profile
	if err := json.Unmarshal(body, &prof); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if prof.UserID != "firebase-new-user-123" {
		t.Errorf("expected UserID 'firebase-new-user-123', got %q", prof.UserID)
	}

	// 2. GET /api/v1/profile/full should return 200 OK
	snapResp, snapBody := doRequest(t, client, http.MethodGet, server.URL+"/api/v1/profile/full", nil, headers)
	if snapResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for GET /profile/full, got %d: %s", snapResp.StatusCode, string(snapBody))
	}

	// 3. GET /api/v1/branches should return 200 OK with default branch
	branchResp, branchBody := doRequest(t, client, http.MethodGet, server.URL+"/api/v1/branches", nil, headers)
	if branchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for GET /branches, got %d: %s", branchResp.StatusCode, string(branchBody))
	}
	var branches []domain.Branch
	if err := json.Unmarshal(branchBody, &branches); err != nil {
		t.Fatalf("decode branches: %v", err)
	}
	if len(branches) != 1 || !branches[0].IsDefault {
		t.Fatalf("expected 1 default branch, got %+v", branches)
	}

	// 4. Verify user record in UserRepo
	user, err := store.User().GetUser(ctx, "firebase-new-user-123")
	if err != nil {
		t.Fatalf("failed to retrieve provisioned user: %v", err)
	}
	if user.Email != "newuser@example.com" {
		t.Errorf("expected email 'newuser@example.com', got %q", user.Email)
	}
}


