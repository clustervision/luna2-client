package fs

import (
	"context"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
)

func TestMakeFilesystemsSingleDiskUEFI(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		Name: "os", Role: "os",
		Volumes: []v2.Volume{
			{Name: "uefi", Provider: "partition", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M"},
			{Name: "boot", Provider: "partition", Mountpoint: "/boot", FS: "xfs", Size: "1G"},
			{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"},
		},
	}
	layout := partition.Layout{
		Role:           "os",
		FirmwareMode:   "uefi",
		RAIDMode:       "none",
		BootFS:         "xfs",
		EFIPartitions:  []string{"/dev/sda1"},
		BootPartitions: []string{"/dev/sda2"},
	}
	lvmState := lvm.State{VGName: "os", PVDevice: "/dev/sda3"}
	got, err := New(Options{Runner: f, MkfsFatName: "mkfs.fat"}).MakeFilesystems(
		context.Background(), set, layout, raid.State{RAIDMode: "none"}, lvmState)
	if err != nil {
		t.Fatalf("MakeFilesystems: %v", err)
	}
	if len(got.Filesystems) != 3 {
		t.Fatalf("expected 3 filesystems; got %d: %+v", len(got.Filesystems), got.Filesystems)
	}
	if got.Filesystems[0].Mount != "/boot/efi" || got.Filesystems[0].FS != "vfat" {
		t.Fatalf("efi fs wrong: %+v", got.Filesystems[0])
	}
	if got.Filesystems[2].Mount != "/" || got.Filesystems[2].Device != "/dev/mapper/os-root" {
		t.Fatalf("root fs wrong: %+v", got.Filesystems[2])
	}

	expectCalls := []string{
		"wipefs -af /dev/sda1",
		"mkfs.fat -F 32 /dev/sda1",
		"wipefs -af /dev/sda2",
		"mkfs.xfs -f /dev/sda2",
		"wipefs -af /dev/mapper/os-root",
		"mkfs.xfs -f /dev/mapper/os-root",
	}
	mustHaveCalls(t, f, expectCalls)
}

func TestMakeFilesystemsPrefersMDForMirroredBoot(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		Name: "os", Role: "os",
		Volumes: []v2.Volume{
			{Name: "uefi", Provider: "partition", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M"},
			{Name: "boot", Provider: "partition", Mountpoint: "/boot", FS: "xfs", Size: "1G"},
			{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"},
		},
	}
	layout := partition.Layout{
		Role:           "os",
		FirmwareMode:   "uefi",
		RAIDMode:       "raid1",
		MirrorBoot:     true,
		BootFS:         "xfs",
		EFIPartitions:  []string{"/dev/sda1", "/dev/sdb1"},
		BootPartitions: []string{"/dev/sda2", "/dev/sdb2"},
	}
	md := raid.State{
		RAIDMode:     "raid1",
		MDDevice:     "/dev/md/os",
		EFIMDDevice:  "/dev/md/os-efi",
		BootMDDevice: "/dev/md/os-boot",
	}
	got, err := New(Options{Runner: f, MkfsFatName: "mkfs.fat"}).MakeFilesystems(
		context.Background(), set, layout, md, lvm.State{VGName: "os"})
	if err != nil {
		t.Fatalf("MakeFilesystems: %v", err)
	}
	// EFI and /boot should now point at the md devices.
	wantDev := map[string]string{
		"/boot/efi": "/dev/md/os-efi",
		"/boot":     "/dev/md/os-boot",
		"/":         "/dev/mapper/os-root",
	}
	for _, fs := range got.Filesystems {
		if w, ok := wantDev[fs.Mount]; ok && fs.Device != w {
			t.Fatalf("%s device = %q, want %q", fs.Mount, fs.Device, w)
		}
	}
}

func TestMakeFilesystemsDirectPartitionsAndSwap(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		Name: "os", Role: "os",
		Volumes: []v2.Volume{
			{Name: "uefi", Provider: "partition", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M"},
			{Name: "boot", Provider: "partition", Mountpoint: "/boot", FS: "xfs", Size: "1G"},
			{Name: "swap", Provider: "partition", Mountpoint: "swap", FS: "swap", Size: "1G"},
			{Name: "root", Provider: "partition", Mountpoint: "/", FS: "xfs", Size: "100%"},
		},
	}
	layout := partition.Layout{
		Role:           "os",
		FirmwareMode:   "uefi",
		RAIDMode:       "none",
		BootFS:         "xfs",
		EFIPartitions:  []string{"/dev/sda1"},
		BootPartitions: []string{"/dev/sda2"},
		PartitionVolumes: []partition.PartitionVolume{
			{Name: "swap", Mount: "swap", FS: "swap", Device: "/dev/sda3"},
			{Name: "root", Mount: "/", FS: "xfs", Device: "/dev/sda4"},
		},
	}
	_, err := New(Options{Runner: f, MkfsFatName: "mkfs.fat"}).MakeFilesystems(
		context.Background(), set, layout, raid.State{RAIDMode: "none"}, lvm.State{})
	if err != nil {
		t.Fatalf("MakeFilesystems: %v", err)
	}
	mustHaveCalls(t, f, []string{
		"mkswap /dev/sda3",
		"mkfs.xfs -f /dev/sda4",
	})
}

func TestMakeFilesystemsExt4(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		Name: "data", Role: "data",
		Volumes: []v2.Volume{
			{Name: "data", Provider: "lvm", Mountpoint: "/data", FS: "ext4", Size: "100%"},
		},
	}
	layout := partition.Layout{Role: "data", RAIDMode: "none"}
	lvmState := lvm.State{VGName: "data"}
	_, err := New(Options{Runner: f, MkfsFatName: "mkfs.fat"}).MakeFilesystems(
		context.Background(), set, layout, raid.State{RAIDMode: "none"}, lvmState)
	if err != nil {
		t.Fatalf("MakeFilesystems: %v", err)
	}
	mustHaveCalls(t, f, []string{
		"mkfs.ext4 -F /dev/mapper/data-data",
	})
}

func TestMakeFilesystemsUnsupportedFS(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
		{Name: "weird", Provider: "lvm", Mountpoint: "/weird", FS: "btrfs", Size: "100%"},
	}}
	_, err := New(Options{Runner: f, MkfsFatName: "mkfs.fat"}).MakeFilesystems(
		context.Background(), set, partition.Layout{}, raid.State{},
		lvm.State{VGName: "os"})
	if err == nil || !strings.Contains(err.Error(), "unsupported filesystem") {
		t.Fatalf("expected unsupported-fs error; got %v", err)
	}
}

