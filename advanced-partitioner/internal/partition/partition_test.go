package partition

import (
	"context"
	"strings"
	"testing"
	"time"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func noSleep(time.Duration) {}

// fakeRunnerWithCleanDisk pre-loads a FakeRunner so the partition-count
// poll sees zero "part" rows for the named disk. Tests that expect the
// build to succeed without waiting for partitions to settle use this.
func fakeRunnerWithCleanDisk(disks ...string) *executor.FakeRunner {
	r := executor.NewFakeRunner(executor.FakeResponse{})
	// Default lsblk -ln -o TYPE response: any number of "part" rows
	// equal to the expected partition count would also satisfy the
	// waitForPartitions check; the simplest is to claim there are
	// many partitions so the first check always succeeds.
	for _, d := range disks {
		r.AddMatch(executor.FakeMatch{
			Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", d},
			Response: executor.FakeResponse{Stdout: []byte("disk\npart\npart\npart\npart\npart\npart\n")},
		})
	}
	return r
}

func TestValidateRAID(t *testing.T) {
	cases := []struct {
		mode   string
		disks  int
		wantOK bool
	}{
		{"none", 1, true},
		{"none", 2, false},
		{"raid0", 2, true},
		{"raid0", 1, false},
		{"raid1", 2, true},
		{"raid1", 3, false},
		{"raid5", 3, true},
		{"raid5", 2, false},
		{"raid6", 4, true},
		{"raid6", 3, false},
		{"raid10", 4, true},
		{"raid10", 5, false}, // odd
		{"raid10", 6, true},
		{"bogus", 1, false},
	}
	for _, c := range cases {
		err := ValidateRAID(c.mode, c.disks)
		if (err == nil) != c.wantOK {
			t.Fatalf("ValidateRAID(%q, %d) ok=%v want=%v err=%v",
				c.mode, c.disks, err == nil, c.wantOK, err)
		}
	}
}

func TestTruncatePartLabel(t *testing.T) {
	cases := map[string]string{
		"root":                  "root",
		"root-data":             "root_data",
		"verylongbutfine":       "verylongbutfine",
		strings.Repeat("a", 40): strings.Repeat("a", 36),
		"data with spaces":      "data_with_spaces",
	}
	for in, want := range cases {
		if got := truncatePartLabel(in); got != want {
			t.Fatalf("truncatePartLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStorageTypeFor(t *testing.T) {
	if storageTypeFor("none") != "8E00" {
		t.Fatal("none should be 8E00")
	}
	for _, m := range []string{"raid0", "raid1", "raid5", "raid6", "raid10"} {
		if storageTypeFor(m) != "FD00" {
			t.Fatalf("%s should be FD00", m)
		}
	}
}

// buildSetSingleDiskUEFI returns a typical 1-disk UEFI OS set with the
// standard layout (efi + boot direct partitions, root LVM).
func buildSetSingleDiskUEFI() *v2.Set {
	return &v2.Set{
		Name:      "os",
		Role:      "os",
		Selection: "manual",
		Raid:      "none",
		Devices:   []string{"/dev/sda"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		},
	}
}

func TestBuildLayoutSingleDiskUEFI(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/sda")

	b := New(Options{Runner: f, Sleep: noSleep})
	layout, err := b.BuildLayout(context.Background(), buildSetSingleDiskUEFI(),
		[]selector.Resolved{{ResolvedPath: "/dev/sda", SizeBytes: 500 * 1024 * 1024 * 1024}},
		"uefi")
	if err != nil {
		t.Fatalf("BuildLayout: %v", err)
	}
	if !equalSlices(layout.EFIPartitions, []string{"/dev/sda1"}) {
		t.Fatalf("EFI partitions wrong: %v", layout.EFIPartitions)
	}
	if !equalSlices(layout.BootPartitions, []string{"/dev/sda2"}) {
		t.Fatalf("Boot partitions wrong: %v", layout.BootPartitions)
	}
	if !equalSlices(layout.StoragePartitions, []string{"/dev/sda3"}) {
		t.Fatalf("Storage partitions wrong: %v", layout.StoragePartitions)
	}

	expect := []string{
		"sgdisk --zap-all /dev/sda",
		"sgdisk -o /dev/sda",
		"sgdisk -n 1:0:+600M -t 1:EF00 -c 1:EFI /dev/sda",
		"sgdisk -n 2:0:+1G -t 2:8300 -c 2:BOOT /dev/sda",
		"sgdisk -n 3:0:0 -t 3:8E00 -c 3:STORAGE /dev/sda",
		"udevadm settle",
	}
	mustHaveCalls(t, f, expect)
}

// A BIOS node whose os set does not ask for a bootloader gets the same GPT
// with the same ESP as a UEFI one, so switching the node to UEFI later needs
// no repartition.
func TestBuildLayoutIdenticalOnBIOS(t *testing.T) {
	disks := []selector.Resolved{{ResolvedPath: "/dev/sda", SizeBytes: 500 * 1024 * 1024 * 1024}}

	fu := fakeRunnerWithCleanDisk("/dev/sda")
	uefi, err := New(Options{Runner: fu, Sleep: noSleep}).BuildLayout(
		context.Background(), buildSetSingleDiskUEFI(), disks, "uefi")
	if err != nil {
		t.Fatalf("BuildLayout(uefi): %v", err)
	}

	fb := fakeRunnerWithCleanDisk("/dev/sda")
	bios, err := New(Options{Runner: fb, Sleep: noSleep}).BuildLayout(
		context.Background(), buildSetSingleDiskUEFI(), disks, "bios")
	if err != nil {
		t.Fatalf("BuildLayout(bios): %v", err)
	}

	if bios.FirmwareMode != "bios" {
		t.Fatalf("FirmwareMode = %q, want bios (the detected mode is recorded)", bios.FirmwareMode)
	}
	if !equalSlices(bios.EFIPartitions, uefi.EFIPartitions) ||
		!equalSlices(bios.BootPartitions, uefi.BootPartitions) ||
		!equalSlices(bios.StoragePartitions, uefi.StoragePartitions) {
		t.Fatalf("layout differs on BIOS:\n uefi=%+v\n bios=%+v", uefi, bios)
	}
	// The ESP is created with the same type code, and no BIOS boot partition
	// appears: nothing asked for a bootloader here.
	mustHaveCalls(t, fb, []string{"sgdisk -n 1:0:+600M -t 1:EF00 -c 1:EFI /dev/sda"})
	for _, c := range fb.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "EF02") {
			t.Fatalf("no BIOS-boot partition may be produced; saw: %+v", c.Args)
		}
	}
}

func TestBuildLayoutRAID1MirrorsBoot(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/sda", "/dev/sdb")
	set := &v2.Set{
		Name:      "os",
		Role:      "os",
		Selection: "manual",
		Raid:      "1",
		Devices:   []string{"/dev/sda", "/dev/sdb"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		},
	}
	b := New(Options{Runner: f, Sleep: noSleep})
	layout, err := b.BuildLayout(context.Background(), set,
		[]selector.Resolved{
			{ResolvedPath: "/dev/sda", SizeBytes: 500 * 1024 * 1024 * 1024},
			{ResolvedPath: "/dev/sdb", SizeBytes: 500 * 1024 * 1024 * 1024},
		},
		"uefi")
	if err != nil {
		t.Fatalf("BuildLayout: %v", err)
	}
	if !layout.MirrorBoot {
		t.Fatal("MirrorBoot should be true for OS+UEFI+raid1")
	}
	// Both EFI partitions should be FD00 (RAID member) not EF00.
	mustHaveCalls(t, f, []string{
		"sgdisk -n 1:0:+600M -t 1:FD00 -c 1:EFI /dev/sda",
		"sgdisk -n 1:0:+600M -t 1:FD00 -c 1:EFI /dev/sdb",
		"sgdisk -n 2:0:+1G -t 2:FD00 -c 2:BOOT /dev/sda",
		"sgdisk -n 2:0:+1G -t 2:FD00 -c 2:BOOT /dev/sdb",
		// Storage partition type is FD00 for raid1.
		"sgdisk -n 3:0:0 -t 3:FD00 -c 3:STORAGE /dev/sda",
		"sgdisk -n 3:0:0 -t 3:FD00 -c 3:STORAGE /dev/sdb",
	})
	if !equalSlices(layout.EFIPartitions, []string{"/dev/sda1", "/dev/sdb1"}) {
		t.Fatalf("EFI partitions wrong: %v", layout.EFIPartitions)
	}
	if !equalSlices(layout.StoragePartitions, []string{"/dev/sda3", "/dev/sdb3"}) {
		t.Fatalf("Storage partitions wrong: %v", layout.StoragePartitions)
	}
}

func TestBuildLayoutDirectPartitionsNoLVM(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/sda")
	set := &v2.Set{
		Name:      "os",
		Role:      "os",
		Selection: "manual",
		Raid:      "none",
		Devices:   []string{"/dev/sda"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"},
			{Name: "swap", Mountpoint: "swap", FS: "swap", Size: "1G", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "partition"},
		},
	}
	b := New(Options{Runner: f, Sleep: noSleep})
	layout, err := b.BuildLayout(context.Background(), set,
		[]selector.Resolved{{ResolvedPath: "/dev/sda", SizeBytes: 500 * 1024 * 1024 * 1024}},
		"uefi")
	if err != nil {
		t.Fatalf("BuildLayout: %v", err)
	}
	if len(layout.StoragePartitions) != 0 {
		t.Fatalf("partition-only set should not produce a STORAGE partition; got %v",
			layout.StoragePartitions)
	}
	if len(layout.PartitionVolumes) != 2 { // swap and root (efi/boot tracked separately)
		t.Fatalf("PartitionVolumes len = %d, want 2: %v", len(layout.PartitionVolumes), layout.PartitionVolumes)
	}
	mustHaveCalls(t, f, []string{
		"sgdisk -n 3:0:+1G -t 3:8200 -c 3:swap /dev/sda", // swap
		"sgdisk -n 4:0:0 -t 4:8300 -c 4:root /dev/sda",   // root 100%
	})
}

func TestBuildLayoutNVMePartitionPaths(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/nvme0n1")
	set := &v2.Set{
		Name:      "os",
		Role:      "os",
		Selection: "manual",
		Raid:      "none",
		Devices:   []string{"/dev/nvme0n1"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		},
	}
	b := New(Options{Runner: f, Sleep: noSleep})
	layout, err := b.BuildLayout(context.Background(), set,
		[]selector.Resolved{{ResolvedPath: "/dev/nvme0n1", SizeBytes: 500 * 1024 * 1024 * 1024}},
		"uefi")
	if err != nil {
		t.Fatalf("BuildLayout: %v", err)
	}
	// NVMe partitions get the "p<N>" suffix.
	if !equalSlices(layout.EFIPartitions, []string{"/dev/nvme0n1p1"}) {
		t.Fatalf("EFI partitions wrong: %v", layout.EFIPartitions)
	}
	if !equalSlices(layout.StoragePartitions, []string{"/dev/nvme0n1p3"}) {
		t.Fatalf("Storage partitions wrong: %v", layout.StoragePartitions)
	}
}

func TestBuildLayoutPreflightRequiresRunner(t *testing.T) {
	b := New(Options{Sleep: noSleep})
	_, err := b.BuildLayout(context.Background(), buildSetSingleDiskUEFI(),
		[]selector.Resolved{{ResolvedPath: "/dev/sda"}}, "uefi")
	if err == nil || !strings.Contains(err.Error(), "Runner is required") {
		t.Fatalf("expected Runner-required error; got %v", err)
	}
}

// TestBuildLayoutRefusesTooSmallDisk pins the disk-fit preflight (BE-G6):
// a disk that cannot hold the fixed-size partitions must be refused with a
// clear error BEFORE any sgdisk runs — not surface 30s later as the generic
// "timed out waiting for partitions".
func TestBuildLayoutRefusesTooSmallDisk(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/sda")
	set := buildSetSingleDiskUEFI()
	set.Volumes = append(set.Volumes,
		v2.Volume{Name: "scratch", Mountpoint: "/scratch", FS: "xfs", Size: "2T", Provider: "partition"})
	b := New(Options{Runner: f, Sleep: noSleep})
	// 100 GiB disk vs a 2T fixed partition (+600M EFI + 1G boot).
	_, err := b.BuildLayout(context.Background(), set,
		[]selector.Resolved{{ResolvedPath: "/dev/sda", SizeBytes: 100 * 1024 * 1024 * 1024}},
		"uefi")
	if err == nil || !strings.Contains(err.Error(), "too small for layout") {
		t.Fatalf("expected too-small refusal; got %v", err)
	}
	if !strings.Contains(err.Error(), "/dev/sda") ||
		!strings.Contains(err.Error(), "need >=") {
		t.Fatalf("error should name the disk and the required size; got %v", err)
	}
	// It must not have touched the disk (no partitioning on refusal).
	for _, c := range f.Calls() {
		if c.Path == "sgdisk" {
			t.Fatalf("too-small refusal must not run sgdisk; saw: %+v", c.Args)
		}
	}
}

// A disk with an unknown size (SizeBytes == 0, e.g. an older resolve record)
// skips the fit check rather than failing on missing data.
func TestBuildLayoutSkipsFitCheckOnUnknownSize(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/sda")
	b := New(Options{Runner: f, Sleep: noSleep})
	if _, err := b.BuildLayout(context.Background(), buildSetSingleDiskUEFI(),
		[]selector.Resolved{{ResolvedPath: "/dev/sda"}}, "uefi"); err != nil {
		t.Fatalf("BuildLayout with unknown disk size: %v", err)
	}
}

func TestPartitionWaitTimesOut(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// Always 0 part rows -> partition wait should time out.
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})
	b := New(Options{Runner: f, Sleep: noSleep, WaitAttempts: 3})
	_, err := b.BuildLayout(context.Background(), buildSetSingleDiskUEFI(),
		[]selector.Resolved{{ResolvedPath: "/dev/sda"}}, "uefi")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout; got %v", err)
	}
}

