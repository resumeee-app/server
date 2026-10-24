package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"resumeee/internal/domain"
)

type profileHandler struct {
	cfg RouterConfig
}

func parseJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty request body")
	}
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(dst)
}

func registerProfileRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &profileHandler{cfg: cfg}

	// Master Profile
	mux.HandleFunc("GET /api/v1/profile", h.getProfile)
	mux.HandleFunc("PUT /api/v1/profile", h.updateProfile)
	mux.HandleFunc("GET /api/v1/profile/full", h.getFullProfile)
	mux.HandleFunc("POST /api/v1/profile/jsonresume/import", h.importJSONResume)
	mux.HandleFunc("GET /api/v1/profile/jsonresume/export", h.exportJSONResume)

	// Skills
	mux.HandleFunc("GET /api/v1/skills", h.listSkills)
	mux.HandleFunc("POST /api/v1/skills", h.createSkill)
	mux.HandleFunc("GET /api/v1/skills/{id}", h.getSkill)
	mux.HandleFunc("PUT /api/v1/skills/{id}", h.updateSkill)
	mux.HandleFunc("DELETE /api/v1/skills/{id}", h.deleteSkill)

	// Work Experiences
	mux.HandleFunc("GET /api/v1/experiences", h.listExperiences)
	mux.HandleFunc("POST /api/v1/experiences", h.createExperience)
	mux.HandleFunc("GET /api/v1/experiences/{id}", h.getExperience)
	mux.HandleFunc("PUT /api/v1/experiences/{id}", h.updateExperience)
	mux.HandleFunc("DELETE /api/v1/experiences/{id}", h.deleteExperience)

	// Projects
	mux.HandleFunc("GET /api/v1/projects", h.listProjects)
	mux.HandleFunc("POST /api/v1/projects", h.createProject)
	mux.HandleFunc("GET /api/v1/projects/{id}", h.getProject)
	mux.HandleFunc("PUT /api/v1/projects/{id}", h.updateProject)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", h.deleteProject)

	// Education
	mux.HandleFunc("GET /api/v1/education", h.listEducation)
	mux.HandleFunc("POST /api/v1/education", h.createEducation)
	mux.HandleFunc("GET /api/v1/education/{id}", h.getEducation)
	mux.HandleFunc("PUT /api/v1/education/{id}", h.updateEducation)
	mux.HandleFunc("DELETE /api/v1/education/{id}", h.deleteEducation)

	// Certifications
	mux.HandleFunc("GET /api/v1/certifications", h.listCertifications)
	mux.HandleFunc("POST /api/v1/certifications", h.createCertification)
	mux.HandleFunc("GET /api/v1/certifications/{id}", h.getCertification)
	mux.HandleFunc("PUT /api/v1/certifications/{id}", h.updateCertification)
	mux.HandleFunc("DELETE /api/v1/certifications/{id}", h.deleteCertification)

	// Facts
	mux.HandleFunc("GET /api/v1/facts", h.listFacts)
	mux.HandleFunc("POST /api/v1/facts", h.createFact)
	mux.HandleFunc("GET /api/v1/facts/{id}", h.getFact)
	mux.HandleFunc("PUT /api/v1/facts/{id}", h.updateFact)
	mux.HandleFunc("DELETE /api/v1/facts/{id}", h.deleteFact)
}

// --- Request DTOs ---

type UpdateProfileRequest struct {
	FullName           *string             `json:"full_name"`
	Headline           *string             `json:"headline"`
	Email              *string             `json:"email"`
	Phone              *string             `json:"phone"`
	Location           *domain.Location    `json:"location"`
	Profiles           []domain.SocialLink `json:"profiles"`
	Summary            *string             `json:"summary"`
	GlobalWritingRules *string             `json:"global_writing_rules"`
	DefaultProvider    *string             `json:"default_provider"`
	DefaultModel       *string             `json:"default_model"`
	Metadata           map[string]any      `json:"metadata"`
}

