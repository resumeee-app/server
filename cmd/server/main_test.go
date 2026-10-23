package main

import (
	"context"
	"testing"
	"time"
)

func TestRunGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx)
	}()

	// Allow run to initialize and wait on ctx.Done()
	time.Sleep(50 * time.Millisecond)

	// Trigger cancellation
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean shutdown without error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}
}
