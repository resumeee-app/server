package api

import (
	"context"
	"errors"
	htmltemplate "html/template"
	"net/http"

	"github.com/google/uuid"

	"github.com/resumeee-app/resumeee-api/internal/access"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/renderer"
)

// templateHandler serves resume template set CRUD and preview rendering endpoints.
type templateHandler struct {
	cfg RouterConfig
}

// registerTemplateRoutes configures HTTP endpoints for template set CRUD and server-side preview operations.
func registerTemplateRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &templateHandler{cfg: cfg}

	storeGuard := routeGuard{ok: cfg.TemplateStore != nil, msg: "template store unavailable"}
	renderGuard := routeGuard{ok: cfg.Renderer != nil, msg: "renderer unavailable"}

	mux.HandleFunc("GET /api/v1/templates", withRouteGuards(h.listTemplates, storeGuard))
	mux.HandleFunc("POST /api/v1/templates", withRouteGuards(h.createTemplate, storeGuard, renderGuard))
	mux.HandleFunc("POST /api/v1/templates/preview", withRouteGuards(h.renderPreview, renderGuard))
	mux.HandleFunc("GET /api/v1/templates/{id}", withRouteGuards(h.getTemplate, storeGuard))
	mux.HandleFunc("GET /api/v1/templates/{id}/preview", withRouteGuards(h.previewTemplate, storeGuard, renderGuard))
	mux.HandleFunc("PUT /api/v1/templates/{id}", withRouteGuards(h.updateTemplate, storeGuard, renderGuard))
	mux.HandleFunc("DELETE /api/v1/templates/{id}", withRouteGuards(h.deleteTemplate, storeGuard))
}

// RenderTemplatePreviewRequest defines parameters for rendering arbitrary Go template snippets on the server.
type RenderTemplatePreviewRequest struct {
	TemplateID       string `json:"template_id"`
	HTMLTemplate     string `json:"html_template"`
	CSSStyles        string `json:"css_styles"`
	MarkdownTemplate string `json:"markdown_template"`
	TextTemplate     string `json:"text_template"`
}

// CreateTemplateRequest defines the JSON payload for creating a custom resume template set.
type CreateTemplateRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	HTMLTemplate     string `json:"html_template"`
	CSSStyles        string `json:"css_styles"`
	MarkdownTemplate string `json:"markdown_template"`
	TextTemplate     string `json:"text_template"`
}

// UpdateTemplateRequest defines the partial JSON payload for updating a custom resume template set.
type UpdateTemplateRequest struct {
	Name             *string `json:"name"`
	Description      *string `json:"description"`
	HTMLTemplate     *string `json:"html_template"`
	CSSStyles        *string `json:"css_styles"`
	MarkdownTemplate *string `json:"markdown_template"`
	TextTemplate     *string `json:"text_template"`
}

// listTemplates returns all custom and built-in template sets for the user.
func (h *templateHandler) listTemplates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	templates, err := h.cfg.TemplateStore.ListTemplates(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}
	if templates == nil {
		templates = []core.TemplateSet{}
	}
	writeJSON(w, http.StatusOK, templates)
}

// createTemplate validates syntax across templates and saves a new template set.
func (h *templateHandler) createTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreateTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" || req.HTMLTemplate == "" || req.MarkdownTemplate == "" || req.TextTemplate == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name, html_template, markdown_template, and text_template are required", "")
		return
	}

	tmpl := core.TemplateSet{
		ID:               "tmpl-" + uuid.New().String(),
		UserID:           userID,
		Name:             req.Name,
		Description:      req.Description,
		HTMLTemplate:     req.HTMLTemplate,
		CSSStyles:        req.CSSStyles,
		MarkdownTemplate: req.MarkdownTemplate,
		TextTemplate:     req.TextTemplate,
	}

	if err := h.cfg.Renderer.Validate(&tmpl); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template validation failed", err.Error())
		return
	}

	if err := h.cfg.TemplateStore.CreateTemplate(ctx, &tmpl); err != nil {
		if errors.Is(err, core.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template syntax error", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, tmpl)
}

// getTemplate retrieves a template set by its identifier.
func (h *templateHandler) getTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	tmpl, err := h.cfg.TemplateStore.Template(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, tmpl)
}

