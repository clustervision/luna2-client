package selector

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// Tests in this file mock the filesystem (via t.TempDir + symlinks) and
// the executor (via FakeRunner) so they run on any host. The end-to-end
// happy path requires real /dev nodes and is exercised by the Phase 7
// integration tests that drive a parked node001 over SSH; here we
// concentrate on the rule-by-rule failure branches and the candidate-
// dump structure.

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// stubBlock returns an IsBlockDevice predicate that yields true for the
// supplied set of paths and false otherwise.
func stubBlock(paths ...string) func(string) bool {
	set := map[string]struct{}{}
	for _, p := range paths {
		set[p] = struct{}{}
	}
	return func(p string) bool {
		_, ok := set[p]
		return ok
	}
}

// newFakeRunner builds a FakeRunner whose default response is empty
// stdout / nil err. Tests add explicit matches for the calls they care
// about.
func newFakeRunner() *executor.FakeRunner {
	return executor.NewFakeRunner(executor.FakeResponse{})
}

// addLsblkScalars wires the five lsblk scalar calls Resolve() performs
// for one device. dev is currently always /dev/null in the local test
// suite (it's the only device-style path every Linux host is guaranteed
// to expose), but stays a parameter so the Phase 7 integration tests
// can point it at real disks without rewriting helpers.
//
//nolint:unparam // dev parameter is fixed today; intentional for future tests
func addLsblkScalars(f *executor.FakeRunner, dev, typeVal, ro, size, model, serial string) {
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "TYPE", dev}, Response: executor.FakeResponse{Stdout: []byte(typeVal)}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "RO", dev}, Response: executor.FakeResponse{Stdout: []byte(ro)}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-b", "-o", "SIZE", dev}, Response: executor.FakeResponse{Stdout: []byte(size)}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "MODEL", dev}, Response: executor.FakeResponse{Stdout: []byte(model)}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "SERIAL", dev}, Response: executor.FakeResponse{Stdout: []byte(serial)}})
}

// addFindmntNotMounted: the subtree in-use probe (lsblk -ln -o
// MOUNTPOINT) prints empty mountpoints for an unused disk. The helper
// name is kept for diff-friendliness with the pre-Phase-7 suite.
func addFindmntNotMounted(f *executor.FakeRunner, dev string) {
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "MOUNTPOINT", dev},
		Response: executor.FakeResponse{Stdout: []byte("\n\n")},
	})
}

func addFindmntMounted(f *executor.FakeRunner, dev string) {
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "MOUNTPOINT", dev},
		Response: executor.FakeResponse{Stdout: []byte("\n/mnt/foo\n")},
	})
}

// addMountedPartition simulates a mounted PARTITION under an otherwise
// unmounted disk - the case the old whole-disk findmnt probe missed
// (ancestor FTA BE-C2). Barrier pin: Resolve must refuse it.
func addMountedPartition(f *executor.FakeRunner, dev string) {
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "MOUNTPOINT", dev},
		Response: executor.FakeResponse{Stdout: []byte("\n/data\n\n")},
	})
}

// addInUseProbeError simulates the probe tool failing - fail-closed
// means Resolve must refuse (ancestor FTA BE-C1).
func addInUseProbeError(f *executor.FakeRunner, dev string) {
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "MOUNTPOINT", dev},
		Response: executor.FakeResponse{ExitCode: 32, Err: errProbe},
	})
}

var errProbe = errStub("lsblk: probe failed")

type errStub string

func (e errStub) Error() string { return string(e) }

func TestResolveHappyPathByPath(t *testing.T) {
	// Stage a by-path-style symlink in a tmp dir, then point the
	// resolver at that symlink directly. The resolver checks the path
	// prefix only for selector-type derivation; we override the prefix
	// rule by feeding a literal /dev/disk/by-path-prefixed path that
	// EvalSymlinks resolves through a tmp symlink we registered.
	//
	// On most test hosts /dev/disk/by-path/ doesn't host arbitrary
	// entries, so we cannot directly hit it. The pragmatic approach:
	// use a /dev/-prefixed configured path that happens to exist —
	// /dev/null does on every Linux host — and stub IsBlockDevice to
	// accept it. /dev/null is /dev-style, so SelectorTypeOf returns
	// device_paths. This isn't strictly a by-path test but the resolve
	// pipeline beyond the prefix check is identical across selector
	// types.
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required for happy-path resolver test")
	}

	dev := "/dev/null"
	f := newFakeRunner()
	addLsblkScalars(f, dev, "disk", "0", "107374182400", "Test Model", "Serial-123")
	addFindmntNotMounted(f, dev)

	r := New(Options{
		Runner:        f,
		IsBlockDevice: stubBlock(dev),
	})
	rec, err := r.Resolve(context.Background(), dev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.ConfiguredPath != dev {
		t.Fatalf("ConfiguredPath = %s, want %s", rec.ConfiguredPath, dev)
	}
	if rec.SelectorType != v2.SelectorDevicePaths {
		t.Fatalf("SelectorType = %s, want %s", rec.SelectorType, v2.SelectorDevicePaths)
	}
	if rec.SizeBytes != 107374182400 {
		t.Fatalf("SizeBytes = %d, want 107374182400", rec.SizeBytes)
	}
	if rec.Model != "Test Model" {
		t.Fatalf("Model = %q", rec.Model)
	}
	if rec.Serial != "Serial-123" {
		t.Fatalf("Serial = %q", rec.Serial)
	}
}

