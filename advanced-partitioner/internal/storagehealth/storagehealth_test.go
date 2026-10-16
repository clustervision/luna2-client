package storagehealth

import (
	"context"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
)

func resolved(paths ...string) []selector.Resolved {
	out := make([]selector.Resolved, 0, len(paths))
	for _, p := range paths {
		out = append(out, selector.Resolved{ConfiguredPath: p, ResolvedPath: p})
	}
	return out
}

func TestDisksBlankTrueWhenEmpty(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// lsblk shows only the whole disk (no children); blkid shows no signature.
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")},
	})
	if !DisksBlank(context.Background(), f, resolved("/dev/sda")) {
		t.Fatal("an empty disk should be blank")
	}
}

// Barrier pin (FTA A9, fail-closed blkid): only blkid's genuine "no
// signature" answer (exit 2, empty output) may classify a disk blank; any
// tool error is UNKNOWN and must read as not-blank, because "blank"
// authorises a wipe. Removing the fail-closed branch fails these.

func TestDisksBlankGenuineNoSignatureExit2(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")},
	})
	f.AddMatch(executor.FakeMatch{
		Path: "blkid", ArgsPrefix: []string{"-s", "TYPE", "-o", "value", "/dev/sda"},
		Response: executor.FakeResponse{ExitCode: 2, Err: &stubExitErr{rc: 2}},
	})
	if !DisksBlank(context.Background(), f, resolved("/dev/sda")) {
		t.Fatal("blkid exit 2 with empty output is a real 'no signature' = blank")
	}
}

func TestDisksBlankFailsClosedOnBlkidError(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")},
	})
	// blkid fails for an unknown reason (rc 4 usage error / tool missing).
	f.AddMatch(executor.FakeMatch{
		Path: "blkid", ArgsPrefix: []string{"-s", "TYPE", "-o", "value", "/dev/sda"},
		Response: executor.FakeResponse{ExitCode: 4, Err: &stubExitErr{rc: 4}},
	})
	if DisksBlank(context.Background(), f, resolved("/dev/sda")) {
		t.Fatal("a blkid ERROR must never classify a disk blank (fail-closed)")
	}
}

// stubExitErr carries an exit code like exec.ExitError does.
type stubExitErr struct{ rc int }

func (e *stubExitErr) Error() string { return "exit status" }
func (e *stubExitErr) ExitCode() int { return e.rc }

func TestDisksBlankFalseWithPartitions(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\npart\npart\n")},
	})
	if DisksBlank(context.Background(), f, resolved("/dev/sda")) {
		t.Fatal("a disk with partitions is not blank")
	}
}

// A non-persistent set whose declared partitions don't exist on an otherwise
// empty disk classifies as blank (safe to create), not broken.
func TestClassifyBlank(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")},
	})
	set := &v2.Set{
		Name: "data", Role: "data", Selection: "manual", Raid: "none",
		Devices: []string{"/dev/sda"},
		Volumes: []v2.Volume{{Name: "d", Mountpoint: "/d", FS: "xfs", Provider: "partition", Size: "100%"}},
	}
	checker := fscheck.New(fscheck.Options{Runner: f})
	verdict, failures := Classify(context.Background(), f, checker, set, resolved("/dev/sda"), nil)
	if verdict != VerdictBlank {
		t.Fatalf("verdict=%s want blank (failures=%v)", verdict, failures)
	}
}

// TestVolumeDevice locks in the device a volume's filesystem lives on — the fix
// for the RAID1 non-idempotency: an os+raid1 set's ESP/boot live on md arrays,
// not the raw partitions.
func TestVolumeDevice(t *testing.T) {
	esp := v2.Volume{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Provider: "partition"}
	boot := v2.Volume{Name: "boot", Mountpoint: "/boot", FS: "xfs", Provider: "partition"}
	root := v2.Volume{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm"}

	raid1 := &v2.Set{Name: "os", Role: "os", Selection: "manual", Raid: "1"}
	if got := volumeDevice(raid1, esp, "/dev/sda", 1); got != "/dev/md/os-efi" {
		t.Errorf("raid1 ESP: got %q want /dev/md/os-efi", got)
	}
	if got := volumeDevice(raid1, boot, "/dev/sda", 2); got != "/dev/md/os-boot" {
		t.Errorf("raid1 /boot: got %q want /dev/md/os-boot", got)
	}
	if got := volumeDevice(raid1, root, "/dev/sda", 0); got != "/dev/os/root" {
		t.Errorf("raid1 root LV: got %q want /dev/os/root", got)
	}

	// raid=none (single disk): ESP/boot are raw partitions.
	none := &v2.Set{Name: "os", Role: "os", Selection: "manual", Raid: "none"}
	if got := volumeDevice(none, esp, "/dev/sda", 1); got != "/dev/sda1" {
		t.Errorf("none ESP: got %q want /dev/sda1", got)
	}
	if got := volumeDevice(none, boot, "/dev/sda", 2); got != "/dev/sda2" {
		t.Errorf("none /boot: got %q want /dev/sda2", got)
	}

	// raid0 os does NOT mirror boot -> raw partitions.
	raid0 := &v2.Set{Name: "os", Role: "os", Selection: "manual", Raid: "0"}
	if got := volumeDevice(raid0, esp, "/dev/sda", 1); got != "/dev/sda1" {
		t.Errorf("raid0 ESP must be raw: got %q want /dev/sda1", got)
	}
}

// allow_degraded is advised only to a set that does not already have it.
func TestDegradedRefusalAdvisesOnlyWhatIsMissing(t *testing.T) {
	for _, allow := range []bool{false, true} {
		msg := DegradedRefusal(&v2.Set{Name: "os", AllowDegraded: allow})
		if advised := strings.Contains(msg, "set allow_degraded"); advised == allow {
			t.Errorf("allow_degraded=%v: advice to set it = %v: %q", allow, advised, msg)
		}
		if !strings.Contains(msg, "replace the disk") {
			t.Errorf("allow_degraded=%v: the replace advice is gone: %q", allow, msg)
		}
	}
}
