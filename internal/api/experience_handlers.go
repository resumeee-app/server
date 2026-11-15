package api

import (
	"net/http"

	"github.com/resumeee-app/resumeee-core"
)

// CreateExperienceRequest defines the JSON payload for creating a work experience entry.
type CreateExperienceRequest struct {
	ShortID           string              `json:"short_id,omitempty"`
	Company           string              `json:"company"`
	RoleTitle         string              `json:"role_title"`
	EmploymentType    core.EmploymentType `json:"employment_type"`
	StartDate         core.Date           `json:"start_date"`
	EndDate           *core.Date          `json:"end_date"`
	IsCurrent         bool                `json:"is_current"`
	Location          core.Location       `json:"location"`
	CompanyLocation   *core.Location      `json:"company_location"`
	CompanyContact    *core.ContactDetail `json:"company_contact"`
	HoursPerWeek      *int                `json:"hours_per_week"`
	SupervisorName    string              `json:"supervisor_name"`
	SupervisorContact string              `json:"supervisor_contact"`
	DutiesSummary     string              `json:"duties_summary"`
	SkillIDs          []string            `json:"skill_ids"`
	Metadata          map[string]any      `json:"metadata"`
}

// UpdateExperienceRequest defines the partial JSON payload for updating an existing work experience entry.
type UpdateExperienceRequest struct {
	ShortID           *string              `json:"short_id,omitempty"`
	Company           *string              `json:"company"`
	RoleTitle         *string              `json:"role_title"`
	EmploymentType    *core.EmploymentType `json:"employment_type"`
	StartDate         *core.Date           `json:"start_date"`
	EndDate           *core.Date           `json:"end_date"`
	IsCurrent         *bool                `json:"is_current"`
	Location          *core.Location       `json:"location"`
	CompanyLocation   *core.Location       `json:"company_location"`
	CompanyContact    *core.ContactDetail  `json:"company_contact"`
	HoursPerWeek      *int                 `json:"hours_per_week"`
	SupervisorName    *string              `json:"supervisor_name"`
	SupervisorContact *string              `json:"supervisor_contact"`
	DutiesSummary     *string              `json:"duties_summary"`
	SkillIDs          []string             `json:"skill_ids"`
	Metadata          map[string]any       `json:"metadata"`
}

// listExperiences retrieves all work experience entries for the authenticated user.
func (h *profileHandler) listExperiences(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	exps, err := h.cfg.CareerStore.ListExperiences(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}
	if exps == nil {
		exps = []core.WorkExperience{}
	}
	writeJSON(w, http.StatusOK, exps)
}

// createExperience handles POST /api/v1/experiences requests, persisting a new WorkExperience.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreateExperienceRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Company == "" || req.RoleTitle == "" || req.StartDate.IsZero() {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "company, role_title, and start_date are required", "")
		return
	}

	var skills []core.Skill
	if len(req.SkillIDs) > 0 {
		skills = core.ParseSkillsInput(req.SkillIDs)
	}

	exp := core.WorkExperience{
		ID:                core.GenerateID("exp"),
		ShortID:           resolveEntityShortID(req.ShortID, "exp"),
		UserID:            userID,
		Company:           req.Company,
		RoleTitle:         req.RoleTitle,
		EmploymentType:    req.EmploymentType,
		StartDate:         req.StartDate,
		EndDate:           optionalDatePtr(req.EndDate),
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

	if err := h.cfg.CareerStore.CreateExperience(ctx, &exp); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, exp)
}

// getExperience retrieves a specific work experience entry by its identifier.
func (h *profileHandler) getExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	exp, err := h.cfg.CareerStore.Experience(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, exp)
}

// updateExperience handles PUT/PATCH /api/v1/experiences/{id} requests, updating experience attributes
// including optional short_id updates.
func (h *profileHandler) updateExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	exp, err := h.cfg.CareerStore.Experience(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	var req UpdateExperienceRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	applyExperienceUpdates(exp, req)

	if err := h.cfg.CareerStore.UpdateExperience(ctx, exp); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, exp)
}

// applyExperienceUpdates applies non-nil field updates from req onto exp, clearing zero/empty optional fields when specified.
func applyExperienceUpdates(exp *core.WorkExperience, req UpdateExperienceRequest) {
	applyShortIDUpdate(&exp.ShortID, req.ShortID)
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
	applyOptionalDate(&exp.EndDate, req.EndDate)
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
		if req.CompanyContact.Value == "" {
			exp.CompanyContact = nil
		} else {
			exp.CompanyContact = req.CompanyContact
		}
	}
	if req.HoursPerWeek != nil {
		if *req.HoursPerWeek <= 0 {
			exp.HoursPerWeek = nil
		} else {
			exp.HoursPerWeek = req.HoursPerWeek
		}
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
		exp.Skills = core.ParseSkillsInput(req.SkillIDs)
	}
}

// deleteExperience removes a work experience entry by its identifier.
func (h *profileHandler) deleteExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.CareerStore.DeleteExperience(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
