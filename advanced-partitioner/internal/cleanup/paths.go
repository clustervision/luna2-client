// Package cleanup ports the 5-step storage-cleanup cascade from
// /root/NEWTREE/lib/cleanup.sh: unmount → LVM teardown → md teardown
// → wipefs/sgdisk → runtime artifact removal. Every step is
// best-effort: failures are logged through the supplied Logger but do
// not abort the cascade, mirroring _run_cleanup_cmd in the Bash tree.
//
// The package operates exclusively through the executor.Runner — no
// direct os/exec — so tests drive the full cascade via FakeRunner.
//
// Cross-platform notes:
//   - Partition naming differs by transport: SCSI/SATA/virtio append a
//     bare integer (sda1), NVMe and MMC append `p<N>` (nvme0n1p1). The
//     paths helpers below cover both. The Bash equivalent had a
//     long-standing bug for NVMe that this Go port deliberately fixes;
//     see BOOKKEEPING.md decision log.
//   - Every command used (umount, vgchange, lvremove, vgremove,
//     pvremove, dmsetup, mdadm, wipefs, sgdisk, partprobe, udevadm) is
//     present on RHEL/Debian/Ubuntu/arm64 installer environments. dmsetup
//     and partprobe are probed via CommandAvailable because we still
//     want to limp along on a minimal image that lacks them.
package cleanup

import (
	"path/filepath"
	"regexp"
)

// Partition-name patterns. Two regexes cover the entire Linux block-device
// landscape that any TrinityX target supports.
var (
	// SCSI/SATA/virtio/Xen/legacy IDE: sd[a-z]+, vd[a-z]+, hd[a-z]+,
	// xvd[a-z]+ followed by a bare partition number.
	scsiPartRE = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|hd[a-z]+|xvd[a-z]+)(\d+)$`)

	// NVMe / MMC / md / loop: <name><digits>(n<digits>)? followed by
	// p<digits>. The optional `n<digits>` block covers nvme namespaces
	// (nvme0n1) so partitions like nvme0n1p2 resolve to the right disk.
	nvmePartRE = regexp.MustCompile(`^((?:nvme|mmcblk|md|loop)\d+(?:n\d+)?)p(\d+)$`)
)

// DiskBasename returns the whole-disk basename for a partition path:
//
//	/dev/sda1        -> sda
//	/dev/sdb12       -> sdb
//	/dev/nvme0n1p1   -> nvme0n1
//	/dev/mmcblk0p1   -> mmcblk0
//	/dev/sda         -> sda    (no partition, returned as-is)
//	/dev/md0         -> md0    (md device, no partition suffix)
//
// Compared to the Bash _cleanup_lvm_on_selected_disks helper this fixes
// the NVMe handling — the bash expression `${member%%[0-9]*}` produces
// "/dev/nvme" for any nvme partition, which causes md member-intersection
// checks to skip valid NVMe targets. See BOOKKEEPING.md section J.
func DiskBasename(devicePath string) string {
	base := filepath.Base(devicePath)
	if m := nvmePartRE.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	if m := scsiPartRE.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return base
}

// SelectedDisksSet builds an O(1) lookup of disk basenames from a list of
// resolved device paths. Mirrors _selected_disks_regex but uses a set
// instead of a regex alternation because we already know the basenames
// and Go's regex engine is overkill for membership testing.
func SelectedDisksSet(devicePaths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(devicePaths))
	for _, p := range devicePaths {
		base := filepath.Base(p)
		set[base] = struct{}{}
	}
	return set
}

// PartitionOnSelectedDisk returns true if `partition` is a partition
// (or whole-disk) that belongs to one of the selected disks.
//
// Examples (selected={"sda","nvme0n1"}):
//
//	/dev/sda1      -> true
//	/dev/sda12     -> true
//	/dev/nvme0n1p1 -> true
//	/dev/sdb1      -> false
//	/dev/md0       -> false
func PartitionOnSelectedDisk(partition string, selected map[string]struct{}) bool {
	basename := DiskBasename(partition)
	_, ok := selected[basename]
	return ok
}

// IsMDDevice answers "is this PV name (basename, e.g. md0, md/data) a
// member of the md namespace?". Matches the `[[ "$pv_base" == md* ]]`
// guard in the Bash version.
func IsMDDevice(pvBasename string) bool {
	if len(pvBasename) < 2 {
		return false
	}
	return pvBasename[:2] == "md"
}