type CreateSkillRequest struct {
	Name            string         `json:"name"`
	Category        string         `json:"category"`
	Proficiency     string         `json:"proficiency"`
	YearsExperience float64        `json:"years_experience"`
	Keywords        []string       `json:"keywords"`
	Metadata        map[string]any `json:"metadata"`
}

type UpdateSkillRequest struct {
	Name            *string        `json:"name"`
	Category        *string        `json:"category"`
	Proficiency     *string        `json:"proficiency"`
	YearsExperience *float64       `json:"years_experience"`
	Keywords        []string       `json:"keywords"`
	Metadata        map[string]any `json:"metadata"`
}

type CreateExperienceRequest struct {
	Company           string                `json:"company"`
	RoleTitle         string                `json:"role_title"`
	EmploymentType    domain.EmploymentType `json:"employment_type"`
	StartDate         string                `json:"start_date"`
	EndDate           *string               `json:"end_date"`
	IsCurrent         bool                  `json:"is_current"`
	Location          domain.Location       `json:"location"`
	CompanyLocation   *domain.Location      `json:"company_location"`
	CompanyContact    *domain.ContactDetail `json:"company_contact"`
	HoursPerWeek      *int                  `json:"hours_per_week"`
	SupervisorName    string                `json:"supervisor_name"`
	SupervisorContact string                `json:"supervisor_contact"`
	DutiesSummary     string                `json:"duties_summary"`
	SkillIDs          []string              `json:"skill_ids"`
	Metadata          map[string]any        `json:"metadata"`
}

type UpdateExperienceRequest struct {
	Company           *string                `json:"company"`
	RoleTitle         *string                `json:"role_title"`
	EmploymentType    *domain.EmploymentType `json:"employment_type"`
	StartDate         *string                `json:"start_date"`
	EndDate           *string                `json:"end_date"`
	IsCurrent         *bool                  `json:"is_current"`
	Location          *domain.Location       `json:"location"`
	CompanyLocation   *domain.Location       `json:"company_location"`
	CompanyContact    *domain.ContactDetail  `json:"company_contact"`
	HoursPerWeek      *int                   `json:"hours_per_week"`
	SupervisorName    *string                `json:"supervisor_name"`
	SupervisorContact *string                `json:"supervisor_contact"`
	DutiesSummary     *string                `json:"duties_summary"`
	SkillIDs          []string               `json:"skill_ids"`
	Metadata          map[string]any         `json:"metadata"`
}

type CreateProjectRequest struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	URL       string         `json:"url"`
	StartDate string         `json:"start_date"`
	EndDate   *string        `json:"end_date"`
	Summary   string         `json:"summary"`
	SkillIDs  []string       `json:"skill_ids"`
	Metadata  map[string]any `json:"metadata"`
}

type UpdateProjectRequest struct {
	Name      *string        `json:"name"`
	Role      *string        `json:"role"`
	URL       *string        `json:"url"`
	StartDate *string        `json:"start_date"`
	EndDate   *string        `json:"end_date"`
	Summary   *string        `json:"summary"`
	SkillIDs  []string       `json:"skill_ids"`
	Metadata  map[string]any `json:"metadata"`
}

type CreateEducationRequest struct {
	Institution string          `json:"institution"`
	Degree      string          `json:"degree"`
	Field       string          `json:"field"`
	StartDate   string          `json:"start_date"`
	EndDate     *string         `json:"end_date"`
	Score       string          `json:"score"`
	Location    domain.Location `json:"location"`
	Metadata    map[string]any  `json:"metadata"`
}

type UpdateEducationRequest struct {
	Institution *string          `json:"institution"`
	Degree      *string          `json:"degree"`
	Field       *string          `json:"field"`
	StartDate   *string          `json:"start_date"`
	EndDate     *string          `json:"end_date"`
	Score       *string          `json:"score"`
	Location    *domain.Location `json:"location"`
	Metadata    map[string]any   `json:"metadata"`
}

type CreateCertificationRequest struct {
	Name     string         `json:"name"`
	Issuer   string         `json:"issuer"`
	Date     string         `json:"date"`
	URL      string         `json:"url"`
	Summary  string         `json:"summary"`
	Metadata map[string]any `json:"metadata"`
}

