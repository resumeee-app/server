package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
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
