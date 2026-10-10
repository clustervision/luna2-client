package storagehealth

import (
	"context"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
)

func withAllDevices(t *testing.T) {
	t.Helper()
	orig := blockDeviceExistsFn
	blockDeviceExistsFn = func(string) bool { return true }
	t.Cleanup(func() { blockDeviceExistsFn = orig })
}

func addFS(f *executor.FakeRunner, dev, fs string) {
	f.AddMatch(executor.FakeMatch{Path: "blkid", ArgsPrefix: []string{"-s", "TYPE", "-o", "value", dev},
		Response: executor.FakeResponse{Stdout: []byte(fs + "\n")}})
}

// A healthy BIOS (bootloader grub) os set: vda1 = EF02, vda2 = ESP, vda3 =
// /boot, vda4 = LVM PV. The probe used to check the ESP on vda1 and /boot on
// vda2 ("expected=xfs actual=vfat") and judged it broken.
func TestVerifyDiskSetBIOSBootLayoutHealthy(t *testing.T) {
	withAllDevices(t)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "blkid", ArgsPrefix: []string{"-p", "-s", "PART_ENTRY_TYPE", "-o", "value", "/dev/vda1"},
		Response: executor.FakeResponse{Stdout: []byte(biosBootGUID + "\n")}})
	addFS(f, "/dev/vda2", "vfat")
	addFS(f, "/dev/vda3", "xfs")
	addFS(f, "/dev/os/root", "xfs")
	set := &v2.Set{Name: "os", Role: "os", Raid: "none", Bootloader: v2.BootloaderFirst, Devices: []string{"/dev/vda"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "2G", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		}}
	checker := fscheck.New(fscheck.Options{Runner: f})
	if fails := VerifyDiskSet(context.Background(), f, checker, set, resolved("/dev/vda"), nil); len(fails) != 0 {
		t.Fatalf("healthy BIOS layout judged unhealthy: %+v", fails)
	}
	for _, c := range f.Calls() {
		if c.Path == "fsck.vfat" && c.Args[len(c.Args)-1] != "/dev/vda2" {
			t.Fatalf("ESP checked on %v, want /dev/vda2", c.Args)
		}
	}
}

// A raid1 os set: the md arrays must be assembled from explicit members BEFORE
// vgchange, because md auto-assembly is off in the installer. The probe used to
// run vgchange straight away, which failed ("exit status 5") on every healthy
// mirror.
func TestVerifyDiskSetAssemblesRaidBeforeVgchange(t *testing.T) {
	withAllDevices(t)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	for _, d := range []string{"/dev/sda", "/dev/sdb"} {
		f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", d},
			Response: executor.FakeResponse{Stdout: []byte(strings.TrimPrefix(d, "/dev/") + " disk\n" +
				strings.TrimPrefix(d, "/dev/") + "3 part\n")}})
	}
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
		Response: executor.FakeResponse{Stdout: []byte("           Name : any:os  (local to host any)\n")}})
	addFS(f, "/dev/md/os-efi", "vfat")
	addFS(f, "/dev/md/os-boot", "xfs")
	addFS(f, "/dev/os/root", "xfs")
	set := &v2.Set{Name: "os", Role: "os", Raid: "1", Devices: []string{"/dev/sda", "/dev/sdb"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "2G", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		}}
	checker := fscheck.New(fscheck.Options{Runner: f})
	if fails := VerifyDiskSet(context.Background(), f, checker, set, resolved("/dev/sda", "/dev/sdb"), nil); len(fails) != 0 {
		t.Fatalf("healthy raid1 set judged unhealthy: %+v", fails)
	}
	assembled, vgAt := -1, -1
	for i, c := range f.Calls() {
		if c.Path == "mdadm" && len(c.Args) > 0 && c.Args[0] == "--assemble" && assembled < 0 {
			assembled = i
		}
		if c.Path == "vgchange" && vgAt < 0 {
			vgAt = i
		}
	}
	if assembled < 0 || vgAt < 0 || assembled > vgAt {
		t.Fatalf("want mdadm --assemble before vgchange; assemble at %d, vgchange at %d", assembled, vgAt)
	}
}

func TestHasBIOSBootPartition(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "blkid", ArgsPrefix: []string{"-p", "-s", "PART_ENTRY_TYPE", "-o", "value", "/dev/nvme0n1p1"},
		Response: executor.FakeResponse{Stdout: []byte(strings.ToUpper(biosBootGUID) + "\n")}})
	if !HasBIOSBootPartition(context.Background(), f, "/dev/nvme0n1") {
		t.Fatal("EF02 on nvme0n1p1 not detected (case-insensitive GUID, nvme p-suffix)")
	}
	g := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte("c12a7328-f81f-11d2-ba4b-00a0c93ec93b\n")})
	if HasBIOSBootPartition(context.Background(), g, "/dev/vda") {
		t.Fatal("an ESP on partition 1 is not a BIOS boot partition")
	}
}