type UpdateCertificationRequest struct {
	Name     *string        `json:"name"`
	Issuer   *string        `json:"issuer"`
	Date     *string        `json:"date"`
	URL      *string        `json:"url"`
	Summary  *string        `json:"summary"`
	Metadata map[string]any `json:"metadata"`
}

type CreateFactRequest struct {
	OwnerID   string           `json:"owner_id"`
	OwnerType domain.OwnerType `json:"owner_type"`
	Statement string           `json:"statement"`
	Impact    string           `json:"impact"`
	SkillIDs  []string         `json:"skill_ids"`
}

type UpdateFactRequest struct {
	Statement *string  `json:"statement"`
	Impact    *string  `json:"impact"`
	SkillIDs  []string `json:"skill_ids"`
}

// --- Master Profile Handlers ---

func (h *profileHandler) getProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, prof)
}

func (h *profileHandler) updateProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req UpdateProfileRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid json payload", err.Error())
		return
	}

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			prof = &domain.Profile{
				ID:             "profile-" + userID,
				UserID:         userID,
				MasterRevision: 0,
			}
		} else {
			writeDomainError(w, err)
			return
		}
	}

	if req.FullName != nil {
		prof.FullName = *req.FullName
	}
	if req.Headline != nil {
		prof.Headline = *req.Headline
	}
	if req.Email != nil {
		prof.Email = *req.Email
	}
	if req.Phone != nil {
		prof.Phone = *req.Phone
	}
	if req.Location != nil {
		prof.Location = *req.Location
	}
	if req.Profiles != nil {
		prof.Profiles = req.Profiles
	}
	if req.Summary != nil {
		prof.Summary = *req.Summary
	}
	if req.GlobalWritingRules != nil {
		prof.GlobalWritingRules = *req.GlobalWritingRules
	}
	if req.DefaultProvider != nil {
		prof.DefaultProvider = *req.DefaultProvider
	}
	if req.DefaultModel != nil {
		prof.DefaultModel = *req.DefaultModel
	}
	if req.Metadata != nil {
		prof.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpsertProfile(ctx, prof); err != nil {
		writeDomainError(w, err)
		return
	}

	updated, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

func (h *profileHandler) getFullProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	snap, err := h.cfg.ProfileRepo.GetSnapshot(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

func (h *profileHandler) importJSONResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	if r.Body == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty JSON Resume payload", "")
		return
	}
	defer func() { _ = r.Body.Close() }()

	payload, err := io.ReadAll(r.Body)
	if err != nil || len(payload) == 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty or unreadable JSON Resume payload", "")
		return
	}

	var testMap map[string]any
	if err := json.Unmarshal(payload, &testMap); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed JSON payload", err.Error())
		return
	}

	snap, err := h.cfg.ProfileService.ImportJSONResume(ctx, userID, payload)
	if err != nil {
		if errors.Is(err, domain.ErrValidation) {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid JSON Resume payload", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

func (h *profileHandler) exportJSONResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var versionIDPtr *string
	if v := strings.TrimSpace(r.URL.Query().Get("version_id")); v != "" {
		versionIDPtr = &v
	}

	resume, err := h.cfg.ProfileService.ExportJSONResume(ctx, userID, versionIDPtr)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resume)
}

// --- Skills Handlers ---

func (h *profileHandler) listSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	skills, err := h.cfg.ProfileRepo.ListSkills(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if skills == nil {
		skills = []domain.Skill{}
	}
	writeJSON(w, http.StatusOK, skills)
}

func (h *profileHandler) createSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateSkillRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	skill := domain.Skill{
		ID:              "skill-" + uuid.New().String(),
		UserID:          userID,
		Name:            req.Name,
		Category:        req.Category,
		Proficiency:     req.Proficiency,
		YearsExperience: req.YearsExperience,
		Keywords:        req.Keywords,
		Metadata:        req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateSkill(ctx, &skill); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, skill)
}

func (h *profileHandler) getSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	skill, err := h.cfg.ProfileRepo.GetSkill(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, skill)
}

