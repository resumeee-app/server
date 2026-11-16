package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/memory"
	"github.com/resumeee-app/core/store"
	"github.com/resumeee-app/resumeee-api/internal/config"
)

func TestCLI_Run_StdioHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Prepare JSON-RPC initialize request
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}
	initBytes, err := json.Marshal(initReq)
	if err != nil {
		t.Fatalf("failed to marshal init request: %v", err)
	}

	inBuf := bytes.NewBuffer(append(initBytes, '\n'))
	outBuf := &bytes.Buffer{}

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, inBuf, outBuf, "-db", ":memory:", "-user-id", "test-user")
	}()

	// Wait for response or timeout
	select {
	case err := <-runErr:
		// Stdio server terminates with nil or context.Canceled when inBuf EOFs or ctx expires
		if err != nil && err != context.Canceled {
			t.Logf("run returned: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		if drainErr := <-runErr; drainErr != nil && drainErr != context.Canceled {
			t.Logf("run error during drain: %v", drainErr)
		}
	}

	outBytes := bytes.TrimSpace(outBuf.Bytes())
	var rpcResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(outBytes, &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal JSON-RPC initialize response: %v, raw: %s", err, string(outBytes))
	}
	if rpcResp.Result.ServerInfo.Name != "Resumeee Career & Resume Engine" {
		t.Errorf("expected server name 'Resumeee Career & Resume Engine', got: %q", rpcResp.Result.ServerInfo.Name)
	}
}

func TestCLI_Run_InvalidFlag(t *testing.T) {
	ctx := context.Background()
	inBuf := bytes.NewBuffer(nil)
	outBuf := &bytes.Buffer{}

	err := run(ctx, inBuf, outBuf, "-nonexistent-flag-xyz")
	if err == nil {
		t.Fatal("expected error on nonexistent flag")
	}
}

func TestCLI_RunWithStore_MemoryBackend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	memStore, err := memory.NewSeeded(ctx)
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}

	storeInit := func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
		return memStore, nil
	}

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}
	initBytes, err := json.Marshal(initReq)
	if err != nil {
		t.Fatalf("failed to marshal init request: %v", err)
	}

	inBuf := bytes.NewBuffer(append(initBytes, '\n'))
	outBuf := &bytes.Buffer{}

	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithStore(ctx, storeInit, inBuf, outBuf)
	}()

	select {
	case err := <-runErr:
		if err != nil && err != context.Canceled {
			t.Logf("run returned: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		if drainErr := <-runErr; drainErr != nil && drainErr != context.Canceled {
			t.Logf("run error during drain: %v", drainErr)
		}
	}

	outBytes := bytes.TrimSpace(outBuf.Bytes())
	var rpcResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(outBytes, &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal JSON-RPC initialize response: %v, raw: %s", err, string(outBytes))
	}
	if rpcResp.Result.ServerInfo.Name != "Resumeee Career & Resume Engine" {
		t.Errorf("expected server name 'Resumeee Career & Resume Engine', got: %q", rpcResp.Result.ServerInfo.Name)
	}
}

func TestCLI_Run_AdminFlags(t *testing.T) {
	tests := []struct {
		name         string
		envAdminMode string
		args         []string
		expected     bool
	}{
		{
			name:         "flag -admin-mode=true enables admin mode",
			envAdminMode: "false",
			args:         []string{"-admin-mode=true"},
			expected:     true,
		},
		{
			name:         "flag -admin-mode shorthand enables admin mode",
			envAdminMode: "false",
			args:         []string{"-admin-mode"},
			expected:     true,
		},
		{
			name:         "flag -admin-mode=false overrides ADMIN_MODE=true",
			envAdminMode: "true",
			args:         []string{"-admin-mode=false"},
			expected:     false,
		},
		{
			name:         "flag -admin=false overrides ADMIN_MODE=true",
			envAdminMode: "true",
			args:         []string{"-admin=false"},
			expected:     false,
		},
		{
			name:         "flag -admin shorthand enables admin mode",
			envAdminMode: "false",
			args:         []string{"-admin"},
			expected:     true,
		},
		{
			name:         "inherits ADMIN_MODE=true when no flag provided",
			envAdminMode: "true",
			args:         nil,
			expected:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_MODE", tc.envAdminMode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var capturedAdmin bool
			storeInit := func(_ context.Context, cfg *config.Config, _ *slog.Logger) (*store.Store, error) {
				capturedAdmin = cfg.Admin
				cancel()
				return memory.NewSeeded(context.Background())
			}

			inBuf := bytes.NewBuffer(nil)
			outBuf := &bytes.Buffer{}

			_ = runWithStore(ctx, storeInit, inBuf, outBuf, tc.args...)

			if capturedAdmin != tc.expected {
				t.Errorf("expected cfg.Admin=%v, got %v", tc.expected, capturedAdmin)
			}
		})
	}
}

