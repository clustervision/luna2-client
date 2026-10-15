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

// failureBanner is what the boot wrapper shows of the failure file: its first 15 lines.
func failureBanner(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, testFailurePath)) //nolint:gosec // the test's own temp dir
	if err != nil {
		t.Fatalf("read failure file: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	return strings.Join(lines[:min(15, len(lines))], "\n")
}

func failStep(t *testing.T, dir, script string) {
	t.Helper()
	p := New(context.Background(), Options{Phase: PhasePart, LogDir: dir, StepTimeout: 30 * time.Second})
	err := p.runStepArgv("storage-prepare", []string{"/bin/sh", "-c", script})
	if err == nil {
		t.Fatal("expected the step to fail")
	}
	p.stageFail("storage-prepare", err.Error())
}

// A park names its cause on the console: the tool's own verdict line is the
// reason, however much debug output came before it.
func TestTheParkReasonIsTheToolsVerdict(t *testing.T) {
	dir := t.TempDir()
	failStep(t, dir, `i=0; while [ $i -lt 30 ]; do echo "[config_validation] debug $i" >&2; i=$((i+1)); done
echo 'lpart-storage-prepare: safety_refusal=data-fence set "os" is broken and install_mode=sync will not wipe it' >&2; exit 2`)
	got := failureBanner(t, dir)
	if !strings.Contains(got, `reason: data-fence: set "os" is broken and install_mode=sync will not wipe it`) {
		t.Fatalf("the banner does not say why the node parked:\n%s", got)
	}
}

// Without a verdict line the cause is at the end of stderr: the banner shows the
// tail, not the debug lines a step prints first.
func TestTheParkBannerShowsTheEndOfStderr(t *testing.T) {
	dir := t.TempDir()
	failStep(t, dir, `i=0; while [ $i -lt 30 ]; do echo "debug line $i" >&2; i=$((i+1)); done
echo 'mount: /sysroot: wrong fs type' >&2; exit 1`)
	got := failureBanner(t, dir)
	if !strings.Contains(got, "mount: /sysroot: wrong fs type") || strings.Contains(got, "debug line 0\n") {
		t.Fatalf("the banner shows the start of stderr instead of its end:\n%s", got)
	}
}
