package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Per the cross-tool convention, cmd-level tests cover the orchestrator
// + CLI flag wiring + guard paths only. The disk-touching download +
// tar extract is exercised by the on-node integration runs from
// /root/NEWTREE/regression.md, and the unit tests in internal/osimage
// already cover every helper with FakeRunner.

func runEnv(t *testing.T, args ...string) (stderr string, code int) {
	t.Helper()
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	// --storage-result '' = explicit no-result opt-out so unit tests are
	// hermetic (the fail-closed default would refuse on a missing file).
	// A test's own --storage-result appended later overrides this.
	args = append([]string{"--log-dir", logDir, "--allow-non-installer", "--storage-result", ""}, args...)
	var outBuf, errBuf bytes.Buffer
	rc := run(append([]string{"lpart-osimage-install"}, args...), &outBuf, &errBuf)
	return errBuf.String(), rc
}

// runEnvWithLog is runEnv's sibling that also returns the per-tool log
// file contents. The Logger writes UserInfo / KV / stage banners there;
// errln writes to stderr. Tests asserting on logged output read from
// the file, since the in-process Logger goes to its own os.Stderr.
func runEnvWithLog(t *testing.T, args ...string) (stderr, logBody string, code int) {
	t.Helper()
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	args = append([]string{"--log-dir", logDir, "--allow-non-installer", "--storage-result", ""}, args...)
	var outBuf, errBuf bytes.Buffer
	rc := run(append([]string{"lpart-osimage-install"}, args...), &outBuf, &errBuf)
	body, _ := os.ReadFile(filepath.Join(logDir, "lpart-osimage-install.log")) //nolint:gosec // test artefact
	return errBuf.String(), string(body), rc
}