// --- helpers --------------------------------------------------------------

func mustHaveCalls(t *testing.T, f *executor.FakeRunner, want []string) {
	t.Helper()
	calls := f.Calls()
	got := make([]string, 0, len(calls))
	for _, c := range calls {
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
			t.Fatalf("expected call %q in:\n  %s", w, strings.Join(got, "\n  "))
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] { //nolint:gosec // G602 false positive: i < len(a) == len(b) (checked above)
			return false
		}
	}
	return true
}

// Barrier pin (FTA C2): every redundant OS RAID mode mirrors the boot
// chain, not only raid1 — a raid5/6/10 root that survives disk-0 loss must
// still boot. raid0 (no redundancy) stays unmirrored.
func TestMirrorBootForAllRedundantModes(t *testing.T) {
	for mode, want := range map[string]bool{
		"raid1": true, "raid5": true, "raid6": true, "raid10": true,
		"raid0": false, "none": false,
	} {
		if got := raidIsRedundant(mode); got != want {
			t.Errorf("raidIsRedundant(%s) = %v, want %v", mode, got, want)
		}
	}
}

// buildSetBIOSFirst is buildSetSingleDiskUEFI with the os set asking for a
// bootloader, the one value that means "install one" on BIOS.
func buildSetBIOSFirst() *v2.Set {
	set := buildSetSingleDiskUEFI()
	set.Bootloader = v2.BootloaderFirst
	return set
}

