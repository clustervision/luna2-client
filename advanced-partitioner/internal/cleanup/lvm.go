package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm/vgselect"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
)

// EnsureNoLVMAutoActivate writes an LVM local override disabling event-based
// auto-activation of all volume groups while the installer runs, so udev's
// pvscan (lvm2-pvscan) cannot re-activate a stale VG from surviving metadata
// during cleanup/partition/recreate — re-creating the very `<vg>-<lv>` mapper
// node teardownVG just removed. It is the LVM analog of EnsureNoAutoAssemble.
//
// It does NOT govern the installer's own explicit `lvcreate -y` (manual
// activation is unaffected by auto_activation_volume_list), nor does it
// deactivate already-active VGs (teardownVG does that). Idempotent via a marker.
// Best-effort: returns the first error; callers log and continue. lvmlocal.conf
// is LVM's sanctioned local-override file, read after lvm.conf, so this never
// rewrites the main config.
func EnsureNoLVMAutoActivate(paths ...string) error {
	const marker = "auto_activation_volume_list = []"
	// Two installer-environment overrides, written together:
	//
	//  1. auto_activation_volume_list = [] — stop udev pvscan re-activating a
	//     stale VG mid-cleanup (the original reason this helper exists).
	//
	//  2. use_devicesfile = 0 — DISABLE the el9+ LVM devices file
	//     (/etc/lvm/devices/system.devices). That feature keys devices by
	//     IDTYPE=sys_serial IDNAME=<serial>; a PARTITION inherits its disk's
	//     serial, so `pvcreate /dev/sdX3` on a real disk (every HPC disk has a
	//     serial/WWN) collides with the whole-disk idname -> the VG created on
	//     the partition is then "not in devices file" -> lvcreate exits 5 and
	//     the install fails. The installer wipes a known, operator-declared set
	//     of disks, so the devices-file restriction (a production multi-tenant
	//     safety feature) is both inappropriate and actively harmful here.
	//     Disabling it installer-wide makes every lpart LVM command see all
	//     devices, matching pre-el9 behaviour. (Found by the §3.41 by-id
	//     regression once virtio disks were given real serials; the bug hits
	//     ALL real hardware, not just the lab.)
	//  3. locking_type = 1 — force normal file locking. Some install-initramfs
	//     LVM configs (notably el8's dracut lvm module) ship locking_type = 4
	//     ("--sysinit --readonly"), under which every LVM WRITE is prohibited:
	//     `pvcreate /dev/vdaN` fails immediately with exit 5 ("Operation
	//     prohibited while --readonly is set"). lvmlocal.conf is read after
	//     lvm.conf, so this override restores writable locking for the install.
	//     (locking_type is deprecated on el9+/modern lvm, which simply ignore it.)
	block := "\n# disabled by lpart-storage-prepare during install\n" +
		"global {\n    locking_type = 1\n}\n" +
		"devices {\n    use_devicesfile = 0\n}\n" +
		"activation {\n    " + marker + "\n}\n"
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // lvmlocal.conf path is a fixed installer config location, not user input
			if strings.Contains(string(b), marker) {
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // installer RAM root; dir must be traversable
			return err
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // lvm config is world-readable by design
		if err != nil {
			return err
		}
		if _, err := f.WriteString(block); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// RemoveLVMDevicesFile deletes the LVM "devices file"
// (/etc/lvm/devices/system.devices, and any peer *.devices path passed in) so
// its per-device restriction cannot block the installer's pvcreate/vgcreate.
//
// It is the portable BELT to EnsureNoLVMAutoActivate's use_devicesfile=0 write.
// On el9 the lvmlocal.conf override alone suffices, but on SLE 15.x it did NOT
// take effect: a pre-seeded system.devices was still consulted, so
// `pvcreate /dev/vdaN` on a serialed disk exited 5 ("not in devices file").
// Removing the file kills the enforcement regardless of which config path a
// distro's lvm actually reads; with use_devicesfile=0 also set, lvm will not
// recreate or consult a devices file for the rest of the install. The installer
// wipes an operator-declared disk set in an ephemeral RAM root, so dropping this
// production multi-tenant safety feature is both safe and (per the BE-A6c
// rationale on EnsureNoLVMAutoActivate) necessary here.
//
// Best-effort: a missing file is success; returns the first real error.
func RemoveLVMDevicesFile(paths ...string) error {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // fixed installer config path, not user input
			return err
		}
	}
	return nil
}

