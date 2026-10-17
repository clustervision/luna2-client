package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// spanCleaner runs a cleanup of selected sda against the given pvs output,
// /proc/mdstat body and member superblock name; sdc's transport is tran.
func spanCleaner(t *testing.T, pvs, mdstat, name, tran string) (*Cleaner, *executor.FakeRunner) {
	t.Helper()
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte(pvs)}})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/sda1"},
		Response: executor.FakeResponse{Stdout: []byte("           Name : " + name + "  (local to host x)\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dno", "TRAN", "/dev/sdc"},
		Response: executor.FakeResponse{Stdout: []byte(tran + "\n")}})
	stat := filepath.Join(t.TempDir(), "mdstat")
	if err := os.WriteFile(stat, []byte(mdstat), 0o600); err != nil {
		t.Fatal(err)
	}
	md := &fakeMDDetail{bySource: map[string][]string{
		"/dev/md0": {"/dev/sda1", "/dev/sdc1"},
		"/dev/md5": {"/dev/sdc1", "/dev/sdd1"},
	}}
	return New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep, MDStatPath: stat,
		MDAdmDetail: md.detail, Stat: statNotExist}), f
}

func runSpan(c *Cleaner) error {
	_, err := c.CleanupSelectedSet(context.Background(), Set{Name: "os", SelectedDisks: []string{"/dev/sda"}})
	return err
}

// A VG with a PV directly on a disk outside the selection was never lpart's
// (it builds every VG on one PV): refused, nothing touched.
func TestCleanupRefusesVGSpanningOutsideDisk(t *testing.T) {
	c, f := spanCleaner(t, "  /dev/sda2 scratch\n  /dev/sdc2 scratch\n", "", "", "sata")
	err := runSpan(c)
	if err == nil {
		t.Fatal("a VG reaching outside the selected disks must refuse")
	}
	assertProtected(t, err)
	assertNoDestruction(t, f)
}

// An md array reaching outside the selection that lpart did not create (no
// homehost "any") is refused.
func TestCleanupRefusesForeignArraySpanningOutsideDisk(t *testing.T) {
	c, f := spanCleaner(t, "", "md0 : active raid1 sda1[0] sdc1[1]\n", "node7:data", "sata")
	err := runSpan(c)
	if err == nil {
		t.Fatal("a foreign array reaching outside the selected disks must refuse")
	}
	assertProtected(t, err)
	assertNoDestruction(t, f)
}

// lpart's own leftover array is torn down as before, outside member included.
func TestCleanupTearsDownOwnLeftoverArray(t *testing.T) {
	c, f := spanCleaner(t, "", "md0 : active raid1 sda1[0] sdc1[1]\n", "any:os", "sata")
	if err := runSpan(c); err != nil {
		t.Fatalf("lpart's own array must be torn down: %v", err)
	}
	zeroed := false
	for _, call := range f.Calls() {
		if call.Path == "mdadm" && len(call.Args) == 2 && call.Args[0] == "--zero-superblock" && call.Args[1] == "/dev/sdc1" {
			zeroed = true
		}
	}
	if !zeroed {
		t.Fatal("the leftover's outside member was not cleared")
	}
}

// Even lpart's own array is refused when its outside member is a SAN LUN.
func TestCleanupRefusesOwnArrayOnSANLUN(t *testing.T) {
	c, f := spanCleaner(t, "", "md0 : active raid1 sda1[0] sdc1[1]\n", "any:os", "fc")
	err := runSpan(c)
	if err == nil {
		t.Fatal("an array reaching an FC LUN must refuse")
	}
	assertProtected(t, err)
	assertNoDestruction(t, f)
}

// A VG and an array wholly on the selected disks are torn down as always.
func TestCleanupAcceptsSpansInsideSelection(t *testing.T) {
	c, _ := spanCleaner(t, "  /dev/sda2 os\n", "md0 : active raid1 sda1[0] sda3[1]\n", "node7:data", "sata")
	if err := runSpan(c); err != nil {
		t.Fatalf("nothing reaches outside the selected disks: %v", err)
	}
}

// A VG whose md PV lies wholly on other disks reaches outside the selection
// like a plain PV there: refused, nothing touched.
func TestCleanupRefusesVGWithMDPVOutsideDisks(t *testing.T) {
	c, f := spanCleaner(t, "  /dev/sda2 os\n  /dev/md5 os\n", "md5 : active raid1 sdc1[0] sdd1[1]\n", "", "sata")
	err := runSpan(c)
	if err == nil {
		t.Fatal("a VG with an md PV outside the selected disks must refuse")
	}
	assertProtected(t, err)
	assertNoDestruction(t, f)
}

// An md PV that reaches the selection is left to the array check: lpart's own
// leftover array under its VG is still torn down.
func TestCleanupAcceptsVGOnOwnArrayReachingSelection(t *testing.T) {
	c, _ := spanCleaner(t, "  /dev/md0 os\n", "md0 : active raid1 sda1[0] sdc1[1]\n", "any:os", "sata")
	if err := runSpan(c); err != nil {
		t.Fatalf("lpart's own array reaching the selection must be torn down: %v", err)
	}
}

// A pvs that fails gives no PV map, so neither the span refusal nor the
// spanning barrier can judge anything: retryable error, nothing touched.
func TestCleanupFailedPVsRefusesBeforeTouching(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", Response: executor.FakeResponse{
		ExitCode: 5, Err: errors.New("pvs --noheadings -o pv_name,vg_name: exit status 5")}})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep,
		MDStatPath: filepath.Join(t.TempDir(), "mdstat"), Stat: statNotExist})
	err := runSpan(c)
	if err == nil {
		t.Fatal("a failed pvs must stop the cleanup before anything is wiped")
	}
	if errors.Is(err, ErrProtected) {
		t.Fatalf("a failed probe is retryable, not a safety refusal: %v", err)
	}
	assertNoDestruction(t, f)
}

// The VG teardown lists the PVs again; a pvs that fails there must not read as
// "no VG on the selected disks" and let the wipe run under live mappings.
func TestTeardownSameDiskVGsFailedPVsIsRetryable(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", Response: executor.FakeResponse{
		ExitCode: 5, Err: errors.New("pvs --noheadings -o pv_name,vg_name: exit status 5")}})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	err := c.teardownSameDiskVGs(context.Background(), SelectedDisksSet([]string{"/dev/sda"}), nil, nil)
	if err == nil {
		t.Fatal("a failed pvs must stop the teardown before the wipe")
	}
	// the set's own VG is removed before this step: the operator must not read the set as intact
	if !strings.Contains(err.Error(), "own volume group (if any) was already removed") {
		t.Fatalf("the message must say the set's own VG is already gone: %v", err)
	}
	if errors.Is(err, ErrProtected) {
		t.Fatalf("a failed probe is retryable, not a safety refusal: %v", err)
	}
	assertNoDestruction(t, f)
}