func (h *profileHandler) updateSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	skill, err := h.cfg.ProfileRepo.GetSkill(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateSkillRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name != nil {
		skill.Name = *req.Name
	}
	if req.Category != nil {
		skill.Category = *req.Category
	}
	if req.Proficiency != nil {
		skill.Proficiency = *req.Proficiency
	}
	if req.YearsExperience != nil {
		skill.YearsExperience = *req.YearsExperience
	}
	if req.Keywords != nil {
		skill.Keywords = req.Keywords
	}
	if req.Metadata != nil {
		skill.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpdateSkill(ctx, skill); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, skill)
}

func (h *profileHandler) deleteSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteSkill(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Work Experiences Handlers ---

func (h *profileHandler) listExperiences(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	exps, err := h.cfg.ProfileRepo.ListExperiences(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if exps == nil {
		exps = []domain.WorkExperience{}
	}
	writeJSON(w, http.StatusOK, exps)
}

func (h *profileHandler) createExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateExperienceRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Company == "" || req.RoleTitle == "" || req.StartDate == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "company, role_title, and start_date are required", "")
		return
	}

	var skills []domain.Skill
	if len(req.SkillIDs) > 0 {
		skills = make([]domain.Skill, len(req.SkillIDs))
		for i, sID := range req.SkillIDs {
			skills[i] = domain.Skill{ID: sID}
		}
	}

	exp := domain.WorkExperience{
		ID:                "exp-" + uuid.New().String(),
		UserID:            userID,
		Company:           req.Company,
		RoleTitle:         req.RoleTitle,
		EmploymentType:    req.EmploymentType,
		StartDate:         req.StartDate,
		EndDate:           req.EndDate,
		IsCurrent:         req.IsCurrent,
		Location:          req.Location,
		HoursPerWeek:      req.HoursPerWeek,
		SupervisorName:    req.SupervisorName,
		SupervisorContact: req.SupervisorContact,
		DutiesSummary:     req.DutiesSummary,
		Skills:            skills,
		Metadata:          req.Metadata,
	}
	if req.CompanyLocation != nil {
		exp.CompanyLocation = *req.CompanyLocation
	}
	if req.CompanyContact != nil {
		exp.CompanyContact = req.CompanyContact
	}

	if err := h.cfg.ProfileRepo.CreateExperience(ctx, &exp); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, exp)
}

func (h *profileHandler) getExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	exp, err := h.cfg.ProfileRepo.GetExperience(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, exp)
}

func (h *profileHandler) updateExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	exp, err := h.cfg.ProfileRepo.GetExperience(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateExperienceRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Company != nil {
		exp.Company = *req.Company
	}
	if req.RoleTitle != nil {
		exp.RoleTitle = *req.RoleTitle
	}
	if req.EmploymentType != nil {
		exp.EmploymentType = *req.EmploymentType
	}
	if req.StartDate != nil {
		exp.StartDate = *req.StartDate
	}
	if req.EndDate != nil {
		exp.EndDate = req.EndDate
	}
	if req.IsCurrent != nil {
		exp.IsCurrent = *req.IsCurrent
	}
	if req.Location != nil {
		exp.Location = *req.Location
	}
	if req.CompanyLocation != nil {
		exp.CompanyLocation = *req.CompanyLocation
	}
	if req.CompanyContact != nil {
		exp.CompanyContact = req.CompanyContact
	}
	if req.HoursPerWeek != nil {
		exp.HoursPerWeek = req.HoursPerWeek
	}
	if req.SupervisorName != nil {
		exp.SupervisorName = *req.SupervisorName
	}
	if req.SupervisorContact != nil {
		exp.SupervisorContact = *req.SupervisorContact
	}
	if req.DutiesSummary != nil {
		exp.DutiesSummary = *req.DutiesSummary
	}
	if req.Metadata != nil {
		exp.Metadata = req.Metadata
	}
	if req.SkillIDs != nil {
		skills := make([]domain.Skill, len(req.SkillIDs))
		for i, sID := range req.SkillIDs {
			skills[i] = domain.Skill{ID: sID}
		}
		exp.Skills = skills
	}

	if err := h.cfg.ProfileRepo.UpdateExperience(ctx, exp); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, exp)
}