// teardownVG is _cleanup_lvm. It expects the VG name; LVs and PVs are
// discovered live. Best-effort throughout — except the protected-disk
// barrier, which is a hard error.
//
// Only the VG of that name whose PVs sit on THIS set's selected disks is torn
// down, addressed by UUID (vgselect). A same-named VG elsewhere — an old OS
// disk still attached carries VG "os" too — is foreign: it is left alone with
// a warning. Removing it by name destroyed a disk outside the layout, and with
// two VGs of one name every by-name command is ambiguous anyway.
//
// Order matters (HW regression, node001 2026-06-08): LVM deactivation
// (vgchange -an) MUST run before dmsetup remove. Removing a device-mapper node
// while LVM still holds the LV open fails ("device busy"), so the node survives
// and the next install's `lvcreate <vg>/<lv>` collides with the orphaned
// `<vg>-<lv>` mapper name and fails (exit 5). Deactivate first → the node is
// unheld → dmsetup remove succeeds (retried after a settle for the udev tick).
func (c *Cleaner) teardownVG(ctx context.Context, vgName string, selected, protected map[string]struct{}) error {
	if vgName == "" {
		return nil
	}
	c.log("cleanup: teardown VG=%s", vgName)

	hasDM := c.o.CommandAvailable("dmsetup")
	hasLVM := c.o.CommandAvailable("vgs")

	// Protected-disk barrier: a same-named VG (or name collision) living on
	// a persistent set's disk must never be torn down. Refuse the whole
	// cleanup — a human must rename/resolve, we must not guess.
	if hasLVM && len(protected) > 0 {
		for _, pv := range c.pvsForVG(ctx, vgName) {
			if PartitionOnSelectedDisk(pv, protected) {
				return protectedf(
					"cleanup: VG %q has physical volume %s on a persistent set's disk — refusing teardown (protected-disk barrier)",
					vgName, pv)
			}
		}
	}

	var own, foreign []vgselect.VG
	if hasLVM {
		loc, err := vgselect.Locate(ctx, c.o.Runner, vgName, diskPaths(selected))
		if err != nil {
			c.warn("cleanup: cannot tell which VG %q belongs to this set (%v): no VG is removed by name; the selected-disk sweep still removes VGs on these disks", vgName, err)
		}
		own, foreign = loc.Own, loc.Foreign
		for _, v := range foreign {
			c.warn("cleanup: VG %q (uuid %s, PVs %v) is not on this set's disks -- left alone", v.Name, v.UUID, v.PVs)
		}
	}
	mappers := func() []string { return c.ownMappers(ctx, vgName, foreign) }

	// 1. Force-unmount anything still mounted from this VG's LV mapper paths so
	//    the LVs can be deactivated.
	if hasDM {
		for _, mapper := range mappers() {
			if mt := c.findmntTargetForSource(ctx, "/dev/mapper/"+mapper); mt != "" {
				_ = c.forceUnmountTarget(ctx, mt)
			}
		}
	}

	// 2. Deactivate + remove via LVM FIRST (see the order note above).
	if hasLVM {
		c.purgeVGs(ctx, own)
	}

	// 3. Remove leftover device-mapper nodes (now unheld), retrying after a
	//    settle since a just-removed node can linger a udev tick.
	if hasDM {
		c.removeDMMappers(ctx, mappers())
		c.runBest(ctx, "udevadm", "settle")
		c.removeDMMappers(ctx, mappers())
	}

	// /dev/<vg> also holds a foreign same-named VG's links when it is active.
	if len(foreign) == 0 {
		c.runBest(ctx, "rm", "-rf", filepath.Join(c.o.DevDir, vgName))
	}
	c.runBest(ctx, "udevadm", "settle")
	return nil
}

// diskPaths renders a selected-disk basename set as /dev paths.
func diskPaths(selected map[string]struct{}) []string {
	out := make([]string, 0, len(selected))
	for _, k := range selectedKeys(selected, true) {
		out = append(out, "/dev/"+k)
	}
	return out
}

// purgeVGs deactivates and removes each VG by UUID (a name may be shared with
// a foreign VG, and `vgremove <name>` refuses an ambiguous name), then scrubs
// its PVs so a later pvcreate/vgcreate is not blocked.
//
// All teardown LVM commands run --noudevsync: udev-cookie synchronisation
// deadlocks FOREVER when the initramfs lacks the dm udev rules that ack the
// cookie (ubuntu26/resolute shipped none -- `vgchange -an` hung the whole
// battery, 2026-07-17). The CREATE path has been --noudevsync universally
// since 92dc96e for the same disease; the teardown path was missed then.
func (c *Cleaner) purgeVGs(ctx context.Context, vgs []vgselect.VG) {
	for _, v := range vgs {
		c.log("cleanup: removing VG %s (uuid %s)", v.Name, v.UUID)
		c.runBest(ctx, "vgchange", "--noudevsync", "-an", "--select", v.Select())
		c.runBest(ctx, "vgremove", "--noudevsync", "-fy", "--select", v.Select())
		for _, pv := range v.PVs {
			c.runBest(ctx, "pvremove", "-ff", "-y", pv)
		}
	}
}