func TestCLI_Run_ConfigFlag(t *testing.T) {
	t.Run("loads environment overrides from -config file", func(t *testing.T) {
		envPath := filepath.Join(t.TempDir(), "mcp.env")
		content := "DEFAULT_USER_ID=custom-cfg-user\nADMIN_MODE=true\nREST_TIMEOUT=45s\nMCP_SSE_KEEPALIVE_INTERVAL=25s\n"
		if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
			t.Fatalf("write config file: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var captured *config.Config
		storeInit := func(_ context.Context, cfg *config.Config, _ *slog.Logger) (*store.Store, error) {
			captured = cfg
			cancel()
			return memory.NewSeeded(context.Background())
		}

		_ = runWithStore(ctx, storeInit, bytes.NewBuffer(nil), &bytes.Buffer{}, "-config", envPath)

		if captured == nil {
			t.Fatal("expected storeInit to receive loaded config")
		}
		if captured.DefaultUserID != "custom-cfg-user" {
			t.Errorf("expected DefaultUserID 'custom-cfg-user', got %q", captured.DefaultUserID)
		}
		if !captured.Admin {
			t.Errorf("expected Admin=true from config file")
		}
		if captured.RESTTimeout != 45*time.Second {
			t.Errorf("expected RESTTimeout 45s, got %v", captured.RESTTimeout)
		}
		if captured.MCPSSEKeepAliveInterval != 25*time.Second {
			t.Errorf("expected MCPSSEKeepAliveInterval 25s, got %v", captured.MCPSSEKeepAliveInterval)
		}
	})

	t.Run("returns error when -config file does not exist", func(t *testing.T) {
		missingPath := filepath.Join(t.TempDir(), "nonexistent.env")
		err := runWithStore(context.Background(), func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
			t.Fatal("storeInit should not be called when -config file is missing")
			return nil, nil
		}, bytes.NewBuffer(nil), &bytes.Buffer{}, "-config", missingPath)
		if err == nil {
			t.Fatal("expected error for nonexistent -config path")
		}
	})

	t.Run("returns error on invalid environment config without -config", func(t *testing.T) {
		t.Setenv("REST_TIMEOUT", "not-a-duration")
		err := runWithStore(context.Background(), func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
			t.Fatal("storeInit should not be called when environment configuration is invalid")
			return nil, nil
		}, bytes.NewBuffer(nil), &bytes.Buffer{})
		if err == nil {
			t.Fatal("expected error for invalid REST_TIMEOUT without -config")
		}
	})
}

func TestCLI_Run_InvalidAuthProvider(t *testing.T) {
	tests := []struct {
		name              string
		authProvider      string
		firebaseProjectID string
	}{
		{
			name:         "unknown AUTH_PROVIDER fails fast",
			authProvider: "nonexistent-provider",
		},
		{
			name:              "firebase AUTH_PROVIDER without FIREBASE_PROJECT_ID fails fast",
			authProvider:      "firebase",
			firebaseProjectID: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AUTH_PROVIDER", tc.authProvider)
			t.Setenv("FIREBASE_PROJECT_ID", tc.firebaseProjectID)
			storeInit := func(ctx context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
				return memory.NewSeeded(ctx)
			}
			err := runWithStore(context.Background(), storeInit, bytes.NewBuffer(nil), &bytes.Buffer{})
			if err == nil {
				t.Fatalf("expected error for invalid AUTH_PROVIDER=%q", tc.authProvider)
			}
		})
	}
}

