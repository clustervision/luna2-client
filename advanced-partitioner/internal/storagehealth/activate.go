package storagehealth

import (
	"context"
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
func ActivateExisting(ctx context.Context, runner executor.Runner, set *v2.Set, disks []string, logger *txlog.Logger) {
	if runner == nil || set == nil {
		return
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
		assembleExplicit(ctx, runner, mdName, disks, run, logger)
		if set.Role == "os" {
			assembleExplicit(ctx, runner, mdName+"-efi", disks, run, logger)
			assembleExplicit(ctx, runner, mdName+"-boot", disks, run, logger)
		}
		run("udevadm", "settle")
	}
	if set.StorageType() == v2.StorageLVM {
		run("vgchange", "-ay", set.VGName())
		run("udevadm", "settle")
	}
}

// assembleExplicit assembles /dev/md/<mdName> from the member partitions it
// finds on the given disks (mdadm --examine, superblock Name match). No
// members found is a no-op: the array may already be running.
func assembleExplicit(ctx context.Context, runner executor.Runner, mdName string, disks []string, run func(string, ...string), logger *txlog.Logger) {
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
			logger.Info("activate: no member partitions with superblock name %q on the set's disks (array already running, or not raid)", mdName)
		}
		return
	}
	run("mdadm", append([]string{"--assemble", "/dev/md/" + mdName}, members...)...)
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
