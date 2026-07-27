package cleanup

import (
	"context"
	"strings"
	"time"
)

// clearZFSLabels mirrors _cleanup_zfs_for_set: when zpool is available,
// run `zpool labelclear -f` on each selected disk and log success.
// Best-effort; zpool is rarely present on the installer image.
func (c *Cleaner) clearZFSLabels(ctx context.Context, disks []string) {
	if !c.o.CommandAvailable("zpool") {
		return
	}
	for _, d := range disks {
		if _, ok := c.runOK(ctx, "zpool", "labelclear", "-f", d); ok {
			c.log("cleanup: cleared ZFS label on %s", d)
		}
	}
}

// wipePartitionsAndDisks mirrors _cleanup_partitions_and_disks.
//
//	for each disk:
//	  for each partition: dmsetup remove (if LVM holder), wipefs -af,
//	                      mdadm --zero-superblock
//	  wipefs -af <disk>
//	  sgdisk --zap-all <disk>
//	if partprobe: partprobe each disk
//	udevadm settle
//	for each disk: poll up to 20s waiting for partitions to disappear
func (c *Cleaner) wipePartitionsAndDisks(ctx context.Context, disks []string) {
	hasDM := c.o.CommandAvailable("dmsetup")
	hasMD := c.o.CommandAvailable("mdadm")
	hasPartprobe := c.o.CommandAvailable("partprobe")

	for _, disk := range disks {
		for _, part := range c.partitionsOf(ctx, disk) {
			if hasDM {
				if dmName := c.lvmDMNameForPartition(ctx, part); dmName != "" {
					c.runBest(ctx, "dmsetup", "remove", "-f", dmName)
				}
			}
			c.runBest(ctx, "wipefs", "-af", part)
			if hasMD {
				c.runBest(ctx, "mdadm", "--zero-superblock", part)
			}
		}
		c.runBest(ctx, "wipefs", "-af", disk)
		c.runBest(ctx, "sgdisk", "--zap-all", disk)
	}

	if hasPartprobe {
		for _, disk := range disks {
			c.runBest(ctx, "partprobe", disk)
		}
	}
	c.runBest(ctx, "udevadm", "settle")

	for _, disk := range disks {
		c.waitForNoPartitions(ctx, disk, 20)
	}
}

// lvmDMNameForPartition mirrors:
//
//	lsblk -ln -o NAME,TYPE <part> | awk '$2 == "lvm" {print $1; exit}'
//
// Returns "" if no LVM holder is layered on top of <part>.
func (c *Cleaner) lvmDMNameForPartition(ctx context.Context, part string) string {
	res, ok := c.runOK(ctx, "lsblk", "-ln", "-o", "NAME,TYPE", part)
	if !ok {
		return ""
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "lvm" {
			return fields[0]
		}
	}
	return ""
}

// waitForNoPartitions polls lsblk until <disk> reports zero partitions
// or attempts is exhausted (1 second per attempt).
func (c *Cleaner) waitForNoPartitions(ctx context.Context, disk string, attempts int) {
	for i := 0; i < attempts; i++ {
		if c.partitionCount(ctx, disk) == 0 {
			return
		}
		c.o.Sleep(time.Second)
		if c.o.CommandAvailable("partprobe") {
			c.runBest(ctx, "partprobe", disk)
		}
		c.runBest(ctx, "udevadm", "settle")
	}
}

func (c *Cleaner) partitionCount(ctx context.Context, disk string) int {
	res, ok := c.runOK(ctx, "lsblk", "-ln", "-o", "TYPE", disk)
	if !ok {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if strings.TrimSpace(line) == "part" {
			count++
		}
	}
	return count
}