func (h *profileHandler) deleteExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteExperience(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Projects Handlers ---

func (h *profileHandler) listProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	projs, err := h.cfg.ProfileRepo.ListProjects(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if projs == nil {
		projs = []domain.Project{}
	}
	writeJSON(w, http.StatusOK, projs)
}

func (h *profileHandler) createProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateProjectRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	var skills []domain.Skill
	if len(req.SkillIDs) > 0 {
		skills = make([]domain.Skill, len(req.SkillIDs))
		for i, sID := range req.SkillIDs {
			skills[i] = domain.Skill{ID: sID}
		}
	}

	proj := domain.Project{
		ID:        "proj-" + uuid.New().String(),
		UserID:    userID,
		Name:      req.Name,
		Role:      req.Role,
		URL:       req.URL,
		StartDate: req.StartDate,
		EndDate:   req.EndDate,
		Summary:   req.Summary,
		Skills:    skills,
		Metadata:  req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateProject(ctx, &proj); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, proj)
}

func (h *profileHandler) getProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	proj, err := h.cfg.ProfileRepo.GetProject(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, proj)
}

func (h *profileHandler) updateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	proj, err := h.cfg.ProfileRepo.GetProject(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateProjectRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name != nil {
		proj.Name = *req.Name
	}
	if req.Role != nil {
		proj.Role = *req.Role
	}
	if req.URL != nil {
		proj.URL = *req.URL
	}
	if req.StartDate != nil {
		proj.StartDate = *req.StartDate
	}
	if req.EndDate != nil {
		proj.EndDate = req.EndDate
	}
	if req.Summary != nil {
		proj.Summary = *req.Summary
	}
	if req.Metadata != nil {
		proj.Metadata = req.Metadata
	}
	if req.SkillIDs != nil {
		skills := make([]domain.Skill, len(req.SkillIDs))
		for i, sID := range req.SkillIDs {
			skills[i] = domain.Skill{ID: sID}
		}
		proj.Skills = skills
	}

	if err := h.cfg.ProfileRepo.UpdateProject(ctx, proj); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, proj)
}

func (h *profileHandler) deleteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteProject(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Education Handlers ---

func (h *profileHandler) listEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	edus, err := h.cfg.ProfileRepo.ListEducation(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if edus == nil {
		edus = []domain.Education{}
	}
	writeJSON(w, http.StatusOK, edus)
}

func (h *profileHandler) createEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateEducationRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Institution == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "institution is required", "")
		return
	}

	edu := domain.Education{
		ID:          "edu-" + uuid.New().String(),
		UserID:      userID,
		Institution: req.Institution,
		Degree:      req.Degree,
		Field:       req.Field,
		StartDate:   req.StartDate,
		EndDate:     req.EndDate,
		Score:       req.Score,
		Location:    req.Location,
		Metadata:    req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateEducation(ctx, &edu); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, edu)
}

func (h *profileHandler) getEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	edu, err := h.cfg.ProfileRepo.GetEducation(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, edu)
}

func (h *profileHandler) updateEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	edu, err := h.cfg.ProfileRepo.GetEducation(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateEducationRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Institution != nil {
		edu.Institution = *req.Institution
	}
	if req.Degree != nil {
		edu.Degree = *req.Degree
	}
	if req.Field != nil {
		edu.Field = *req.Field
	}
	if req.StartDate != nil {
		edu.StartDate = *req.StartDate
	}
	if req.EndDate != nil {
		edu.EndDate = req.EndDate
	}
	if req.Score != nil {
		edu.Score = *req.Score
	}
	if req.Location != nil {
		edu.Location = *req.Location
	}
	if req.Metadata != nil {
		edu.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpdateEducation(ctx, edu); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, edu)
}

