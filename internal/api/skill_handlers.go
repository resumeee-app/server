package api

import (
	"net/http"
	"strings"

	"github.com/resumeee-app/resumeee-core"
)

// CreateSkillRequest defines the JSON payload for creating a custom skill in the candidate profile.
type CreateSkillRequest struct {
	ShortID         string         `json:"short_id,omitempty"`
	Name            string         `json:"name"`
	Category        string         `json:"category"`
	Proficiency     string         `json:"proficiency"`
	YearsExperience float64        `json:"years_experience"`
	Keywords        []string       `json:"keywords"`
	Metadata        map[string]any `json:"metadata"`
}

// UpdateSkillRequest defines the partial JSON payload for updating an existing custom skill.
type UpdateSkillRequest struct {
	ShortID         *string        `json:"short_id,omitempty"`
	Name            *string        `json:"name"`
	Category        *string        `json:"category"`
	Proficiency     *string        `json:"proficiency"`
	YearsExperience *float64       `json:"years_experience"`
	Keywords        []string       `json:"keywords"`
	Metadata        map[string]any `json:"metadata"`
}

// listSkills returns skills registered under the authenticated user, optionally filtered by origin type.
func (h *profileHandler) listSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var (
		skills []core.Skill
		err    error
	)

	skillType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	switch skillType {
	case "", "all":
		skills, err = h.cfg.CareerStore.ListSkills(ctx, userID)
	case "global":
		skills, err = h.cfg.CareerStore.ListGlobalSkills(ctx)
	case "custom":
		var allSkills []core.Skill
		allSkills, err = h.cfg.CareerStore.ListSkills(ctx, userID)
		if err == nil {
			skills = make([]core.Skill, 0, len(allSkills))
			for _, s := range allSkills {
				if !s.IsGlobal && s.UserID == userID {
					skills = append(skills, s)
				}
			}
		}
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid skill type filter: must be one of all, global, custom", "")
		return
	}

	if err != nil {
		writeDomainError(w, err)
		return
	}
	if skills == nil {
		skills = []core.Skill{}
	}
	writeJSON(w, http.StatusOK, skills)
}

// createSkill handles POST /api/v1/skills requests, persisting a new Skill.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreateSkillRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	skill := core.Skill{
		ID:              core.GenerateID("skill"),
		ShortID:         resolveEntityShortID(req.ShortID, "skill"),
		UserID:          userID,
		Name:            req.Name,
		Category:        req.Category,
		Proficiency:     req.Proficiency,
		YearsExperience: req.YearsExperience,
		Keywords:        req.Keywords,
		Metadata:        req.Metadata,
	}

	if err := h.cfg.CareerStore.CreateSkill(ctx, &skill); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, skill)
}

// getSkill retrieves a single skill by ID.
func (h *profileHandler) getSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	skill, err := h.cfg.CareerStore.Skill(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, skill)
}

// updateSkill handles PUT/PATCH /api/v1/skills/{id} requests, updating skill attributes
// including optional short_id updates.
func (h *profileHandler) updateSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	skill, err := h.cfg.CareerStore.Skill(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateSkillRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	applyShortIDUpdate(&skill.ShortID, req.ShortID)
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

	if err := h.cfg.CareerStore.UpdateSkill(ctx, skill); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, skill)
}

// deleteSkill deletes a skill by ID.
func (h *profileHandler) deleteSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.CareerStore.DeleteSkill(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
