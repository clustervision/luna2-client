package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
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
	if rc != 1 { // the same exit as every lpart tool's installer-env guard
		t.Fatalf("expected refusal with exit 1, got %d; stderr=%s", rc, errBuf.String())
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

// storage-check refuses --dry-run outright: stubbing its probes would fabricate a verdict,
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

// discoverConfig is a v1.4 disklayout whose os set picks its disks from the
// pool: the config names no devices, storage-prepare resolves them.
const discoverConfig = `{
	"version": 2,
	"sets": [
		{"name": "os", "role": "os", "selection": "discover", "raid": "1",
		 "volumes": [
		   {"name": "uefi", "mountpoint": "/boot/efi", "fs": "vfat", "provider": "partition", "size": "600M"},
		   {"name": "root", "mountpoint": "/", "fs": "xfs", "provider": "lvm", "size": "100%"}
		 ]}
	]
}`

// storage-resolved.json as lpart-storage-prepare writes it (result.WriteResolved).
const resolvedJSON = `{
  "version": 2,
  "sets": [
    {"name": "os", "role": "os", "firmware_mode": "uefi", "selector_type": "device_paths",
     "storage_type": "disk", "raid_mode": "raid1",
     "selected_disks": [
       {"configured_path": "/dev/vdb", "resolved_path": "/dev/vdb", "selector_type": "device_paths", "size_bytes": 1, "model": "", "serial": ""},
       {"configured_path": "/dev/vda", "resolved_path": "/dev/vda", "selector_type": "device_paths", "size_bytes": 1, "model": "", "serial": ""}
     ]}
  ]
}`

func loadConfig(t *testing.T, body string) *v2.Config {
	t.Helper()
	cfg, raw, err := v2.LoadFile(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateRaw(raw); err != nil {
		t.Fatalf("fixture must validate: %v", err)
	}
	return cfg
}

// install_mode=local on a selection=discover layout: storage-check must check
// the disks storage-prepare selected (from storage-resolved.json), in its
// order, instead of an empty device list ("set has no resolved devices" ->
// exit 2 -> the node parked on every boot).
func TestApplyResolvedFillsDiscoverSets(t *testing.T) {
	cfg := loadConfig(t, discoverConfig)
	resolved := writeConfig(t, resolvedJSON)
	if err := applyResolved(cfg, resolved); err != nil {
		t.Fatalf("applyResolved: %v", err)
	}
	if got := strings.Join(cfg.Sets[0].EffectiveDevices(), ","); got != "/dev/vdb,/dev/vda" {
		t.Fatalf("devices = %q, want storage-prepare's selection in its order", got)
	}
	// A manual layout needs no resolved file.
	if err := applyResolved(loadConfig(t, validConfig), ""); err != nil {
		t.Fatalf("manual set: %v", err)
	}
}

func TestApplyResolvedRefusesWithoutTheDisks(t *testing.T) {
	if err := applyResolved(loadConfig(t, discoverConfig), ""); err == nil || !strings.Contains(err.Error(), "--resolved") {
		t.Fatalf("a discover set without --resolved must be refused, got %v", err)
	}
	other := writeConfig(t, strings.ReplaceAll(resolvedJSON, `"name": "os"`, `"name": "data"`))
	if err := applyResolved(loadConfig(t, discoverConfig), other); err == nil || !strings.Contains(err.Error(), "not in") {
		t.Fatalf("a set missing from the resolved file must be refused, got %v", err)
	}
	if err := applyResolved(loadConfig(t, discoverConfig), "/no/such/storage-resolved.json"); err == nil {
		t.Fatal("an unreadable resolved file must be refused")
	}
}

// The refusal is deterministic, so it is a fatal config error (exit 3), not
// a retry every 30 s.
func TestDiscoverWithoutResolvedIsFatal(t *testing.T) {
	stderr, rc := runEnv(t, "--config", writeConfig(t, discoverConfig),
		"--install-context", writeConfig(t, `{"install_mode":"local"}`))
	if rc != 3 || !strings.Contains(stderr, "fatal_config=resolved-devices") {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
}

// Deterministic failures exit 3 (park), not 1 (retry every 30 s for ever).
func TestDeterministicFailuresAreFatal(t *testing.T) {
	ic := writeConfig(t, `{"install_mode":"local"}`)
	cases := []struct {
		name   string
		args   []string
		marker string
	}{
		{"no config", []string{"--install-context", ic}, "fatal_config=config-missing"},
		{"config not found", []string{"--install-context", ic, "--config", "/no/such/file.json"}, "fatal_config=config-missing"},
		{"unparsable config", []string{"--install-context", ic, "--config", writeConfig(t, "{not json")}, "fatal_config=config-load"},
		{"invalid config", []string{"--install-context", ic, "--config", writeConfig(t, strings.ReplaceAll(validConfig, `"raid": "none"`, `"raid": "7"`))}, "fatal_config=config-validation"},
		{"dry-run", []string{"--dry-run"}, "fatal_config=dry-run"},
		{"bad flag", []string{"--no-such-flag"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stderr, rc := runEnv(t, c.args...)
			if rc != 3 || !strings.Contains(stderr, c.marker) {
				t.Fatalf("rc=%d (want 3), stderr=%s", rc, stderr)
			}
		})
	}
}

// A fsck tool missing from the install initramfs is missing on every retry.
func TestMissingFsckToolIsFatal(t *testing.T) {
	tmp := t.TempDir()
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=luna rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // nothing to find
	var outBuf, errBuf bytes.Buffer
	rc := run([]string{"lpart-storage-check", "--log-dir", filepath.Join(tmp, "logs"), "--cmdline-path", cmdline,
		"--install-context", writeConfig(t, `{"install_mode":"local"}`), "--config", writeConfig(t, validConfig)}, &outBuf, &errBuf)
	if rc != 3 || !strings.Contains(errBuf.String(), "fatal_config=missing-command") {
		t.Fatalf("rc=%d (want 3), stderr=%s", rc, errBuf.String())
	}
}
