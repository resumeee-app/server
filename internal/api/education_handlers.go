package api

import (
	"net/http"

	"github.com/resumeee-app/resumeee-core"
)

// CreateEducationRequest defines the JSON payload for creating an academic education entry.
type CreateEducationRequest struct {
	ShortID     string         `json:"short_id,omitempty"`
	Institution string         `json:"institution"`
	Degree      string         `json:"degree"`
	Field       string         `json:"field"`
	StartDate   *core.Date     `json:"start_date"`
	EndDate     *core.Date     `json:"end_date"`
	Score       string         `json:"score"`
	Location    core.Location  `json:"location"`
	Metadata    map[string]any `json:"metadata"`
}

// UpdateEducationRequest defines the partial JSON payload for updating an existing education entry.
type UpdateEducationRequest struct {
	ShortID     *string        `json:"short_id,omitempty"`
	Institution *string        `json:"institution"`
	Degree      *string        `json:"degree"`
	Field       *string        `json:"field"`
	StartDate   *core.Date     `json:"start_date"`
	EndDate     *core.Date     `json:"end_date"`
	Score       *string        `json:"score"`
	Location    *core.Location `json:"location"`
	Metadata    map[string]any `json:"metadata"`
}

// listEducation returns all education records for the authenticated user.
func (h *profileHandler) listEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	edus, err := h.cfg.CareerStore.ListEducation(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}
	if edus == nil {
		edus = []core.Education{}
	}
	writeJSON(w, http.StatusOK, edus)
}

// createEducation handles POST /api/v1/education requests, persisting a new Education record.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreateEducationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Institution == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "institution is required", "")
		return
	}

	edu := core.Education{
		ID:          core.GenerateID("edu"),
		ShortID:     resolveEntityShortID(req.ShortID, "edu"),
		UserID:      userID,
		Institution: req.Institution,
		Degree:      req.Degree,
		Field:       req.Field,
		StartDate:   optionalDatePtr(req.StartDate),
		EndDate:     optionalDatePtr(req.EndDate),
		Score:       req.Score,
		Location:    req.Location,
		Metadata:    req.Metadata,
	}

	if err := h.cfg.CareerStore.CreateEducation(ctx, &edu); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, edu)
}

// getEducation retrieves an education record by its identifier.
func (h *profileHandler) getEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	edu, err := h.cfg.CareerStore.Education(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, edu)
}

// updateEducation handles PUT/PATCH /api/v1/education/{id} requests, updating education attributes
// including optional short_id updates.
func (h *profileHandler) updateEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	edu, err := h.cfg.CareerStore.Education(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	var req UpdateEducationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	applyShortIDUpdate(&edu.ShortID, req.ShortID)
	if req.Institution != nil {
		edu.Institution = *req.Institution
	}
	if req.Degree != nil {
		edu.Degree = *req.Degree
	}
	if req.Field != nil {
		edu.Field = *req.Field
	}
	applyOptionalDate(&edu.StartDate, req.StartDate)
	applyOptionalDate(&edu.EndDate, req.EndDate)
	if req.Score != nil {
		edu.Score = *req.Score
	}
	if req.Location != nil {
		edu.Location = *req.Location
	}
	if req.Metadata != nil {
		edu.Metadata = req.Metadata
	}

	if err := h.cfg.CareerStore.UpdateEducation(ctx, edu); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, edu)
}

// deleteEducation removes an education entry by its identifier.
func (h *profileHandler) deleteEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.CareerStore.DeleteEducation(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
