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

	"github.com/google/uuid"

	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/llm"
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

// AsyncPDFJobResponse represents the acknowledgement payload returned when an asynchronous PDF parse job is accepted.
type AsyncPDFJobResponse struct {
	JobID   string                 `json:"job_id"`
	Status  core.PDFParseJobStatus `json:"status"`
	Message string                 `json:"message"`
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

// parsePDFResume parses an uploaded candidate resume PDF. By default, it operates asynchronously,
// enqueuing the extraction task and returning 202 Accepted with a job ID for status polling.
// If the request explicitly supplies ?sync=true, it executes synchronously for backward compatibility.
func (h *profileHandler) parsePDFResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	upload, ok := h.readUploadedPDF(ctx, w, r)
	if !ok {
		return
	}

	userID := requestUserID(r, h.cfg.DefaultUserID)
	text, ok := h.extractUploadedPDFText(ctx, w, upload.filename, upload.data)
	if !ok {
		return
	}

	if h.cfg.ResumeExtractor == nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "LLM resume extractor not configured", "filename", upload.filename)
		}
		writeError(w, http.StatusInternalServerError, CodeInternalError, "LLM resume extractor not configured", "")
		return
	}

	// Run asynchronously by default when an enqueuer is available unless the caller explicitly requests ?sync=true.
	runAsync := strings.EqualFold(r.URL.Query().Get("async"), "true") ||
		(h.cfg.PDFJobEnqueuer != nil && !strings.EqualFold(r.URL.Query().Get("sync"), "true"))

	if !runAsync {
		h.extractPDFResumeSync(ctx, w, r, userID, upload, text)
		return
	}

	h.enqueueAsyncPDFJob(ctx, w, userID, upload, text)
}

// readUploadedPDF enforces the 10 MiB upload ceiling, validates multipart form fields, and returns raw PDF bytes.
func (h *profileHandler) readUploadedPDF(ctx context.Context, w http.ResponseWriter, r *http.Request) (pdfUploadPayload, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPDFUploadBytes)

	if err := r.ParseMultipartForm(maxPDFUploadBytes); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			if h.cfg.Logger != nil {
				h.cfg.Logger.WarnContext(ctx, "pdf upload exceeded maximum size limit", "limit_bytes", maxPDFUploadBytes, "error", err)
			}
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "file exceeds maximum allowed size (10MB)", "")
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
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			if h.cfg.Logger != nil {
				h.cfg.Logger.WarnContext(ctx, "pdf upload exceeded maximum size limit", "limit_bytes", maxPDFUploadBytes, "error", err)
			}
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "file exceeds maximum allowed size (10MB)", "")
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

// pdfQuotaLimits returns the effective daily request and monthly token quotas for PDF extraction.
func (h *profileHandler) pdfQuotaLimits() (int, int) {
	dailyLimit := h.cfg.DailyRequestLimit
	if dailyLimit <= 0 {
		dailyLimit = config.DefaultLLMDailyRequestLimit
	}
	monthlyLimit := h.cfg.MonthlyTokenLimit
	if monthlyLimit <= 0 {
		monthlyLimit = config.DefaultLLMMonthlyTokenLimit
	}
	return dailyLimit, monthlyLimit
}

// pdfParseTimeout returns the configured synchronous PDF LLM extraction timeout budget.
func (h *profileHandler) pdfParseTimeout() time.Duration {
	if h.cfg.PDFParseTimeout > 0 {
		return h.cfg.PDFParseTimeout
	}
	if h.cfg.LLMTimeout > 0 {
		return h.cfg.LLMTimeout
	}
	return config.DefaultPDFParseTimeout
}

