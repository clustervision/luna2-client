package inventory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func newFakeRunner() *executor.FakeRunner {
	return executor.NewFakeRunner(executor.FakeResponse{})
}

func TestSnapshotWritesLsblkAndMount(t *testing.T) {
	tmp := t.TempDir()
	f := newFakeRunner()
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-o"},
		Response: executor.FakeResponse{Stdout: []byte("NAME KNAME TYPE SIZE\nsda sda disk 100G\n")},
	})
	// findmnt -R -M <sysroot> success means /sysroot is mounted, then
	// we issue the second call with -o ... to capture the table.
	sysroot := "/sysroot"
	f.AddMatch(executor.FakeMatch{
		Path: "findmnt", ArgsPrefix: []string{"-R", "-M", sysroot, "-o"},
		Response: executor.FakeResponse{Stdout: []byte("TARGET SOURCE FSTYPE OPTIONS\n/sysroot /dev/sda1 xfs rw\n")},
	})
	f.AddMatch(executor.FakeMatch{
		Path: "findmnt", ArgsPrefix: []string{"-R", "-M", sysroot},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/sda1 xfs rw")},
	})

	got, err := Snapshot(context.Background(), SnapshotOptions{
		Runner:  f,
		Dir:     tmp,
		Prefix:  "before",
		Sysroot: sysroot,
	})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d captures, want 2: %+v", len(got), got)
	}

	lsblk, err := os.ReadFile(filepath.Join(tmp, "lsblk-before.txt")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("read lsblk-before: %v", err)
	}
	if !strings.Contains(string(lsblk), "sda sda disk") {
		t.Fatalf("lsblk-before content wrong: %s", lsblk)
	}

	mount, err := os.ReadFile(filepath.Join(tmp, "mount-before.txt")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("read mount-before: %v", err)
	}
	if !strings.Contains(string(mount), "/sysroot") {
		t.Fatalf("mount-before content wrong: %s", mount)
	}
}

func TestSnapshotEmptyMountWhenSysrootUnmounted(t *testing.T) {
	tmp := t.TempDir()
	f := executor.NewFakeRunner(executor.FakeResponse{Err: nil})
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-o"},
		Response: executor.FakeResponse{Stdout: []byte("sda disk 100G\n")},
	})
	// findmnt probe fails — sysroot is not mounted.
	f.AddMatch(executor.FakeMatch{
		Path: "findmnt", ArgsPrefix: []string{"-R", "-M", "/sysroot"},
		Response: executor.FakeResponse{Err: errExitOne()},
	})

	if _, err := Snapshot(context.Background(), SnapshotOptions{
		Runner:  f,
		Dir:     tmp,
		Prefix:  "after",
		Sysroot: "/sysroot",
	}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	mount, err := os.ReadFile(filepath.Join(tmp, "mount-after.txt")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("read mount-after: %v", err)
	}
	if len(strings.TrimSpace(string(mount))) != 0 {
		t.Fatalf("mount-after should be empty when sysroot unmounted; got %q", string(mount))
	}
}

func TestFinalStateCapturesEveryCommand(t *testing.T) {
	tmp := t.TempDir()
	f := newFakeRunner()
	f.AddMatch(executor.FakeMatch{Path: "blkid", Response: executor.FakeResponse{Stdout: []byte("/dev/sda1: UUID=abc TYPE=xfs\n")}})
	f.AddMatch(executor.FakeMatch{Path: "pvs", Response: executor.FakeResponse{Stdout: []byte("  PV   VG\n  /dev/sda2 os\n")}})
	f.AddMatch(executor.FakeMatch{Path: "vgs", Response: executor.FakeResponse{Stdout: []byte("  VG  #PV\n  os  1\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lvs", Response: executor.FakeResponse{Stdout: []byte("  LV    VG\n  root  os\n")}})

	captures, err := FinalState(context.Background(), FinalStateOptions{
		Runner: f,
		Dir:    tmp,
		// HasMDStat=false so mdadm is not invoked
	})
	if err != nil {
		t.Fatalf("FinalState: %v", err)
	}
	wantFiles := []string{"blkid-after.txt", "pvs-after.txt", "vgs-after.txt", "lvs-after.txt"}
	if len(captures) != len(wantFiles) {
		t.Fatalf("got %d captures, want %d: %+v", len(captures), len(wantFiles), captures)
	}
	for _, w := range wantFiles {
		if _, err := os.Stat(filepath.Join(tmp, w)); err != nil {
			t.Fatalf("missing %s: %v", w, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "mdadm-detail-after.txt")); !os.IsNotExist(err) {
		t.Fatalf("mdadm file should be absent when HasMDStat=false")
	}
}

func TestFinalStateIncludesMDAdmWhenMDStatPresent(t *testing.T) {
	tmp := t.TempDir()
	f := newFakeRunner()
	f.AddMatch(executor.FakeMatch{Path: "blkid", Response: executor.FakeResponse{}})
	f.AddMatch(executor.FakeMatch{Path: "pvs", Response: executor.FakeResponse{}})
	f.AddMatch(executor.FakeMatch{Path: "vgs", Response: executor.FakeResponse{}})
	f.AddMatch(executor.FakeMatch{Path: "lvs", Response: executor.FakeResponse{}})
	f.AddMatch(executor.FakeMatch{
		Path: "mdadm", ArgsPrefix: []string{"--detail", "--scan"},
		Response: executor.FakeResponse{Stdout: []byte("ARRAY /dev/md0 level=raid1\n")},
	})

	captures, err := FinalState(context.Background(), FinalStateOptions{
		Runner:    f,
		Dir:       tmp,
		HasMDStat: true,
	})
	if err != nil {
		t.Fatalf("FinalState: %v", err)
	}
	if len(captures) != 5 {
		t.Fatalf("expected 5 captures with HasMDStat=true, got %d", len(captures))
	}
	md, err := os.ReadFile(filepath.Join(tmp, "mdadm-detail-after.txt")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("read mdadm file: %v", err)
	}
	if !strings.Contains(string(md), "ARRAY /dev/md0") {
		t.Fatalf("mdadm capture missing expected line: %s", md)
	}
}

func TestFinalStateTolerantOfMissingCommand(t *testing.T) {
	tmp := t.TempDir()
	// Default response = success/empty for every command except pvs which
	// we mark as failing (e.g. lvm not installed on the host).
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", Response: executor.FakeResponse{Err: errExitOne()}})
	if _, err := FinalState(context.Background(), FinalStateOptions{
		Runner: f, Dir: tmp,
	}); err != nil {
		t.Fatalf("FinalState should be tolerant of pvs failure; got %v", err)
	}
	pvs, err := os.ReadFile(filepath.Join(tmp, "pvs-after.txt")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("read pvs file: %v", err)
	}
	if !strings.Contains(string(pvs), "failed or unavailable") {
		t.Fatalf("expected placeholder line in pvs file; got %q", string(pvs))
	}
}

// errExitOne returns a stand-in for a non-zero exit status. The
// FakeRunner doesn't model real ExitError; any non-nil error suffices
// for the "command failed" branches in our code.
func errExitOne() error {
	return &fakeErr{msg: "exit 1"}
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
