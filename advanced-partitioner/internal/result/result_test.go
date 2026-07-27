package result

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	fsmod "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
)

func TestAssembleMemorySet(t *testing.T) {
	set := &v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
		{Name: "root", Mountpoint: "/", FS: "tmpfs", Provider: "memory", Options: "defaults", ClearUEFINVRAM: true},
	}}
	in := AssembleInput{
		Set:         set,
		Filesystems: fsmod.MemoryFilesystems(set),
		Mount:       mount.Result{MountedTargetRoot: "/sysroot"},
	}
	got := Assemble(in)
	if got.StorageType != "memory" {
		t.Fatalf("StorageType = %q", got.StorageType)
	}
	if !got.ClearUEFINVRAM {
		t.Fatalf("ClearUEFINVRAM should propagate from the memory volume")
	}
	if got.RootDevice != "tmpfs" {
		t.Fatalf("RootDevice = %q", got.RootDevice)
	}
	if got.BootMethod != "none" {
		t.Fatalf("BootMethod = %q", got.BootMethod)
	}
	if got.MDRaidDevice != nil || got.EFIMDDevice != nil || got.BootMDDevice != nil {
		t.Fatalf("md device fields should be nil: %+v", got)
	}
}

func TestAssembleLVMSet(t *testing.T) {
	set := &v2.Set{
		Name: "os", Role: "os", Selection: "manual", Raid: "1",
		Devices: []string{"/dev/sda", "/dev/sdb"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		},
	}
	in := AssembleInput{
		Set:          set,
		FirmwareMode: "uefi",
		Selected: []selector.Resolved{
			{ConfiguredPath: "/dev/sda", ResolvedPath: "/dev/sda", SizeBytes: 100 << 30},
			{ConfiguredPath: "/dev/sdb", ResolvedPath: "/dev/sdb", SizeBytes: 100 << 30},
		},
		Layout: partition.Layout{
			FirmwareMode: "uefi", RAIDMode: "raid1", MirrorBoot: true,
			EFIPartitions:  []string{"/dev/sda1", "/dev/sdb1"},
			BootPartitions: []string{"/dev/sda2", "/dev/sdb2"},
		},
		RAID: raid.State{
			RAIDMode: "raid1", MDDevice: "/dev/md/os", MetadataVersion: "1.2",
			Members:     []string{"/dev/sda3", "/dev/sdb3"},
			EFIMDDevice: "/dev/md/os-efi", EFIMetadataVersion: "1.0",
			BootMDDevice: "/dev/md/os-boot", BootMetadataVersion: "1.0",
		},
		LVM: lvm.State{VGName: "os", PVDevice: "/dev/md/os"},
		Filesystems: fsmod.Filesystems{
			Role: "os", FirmwareMode: "uefi", RAIDMode: "raid1",
			Filesystems: []fsmod.Filesystem{
				{Mount: "/boot/efi", Device: "/dev/md/os-efi", FS: "vfat"},
				{Mount: "/boot", Device: "/dev/md/os-boot", FS: "xfs"},
				{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs"},
			},
		},
		Mount: mount.Result{MountedTargetRoot: "/sysroot", RootDevice: "/dev/mapper/os-root"},
	}
	got := Assemble(in)
	if got.StorageType != "lvm" {
		t.Fatalf("StorageType = %q", got.StorageType)
	}
	if got.RAIDMode != "raid1" {
		t.Fatalf("RAIDMode = %q", got.RAIDMode)
	}
	if got.RootDevice != "/dev/mapper/os-root" {
		t.Fatalf("RootDevice = %q", got.RootDevice)
	}
	if got.LVM.VGName == nil || *got.LVM.VGName != "os" {
		t.Fatalf("LVM.VGName wrong: %+v", got.LVM)
	}
	if got.MDRaidDevice == nil || *got.MDRaidDevice != "/dev/md/os" {
		t.Fatalf("MDRaidDevice wrong: %v", got.MDRaidDevice)
	}
	if got.EFIMDDevice == nil || *got.EFIMDDevice != "/dev/md/os-efi" {
		t.Fatalf("EFIMDDevice wrong: %v", got.EFIMDDevice)
	}
	if got.BootMethod != "grub" || got.BootFirmware != "uefi" || got.RegisterNVRAM != "auto" {
		t.Fatalf("boot.* wrong (UEFI-only constants expected): %+v", got)
	}
	wantBoot := []string{"/dev/sda", "/dev/sdb"}
	if len(got.BootDevices) != len(wantBoot) {
		t.Fatalf("BootDevices = %v, want %v", got.BootDevices, wantBoot)
	}
	for i := range wantBoot {
		if got.BootDevices[i] != wantBoot[i] {
			t.Fatalf("BootDevices[%d] = %q, want %q", i, got.BootDevices[i], wantBoot[i])
		}
	}
}

func TestRecordBlockUUIDs(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "blkid", ArgsPrefix: []string{"-s", "UUID", "-o", "value", "/dev/sda1"},
		Response: executor.FakeResponse{Stdout: []byte("ABCD-1234\n")},
	})
	f.AddMatch(executor.FakeMatch{
		Path: "blkid", ArgsPrefix: []string{"-s", "UUID", "-o", "value", "/dev/mapper/os-root"},
		Response: executor.FakeResponse{Stdout: []byte("11111111-2222-3333-4444-555555555555\n")},
	})
	res := &StorageResult{
		Filesystems: []Filesystem{
			{Mount: "/boot/efi", Device: "/dev/sda1", FS: "vfat"},
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs"},
			{Mount: "swap", Device: "tmpfs", FS: "tmpfs"},
		},
	}
	if err := RecordBlockUUIDs(context.Background(), f, res); err != nil {
		t.Fatalf("RecordBlockUUIDs: %v", err)
	}
	if res.Filesystems[0].UUID != "ABCD-1234" {
		t.Fatalf("efi UUID = %q", res.Filesystems[0].UUID)
	}
	if !strings.HasPrefix(res.Filesystems[1].UUID, "11111111") {
		t.Fatalf("root UUID = %q", res.Filesystems[1].UUID)
	}
	if res.Filesystems[2].UUID != "" {
		t.Fatalf("tmpfs UUID should remain empty; got %q", res.Filesystems[2].UUID)
	}
}

