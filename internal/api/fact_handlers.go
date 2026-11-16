package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/resumeee-app/core"
)

// CreateFactRequest defines the JSON payload for creating a verifiable achievement fact linked to an owner entity.
type CreateFactRequest struct {
	ShortID   string         `json:"short_id,omitempty"`
	OwnerID   string         `json:"owner_id"`
	OwnerType core.OwnerType `json:"owner_type"`
	Statement string         `json:"statement"`
	Impact    string         `json:"impact"`
	SkillIDs  []string       `json:"skill_ids"`
}

// UpdateFactRequest defines the partial JSON payload for updating an existing achievement fact.
type UpdateFactRequest struct {
	ShortID   *string  `json:"short_id,omitempty"`
	Statement *string  `json:"statement"`
	Impact    *string  `json:"impact"`
	SkillIDs  []string `json:"skill_ids"`
}

// listFacts returns career facts associated with an entity specified by owner_type and owner_id query params.
func (h *profileHandler) listFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	ownerType := r.URL.Query().Get("owner_type")
	ownerID := r.URL.Query().Get("owner_id")

	if ownerType == "" || ownerID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "owner_type and owner_id query parameters are required", "")
		return
	}

	facts, err := h.cfg.CareerStore.ListFactsByOwner(ctx, userID, ownerType, ownerID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}
	if facts == nil {
		facts = []core.Fact{}
	}
	writeJSON(w, http.StatusOK, facts)
}

// createFact handles POST /api/v1/facts requests, persisting a new child Fact.
// It generates a canonical UUID, auto-generates a short_id if omitted, and resolves owner_id to its canonical UUID.
func (h *profileHandler) createFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreateFactRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.OwnerID == "" || req.OwnerType == "" || req.Statement == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "owner_id, owner_type, and statement are required", "")
		return
	}

	canonicalOwnerID, ok := h.resolveCanonicalFactOwner(ctx, w, userID, req.OwnerType, req.OwnerID)
	if !ok {
		return
	}

	var skills []core.Skill
	if len(req.SkillIDs) > 0 {
		skills = core.ParseSkillsInput(req.SkillIDs)
	}

	fact := core.Fact{
		ID:        core.GenerateID("fact"),
		ShortID:   resolveEntityShortID(req.ShortID, "fact"),
		UserID:    userID,
		OwnerID:   canonicalOwnerID,
		OwnerType: req.OwnerType,
		Statement: req.Statement,
		Impact:    req.Impact,
		Skills:    skills,
	}

	if err := h.cfg.CareerStore.CreateFact(ctx, &fact); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, fact)
}

// resolveCanonicalFactOwner verifies ownership of ownerID (supporting short IDs) and returns its canonical UUID.
func (h *profileHandler) resolveCanonicalFactOwner(ctx context.Context, w http.ResponseWriter, userID string, ownerType core.OwnerType, ownerID string) (string, bool) {
	switch ownerType {
	case core.OwnerTypeWorkExperience:
		exp, err := h.cfg.CareerStore.Experience(ctx, userID, ownerID)
		if err != nil || exp == nil {
			h.writeFactOwnerError(w, "work_experience", err)
			return "", false
		}
		return exp.ID, true
	case core.OwnerTypeProject:
		proj, err := h.cfg.CareerStore.Project(ctx, userID, ownerID)
		if err != nil || proj == nil {
			h.writeFactOwnerError(w, "project", err)
			return "", false
		}
		return proj.ID, true
	case core.OwnerTypeEducation:
		edu, err := h.cfg.CareerStore.Education(ctx, userID, ownerID)
		if err != nil || edu == nil {
			h.writeFactOwnerError(w, "education", err)
			return "", false
		}
		return edu.ID, true
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, fmt.Sprintf("invalid owner_type: %q", ownerType), "")
		return "", false
	}
}

// writeFactOwnerError writes a 400 response when an owner entity is missing or delegates unexpected store errors.
func (h *profileHandler) writeFactOwnerError(w http.ResponseWriter, ownerLabel string, err error) {
	if err == nil || errors.Is(err, core.ErrNotFound) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid owner_id for "+ownerLabel, "")
		return
	}
	writeDomainError(w, h.cfg.Logger, err)
}

// getFact retrieves an individual fact by its identifier.
func (h *profileHandler) getFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	fact, err := h.cfg.CareerStore.Fact(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, fact)
}

// updateFact handles PUT/PATCH /api/v1/facts/{id} requests, updating fact attributes
// including optional short_id updates.
func (h *profileHandler) updateFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	fact, err := h.cfg.CareerStore.Fact(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	var req UpdateFactRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	applyShortIDUpdate(&fact.ShortID, req.ShortID)
	if req.Statement != nil {
		fact.Statement = *req.Statement
	}
	if req.Impact != nil {
		fact.Impact = *req.Impact
	}
	if req.SkillIDs != nil {
		fact.Skills = core.ParseSkillsInput(req.SkillIDs)
	}

	if err := h.cfg.CareerStore.UpdateFact(ctx, fact); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, fact)
}

// deleteFact removes a fact record by its identifier.
func (h *profileHandler) deleteFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.CareerStore.DeleteFact(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
