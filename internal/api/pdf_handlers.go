package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/config"
	core "github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/worker"
)

// PDF ingestion size, token quota, and retry defaults.
const (
	// maxPDFUploadBytes caps multipart PDF upload request bodies at 10 MiB.
	maxPDFUploadBytes = 10 << 20

	// estimatedPDFParseTokens is the token reservation estimate used prior to LLM extraction.
	estimatedPDFParseTokens = 1000

	// defaultPDFQuotaRetryAfterSeconds is returned in Retry-After headers when quota is exhausted.
	defaultPDFQuotaRetryAfterSeconds = 3600
)

// PDFParseJobResponse represents the public API projection of an asynchronous PDF parse job.
type PDFParseJobResponse struct {
	ID           string           `json:"id"`
	UserID       string           `json:"user_id"`
	Status       core.JobStatus   `json:"status"`
	Filename     string           `json:"filename,omitempty"`
	Provider     string           `json:"provider,omitempty"`
	Model        string           `json:"model,omitempty"`
	Result       *core.JSONResume `json:"result,omitempty"`
	ErrorMessage string           `json:"error_message,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	CompletedAt  *time.Time       `json:"completed_at,omitempty"`
}

// newPDFParseJobResponse projects a persistent Job onto PDFParseJobResponse when it represents a PDF parse workload.
func newPDFParseJobResponse(job *core.Job) (*PDFParseJobResponse, bool) {
	if job == nil || job.Kind != core.JobKindPDFParse || job.PDF == nil {
		return nil, false
	}
	return &PDFParseJobResponse{
		ID:           job.ID,
		UserID:       job.UserID,
		Status:       job.Status,
		Filename:     job.PDF.Filename,
		Provider:     job.PDF.Provider,
		Model:        job.PDF.Model,
		Result:       job.PDF.Result,
		ErrorMessage: job.ErrorMessage,
		CreatedAt:    job.CreatedAt,
		UpdatedAt:    job.UpdatedAt,
		CompletedAt:  job.CompletedAt,
	}, true
}

// pdfUploadPayload captures the decoded bytes and optional form overrides of a multipart PDF upload.
type pdfUploadPayload struct {
	filename string
	data     []byte
	provider string
	model    string
}

// reserveQuota reserves quota for an LLM operation, writing error responses directly and returning (reservationID, ok).
func (h *profileHandler) reserveQuota(ctx context.Context, w http.ResponseWriter, userID string, op core.LLMOperationType, estimatedTokens, dailyLimit, monthlyLimit int) (string, bool) {
	if h.cfg.QuotaStore == nil {
		return "", true
	}
	rec, qErr := h.cfg.QuotaStore.ReserveQuota(ctx, userID, op, estimatedTokens, dailyLimit, monthlyLimit)
	if qErr != nil {
		if errors.Is(qErr, core.ErrQuotaExceeded) {
			writeQuotaError(w, defaultPDFQuotaRetryAfterSeconds, "llm operation quota exceeded", qErr.Error())
			return "", false
		}
		writeDomainError(w, h.cfg.Logger, qErr)
		return "", false
	}
	return rec.ID, true
}

// restTimeout returns the configured standard REST timeout budget, falling back to config.DefaultRESTTimeout.
func (h *profileHandler) restTimeout() time.Duration {
	if h.cfg.RESTTimeout > 0 {
		return h.cfg.RESTTimeout
	}
	return config.DefaultRESTTimeout
}

// withRESTTimeout derives a child context bounded by restTimeout for fast non-LLM PDF stages.
func (h *profileHandler) withRESTTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, h.restTimeout())
}

// releasePDFQuota refunds a pending PDF parse reservation while recording any upstream call telemetry.
// It is a no-op when quota accounting is disabled or no reservation was taken.
func (h *profileHandler) releasePDFQuota(parent context.Context, reservationID, reason string, telemetry core.LLMTelemetry) {
	if h.cfg.QuotaStore == nil || reservationID == "" {
		return
	}
	releaseCtx, cancel := h.withRESTTimeout(parent)
	defer cancel()
	if err := h.cfg.QuotaStore.ReleaseQuota(releaseCtx, reservationID, reason, telemetry); err != nil && h.cfg.Logger != nil {
		h.cfg.Logger.ErrorContext(parent, "failed to release pdf parse quota", "reservation_id", reservationID, "error", err)
	}
}

// pdfExtractionReleaseReason maps an extraction failure to a sanitized reason persisted on the usage record,
// so raw upstream or validation error text never reaches quota storage.
func pdfExtractionReleaseReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "resume extraction timed out"
	case errors.Is(err, core.ErrValidation):
		return "document validation failed during extraction"
	default:
		return "resume extraction failed"
	}
}

// parsePDFResume parses an uploaded candidate resume PDF. By default, it operates asynchronously,
// enqueuing the extraction task and returning 202 Accepted with a job ID for status polling.
// If the request explicitly supplies ?sync=true, it executes synchronously for backward compatibility.
func (h *profileHandler) parsePDFResume(w http.ResponseWriter, r *http.Request) {
	reqCtx := r.Context()

	upload, ok := h.readUploadedPDF(reqCtx, w, r)
	if !ok {
		return
	}

	userID := requestUserID(r, h.cfg.DefaultUserID)
	extractCtx, cancelExtract := h.withRESTTimeout(reqCtx)
	text, ok := h.extractUploadedPDFText(extractCtx, w, upload.filename, upload.data)
	cancelExtract()
	if !ok {
		return
	}

	if h.cfg.ResumeExtractor == nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(reqCtx, "LLM resume extractor not configured", "filename", upload.filename)
		}
		writeError(w, http.StatusInternalServerError, CodeInternalError, "LLM resume extractor not configured", "")
		return
	}

	// Run asynchronously by default when a job notifier is available unless the caller explicitly requests ?sync=true.
	runAsync := strings.EqualFold(r.URL.Query().Get("async"), "true") ||
		(h.cfg.JobNotifier != nil && !strings.EqualFold(r.URL.Query().Get("sync"), "true"))

	if !runAsync {
		h.extractPDFResumeSync(reqCtx, w, userID, upload, text)
		return
	}

	asyncCtx, cancelAsync := h.withRESTTimeout(reqCtx)
	defer cancelAsync()
	h.enqueueAsyncPDFJob(asyncCtx, w, userID, upload, text)
}

// readUploadedPDF enforces the 10 MiB upload ceiling, validates multipart form fields, and returns raw PDF bytes.
func (h *profileHandler) readUploadedPDF(ctx context.Context, w http.ResponseWriter, r *http.Request) (pdfUploadPayload, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPDFUploadBytes)

	if err := r.ParseMultipartForm(maxPDFUploadBytes); err != nil {
		if h.writeMaxPDFBytesError(ctx, w, err) {
			return pdfUploadPayload{}, false
		}
		if h.cfg.Logger != nil {
			h.cfg.Logger.WarnContext(ctx, "failed to parse multipart form for pdf upload", "error", err)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "file exceeds maximum allowed size (10MB) or invalid form data", err.Error())
		return pdfUploadPayload{}, false
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.WarnContext(ctx, "missing required 'file' form field", "error", err)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "missing required 'file' form field", err.Error())
		return pdfUploadPayload{}, false
	}
	defer func() { _ = file.Close() }()

	if fileHeader.Size <= 0 {
		if h.cfg.Logger != nil {
			h.cfg.Logger.WarnContext(ctx, "empty file uploaded", "filename", fileHeader.Filename)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty file uploaded", "")
		return pdfUploadPayload{}, false
	}

	data, err := io.ReadAll(file)
	if err != nil {
		if h.writeMaxPDFBytesError(ctx, w, err) {
			return pdfUploadPayload{}, false
		}
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "failed to read uploaded file", "filename", fileHeader.Filename, "error", err)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "failed to read uploaded file", err.Error())
		return pdfUploadPayload{}, false
	}

	return pdfUploadPayload{
		filename: fileHeader.Filename,
		data:     data,
		provider: strings.TrimSpace(r.FormValue("provider")),
		model:    strings.TrimSpace(r.FormValue("model")),
	}, true
}

// writeMaxPDFBytesError reports whether err is a *http.MaxBytesError, logging and writing a 413 response if so.
func (h *profileHandler) writeMaxPDFBytesError(ctx context.Context, w http.ResponseWriter, err error) bool {
	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) {
		return false
	}
	if h.cfg.Logger != nil {
		h.cfg.Logger.WarnContext(ctx, "pdf upload exceeded maximum size limit", "limit_bytes", maxPDFUploadBytes, "error", err)
	}
	writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "file exceeds maximum allowed size (10MB)", "")
	return true
}

// extractUploadedPDFText extracts plain text from uploaded PDF bytes using the configured PDFExtractor.
func (h *profileHandler) extractUploadedPDFText(ctx context.Context, w http.ResponseWriter, filename string, data []byte) (string, bool) {
	if h.cfg.PDFExtractor == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "PDF extractor unavailable", "")
		return "", false
	}

	text, err := h.cfg.PDFExtractor.ExtractText(ctx, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if errors.Is(err, core.ErrValidation) {
			if h.cfg.Logger != nil {
				h.cfg.Logger.WarnContext(ctx, "pdf text extraction validation failed", "filename", filename, "error", err)
			}
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return "", false
		}
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "unexpected pdf extraction error", "filename", filename, "error", err)
		}
		writeDomainError(w, h.cfg.Logger, err)
		return "", false
	}
	return text, true
}

// pdfParseTimeout returns the configured synchronous PDF LLM extraction timeout budget.
func (h *profileHandler) pdfParseTimeout() time.Duration {
	if h.cfg.PDFParseTimeout > 0 {
		return h.cfg.PDFParseTimeout
	}
	return config.DefaultPDFParseTimeout
}

// extractPDFResumeSync reserves quota and synchronously extracts structured JSON Resume data via LLM.
func (h *profileHandler) extractPDFResumeSync(reqCtx context.Context, w http.ResponseWriter, userID string, upload pdfUploadPayload, text string) {
	dailyLimit, monthlyLimit := resolveQuotaLimits(h.cfg.DailyRequestLimit, h.cfg.MonthlyTokenLimit)
	reserveCtx, cancelReserve := h.withRESTTimeout(reqCtx)
	reservationID, ok := h.reserveQuota(reserveCtx, w, userID, core.OperationPDFParse, estimatedPDFParseTokens, dailyLimit, monthlyLimit)
	cancelReserve()
	if !ok {
		return
	}

	parseTimeout := h.pdfParseTimeout()
	extendResponseWriteDeadline(w, parseTimeout, h.cfg.HTTPWriteTimeoutBuffer)
	syncCtx, cancel := context.WithTimeout(reqCtx, parseTimeout)
	defer cancel()

	extracted, telemetry, err := llm.ExtractResumeWithTelemetry(syncCtx, h.cfg.ResumeExtractor, text, upload.provider, upload.model, estimatedPDFParseTokens)
	if err == nil && extracted == nil {
		// Guard against extractors returning (nil, nil) so quota is never committed for an empty result.
		err = fmt.Errorf("%w: resume extractor returned no result", llm.ErrInvalidResponse)
	}
	if err != nil {
		h.releasePDFQuota(reqCtx, reservationID, pdfExtractionReleaseReason(err), telemetry)
		h.writePDFExtractionError(reqCtx, w, upload.provider, parseTimeout, err)
		return
	}

	if h.cfg.QuotaStore != nil && reservationID != "" {
		commitCtx, cancelCommit := h.withRESTTimeout(reqCtx)
		_ = h.cfg.QuotaStore.CommitUsage(commitCtx, reservationID, telemetry)
		cancelCommit()
	}

	if h.cfg.Logger != nil {
		h.cfg.Logger.InfoContext(reqCtx, "successfully parsed pdf resume synchronously", "filename", upload.filename, "provider", upload.provider, "model", upload.model)
	}
	writeJSON(w, http.StatusOK, extracted)
}

// writePDFExtractionError maps synchronous LLM resume extraction failures onto HTTP status codes and structured logs.
func (h *profileHandler) writePDFExtractionError(ctx context.Context, w http.ResponseWriter, provider string, parseTimeout time.Duration, err error) {
	logLevel := slog.LevelError
	var (
		logMsg     string
		statusCode int
		errorCode  string
		respMsg    string
	)

	switch {
	case errors.Is(err, llm.ErrNoProviders):
		logMsg = "no LLM provider configured on server"
		statusCode = http.StatusServiceUnavailable
		errorCode = CodeServiceUnavailable
	case errors.Is(err, llm.ErrUnauthorized):
		logMsg = "upstream LLM provider authentication failed"
		statusCode = http.StatusBadGateway
		errorCode = CodeUpstreamError
	case errors.Is(err, llm.ErrRateLimited):
		logMsg = "upstream LLM provider rate limit exceeded"
		statusCode = http.StatusTooManyRequests
		errorCode = CodeUpstreamError
		respMsg = "upstream LLM provider rate limit exceeded; retry shortly"
	case errors.Is(err, llm.ErrUpstream):
		logMsg = "upstream LLM provider returned server error"
		statusCode = http.StatusBadGateway
		errorCode = CodeUpstreamError
	case errors.Is(err, llm.ErrInvalidResponse):
		logMsg = "upstream LLM provider returned invalid response"
		statusCode = http.StatusBadGateway
		errorCode = CodeUpstreamError
	case errors.Is(err, llm.ErrProviderNotFound):
		logLevel = slog.LevelWarn
		logMsg = "requested LLM provider not configured"
		statusCode = http.StatusBadRequest
		errorCode = CodeInvalidRequest
	case errors.Is(err, context.DeadlineExceeded):
		logMsg = "upstream LLM request timed out"
		statusCode = http.StatusGatewayTimeout
		errorCode = CodeUpstreamError
		respMsg = fmt.Sprintf("upstream LLM parsing timed out after %s", parseTimeout)
	case errors.Is(err, core.ErrValidation):
		logLevel = slog.LevelWarn
		logMsg = "failed to extract structured resume validation error"
		statusCode = http.StatusBadRequest
		errorCode = CodeInvalidRequest
		respMsg = "failed to extract structured resume: " + err.Error()
	default:
		logMsg = "unexpected error during resume extraction"
	}

	if respMsg == "" {
		respMsg = logMsg
	}

	if h.cfg.Logger != nil {
		h.cfg.Logger.Log(ctx, logLevel, logMsg, "error", err, "provider", provider)
	}

	if statusCode > 0 {
		writeError(w, statusCode, errorCode, respMsg, "")
		return
	}
	writeDomainError(w, h.cfg.Logger, err)
}

// enqueueAsyncPDFJob persists a pending PDF parse job and signals background workers.
func (h *profileHandler) enqueueAsyncPDFJob(ctx context.Context, w http.ResponseWriter, userID string, upload pdfUploadPayload, text string) {
	if h.cfg.JobStore == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "pdf job store unavailable", "")
		return
	}

	if h.cfg.JobNotifier == nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "PDF worker queue not configured on server")
		}
		writeError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "PDF worker queue not configured on server", "")
		return
	}

	job, err := worker.SubmitPDFJob(ctx, worker.PDFSubmitConfig{
		Jobs:                h.cfg.JobStore,
		Quotas:              h.cfg.QuotaStore,
		Notifier:            h.cfg.JobNotifier,
		MaxPendingPerTenant: h.cfg.MaxPendingPerTenant,
	}, worker.PDFJobSubmission{
		UserID:       userID,
		Filename:     upload.filename,
		Provider:     upload.provider,
		Model:        upload.model,
		RawText:      text,
		DailyLimit:   h.cfg.DailyRequestLimit,
		MonthlyLimit: h.cfg.MonthlyTokenLimit,
	})
	if err != nil {
		if errors.Is(err, core.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	resp, ok := newPDFParseJobResponse(job)
	if !ok {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "unexpected pdf parse job payload", "")
		return
	}

	if h.cfg.Logger != nil {
		h.cfg.Logger.InfoContext(ctx, "enqueued pdf resume parse job", "job_id", job.ID, "filename", upload.filename)
	}

	writeJSON(w, http.StatusAccepted, resp)
}

// getPDFParseJob returns the status and candidate result of an asynchronous PDF resume parsing job.
func (h *profileHandler) getPDFParseJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	jobID := r.PathValue("id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "job id is required", "")
		return
	}

	job, err := h.cfg.JobStore.Job(ctx, userID, jobID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "pdf parse job not found", "")
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	resp, ok := newPDFParseJobResponse(job)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "pdf parse job not found", "")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// commitPDFResume commits approved JSON Resume data into the user's main career graph.
func (h *profileHandler) commitPDFResume(w http.ResponseWriter, r *http.Request) {
	h.handleJSONResumeImport(w, r, "empty request body", "empty or unreadable payload")
}