// updateTemplate validates updated templates and updates the template set.
func (h *templateHandler) updateTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	tmpl, err := h.cfg.TemplateStore.Template(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	if access.IsSystemTemplate(tmpl) {
		writeError(w, http.StatusForbidden, CodeForbidden, "built-in templates cannot be modified", "")
		return
	}

	if tmpl.UserID != userID {
		writeError(w, http.StatusForbidden, CodeForbidden, "access denied to template", "")
		return
	}

	var req UpdateTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name != nil {
		tmpl.Name = *req.Name
	}
	if req.Description != nil {
		tmpl.Description = *req.Description
	}
	if req.HTMLTemplate != nil {
		tmpl.HTMLTemplate = *req.HTMLTemplate
	}
	if req.CSSStyles != nil {
		tmpl.CSSStyles = *req.CSSStyles
	}
	if req.MarkdownTemplate != nil {
		tmpl.MarkdownTemplate = *req.MarkdownTemplate
	}
	if req.TextTemplate != nil {
		tmpl.TextTemplate = *req.TextTemplate
	}

	if err := h.cfg.Renderer.Validate(tmpl); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template validation failed", err.Error())
		return
	}

	if err := h.cfg.TemplateStore.UpdateTemplate(ctx, tmpl); err != nil {
		if errors.Is(err, core.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template syntax error", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, tmpl)
}

// deleteTemplate removes a custom template set by its identifier.
func (h *templateHandler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	tmpl, err := h.cfg.TemplateStore.Template(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	if access.IsSystemTemplate(tmpl) {
		writeError(w, http.StatusForbidden, CodeForbidden, "built-in templates cannot be deleted", "")
		return
	}

	if tmpl.UserID != userID {
		writeError(w, http.StatusForbidden, CodeForbidden, "access denied to template", "")
		return
	}

	if err := h.cfg.TemplateStore.DeleteTemplate(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// previewTemplate renders a saved template set on the server across HTML, Markdown, and text formats.
func (h *templateHandler) previewTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	tmpl, err := h.cfg.TemplateStore.Template(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	renderCtx := renderer.BuildPreviewRenderContext(ctx, h.cfg.ProfileStore, userID, tmpl)
	out, err := renderer.RenderAllFormats(h.cfg.Renderer, tmpl, renderCtx)
	if err != nil {
		if errors.Is(err, core.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, err.Error(), "")
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeRenderOutput(w, r, out)
}

// renderPreview renders draft Go template definitions on the server for real-time editor previews.
func (h *templateHandler) renderPreview(w http.ResponseWriter, r *http.Request) {
	previewTimeout := h.cfg.RenderPreviewTimeout
	if previewTimeout <= 0 {
		previewTimeout = config.DefaultRenderPreviewTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), previewTimeout)
	defer cancel()

	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req RenderTemplatePreviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	htmlTmpl := req.HTMLTemplate
	cssStyles := req.CSSStyles
	mdTmpl := req.MarkdownTemplate
	txtTmpl := req.TextTemplate

	if htmlTmpl == "" && mdTmpl == "" && txtTmpl == "" && req.TemplateID != "" {
		if orig, err := access.Template(ctx, h.cfg.TemplateStore, userID, req.TemplateID); err == nil && orig != nil {
			htmlTmpl = orig.HTMLTemplate
			cssStyles = orig.CSSStyles
			mdTmpl = orig.MarkdownTemplate
			txtTmpl = orig.TextTemplate
		} else if msg, ok := access.StoreUnavailableMessage(err); ok {
			writeError(w, http.StatusInternalServerError, CodeInternalError, msg, "")
			return
		}
	}

	tmpl := &core.TemplateSet{
		HTMLTemplate:     htmlTmpl,
		CSSStyles:        cssStyles,
		MarkdownTemplate: mdTmpl,
		TextTemplate:     txtTmpl,
	}

	renderCtx := renderer.BuildPreviewRenderContext(ctx, h.cfg.ProfileStore, userID, tmpl)
	if cssStyles != "" {
		renderCtx.CSSStyles = htmltemplate.CSS(cssStyles)
	}

	out, err := renderer.RenderAllFormats(h.cfg.Renderer, tmpl, renderCtx)
	if err != nil {
		if errors.Is(err, core.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, err.Error(), "")
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeRenderOutput(w, r, out)
}
