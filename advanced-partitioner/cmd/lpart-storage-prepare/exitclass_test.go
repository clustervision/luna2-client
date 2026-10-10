package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
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

// exitForError is the single mapping from runDecision errors to exit codes.
func TestExitForError(t *testing.T) {
	var buf bytes.Buffer
	if rc := exitForError(&buf, safetyRefusalf("data-fence", "x")); rc != 2 || !strings.Contains(buf.String(), "safety_refusal=data-fence") {
		t.Fatalf("safety refusal: rc=%d %s", rc, buf.String())
	}
	buf.Reset()
	if rc := exitForError(&buf, fmt.Errorf("wrapped: %w", fatalConfigf("device-missing", "set %q: gone", "os"))); rc != 3 || !strings.Contains(buf.String(), "fatal_config=device-missing") {
		t.Fatalf("fatal config: rc=%d %s", rc, buf.String())
	}
	buf.Reset()
	if rc := exitForError(&buf, errors.New("udevadm settle timed out")); rc != 1 {
		t.Fatalf("environmental failures stay retryable, rc=%d", rc)
	}
}

// A ramdisk without the storage tools (the fs checkers included) parks with
// exit 3 before anything else happens.
func TestMissingToolsAreFatal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tmp := t.TempDir()
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=luna rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var outBuf, errBuf bytes.Buffer
	rc := run([]string{"lpart-storage-prepare", "--log-dir", filepath.Join(tmp, "logs"),
		"--cmdline-path", cmdline, "--skip-final-inventory", "--dry-run"}, &outBuf, &errBuf)
	if rc != 3 || !strings.Contains(errBuf.String(), "fatal_config=missing-commands") || !strings.Contains(errBuf.String(), "e2fsck") {
		t.Fatalf("missing tools: want rc=3 fatal_config=missing-commands naming e2fsck; rc=%d stderr=%s", rc, errBuf.String())
	}
}

// Sanitize erases exactly the discovered pool: a failed discovery is an
// error (it may be transient: exit 1), never a "successful" erase of
// nothing; an empty pool is a legitimate diskless node (exit 0) that says
// nothing was erased.
func TestSanitizeDiscoveryFailureIsNotSuccess(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"sanitize"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := listDevices
	t.Cleanup(func() { listDevices = prev })
	runSan := func() (string, string, int) {
		cmdline := filepath.Join(tmp, "cmdline")
		if err := os.WriteFile(cmdline, []byte("root=luna rw\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		logDir := t.TempDir()
		var outBuf, errBuf bytes.Buffer
		rc := run([]string{"lpart-storage-prepare", "--log-dir", logDir, "--cmdline-path", cmdline,
			"--skip-command-check", "--skip-final-inventory", "--install-context", ic, "--dry-run"}, &outBuf, &errBuf)
		return errBuf.String(), readToolLog(t, logDir), rc
	}

	listDevices = func(context.Context, discover.Options) ([]discover.Device, error) {
		return nil, errors.New("lsblk: exit status 1")
	}
	stderr, _, rc := runSan()
	if rc != 1 || !strings.Contains(stderr, "nothing was erased") {
		t.Fatalf("failed discovery must not be a sanitize success; rc=%d stderr=%s", rc, stderr)
	}

	listDevices = func(context.Context, discover.Options) ([]discover.Device, error) { return nil, nil }
	_, log, rc := runSan()
	if rc != 0 || !strings.Contains(log, "no local disks found -- nothing was erased") || !strings.Contains(log, "USB") {
		t.Fatalf("an empty pool exits 0 and says nothing was erased (USB excluded by design); rc=%d log=%s", rc, log)
	}
}