func writeInstallScript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigExclusiveWithPreserve(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "filter.json")
	if err := os.WriteFile(cfg, []byte(`{"preserve":["x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t,
		"--config", cfg,
		"--preserve", "etc/hosts",
		"--skip-sysroot-check",
	)
	if rc == 0 {
		t.Fatalf("expected exclusive error; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "--config is exclusive") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestMissingConfigFile(t *testing.T) {
	stderr, rc := runEnv(t,
		"--config", "/no/such/file.json",
		"--skip-sysroot-check",
	)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "config file not found") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestEmptyConfigParses(t *testing.T) {
	// Empty {} config must not be rejected at the parse stage; it's
	// the "no filters" form that the bash version accepts as well.
	// Without a LUNA_TOKEN we expect the next preflight to fail with
	// "LUNA_TOKEN is not set", proving we got past --config parsing.
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "filter.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t,
		"--config", cfg,
		"--skip-sysroot-check",
		"--luna-install-script", "/nonexistent-for-test",
	)
	if rc == 0 {
		t.Fatalf("expected non-zero rc (LUNA_TOKEN missing); stderr=%s", stderr)
	}
	if strings.Contains(stderr, "config file not found") || strings.Contains(stderr, "invalid JSON") {
		t.Fatalf("empty config wrongly rejected at parse stage: %s", stderr)
	}
	if !strings.Contains(stderr, "LUNA_TOKEN is not set") {
		t.Fatalf("expected LUNA_TOKEN preflight failure; got %s", stderr)
	}
}

func TestSysrootNotMountedRefused(t *testing.T) {
	// Synthetic /proc/mounts with no sysroot entry → tool should
	// refuse.
	tmp := t.TempDir()
	pm := filepath.Join(tmp, "mounts")
	if err := os.WriteFile(pm, []byte("proc /proc proc rw 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t, "--proc-mounts", pm, "--sysroot", "/sysroot")
	if rc == 0 {
		t.Fatalf("expected refusal; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "sysroot is not mounted") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestLunaTokenMissing(t *testing.T) {
	// Sysroot check passes via --skip-sysroot-check; LUNA_TOKEN is
	// missing → the tool should fail at the LUNA_TOKEN preflight.
	stderr, rc := runEnv(t,
		"--skip-sysroot-check",
		"--luna-install-script", "/no/such/file",
	)
	if rc == 0 {
		t.Fatalf("expected refusal; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "LUNA_TOKEN is not set") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestImageFilenameMissing(t *testing.T) {
	// Token override is set so LUNA_TOKEN preflight passes; the
	// install.sh file exists but has no /files/<name>.tar.bz2 → the
	// tool should fail at image-filename detection.
	tmp := t.TempDir()
	pm := filepath.Join(tmp, "mounts")
	if err := os.WriteFile(pm, []byte("tmpfs /sysroot tmpfs rw 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := writeInstallScript(t, `LUNA_TOKEN="x"`+"\n")
	stderr, rc := runEnv(t,
		"--proc-mounts", pm,
		"--luna-install-script", script,
		"--luna-token", "envtoken",
		"--sysroot", "/sysroot",
		"--skip-extract",
	)
	if rc == 0 {
		t.Fatalf("expected refusal; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "does not contain a /files/") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

// lpart-osimage-install refuses to run when install_mode=local.

func TestRefusesOnInstallModeLocal(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	body := `{"install_mode":"local"}`
	if err := os.WriteFile(ic, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t,
		"--install-context", ic,
		"--skip-sysroot-check",
	)
	if rc != 1 {
		t.Fatalf("expected rc=1; got %d (stderr=%s)", rc, stderr)
	}
	if !strings.Contains(stderr, "install_mode=local") {
		t.Fatalf("error should quote install_mode value:\n%s", stderr)
	}
	if !strings.Contains(stderr, "lpart-storage-check instead") {
		t.Fatalf("error should redirect operator:\n%s", stderr)
	}
}

func TestIgnoreInstallContextEscapeForLocal(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"local"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// With --ignore-install-context the guard is bypassed; the next
	// preflight is sysroot-not-mounted. We use --skip-sysroot-check so
	// the test stops at LUNA_TOKEN.
	stderr, rc := runEnv(t,
		"--install-context", ic,
		"--ignore-install-context",
		"--skip-sysroot-check",
	)
	if rc == 0 {
		t.Fatalf("expected next preflight to fail; stderr=%s", stderr)
	}
	if strings.Contains(stderr, "install_mode=local") {
		t.Fatalf("install-context guard should have been bypassed:\n%s", stderr)
	}
}

// Phase 14: persistent-set mountpoints in storage-result.json must be
// auto-excluded from the tar extract in every mode.

func TestPersistentMountpointsAddedToExcludes(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"auto","osimage_sync":"Full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sr := filepath.Join(tmp, "storage-result.json")
	body := `{"version":2,"sets":[
        {"name":"os","role":"os","persistent":false,"filesystems":[{"mount":"/","device":"tmpfs","fs":"tmpfs"}]},
        {"name":"data","role":"data","persistent":true,"filesystems":[{"mount":"/data","device":"/dev/data/scratch","fs":"xfs"}]}
    ]}`
	if err := os.WriteFile(sr, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	script := writeInstallScript(t,
		`LUNA_TOKEN="x"`+"\n"+
			`luna_url="http://controller/files/image.tar.bz2"`+"\n")
	stderr, logBody, rc := runEnvWithLog(t,
		"--install-context", ic,
		"--storage-result", sr,
		"--skip-sysroot-check",
		"--skip-download",
		"--allow-non-installer",
		"--luna-install-script", script,
		"--luna-token", "envtoken",
	)
	if rc != 0 {
		t.Fatalf("expected rc=0 (--skip-download halts cleanly); got %d stderr=%s log=%s", rc, stderr, logBody)
	}
	if !strings.Contains(logBody, "persistent-set-mountpoint excluded from extract: /data") {
		t.Fatalf("missing persistent-set-mountpoint log line; got:\n%s", logBody)
	}
	if !strings.Contains(logBody, "--exclude=./data") {
		t.Fatalf("missing --exclude=./data in tar_excludes; got:\n%s", logBody)
	}
}

// readStorageResultSummary handles both single-set and multi-set shapes,
// including a single-set with persistent=true at the root.
func TestPersistentSingleSetShape(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"auto","osimage_sync":"Full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sr := filepath.Join(tmp, "storage-result.json")
	body := `{"version":2,"name":"data","role":"data","persistent":true,"filesystems":[{"mount":"/data","device":"/dev/data/scratch","fs":"xfs"}]}`
	if err := os.WriteFile(sr, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	script := writeInstallScript(t,
		`LUNA_TOKEN="x"`+"\n"+
			`luna_url="http://controller/files/image.tar.bz2"`+"\n")
	stderr, logBody, rc := runEnvWithLog(t,
		"--install-context", ic,
		"--storage-result", sr,
		"--skip-sysroot-check",
		"--skip-download",
		"--allow-non-installer",
		"--luna-install-script", script,
		"--luna-token", "envtoken",
	)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s log=%s", rc, stderr, logBody)
	}
	if !strings.Contains(logBody, "persistent-set-mountpoint excluded from extract: /data") {
		t.Fatalf("missing log line; got:\n%s", logBody)
	}
}

// Barrier pin (fail-closed storage-result): an unreadable or unparseable
// storage-result.json must be a SAFETY REFUSAL (exit 2), never a silent
// zero-value — the zero value would erase the persistent-mountpoint list and
// let the wipe/extract treat protected volumes as fair game. Removing the
// fail-closed behaviour makes these fail.

func TestCorruptStorageResultRefused(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"auto","osimage_sync":"Full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sr := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(sr, []byte(`{"version":2,"sets":[TRUNCATED`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t,
		"--install-context", ic,
		"--storage-result", sr,
		"--skip-sysroot-check",
	)
	if rc != 2 {
		t.Fatalf("expected exit 2 (safety refusal) on corrupt storage-result; got %d stderr=%s", rc, stderr)
	}
	if !strings.Contains(stderr, "safety_refusal=storage-result") {
		t.Fatalf("expected safety_refusal marker; got: %s", stderr)
	}
}

func TestMissingStorageResultRefused(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"auto","osimage_sync":"Full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t,
		"--install-context", ic,
		"--storage-result", filepath.Join(tmp, "does-not-exist.json"),
		"--skip-sysroot-check",
	)
	if rc != 2 {
		t.Fatalf("expected exit 2 (safety refusal) on missing storage-result; got %d stderr=%s", rc, stderr)
	}
}

func TestEmptyStorageResultPathIsExplicitOptOut(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"auto","osimage_sync":"Full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	script := writeInstallScript(t,
		`LUNA_TOKEN="x"`+"\n"+
			`luna_url="http://controller/files/image.tar.bz2"`+"\n")
	stderr, rc := runEnv(t,
		"--install-context", ic,
		"--storage-result", "",
		"--skip-sysroot-check",
		"--skip-download",
		"--luna-install-script", script,
		"--luna-token", "envtoken",
	)
	if rc != 0 {
		t.Fatalf("explicit --storage-result '' must be accepted; got %d stderr=%s", rc, stderr)
	}
}

// TestRefusesOutsideInstallerEnv locks in the installer-env guard: without
// --allow-non-installer and with a non-Luna cmdline, lpart-osimage-install must
// refuse (it extracts the OS image into /sysroot and must never run on a live
// system). Calls run() directly so the runEnv helper's --allow-non-installer
// does not mask the guard.
func TestRefusesOutsideInstallerEnv(t *testing.T) {
	tmp := t.TempDir()
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=/dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	rc := run([]string{"lpart-osimage-install", "--cmdline-path", cmdline,
		"--log-dir", filepath.Join(tmp, "logs")}, &out, &errBuf)
	if rc == 0 {
		t.Fatal("expected non-zero exit outside the installer environment")
	}
	if !strings.Contains(errBuf.String(), "root=luna") {
		t.Errorf("error should mention the root=luna marker, got: %s", errBuf.String())
	}
}

func TestResolveStreamPolicy(t *testing.T) {
	writeCmdline := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "cmdline")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		flagVal string
		cmdline string
		want    string
	}{
		{"default no token", "auto", "root=luna quiet", "auto"},
		{"cmdline always", "auto", "root=luna lpart.stream=always", "always"},
		{"cmdline never", "auto", "root=luna lpart.stream=never", "never"},
		{"cmdline invalid ignored", "auto", "root=luna lpart.stream=banana", "auto"},
		{"explicit flag beats cmdline", "never", "root=luna lpart.stream=always", "never"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := resolveStreamPolicy(tc.flagVal, writeCmdline(t, tc.cmdline))
			if got != tc.want {
				t.Fatalf("resolveStreamPolicy(%q, %q) = %q, want %q", tc.flagVal, tc.cmdline, got, tc.want)
			}
		})
	}
}