// ownMappers returns the VG's device-mapper names, minus those of a foreign
// same-named VG (told apart by the dm uuid, "LVM-<vg uuid>..."). A mapping
// whose uuid cannot be read while a foreign VG exists is skipped too: when in
// doubt, leave it.
func (c *Cleaner) ownMappers(ctx context.Context, vgName string, foreign []vgselect.VG) []string {
	names := c.dmsetupMappersForVG(ctx, vgName)
	if len(foreign) == 0 {
		return names
	}
	var out []string
	for _, m := range names {
		res, ok := c.runOK(ctx, "dmsetup", "info", "-c", "--noheadings", "-o", "uuid", m)
		if !ok {
			continue
		}
		uuid := strings.TrimSpace(string(res.Stdout))
		isForeign := false
		for _, v := range foreign {
			if strings.HasPrefix(uuid, v.DMUUIDPrefix()) {
				isForeign = true
				break
			}
		}
		if !isForeign {
			out = append(out, m)
		}
	}
	return out
}

// removeDMMappers force-removes the given device-mapper nodes, with --retry so
// a transiently-open node is still cleared rather than left to block the next
// lvcreate.
func (c *Cleaner) removeDMMappers(ctx context.Context, mappers []string) {
	for _, mapper := range mappers {
		c.runBest(ctx, "dmsetup", "remove", "--retry", "-f", mapper)
	}
}

// teardownSameDiskVGs is _cleanup_lvm_on_selected_disks. For every PV in
// the system, if its underlying disk is one of the selected disks (either
// directly or via an md array whose members intersect the selected
// disks), the owning VG is removed.
//
// This contains the bug fix the Bash tree shipped at NEXT_CHAT_HANDOFF:
// md-backed PVs are only included when their member disks intersect the
// selected set, not unconditionally.
//
// Protected-disk barrier: `vgremove` destroys the WHOLE volume group. A VG
// that spans a selected disk AND a persistent set's disk must therefore not
// be removed at all — removing it would destroy the persistent data on the
// non-selected members. That state is a config/operator conflict; refuse
// (hard error → install halts for review).
func (c *Cleaner) teardownSameDiskVGs(ctx context.Context, selected, protected map[string]struct{}) error {
	if len(selected) == 0 {
		return nil
	}
	if !c.o.CommandAvailable("pvs") {
		return nil
	}

	toRemove := map[string]struct{}{}
	allPVs, err := c.allPVs(ctx)
	if err != nil {
		return fmt.Errorf("cleanup: cannot list the physical volumes to tear down the selected disks' VGs; no disk was wiped: %w", err)
	}
	for pvPath, vg := range allPVs {
		if vg == "" {
			continue
		}
		pvBase := filepath.Base(pvPath)
		if PartitionOnSelectedDisk(pvPath, selected) {
			toRemove[vg] = struct{}{}
			continue
		}
		if IsMDDevice(pvBase) {
			mdPath := "/dev/" + pvBase
			members, err := c.o.MDAdmDetail(ctx, mdPath)
			if err != nil {
				continue
			}
			for _, m := range members {
				if PartitionOnSelectedDisk(m, selected) {
					toRemove[vg] = struct{}{}
					break
				}
			}
		}
	}
	if len(toRemove) == 0 {
		return nil
	}

	// Spanning check against the protected disks before any removal.
	for vg := range toRemove {
		if len(protected) == 0 {
			break
		}
		for pvPath, pvVG := range allPVs {
			if pvVG != vg {
				continue
			}
			if PartitionOnSelectedDisk(pvPath, protected) {
				return protectedf(
					"cleanup: VG %q spans a selected disk and persistent-set PV %s — refusing to remove it (protected-disk barrier); resolve the disk layout conflict first",
					vg, pvPath)
			}
			pvBase := filepath.Base(pvPath)
			if IsMDDevice(pvBase) {
				members, err := c.o.MDAdmDetail(ctx, "/dev/"+pvBase)
				if err != nil {
					continue
				}
				for _, m := range members {
					if PartitionOnSelectedDisk(m, protected) {
						return protectedf(
							"cleanup: VG %q has md PV %s with member %s on a persistent set's disk — refusing to remove it (protected-disk barrier)",
							vg, pvPath, m)
					}
				}
			}
		}
	}

	for vg := range toRemove {
		c.log("cleanup: removing selected-disk VG %s", vg)
		for _, lv := range c.lvsPaths(ctx, vg) {
			if mt := c.findmntTargetForSource(ctx, lv); mt != "" {
				_ = c.forceUnmountTarget(ctx, mt)
			}
		}
		c.runBest(ctx, "vgchange", "--noudevsync", "-an", vg)
		for _, lv := range c.lvsPaths(ctx, vg) {
			c.runBest(ctx, "lvremove", "--noudevsync", "-fy", lv)
		}
		c.runBest(ctx, "vgremove", "--noudevsync", "-fy", vg)
		if c.o.CommandAvailable("dmsetup") {
			for _, mapper := range c.dmsetupMappersForVG(ctx, vg) {
				c.runBest(ctx, "dmsetup", "remove", "-f", mapper)
			}
		}
	}
	c.runBest(ctx, "udevadm", "settle")
	return nil
}

