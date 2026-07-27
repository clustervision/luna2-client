package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// runWithEnv drives main.run with the given args and an empty default
// environment, returning stdout/stderr and the exit code. The CLI's
// --cmdline-path and --skip-command-check are wired so we never touch
// /proc/cmdline or PATH probes during tests. stdout is returned even
// though no current test inspects it, to keep the helper symmetric
// with the cmd/lpart-discover test suite — easier diffing.
//
//nolint:unparam // stdout intentionally exposed for cross-suite symmetry
func runWithEnv(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	// Pin firmware to UEFI so these tests do not depend on the firmware of
	// the host the suite runs on (a BIOS CI runner would otherwise trip the
	// UEFI-required gate). BIOS refusal is covered by TestRefusesBIOSFirmware.
	prevDetect := detectFirmware
	detectFirmware = func() string { return "uefi" }
	t.Cleanup(func() { detectFirmware = prevDetect })
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
		"--skip-final-inventory",
	}, args...)

	var outBuf, errBuf bytes.Buffer
	rc := run(append([]string{"lpart-storage-prepare"}, args...), &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), rc
}

// writeConfig dumps the supplied JSON body to a temp file and returns
// the path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// validConfig is a minimal valid v1.4 disklayout (single-disk manual LVM OS
// set) used by the guard tests, which return before any disk work.
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

// With no disklayout and the default install_mode=auto, the decision engine
// falls back to a memboot RAM root (dry-run here so no real mounts happen).
func TestNoConfigAutoFallsBackToMemboot(t *testing.T) {
	tmp := t.TempDir()
	_, stderr, rc := runWithEnv(t,
		"--dry-run",
		"--result-out", filepath.Join(tmp, "r.json"),
		"--resolved-out", filepath.Join(tmp, "rs.json"),
	)
	if rc != 0 {
		t.Fatalf("expected memboot fallback to succeed; rc=%d stderr=%s", rc, stderr)
	}
}

