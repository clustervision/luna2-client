package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// stubErr is a tiny placeholder error so FakeRunner can model command
// failures without depending on os/exec.
type stubErr struct{ msg string }

func (e *stubErr) Error() string { return e.msg }

func errFail() error { return &stubErr{msg: "exit 1"} }

// alwaysAvailable says yes to every CommandAvailable probe; tests that
// want to exercise the "command missing" branch override per-name.
func alwaysAvailable(string) bool { return true }

// statNotExist pins stopMDIfExists' /dev probe to "nothing there" so a unit
// test can never inherit the HOST's real /dev/md state.
func statNotExist(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

// noSleep replaces time.Sleep in tests; the partition-wait loops would
// otherwise add 20 seconds to every "stuck partition" test.
func noSleep(time.Duration) {}

// fakeMDDetail stubs Options.MDAdmDetail to deterministic member lists
// keyed by md device path.
type fakeMDDetail struct {
	bySource map[string][]string
}

func (f *fakeMDDetail) detail(_ context.Context, md string) ([]string, error) {
	if m, ok := f.bySource[md]; ok {
		return m, nil
	}
	return nil, errors.New("no detail")
}

// ----------------------------------------------------------------------
// mdraid parsers
// ----------------------------------------------------------------------

func TestParseMDStat(t *testing.T) {
	in := `Personalities : [raid1] [raid10]
md0 : active raid1 sda1[0] sdb1[1]
      102400 blocks super 1.2 [2/2] [UU]

md1 : active raid10 sda2[0] sdb2[1] sdc2[2] sdd2[3]
      204800 blocks super 1.2 64K chunks 2 near-copies [4/4]

unused devices: <none>
`
	got := parseMDStat(in)
	want := []string{"/dev/md0", "/dev/md1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseMDStat = %v, want %v", got, want)
	}
}

func TestParseMDAdmDetailMembers(t *testing.T) {
	out := `/dev/md0:
        Version : 1.2
  Creation Time : ...
     Raid Level : raid1
     Array Size : 102400 (100.00 MiB)
   ...
    Number   Major   Minor   RaidDevice State
       0       8        1        0      active sync   /dev/sda1
       1       8       17        1      active sync   /dev/sdb1
       -       0        0        2      removed
`
	got := parseMDAdmDetailMembers(out)
	want := []string{"/dev/sda1", "/dev/sdb1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

// ----------------------------------------------------------------------
// unmount
// ----------------------------------------------------------------------

func TestForceUnmountNotMounted(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// findmnt -rn -M <target> returns non-zero when target isn't mounted.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot/boot"},
		Response: executor.FakeResponse{Err: errFail()}})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable})
	if !c.forceUnmountTarget(context.Background(), "/sysroot/boot") {
		t.Fatal("not-mounted target should be reported as success")
	}
}

func TestForceUnmountLazyFallback(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	target := "/sysroot/var/log"
	// Mounted check succeeds.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", target},
		Response: executor.FakeResponse{Stdout: []byte(target + " /dev/sda1 xfs rw\n")}})
	// Normal umount fails.
	f.AddMatch(executor.FakeMatch{Path: "umount", ArgsPrefix: []string{target},
		Response: executor.FakeResponse{Err: errFail()}})
	// Lazy umount succeeds.
	f.AddMatch(executor.FakeMatch{Path: "umount", ArgsPrefix: []string{"-l", target},
		Response: executor.FakeResponse{}})

	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable})
	if !c.forceUnmountTarget(context.Background(), target) {
		t.Fatal("lazy fallback should report success")
	}
	// Verify both umount paths were tried.
	calls := f.Calls()
	var sawNormal, sawLazy bool
	for _, c := range calls {
		if c.Path == "umount" && len(c.Args) == 1 && c.Args[0] == target {
			sawNormal = true
		}
		if c.Path == "umount" && len(c.Args) == 2 && c.Args[0] == "-l" && c.Args[1] == target {
			sawLazy = true
		}
	}
	if !sawNormal || !sawLazy {
		t.Fatalf("expected both umount paths; normal=%v lazy=%v", sawNormal, sawLazy)
	}
}

