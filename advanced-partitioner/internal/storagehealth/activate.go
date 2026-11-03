package storagehealth

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/devices"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// biosBootGUID is the GPT partition type GUID of a BIOS boot partition (EF02).
const biosBootGUID = "21686148-6449-6e6f-744e-656564454649"

// HasBIOSBootPartition reports whether partition 1 of disk is a GPT BIOS boot
// partition (EF02). The partition builder leads an os set with one on a BIOS
// node whose layout asks for a bootloader, which shifts every later partition
// number by one. The disk itself is the truth, not the current firmware: a disk
// laid out under BIOS keeps its EF02 partition when the node is switched to
// UEFI. blkid -p reads the partition table entry directly (util-linux 2.32 on
// el8 supports it). Any probe failure reports false.
func HasBIOSBootPartition(ctx context.Context, runner executor.Runner, disk string) bool {
	if runner == nil || disk == "" {
		return false
	}
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "blkid", Args: []string{"-p", "-s", "PART_ENTRY_TYPE", "-o", "value", devices.PartitionPath(disk, 1)},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(res.Stdout)), biosBootGUID)
}

// ActivateExisting brings an existing layout of set up so its devices can be
// probed or mounted: it assembles the set's md arrays (root, and for an os set
// the mirrored -efi and -boot arrays) from EXPLICIT member partitions found on
// the set's own disks, then activates the set's volume group. md
// auto-assembly and event-based LVM autoactivation are disabled for the whole
// install (rd.md=0, EnsureNoAutoAssemble, EnsureNoLVMAutoActivate), so nothing
// appears by itself. Best-effort: an array or VG that is already active is
// fine, and the probe or mount that follows stays the hard gate. Shared by the
// health probe (sync/auto/local verdicts) and the mount-existing path, so both
// see the same devices.
//
// A degraded array is started (--run) only when the set allows it
// (allow_degraded); otherwise it stays inactive and its name is returned in
// Degraded. Assembled names the arrays this call assembled, for Release.
func ActivateExisting(ctx context.Context, runner executor.Runner, set *v2.Set, disks []string, logger *txlog.Logger) Activation {
	var act Activation
	if runner == nil || set == nil {
		return act
	}
	run := func(path string, args ...string) {
		if _, err := runner.Run(ctx, executor.CommandSpec{
			Path: path, Args: args,
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil && logger != nil {
			logger.Info("activate (best-effort) " + path + " " + strings.Join(args, " ") + ": " + err.Error())
		}
	}
	rmode := set.RAIDMode()
	if rmode != "" && rmode != "none" {
		mdName := set.MDName()
		names := []string{mdName}
		if set.Role == "os" {
			names = append(names, mdName+"-efi", mdName+"-boot")
		}
		for _, n := range names {
			assembled, degraded := assembleExplicit(ctx, runner, n, disks, run, logger, set.AllowDegraded)
			if assembled {
				act.Assembled = append(act.Assembled, n)
			}
			if degraded {
				act.Degraded = append(act.Degraded, n)
			}
		}
		run("udevadm", "settle")
		for _, n := range names {
			active, total, state, degraded := runningDegraded(ctx, runner, n)
			switch {
			case !degraded:
			case set.AllowDegraded:
				if logger != nil {
					logger.Warn(fmt.Sprintf("set %s: array %s runs DEGRADED (%s of %s members active, state %q); allow_degraded is set, so the set is used as it is -- re-add or replace the failed member (mdadm --detail /dev/md/%s), or reinstall with install_mode full to rebuild it", set.Name, n, active, total, state, n))
				}
			case !slices.Contains(act.Degraded, n):
				// Already running on fewer members (started before lpart):
				// without allow_degraded that is the same refusal as a
				// member that is missing.
				act.Degraded = append(act.Degraded, n)
			}
		}
	}
	if set.StorageType() == v2.StorageLVM {
		// By UUID, the VG on the set's own disks: a same-named VG on another
		// disk makes `vgchange -ay <name>` ambiguous (see OwnVG).
		vg, problem, _ := OwnVG(ctx, runner, set.VGName(), disks, logger)
		if problem != "" {
			if logger != nil {
				logger.Info("activate (best-effort) VG " + set.VGName() + ": " + problem)
			}
			return act
		}
		run("vgchange", "-ay", "--select", vg.Select())
		run("udevadm", "settle")
		act.VGSelect = vg.Select()
	}
	return act
}

// Activation is what ActivateExisting brought up.
type Activation struct {
	Assembled []string // arrays this call assembled (not ones already running)
	Degraded  []string // arrays left inactive because a member is missing
	VGSelect  string   // the volume group this call activated
}

// Release stops what a health probe brought up -- the volume group (when
// vgSelect is set) and the arrays it assembled -- so the decision that follows
// starts from the disks as they were, and the probe's read-only arrays never
// reach a mount.
func Release(ctx context.Context, runner executor.Runner, act Activation, vgSelect string, logger *txlog.Logger) {
	run := func(path string, args ...string) {
		if _, err := runner.Run(ctx, executor.CommandSpec{
			Path: path, Args: args, CaptureStdout: true, CaptureStderr: true,
		}); err != nil && logger != nil {
			logger.Info("release (best-effort) " + path + " " + strings.Join(args, " ") + ": " + err.Error())
		}
	}
	if vgSelect == "" {
		// a probe that returns early never recorded the VG it brought up
		vgSelect = act.VGSelect
	}
	if vgSelect != "" {
		run("vgchange", "-an", "--select", vgSelect)
	}
	for _, n := range act.Assembled {
		run("mdadm", "--stop", "/dev/md/"+n)
	}
}

// assembleExplicit assembles /dev/md/<mdName> from the member partitions it
// finds on the given disks (mdadm --examine, superblock Name match). No
// members found is a no-op: the array may already be running.
//
// Without --run mdadm leaves an array that misses a member inactive; start
// adds --run, so it runs degraded. assembled reports that this call assembled
// the array, degraded that it is left inactive.
func assembleExplicit(ctx context.Context, runner executor.Runner, mdName string, disks []string, run func(string, ...string), logger *txlog.Logger, start bool) (assembled, degraded bool) {
	var members []string
	for _, d := range disks {
		for _, p := range partitionsOn(ctx, runner, d) {
			if mdSuperblockName(ctx, runner, p) == mdName {
				members = append(members, p)
			}
		}
	}
	if len(members) == 0 {
		if logger != nil {
			logger.Info(fmt.Sprintf("activate: no member partitions with superblock name %q on the set's disks (array already running, or not raid)", mdName))
		}
		return false, false
	}
	for _, m := range members {
		if md := mdHolder(ctx, runner, m); md != "" {
			return false, adoptAssembledArray(ctx, runner, mdName, md, members, run, logger, start)
		}
	}
	args := []string{"--assemble"}
	if start {
		args = append(args, "--run")
	}
	args = append(append(args, "/dev/md/"+mdName), members...)
	res, err := runner.Run(ctx, executor.CommandSpec{Path: "mdadm", Args: args, CaptureStdout: true, CaptureStderr: true})
	if err != nil && logger != nil {
		logger.Info("activate (best-effort) mdadm " + strings.Join(args, " ") + ": " + err.Error())
	}
	md := mdHolder(ctx, runner, members[0])
	if md == "" {
		// mdadm would not start it: a member is missing, also when only its
		// superblock is gone and the disk is there. "not clean" is mdadm
		// counting active members short on a dirty array, which a present
		// spare hides from the member count.
		stderr := string(res.Stderr)
		return false, strings.Contains(stderr, "need all") || strings.Contains(stderr, "not clean") ||
			len(members) < raidDevices(ctx, runner, members[0])
	}
	return true, arrayState(ctx, runner, md) == "inactive"
}

// raidDevices is the member count the array was created with, from one
// member's superblock (0 when it cannot be read).
func raidDevices(ctx context.Context, runner executor.Runner, member string) int {
	res, err := runner.Run(ctx, executor.CommandSpec{Path: "mdadm", Args: []string{"--examine", member}, CaptureStdout: true, CaptureStderr: true})
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " : "); ok && strings.TrimSpace(k) == "Raid Devices" {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// arrayState is the md array's sysfs state ("clean", "inactive", ...), or "".
func arrayState(ctx context.Context, runner executor.Runner, md string) string {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "cat", Args: []string{"/sys/block/" + md + "/md/array_state"},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// mdHolder returns the kernel name of the md array that already holds
// partition dev (e.g. "md125"), or "" when none does (sysfs holders, read
// through the runner so tests stay hermetic).
func mdHolder(ctx context.Context, runner executor.Runner, dev string) string {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "ls", Args: []string{"-1", "/sys/class/block/" + filepath.Base(dev) + "/holders"},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return ""
	}
	for _, n := range strings.Fields(string(res.Stdout)) {
		if strings.HasPrefix(n, "md") {
			return n
		}
	}
	return ""
}

// adoptAssembledArray takes over an array the initramfs assembled by itself.
// initramfs-tools (Ubuntu) runs udev incremental md assembly before the
// installer starts; dracut does not (rd.md=0). An incrementally assembled
// array that misses a member stays inactive and gets no /dev/md/<name> link,
// so `mdadm --assemble` on its members failed "is busy - skipping" and the
// health probe judged the set broken ("block device not found:
// /dev/md/os-efi") -- a degraded mirror could never be synced or booted
// locally on Ubuntu. Start it (degraded) when start allows it, and give it its
// name; otherwise report it degraded and leave it as it is.
func adoptAssembledArray(ctx context.Context, runner executor.Runner, mdName, md string, members []string, run func(string, ...string), logger *txlog.Logger, start bool) (degraded bool) {
	state := arrayState(ctx, runner, md)
	if logger != nil {
		logger.Info(fmt.Sprintf("activate: array %s is already assembled as /dev/%s (state %q); using it", mdName, md, state))
	}
	switch state {
	case "clean", "active", "active-idle", "readonly", "read-auto", "write-pending":
		// the kernel decides which members of a running array are in sync: their
		// superblock event counts can differ by one after an unclean shutdown
		if why := kernelOutOfSync(ctx, runner, md, members); why != "" {
			if logger != nil {
				logger.Warn(fmt.Sprintf("activate: array %s (/dev/%s) %s; not taken over", mdName, md, why))
			}
			return true
		}
	default: // inactive (degraded, not started) or unknown
		if !start {
			return true
		}
		// a member that missed writes (a stale disk put back) must not be
		// started with the others; never, whatever allow_degraded says
		if events := memberEvents(ctx, runner, members); len(events) > 1 {
			if logger != nil {
				logger.Warn(fmt.Sprintf("activate: array %s (/dev/%s) members disagree on their event count (%s); not started", mdName, md, strings.Join(events, ", ")))
			}
			return true
		}
		run("mdadm", "--run", "/dev/"+md)
	}
	run("mkdir", "-p", "/dev/md")
	run("ln", "-sfn", "../"+md, "/dev/md/"+mdName)
	return false
}

// kernelOutOfSync says why the kernel does not run every member of a running
// array in sync, or "" when it does: md/degraded and each member's state.
func kernelOutOfSync(ctx context.Context, runner executor.Runner, md string, members []string) string {
	read := func(path string) string {
		res, err := runner.Run(ctx, executor.CommandSpec{Path: "cat", Args: []string{path}, CaptureStdout: true, CaptureStderr: true})
		if err != nil {
			return "unreadable"
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	if d := read("/sys/block/" + md + "/md/degraded"); d != "0" {
		return fmt.Sprintf("runs degraded (md/degraded %s)", d)
	}
	for _, m := range members {
		st := read("/sys/block/" + md + "/md/dev-" + filepath.Base(m) + "/state")
		if !slices.Contains(strings.Split(st, ","), "in_sync") {
			return fmt.Sprintf("member %s is not in sync (state %q)", m, st)
		}
	}
	return ""
}

// MDNamesOnDisks maps the md array name in every superblock found on the
// disks (whole disk and each partition; running or not) to one device that
// carries it. Read-only (mdadm --examine).
func MDNamesOnDisks(ctx context.Context, runner executor.Runner, disks []string) map[string]string {
	out := map[string]string{}
	for _, d := range disks {
		for _, dev := range append([]string{d}, partitionsOn(ctx, runner, d)...) {
			if n := mdSuperblockName(ctx, runner, dev); n != "" {
				if _, seen := out[n]; !seen {
					out[n] = dev
				}
			}
		}
	}
	return out
}

// partitionsOn lists the partition device paths on one disk. KNAME, not PATH:
// the PATH column arrived in util-linux 2.33 and el8 ships 2.32.
func partitionsOn(ctx context.Context, runner executor.Runner, disk string) []string {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "lsblk", Args: []string{"-ln", "-o", "KNAME,TYPE", disk},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return nil
	}
	var parts []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "part" {
			parts = append(parts, "/dev/"+fields[0])
		}
	}
	return parts
}

// mdSuperblockName returns the array name from a partition's md superblock
// ("Name : any:data (...)" -> "data"), or "" when the partition has none.
func mdSuperblockName(ctx context.Context, runner executor.Runner, part string) string {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "mdadm", Args: []string{"--examine", part},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Name :") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "Name :"))
		if i := strings.IndexAny(name, " \t"); i >= 0 {
			name = name[:i]
		}
		if i := strings.LastIndexByte(name, ':'); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	return ""
}

// memberEvents returns the distinct event counts in the members' superblocks
// (mdadm --examine "Events :"); members it cannot read are skipped.
func memberEvents(ctx context.Context, runner executor.Runner, members []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range members {
		res, err := runner.Run(ctx, executor.CommandSpec{
			Path: "mdadm", Args: []string{"--examine", m},
			CaptureStdout: true, CaptureStderr: true,
		})
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(res.Stdout), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), " : "); ok && strings.TrimSpace(k) == "Events" {
				if v = strings.TrimSpace(v); !seen[v] {
					seen[v] = true
					out = append(out, m+"="+v)
				}
			}
		}
	}
	return out
}

// runningDegraded reports whether /dev/md/<mdName> runs degraded, with its
// active and total member counts and state as mdadm --detail prints them. An
// array that does not exist is not degraded.
func runningDegraded(ctx context.Context, runner executor.Runner, mdName string) (active, total, state string, degraded bool) {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "mdadm", Args: []string{"--detail", "/dev/md/" + mdName},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return "", "", "", false
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " : ")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "State":
			state = strings.TrimSpace(v)
		case "Active Devices":
			active = strings.TrimSpace(v)
		case "Raid Devices":
			total = strings.TrimSpace(v)
		}
	}
	return active, total, state, strings.Contains(state, "degraded")
}
