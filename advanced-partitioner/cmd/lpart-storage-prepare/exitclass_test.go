package main

import (
	"context"
	"errors"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// A protected-disk barrier refusal from cleanup is a safety refusal (exit 2),
// not a retryable "cleanup: ..." exit 1 that re-runs every 30 s for ever.
func TestCleanupBarrierRefusalIsSafetyRefusal(t *testing.T) {
	// Drive the real barrier: a selected disk that is protected.
	c := cleanup.New(cleanup.Options{Runner: executor.NewFakeRunner(executor.FakeResponse{}), CommandAvailable: func(string) bool { return true }})
	_, err := c.CleanupSelectedSet(context.Background(), cleanup.Set{
		Name: "os", SelectedDisks: []string{"/dev/sdb"},
		Protected: cleanup.SelectedDisksSet([]string{"/dev/sdb"}),
	})
	if !errors.Is(err, cleanup.ErrProtected) {
		t.Fatalf("barrier must refuse with ErrProtected, got %v", err)
	}
	var se *safetyError
	if !errors.As(cleanupError("os", err), &se) || se.reason != "protected-disk" {
		t.Fatalf("barrier refusal must map to safety_refusal=protected-disk, got %v", cleanupError("os", err))
	}
	var se2 *safetyError
	if errors.As(cleanupError("os", errors.New("boom")), &se2) {
		t.Fatal("an ordinary cleanup failure stays retryable")
	}
}