func TestFindSysrootMountsCollectsSysrootAndVGOwned(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// findmnt -rn -o TARGET,SOURCE.
	body := strings.Join([]string{
		"/sysroot /dev/mapper/os-root xfs rw",
		"/sysroot/boot /dev/sda2 xfs rw",
		"/var/lib/os-data /dev/mapper/os-data xfs rw", // outside sysroot but contains "os"
		"/var/lib/unrelated /dev/sdb1 ext4 rw",        // unrelated
	}, "\n") + "\n"
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte(body)}})

	c := New(Options{Runner: f, Sysroot: "/sysroot", CommandAvailable: alwaysAvailable})
	got := c.findSysrootMounts(context.Background(), "os", true /* ownsSysroot */, nil)
	want := map[string]struct{}{
		"/sysroot":         {},
		"/sysroot/boot":    {},
		"/var/lib/os-data": {},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d entries", got, len(want))
	}
	for _, g := range got {
		if _, ok := want[g]; !ok {
			t.Fatalf("unexpected target %q in %v", g, got)
		}
	}
}

// A non-owner (data) set's cleanup must NOT touch the OS set's freshly
// mounted tree in a multi-set run. ExecutionOrder processes the OS set
// first, so /sysroot{,/boot,/boot/efi} are live when the data set's
// cleanup scans — the old strictly-under-sysroot collection unmounted
// them, the extract landed /boot on the root LV, and every multi-set
// install shipped an empty ESP (loud "missing EFI mount directory" on
// ubuntu24, silent unbootable install + PXE reinstall churn on rocky,
// 2026-07-17). The non-owner scan is source-scoped to the set's own
// devices; its own prior mounts (re-entrancy) still collect.
func TestFindSysrootMountsNonOwnerLeavesOtherSetsMounts(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	body := strings.Join([]string{
		"/sysroot /dev/mapper/os-root xfs rw",        // OS set: must survive
		"/sysroot/boot /dev/vda2 xfs rw",             // OS set: must survive
		"/sysroot/boot/efi /dev/vda1 vfat rw",        // OS set: must survive
		"/sysroot/data /dev/vdb1 xfs rw",             // THIS set's prior partition mount
		"/sysroot/scratch /dev/mapper/data-s xfs rw", // THIS set's prior LV mount
		"/sysroot/raid /dev/md/data-r xfs rw",        // THIS set's prior md mount
		"/sysroot/other /dev/vdbb1 xfs rw",           // /dev/vdbb is NOT /dev/vdb: must survive
	}, "\n") + "\n"
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte(body)}})

	c := New(Options{Runner: f, Sysroot: "/sysroot", CommandAvailable: alwaysAvailable})
	got := c.findSysrootMounts(context.Background(), "data", false, /* ownsSysroot */
		map[string]struct{}{"/dev/vdb": {}})
	want := map[string]struct{}{
		"/sysroot/data":    {},
		"/sysroot/scratch": {},
		"/sysroot/raid":    {},
	}
	if len(got) != len(want) {
		t.Fatalf("non-owner scan got %v, want exactly %v", got, want)
	}
	for _, g := range got {
		if _, ok := want[g]; !ok {
			t.Fatalf("non-owner scan unmounts another set's mount %q (got %v)", g, got)
		}
	}
}

// ----------------------------------------------------------------------
// LVM
// ----------------------------------------------------------------------

// ownVG stubs the vgselect probes: VG "os" (uuid AAAA-1111) on sda2+sdb2,
// which are the selected disks' partitions.
func ownVG(f *executor.FakeRunner, extra string) {
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		Response: executor.FakeResponse{Stdout: []byte("  AAAA-1111 os /dev/sda2\n  AAAA-1111 os /dev/sdb2\n" + extra)}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("sda\nsda1\nsda2\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", "/dev/sdb"},
		Response: executor.FakeResponse{Stdout: []byte("sdb\nsdb1\nsdb2\n")}})
}