// extractPDFResumeSync reserves quota and synchronously extracts structured JSON Resume data via LLM.
func (h *profileHandler) extractPDFResumeSync(ctx context.Context, w http.ResponseWriter, r *http.Request, userID string, upload pdfUploadPayload, text string) {
	dailyLimit, monthlyLimit := h.pdfQuotaLimits()
	reservationID, ok := h.reserveQuota(ctx, w, userID, core.OperationPDFParse, estimatedPDFParseTokens, dailyLimit, monthlyLimit)
	if !ok {
		return
	}

	parseTimeout := h.pdfParseTimeout()
	syncCtx, cancel := context.WithTimeout(r.Context(), parseTimeout)
	defer cancel()

	extracted, usage, err := llm.ExtractResumeWithTokenUsage(syncCtx, h.cfg.ResumeExtractor, text, upload.provider, upload.model, estimatedPDFParseTokens)
	if err != nil {
		if h.cfg.QuotaStore != nil && reservationID != "" {
			_ = h.cfg.QuotaStore.ReleaseQuota(ctx, reservationID, err.Error())
		}
		h.writePDFExtractionError(ctx, w, upload.provider, parseTimeout, err)
		return
	}

	if h.cfg.QuotaStore != nil && reservationID != "" {
		_ = h.cfg.QuotaStore.CommitUsage(ctx, reservationID, usage)
	}

	if h.cfg.Logger != nil {
		h.cfg.Logger.InfoContext(ctx, "successfully parsed pdf resume synchronously", "filename", upload.filename, "provider", upload.provider, "model", upload.model)
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

// enqueueAsyncPDFJob persists a pending PDF parse job and enqueues it onto the background PDF worker pool.
func (h *profileHandler) enqueueAsyncPDFJob(ctx context.Context, w http.ResponseWriter, userID string, upload pdfUploadPayload, text string) {
	if h.cfg.PDFJobStore == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "PDF job store not configured", "")
		return
	}

	if h.cfg.PDFJobEnqueuer == nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "PDF worker queue not configured on server", "job_id", "")
		}
		writeError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "PDF worker queue not configured on server", "")
		return
	}

	dailyLimit, monthlyLimit := h.pdfQuotaLimits()
	reservationID, ok := h.reserveQuota(ctx, w, userID, core.OperationPDFParse, estimatedPDFParseTokens, dailyLimit, monthlyLimit)
	if !ok {
		return
	}

	jobID := "pdf-job-" + uuid.New().String()
	now := time.Now().UTC()
	job := &core.PDFParseJob{
		ID:        jobID,
		UserID:    userID,
		Status:    core.PDFParseJobStatusPending,
		Filename:  upload.filename,
		Provider:  upload.provider,
		Model:     upload.model,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.cfg.PDFJobStore.CreateJob(ctx, job); err != nil {
		if h.cfg.QuotaStore != nil && reservationID != "" {
			_ = h.cfg.QuotaStore.ReleaseQuota(ctx, reservationID, "failed to create PDF job")
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	task := core.PDFParseJobTask{
		JobID:         jobID,
		UserID:        userID,
		RawText:       text,
		Provider:      upload.provider,
		Model:         upload.model,
		ReservationID: reservationID,
	}

	if err := h.cfg.PDFJobEnqueuer.EnqueuePDFJob(task); err != nil {
		if h.cfg.QuotaStore != nil && reservationID != "" {
			_ = h.cfg.QuotaStore.ReleaseQuota(ctx, reservationID, "failed to enqueue PDF job")
		}
		if errors.Is(err, core.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	if h.cfg.Logger != nil {
		h.cfg.Logger.InfoContext(ctx, "enqueued pdf resume parse job", "job_id", jobID, "filename", upload.filename)
	}

	writeJSON(w, http.StatusAccepted, AsyncPDFJobResponse{
		JobID:   jobID,
		Status:  core.PDFParseJobStatusPending,
		Message: "PDF parsing job queued",
	})
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

	if h.cfg.PDFJobStore == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "PDF job store not configured", "")
		return
	}

	job, err := h.cfg.PDFJobStore.Job(ctx, userID, jobID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "pdf parse job not found", "")
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// commitPDFResume commits approved JSON Resume data into the user's main career graph.
func (h *profileHandler) commitPDFResume(w http.ResponseWriter, r *http.Request) {
	h.handleJSONResumeImport(w, r, "empty request body", "empty or unreadable payload")
}
