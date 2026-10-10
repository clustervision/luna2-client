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

// The sanitize marker carries what storage-prepare reported.
func TestSanitizeMarkerReportsTheErase(t *testing.T) {
	rt, logs, bin := partEnv(t, "sanitize")
	fakeBin(t, bin, "lpart-storage-prepare", `echo "sanitize: erasing 2 local disk(s): [/dev/sda /dev/sdb]" >&2
echo "/dev/sda: zero-filled (read-back verified blank)" >&2
echo "/dev/sdb: blkdiscard succeeded (blocks discarded, read-back verified blank)" >&2`)
	if err := runPartWith(t, rt, logs, bin); err != nil {
		t.Fatalf("runPart: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(rt, SanitizeDoneMarker))
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	got := string(b)
	for _, want := range []string{"2 local disk(s) erased", "/dev/sda (zero-filled)", "/dev/sdb (discarded)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("marker lost %q:\n%s", want, got)
		}
	}
}

func TestSanitizeMarkerForAnEmptyPool(t *testing.T) {
	rt, logs, bin := partEnv(t, "sanitize")
	fakeBin(t, bin, "lpart-storage-prepare", `echo "sanitize: no local disks found -- nothing was erased" >&2`)
	if err := runPartWith(t, rt, logs, bin); err != nil {
		t.Fatalf("runPart: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(rt, SanitizeDoneMarker))
	if got := string(b); !strings.Contains(got, "nothing was erased") || strings.Contains(got, "verified") {
		t.Fatalf("empty pool must not claim an erase:\n%s", got)
	}
}

// install_mode=local verifies the disks storage-prepare selected: a
// selection=discover set has its devices only in storage-resolved.json.
func TestLocalModePassesTheResolvedDisksToStorageCheck(t *testing.T) {
	rt, logs, bin := partEnv(t, "local")
	argsOut := filepath.Join(rt, "check-args")
	fakeBin(t, bin, "lpart-storage-prepare", `exit 0`)
	fakeBin(t, bin, "lpart-storage-check", `echo "$@" > `+argsOut)
	if err := runPartWith(t, rt, logs, bin); err != nil {
		t.Fatalf("runPart: %v", err)
	}
	b, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("storage-check not run: %v", err)
	}
	if want := "--resolved " + filepath.Join(rt, "storage-resolved.json"); !strings.Contains(string(b), want) {
		t.Fatalf("storage-check args %q lack %q", strings.TrimSpace(string(b)), want)
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

func TestSanitizeReportUnknown(t *testing.T) {
	var r sanitizeReport
	r.observe("unrelated line")
	if got := r.summary(); !strings.Contains(got, "reported no erased disk") {
		t.Fatalf("no evidence must not claim an erase: %q", got)
	}
	r = sanitizeReport{}
	r.observe("sanitize: erasing 3 local disk(s): [a b c]")
	r.observe("/dev/a: zero-filled (read-back verified blank)")
	if got := r.summary(); !strings.Contains(got, "1 of 3 local disk(s)") {
		t.Fatalf("partial evidence must say so: %q", got)
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