func TestRequiresWipeWhenNotDryRun(t *testing.T) {
	cfg := writeConfig(t, validConfig)
	_, stderr, rc := runWithEnv(t, "--config", cfg)
	if rc == 0 {
		t.Fatalf("expected --wipe required; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "--wipe is required") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

// A node whose firmware is not UEFI is refused up front with the
// safety_refusal (exit 2) contract -- lpart never partitions a BIOS/legacy
// node (no NVRAM boot management, no mirrored boot chain). This is the
// operator-facing firmware gate; the partition/bootloader belts back it up.
func TestRefusesBIOSFirmware(t *testing.T) {
	prev := detectFirmware
	detectFirmware = func() string { return "bios" }
	t.Cleanup(func() { detectFirmware = prev })

	tmp := t.TempDir()
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=luna rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, validConfig)
	var outBuf, errBuf bytes.Buffer
	rc := run([]string{
		"lpart-storage-prepare",
		"--log-dir", filepath.Join(tmp, "logs"),
		"--cmdline-path", cmdline,
		"--skip-command-check",
		"--skip-final-inventory",
		"--config", cfg, "--dry-run",
	}, &outBuf, &errBuf)
	if rc != 2 {
		t.Fatalf("BIOS firmware must exit 2 (safety refusal); got rc=%d stderr=%s", rc, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "safety_refusal=firmware-not-uefi") {
		t.Fatalf("expected firmware-not-uefi safety refusal; stderr=%s", errBuf.String())
	}
}

// A completed sanitize produces NO per-set storage results by design (it
// erases and stops; the pipeline installs nothing after it). That empty
// result set must be SUCCESS (exit 0) -- before the fix it fell through to
// result.WriteAuto's "no results" error and exited 1, so luna2-start.sh
// re-erased the already-blank disks every 30s forever.
// FTA v2 BE-Z1 / MCS-18, barrier NB-1.
func TestSanitizeWithNoResultsExitsZero(t *testing.T) {
	tmp := t.TempDir()
	icPath := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(icPath, []byte(`{"install_mode":"sanitize"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// In the test sandbox device discovery fails -> empty pool -> sanitize
	// erases nothing and returns zero results: exactly the empty-result shape.
	_, stderr, rc := runWithEnv(t,
		"--install-context", icPath,
		"--dry-run",
	)
	if rc != 0 {
		t.Fatalf("sanitize with no results must exit 0; got rc=%d stderr=%s", rc, stderr)
	}
	if strings.Contains(stderr, "no results") {
		t.Fatalf("the WriteAuto no-results error must not fire for sanitize: %s", stderr)
	}
}

// Under v1.4 the memboot RAM root is mode-driven (install_mode=memboot),
// synthesized by the decision engine, NOT declared as a provider=memory
// volume in a disklayout. So a disklayout that declares one is rejected.
// (The decision-engine memboot path is exercised by Phase 3 tests.)
func TestMemoryProviderInConfigRejected(t *testing.T) {
	cfg := writeConfig(t, `{
		"version": 2,
		"sets": [
			{"name": "os", "role": "os", "selection":"discover", "raid": "none",
			 "volumes": [{"name": "root", "mountpoint": "/", "fs": "tmpfs", "provider": "memory"}]}
		]
	}`)
	_, stderr, rc := runWithEnv(t, "--config", cfg, "--dry-run")
	if rc == 0 {
		t.Fatalf("expected rejection of provider=memory; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "reserved for the memboot RAM root") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

// Disk flows (single-disk, raid, multi-set) touch real block devices through
// the selector's lsblk/findmnt probes and are validated by the per-module
// FakeRunner tests plus the on-node regression runs, not here.

// Refuses non-installer env unless --allow-non-installer. The env guard
// fires before the config is even loaded, so any config body works.
func TestRefusesWithoutLunaMarker(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeConfig(t, validConfig)
	// Synthetic cmdline WITHOUT the luna marker.
	badCmdline := filepath.Join(tmp, "cmdline-bad")
	if err := os.WriteFile(badCmdline, []byte("root=/dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var outBuf, errBuf bytes.Buffer
	rc := run([]string{
		"lpart-storage-prepare",
		"--log-dir", filepath.Join(tmp, "logs"),
		"--cmdline-path", badCmdline,
		"--skip-command-check",
		"--skip-final-inventory",
		"--config", cfg, "--dry-run",
	}, &outBuf, &errBuf)
	if rc == 0 {
		t.Fatalf("expected refusal; stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "root=luna") {
		t.Fatalf("wording wrong: %s", errBuf.String())
	}
}

// Barrier pin (FTA Cluster B, exit-code contract): a safety-classed halt
// must leave run() with exit code 2 (the safety_refusal contract the boot
// wrapper parks on), not a generic 1. Sanitize×persistent is the cheapest
// such halt to drive end-to-end (no devices needed).
func TestSafetyRefusalExitsTwo(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"sanitize"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, `{
	"version": 2,
	"sets": [
		{"name": "data", "role": "data", "selection": "manual", "raid": "none",
		 "persistent": true,
		 "devices": ["/dev/sdb"],
		 "volumes": [
		   {"name": "d", "mountpoint": "/data", "fs": "xfs", "provider": "lvm", "size": "100%"}
		 ]}
	]
}`)
	_, stderr, rc := runWithEnv(t,
		"--install-context", ic,
		"--config", cfg,
		"--dry-run",
	)
	if rc != 2 {
		t.Fatalf("safety halt must exit 2; got %d (stderr=%s)", rc, stderr)
	}
	if !strings.Contains(stderr, "safety_refusal=") {
		t.Fatalf("stderr should carry the safety_refusal marker: %s", stderr)
	}
}

// Legacy misroute is deterministic on every retry → safety-refusal class
// (exit 2), so the wrapper parks instead of looping (FTA D4).
func TestLegacyMisrouteExitsTwo(t *testing.T) {
	tmp := t.TempDir()
	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, rc := runWithEnv(t, "--install-context", ic, "--dry-run")
	if rc != 2 {
		t.Fatalf("legacy misroute must exit 2; got %d (stderr=%s)", rc, stderr)
	}
}

// serialCollisionWarnings (BE-N5): a duplicated non-empty serial or an empty
// serial must produce a warning — the pre-write identity re-check
// (verifyDiskIdentity) is vacuous for such disks — but never an error.
func TestSerialCollisionWarnings(t *testing.T) {
	// Duplicate serial across two selected disks.
	warns := serialCollisionWarnings([]selector.Resolved{
		{ResolvedPath: "/dev/sda", Serial: "S1"},
		{ResolvedPath: "/dev/sdb", Serial: "S1"},
	})
	if len(warns) != 1 {
		t.Fatalf("duplicate serial: want 1 warning, got %v", warns)
	}
	for _, want := range []string{"/dev/sda", "/dev/sdb", `"S1"`, "BE-N5"} {
		if !strings.Contains(warns[0], want) {
			t.Fatalf("duplicate-serial warning missing %q: %s", want, warns[0])
		}
	}

	// Empty serial on a selected disk.
	warns = serialCollisionWarnings([]selector.Resolved{
		{ResolvedPath: "/dev/sda", Serial: "S1"},
		{ResolvedPath: "/dev/sdb", Serial: "  "},
	})
	if len(warns) != 1 || !strings.Contains(warns[0], "no serial") ||
		!strings.Contains(warns[0], "/dev/sdb") {
		t.Fatalf("empty serial: want 1 no-serial warning for /dev/sdb, got %v", warns)
	}

	// Distinct non-empty serials: silent.
	warns = serialCollisionWarnings([]selector.Resolved{
		{ResolvedPath: "/dev/sda", Serial: "S1"},
		{ResolvedPath: "/dev/sdb", Serial: "S2"},
	})
	if len(warns) != 0 {
		t.Fatalf("distinct serials must not warn: %v", warns)
	}
}

func TestAssembleExplicitUsesExaminedMembers(t *testing.T) {
	logger, err := txlog.Init(txlog.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()
	fake := executor.NewFakeRunner(executor.FakeResponse{})
	// two disks, one partition each; both partitions carry the "data" array
	fake.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vdb"},
		Response: executor.FakeResponse{Stdout: []byte("vdb disk\nvdb1 part\n")}})
	fake.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vdc"},
		Response: executor.FakeResponse{Stdout: []byte("vdc disk\nvdc1 part\n")}})
	fake.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
		Response: executor.FakeResponse{Stdout: []byte("           Name : any:data  (local to host any)\n")}})
	sp := &setProcessor{runner: fake, logger: logger}
	var got [][]string
	run := func(path string, args ...string) { got = append(got, append([]string{path}, args...)) }
	sp.assembleExplicit(context.Background(), "data",
		[]selector.Resolved{{ResolvedPath: "/dev/vdb"}, {ResolvedPath: "/dev/vdc"}}, run)
	if len(got) != 1 {
		t.Fatalf("want 1 assemble call, got %v", got)
	}
	want := []string{"mdadm", "--assemble", "/dev/md/data", "/dev/vdb1", "/dev/vdc1"}
	if strings.Join(got[0], " ") != strings.Join(want, " ") {
		t.Fatalf("assemble argv = %v, want %v", got[0], want)
	}
}

func TestAssembleExplicitNoMembersIsNoop(t *testing.T) {
	logger, err := txlog.Init(txlog.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()
	fake := executor.NewFakeRunner(executor.FakeResponse{ExitCode: 1, Err: fmt.Errorf("no superblock")})
	sp := &setProcessor{runner: fake, logger: logger}
	var got [][]string
	run := func(path string, args ...string) { got = append(got, append([]string{path}, args...)) }
	sp.assembleExplicit(context.Background(), "data",
		[]selector.Resolved{{ResolvedPath: "/dev/vdb"}}, run)
	if len(got) != 0 {
		t.Fatalf("want no assemble call without members, got %v", got)
	}
}

func TestMDSuperblockNameParsing(t *testing.T) {
	logger, err := txlog.Init(txlog.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()
	for _, tc := range []struct{ out, want string }{
		{"           Name : any:data  (local to host any)\n", "data"},
		{"           Name : node002:os-boot\n", "os-boot"},
		{"           Name : bare\n", "bare"},
		{"no superblock here\n", ""},
	} {
		fake := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(tc.out)})
		sp := &setProcessor{runner: fake, logger: logger}
		if got := sp.mdSuperblockName(context.Background(), "/dev/vdb1"); got != tc.want {
			t.Fatalf("mdSuperblockName(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}