// A BIOS GRUB install needs a BIOS boot partition for its core image and
// refuses without one, so the set that asks for a bootloader on BIOS gets one
// as partition 1 and everything after it shifts up.
func TestBuildLayoutBIOSFirstCreatesBIOSBootPartition(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/sda")

	layout, err := New(Options{Runner: f, Sleep: noSleep}).BuildLayout(
		context.Background(), buildSetBIOSFirst(),
		[]selector.Resolved{{ResolvedPath: "/dev/sda", SizeBytes: 500 * 1024 * 1024 * 1024}},
		"bios")
	if err != nil {
		t.Fatalf("BuildLayout: %v", err)
	}
	if !equalSlices(layout.BIOSBootPartitions, []string{"/dev/sda1"}) {
		t.Fatalf("BIOSBootPartitions = %v, want [/dev/sda1]", layout.BIOSBootPartitions)
	}
	if !equalSlices(layout.EFIPartitions, []string{"/dev/sda2"}) {
		t.Fatalf("EFIPartitions = %v, want [/dev/sda2]", layout.EFIPartitions)
	}
	if !equalSlices(layout.BootPartitions, []string{"/dev/sda3"}) {
		t.Fatalf("BootPartitions = %v, want [/dev/sda3]", layout.BootPartitions)
	}
	if !equalSlices(layout.StoragePartitions, []string{"/dev/sda4"}) {
		t.Fatalf("StoragePartitions = %v, want [/dev/sda4]", layout.StoragePartitions)
	}
	mustHaveCalls(t, f, []string{
		"sgdisk -n 1:0:+1M -t 1:EF02 -c 1:BIOSBOOT /dev/sda",
		"sgdisk -n 2:0:+600M -t 2:EF00 -c 2:EFI /dev/sda",
		"sgdisk -n 3:0:+1G -t 3:8300 -c 3:BOOT /dev/sda",
		"sgdisk -n 4:0:0 -t 4:8E00 -c 4:STORAGE /dev/sda",
	})
}

