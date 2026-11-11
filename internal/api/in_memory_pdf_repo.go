package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/resumeee-app/resumeee-core/domain"
)

// inMemoryPDFJobRepository provides a concurrent in-memory implementation of domain.PDFParseJobRepository.
type inMemoryPDFJobRepository struct {
	mu   sync.RWMutex
	jobs map[string]*domain.PDFParseJob
}

// NewInMemoryPDFJobRepository constructs a thread-safe in-memory PDFParseJobRepository.
func NewInMemoryPDFJobRepository() domain.PDFParseJobRepository {
	return &inMemoryPDFJobRepository{
		jobs: make(map[string]*domain.PDFParseJob),
	}
}

// CreateJob persists a new PDFParseJob in the in-memory map.
func (r *inMemoryPDFJobRepository) CreateJob(ctx context.Context, job *domain.PDFParseJob) error {
	if job == nil || job.ID == "" || job.UserID == "" {
		return fmt.Errorf("%w: job id and user_id are required", domain.ErrValidation)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now

	cloned := *job
	r.jobs[job.ID] = &cloned
	return nil
}

// GetJob retrieves a PDFParseJob by its unique ID scoped to the authenticated user ID.
func (r *inMemoryPDFJobRepository) GetJob(ctx context.Context, userID, jobID string) (*domain.PDFParseJob, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user id cannot be empty", domain.ErrValidation)
	}
	if jobID == "" {
		return nil, fmt.Errorf("%w: job id cannot be empty", domain.ErrValidation)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	job, ok := r.jobs[jobID]
	if !ok || job.UserID != userID {
		return nil, domain.ErrNotFound
	}

	cloned := *job
	return &cloned, nil
}

// UpdateJobStatus updates the status, candidate result, and diagnostic error message of a parse job.
func (r *inMemoryPDFJobRepository) UpdateJobStatus(ctx context.Context, jobID string, status domain.PDFParseJobStatus, result any, errMsg string) error {
	if jobID == "" {
		return fmt.Errorf("%w: job id cannot be empty", domain.ErrValidation)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	job, ok := r.jobs[jobID]
	if !ok {
		return domain.ErrNotFound
	}

	now := time.Now().UTC()
	job.Status = status
	job.UpdatedAt = now
	if errMsg != "" {
		job.ErrorMessage = errMsg
	}
	if result != nil {
		job.Result = result
	}
	if status == domain.PDFParseJobStatusCompleted || status == domain.PDFParseJobStatusFailed {
		job.CompletedAt = &now
	}

	return nil
}

// FailInterruptedJobs marks all pending and processing jobs as failed with the given reason on server recovery.
func (r *inMemoryPDFJobRepository) FailInterruptedJobs(ctx context.Context, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	for _, job := range r.jobs {
		if job.Status == domain.PDFParseJobStatusPending || job.Status == domain.PDFParseJobStatusProcessing {
			job.Status = domain.PDFParseJobStatusFailed
			job.ErrorMessage = reason
			job.UpdatedAt = now
			job.CompletedAt = &now
		}
	}
	return nil
}

// CleanupCompletedJobs purges completed or failed PDF parse jobs older than the retention threshold.
func (r *inMemoryPDFJobRepository) CleanupCompletedJobs(ctx context.Context, userID string, olderThan time.Duration) error {
	if userID == "" {
		return fmt.Errorf("%w: user id cannot be empty", domain.ErrValidation)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().UTC().Add(-olderThan)
	for id, job := range r.jobs {
		if job.UserID == userID && (job.Status == domain.PDFParseJobStatusCompleted || job.Status == domain.PDFParseJobStatusFailed) {
			if (job.CompletedAt != nil && job.CompletedAt.Before(cutoff)) || (job.CompletedAt == nil && job.UpdatedAt.Before(cutoff)) {
				delete(r.jobs, id)
			}
		}
	}
	return nil
}

