package partition

import (
	"errors"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
)

func disksOf(n int, size int64) []selector.Resolved {
	out := make([]selector.Resolved, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, selector.Resolved{ResolvedPath: "/dev/sd" + string(rune('a'+i)), SizeBytes: size})
	}
	return out
}

// CheckDiskFit counts the fixed-size LVs too (they must fit in the storage
// partition, or in the array across the members), so a layout whose LVs
// cannot fit is refused BEFORE cleanup -- not by a failing lvcreate after
// the disks were wiped.
func TestCheckDiskFitCountsLVMVolumes(t *testing.T) {
	const gib = int64(1) << 30
	osSet := func(raid string, rootSize string) *v2.Set {
		return &v2.Set{Name: "os", Role: "os", Raid: raid, Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: rootSize, Provider: "lvm"},
			{Name: "var", Mountpoint: "/var", FS: "xfs", Size: "100%", Provider: "lvm"},
		}}
	}
	cases := []struct {
		name  string
		set   *v2.Set
		disks []selector.Resolved
		fits  bool
	}{
		{"none: 30G LV on a 20G disk", osSet("none", "30G"), disksOf(1, 20*gib), false},
		{"none: 15G LV on a 20G disk", osSet("none", "15G"), disksOf(1, 20*gib), true},
		{"raid1: 30G LV on 2x20G (mirror = one disk)", osSet("1", "30G"), disksOf(2, 20*gib), false},
		{"raid0: 30G LV on 2x20G (stripe = two disks)", osSet("0", "30G"), disksOf(2, 20*gib), true},
		{"raid10: 30G LV on 4x20G", osSet("10", "30G"), disksOf(4, 20*gib), true},
		{"100% only fits anything with room for the ESP", osSet("none", "100%"), disksOf(1, 2*gib), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckDiskFit(c.set, c.disks, "uefi")
			if c.fits && err != nil {
				t.Fatalf("must fit: %v", err)
			}
			if !c.fits {
				var tooSmall *DiskTooSmallError
				if !errors.As(err, &tooSmall) {
					t.Fatalf("want *DiskTooSmallError, got %v", err)
				}
			}
		})
	}
}