func joinedCalls(f *executor.FakeRunner) []string {
	calls := f.Calls()
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Path+" "+strings.Join(c.Args, " "))
	}
	return out
}

func TestTeardownVGRemovesEverythingWhenVGExists(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// dmsetup ls returns two mapper names for the VG.
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("os-root\t(253:0)\nos-boot\t(253:1)\nunrelated-x\t(253:9)\n")}})
	// findmnt -S for the mapper paths returns no mount.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/mapper/os-root", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/mapper/os-boot", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	ownVG(f, "")

	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable})
	_ = c.teardownVG(context.Background(), "os", SelectedDisksSet([]string{"/dev/sda", "/dev/sdb"}), nil)

	// Collect the sequence of invocations we should have seen.
	wanted := []string{
		"dmsetup ls",
		"vgchange --noudevsync -an --select vg_uuid=AAAA-1111",
		"vgremove --noudevsync -fy --select vg_uuid=AAAA-1111",
		"pvremove -ff -y /dev/sda2",
		"pvremove -ff -y /dev/sdb2",
		"dmsetup remove --retry -f os-root",
		"dmsetup remove --retry -f os-boot",
		"rm -rf " + filepath.Join("/dev", "os"),
	}
	gotJoined := joinedCalls(f)
	for _, w := range wanted {
		found := false
		for _, g := range gotJoined {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected call %q in:\n%s", w, strings.Join(gotJoined, "\n"))
		}
	}
	for _, g := range gotJoined {
		if strings.Contains(g, "unrelated-x") && strings.HasPrefix(g, "dmsetup remove") {
			t.Fatalf("unrelated mapping removed: %s", g)
		}
	}
}

func TestTeardownVGSkipsLVMWhenVGAbsent(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	// pvs lists no VG named os.
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		Response: executor.FakeResponse{Stdout: []byte("  CCCC-3333 data /dev/sdc1\n")}})

	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable})
	_ = c.teardownVG(context.Background(), "os", SelectedDisksSet([]string{"/dev/sda"}), nil)

	// No vgchange / lvremove / vgremove calls should have happened.
	for _, call := range f.Calls() {
		switch call.Path {
		case "vgchange", "lvremove", "vgremove", "pvremove":
			t.Fatalf("unexpected call to %s when VG is absent: %v", call.Path, call.Args)
		}
	}
}

func TestTeardownSameDiskVGsMDMemberIntersectionBugFix(t *testing.T) {
	// Three PVs in the system:
	//   /dev/sda1 -> belongs to VG "os"        (direct, selected disk)
	//   /dev/md0  -> belongs to VG "data"      (md whose members include sda)
	//   /dev/md1  -> belongs to VG "preserved" (md on unrelated sdc/sdd)
	//
	// Selected disks: sda, sdb. The bug-fix path: "preserved" must NOT
	// be removed because none of /dev/md1's members are on selected
	// disks. The Bash code before the fix would have removed it.
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte(
			"  /dev/sda1 os\n  /dev/md0 data\n  /dev/md1 preserved\n")}})

	// vgremove probe: lvs returns nothing for the small example, both
	// VGs go through vgchange/vgremove only.
	f.AddMatch(executor.FakeMatch{Path: "lvs", ArgsPrefix: []string{"--noheadings", "-o", "lv_path", "os"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "lvs", ArgsPrefix: []string{"--noheadings", "-o", "lv_path", "data"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	// dmsetup ls returns nothing.
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("")}})

	md := &fakeMDDetail{
		bySource: map[string][]string{
			"/dev/md0": {"/dev/sda2", "/dev/sdb2"}, // intersects {sda,sdb}
			"/dev/md1": {"/dev/sdc2", "/dev/sdd2"}, // does not intersect
		},
	}

	selected := SelectedDisksSet([]string{"/dev/sda", "/dev/sdb"})
	c := New(Options{
		Runner:           f,
		CommandAvailable: alwaysAvailable,
		MDAdmDetail:      md.detail,
	})
	_ = c.teardownSameDiskVGs(context.Background(), selected, nil)

	calls := f.Calls()
	var removedVGs []string
	for _, call := range calls {
		// arg order is "--noudevsync -fy <vg>" (by-name form); the --select
		// form does not occur in this path
		if call.Path == "vgremove" && len(call.Args) == 3 && call.Args[0] == "--noudevsync" && call.Args[1] == "-fy" {
			removedVGs = append(removedVGs, call.Args[2])
		}
	}
	wantRemoved := map[string]bool{"os": true, "data": true}
	for _, v := range removedVGs {
		delete(wantRemoved, v)
	}
	if len(wantRemoved) != 0 {
		t.Fatalf("missing vgremove calls: %v; saw: %v", wantRemoved, removedVGs)
	}
	for _, v := range removedVGs {
		if v == "preserved" {
			t.Fatalf("VG 'preserved' should NOT have been removed (bug fix); calls: %v", removedVGs)
		}
	}
}