func TestWriteAutoSingle(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "out", "storage-result.json")
	res := StorageResult{
		Version: 2, Name: "os", Role: "os", StorageType: "memory", BootMethod: "none",
		MountedTargetRoot: "/sysroot",
	}
	if err := WriteAuto(path, []StorageResult{res}); err != nil {
		t.Fatalf("WriteAuto: %v", err)
	}
	body, err := os.ReadFile(path) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	var decoded StorageResult
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Name != "os" {
		t.Fatalf("decoded Name = %q", decoded.Name)
	}
}

func TestWriteAutoMulti(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "storage-result.json")
	if err := WriteAuto(path, []StorageResult{
		{Name: "os", Role: "os", StorageType: "memory", MountedTargetRoot: "/sysroot"},
		{Name: "data", Role: "data", StorageType: "lvm", MountedTargetRoot: "/sysroot"},
	}); err != nil {
		t.Fatalf("WriteAuto: %v", err)
	}
	body, err := os.ReadFile(path) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	var decoded MultiSet
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Version != 2 || len(decoded.Sets) != 2 {
		t.Fatalf("multi shape wrong: %+v", decoded)
	}
}

func TestWriteResolved(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "storage-resolved.json")
	if err := WriteResolved(path, []ResolvedSet{
		{Name: "os", Role: "os", FirmwareMode: "uefi", SelectorType: "device_paths",
			StorageType: "lvm", RAIDMode: "raid1",
			SelectedDisks: []selector.Resolved{{ConfiguredPath: "/dev/sda", ResolvedPath: "/dev/sda"}}},
	}); err != nil {
		t.Fatalf("WriteResolved: %v", err)
	}
	body, err := os.ReadFile(path) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	var got ResolvedAggregate
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != 2 || len(got.Sets) != 1 || got.Sets[0].Name != "os" {
		t.Fatalf("wrong shape: %+v", got)
	}
}
