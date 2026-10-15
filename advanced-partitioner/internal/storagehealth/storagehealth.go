// Package storagehealth verifies whether a v1.4 set's on-disk layout matches
// its declared volumes, and classifies a non-persistent set as healthy,
// blank, or broken. It is shared by lpart-storage-check (the local-mode
// verifier) and lpart-storage-prepare's decision engine (which uses the verdict
// to drive the data fence: create on blank, refresh on healthy, halt or
// recreate on broken).
package storagehealth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/devices"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm/vgselect"
)

// Verdict classifies a non-persistent set's on-disk state.
type Verdict string

const (
	// VerdictHealthy means the declared layout exists and every volume's
	// filesystem is present, type-matched, and passes its fs check.
	VerdictHealthy Verdict = "healthy"
	// VerdictBlank means the selected disks carry no relevant layout at all
	// (no partitions/arrays/signatures) — safe to create from scratch.
	VerdictBlank Verdict = "blank"
	// VerdictBroken means a partial or mismatched layout exists — neither
	// healthy nor cleanly blank. The data fence halts (sync) or recreates
	// only the OS set (auto) on this verdict; it never wipes a data set.
	VerdictBroken Verdict = "broken"
	// VerdictUnknown means the probe could not decide: a filesystem checker
	// did not run (missing tool, exec failure). It is neither healthy nor
	// broken, so the data fence must neither mount nor wipe on it -- auto
	// used to read a missing e2fsck as "broken" and recreate the os set.
	VerdictUnknown Verdict = "unknown"
	// VerdictDegraded means an array of the set misses a member and was not
	// started (the set does not allow running degraded), or a member is stale:
	// the install parks.
	VerdictDegraded Verdict = "degraded"
)

// DegradedRefusal is the reason a VerdictDegraded set parks. allow_degraded is
// advised only when the set lacks it: with it set, the verdict means a stale
// member, which allow_degraded never accepts.
func DegradedRefusal(set *v2.Set) string {
	msg := fmt.Sprintf("set %q is degraded: an array of the set misses a member; replace the disk and reinstall with install_mode full", set.Name)
	if !set.AllowDegraded {
		msg += ", or set allow_degraded on the set to install on it degraded"
	}
	return msg
}

// Unhealthy is one volume-level verification failure.
type Unhealthy struct {
	SetName    string
	VolumeName string
	Reason     string
	// Unknown marks a failure that is not evidence of damage: the probe
	// itself could not run (fscheck Result.Unavailable). Classify turns any
	// Unknown failure into VerdictUnknown, never VerdictBroken.
	Unknown bool
	// Degraded marks an array left inactive for a missing member; Classify
	// turns it into VerdictDegraded.
	Degraded bool
}