// ----------------------------------------------------------------------
// mdraid
// ----------------------------------------------------------------------

func TestStopMDArraysScansMDStat(t *testing.T) {
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	body := "Personalities : [raid1]\nmd0 : active raid1 sda1[0] sdb1[1]\n      102400 blocks super 1.2 [2/2] [UU]\n"
	if err := writeFileTest(mdstatPath, body); err != nil {
		t.Fatalf("write mdstat: %v", err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	// lsblk for "what are the partitions of /dev/sda?" → none for this
	// test (we only care about the md-stop and zero-superblock paths).
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("")}})

	md := &fakeMDDetail{bySource: map[string][]string{
		"/dev/md0": {"/dev/sda1", "/dev/sdb1"},
	}}

	c := New(Options{
		Runner:           f,
		MDStatPath:       mdstatPath,
		CommandAvailable: alwaysAvailable,
		MDAdmDetail:      md.detail,
	})
	_ = c.stopMDArrays(context.Background(), "os", SelectedDisksSet([]string{"/dev/sda"}), nil)

	var stoppedMD0 bool
	for _, call := range f.Calls() {
		if call.Path == "mdadm" && len(call.Args) == 2 && call.Args[0] == "--stop" && call.Args[1] == "/dev/md0" {
			stoppedMD0 = true
		}
	}
	if !stoppedMD0 {
		t.Fatalf("mdadm --stop /dev/md0 expected; calls: %v", f.Calls())
	}
}

func TestStopMDArraysSkipsUnrelated(t *testing.T) {
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	body := "md9 : active raid1 sdz1[0] sdy1[1]\n      102400 blocks super 1.2 [2/2] [UU]\n"
	if err := writeFileTest(mdstatPath, body); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("")}})

	md := &fakeMDDetail{bySource: map[string][]string{
		"/dev/md9": {"/dev/sdz1", "/dev/sdy1"}, // members do not intersect selected
	}}

	c := New(Options{
		Runner:           f,
		MDStatPath:       mdstatPath,
		CommandAvailable: alwaysAvailable,
		MDAdmDetail:      md.detail,
	})
	_ = c.stopMDArrays(context.Background(), "os", SelectedDisksSet([]string{"/dev/sda"}), nil)

	for _, call := range f.Calls() {
		if call.Path == "mdadm" && len(call.Args) == 2 && call.Args[0] == "--stop" && call.Args[1] == "/dev/md9" {
			t.Fatalf("mdadm --stop /dev/md9 should NOT have been called; calls: %v", f.Calls())
		}
	}
}

// ----------------------------------------------------------------------
// disks
// ----------------------------------------------------------------------