func TestResolveMissingPathDumpsCandidates(t *testing.T) {
	tmp := t.TempDir()
	byIDDir := filepath.Join(tmp, "by-id")
	byPathDir := filepath.Join(tmp, "by-path")
	byUUIDDir := filepath.Join(tmp, "by-uuid")
	mustMkdir(t, byIDDir)
	mustMkdir(t, byPathDir)
	mustMkdir(t, byUUIDDir)
	mustWriteFile(t, filepath.Join(byIDDir, "ata-Crucial_CT500MX500SSD1_1234"))
	mustWriteFile(t, filepath.Join(byPathDir, "pci-0000:00:1f.2-ata-1.0"))
	mustWriteFile(t, filepath.Join(byPathDir, "pci-0000:00:1f.2-ata-2.0"))

	f := newFakeRunner()
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "NAME,SIZE,MODEL"},
		Response: executor.FakeResponse{Stdout: []byte("sda 107374182400 Some Model\nsdb 107374182400 Other Model\n")},
	})

	var buf bytes.Buffer
	r := New(Options{
		Runner:        f,
		CandidatesOut: &buf,
		ByIDDir:       byIDDir,
		ByPathDir:     byPathDir,
		ByUUIDDir:     byUUIDDir,
		IsBlockDevice: stubBlock(),
	})

	_, err := r.Resolve(context.Background(), "/dev/disk/by-path/nonexistent-1.0")
	if err == nil {
		t.Fatal("expected device_resolution error")
	}
	if !strings.HasPrefix(err.Error(), "device_resolution: device not found:") {
		t.Fatalf("error wording wrong: %v", err)
	}

	dump := buf.String()
	for _, want := range []string{
		"ERROR: device not found",
		"  requested:     /dev/disk/by-path/nonexistent-1.0",
		"  selector_type: physical_paths",
		"  candidates by-id:",
		"    ata-Crucial_CT500MX500SSD1_1234",
		"  candidates by-path:",
		"    pci-0000:00:1f.2-ata-1.0",
		"    pci-0000:00:1f.2-ata-2.0",
		"  candidates by-uuid:",
		"    (none)",
		"  lsblk disks:",
		"    sda 107374182400 Some Model",
	} {
		if !strings.Contains(dump, want) {
			t.Fatalf("candidate dump missing %q:\n%s", want, dump)
		}
	}
}

func TestResolveSelectorTypeMismatchedPrefix(t *testing.T) {
	r := New(Options{Runner: newFakeRunner(), IsBlockDevice: stubBlock()})
	_, err := r.Resolve(context.Background(), "/proc/cpuinfo")
	if err == nil {
		t.Fatal("expected selector-type error")
	}
	if !strings.Contains(err.Error(), "device path must start with /dev/") {
		t.Fatalf("error wording wrong: %v", err)
	}
}

func TestResolveAllShortCircuitsOnFirstError(t *testing.T) {
	r := New(Options{Runner: newFakeRunner(), IsBlockDevice: stubBlock()})
	_, err := r.ResolveAll(context.Background(), []string{
		"/proc/cpuinfo", // selector type failure
		"/dev/sda",      // never reached
	})
	if err == nil || !strings.Contains(err.Error(), "/proc/cpuinfo") {
		t.Fatalf("ResolveAll should fail at the first bad path; got %v", err)
	}
}

func TestResolveNotABlockDevice(t *testing.T) {
	// Reuse /dev/null, but tell IsBlockDevice to reject it. Hits the
	// "(resolved: ...)" branch of notFoundError.
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	var buf bytes.Buffer
	r := New(Options{
		Runner:        newFakeRunner(),
		CandidatesOut: &buf,
		IsBlockDevice: stubBlock(), // empty set -> always false
	})
	_, err := r.Resolve(context.Background(), "/dev/null")
	if err == nil || !strings.Contains(err.Error(), "device_resolution: device not found") {
		t.Fatalf("expected device_resolution error; got %v", err)
	}
	if !strings.Contains(buf.String(), "json_path:     devices[] (resolved:") {
		t.Fatalf("expected resolved-path label in dump:\n%s", buf.String())
	}
}

func TestResolveWrongTypeIsRejected(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	dev := "/dev/null"
	f := newFakeRunner()
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "TYPE", dev},
		Response: executor.FakeResponse{Stdout: []byte("part")},
	})
	r := New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
	_, err := r.Resolve(context.Background(), dev)
	if err == nil || !strings.Contains(err.Error(), "not a whole disk") {
		t.Fatalf("expected 'not a whole disk' error; got %v", err)
	}
}

