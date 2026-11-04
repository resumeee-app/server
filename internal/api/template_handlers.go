package api

import (
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"net/http"

	"github.com/google/uuid"

	"resumeee/internal/domain"
	"resumeee/internal/renderer"
)

type templateHandler struct {
	cfg RouterConfig
}

// registerTemplateRoutes configures HTTP endpoints for template set CRUD and server-side preview operations.
func registerTemplateRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &templateHandler{cfg: cfg}

	mux.HandleFunc("GET /api/v1/templates", h.listTemplates)
	mux.HandleFunc("POST /api/v1/templates", h.createTemplate)
	mux.HandleFunc("POST /api/v1/templates/preview", h.renderPreview)
	mux.HandleFunc("GET /api/v1/templates/{id}", h.getTemplate)
	mux.HandleFunc("GET /api/v1/templates/{id}/preview", h.previewTemplate)
	mux.HandleFunc("PUT /api/v1/templates/{id}", h.updateTemplate)
	mux.HandleFunc("DELETE /api/v1/templates/{id}", h.deleteTemplate)
}

// RenderTemplatePreviewRequest defines parameters for rendering arbitrary Go template snippets on the server.
type RenderTemplatePreviewRequest struct {
	TemplateID       string `json:"template_id"`
	HTMLTemplate     string `json:"html_template"`
	CSSStyles        string `json:"css_styles"`
	MarkdownTemplate string `json:"markdown_template"`
	TextTemplate     string `json:"text_template"`
}

type CreateTemplateRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	HTMLTemplate     string `json:"html_template"`
	CSSStyles        string `json:"css_styles"`
	MarkdownTemplate string `json:"markdown_template"`
	TextTemplate     string `json:"text_template"`
}

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
	userID := getUserID(r, h.cfg.DefaultUserID)

	templates, err := h.cfg.TemplateRepo.ListTemplates(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if templates == nil {
		templates = []domain.TemplateSet{}
	}
	writeJSON(w, http.StatusOK, templates)
}

// createTemplate validates syntax across templates and saves a new template set.
func (h *templateHandler) createTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" || req.HTMLTemplate == "" || req.MarkdownTemplate == "" || req.TextTemplate == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name, html_template, markdown_template, and text_template are required", "")
		return
	}

	tmpl := domain.TemplateSet{
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

	if err := h.cfg.TemplateRepo.CreateTemplate(ctx, &tmpl); err != nil {
		if errors.Is(err, domain.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template syntax error", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, tmpl)
}

// getTemplate retrieves a template set by its identifier.
func (h *templateHandler) getTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tmpl)
}