func (h *profileHandler) deleteEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteEducation(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Certifications Handlers ---

func (h *profileHandler) listCertifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	certs, err := h.cfg.ProfileRepo.ListCertifications(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if certs == nil {
		certs = []domain.Certification{}
	}
	writeJSON(w, http.StatusOK, certs)
}

func (h *profileHandler) createCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateCertificationRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	cert := domain.Certification{
		ID:       "cert-" + uuid.New().String(),
		UserID:   userID,
		Name:     req.Name,
		Issuer:   req.Issuer,
		Date:     req.Date,
		URL:      req.URL,
		Summary:  req.Summary,
		Metadata: req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateCertification(ctx, &cert); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, cert)
}

func (h *profileHandler) getCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	cert, err := h.cfg.ProfileRepo.GetCertification(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, cert)
}

func (h *profileHandler) updateCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	cert, err := h.cfg.ProfileRepo.GetCertification(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateCertificationRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name != nil {
		cert.Name = *req.Name
	}
	if req.Issuer != nil {
		cert.Issuer = *req.Issuer
	}
	if req.Date != nil {
		cert.Date = *req.Date
	}
	if req.URL != nil {
		cert.URL = *req.URL
	}
	if req.Summary != nil {
		cert.Summary = *req.Summary
	}
	if req.Metadata != nil {
		cert.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpdateCertification(ctx, cert); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, cert)
}

func (h *profileHandler) deleteCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteCertification(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Facts Handlers ---

func (h *profileHandler) listFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	ownerType := r.URL.Query().Get("owner_type")
	ownerID := r.URL.Query().Get("owner_id")

	if ownerType == "" || ownerID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "owner_type and owner_id query parameters are required", "")
		return
	}

	facts, err := h.cfg.ProfileRepo.ListFactsByOwner(ctx, userID, ownerType, ownerID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if facts == nil {
		facts = []domain.Fact{}
	}
	writeJSON(w, http.StatusOK, facts)
}

func (h *profileHandler) createFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateFactRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.OwnerID == "" || req.OwnerType == "" || req.Statement == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "owner_id, owner_type, and statement are required", "")
		return
	}

	switch req.OwnerType {
	case domain.OwnerTypeWorkExperience:
		if _, err := h.cfg.ProfileRepo.GetExperience(ctx, userID, req.OwnerID); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid owner_id for work_experience", err.Error())
			return
		}
	case domain.OwnerTypeProject:
		if _, err := h.cfg.ProfileRepo.GetProject(ctx, userID, req.OwnerID); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid owner_id for project", err.Error())
			return
		}
	case domain.OwnerTypeEducation:
		if _, err := h.cfg.ProfileRepo.GetEducation(ctx, userID, req.OwnerID); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid owner_id for education", err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, fmt.Sprintf("invalid owner_type: %q", req.OwnerType), "")
		return
	}

	var skills []domain.Skill
	if len(req.SkillIDs) > 0 {
		skills = make([]domain.Skill, len(req.SkillIDs))
		for i, sID := range req.SkillIDs {
			skills[i] = domain.Skill{ID: sID}
		}
	}

	fact := domain.Fact{
		ID:        "fact-" + uuid.New().String(),
		UserID:    userID,
		OwnerID:   req.OwnerID,
		OwnerType: req.OwnerType,
		Statement: req.Statement,
		Impact:    req.Impact,
		Skills:    skills,
	}

	if err := h.cfg.ProfileRepo.CreateFact(ctx, &fact); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, fact)
}

func (h *profileHandler) getFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	fact, err := h.cfg.ProfileRepo.GetFact(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, fact)
}

func (h *profileHandler) updateFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	fact, err := h.cfg.ProfileRepo.GetFact(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateFactRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Statement != nil {
		fact.Statement = *req.Statement
	}
	if req.Impact != nil {
		fact.Impact = *req.Impact
	}
	if req.SkillIDs != nil {
		skills := make([]domain.Skill, len(req.SkillIDs))
		for i, sID := range req.SkillIDs {
			skills[i] = domain.Skill{ID: sID}
		}
		fact.Skills = skills
	}

	if err := h.cfg.ProfileRepo.UpdateFact(ctx, fact); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, fact)
}

func (h *profileHandler) deleteFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteFact(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