// VerifyDiskSet runs the per-volume verification for one disk-backed set and
// returns the list of unhealthy reasons (empty when all is OK). resolved are
// the set's resolved disks. Mirrors the former check_disk_set logic.
func VerifyDiskSet(ctx context.Context, runner executor.Runner, checker *fscheck.Checker, set *v2.Set, resolved []selector.Resolved, logger *txlog.Logger) []Unhealthy {
	var failures []Unhealthy
	mark := func(volName, reason string) {
		failures = append(failures, Unhealthy{SetName: set.Name, VolumeName: volName, Reason: reason})
		if logger != nil {
			logger.Warn("UNHEALTHY set=" + set.Name + " vol=" + volName + ": " + reason)
		}
	}
	markUnknown := func(volName, reason string) {
		failures = append(failures, Unhealthy{SetName: set.Name, VolumeName: volName, Reason: reason, Unknown: true})
		if logger != nil {
			logger.Warn("UNKNOWN set=" + set.Name + " vol=" + volName + ": " + reason)
		}
	}

	if len(resolved) == 0 {
		mark("(devices)", "set has no resolved devices")
		return failures
	}

	// Assemble the set's md arrays from their explicit members first: md
	// auto-assembly is off in the installer (rd.md=0, no ARRAY lines), so on a
	// raid set the PV and the mirrored ESP and /boot arrays do not exist yet and
	// the vgchange below failed on every healthy raid1 os set, which made sync
	// park and auto re-create a healthy mirror on every boot.
	disks := make([]string, 0, len(resolved))
	for _, r := range resolved {
		disks = append(disks, r.ResolvedPath)
	}
	// Data sets are assembled read-only; nothing is started degraded unless
	// the set allows it, and what the probe brought up is released after it.
	vgSelect := ""
	if rm := set.RAIDMode(); rm != "" && rm != "none" {
		act := ActivateExisting(ctx, runner, set, disks, logger, set.Role != "os")
		defer func() { Release(ctx, runner, act, vgSelect, logger) }()
		if len(act.Degraded) > 0 {
			failures = append(failures, Unhealthy{SetName: set.Name, VolumeName: "(raid)", Degraded: true,
				Reason: fmt.Sprintf("array(s) %s miss a member and were not started", strings.Join(act.Degraded, ", "))})
			if logger != nil {
				logger.Warn("DEGRADED set=" + set.Name + ": array(s) " + strings.Join(act.Degraded, ", ") + " miss a member")
			}
			return failures
		}
	}

	if set.StorageType() == v2.StorageLVM {
		// Activate THIS set's VG by UUID: the one whose PVs are on the set's
		// disks. `vgchange -ay <name>` is ambiguous (and refuses) when a disk
		// outside the set -- an old OS disk still attached -- carries a VG of
		// the same name; the healthy set was then judged broken and auto
		// recreated it.
		vgName := set.VGName()
		vg, problem, unknown := OwnVG(ctx, runner, vgName, disks, logger)
		switch {
		case unknown:
			markUnknown("(lvm)", problem)
			return failures
		case problem != "":
			mark("(lvm)", problem)
			return failures
		}
		if _, err := runner.Run(ctx, executor.CommandSpec{
			Path: "vgchange", Args: []string{"-ay", "--select", vg.Select()},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			mark("(lvm)", fmt.Sprintf("vgchange -ay --select %s (VG %s) failed: %v", vg.Select(), vgName, err))
			return failures
		}
		vgSelect = vg.Select()
	}

	diskPath := resolved[0].ResolvedPath
	biosBoot := set.Role == "os" && HasBIOSBootPartition(ctx, runner, diskPath)
	for _, vol := range set.Volumes {
		if vol.Provider == "memory" {
			continue
		}
		device := volumeDevice(set, vol, diskPath, set.PartitionNumber(vol, biosBoot))
		if device == "" {
			continue
		}

		if !blockDeviceExistsFn(device) {
			mark(vol.Name, "block device not found: "+device)
			continue
		}
		if vol.FS != "" && vol.FS != "tmpfs" {
			actualFS := blkidScalar(ctx, runner, device, "TYPE")
			if actualFS == "" {
				mark(vol.Name, fmt.Sprintf("no filesystem signature on %s (expected %s)", device, vol.FS))
				continue
			}
			expected := vol.FS
			if expected == "fat32" {
				expected = "vfat"
			}
			if actualFS != expected {
				mark(vol.Name, fmt.Sprintf("FS type mismatch on %s: expected=%s actual=%s", device, vol.FS, actualFS))
				continue
			}
		}
		if vol.FS != "" {
			// only the os set's XFS log may be replayed by the probe; a data
			// set's is left for the operator
			res := checker.CheckFSReadOnly(ctx, device, vol.FS)
			if set.Role == "os" {
				res = checker.CheckFS(ctx, device, vol.FS)
			}
			switch {
			case res.Unavailable:
				markUnknown(vol.Name, res.Reason)
			case !res.OK:
				mark(vol.Name, res.Reason)
			}
		}
	}
	return failures
}

// OwnVG finds the set's volume group: the VG named vgName with PVs on the
// set's disks. A same-named VG elsewhere is logged and ignored. problem is a
// verification failure ("" when exactly one own VG exists); unknown is set
// when the probe itself failed (pvs/lsblk error), which says nothing about the
// set's health.
func OwnVG(ctx context.Context, runner executor.Runner, vgName string, disks []string, logger *txlog.Logger) (vg vgselect.VG, problem string, unknown bool) {
	loc, err := vgselect.Locate(ctx, runner, vgName, disks)
	if err != nil {
		return vgselect.VG{}, fmt.Sprintf("cannot list volume groups: %v", err), true
	}
	for _, f := range loc.Foreign {
		if logger != nil {
			logger.Warn(fmt.Sprintf("VG %q (uuid %s, PVs %v) is on a disk outside set %q -- ignored, never activated", f.Name, f.UUID, f.PVs, vgName))
		}
	}
	switch len(loc.Own) {
	case 1:
		return loc.Own[0], "", false
	case 0:
		return vgselect.VG{}, fmt.Sprintf("no volume group %q on the set's disks", vgName), false
	default:
		return vgselect.VG{}, fmt.Sprintf("%d volume groups named %q on the set's disks", len(loc.Own), vgName), false
	}
}

// volumeDevice returns the block device a volume's filesystem lives on, given
// the set's layout — the single place that must agree with the partition/raid
// builder. A boot-mirroring set (os + raid1) places the ESP and /boot on md
// arrays (the raw partition shows as linux_raid_member, not the real fs), so
// the health probe must look there or it falsely reports a fresh mirror broken
// and re-runs reformat instead of mounting. partIdx is the 1-based partition
// number the builder gave the volume (set.PartitionNumber). Returns "" for
// providers with no device (e.g. memory) and for partIdx 0.
func volumeDevice(set *v2.Set, vol v2.Volume, diskPath string, partIdx int) string {
	switch vol.Provider {
	case "partition":
		if md := set.BootMirrorDevice(vol); md != "" {
			return md
		}
		if partIdx <= 0 {
			return ""
		}
		return devices.PartitionPath(diskPath, partIdx)
	case "lvm":
		return "/dev/" + set.VGName() + "/" + vol.Name
	default:
		return ""
	}
}

// Classify returns the verdict for a non-persistent disk-backed set. A clean
// verification is healthy; a probe that could not run (a checker missing from
// the ramdisk) is unknown, whatever else was found; otherwise the selected
// disks are probed for any existing layout — entirely empty disks are blank
// (safe to create), anything partial or mismatched is broken.
func Classify(ctx context.Context, runner executor.Runner, checker *fscheck.Checker, set *v2.Set, resolved []selector.Resolved, logger *txlog.Logger) (Verdict, []Unhealthy) {
	failures := VerifyDiskSet(ctx, runner, checker, set, resolved, logger)
	if len(failures) == 0 {
		return VerdictHealthy, nil
	}
	for _, f := range failures {
		if f.Degraded {
			return VerdictDegraded, failures
		}
	}
	for _, f := range failures {
		if f.Unknown {
			return VerdictUnknown, failures
		}
	}
	if DisksBlank(ctx, runner, resolved) {
		return VerdictBlank, failures
	}
	return VerdictBroken, failures
}

// DisksBlank reports whether every resolved disk is empty: no partition/array
// children and no whole-disk filesystem/array signature.
func DisksBlank(ctx context.Context, runner executor.Runner, resolved []selector.Resolved) bool {
	if len(resolved) == 0 {
		return false
	}
	for _, d := range resolved {
		if !diskBlank(ctx, runner, d.ResolvedPath) {
			return false
		}
	}
	return true
}

func diskBlank(ctx context.Context, runner executor.Runner, path string) bool {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "lsblk", Args: []string{"-ln", "-o", "TYPE", path},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		// Can't tell — treat as not-blank so we never wipe on uncertainty.
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		t := strings.TrimSpace(line)
		if t != "" && t != "disk" {
			return false // has a partition/raid/lvm child
		}
	}
	// No children; a whole-disk signature (e.g. an mdraid member or fs laid
	// directly on the disk) also makes it non-blank. FAIL-CLOSED (FTA A9):
	// only blkid's genuine "no signature found" answer (exit 2, empty
	// output) counts as blank; a tool error / missing blkid / I/O problem
	// is UNKNOWN and must never authorise a wipe. (The lsblk branch above
	// already fails closed — this makes the whole guard one policy.)
	bres, berr := runner.Run(ctx, executor.CommandSpec{
		Path: "blkid", Args: []string{"-s", "TYPE", "-o", "value", path},
		CaptureStdout: true, CaptureStderr: true,
	})
	if berr == nil {
		return strings.TrimSpace(string(bres.Stdout)) == ""
	}
	if rcOf(bres, berr) == 2 && strings.TrimSpace(string(bres.Stdout)) == "" {
		return true // blkid exit 2 = nothing found: a real "blank" answer
	}
	return false
}

// rcOf resolves the exit code from a CommandResult + error (mirrors
// fscheck.exitCode; a non-exec error yields -1 = unknown).
func rcOf(res executor.CommandResult, err error) int {
	if err == nil {
		return res.ExitCode
	}
	if res.ExitCode != 0 {
		return res.ExitCode
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// blockDeviceExistsFn is a test seam; it defaults to stat'ing the real device.
var blockDeviceExistsFn = blockDeviceExists

func blockDeviceExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0
}

func blkidScalar(ctx context.Context, runner executor.Runner, device, key string) string {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "blkid", Args: []string{"-s", key, "-o", "value", device},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}