func TestWipePartitionsAndDisksHappy(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// /dev/sda has two partitions: sda1 (no lvm holder), sda2 (lvm holder os-root).
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("sda  disk\nsda1  part\nsda2  part\n")}})
	// lvmDMNameForPartition probes per partition.
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "NAME,TYPE", "/dev/sda1"},
		Response: executor.FakeResponse{Stdout: []byte("sda1 part\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "NAME,TYPE", "/dev/sda2"},
		Response: executor.FakeResponse{Stdout: []byte("sda2 part\nos-root lvm\n")}})
	// partition-wait poll: TYPE column has no "part" rows -> disk clean.
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})

	c := New(Options{
		Runner:           f,
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
	})
	c.wipePartitionsAndDisks(context.Background(), []string{"/dev/sda"})

	// We expect wipefs + sgdisk + partprobe for /dev/sda; dmsetup remove
	// for os-root; wipefs + mdadm --zero-superblock for each partition.
	wanted := []string{
		"dmsetup remove -f os-root",
		"wipefs -af /dev/sda1",
		"mdadm --zero-superblock /dev/sda1",
		"wipefs -af /dev/sda2",
		"mdadm --zero-superblock /dev/sda2",
		"wipefs -af /dev/sda",
		"sgdisk --zap-all /dev/sda",
		"partprobe /dev/sda",
	}
	calls := f.Calls()
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, c.Path+" "+strings.Join(c.Args, " "))
	}
	for _, w := range wanted {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected call %q in:\n%s", w, strings.Join(got, "\n"))
		}
	}
}

// ----------------------------------------------------------------------
// orchestrator
// ----------------------------------------------------------------------

// TestCleanupSelectedSetEndToEnd exercises the full 5-step cascade with
// canned responses for every command the Cleaner issues. It is the
// closest unit-test parity we get without real disks.
func TestCleanupSelectedSetEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	if err := writeFileTest(mdstatPath, "Personalities : [raid1]\nmd0 : active raid1 sda1[0] sdb1[1]\n"); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	// the array is lpart's own (homehost "any") from the previous install
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/sda1"},
		Response: executor.FakeResponse{Stdout: []byte("  Name : any:os\n")}})
	// Step 1: unmount sysroot tree (one mount).
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/mapper/os-root xfs rw\n")}})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/mapper/os-root xfs rw\n")}})
	f.AddMatch(executor.FakeMatch{Path: "umount", ArgsPrefix: []string{"/sysroot"},
		Response: executor.FakeResponse{}})
	// Step 2a: dmsetup ls + vgs/lvs/pvs for VG "os".
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("os-root\t(253:0)\n")}})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/mapper/os-root", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "vgs", ArgsPrefix: []string{"os"}, Response: executor.FakeResponse{}})
	f.AddMatch(executor.FakeMatch{Path: "lvs", ArgsPrefix: []string{"--noheadings", "-o", "lv_path", "os"},
		Response: executor.FakeResponse{Stdout: []byte("/dev/os/root\n")}})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("  /dev/sda2 os\n")}})
	// Step 3: mdraid scan.
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("sda  disk\nsda1  part\nsda2  part\n")}})
	// Step 5: wipe. lvmDMNameForPartition probes:
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "NAME,TYPE", "/dev/sda1"},
		Response: executor.FakeResponse{Stdout: []byte("sda1 part\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "NAME,TYPE", "/dev/sda2"},
		Response: executor.FakeResponse{Stdout: []byte("sda2 part\n")}})
	// Step 5 final: partition-wait probe returns "disk\n" (no part rows).
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})

	md := &fakeMDDetail{bySource: map[string][]string{
		"/dev/md0": {"/dev/sda1", "/dev/sdb1"},
	}}

	c := New(Options{
		Runner:           f,
		Sysroot:          "/sysroot",
		MDStatPath:       mdstatPath,
		DevDir:           "/dev",
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
		MDAdmDetail:      md.detail,
	})
	res, err := c.CleanupSelectedSet(context.Background(), Set{
		Name:          "os",
		SelectedDisks: []string{"/dev/sda"},
		OwnsSysroot:   true,
	})
	if err != nil {
		t.Fatalf("CleanupSelectedSet: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Result.Status = %q, want ok", res.Status)
	}

	// Verify the cascade hit every checkpoint.
	calls := f.Calls()
	mustSee := []string{
		"umount /sysroot",              // step 1
		"vgremove --noudevsync -fy os", // step 2a
		"mdadm --stop /dev/md0",        // step 3
		"wipefs -af /dev/sda",          // step 5
		"sgdisk --zap-all /dev/sda",    // step 5
	}
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, c.Path+" "+strings.Join(c.Args, " "))
	}
	for _, w := range mustSee {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q in cascade calls:\n%s", w, strings.Join(got, "\n"))
		}
	}
}

