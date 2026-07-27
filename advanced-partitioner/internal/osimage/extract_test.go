package osimage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func TestSysrootFSTypeTmpfs(t *testing.T) {
	dir := t.TempDir()
	pm := filepath.Join(dir, "mounts")
	body := "" +
		"proc /proc proc rw 0 0\n" +
		"tmpfs /sysroot tmpfs rw,size=2G 0 0\n" +
		"sysfs /sys sysfs rw 0 0\n"
	if err := os.WriteFile(pm, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SysrootFSType(pm, "/sysroot")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tmpfs" {
		t.Fatalf("got %q", got)
	}
}

func TestSysrootFSTypeRealDisk(t *testing.T) {
	dir := t.TempDir()
	pm := filepath.Join(dir, "mounts")
	body := "" +
		"/dev/mapper/os-root /sysroot xfs rw 0 0\n" +
		"/dev/sda2 /sysroot/boot xfs rw 0 0\n"
	if err := os.WriteFile(pm, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SysrootFSType(pm, "/sysroot")
	if err != nil {
		t.Fatal(err)
	}
	if got != "xfs" {
		t.Fatalf("got %q", got)
	}
}

// TestSysrootFSTypePropagationFirewall pins the two-entry stack every disk
// install has since the propagation firewall: the firewall tmpfs is mounted
// at the sysroot first, the real root fs over it. The EFFECTIVE fs is the
// last entry — first-match reported tmpfs for every disk sysroot (no-acls
// extracts + auto-stream policy inversion, the 3.56a acls=0 signature).
func TestSysrootFSTypePropagationFirewall(t *testing.T) {
	dir := t.TempDir()
	pm := filepath.Join(dir, "mounts")
	body := "" +
		"proc /proc proc rw 0 0\n" +
		"lpart-propagation-firewall /sysroot tmpfs rw,size=64k,mode=755 0 0\n" +
		"/dev/mapper/os-root /sysroot xfs rw 0 0\n" +
		"/dev/sda2 /sysroot/boot xfs rw 0 0\n"
	if err := os.WriteFile(pm, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SysrootFSType(pm, "/sysroot")
	if err != nil {
		t.Fatal(err)
	}
	if got != "xfs" {
		t.Fatalf("got %q, want xfs (topmost mount wins)", got)
	}
}

func TestSysrootFSTypeNotMounted(t *testing.T) {
	dir := t.TempDir()
	pm := filepath.Join(dir, "mounts")
	if err := os.WriteFile(pm, []byte("proc /proc proc rw 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SysrootFSType(pm, "/sysroot")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// testMounts is the standard FindmntTargets injection for tests: the
// sysroot itself (always a mount in the install env — PrepareSysroot
// refuses a mount list that misses it) plus any extra targets.
func testMounts(sysroot string, extra ...string) func() ([]string, error) {
	targets := append([]string{sysroot}, extra...)
	return func() ([]string, error) { return targets, nil }
}

// stageSysroot builds a tiny sysroot tree: etc/, var/log/, home/u1,
// boot/, proc/.
func stageSysroot(t *testing.T) string {
	t.Helper()
	sysroot := t.TempDir()
	for _, sub := range []string{
		"etc", "var/log/sub", "home/u1", "boot", "proc",
	} {
		if err := os.MkdirAll(filepath.Join(sysroot, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		"etc/hosts", "var/log/messages", "var/log/sub/x", "home/u1/file",
	} {
		if err := os.WriteFile(filepath.Join(sysroot, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return sysroot
}

func TestPrepareSysrootTopLevelWipe(t *testing.T) {
	sysroot := stageSysroot(t)
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveOnly:   ParsePatterns([]string{"etc/hosts"}),
		PreserveEmpty:  ParsePatterns([]string{"home"}),
		NoNewFiles:     ParsePatterns([]string{"proc"}),
	})
	if err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}

	// Preserved + no-new-files top-levels should still exist;
	// preserve-empty's home should exist but be empty; everything else
	// removed.
	mustExist := []string{"etc", "etc/hosts", "home", "proc"}
	mustGone := []string{"boot", "var"}
	for _, p := range mustExist {
		if _, err := os.Stat(filepath.Join(sysroot, p)); err != nil {
			t.Fatalf("expected %s to remain: %v", p, err)
		}
	}
	for _, p := range mustGone {
		if _, err := os.Stat(filepath.Join(sysroot, p)); err == nil {
			t.Fatalf("expected %s to be removed", p)
		}
	}
	// home should be empty.
	homeEntries, err := os.ReadDir(filepath.Join(sysroot, "home"))
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	if len(homeEntries) != 0 {
		t.Fatalf("home should be empty; got %v", homeEntries)
	}
}

func TestPrepareSysrootSiblingDeletion(t *testing.T) {
	sysroot := t.TempDir()
	// /var/log/keepers and /var/log/junk; preserve only keepers.
	for _, sub := range []string{"var/log/keepers", "var/log/junk", "var/log/unrelated"} {
		if err := os.MkdirAll(filepath.Join(sysroot, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveOnly:   ParsePatterns([]string{"var/log/keepers"}),
	}); err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "var/log/keepers")); err != nil {
		t.Fatalf("keepers should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "var/log/junk")); err == nil {
		t.Fatal("junk should be removed by sibling-deletion step")
	}
}

func TestPrepareSysrootCreatesMissingPreserveEmpty(t *testing.T) {
	sysroot := t.TempDir()
	if err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveEmpty:  ParsePatterns([]string{"data", "home"}),
	}); err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	for _, sub := range []string{"data", "home"} {
		info, err := os.Stat(filepath.Join(sysroot, sub))
		if err != nil {
			t.Fatalf("preserve-empty %s missing: %v", sub, err)
		}
		if !info.IsDir() {
			t.Fatalf("preserve-empty %s should be a dir", sub)
		}
	}
}

func TestPrepareSysrootSkipsTopLevelWithActiveMounts(t *testing.T) {
	sysroot := stageSysroot(t)
	// Pretend something is mounted under /sysroot/var/log via the
	// FindmntTargets injection point.
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot, filepath.Join(sysroot, "var", "log")),
	})
	if err != nil {
		t.Fatal(err)
	}
	// /var should still exist because /var/log is "mounted" under it.
	if _, err := os.Stat(filepath.Join(sysroot, "var")); err != nil {
		t.Fatal("/var should be skipped because of active mount underneath")
	}
}

func TestBuildTarExcludes(t *testing.T) {
	got := BuildTarExcludes(
		ParsePatterns([]string{"var/log/***", "etc/hosts"}),
		ParsePatterns([]string{"home", "tmp"}),
		ParsePatterns([]string{"proc", "sys"}),
	)
	want := []string{
		"--exclude=./var/log",
		"--exclude=./etc/hosts",
		"--exclude=./home/*",
		"--exclude=./tmp/*",
		"--exclude=./proc",
		"--exclude=./sys",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractIssuesTarWithExcludesAndACLs(t *testing.T) {
	sysroot := t.TempDir()
	// Pre-create etc/os-release so the post-extract sanity check passes.
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc/os-release"), []byte("NAME=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tar := filepath.Join(t.TempDir(), "image.tar.bz2")
	if err := os.WriteFile(tar, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "tar", ArgsPrefix: []string{"-I", "lbzip2", "--acls", "--numeric-owner"},
		Response: executor.FakeResponse{},
	})

	if err := Extract(context.Background(), ExtractOptions{
		Runner:      f,
		Sysroot:     sysroot,
		TarballPath: tar,
		TarExcludes: []string{"--exclude=./var/log", "--exclude=./home/*"},
		ACLs:        true,
		RemoveAfter: true,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	calls := f.Calls()
	if len(calls) != 1 || calls[0].Path != "tar" {
		t.Fatalf("expected one tar call; got %+v", calls)
	}
	args := strings.Join(calls[0].Args, " ")
	for _, want := range []string{"--acls", "--numeric-owner", "--exclude=./var/log", "--exclude=./home/*", "-xf", tar, "-C", sysroot} {
		if !strings.Contains(args, want) {
			t.Fatalf("tar args missing %q: %s", want, args)
		}
	}
	// Tarball should be removed after a successful extract.
	if _, err := os.Stat(tar); err == nil {
		t.Fatal("tarball should be removed after extract")
	}
}

func TestExtractNoACLs(t *testing.T) {
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc/os-release"), []byte("NAME=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tar := filepath.Join(t.TempDir(), "image.tar.bz2")
	if err := os.WriteFile(tar, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "tar", ArgsPrefix: []string{"-I", "lbzip2", "--no-acls"},
		Response: executor.FakeResponse{},
	})
	if err := Extract(context.Background(), ExtractOptions{
		Runner: f, Sysroot: sysroot, TarballPath: tar, ACLs: false,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
}

func TestExtractFailsWithoutOSRelease(t *testing.T) {
	sysroot := t.TempDir() // no /etc/os-release
	tar := filepath.Join(t.TempDir(), "image.tar.bz2")
	if err := os.WriteFile(tar, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	err := Extract(context.Background(), ExtractOptions{
		Runner: f, Sysroot: sysroot, TarballPath: tar, ACLs: true,
	})
	if err == nil || !strings.Contains(err.Error(), "os-release") {
		t.Fatalf("expected os-release error; got %v", err)
	}
}
