package storagehealth

import (
	"context"
	"os/exec"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
)

// A filesystem checker that cannot run makes the verdict UNKNOWN, never
// broken -- even on a disk that is clearly not blank, which is exactly the
// shape auto used to recreate (wipe) as a "broken" os set.
func TestClassifyMissingCheckerIsUnknownNotBroken(t *testing.T) {
	withAllDevices(t)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	addFS(f, "/dev/sda1", "vfat")
	addFS(f, "/dev/sda2", "ext4")
	f.AddMatch(executor.FakeMatch{Path: "e2fsck",
		Response: executor.FakeResponse{Err: &exec.Error{Name: "e2fsck", Err: exec.ErrNotFound}}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\npart\npart\n")}})
	set := &v2.Set{Name: "os", Role: "os", Selection: "manual", Raid: "none", Devices: []string{"/dev/sda"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "ext4", Size: "100%", Provider: "partition"},
		}}
	verdict, fails := Classify(context.Background(), f, fscheck.New(fscheck.Options{Runner: f}), set, resolved("/dev/sda"), nil)
	if verdict != VerdictUnknown {
		t.Fatalf("verdict=%s want unknown (failures=%+v)", verdict, fails)
	}
	if len(fails) != 1 || !fails[0].Unknown {
		t.Fatalf("want one Unknown failure, got %+v", fails)
	}
}
