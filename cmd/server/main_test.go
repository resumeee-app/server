package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"resumeee/internal/domain"
	"resumeee/internal/repository/sqlite"
)

func TestRunGracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	t.Setenv("DB_PATH", dbPath)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx)
	}()

	// Allow run to initialize, auto-migrate, seed, and wait on ctx.Done()
	time.Sleep(100 * time.Millisecond)

	// Trigger cancellation
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean shutdown without error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}

	// Verify database was initialized and seeded
	db, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to open database after run: %v", err)
	}

	profileRepo := sqlite.NewProfileRepository(db)
	prof, err := profileRepo.GetProfile(context.Background(), "default")
	if err != nil {
		t.Fatalf("expected default profile to be seeded, got: %v", err)
	}
	if prof.UserID != "default" {
		t.Errorf("expected UserID 'default', got %s", prof.UserID)
	}
}

func TestRun_RecoversInterruptedVersions(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "recover.db")
	t.Setenv("DB_PATH", dbPath)

	// Pre-create database with an in-flight branch version
	db, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}

	branchRepo := sqlite.NewBranchRepository(db)
	branch := &domain.Branch{
		ID:            "br-recover",
		UserID:        "default",
		Name:          "Pending Branch",
		TemplateSetID: "tmpl-1",
	}
	if err := branchRepo.CreateBranch(context.Background(), branch); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	ver := &domain.BranchVersion{
		ID:       "ver-interrupted",
		BranchID: branch.ID,
		Status:   domain.VersionStatusProcessing,
	}
	if err := branchRepo.CreateVersion(context.Background(), ver); err != nil {
		t.Fatalf("failed to create version: %v", err)
	}
	// Force status to processing
	_ = branchRepo.UpdateVersionStatus(context.Background(), ver.ID, domain.VersionStatusProcessing, "")

	// Now run the server boot sequence
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}

	// Verify interrupted version was marked failed
	recoveredVer, err := branchRepo.GetVersion(context.Background(), branch.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to get version: %v", err)
	}
	if recoveredVer.Status != domain.VersionStatusFailed {
		t.Errorf("expected version to be transitioned to failed on boot, got: %s", recoveredVer.Status)
	}
}