func TestCleanupSelectedSetPreflightRequiresRunner(t *testing.T) {
	c := New(Options{Runner: nil, CommandAvailable: alwaysAvailable})
	_, err := c.CleanupSelectedSet(context.Background(), Set{Name: "os"})
	if err == nil {
		t.Fatal("expected preflight error when Runner is nil")
	}
}

// TestCleanupSelectedSetPersistentSkipsEntirely proves that a set marked
// Persistent=true runs zero commands against its devices. This is the
// Phase 14 safety guarantee: no probes, no lsblk, no findmnt, nothing.
func TestCleanupSelectedSetPersistentSkipsEntirely(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	c := New(Options{
		Runner:           f,
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
	})
	res, err := c.CleanupSelectedSet(context.Background(), Set{
		Name:          "data",
		SelectedDisks: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd"},
		Persistent:    true,
	})
	if err != nil {
		t.Fatalf("CleanupSelectedSet: %v", err)
	}
	if res.Status != "skipped_persistent" {
		t.Fatalf("Result.Status = %q, want skipped_persistent", res.Status)
	}
	if calls := f.Calls(); len(calls) != 0 {
		t.Fatalf("persistent set should run zero commands; got %d:\n%v", len(calls), calls)
	}
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func writeFileTest(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600) //nolint:gosec // test fixture in t.TempDir
}

// TestParseMDStatArraysInactive proves members are read from the /proc/mdstat
// line itself, so an INACTIVE array (which mdadm --detail cannot enumerate) is
// still seen — the stale-mdraid re-entrancy fix.
func TestParseMDStatArraysInactive(t *testing.T) {
	in := `Personalities : [raid1]
md124 : inactive sdd1[3](S) sdc1[2](S)

md125 : active raid1 sda2[0] sdb2[1]
      102400 blocks super 1.2 [2/2] [UU]

unused devices: <none>
`
	arrs := parseMDStatArrays(in)
	if len(arrs) != 2 {
		t.Fatalf("want 2 arrays, got %d: %+v", len(arrs), arrs)
	}
	if arrs[0].Device != "/dev/md124" ||
		len(arrs[0].Members) != 2 || arrs[0].Members[0] != "sdd1" || arrs[0].Members[1] != "sdc1" {
		t.Errorf("inactive md124 members not parsed from line: %+v", arrs[0])
	}
	if arrs[1].Device != "/dev/md125" || len(arrs[1].Members) != 2 {
		t.Errorf("active md125 members: %+v", arrs[1])
	}
}

// TestArrayTouchesSelectedInactive: an inactive array (mdadm --detail errors)
// on a selected disk must still be detected via the line members.
func TestArrayTouchesSelectedInactive(t *testing.T) {
	c := New(Options{
		Runner:           executor.NewFakeRunner(executor.FakeResponse{}),
		CommandAvailable: alwaysAvailable,
		MDAdmDetail:      (&fakeMDDetail{}).detail, // empty map -> always errors
	})
	selected := SelectedDisksSet([]string{"/dev/sdc", "/dev/sdd"})
	if !c.arrayTouchesSelected(context.Background(),
		mdArray{Device: "/dev/md124", Members: []string{"sdd1", "sdc1"}}, selected) {
		t.Error("inactive array on selected disks must be detected via line members")
	}
	if c.arrayTouchesSelected(context.Background(),
		mdArray{Device: "/dev/md9", Members: []string{"sde1", "sdf1"}}, selected) {
		t.Error("array on unrelated disks must not match")
	}
}