// Every disk of the set gets one, so GRUB can be written to either member of
// a mirrored boot chain. It is never an md member: it holds a raw core image,
// not a filesystem.
func TestBuildLayoutBIOSFirstOnEveryDisk(t *testing.T) {
	f := fakeRunnerWithCleanDisk("/dev/sda", "/dev/sdb")
	set := buildSetBIOSFirst()
	set.Raid = "raid1"
	set.Devices = []string{"/dev/sda", "/dev/sdb"}

	layout, err := New(Options{Runner: f, Sleep: noSleep}).BuildLayout(
		context.Background(), set, []selector.Resolved{
			{ResolvedPath: "/dev/sda", SizeBytes: 500 * 1024 * 1024 * 1024},
			{ResolvedPath: "/dev/sdb", SizeBytes: 500 * 1024 * 1024 * 1024},
		}, "bios")
	if err != nil {
		t.Fatalf("BuildLayout: %v", err)
	}
	if !equalSlices(layout.BIOSBootPartitions, []string{"/dev/sda1", "/dev/sdb1"}) {
		t.Fatalf("BIOSBootPartitions = %v, want both disks", layout.BIOSBootPartitions)
	}
	mustHaveCalls(t, f, []string{
		"sgdisk -n 1:0:+1M -t 1:EF02 -c 1:BIOSBOOT /dev/sda",
		"sgdisk -n 1:0:+1M -t 1:EF02 -c 1:BIOSBOOT /dev/sdb",
	})
}

