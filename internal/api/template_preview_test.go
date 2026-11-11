package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/resumeee-app/resumeee-core/domain"
)

func TestTemplatePreview_SanitizesBuiltinTemplates(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	builtinIDs := []string{
		"builtin-standard-clean",
		"builtin-detailed-immigration-cv",
		"builtin-ats-minimal-txt",
		"builtin-ats-hybrid-tech",
		"builtin-ats-executive-clean",
	}

	for _, id := range builtinIDs {
		t.Run(id, func(t *testing.T) {
			// 1. Verify JSON preview endpoint strips script tags
			resp, body := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+id+"/preview", nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 from GET /templates/%s/preview, got %d: %s", id, resp.StatusCode, string(body))
			}

			var out domain.RenderOutput
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("failed to decode preview JSON: %v", err)
			}

			if strings.Contains(strings.ToLower(out.HTML), "<script") || strings.Contains(strings.ToLower(out.HTML), "</script>") {
				t.Errorf("expected out.HTML for template %s to have script tags stripped, but found script tag: %s", id, out.HTML)
			}

			// 2. Verify ?format=html preview endpoint strips script tags
			resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+id+"/preview?format=html", nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 from GET /templates/%s/preview?format=html, got %d: %s", id, resp.StatusCode, string(body))
			}
			htmlStr := string(body)
			if strings.Contains(strings.ToLower(htmlStr), "<script") || strings.Contains(strings.ToLower(htmlStr), "</script>") {
				t.Errorf("expected ?format=html for template %s to have script tags stripped, but found script tag: %s", id, htmlStr)
			}
		})
	}
}

func TestTemplatePreview_SanitizesCustomTemplateWithScripts(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	// Create custom template containing various script tags
	createReq := map[string]string{
		"name": "Custom Script Test Template",
		"description": "Template with script tags to verify preview sanitization",
		"html_template": `
			<div class="resume">
				<h1>{{.FullName}}</h1>
				<script type="application/ld+json">{"@context": "https://schema.org"}</script>
				<script>alert('xss');</script>
				<script src="https://evil.example.com/payload.js"></script>
				<p>{{.Headline}}</p>
			</div>
		`,
		"css_styles": ".resume { color: #333; }",
		"markdown_template": "# {{.FullName}}\n{{.Headline}}",
		"text_template": "{{.FullName}} - {{.Headline}}",
	}

	resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates", createReq, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create custom template, got %d: %s", resp.StatusCode, string(body))
	}

	var created domain.TemplateSet
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("failed to decode created template: %v", err)
	}

	// GET preview JSON
	resp, body = doRequest(t, client, http.MethodGet, baseURL+"/api/v1/templates/"+created.ID+"/preview", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from GET /templates/{id}/preview, got %d: %s", resp.StatusCode, string(body))
	}

	var previewOut domain.RenderOutput
	if err := json.Unmarshal(body, &previewOut); err != nil {
		t.Fatalf("failed to parse preview JSON: %v", err)
	}

	if strings.Contains(strings.ToLower(previewOut.HTML), "<script") || strings.Contains(strings.ToLower(previewOut.HTML), "</script>") {
		t.Errorf("expected custom template preview HTML to have script tags stripped, got: %s", previewOut.HTML)
	}
	if !strings.Contains(previewOut.HTML, "Jane Doe") && !strings.Contains(previewOut.HTML, "Alex Morgan") {
		t.Errorf("expected template preview to retain rendered user data, got: %s", previewOut.HTML)
	}
	if !strings.Contains(previewOut.HTML, "<p>") {
		t.Errorf("expected template preview to retain safe HTML tags, got: %s", previewOut.HTML)
	}
}

func TestRenderDraftPreview_SanitizesScripts(t *testing.T) {
	env := setupTestServer(t, 10)
	client := env.server.Client()
	baseURL := env.server.URL

	draftReq := map[string]string{
		"html_template": `
			<header>
				<script>console.log('malicious');</script>
				<h1>{{.FullName}}</h1>
				<script type="application/ld+json">{"@type": "Person"}</script>
			</header>
			<section>
				<script src="/evil.js"></script>
				<p>{{.Headline}}</p>
			</section>
		`,
		"css_styles":        "h1 { font-size: 20px; }",
		"markdown_template": "## {{.FullName}}",
		"text_template":     "{{.FullName}}",
	}

	// 1. POST JSON draft preview
	resp, body := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates/preview", draftReq, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from POST /templates/preview, got %d: %s", resp.StatusCode, string(body))
	}

	var draftOut domain.RenderOutput
	if err := json.Unmarshal(body, &draftOut); err != nil {
		t.Fatalf("failed to decode draft preview: %v", err)
	}

	if strings.Contains(strings.ToLower(draftOut.HTML), "<script") || strings.Contains(strings.ToLower(draftOut.HTML), "</script>") {
		t.Errorf("expected draft preview HTML to have script tags stripped, got: %s", draftOut.HTML)
	}
	if !strings.Contains(draftOut.HTML, "<header>") || !strings.Contains(draftOut.HTML, "<section>") {
		t.Errorf("expected draft preview to preserve legitimate HTML structure, got: %s", draftOut.HTML)
	}

	// 2. POST ?format=html draft preview
	resp, body = doRequest(t, client, http.MethodPost, baseURL+"/api/v1/templates/preview?format=html", draftReq, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from POST /templates/preview?format=html, got %d: %s", resp.StatusCode, string(body))
	}
	htmlStr := string(body)
	if strings.Contains(strings.ToLower(htmlStr), "<script") || strings.Contains(strings.ToLower(htmlStr), "</script>") {
		t.Errorf("expected draft preview ?format=html to have script tags stripped, got: %s", htmlStr)
	}
}