// TestEnsureNoAutoAssemble: writes AUTO -all (creating dirs), is idempotent.
func TestEnsureNoAutoAssemble(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "mdadm.conf")
	p2 := filepath.Join(dir, "sub", "mdadm.conf") // parent dir absent
	if err := EnsureNoAutoAssemble(p1, p2); err != nil {
		t.Fatalf("EnsureNoAutoAssemble: %v", err)
	}
	for _, p := range []string{p1, p2} {
		b, err := os.ReadFile(p) //nolint:gosec // test temp path
		if err != nil || !strings.Contains(string(b), "AUTO -all") {
			t.Errorf("%s missing AUTO -all (err=%v): %q", p, err, string(b))
		}
	}
	if err := EnsureNoAutoAssemble(p1); err != nil { // idempotent
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p1) //nolint:gosec // test temp path
	if strings.Count(string(b), "AUTO -all") != 1 {
		t.Errorf("AUTO -all duplicated: %q", string(b))
	}
}

// TestTeardownVGDeactivatesBeforeRemovingDMNodes is the regression guard for the
// node001 (2026-06-08) failure: vgchange -an MUST precede dmsetup remove, else
// an open mapper node survives and the next lvcreate collides (exit 5).
func TestTeardownVGDeactivatesBeforeRemovingDMNodes(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("os-root\t(253:0)\n")}})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/mapper/os-root", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	ownVG(f, "")

	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable})
	_ = c.teardownVG(context.Background(), "os", SelectedDisksSet([]string{"/dev/sda", "/dev/sdb"}), nil)

	deactivateIdx, removeIdx := -1, -1
	for i, call := range f.Calls() {
		joined := call.Path + " " + strings.Join(call.Args, " ")
		if deactivateIdx == -1 && joined == "vgchange --noudevsync -an --select vg_uuid=AAAA-1111" {
			deactivateIdx = i
		}
		if removeIdx == -1 && strings.HasPrefix(joined, "dmsetup remove --retry -f os-root") {
			removeIdx = i
		}
	}
	if deactivateIdx == -1 || removeIdx == -1 {
		t.Fatalf("missing calls: deactivate=%d remove=%d", deactivateIdx, removeIdx)
	}
	if deactivateIdx > removeIdx {
		t.Fatalf("vgchange -an (idx %d) must run BEFORE dmsetup remove (idx %d)", deactivateIdx, removeIdx)
	}
}

// TestTeardownVGLeavesForeignSameNameVGAlone: a VG named like the set but
// with no PV on the set's disks (an old OS disk still attached) is foreign:
// it is never deactivated or removed, its mappings are kept (told apart by the
// dm uuid), and /dev/<vg> stays. Only this set's VG goes, by UUID.
func TestTeardownVGLeavesForeignSameNameVGAlone(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	ownVG(f, "  BBBB-2222 os /dev/sdc3\n  CCCC-3333 other /dev/sda3\n")
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("os-root\t(253:0)\nos-home\t(253:1)\n")}})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"info", "-c", "--noheadings", "-o", "uuid", "os-root"},
		Response: executor.FakeResponse{Stdout: []byte("LVM-AAAA1111rootlvuuid\n")}})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"info", "-c", "--noheadings", "-o", "uuid", "os-home"},
		Response: executor.FakeResponse{Stdout: []byte("LVM-BBBB2222homelvuuid\n")}})

	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	if err := c.teardownVG(context.Background(), "os", SelectedDisksSet([]string{"/dev/sda", "/dev/sdb"}), nil); err != nil {
		t.Fatalf("teardownVG: %v", err)
	}
	got := map[string]bool{}
	for _, g := range joinedCalls(f) {
		got[g] = true
		if strings.Contains(g, "BBBB-2222") || strings.Contains(g, "/dev/sdc3") || g == "dmsetup remove --retry -f os-home" {
			t.Fatalf("foreign VG touched: %s", g)
		}
		if strings.HasPrefix(g, "rm -rf") {
			t.Fatalf("/dev/os also holds the foreign VG's links: %s", g)
		}
		if strings.HasPrefix(g, "vgremove") && !strings.Contains(g, "--select") {
			t.Fatalf("a VG must never be removed by its (ambiguous) name: %s", g)
		}
	}
	for _, w := range []string{
		"vgchange --noudevsync -an --select vg_uuid=AAAA-1111",
		"vgremove --noudevsync -fy --select vg_uuid=AAAA-1111",
		"dmsetup remove --retry -f os-root",
	} {
		if !got[w] {
			t.Fatalf("missing own-VG teardown call %q", w)
		}
	}
	if got["vgremove --noudevsync -fy --select vg_uuid=CCCC-3333"] {
		t.Fatal("must NOT touch the differently-named VG 'other'")
	}
}

