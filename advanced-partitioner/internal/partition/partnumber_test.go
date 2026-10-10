package partition

import (
	"context"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/devices"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
)

// TestPartitionNumberMatchesBuilder pins Set.PartitionNumber (used by the
// health probe and the mount-existing predictor) to the device paths the
// builder actually creates, for every firmware and volume order. The probe and
// the predictor used to count partition volumes in JSON order, which put
// /boot on the ESP on every BIOS node with a bootloader: sync parked, local
// looped on "mount -t xfs /dev/vda2", auto wiped a healthy disk.
func TestPartitionNumberMatchesBuilder(t *testing.T) {
	esp := v2.Volume{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"}
	boot := v2.Volume{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "2G", Provider: "partition"}
	swap := v2.Volume{Name: "swap", Mountpoint: "swap", FS: "swap", Size: "4G", Provider: "partition"}
	rootPart := v2.Volume{Name: "root", Mountpoint: "/", FS: "ext4", Size: "100%", Provider: "partition"}
	rootLVM := v2.Volume{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"}
	scratch := v2.Volume{Name: "scratch", Mountpoint: "/scratch", FS: "xfs", Size: "100%", Provider: "partition"}

	cases := []struct {
		name       string
		role       string
		bootloader v2.Bootloader
		vols       []v2.Volume
	}{
		{"os lvm", "os", "", []v2.Volume{esp, boot, rootLVM}},
		{"os lvm grub", "os", v2.BootloaderFirst, []v2.Volume{esp, boot, rootLVM}},
		{"os partitions (NRW group layout)", "os", "", []v2.Volume{esp, boot, swap, rootPart}},
		{"os partitions grub", "os", v2.BootloaderFirst, []v2.Volume{esp, boot, swap, rootPart}},
		{"os swap listed before boot", "os", v2.BootloaderFirst, []v2.Volume{swap, esp, rootPart, boot}},
		{"os root listed first", "os", "", []v2.Volume{rootPart, boot, esp}},
		{"data set", "data", "", []v2.Volume{swap, scratch}},
	}
	for _, c := range cases {
		for _, fw := range []string{"uefi", "bios"} {
			set := &v2.Set{Name: "os", Role: c.role, Selection: "manual", Raid: "none",
				Devices: []string{"/dev/vda"}, Bootloader: c.bootloader, Volumes: c.vols}
			layout, err := New(Options{Runner: fakeRunnerWithCleanDisk("/dev/vda"), Sleep: noSleep}).BuildLayout(
				context.Background(), set,
				[]selector.Resolved{{ResolvedPath: "/dev/vda", SizeBytes: 500 << 30}}, fw)
			if err != nil {
				t.Fatalf("%s/%s: BuildLayout: %v", c.name, fw, err)
			}
			biosBoot := len(layout.BIOSBootPartitions) > 0
			built := map[string]string{}
			if len(layout.EFIPartitions) > 0 {
				built["/boot/efi"] = layout.EFIPartitions[0]
			}
			if len(layout.BootPartitions) > 0 {
				built["/boot"] = layout.BootPartitions[0]
			}
			for _, pv := range layout.PartitionVolumes {
				built[pv.Mount] = pv.Device
			}
			for _, v := range c.vols {
				if v.Provider != "partition" {
					continue
				}
				want, ok := built[v.Mountpoint]
				n := set.PartitionNumber(v, biosBoot)
				if !ok {
					if n != 0 {
						t.Errorf("%s/%s: %s: builder made no partition, PartitionNumber=%d", c.name, fw, v.Name, n)
					}
					continue
				}
				if got := devices.PartitionPath("/dev/vda", n); got != want {
					t.Errorf("%s/%s: %s: PartitionNumber gives %s, builder made %s", c.name, fw, v.Name, got, want)
				}
			}
		}
	}
}

// TestMirrorBootFollowsMirrorsBoot: the builder's boot-mirroring decision is
// Set.MirrorsBoot, the same rule the probe and the predictor use.
func TestMirrorBootFollowsMirrorsBoot(t *testing.T) {
	for _, r := range []string{"none", "0", "1", "5", "6", "10"} {
		set := buildSetSingleDiskUEFI()
		set.Raid = r
		disks := []selector.Resolved{{ResolvedPath: "/dev/sda", SizeBytes: 500 << 30}}
		f := fakeRunnerWithCleanDisk("/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd")
		need := map[string]int{"none": 1, "0": 2, "1": 2, "5": 3, "6": 4, "10": 4}[r]
		for _, d := range []string{"/dev/sdb", "/dev/sdc", "/dev/sdd"}[:need-1] {
			disks = append(disks, selector.Resolved{ResolvedPath: d, SizeBytes: 500 << 30})
		}
		set.Devices = nil
		for _, d := range disks {
			set.Devices = append(set.Devices, d.ResolvedPath)
		}
		layout, err := New(Options{Runner: f, Sleep: noSleep}).BuildLayout(context.Background(), set, disks, "uefi")
		if err != nil {
			t.Fatalf("raid %s: BuildLayout: %v", r, err)
		}
		if layout.MirrorBoot != set.MirrorsBoot() {
			t.Errorf("raid %s: builder MirrorBoot=%v, Set.MirrorsBoot=%v", r, layout.MirrorBoot, set.MirrorsBoot())
		}
	}
}
