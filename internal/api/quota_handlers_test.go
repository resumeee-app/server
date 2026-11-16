package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/memory"
	"github.com/resumeee-app/core/worker"
	"github.com/resumeee-app/server/internal/api"
)

func TestGetQuota_DefaultsAndRemaining(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "tenant-test-user-1"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	router := api.NewRouter(api.RouterConfig{
		QuotaStore:        store.Quotas(),
		DefaultUserID:     defaultUserID,
		Admin:             true,
		DailyRequestLimit: 30,
		MonthlyTokenLimit: 750000,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	// 1. Query GET /api/v1/quota
	resp, body := doRequest(t, server.Client(), http.MethodGet, server.URL+"/api/v1/quota", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	var quotaPayload struct {
		UserID                 string    `json:"user_id"`
		DailyRequestsUsed      int       `json:"daily_requests_used"`
		DailyRequestLimit      int       `json:"daily_request_limit"`
		MonthlyTokensUsed      int       `json:"monthly_tokens_used"`
		MonthlyTokenLimit      int       `json:"monthly_token_limit"`
		DailyRequestsRemaining int       `json:"daily_requests_remaining"`
		MonthlyTokensRemaining int       `json:"monthly_tokens_remaining"`
		DailyResetAt           time.Time `json:"daily_reset_at"`
		MonthlyResetAt         time.Time `json:"monthly_reset_at"`
	}
	if err := json.Unmarshal(body, &quotaPayload); err != nil {
		t.Fatalf("failed to decode quota response: %v", err)
	}

	if quotaPayload.UserID != defaultUserID {
		t.Errorf("expected user_id %q, got %q", defaultUserID, quotaPayload.UserID)
	}
	if quotaPayload.DailyRequestLimit != 30 {
		t.Errorf("expected daily_request_limit 30, got %d", quotaPayload.DailyRequestLimit)
	}
	if quotaPayload.MonthlyTokenLimit != 750000 {
		t.Errorf("expected monthly_token_limit 750000, got %d", quotaPayload.MonthlyTokenLimit)
	}
	if quotaPayload.DailyRequestsRemaining != 30 {
		t.Errorf("expected daily_requests_remaining 30, got %d", quotaPayload.DailyRequestsRemaining)
	}
	if quotaPayload.MonthlyTokensRemaining != 750000 {
		t.Errorf("expected monthly_tokens_remaining 750000, got %d", quotaPayload.MonthlyTokensRemaining)
	}
	if quotaPayload.DailyResetAt.IsZero() {
		t.Errorf("expected daily_reset_at to be non-zero")
	}
	if quotaPayload.MonthlyResetAt.IsZero() {
		t.Errorf("expected monthly_reset_at to be non-zero")
	}

	// 2. Also check GET /quota alias
	respAlias, bodyAlias := doRequest(t, server.Client(), http.MethodGet, server.URL+"/quota", nil, nil)
	if respAlias.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 on /quota alias, got %d: %s", respAlias.StatusCode, string(bodyAlias))
	}
}

func TestGetQuota_AfterUsageReservation(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "tenant-test-user-2"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	// Reserve 5 requests and 5000 tokens
	for i := 0; i < 5; i++ {
		_, err := store.Quotas().ReserveQuota(ctx, defaultUserID, core.OperationResumeTailoring, 1000, 50, 1000000)
		if err != nil {
			t.Fatalf("failed to reserve quota: %v", err)
		}
	}

	router := api.NewRouter(api.RouterConfig{
		QuotaStore:        store.Quotas(),
		DefaultUserID:     defaultUserID,
		Admin:             true,
		DailyRequestLimit: 50,
		MonthlyTokenLimit: 1000000,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, body := doRequest(t, server.Client(), http.MethodGet, server.URL+"/api/v1/quota", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	var quotaPayload struct {
		DailyRequestsUsed      int `json:"daily_requests_used"`
		DailyRequestsRemaining int `json:"daily_requests_remaining"`
		MonthlyTokensUsed      int `json:"monthly_tokens_used"`
		MonthlyTokensRemaining int `json:"monthly_tokens_remaining"`
	}
	if err := json.Unmarshal(body, &quotaPayload); err != nil {
		t.Fatalf("failed to decode quota response: %v", err)
	}

	if quotaPayload.DailyRequestsUsed != 5 {
		t.Errorf("expected daily_requests_used 5, got %d", quotaPayload.DailyRequestsUsed)
	}
	if quotaPayload.DailyRequestsRemaining != 45 {
		t.Errorf("expected daily_requests_remaining 45, got %d", quotaPayload.DailyRequestsRemaining)
	}
	if quotaPayload.MonthlyTokensUsed != 5000 {
		t.Errorf("expected monthly_tokens_used 5000, got %d", quotaPayload.MonthlyTokensUsed)
	}
	if quotaPayload.MonthlyTokensRemaining != 995000 {
		t.Errorf("expected monthly_tokens_remaining 995000, got %d", quotaPayload.MonthlyTokensRemaining)
	}
}

func TestWriteQuotaError_RetryAfterHeader(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "tenant-limited"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	// Consume entire quota (daily limit = 1)
	_, err = store.Quotas().ReserveQuota(ctx, defaultUserID, core.OperationResumeTailoring, 100, 1, 100000)
	if err != nil {
		t.Fatalf("failed to reserve quota: %v", err)
	}

	router := api.NewRouter(api.RouterConfig{
		QuotaStore:        store.Quotas(),
		DefaultUserID:     defaultUserID,
		Admin:             true,
		DailyRequestLimit: 1,
		MonthlyTokenLimit: 100000,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	// Calling parse with sync=true should fail with 429 when quota is exceeded
	pdfBytes := buildTestPDF("Valid resume text for PDF quota testing")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "test.pdf")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	_, _ = part.Write(pdfBytes)
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/profile/pdf/parse?sync=true", &buf)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", resp.StatusCode)
	}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("expected Retry-After header to be present on 429 response")
	} else if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds <= 0 {
		t.Errorf("expected positive numeric Retry-After header, got %q", retryAfter)
	}
}

type mockUsageResumeExtractor struct {
	resume    *core.JSONResume
	telemetry core.LLMTelemetry
	err       error
	calls     atomic.Int32
}

// Compile-time assertion ensuring mockUsageResumeExtractor satisfies worker.ResumeExtractor.
var _ worker.ResumeExtractor = (*mockUsageResumeExtractor)(nil)

func (m *mockUsageResumeExtractor) ExtractResume(_ context.Context, _, _, _ string) (*core.JSONResume, core.LLMTelemetry, error) {
	m.calls.Add(1)
	return m.resume, m.telemetry, m.err
}

// usageRecordLister defines the test-only subset needed to inspect persisted LLM usage records.
type usageRecordLister interface {
	ListUsageRecords(ctx context.Context, userID string, limit int) ([]core.LLMUsageRecord, error)
}

// singleUsageRecord returns the only LLM usage record for userID, failing the test when the count differs.
func singleUsageRecord(t *testing.T, store usageRecordLister, userID string) core.LLMUsageRecord {
	t.Helper()
	records, err := store.ListUsageRecords(context.Background(), userID, 10)
	if err != nil {
		t.Fatalf("failed to list usage records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 usage record, got %d: %+v", len(records), records)
	}
	return records[0]
}

func TestSyncPDFParse_QuotaCommitOnSuccess(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "tenant-sync-success"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	extractor := &mockUsageResumeExtractor{
		resume: &core.JSONResume{
			Basics: core.JSONResumeBasics{
				Name:  "Test Candidate",
				Email: "test@example.com",
			},
		},
		telemetry: core.LLMTelemetry{
			Provider:   "openai",
			Model:      "gpt-4o-mini",
			StatusCode: http.StatusOK,
			Latency:    750 * time.Millisecond,
			Usage: core.TokenUsage{
				PromptTokens:     120,
				CompletionTokens: 380,
				TotalTokens:      500,
			},
		},
	}

	router := api.NewRouter(api.RouterConfig{
		QuotaStore:        store.Quotas(),
		DefaultUserID:     defaultUserID,
		Admin:             true,
		ResumeExtractor:   extractor,
		DailyRequestLimit: 50,
		MonthlyTokenLimit: 1000000,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	pdfBytes := buildTestPDF("Valid resume text for PDF parse")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "resume.pdf")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	_, _ = part.Write(pdfBytes)
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/profile/pdf/parse?sync=true", &buf)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	if got := extractor.calls.Load(); got != 1 {
		t.Errorf("expected ResumeExtractor to be called once, got %d", got)
	}

	// Verify quota was committed: daily used should be 1, monthly tokens should be 500 (not 1000)
	quota, err := store.Quotas().Quota(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to get quota: %v", err)
	}
	if quota.DailyRequestsUsed != 1 {
		t.Errorf("expected daily_requests_used 1, got %d", quota.DailyRequestsUsed)
	}
	if quota.MonthlyTokensUsed != 500 {
		t.Errorf("expected monthly_tokens_used 500 (committed actual usage), got %d", quota.MonthlyTokensUsed)
	}

	// Verify provider call telemetry was persisted on the committed usage record.
	rec := singleUsageRecord(t, store.Quotas(), defaultUserID)
	if rec.Status != core.ReservationStatusCommitted {
		t.Errorf("expected committed usage record, got status %q", rec.Status)
	}
	if rec.Provider != "openai" || rec.Model != "gpt-4o-mini" || rec.StatusCode != http.StatusOK || rec.Latency != 750*time.Millisecond {
		t.Errorf("expected committed telemetry openai/gpt-4o-mini/200/750ms, got %q/%q/%d/%v", rec.Provider, rec.Model, rec.StatusCode, rec.Latency)
	}
	if rec.PromptTokens != 120 || rec.CompletionTokens != 380 || rec.TotalTokens != 500 {
		t.Errorf("expected committed tokens 120/380/500, got %d/%d/%d", rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens)
	}
}

func TestSyncPDFParse_QuotaReleaseOnFailure(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "tenant-sync-failure"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	extractor := &mockUsageResumeExtractor{
		err: errors.New("upstream extraction failed: secret-upstream-detail"),
		telemetry: core.LLMTelemetry{
			Provider:   "anthropic",
			Model:      "claude-haiku",
			StatusCode: http.StatusBadGateway,
			Latency:    2 * time.Second,
			Usage:      core.TokenUsage{PromptTokens: 90},
		},
	}

	router := api.NewRouter(api.RouterConfig{
		QuotaStore:        store.Quotas(),
		DefaultUserID:     defaultUserID,
		Admin:             true,
		ResumeExtractor:   extractor,
		DailyRequestLimit: 50,
		MonthlyTokenLimit: 1000000,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	pdfBytes := buildTestPDF("Valid resume text that causes error")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "resume.pdf")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	_, _ = part.Write(pdfBytes)
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/profile/pdf/parse?sync=true", &buf)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error on extraction failure, got %d", resp.StatusCode)
	}
	if got := extractor.calls.Load(); got != 1 {
		t.Errorf("expected ResumeExtractor to be called once before failing, got %d", got)
	}

	// Verify quota was released: daily used should be refunded to 0, monthly tokens should be 0
	quota, err := store.Quotas().Quota(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to get quota: %v", err)
	}
	if quota.DailyRequestsUsed != 0 {
		t.Errorf("expected daily_requests_used 0 after release, got %d", quota.DailyRequestsUsed)
	}
	if quota.MonthlyTokensUsed != 0 {
		t.Errorf("expected monthly_tokens_used 0 after release, got %d", quota.MonthlyTokensUsed)
	}

	// Verify the release recorded upstream telemetry with a sanitized reason rather than raw error text.
	rec := singleUsageRecord(t, store.Quotas(), defaultUserID)
	if rec.Status != core.ReservationStatusReleased {
		t.Errorf("expected released usage record, got status %q", rec.Status)
	}
	if rec.Provider != "anthropic" || rec.Model != "claude-haiku" || rec.StatusCode != http.StatusBadGateway || rec.Latency != 2*time.Second {
		t.Errorf("expected released telemetry anthropic/claude-haiku/502/2s, got %q/%q/%d/%v", rec.Provider, rec.Model, rec.StatusCode, rec.Latency)
	}
	if rec.PromptTokens != 90 || rec.TotalTokens != 90 {
		t.Errorf("expected released prompt/total tokens 90/90, got %d/%d", rec.PromptTokens, rec.TotalTokens)
	}
	if rec.ErrorMessage != "resume extraction failed" {
		t.Errorf("expected sanitized release reason %q, got %q", "resume extraction failed", rec.ErrorMessage)
	}
}

func TestSyncPDFParse_NilExtractionResultReleasesQuota(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "tenant-sync-nil-result"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	extractor := &mockUsageResumeExtractor{
		telemetry: core.LLMTelemetry{Provider: "openai", Model: "gpt-4o-mini", StatusCode: http.StatusOK, Usage: core.TokenUsage{TotalTokens: 42}},
	}
	router := api.NewRouter(api.RouterConfig{
		QuotaStore:        store.Quotas(),
		DefaultUserID:     defaultUserID,
		Admin:             true,
		ResumeExtractor:   extractor,
		DailyRequestLimit: 50,
		MonthlyTokenLimit: 1000000,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "resume.pdf")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	_, _ = part.Write(buildTestPDF("Valid resume text that yields no structured result"))
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/profile/pdf/parse?sync=true", &buf)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway for empty extraction result, got %d", resp.StatusCode)
	}

	quota, err := store.Quotas().Quota(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to get quota: %v", err)
	}
	if quota.DailyRequestsUsed != 0 || quota.MonthlyTokensUsed != 0 {
		t.Errorf("expected quota fully released for empty result, got daily=%d monthly=%d", quota.DailyRequestsUsed, quota.MonthlyTokensUsed)
	}
	rec := singleUsageRecord(t, store.Quotas(), defaultUserID)
	if rec.Status != core.ReservationStatusReleased {
		t.Errorf("expected released usage record for empty result, got status %q", rec.Status)
	}
	if rec.Provider != "openai" || rec.TotalTokens != 42 {
		t.Errorf("expected released record to keep call telemetry openai/42 tokens, got %q/%d", rec.Provider, rec.TotalTokens)
	}
}

func TestGetQuota_UnauthorizedWhenNotAdmin(t *testing.T) {
	memStore := memory.New()
	prodRouter := api.NewRouter(api.RouterConfig{
		QuotaStore: memStore.Quotas(),
		Admin:      false,
	})
	server := httptest.NewServer(prodRouter)
	defer server.Close()

	for _, endpoint := range []string{"/api/v1/quota", "/quota"} {
		t.Run(endpoint, func(t *testing.T) {
			// Without credentials
			req, err := http.NewRequest(http.MethodGet, server.URL+endpoint, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected status 401 Unauthorized, got %d", resp.StatusCode)
			}
			var errResp api.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}
			if errResp.Error.Code != api.CodeUnauthorized {
				t.Errorf("expected error code %q, got %q", api.CodeUnauthorized, errResp.Error.Code)
			}

			// With invalid bearer token
			reqAuth, err := http.NewRequest(http.MethodGet, server.URL+endpoint, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			reqAuth.Header.Set("Authorization", "Bearer invalid-token")
			respAuth, err := server.Client().Do(reqAuth)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = respAuth.Body.Close() }()

			if respAuth.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected status 401 Unauthorized with invalid token, got %d", respAuth.StatusCode)
			}
			var errRespAuth api.ErrorResponse
			if err := json.NewDecoder(respAuth.Body).Decode(&errRespAuth); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}
			if errRespAuth.Error.Code != api.CodeUnauthorized {
				t.Errorf("expected error code %q, got %q", api.CodeUnauthorized, errRespAuth.Error.Code)
			}
		})
	}
}
