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

func TestPredictMemoryRoot(t *testing.T) {
	cfg := &v2.Config{
		Version: 2,
		Sets: []v2.Set{{
			Name: "os", Role: "os",
			Volumes: []v2.Volume{
				{Name: "root", Mountpoint: "/", FS: "tmpfs", Provider: "memory", Options: "defaults", ClearUEFINVRAM: true},
			},
		}},
	}
	got := PredictFilesystemsFromConfig(cfg, nil)
	if len(got.Filesystems) != 1 {
		t.Fatalf("got %d filesystems; want 1", len(got.Filesystems))
	}
	fs := got.Filesystems[0]
	if fs.Mount != "/" || fs.Device != "tmpfs" || fs.FS != "tmpfs" || !fs.ClearUEFINVRAM {
		t.Fatalf("memory fs wrong: %+v", fs)
	}
}

func TestPredictSingleDiskLVM(t *testing.T) {
	cfg := &v2.Config{
		Version: 2,
		Sets: []v2.Set{{
			Name: "os", Role: "os",
			Devices:   []string{"/dev/sda"},
			Selection: "manual", Raid: "none",
			Volumes: []v2.Volume{
				{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
				{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"},
				{Name: "swap", Mountpoint: "swap", FS: "swap", Size: "1G", Provider: "lvm"},
				{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
			},
		}},
	}
	got := PredictFilesystemsFromConfig(cfg, nil)
	// swap is recorded like the create path does (fstab needs it); 4 entries.
	if len(got.Filesystems) != 4 {
		t.Fatalf("got %d filesystems: %+v", len(got.Filesystems), got.Filesystems)
	}
	wantDevs := map[string]string{
		"/boot/efi": "/dev/sda1",
		"/boot":     "/dev/sda2",
		"/":         "/dev/os/root",
		"swap":      "/dev/os/swap",
	}
	for _, fs := range got.Filesystems {
		if w, ok := wantDevs[fs.Mount]; ok && fs.Device != w {
			t.Fatalf("%s device = %q, want %q", fs.Mount, fs.Device, w)
		}
	}
}

func TestPredictNVMePartitionPaths(t *testing.T) {
	cfg := &v2.Config{
		Version: 2,
		Sets: []v2.Set{{
			Name: "os", Role: "os",
			Devices:   []string{"/dev/nvme0n1"},
			Selection: "manual", Raid: "none",
			Volumes: []v2.Volume{
				{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
				{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"},
				{Name: "swap", Mountpoint: "swap", FS: "swap", Size: "1G", Provider: "partition"},
				{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "partition"},
			},
		}},
	}
	got := PredictFilesystemsFromConfig(cfg, nil)
	wantDevs := map[string]string{
		"/boot/efi": "/dev/nvme0n1p1",
		"/boot":     "/dev/nvme0n1p2",
		"/":         "/dev/nvme0n1p4",
		"swap":      "/dev/nvme0n1p3", // never mounted, but recorded for fstab
	}
	for _, fs := range got.Filesystems {
		if w, ok := wantDevs[fs.Mount]; ok && fs.Device != w {
			t.Fatalf("%s device = %q, want %q", fs.Mount, fs.Device, w)
		}
	}
}

func TestPredictKeepsSwapSkipsEmpty(t *testing.T) {
	cfg := &v2.Config{
		Version: 2,
		Sets: []v2.Set{{
			Name: "os", Role: "os",
			Devices:   []string{"/dev/sda"},
			Selection: "manual", Raid: "none",
			Volumes: []v2.Volume{
				{Name: "swap", Mountpoint: "swap", FS: "swap", Size: "1G", Provider: "lvm"},
				{Name: "scratch", Mountpoint: "", FS: "xfs", Size: "1G", Provider: "lvm"},
				{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
			},
		}},
	}
	got := PredictFilesystemsFromConfig(cfg, nil)
	if len(got.Filesystems) != 2 {
		t.Fatalf("expected 2 entries (root + swap; empty mountpoint skipped); got %+v", got.Filesystems)
	}
	var swap *Filesystem
	for i := range got.Filesystems {
		if got.Filesystems[i].Mount == "swap" {
			swap = &got.Filesystems[i]
		}
	}
	if swap == nil || swap.FS != "swap" || swap.Device != "/dev/os/swap" {
		t.Fatalf("swap must be predicted like the create path records it, got %+v", got.Filesystems)
	}
}

func TestPredictMultiSetMemoryPlusData(t *testing.T) {
	cfg := &v2.Config{
		Version: 2,
		Sets: []v2.Set{
			{
				Name: "os", Role: "os",
				Volumes: []v2.Volume{
					{Name: "root", Mountpoint: "/", FS: "tmpfs", Provider: "memory"},
				},
			},
			{
				Name: "data", Role: "data",
				Devices:   []string{"/dev/sdb"},
				Selection: "manual", Raid: "none",
				Volumes: []v2.Volume{
					{Name: "data", Mountpoint: "/data", FS: "xfs", Size: "100%", Provider: "lvm"},
				},
			},
		},
	}
	got := PredictFilesystemsFromConfig(cfg, nil)
	if len(got.Filesystems) != 2 {
		t.Fatalf("expected 2 filesystems; got %d", len(got.Filesystems))
	}
	if got.Filesystems[1].Device != "/dev/data/data" {
		t.Fatalf("data LV path wrong: %+v", got.Filesystems[1])
	}
}

// TestPredictRAID1BootOnMD: a raid1 os set's ESP/boot must resolve to the md
// devices, not raw partitions (mounting the raw partition fails — it is a
// linux_raid_member). Mirrors the health-probe device resolution.
func TestPredictRAID1BootOnMD(t *testing.T) {
	set := &v2.Set{
		Name: "os", Role: "os", Selection: "manual", Raid: "1",
		Devices: []string{"/dev/sda", "/dev/sdb"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Provider: "partition", Size: "600M"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Provider: "partition", Size: "1G"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"},
		},
	}
	got := map[string]string{}
	for _, f := range PredictFilesystemsForSet(set, false).Filesystems {
		got[f.Mount] = f.Device
	}
	if got["/boot/efi"] != "/dev/md/os-efi" {
		t.Errorf("/boot/efi: got %q want /dev/md/os-efi", got["/boot/efi"])
	}
	if got["/boot"] != "/dev/md/os-boot" {
		t.Errorf("/boot: got %q want /dev/md/os-boot", got["/boot"])
	}
	if got["/"] != "/dev/os/root" {
		t.Errorf("/ (lvm): got %q want /dev/os/root", got["/"])
	}
}

// The mount-existing predictor and the create path must describe the same
// filesystems (mount, device, fs) -- swap included -- or a refreshed node's
// fstab differs from a freshly built one's.
func TestPredictMatchesCreatePathIncludingSwap(t *testing.T) {
	set := &v2.Set{
		Name: "os", Role: "os", Selection: "manual", Raid: "none", Devices: []string{"/dev/sda"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "pswap", Mountpoint: "swap", FS: "swap", Size: "2G", Provider: "partition"},
			{Name: "swap", Mountpoint: "swap", FS: "swap", Size: "1G", Provider: "lvm"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		},
	}
	created, err := New(Options{Runner: executor.NewFakeRunner(executor.FakeResponse{})}).MakeFilesystems(
		context.Background(), set,
		partition.Layout{
			EFIPartitions:    []string{"/dev/sda1"},
			PartitionVolumes: []partition.PartitionVolume{{Name: "pswap", Mount: "swap", FS: "swap", Device: "/dev/sda2"}},
		},
		raid.State{RAIDMode: "none"}, lvm.State{VGName: "os", PVDevice: "/dev/sda3"})
	if err != nil {
		t.Fatalf("MakeFilesystems: %v", err)
	}
	key := func(fs Filesystems) map[string]bool {
		out := map[string]bool{}
		for _, f := range fs.Filesystems {
			dev := f.Device
			if strings.HasPrefix(dev, "/dev/mapper/") { // create path uses the mapper form
				dev = "/dev/os/" + strings.TrimPrefix(dev, "/dev/mapper/os-")
			}
			out[f.Mount+" "+dev+" "+f.FS] = true
		}
		return out
	}
	want, got := key(created), key(PredictFilesystemsForSet(set, false))
	if len(want) != len(got) {
		t.Fatalf("create=%v predict=%v", want, got)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("predictor misses %q (create=%v predict=%v)", k, want, got)
		}
	}
}
