// Package devices holds the small device-path helpers used by every
// destructive module in the installer. Centralising them avoids subtle
// platform-specific drift between partition / raid / lvm / fs callers.
//
// Cross-platform notes:
//   - Partition naming follows the kernel's one rule (block/partitions/
//     core.c, disk_name()): when the disk name ends in a digit the
//     partition is "<disk>p<N>" (nvme0n1p1, mmcblk0p3, md127p1, loop3p1,
//     nbd0p1, pmem0p1, rbd0p1), otherwise "<disk><N>" (sda1, vda2,
//     hda5). PartitionPath implements that rule rather than a list of
//     driver prefixes, so a transport nobody listed still gets it right.
//   - LVM device-mapper names double any hyphen in the VG/LV name to
//     disambiguate the dash that separates VG from LV. /dev/mapper/foo-bar
//     could either be VG=foo + LV=bar or VG=foo-bar; LVM doubles internal
//     dashes so foo--bar-baz means VG=foo-bar, LV=baz. VGLVPath reproduces
//     that rule.
package devices

import "strings"

// PartitionPath returns the device path for the n-th partition of disk.
//
//	PartitionPath("/dev/sda", 2)         -> "/dev/sda2"
//	PartitionPath("/dev/nvme0n1", 1)     -> "/dev/nvme0n1p1"
//	PartitionPath("/dev/mmcblk0", 3)     -> "/dev/mmcblk0p3"
//	PartitionPath("/dev/md127", 1)       -> "/dev/md127p1"
//	PartitionPath("/dev/nbd0", 1)        -> "/dev/nbd0p1"
//	PartitionPath("/dev/pmem0", 1)       -> "/dev/pmem0p1"
//	PartitionPath("/dev/vda", 1)         -> "/dev/vda1"
//	PartitionPath("/dev/disk/by-path/pci-0000:03:00.0", 2)
//	                                     -> "/dev/disk/by-path/pci-0000:03:00.0-part2"
//	PartitionPath("/dev/disk/by-id/wwn-0x5000c50…", 1)
//	                                     -> "/dev/disk/by-id/wwn-0x5000c50…-part1"
//
// Mirrors partition_path in /root/NEWTREE/lib/common.sh, with the kernel's
// digit rule in place of its driver list.
func PartitionPath(disk string, n int) string {
	// udev persistent-name symlinks (/dev/disk/by-path, by-id, by-uuid,
	// by-partlabel, …) name partitions with a "-part<N>" suffix — never a
	// bare number or "p<N>". Appending the bare number yields a path that
	// does not exist ("pci-0000:03:00.02" instead of "…0.0-part2"), which
	// breaks mount of a single-disk manual-by-path OS set. (Found by the
	// UEFI regression: RAID sets mount the md device and discover sets
	// resolve to kernel names, so only single-disk by-path hit this.)
	if strings.Contains(disk, "/disk/by-") {
		return disk + "-part" + itoa(n)
	}
	suffix := ""
	if needsPPrefix(disk) {
		suffix = "p"
	}
	return disk + suffix + itoa(n)
}

// needsPPrefix applies the kernel rule: a disk whose name ends in a digit
// separates the partition number with a literal "p", so the two numbers stay
// apart (nvme0n1 + 1 = nvme0n1p1, not nvme0n11). The same rule names the
// udev /dev/md/<name> partition links (63-md-raid-arrays.rules).
//
// Device-mapper paths (/dev/mapper/*, /dev/dm-*) are deliberately NOT
// special-cased: dm partition naming has no reliable convention, so the v2
// config validator refuses them as set devices before they can reach this
// chokepoint (FTA v2 BE-N3 / CCF-8, barrier NB-4).
func needsPPrefix(disk string) bool {
	if disk == "" {
		return false
	}
	last := disk[len(disk)-1]
	return last >= '0' && last <= '9'
}

// VGLVPath returns the /dev/mapper/ path for one LVM logical volume.
// Internal dashes in either name are doubled per the LVM dm naming rule.
//
//	VGLVPath("os", "root")          -> "/dev/mapper/os-root"
//	VGLVPath("os-data", "log")      -> "/dev/mapper/os--data-log"
//	VGLVPath("foo", "bar-baz")      -> "/dev/mapper/foo-bar--baz"
//
// Mirrors vg_lv_path in /root/NEWTREE/lib/common.sh.
func VGLVPath(vg, lv string) string {
	return "/dev/mapper/" + escapeDM(vg) + "-" + escapeDM(lv)
}

func escapeDM(name string) string {
	return strings.ReplaceAll(name, "-", "--")
}

// itoa is a tiny base-10 conversion that avoids pulling in strconv just
// to render single-digit partition numbers in hot paths. Falls back to
// the slow path for >9.
func itoa(n int) string {
	if n >= 0 && n <= 9 {
		return string(rune('0' + n))
	}
	// strconv-equivalent without the import dance.
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	if n == 0 {
		pos--
		buf[pos] = '0'
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
