package api

import (
	"net/http"

	"github.com/resumeee-app/resumeee-core"
)

// CreateCertificationRequest defines the JSON payload for creating a professional certification entry.
type CreateCertificationRequest struct {
	ShortID  string         `json:"short_id,omitempty"`
	Name     string         `json:"name"`
	Issuer   string         `json:"issuer"`
	Date     *core.Date     `json:"date"`
	URL      string         `json:"url"`
	Summary  string         `json:"summary"`
	Metadata map[string]any `json:"metadata"`
}

// UpdateCertificationRequest defines the partial JSON payload for updating an existing certification entry.
type UpdateCertificationRequest struct {
	ShortID  *string        `json:"short_id,omitempty"`
	Name     *string        `json:"name"`
	Issuer   *string        `json:"issuer"`
	Date     *core.Date     `json:"date"`
	URL      *string        `json:"url"`
	Summary  *string        `json:"summary"`
	Metadata map[string]any `json:"metadata"`
}

// listCertifications returns all professional certifications recorded for the user.
func (h *profileHandler) listCertifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	certs, err := h.cfg.CareerStore.ListCertifications(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if certs == nil {
		certs = []core.Certification{}
	}
	writeJSON(w, http.StatusOK, certs)
}

// createCertification handles POST /api/v1/certifications requests, persisting a new Certification.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreateCertificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	cert := core.Certification{
		ID:       core.GenerateID("cert"),
		ShortID:  resolveEntityShortID(req.ShortID, "cert"),
		UserID:   userID,
		Name:     req.Name,
		Issuer:   req.Issuer,
		Date:     optionalDatePtr(req.Date),
		URL:      req.URL,
		Summary:  req.Summary,
		Metadata: req.Metadata,
	}

	if err := h.cfg.CareerStore.CreateCertification(ctx, &cert); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, cert)
}

// getCertification retrieves a certification record by its identifier.
func (h *profileHandler) getCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	cert, err := h.cfg.CareerStore.Certification(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, cert)
}

// updateCertification handles PUT/PATCH /api/v1/certifications/{id} requests, updating certification attributes
// including optional short_id updates.
func (h *profileHandler) updateCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	cert, err := h.cfg.CareerStore.Certification(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateCertificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	applyShortIDUpdate(&cert.ShortID, req.ShortID)
	if req.Name != nil {
		cert.Name = *req.Name
	}
	if req.Issuer != nil {
		cert.Issuer = *req.Issuer
	}
	applyOptionalDate(&cert.Date, req.Date)
	if req.URL != nil {
		cert.URL = *req.URL
	}
	if req.Summary != nil {
		cert.Summary = *req.Summary
	}
	if req.Metadata != nil {
		cert.Metadata = req.Metadata
	}

	if err := h.cfg.CareerStore.UpdateCertification(ctx, cert); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, cert)
}

// deleteCertification removes a certification record by its identifier.
func (h *profileHandler) deleteCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.CareerStore.DeleteCertification(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