// The class, not the instance: only the BIOS set that asks for a bootloader
// gains a partition. Every other combination of firmware and bootloader key
// keeps exactly the partitions and the numbering it has today.
func TestBuildLayoutBIOSBootPartitionOnlyWhenBIOSAsksForOne(t *testing.T) {
	today := []string{
		"sgdisk --zap-all /dev/sda",
		"sgdisk -o /dev/sda",
		"sgdisk -n 1:0:+600M -t 1:EF00 -c 1:EFI /dev/sda",
		"sgdisk -n 2:0:+1G -t 2:8300 -c 2:BOOT /dev/sda",
		"sgdisk -n 3:0:0 -t 3:8E00 -c 3:STORAGE /dev/sda",
		"udevadm settle",
	}
	for _, tc := range []struct {
		firmware   string
		bootloader v2.Bootloader
	}{
		{"uefi", ""},
		{"uefi", v2.BootloaderFirst},
		{"uefi", v2.BootloaderLast},
		{"uefi", v2.BootloaderNone},
		{"bios", ""},
		{"bios", v2.BootloaderLast},
		{"bios", v2.BootloaderNone},
	} {
		name := tc.firmware + "/" + string(tc.bootloader)
		f := fakeRunnerWithCleanDisk("/dev/sda")
		set := buildSetSingleDiskUEFI()
		set.Bootloader = tc.bootloader
		layout, err := New(Options{Runner: f, Sleep: noSleep}).BuildLayout(
			context.Background(), set,
			[]selector.Resolved{{ResolvedPath: "/dev/sda", SizeBytes: 500 * 1024 * 1024 * 1024}},
			tc.firmware)
		if err != nil {
			t.Fatalf("%s: BuildLayout: %v", name, err)
		}
		if layout.BIOSBootPartitions != nil {
			t.Errorf("%s: BIOSBootPartitions = %v, want none", name, layout.BIOSBootPartitions)
		}
		got := make([]string, 0, len(f.Calls()))
		for _, c := range f.Calls() {
			if c.Path != "sgdisk" && c.Path != "udevadm" {
				continue
			}
			got = append(got, c.Path+" "+strings.Join(c.Args, " "))
		}
		if !equalSlices(got, today) {
			t.Errorf("%s: commands changed:\n got=%v\nwant=%v", name, got, today)
		}
	}
}
