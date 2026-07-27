package cleanup

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// unmountUnderSysroot is _cleanup_unmount_all: collect every live mount
// target under sysroot, sort deepest-first, unmount each. Also picks up
// any mounts sourced from this set's VG even when they sit outside the
// sysroot tree (orphan LV mounts). When ownsSysroot is false the scan is
// SOURCE-SCOPED to this set's own devices — a non-OS set must not tear
// down ANY of the OS set's mounts during a multi-set run (see
// findSysrootMounts for the 2026-07-17 incident this rule comes from).
func (c *Cleaner) unmountUnderSysroot(ctx context.Context, vgName string, ownsSysroot bool, selected map[string]struct{}) {
	c.log("cleanup: unmounting under %s and VG=%s", c.o.Sysroot, vgName)

	targets := c.findSysrootMounts(ctx, vgName, ownsSysroot, selected)
	if len(targets) == 0 {
		c.runBest(ctx, "udevadm", "settle")
		return
	}

	// Deepest first: longer path, then lexicographic descending so paired
	// targets of the same depth unmount in a deterministic order.
	sort.SliceStable(targets, func(i, j int) bool {
		if len(targets[i]) != len(targets[j]) {
			return len(targets[i]) > len(targets[j])
		}
		return targets[i] > targets[j]
	})

	failed := 0
	for _, t := range targets {
		if !c.forceUnmountTarget(ctx, t) {
			failed++
		}
	}
	if failed > 0 {
		c.warn("cleanup: %d unmount(s) failed, waiting 2s and retrying", failed)
		c.o.Sleep(2 * time.Second)
		for _, t := range targets {
			_ = c.forceUnmountTarget(ctx, t)
		}
	}
	c.runBest(ctx, "udevadm", "settle")
}

// findSysrootMounts returns the unique set of live mount targets that
// either sit under c.o.Sysroot or are sourced from a device whose source
// path mentions the VG name. Mirrors the two findmnt scans inside
// _cleanup_unmount_all, with one Go-specific addition: when ownsSysroot
// is false, BOTH the /sysroot mount itself AND any target under it that
// is sourced from another set's device are omitted.
//
// The predecessor returned every target strictly under /sysroot/
// regardless of ownsSysroot, on the assumption that unmounting another
// set's child mounts is "at worst a no-op the other set will reverse
// when it re-mounts". That assumption is FALSE by construction:
// ExecutionOrder processes the OS set FIRST, so its /sysroot/boot +
// /sysroot/boot/efi are freshly mounted when a later data set's cleanup
// runs — and the OS set never re-mounts. The data set's cleanup ripped
// them out of every multi-set install: the extract then landed /boot on
// the root LV and the ESP stayed empty. On images whose tar carries a
// /boot/efi dir (rocky) the bootloader finalise proceeded into the
// wrong tree and produced silently unbootable installs (PXE reinstall
// churn); on images without it (ubuntu24) the finalise failed loudly
// with "missing EFI mount directory" (how this was caught, 2026-07-17).
// A non-owner set therefore only unmounts targets it can prove are its
// own: sourced from its selected disks, its VG, or its named md arrays
// — which still covers its prior child mounts (re-entrancy).
func (c *Cleaner) findSysrootMounts(ctx context.Context, vgName string, ownsSysroot bool, selected map[string]struct{}) []string {
	res, ok := c.runOK(ctx, "findmnt", "-rn", "-o", "TARGET,SOURCE")
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	sysroot := c.o.Sysroot
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		target, source := fields[0], fields[1]
		if target == sysroot {
			if ownsSysroot {
				seen[target] = struct{}{}
			}
			continue
		}
		if strings.HasPrefix(target, sysroot+"/") {
			if ownsSysroot || c.sourceBelongsToSet(source, vgName, selected) {
				seen[target] = struct{}{}
			}
			continue
		}
		if matchesVGSource(source, vgName) {
			seen[target] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	return out
}

// sourceBelongsToSet reports whether a findmnt SOURCE device is owned by
// the set being cleaned: an LV in its VG, one of its named md arrays
// (/dev/md/<name> or /dev/md/<name>-*), or a partition on (or the whole
// of) one of its selected disks.
func (c *Cleaner) sourceBelongsToSet(source, vgName string, selected map[string]struct{}) bool {
	if matchesVGSource(source, vgName) {
		return true
	}
	if vgName != "" {
		if source == "/dev/md/"+vgName || strings.HasPrefix(source, "/dev/md/"+vgName+"-") {
			return true
		}
	}
	for disk := range selected {
		if source == disk {
			return true
		}
		// partition on the disk: the remainder must start with a digit
		// (vdb1, sda2) or a partition separator 'p' (nvme0n1p1) so that
		// /dev/vdb never claims /dev/vdbb1
		if rest, okPfx := strings.CutPrefix(source, disk); okPfx && rest != "" {
			if (rest[0] >= '0' && rest[0] <= '9') || rest[0] == 'p' {
				return true
			}
		}
	}
	return false
}

// matchesVGSource reports whether a findmnt SOURCE belongs to the named
// volume group. Exact-prefix matching on the two LVM device forms:
//
//	/dev/<vg>/<lv>                (LV path)
//	/dev/mapper/<vg esc>-<lv esc> (device-mapper name; '-' in names is
//	                               doubled, so the VG/LV separator is the
//	                               single '-')
//
// The predecessor used a substring test (strings.Contains), which made a
// short VG name like "os" or "data" lazy-unmount UNRELATED live filesystems
// (e.g. /dev/mapper/bigdata-lv matches "data") — an ancestor-FTA finding.
func matchesVGSource(source, vgName string) bool {
	if vgName == "" {
		return false
	}
	if strings.HasPrefix(source, "/dev/"+vgName+"/") {
		return true
	}
	esc := strings.ReplaceAll(vgName, "-", "--")
	return strings.HasPrefix(source, "/dev/mapper/"+esc+"-") &&
		!strings.HasPrefix(source, "/dev/mapper/"+esc+"--")
}

// forceUnmountTarget mirrors _force_unmount_target: skip if not mounted,
// try a normal umount, fall back to lazy on failure. Returns true on
// success.
func (c *Cleaner) forceUnmountTarget(ctx context.Context, target string) bool {
	if !c.isMounted(ctx, target) {
		return true
	}
	c.log("cleanup: unmounting %s", target)
	if _, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "umount", Args: []string{target},
		CaptureStdout: true, CaptureStderr: true,
	}); err == nil {
		return true
	}
	c.warn("cleanup: normal unmount failed for %s, trying lazy", target)
	if _, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "umount", Args: []string{"-l", target},
		CaptureStdout: true, CaptureStderr: true,
	}); err == nil {
		return true
	}
	c.warn("cleanup: lazy unmount also failed for %s", target)
	return false
}

// isMounted returns true when findmnt -rn -M <target> exits 0.
func (c *Cleaner) isMounted(ctx context.Context, target string) bool {
	_, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "findmnt",
		Args:          []string{"-rn", "-M", target},
		CaptureStdout: true,
		CaptureStderr: true,
		Quiet:         true, // probe: "not mounted" is fine
	})
	return err == nil
}
