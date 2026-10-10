package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSanitizeTimeout(t *testing.T) {
	floor := 2 * time.Hour
	if got := sanitizeTimeout(nil, floor); got != 0 {
		t.Fatalf("no sizes known: want no bound, got %s", got)
	}
	if got := sanitizeTimeout([]disk{{Size: 100 << 30}}, floor); got != floor {
		t.Fatalf("small disk: want the %s floor, got %s", floor, got)
	}
	// Two 2 TB HDDs at 50 MB/s: 80000 s, ~22 h -- far above the old 2 h bound.
	got := sanitizeTimeout([]disk{{Size: 2e12}, {Size: 2e12}}, floor)
	if got < 22*time.Hour || got > 23*time.Hour {
		t.Fatalf("2x2TB: got %s, want ~22h", got)
	}
}

// A cancelled phase kills the running step with its children and says so.
func TestCancelledStepIsKilledAndReported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := New(ctx, Options{Phase: PhasePart, LogDir: t.TempDir(), StepTimeout: time.Minute})
	go func() {
		for range p.Events() {
		}
	}()
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := p.runStepArgv("osimage", []string{"/bin/sh", "-c", "sleep 60 & wait"})
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("want an interrupted error, got %v", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("cancel took %s", d)
	}
}
