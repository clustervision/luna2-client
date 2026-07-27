package fs

import (
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
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
	got := PredictFilesystemsFromConfig(cfg)
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
	got := PredictFilesystemsFromConfig(cfg)
	// swap and empty mounts are skipped; 3 entries.
	if len(got.Filesystems) != 3 {
		t.Fatalf("got %d filesystems: %+v", len(got.Filesystems), got.Filesystems)
	}
	wantDevs := map[string]string{
		"/boot/efi": "/dev/sda1",
		"/boot":     "/dev/sda2",
		"/":         "/dev/os/root",
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
	got := PredictFilesystemsFromConfig(cfg)
	wantDevs := map[string]string{
		"/boot/efi": "/dev/nvme0n1p1",
		"/boot":     "/dev/nvme0n1p2",
		"/":         "/dev/nvme0n1p4", // swap claims p3 even though it's skipped from the mount list
	}
	for _, fs := range got.Filesystems {
		if w, ok := wantDevs[fs.Mount]; ok && fs.Device != w {
			t.Fatalf("%s device = %q, want %q", fs.Mount, fs.Device, w)
		}
	}
}

func TestPredictSkipsSwapAndEmpty(t *testing.T) {
	cfg := &v2.Config{
		Version: 2,
		Sets: []v2.Set{{
			Name: "os", Role: "os",
			Devices:   []string{"/dev/sda"},
			Selection: "manual", Raid: "none",
			Volumes: []v2.Volume{
				{Name: "swap", Mountpoint: "swap", FS: "swap", Size: "1G", Provider: "lvm"},
				{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
			},
		}},
	}
	got := PredictFilesystemsFromConfig(cfg)
	if len(got.Filesystems) != 1 {
		t.Fatalf("expected 1 entry (swap skipped); got %d", len(got.Filesystems))
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
	got := PredictFilesystemsFromConfig(cfg)
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
	for _, f := range PredictFilesystemsForSet(set).Filesystems {
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