// TestEnsureNoLVMAutoActivate writes the auto-activation override (creating
// dirs) and is idempotent.
func TestEnsureNoLVMAutoActivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lvm", "lvmlocal.conf")
	if err := EnsureNoLVMAutoActivate(p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "auto_activation_volume_list = []") {
		t.Fatalf("marker missing:\n%s", b)
	}
	// The el9 LVM devices file must be disabled too, or `lvcreate` on a
	// partition of a serialed disk (every real HPC disk) exits 5. FTA v2
	// finding #3 / BE-A6c.
	if !strings.Contains(string(b), "use_devicesfile = 0") {
		t.Fatalf("use_devicesfile=0 missing (lvcreate would fail on serialed disks):\n%s", b)
	}
	if err := EnsureNoLVMAutoActivate(p); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(p) //nolint:gosec // test temp path
	if strings.Count(string(b2), "auto_activation_volume_list") != 1 {
		t.Fatalf("not idempotent:\n%s", b2)
	}
	if strings.Count(string(b2), "use_devicesfile") != 1 {
		t.Fatalf("use_devicesfile not idempotent:\n%s", b2)
	}
}

// TestRemoveLVMDevicesFile removes an existing devices file and treats a missing
// one as success (portable belt for SLE, where use_devicesfile=0 in lvmlocal.conf
// did not stop a pre-seeded system.devices from failing pvcreate -- exit 5).
func TestRemoveLVMDevicesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lvm", "devices", "system.devices")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("VERSION=1.1.1\nIDTYPE=sys_serial IDNAME=deadbeef DEVNAME=/dev/vda\n"), 0o644); err != nil { //nolint:gosec // test temp path
		t.Fatal(err)
	}
	if err := RemoveLVMDevicesFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("devices file still present after removal (err=%v)", err)
	}
	// Idempotent: a missing file is not an error.
	if err := RemoveLVMDevicesFile(p); err != nil {
		t.Fatalf("removing a missing devices file must succeed, got %v", err)
	}
}

// findmnt -r prints a space and non-ASCII bytes as \xNN: the targets come back
// as real paths, so the unmount and the lookups by target find them.
func TestFindmntTargetsAreDecoded(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/mapper/os-root\n/sysroot/my\\x20data /dev/sdb1\n")}})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/sdc1"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot/d\\xc3\\xa4ta\n")}})
	c := New(Options{Runner: f, Sysroot: "/sysroot", CommandAvailable: alwaysAvailable})
	got := c.findSysrootMounts(context.Background(), "os", true, nil)
	found := false
	for _, g := range got {
		found = found || g == "/sysroot/my data"
	}
	if !found {
		t.Errorf("findSysrootMounts = %q, want /sysroot/my data among them", got)
	}
	if mt := c.findmntTargetForSource(context.Background(), "/dev/sdc1"); mt != "/sysroot/däta" {
		t.Errorf("findmntTargetForSource = %q, want /sysroot/däta", mt)
	}
}

// The SOURCE column is escaped the same way: a mount of a selected disk's
// partition is only recognised as the set's own once its source is decoded.
func TestFindmntSourcesAreDecoded(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/sda1\n/sysroot/data /dev/my\\x20disk1\n")}})
	c := New(Options{Runner: f, Sysroot: "/sysroot", CommandAvailable: alwaysAvailable})
	selected := map[string]struct{}{"/dev/my disk": {}}
	got := c.findSysrootMounts(context.Background(), "", false, selected)
	if len(got) != 1 || got[0] != "/sysroot/data" {
		t.Errorf("findSysrootMounts = %q, want [/sysroot/data]", got)
	}
}
