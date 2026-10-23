package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"resumeee/internal/api"
	"resumeee/internal/domain"
	"resumeee/internal/profile"
	"resumeee/internal/renderer"
	"resumeee/internal/repository/sqlite"
	"resumeee/internal/validator"
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
}

func setupTestServer(t *testing.T, queueCap int) *testEnv {
	t.Helper()

	db, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize sqlite: %v", err)
	}

	ctx := context.Background()
	defaultUserID := "default"
	if err := sqlite.SeedDefaultData(ctx, db, defaultUserID); err != nil {
		t.Fatalf("failed to seed default data: %v", err)
	}

	profileRepo := sqlite.NewProfileRepository(db)
	presetRepo := sqlite.NewPresetRepository(db)
	templateRepo := sqlite.NewTemplateRepository(db)
	branchRepo := sqlite.NewBranchRepository(db)
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
		ProfileService: profileSvc,
		Renderer:       templateRenderer,
		Validator:      provValidator,
		JobEnqueuer:    enqueuer,
		DefaultUserID:  defaultUserID,
	})

	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
	})

	return &testEnv{
		server:       server,
		db:           db,
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

	// 1. GET /api/v1/profile -> Verify default seeded user initialized with master_revision: 1
	resp, body := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /profile failed: %d, body: %s", resp.StatusCode, string(body))
	}
	prof := decodeJSON[domain.Profile](t, body)
	if prof.MasterRevision != 1 {
		t.Fatalf("expected initial master_revision: 1, got: %d", prof.MasterRevision)
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
		StartDate:      "2021-01-01",
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
		StartDate: "2022-01-01",
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
		StartDate:   "2016-09-01",
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
		Date:   "2023-01-01",
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

	// 20. GET /api/v1/profile/full -> Verify master profile snapshot contains career graph
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile/full", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /profile/full failed: %d", resp.StatusCode)
	}
	snapshot := decodeJSON[domain.MasterProfileSnapshot](t, body)
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

	// 33. GET /api/v1/profile/jsonresume/export (Master Profile export)
	resp, _ = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile/jsonresume/export", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export master jsonresume failed: %d", resp.StatusCode)
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
	freshSnap := decodeJSON[domain.MasterProfileSnapshot](t, body)

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
			MasterRevisionUsed: 1,
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
						StartDate:      "2020-01-01",
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
