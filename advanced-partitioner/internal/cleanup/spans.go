package cleanup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RefuseOutsideSpans is CleanupSelectedSet's read-only span check, for a caller
// that must refuse before the first of several sets is cleaned.
func (c *Cleaner) RefuseOutsideSpans(ctx context.Context, selectedDisks []string) error {
	return c.refuseOutsideSpans(ctx, SelectedDisksSet(selectedDisks), nil)
}

// refuseOutsideSpans refuses a VG or md array that reaches from the selected
// disks onto another disk, unless it is lpart's own leftover: lpart never
// builds a VG across disks, and its md arrays are created with homehost "any".
// Even lpart's array is refused when the other disk is an FC/iSCSI LUN.
func (c *Cleaner) refuseOutsideSpans(ctx context.Context, selected map[string]struct{}, erasesPast func(error) bool) error {
	if c.o.CommandAvailable("pvs") {
		all, err := c.allPVs(ctx)
		if err != nil && erasesPast != nil && erasesPast(err) {
			c.warn("cleanup: cannot list the physical volumes (%v); no VG is removed, and every disk is erased regardless", err)
		} else if err != nil {
			return fmt.Errorf("cleanup: cannot list the physical volumes, so a VG spanning other disks cannot be ruled out; no disk was touched: %w", err)
		}
		touched := map[string]bool{}
		for pv, vg := range all {
			if vg != "" && c.reachesSelected(ctx, pv, selected) {
				touched[vg] = true
			}
		}
		for pv, vg := range all {
			if touched[vg] && !c.reachesSelected(ctx, pv, selected) {
				return protectedf(
					"cleanup: VG %q also has physical volume %s outside the selected disks; lpart never builds a VG across disks, so it is not removed -- refusing",
					vg, pv)
			}
		}
	}
	if !c.o.CommandAvailable("mdadm") {
		return nil
	}
	body, err := os.ReadFile(c.o.MDStatPath)
	if err != nil {
		return nil
	}
	for _, arr := range parseMDStatArrays(string(body)) {
		var inside, outside []string
		for _, m := range arr.Members {
			if PartitionOnSelectedDisk(m, selected) {
				inside = append(inside, m)
			} else {
				outside = append(outside, m)
			}
		}
		if len(inside) == 0 || len(outside) == 0 {
			continue
		}
		if name := c.mdName(ctx, "/dev/"+inside[0]); !strings.HasPrefix(name, "any:") {
			return protectedf(
				"cleanup: md array %s (name %q) also has member %s outside the selected disks and was not created by lpart -- refusing to stop or zero it",
				arr.Device, name, outside[0])
		}
		for _, m := range outside {
			if c.onSAN(ctx, m) {
				return protectedf(
					"cleanup: md array %s has member %s on an FC/iSCSI LUN outside the selected disks -- refusing to zero it", arr.Device, m)
			}
		}
	}
	return nil
}

// reachesSelected reports whether a PV is on a selected disk, directly or as an
// md array with a member there.
func (c *Cleaner) reachesSelected(ctx context.Context, pv string, selected map[string]struct{}) bool {
	if PartitionOnSelectedDisk(pv, selected) {
		return true
	}
	if !IsMDDevice(filepath.Base(pv)) {
		return false
	}
	members, err := c.o.MDAdmDetail(ctx, "/dev/"+filepath.Base(pv))
	if err != nil {
		return false
	}
	for _, m := range members {
		if PartitionOnSelectedDisk(m, selected) {
			return true
		}
	}
	return false
}

// mdName is the array name in a member's superblock ("any:os"), or "" when it
// cannot be read.
func (c *Cleaner) mdName(ctx context.Context, member string) string {
	res, ok := c.runOK(ctx, "mdadm", "--examine", member)
	if !ok {
		return ""
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if k, v, found := strings.Cut(line, ":"); found && strings.TrimSpace(k) == "Name" {
			if f := strings.Fields(v); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

// onSAN reports whether a member's disk is an FC or iSCSI LUN.
func (c *Cleaner) onSAN(ctx context.Context, member string) bool {
	res, ok := c.runOK(ctx, "lsblk", "-dno", "TRAN", "/dev/"+DiskBasename(member))
	if !ok {
		return false
	}
	t := strings.TrimSpace(string(res.Stdout))
	return t == "fc" || t == "iscsi"
}