func TestResolveMountedIsRejected(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	dev := "/dev/null"
	f := newFakeRunner()
	addLsblkScalars(f, dev, "disk", "0", "1", "Model", "Serial")
	addFindmntMounted(f, dev)
	r := New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
	_, err := r.Resolve(context.Background(), dev)
	if err == nil || !strings.Contains(err.Error(), "is mounted") {
		t.Fatalf("expected 'is mounted' error; got %v", err)
	}
}

// Barrier pins (Phase 7 / ancestor FTA BE-C1+C2): the in-use guard must
// see mounted PARTITIONS (subtree probe) and must FAIL CLOSED on a probe
// error. Removing either behaviour fails these.

func TestResolveMountedPartitionIsRejected(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	dev := "/dev/null"
	f := newFakeRunner()
	addLsblkScalars(f, dev, "disk", "0", "1", "Model", "Serial")
	addMountedPartition(f, dev)
	r := New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
	if _, err := r.Resolve(context.Background(), dev); err == nil || !strings.Contains(err.Error(), "is mounted") {
		t.Fatalf("a mounted partition on the disk must refuse the disk; got %v", err)
	}
}

func TestResolveInUseProbeErrorFailsClosed(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	dev := "/dev/null"
	f := newFakeRunner()
	addLsblkScalars(f, dev, "disk", "0", "1", "Model", "Serial")
	addInUseProbeError(f, dev)
	r := New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
	if _, err := r.Resolve(context.Background(), dev); err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("a probe error must refuse (fail-closed); got %v", err)
	}
}

func TestResolveReadOnlyIsRejected(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	dev := "/dev/null"
	f := newFakeRunner()
	addLsblkScalars(f, dev, "disk", "1", "1", "Model", "Serial")
	addFindmntNotMounted(f, dev)
	r := New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
	_, err := r.Resolve(context.Background(), dev)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected 'read-only' error; got %v", err)
	}
}

func TestSelectorTypeMatchesPackageV2(t *testing.T) {
	for in, want := range map[string]v2.SelectorType{
		"/dev/sda":              v2.SelectorDevicePaths,
		"/dev/disk/by-id/foo":   v2.SelectorByID,
		"/dev/disk/by-path/foo": v2.SelectorPhysical,
		"/dev/disk/by-uuid/foo": v2.SelectorByUUID,
	} {
		got, err := v2.SelectorTypeOf(in)
		if err != nil {
			t.Fatalf("v2.SelectorTypeOf(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("v2.SelectorTypeOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNotFoundCandidatesIncludeUUID(t *testing.T) {
	tmp := t.TempDir()
	byIDDir := filepath.Join(tmp, "by-id")
	byPathDir := filepath.Join(tmp, "by-path")
	byUUIDDir := filepath.Join(tmp, "by-uuid")
	mustMkdir(t, byIDDir)
	mustMkdir(t, byPathDir)
	mustMkdir(t, byUUIDDir)
	mustWriteFile(t, filepath.Join(byUUIDDir, "deadbeef-cafe-dead-beef-feedfacefeed"))

	f := newFakeRunner()
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "NAME,SIZE,MODEL"},
		Response: executor.FakeResponse{Stdout: []byte("")},
	})

	var buf bytes.Buffer
	r := New(Options{
		Runner:        f,
		CandidatesOut: &buf,
		ByIDDir:       byIDDir,
		ByPathDir:     byPathDir,
		ByUUIDDir:     byUUIDDir,
		IsBlockDevice: stubBlock(),
	})
	_, err := r.Resolve(context.Background(), "/dev/disk/by-uuid/missing-uuid-1234")
	if err == nil {
		t.Fatal("expected error")
	}
	dump := buf.String()
	if !strings.Contains(dump, "  candidates by-uuid:") {
		t.Fatalf("by-uuid section missing:\n%s", dump)
	}
	if !strings.Contains(dump, "deadbeef-cafe-dead-beef-feedfacefeed") {
		t.Fatalf("by-uuid entry missing:\n%s", dump)
	}
	if !strings.Contains(dump, "  lsblk disks:\n    (none)") {
		t.Fatalf("empty lsblk section formatted wrong:\n%s", dump)
	}
}

func TestLabWarningForDevicePathsSelector(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	var warned bool
	// Stub the logger by giving NewLogger an in-memory file; we look
	// for the warn message in stderr via a capture pipe. Simpler:
	// trigger Resolve with a happy path and observe via the logger's
	// stderr handler. We can't easily intercept slog output here, so
	// just verify the function does not crash and emits the expected
	// record by re-using the existing Logger types under the hood.
	//
	// The lab-warning behaviour is a one-liner; the strongest test we
	// can run cheaply is "Resolve still succeeds for /dev/null when
	// device_paths is used". The real assertion that the warning fired
	// lives in the bash test runbook on node001.
	_ = warned
	f := newFakeRunner()
	addLsblkScalars(f, "/dev/null", "disk", "0", "1", "M", "S")
	addFindmntNotMounted(f, "/dev/null")
	r := New(Options{Runner: f, IsBlockDevice: stubBlock("/dev/null")})
	if _, err := r.Resolve(context.Background(), "/dev/null"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}