func TestCLI_RunWithStore_RecoversStaleJobsAndReleasesQuota(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	memStore, err := memory.NewSeeded(ctx)
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}

	br := &core.Branch{
		ID:            "br-mcp-recover",
		UserID:        "default",
		Name:          "MCP Interrupted Branch",
		TemplateSetID: core.TemplateStandardCleanID,
	}
	if err := memStore.Branches().CreateBranch(ctx, br); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	res, err := memStore.Quotas().ReserveQuota(ctx, "default", core.OperationResumeTailoring, 1500, 50, 1000000)
	if err != nil {
		t.Fatalf("failed to reserve quota: %v", err)
	}

	job := &core.Job{
		ID:            "job-mcp-interrupted",
		UserID:        "default",
		Kind:          core.JobKindBranchTailor,
		ReservationID: res.ID,
	}
	ver := &core.BranchVersion{
		ID:       "ver-mcp-interrupted",
		UserID:   "default",
		BranchID: br.ID,
	}
	if _, err := memStore.Jobs().SubmitBranchTailor(ctx, job, ver, core.PendingJobOptions{}); err != nil {
		t.Fatalf("failed to submit branch tailor job: %v", err)
	}
	claimed, err := memStore.Jobs().ClaimNext(ctx, core.ClaimJobRequest{
		WorkerID: "crashed-mcp-worker",
	})
	if err != nil || claimed == nil {
		t.Fatalf("failed to claim job before mcp boot: claimed=%v err=%v", claimed, err)
	}

	storeInit := func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
		return memStore, nil
	}

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}
	initBytes, err := json.Marshal(initReq)
	if err != nil {
		t.Fatalf("failed to marshal init request: %v", err)
	}

	if err := runWithStore(ctx, storeInit, bytes.NewBuffer(append(initBytes, '\n')), &bytes.Buffer{}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runWithStore returned unexpected error: %v", err)
	}

	recoveredJob, err := memStore.Jobs().Job(context.Background(), "default", job.ID)
	if err != nil {
		t.Fatalf("failed to load recovered job: %v", err)
	}
	if recoveredJob.Status != core.JobStatusFailed {
		t.Errorf("expected interrupted job status %s, got %s", core.JobStatusFailed, recoveredJob.Status)
	}
	if recoveredJob.ErrorMessage != "mcp server restarted during job processing" {
		t.Errorf("unexpected error message %q", recoveredJob.ErrorMessage)
	}

	recoveredVer, err := memStore.Branches().Version(context.Background(), br.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to load recovered version: %v", err)
	}
	if recoveredVer.Status != core.VersionStatusFailed {
		t.Errorf("expected interrupted version status %s, got %s", core.VersionStatusFailed, recoveredVer.Status)
	}

	quotaAfter, err := memStore.Quotas().Quota(context.Background(), "default")
	if err != nil {
		t.Fatalf("failed to load quota after recovery: %v", err)
	}
	if quotaAfter.DailyRequestsUsed != 0 || quotaAfter.MonthlyTokensUsed != 0 {
		t.Errorf("expected reserved quota released on startup recovery, got daily=%d monthly=%d",
			quotaAfter.DailyRequestsUsed, quotaAfter.MonthlyTokensUsed)
	}

	records, err := memStore.Quotas().ListUsageRecords(context.Background(), "default", 10)
	if err != nil {
		t.Fatalf("failed to list usage records: %v", err)
	}
	if len(records) != 1 || records[0].Status != core.ReservationStatusReleased {
		t.Errorf("expected reservation %s status %s, got %+v", res.ID, core.ReservationStatusReleased, records)
	}
}