// --- LVM helpers ----------------------------------------------------------

// dmsetupMappersForVG returns the device-mapper names of the VG's LVs. LVM
// names a mapping "<vg>-<lv>" with every '-' INSIDE either name doubled, so
// the VG ends at the first single '-' (splitDMName). A prefix test on
// vg+"-" got both directions wrong: tearing down VG "data" also matched
// "data--2-root" (VG "data-2", possibly a persistent set's), and a VG with a
// hyphen ("my-vg" -> "my--vg-lv") never matched its own mappers.
func (c *Cleaner) dmsetupMappersForVG(ctx context.Context, vgName string) []string {
	res, ok := c.runOK(ctx, "dmsetup", "ls")
	if !ok {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if vg, _, ok := splitDMName(fields[0]); ok && vg == vgName {
			names = append(names, fields[0])
		}
	}
	return names
}

// splitDMName splits an LVM device-mapper name into its VG and LV names,
// undoing the escaping: the separator is the first '-' that is not part of a
// "--" pair, and "--" inside either part stands for one '-'.
//
//	"data-root"     -> "data",   "root"
//	"data--2-root"  -> "data-2", "root"
//	"my----vg-lv"   -> "my--vg", "lv"
//	"os-swap--a"    -> "os",     "swap-a"
//
// ok is false for a name with no single '-' (not an LVM mapping, e.g.
// "mpatha").
func splitDMName(name string) (vg, lv string, ok bool) {
	for i := 0; i < len(name); i++ {
		if name[i] != '-' {
			continue
		}
		if i+1 < len(name) && name[i+1] == '-' {
			i++ // an escaped '-' inside the VG name
			continue
		}
		if i == 0 || i == len(name)-1 {
			return "", "", false
		}
		unesc := func(s string) string { return strings.ReplaceAll(s, "--", "-") }
		return unesc(name[:i]), unesc(name[i+1:]), true
	}
	return "", "", false
}

// findmntTargetForSource returns the mount target for a given device
// source, or "" if it is not mounted.
func (c *Cleaner) findmntTargetForSource(ctx context.Context, source string) string {
	res, ok := c.runOK(ctx, "findmnt", "-rn", "-S", source, "-o", "TARGET")
	if !ok {
		return ""
	}
	return mount.UnescapeFindmnt(strings.TrimSpace(string(res.Stdout)))
}

// lvsPaths returns the absolute LV paths for a VG.
func (c *Cleaner) lvsPaths(ctx context.Context, vgName string) []string {
	res, ok := c.runOK(ctx, "lvs", "--noheadings", "-o", "lv_path", vgName)
	if !ok {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// pvsForVG returns the PV paths belonging to a given VG.
func (c *Cleaner) pvsForVG(ctx context.Context, vgName string) []string {
	res, ok := c.runOK(ctx, "pvs", "--noheadings", "-o", "pv_name,vg_name")
	if !ok {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == vgName {
			out = append(out, fields[0])
		}
	}
	return out
}

// allPVs returns a pv_name → vg_name map for every PV the system knows.
// Empty vg_name values are passed through so callers can distinguish
// orphaned PVs from missing-VG entries.
func (c *Cleaner) allPVs(ctx context.Context) (map[string]string, error) {
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "pvs", Args: []string{"--noheadings", "-o", "pv_name,vg_name"},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pv := fields[0]
		vg := ""
		if len(fields) >= 2 {
			vg = fields[1]
		}
		out[pv] = vg
	}
	return out, nil
}
