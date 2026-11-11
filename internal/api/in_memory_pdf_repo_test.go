package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/resumeee-app/resumeee-core/domain"
)

func TestInMemoryPDFJobRepository_Lifecycle(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryPDFJobRepository()

	t.Run("Create and Get valid job", func(t *testing.T) {
		job := &domain.PDFParseJob{
			ID:       "pdf-job-1",
			UserID:   "user-1",
			Status:   domain.PDFParseJobStatusPending,
			Filename: "resume.pdf",
		}
		if err := repo.CreateJob(ctx, job); err != nil {
			t.Fatalf("create failed: %v", err)
		}

		fetched, err := repo.GetJob(ctx, "user-1", "pdf-job-1")
		if err != nil {
			t.Fatalf("get failed: %v", err)
		}
		if fetched.ID != "pdf-job-1" || fetched.UserID != "user-1" {
			t.Errorf("unexpected job: %+v", fetched)
		}
		if fetched.Status != domain.PDFParseJobStatusPending {
			t.Errorf("expected pending, got %s", fetched.Status)
		}
	})

	t.Run("Create validation errors", func(t *testing.T) {
		if err := repo.CreateJob(ctx, nil); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("expected ErrValidation on nil job, got %v", err)
		}
		if err := repo.CreateJob(ctx, &domain.PDFParseJob{UserID: "u1"}); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("expected ErrValidation on missing ID, got %v", err)
		}
		if err := repo.CreateJob(ctx, &domain.PDFParseJob{ID: "j1"}); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("expected ErrValidation on missing UserID, got %v", err)
		}
	})

	t.Run("Get validation and isolation errors", func(t *testing.T) {
		if _, err := repo.GetJob(ctx, "", "pdf-job-1"); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("expected ErrValidation on empty userID, got %v", err)
		}
		if _, err := repo.GetJob(ctx, "user-1", ""); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("expected ErrValidation on empty jobID, got %v", err)
		}
		if _, err := repo.GetJob(ctx, "user-1", "non-existent"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected ErrNotFound for non-existent job, got %v", err)
		}
		// Cross-user isolation
		if _, err := repo.GetJob(ctx, "other-user", "pdf-job-1"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected ErrNotFound for mismatched user ID, got %v", err)
		}
	})

	t.Run("UpdateJobStatus transitions", func(t *testing.T) {
		// Update non-existent job returns ErrNotFound
		if err := repo.UpdateJobStatus(ctx, "unknown-id", domain.PDFParseJobStatusProcessing, nil, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected ErrNotFound on update non-existent, got %v", err)
		}

		// Update to processing
		if err := repo.UpdateJobStatus(ctx, "pdf-job-1", domain.PDFParseJobStatusProcessing, nil, ""); err != nil {
			t.Fatalf("update to processing failed: %v", err)
		}
		fetched, _ := repo.GetJob(ctx, "user-1", "pdf-job-1")
		if fetched.Status != domain.PDFParseJobStatusProcessing {
			t.Errorf("expected processing, got %s", fetched.Status)
		}

		// Update to completed with result
		sampleResult := map[string]any{"basics": map[string]any{"name": "Ada"}}
		if err := repo.UpdateJobStatus(ctx, "pdf-job-1", domain.PDFParseJobStatusCompleted, sampleResult, ""); err != nil {
			t.Fatalf("update to completed failed: %v", err)
		}
		fetched, _ = repo.GetJob(ctx, "user-1", "pdf-job-1")
		if fetched.Status != domain.PDFParseJobStatusCompleted {
			t.Errorf("expected completed, got %s", fetched.Status)
		}
		if fetched.CompletedAt == nil {
			t.Errorf("expected completed_at to be set")
		}
		if fetched.Result == nil {
			t.Errorf("expected result to be present")
		}
	})

	t.Run("FailInterruptedJobs transitions pending and processing", func(t *testing.T) {
		repoClean := NewInMemoryPDFJobRepository()
		_ = repoClean.CreateJob(ctx, &domain.PDFParseJob{ID: "j-pending", UserID: "u1", Status: domain.PDFParseJobStatusPending})
		_ = repoClean.CreateJob(ctx, &domain.PDFParseJob{ID: "j-processing", UserID: "u1", Status: domain.PDFParseJobStatusProcessing})
		_ = repoClean.CreateJob(ctx, &domain.PDFParseJob{ID: "j-completed", UserID: "u1", Status: domain.PDFParseJobStatusCompleted})

		if err := repoClean.FailInterruptedJobs(ctx, "server rebooted"); err != nil {
			t.Fatalf("FailInterruptedJobs failed: %v", err)
		}

		jPending, _ := repoClean.GetJob(ctx, "u1", "j-pending")
		if jPending.Status != domain.PDFParseJobStatusFailed || jPending.ErrorMessage != "server rebooted" {
			t.Errorf("expected j-pending to fail, got status %s, msg %q", jPending.Status, jPending.ErrorMessage)
		}

		jProcessing, _ := repoClean.GetJob(ctx, "u1", "j-processing")
		if jProcessing.Status != domain.PDFParseJobStatusFailed || jProcessing.ErrorMessage != "server rebooted" {
			t.Errorf("expected j-processing to fail, got status %s, msg %q", jProcessing.Status, jProcessing.ErrorMessage)
		}

		jCompleted, _ := repoClean.GetJob(ctx, "u1", "j-completed")
		if jCompleted.Status != domain.PDFParseJobStatusCompleted {
			t.Errorf("expected j-completed to remain completed, got status %s", jCompleted.Status)
		}
	})

	t.Run("Concurrent access safety", func(t *testing.T) {
		repoConc := NewInMemoryPDFJobRepository()
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				id := fmt.Sprintf("job-conc-%d", idx)
				_ = repoConc.CreateJob(ctx, &domain.PDFParseJob{ID: id, UserID: "user-conc", Status: domain.PDFParseJobStatusPending})
				_ = repoConc.UpdateJobStatus(ctx, id, domain.PDFParseJobStatusProcessing, nil, "")
				_, _ = repoConc.GetJob(ctx, "user-conc", id)
				_ = repoConc.UpdateJobStatus(ctx, id, domain.PDFParseJobStatusCompleted, map[string]any{"ok": true}, "")
			}(i)
		}
		wg.Wait()

		for i := 0; i < 20; i++ {
			id := fmt.Sprintf("job-conc-%d", i)
			j, err := repoConc.GetJob(ctx, "user-conc", id)
			if err != nil {
				t.Fatalf("expected job %s to exist: %v", id, err)
			}
			if j.Status != domain.PDFParseJobStatusCompleted {
				t.Errorf("expected job %s to be completed, got %s", id, j.Status)
			}
		}
	})
}