func TestMemoryFilesystems(t *testing.T) {
	set := &v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
		{Name: "root", Provider: "memory", Mountpoint: "/", FS: "tmpfs", Options: "defaults", ClearUEFINVRAM: true},
	}}
	got := MemoryFilesystems(set)
	if len(got.Filesystems) != 1 {
		t.Fatalf("expected 1 filesystem; got %d", len(got.Filesystems))
	}
	fs := got.Filesystems[0]
	if fs.Mount != "/" || fs.Device != "tmpfs" || fs.FS != "tmpfs" || fs.Options != "defaults" {
		t.Fatalf("memory fs wrong: %+v", fs)
	}
	if !fs.ClearUEFINVRAM {
		t.Fatal("ClearUEFINVRAM flag should propagate")
	}
}

func TestNewPicksVFatWhenFatMissing(t *testing.T) {
	avail := func(name string) bool { return name == "mkfs.vfat" }
	b := New(Options{Runner: executor.NewFakeRunner(executor.FakeResponse{}), CommandAvailable: avail})
	if b.o.MkfsFatName != "mkfs.vfat" {
		t.Fatalf("MkfsFatName = %q, want mkfs.vfat", b.o.MkfsFatName)
	}
}

func mustHaveCalls(t *testing.T, f *executor.FakeRunner, want []string) {
	t.Helper()
	got := make([]string, 0, len(f.Calls()))
	for _, c := range f.Calls() {
		got = append(got, c.Path+" "+strings.Join(c.Args, " "))
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q in:\n  %s", w, strings.Join(got, "\n  "))
		}
	}
}

// The ESP is formatted whatever the firmware. The health probe and the
// mount-existing prediction both expect a vfat filesystem on every OS set that
// declares one, so skipping it on BIOS would make the next boot classify the
// set as broken and rebuild it forever.
func TestMakeFilesystemsFormatsESPOnBIOS(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		Name: "os", Role: "os",
		Volumes: []v2.Volume{
			{Name: "uefi", Provider: "partition", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M"},
			{Name: "boot", Provider: "partition", Mountpoint: "/boot", FS: "xfs", Size: "1G"},
			{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"},
		},
	}
	layout := partition.Layout{
		Role:           "os",
		FirmwareMode:   "bios",
		RAIDMode:       "none",
		BootFS:         "xfs",
		EFIPartitions:  []string{"/dev/sda1"},
		BootPartitions: []string{"/dev/sda2"},
	}
	lvmState := lvm.State{VGName: "os", PVDevice: "/dev/sda3"}
	got, err := New(Options{Runner: f, MkfsFatName: "mkfs.fat"}).MakeFilesystems(
		context.Background(), set, layout, raid.State{RAIDMode: "none"}, lvmState)
	if err != nil {
		t.Fatalf("MakeFilesystems: %v", err)
	}
	if len(got.Filesystems) != 3 {
		t.Fatalf("expected 3 filesystems on BIOS; got %d: %+v", len(got.Filesystems), got.Filesystems)
	}
	if got.Filesystems[0].Mount != "/boot/efi" || got.Filesystems[0].FS != "vfat" {
		t.Fatalf("efi fs wrong: %+v", got.Filesystems[0])
	}
	mustHaveCalls(t, f, []string{"wipefs -af /dev/sda1", "mkfs.fat -F 32 /dev/sda1"})
}