// updateTemplate validates updated templates and updates the template set.
func (h *templateHandler) updateTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
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

	if err := h.cfg.TemplateRepo.UpdateTemplate(ctx, tmpl); err != nil {
		if errors.Is(err, domain.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template syntax error", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tmpl)
}

// deleteTemplate removes a custom template set by its identifier.
func (h *templateHandler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.TemplateRepo.DeleteTemplate(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// defaultSeededProfileName identifies the default placeholder name seeded upon database initialization.
const defaultSeededProfileName = "Default User"

// hasCustomProfileData returns true if the profile contains non-empty custom user data rather than unconfigured defaults.
func hasCustomProfileData(prof *domain.Profile, snap *domain.MasterProfileSnapshot) bool {
	if prof == nil {
		return false
	}
	if prof.FullName != "" && prof.FullName != defaultSeededProfileName {
		return true
	}
	if snap != nil && (len(snap.Experiences) > 0 || len(snap.Skills) > 0 || len(snap.Projects) > 0 || len(snap.Education) > 0) {
		return true
	}
	return false
}

// getRenderContext constructs a comprehensive execution context for template rendering.
// Prefers actual user profile facts when present, merging with synthetic sample data for empty sections.
func (h *templateHandler) getRenderContext(ctx context.Context, userID string, tmpl *domain.TemplateSet) *renderer.RenderContext {
	synth := renderer.SyntheticRenderContext()
	if tmpl != nil && tmpl.CSSStyles != "" {
		synth.CSSStyles = htmltemplate.CSS(tmpl.CSSStyles)
	}

	if h.cfg.ProfileRepo == nil {
		return synth
	}

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err == nil && prof != nil {
		snapshot, _ := h.cfg.ProfileRepo.GetSnapshot(ctx, userID)
		if hasCustomProfileData(prof, snapshot) {
			var tailored *domain.TailoredContent
			if snapshot != nil {
				tailored = domain.SnapshotToTailoredContent(snapshot)
			}
			userCtx := renderer.NewRenderContext(tmpl, prof, tailored)
			if len(userCtx.Experiences) == 0 {
				userCtx.Experiences = synth.Experiences
			}
			if len(userCtx.SkillGroups) == 0 {
				userCtx.SkillGroups = synth.SkillGroups
			}
			if len(userCtx.Projects) == 0 {
				userCtx.Projects = synth.Projects
			}
			if len(userCtx.Education) == 0 {
				userCtx.Education = synth.Education
			}
			if len(userCtx.Certifications) == 0 {
				userCtx.Certifications = synth.Certifications
			}
			if userCtx.Summary == "" {
				userCtx.Summary = synth.Summary
			}
			return userCtx
		}
	}

	return synth
}

// renderAllFormats compiles and executes all template formats (HTML, Markdown, PlainText) using the provided context.
func (h *templateHandler) renderAllFormats(tmpl *domain.TemplateSet, renderCtx *renderer.RenderContext) (domain.RenderOutput, error) {
	if tmpl == nil {
		return domain.RenderOutput{}, nil
	}

	var out domain.RenderOutput
	if tmpl.HTMLTemplate != "" {
		htmlOut, err := h.cfg.Renderer.RenderFormat(domain.OutputFormatHTML, tmpl.HTMLTemplate, renderCtx)
		if err != nil {
			return domain.RenderOutput{}, fmt.Errorf("html template rendering failed: %w", err)
		}
		out.HTML = htmlOut
	}

	if tmpl.MarkdownTemplate != "" {
		mdOut, err := h.cfg.Renderer.RenderFormat(domain.OutputFormatMarkdown, tmpl.MarkdownTemplate, renderCtx)
		if err != nil {
			return domain.RenderOutput{}, fmt.Errorf("markdown template rendering failed: %w", err)
		}
		out.Markdown = mdOut
	}

	if tmpl.TextTemplate != "" {
		txtOut, err := h.cfg.Renderer.RenderFormat(domain.OutputFormatPlainText, tmpl.TextTemplate, renderCtx)
		if err != nil {
			return domain.RenderOutput{}, fmt.Errorf("text template rendering failed: %w", err)
		}
		out.PlainText = txtOut
	}

	return out, nil
}

// previewTemplate renders a saved template set on the server across HTML, Markdown, and text formats.
func (h *templateHandler) previewTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	renderCtx := h.getRenderContext(ctx, userID, tmpl)
	out, err := h.renderAllFormats(tmpl, renderCtx)
	if err != nil {
		if errors.Is(err, domain.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, err.Error(), "")
			return
		}
		writeDomainError(w, err)
		return
	}

	writeRenderOutput(w, r, out)
}

// renderPreview renders draft Go template definitions on the server for real-time editor previews.
func (h *templateHandler) renderPreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req RenderTemplatePreviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	htmlTmpl := req.HTMLTemplate
	cssStyles := req.CSSStyles
	mdTmpl := req.MarkdownTemplate
	txtTmpl := req.TextTemplate

	if htmlTmpl == "" && mdTmpl == "" && txtTmpl == "" && req.TemplateID != "" {
		if orig, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, req.TemplateID); err == nil && orig != nil {
			htmlTmpl = orig.HTMLTemplate
			cssStyles = orig.CSSStyles
			mdTmpl = orig.MarkdownTemplate
			txtTmpl = orig.TextTemplate
		}
	}

	tmpl := &domain.TemplateSet{
		HTMLTemplate:     htmlTmpl,
		CSSStyles:        cssStyles,
		MarkdownTemplate: mdTmpl,
		TextTemplate:     txtTmpl,
	}

	renderCtx := h.getRenderContext(ctx, userID, tmpl)
	if cssStyles != "" {
		renderCtx.CSSStyles = htmltemplate.CSS(cssStyles)
	}

	out, err := h.renderAllFormats(tmpl, renderCtx)
	if err != nil {
		if errors.Is(err, domain.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, err.Error(), "")
			return
		}
		writeDomainError(w, err)
		return
	}

	writeRenderOutput(w, r, out)
}

