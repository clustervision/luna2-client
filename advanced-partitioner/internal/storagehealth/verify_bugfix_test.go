package storagehealth

import (
	"context"
	"fmt"
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

// addVG stubs the VG probe: VG name (uuid) with one PV per entry, each pv on
// the given disk's lsblk listing.
func addVG(f *executor.FakeRunner, pvs string, disks map[string]string) {
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		Response: executor.FakeResponse{Stdout: []byte(pvs)}})
	for d, kn := range disks {
		f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", d},
			Response: executor.FakeResponse{Stdout: []byte(kn)}})
	}
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
	addVG(f, "  OSUUID os /dev/vda4\n", map[string]string{"/dev/vda": "vda\nvda1\nvda2\nvda3\nvda4\n"})
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
	addVG(f, "  OSUUID os /dev/md127\n", map[string]string{
		"/dev/sda": "sda\nsda3\nmd127\n", "/dev/sdb": "sdb\nsdb3\nmd127\n"})
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

// A same-named VG on a disk OUTSIDE the set (an old OS disk still attached)
// must not make the probe ambiguous: the set's own VG is activated by UUID,
// the foreign one is never activated, and the set is healthy.
func TestVerifyDiskSetIgnoresForeignSameNameVG(t *testing.T) {
	withAllDevices(t)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	addFS(f, "/dev/vda1", "vfat")
	addFS(f, "/dev/os/root", "xfs")
	addVG(f, "  OSUUID os /dev/vda2\n  OLDUUID os /dev/vdz2\n", map[string]string{"/dev/vda": "vda\nvda1\nvda2\n"})
	set := &v2.Set{Name: "os", Role: "os", Raid: "none", Devices: []string{"/dev/vda"},
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		}}
	checker := fscheck.New(fscheck.Options{Runner: f})
	if fails := VerifyDiskSet(context.Background(), f, checker, set, resolved("/dev/vda"), nil); len(fails) != 0 {
		t.Fatalf("a foreign same-name VG made the healthy set unhealthy: %+v", fails)
	}
	sawOwn := false
	for _, c := range f.Calls() {
		if c.Path != "vgchange" {
			continue
		}
		joined := strings.Join(c.Args, " ")
		if joined == "-ay --select vg_uuid=OSUUID" {
			sawOwn = true
			continue
		}
		t.Fatalf("only the set's own VG may be activated, by uuid; got vgchange %s", joined)
	}
	if !sawOwn {
		t.Fatal("own VG not activated")
	}
}

// A data set gets the preen, which replays the journal a power cut leaves,
// whether lpart assembles its array, the initramfs already did, or it has none.
func TestVerifyDiskSetExtDataProbe(t *testing.T) {
	for _, c := range []struct {
		name, raid string
		disks      []string
		pv         string
		running    bool
		want       string
	}{
		{"raid assembled by lpart", "1", []string{"/dev/sda", "/dev/sdb"}, "/dev/md127", false, "-f -p /dev/data/d"},
		{"raid already running", "1", []string{"/dev/sda", "/dev/sdb"}, "/dev/md127", true, "-f -p /dev/data/d"},
		{"non-raid", "none", []string{"/dev/vda"}, "/dev/vda1", false, "-f -p /dev/data/d"},
	} {
		t.Run(c.name, func(t *testing.T) {
			withAllDevices(t)
			f := executor.NewFakeRunner(executor.FakeResponse{})
			kn := map[string]string{}
			for _, d := range c.disks {
				b := strings.TrimPrefix(d, "/dev/")
				f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", d},
					Response: executor.FakeResponse{Stdout: []byte(b + " disk\n" + b + "1 part\n")}})
				kn[d] = b + "\n" + b + "1\n" + strings.TrimPrefix(c.pv, "/dev/") + "\n"
				if c.running {
					f.AddMatch(executor.FakeMatch{Path: "ls", ArgsPrefix: []string{"-1", "/sys/class/block/" + b + "1/holders"},
						Response: executor.FakeResponse{Stdout: []byte("md127\n")}})
					f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/md127/md/dev-" + b + "1/state"},
						Response: executor.FakeResponse{Stdout: []byte("in_sync\n")}})
				}
			}
			f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/md127/md/array_state"},
				Response: executor.FakeResponse{Stdout: []byte("clean\n")}})
			f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/md127/md/degraded"},
				Response: executor.FakeResponse{Stdout: []byte("0\n")}})
			f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
				Response: executor.FakeResponse{Stdout: []byte("           Name : any:data  (local to host any)\n")}})
			addFS(f, "/dev/data/d", "ext4")
			addVG(f, "  DUUID data "+c.pv+"\n", kn)
			set := &v2.Set{Name: "data", Role: "data", Raid: c.raid, Devices: c.disks,
				Volumes: []v2.Volume{{Name: "d", Mountpoint: "/data", FS: "ext4", Size: "100%", Provider: "lvm"}}}
			checker := fscheck.New(fscheck.Options{Runner: f})
			if fails := VerifyDiskSet(context.Background(), f, checker, set, resolved(c.disks...), nil); len(fails) != 0 {
				t.Fatalf("clean data set judged unhealthy: %+v", fails)
			}
			var got []string
			for _, call := range f.Calls() {
				if call.Path == "e2fsck" {
					got = append(got, strings.Join(call.Args, " "))
				}
			}
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("e2fsck ran %q, want exactly %q", got, c.want)
			}
		})
	}
}

// replayRunner answers xfs_repair -n with the dirty-log alert until the
// filesystem has been mounted once, as a power cut leaves an XFS volume.
type replayRunner struct {
	*executor.FakeRunner
	mounted bool
}

func (r *replayRunner) Run(ctx context.Context, spec executor.CommandSpec) (executor.CommandResult, error) {
	if spec.Path == "mount" {
		r.mounted = true
	}
	if spec.Path == "xfs_repair" && len(spec.Args) > 0 && spec.Args[0] == "-n" && !r.mounted {
		return executor.CommandResult{ExitCode: 1, Stderr: []byte("ALERT: The filesystem has valuable metadata changes in a log\n")}, fmt.Errorf("exit status 1")
	}
	return r.FakeRunner.Run(ctx, spec)
}

// A data set written to before a power cut is healthy: the probe replays its
// XFS log like the os set's, instead of parking the node for an operator.
func TestVerifyDiskSetDataSetAfterPowerCut(t *testing.T) {
	withAllDevices(t)
	r := &replayRunner{FakeRunner: executor.NewFakeRunner(executor.FakeResponse{})}
	addFS(r.FakeRunner, "/dev/sdb1", "xfs")
	set := &v2.Set{Name: "data", Role: "data", Raid: "none", Devices: []string{"/dev/sdb"},
		Volumes: []v2.Volume{{Name: "data", Mountpoint: "/srv/data", FS: "xfs", Size: "100%", Provider: "partition"}}}
	checker := fscheck.New(fscheck.Options{Runner: r, MountDir: t.TempDir()})
	if fails := VerifyDiskSet(context.Background(), r, checker, set, resolved("/dev/sdb"), nil); len(fails) != 0 {
		t.Fatalf("data set after a power cut judged %+v, want healthy after its log replay", fails)
	}
	if !r.mounted {
		t.Fatal("the dirty log was not replayed")
	}
}
