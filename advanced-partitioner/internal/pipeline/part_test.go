package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBin writes an executable shell script standing in for an lpart binary.
func fakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// partEnv is a runtime/log/bin dir triple with install_mode set.
func partEnv(t *testing.T, mode string) (rt, logs, bin string) {
	t.Helper()
	root := t.TempDir()
	rt, logs, bin = filepath.Join(root, "runtime"), filepath.Join(root, "logs"), filepath.Join(root, "bin")
	for _, d := range []string{rt, logs, bin} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rt, "install-context.json"), []byte(`{"install_mode":"`+mode+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return rt, logs, bin
}

func runPartWith(t *testing.T, rt, logs, bin string) error {
	t.Helper()
	p := New(context.Background(), Options{Phase: PhasePart, RuntimeDir: rt, LogDir: logs, BinDir: bin, StepTimeout: time.Minute})
	go func() {
		for range p.Events() {
		}
	}()
	return p.runPart()
}

// A previous attempt's results do not survive into this one: the install
// tmpfs persists across retries, and sanitize writes no storage results.
func TestPartPhaseClearsStaleResults(t *testing.T) {
	rt, logs, bin := partEnv(t, "sanitize")
	stale := []string{"storage-result.json", "storage-resolved.json", "storage-check-result.json", SanitizeDoneMarker}
	for _, name := range stale {
		if err := os.WriteFile(filepath.Join(rt, name), []byte(`{"stale":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fakeBin(t, bin, "lpart-storage-prepare", `exit 0`)
	if err := runPartWith(t, rt, logs, bin); err != nil {
		t.Fatalf("runPart: %v", err)
	}
	for _, name := range stale[:3] {
		if _, err := os.Stat(filepath.Join(rt, name)); !os.IsNotExist(err) {
			t.Errorf("stale %s survived the part phase (err=%v)", name, err)
		}
	}
}

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
