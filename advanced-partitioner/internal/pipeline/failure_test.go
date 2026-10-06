package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The boot wrapper reads this path, so the name is part of the contract.
const testFailurePath = "last-failure.txt"

// A stage failure must leave one small file naming the stage, the untruncated
// reason and the failing step's stderr: the node reboots into the next attempt
// ~30 s later, so the console gets one look at why.
func TestStageFailWritesTheReasonAndStderr(t *testing.T) {
	dir := t.TempDir()
	p := New(context.Background(), Options{Phase: PhasePart, LogDir: dir, StepTimeout: 30 * time.Second})
	err := p.runStepArgv("osimage", []string{"/bin/sh", "-c",
		"echo 'tar: /mnt/sysimage/lib: Cannot write: No space left on device' >&2; exit 1"})
	if err == nil {
		t.Fatal("expected the step to fail")
	}
	p.stageFail("osimage", err.Error())

	b, rerr := os.ReadFile(filepath.Join(dir, testFailurePath))
	if rerr != nil {
		t.Fatalf("read failure file: %v", rerr)
	}
	got := string(b)
	for _, want := range []string{"osimage", "exited 1", "No space left on device"} {
		if !strings.Contains(got, want) {
			t.Fatalf("failure file lost %q; got:\n%s", want, got)
		}
	}
}

// A previous attempt's failure file must not be read as this attempt's reason:
// the wrapper prints whatever is there, so the phase clears it on entry.
func TestRunClearsAStaleFailureFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, testFailurePath)
	if err := os.WriteFile(path, []byte("stage: osimage\nreason: last time\n"), 0o640); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}
	p := New(context.Background(), Options{Phase: PhasePre, RuntimeDir: filepath.Join(dir, "runtime"), LogDir: dir})
	go func() {
		for range p.Events() {
		}
	}()
	p.run()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale failure file survived the run (stat err = %v)", err)
	}
}
