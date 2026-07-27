package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validConfig is a minimal valid v1.4 disklayout used by the guard tests.
const validConfig = `{
	"version": 2,
	"sets": [
		{"name": "os", "role": "os", "selection": "manual", "raid": "none",
		 "devices": ["/dev/sda"],
		 "volumes": [
		   {"name": "uefi", "mountpoint": "/boot/efi", "fs": "vfat", "provider": "partition", "size": "600M"},
		   {"name": "root", "mountpoint": "/", "fs": "xfs", "provider": "lvm", "size": "100%"}
		 ]}
	]
}`

// Per the cross-tool convention used in cmd/lpart-storage-prepare, only the
// orchestrator-wiring + memory-set + guard paths are tested at the cmd
// level. The disk-touching verification flow (vgchange / blkid / fsck)
// requires real /dev/sd* devices and is covered by the on-node
// integration runs from /root/NEWTREE/regression.md. Per-module tests
// in internal/fscheck and internal/fs/predict cover the disk-touching
// branches with FakeRunner.

func runEnv(t *testing.T, args ...string) (stderr string, code int) {
	t.Helper()
	tmp := t.TempDir()

	// Synthetic /proc/cmdline that satisfies RequireInstallerEnv.
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=luna rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(tmp, "logs")
	args = append([]string{
		"--log-dir", logDir,
		"--cmdline-path", cmdline,
		"--skip-command-check",
	}, args...)

	var outBuf, errBuf bytes.Buffer
	rc := run(append([]string{"lpart-storage-check"}, args...), &outBuf, &errBuf)
	return errBuf.String(), rc
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingConfig(t *testing.T) {
	stderr, rc := runEnv(t)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "--config is required") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestConfigNotFound(t *testing.T) {
	stderr, rc := runEnv(t, "--config", "/no/such/file.json")
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "config not found") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestRefusesWithoutLunaMarker(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeConfig(t, validConfig)
	badCmdline := filepath.Join(tmp, "cmdline-bad")
	if err := os.WriteFile(badCmdline, []byte("root=/dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var outBuf, errBuf bytes.Buffer
	rc := run([]string{
		"lpart-storage-check",
		"--log-dir", filepath.Join(tmp, "logs"),
		"--cmdline-path", badCmdline,
		"--skip-command-check",
		"--config", cfg,
	}, &outBuf, &errBuf)
	if rc == 0 {
		t.Fatalf("expected refusal; stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "root=luna") {
		t.Fatalf("wording wrong: %s", errBuf.String())
	}
}

// Under v1.4 a memboot RAM root is mode-driven, not a provider=memory volume
// in a disklayout, so a disklayout that declares one is rejected at
// validation. (The tri-state health probe over real sets is exercised by the
// Phase 3 decision-engine tests and the on-node regression runs.)
func TestMemoryProviderInConfigRejected(t *testing.T) {
	cfg := writeConfig(t, `{
		"version": 2,
		"sets": [
			{"name": "os", "role": "os", "selection":"discover", "raid": "none",
			 "volumes": [{"name": "root", "mountpoint": "/", "fs": "tmpfs", "provider": "memory", "options": "defaults"}]}
		]
	}`)
	// No --dry-run: storage-check refuses it outright (TestDryRunRefused);
	// the memory-provider rejection fires at config validation, before any
	// probe could run, so the invocation stays read-only regardless.
	stderr, rc := runEnv(t, "--config", cfg, "--result-out", filepath.Join(t.TempDir(), "r.json"))
	if rc == 0 {
		t.Fatalf("expected rejection of provider=memory; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "reserved for the memboot RAM root") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

// storage-check refuses --dry-run outright: its probes are already read-only,
// and a dry-run would stub them all to rc 0, fabricating health verdicts from
// evidence never gathered -- a false report an operator may act on.
// FTA v2 BE-F7 (TE-D), barrier NB-8.
func TestDryRunRefused(t *testing.T) {
	stderr, rc := runEnv(t, "--dry-run")
	if rc == 0 {
		t.Fatalf("--dry-run must be refused; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "fabricate") {
		t.Fatalf("refusal must explain the fabricated-verdict hazard: %s", stderr)
	}
	// And with a config too -- the refusal fires regardless of other flags.
	stderr, rc = runEnv(t, "--dry-run", "--config", "/nonexistent.json")
	if rc == 0 || !strings.Contains(stderr, "not supported") {
		t.Fatalf("refusal must fire before config handling: rc=%d %s", rc, stderr)
	}
}
