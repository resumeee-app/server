package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"resumeee/internal/config"
	"resumeee/internal/repository"
	"resumeee/internal/repository/memory"
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

	storeInit := func(_ context.Context, _ *config.Config, _ *slog.Logger) (repository.Store, error) {
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
			storeInit := func(_ context.Context, cfg *config.Config, _ *slog.Logger) (repository.Store, error) {
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
