package api

import (
	"net/http"

	"github.com/resumeee-app/resumeee-core"
)

// CreateProjectRequest defines the JSON payload for creating a portfolio project entry.
type CreateProjectRequest struct {
	ShortID   string         `json:"short_id,omitempty"`
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	URL       string         `json:"url"`
	StartDate *core.Date     `json:"start_date"`
	EndDate   *core.Date     `json:"end_date"`
	Summary   string         `json:"summary"`
	SkillIDs  []string       `json:"skill_ids"`
	Metadata  map[string]any `json:"metadata"`
}

// UpdateProjectRequest defines the partial JSON payload for updating an existing portfolio project.
type UpdateProjectRequest struct {
	ShortID   *string        `json:"short_id,omitempty"`
	Name      *string        `json:"name"`
	Role      *string        `json:"role"`
	URL       *string        `json:"url"`
	StartDate *core.Date     `json:"start_date"`
	EndDate   *core.Date     `json:"end_date"`
	Summary   *string        `json:"summary"`
	SkillIDs  []string       `json:"skill_ids"`
	Metadata  map[string]any `json:"metadata"`
}

// listProjects returns all portfolio projects for the authenticated user.
func (h *profileHandler) listProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	projs, err := h.cfg.CareerStore.ListProjects(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}
	if projs == nil {
		projs = []core.Project{}
	}
	writeJSON(w, http.StatusOK, projs)
}

// createProject handles POST /api/v1/projects requests, persisting a new Project.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	var skills []core.Skill
	if len(req.SkillIDs) > 0 {
		skills = core.ParseSkillsInput(req.SkillIDs)
	}

	proj := core.Project{
		ID:        core.GenerateID("proj"),
		ShortID:   resolveEntityShortID(req.ShortID, "proj"),
		UserID:    userID,
		Name:      req.Name,
		Role:      req.Role,
		URL:       req.URL,
		StartDate: optionalDatePtr(req.StartDate),
		EndDate:   optionalDatePtr(req.EndDate),
		Summary:   req.Summary,
		Skills:    skills,
		Metadata:  req.Metadata,
	}

	if err := h.cfg.CareerStore.CreateProject(ctx, &proj); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, proj)
}

// getProject retrieves a project record by its identifier.
func (h *profileHandler) getProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	proj, err := h.cfg.CareerStore.Project(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, proj)
}

// updateProject handles PUT/PATCH /api/v1/projects/{id} requests, updating project attributes
// including optional short_id updates.
func (h *profileHandler) updateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	proj, err := h.cfg.CareerStore.Project(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	var req UpdateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	applyProjectUpdates(proj, req)

	if err := h.cfg.CareerStore.UpdateProject(ctx, proj); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, proj)
}

// applyProjectUpdates applies non-nil field updates from req onto proj.
func applyProjectUpdates(proj *core.Project, req UpdateProjectRequest) {
	applyShortIDUpdate(&proj.ShortID, req.ShortID)
	if req.Name != nil {
		proj.Name = *req.Name
	}
	if req.Role != nil {
		proj.Role = *req.Role
	}
	if req.URL != nil {
		proj.URL = *req.URL
	}
	applyOptionalDate(&proj.StartDate, req.StartDate)
	applyOptionalDate(&proj.EndDate, req.EndDate)
	if req.Summary != nil {
		proj.Summary = *req.Summary
	}
	if req.Metadata != nil {
		proj.Metadata = req.Metadata
	}
	if req.SkillIDs != nil {
		proj.Skills = core.ParseSkillsInput(req.SkillIDs)
	}
}

// deleteProject removes a project entry by its identifier.
func (h *profileHandler) deleteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.CareerStore.DeleteProject(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
