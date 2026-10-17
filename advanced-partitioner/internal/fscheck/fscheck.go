// Package fscheck runs the filesystem health probes that the data fence
// (storagehealth.Classify) and lpart-storage-check use to decide whether an
// existing disk layout matches the v2 storage intent.
//
// SAFETY CONTRACT (FTA BR-7 — a health CHECK must never destroy data):
// these probes feed the decision "may this volume be wiped / must we halt";
// a probe that mutates the volume decides the case before the fence can.
// Therefore:
//
//   - the ONLY writes permitted are data-preserving crash recovery — an
//     ext preen (journal replay + provably-safe fixes) and an xfs journal
//     replay via mount/umount — exactly what the OS itself would do on the
//     next boot of that volume;
//   - destructive repair is NEVER automatic. In particular `xfs_repair -L`
//     (throws away the journal = guaranteed loss of committed-in-flight
//     data) was removed and must not come back: a filesystem that cannot
//     be brought clean by journal replay is reported UNHEALTHY and the
//     fence halts for an operator.
//
// Exit-code semantics (one source of truth per filesystem family):
//
//	ext2/3/4 (e2fsck -f -p — preen):
//	  0  clean
//	  1  safe fixes applied (journal replay etc.)
//	  >1 needs human attention — unhealthy (no -y: it answers yes to
//	     destructive questions too)
//
//	ext2/3/4 on a device assembled read-only (e2fsck -f -n — check only):
//	  0  clean
//	  else unhealthy (4: errors left uncorrected; 8: operational error)
//
//	xfs (xfs_repair -n — check only):
//	  0  clean
//	  else: if the log is dirty, replay it the safe way (mount+umount,
//	  the fs's own crash recovery) and re-check; still unclean → unhealthy.
//
//	vfat (fsck.vfat -n OR fsck.fat -n — check only):
//	  0  clean
//	  1  healthy ONLY when the sole finding is the FAT dirty bit (see
//	     dirtyBitOnly): Linux sets it while the ESP is mounted, so every hard
//	     reset, power loss, crash or fence leaves it; it is not damage.
//	  else unhealthy (no journal to replay; -a's auto-fixes can truncate
//	  crosslinked files, so no auto-repair here)
//
//	swap: no fsck. The caller is expected to check the LABEL/UUID match
//	separately; this package returns OK unconditionally.
//
//	tmpfs: nothing to check — always OK.
//
// A checker that could not run (binary missing, exec failure, killed by a
// signal or a timeout: no exit status) says NOTHING about the filesystem. It
// is reported with Unavailable set, never as a plain unhealthy volume: the
// data fence must not read "e2fsck is not in this ramdisk" as "the set is
// broken" -- auto would then recreate (wipe) a healthy os set.
//
// Cross-platform notes:
//   - e2fsck, xfs_repair, mkswap/swaplabel ship identically on every
//     distro and architecture we support.
//   - fsck.vfat is preferred (util-linux); fsck.fat is the dosfstools
//     name. FsckVfatCmd selects between them once at startup.
package fscheck

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Result describes the outcome of one filesystem health check.
type Result struct {
	Device string
	FS     string
	OK     bool
	// Reason is filled when OK is false. It is the operator-visible
	// "why" string that bubbles into the unhealthy report.
	Reason string
	// Unavailable is set (with OK false) when the checker itself could not
	// run -- missing binary, exec failure, no exit status. The filesystem
	// was NOT checked, so callers must treat the volume as UNKNOWN, never as
	// broken: a missing tool is not evidence of damage.
	Unavailable bool
}

// unavailable builds the Result for a checker that never produced a verdict.
func unavailable(device, fsType, tool string, err error) Result {
	return Result{
		Device: device, FS: fsType, OK: false, Unavailable: true,
		Reason: fmt.Sprintf("checker unavailable: %s did not run on %s (%v) -- the filesystem was NOT checked", tool, device, err),
	}
}

// checkerUnavailable reports whether a probe failed without a verdict: the
// binary is missing or could not be executed, or the process ended without an
// exit status (signal, timeout). Exit codes, even unexpected ones, are
// verdicts and are NOT unavailable.
func checkerUnavailable(res executor.CommandResult, err error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) || errors.Is(err, exec.ErrNotFound) ||
		errors.Is(err, iofs.ErrNotExist) || errors.Is(err, iofs.ErrPermission) {
		return true
	}
	return exitCode(res, err) == -1
}

// Options configures a Checker.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger
	// FsckVfatCmd is the chosen vfat checker name ("fsck.vfat" or
	// "fsck.fat"). sysenv.RequireStorageCheckCommands returns this so
	// the orchestrator can plumb it through. Defaults to "fsck.vfat"
	// for tests that don't care about the picker.
	FsckVfatCmd string
	// MountDir is where the xfs journal-replay mount happens. Defaults
	// to /run/lpart-fscheck-replay (created on demand); tests inject a
	// tempdir.
	MountDir string
}

// Checker runs the per-fs health probes. Construct via New.
type Checker struct {
	o Options
}

// New returns a Checker with sensible defaults.
func New(o Options) *Checker {
	if o.FsckVfatCmd == "" {
		o.FsckVfatCmd = "fsck.vfat"
	}
	return &Checker{o: o}
}

// CheckFS runs the appropriate health probe for `fs` on `device` and
// returns the typed Result. The Logger receives one INFO line per
// outcome. An XFS log that needs replaying is replayed (mount+umount).
func (c *Checker) CheckFS(ctx context.Context, device, fs string) Result {
	return c.check(ctx, device, fs, true, true)
}

// CheckFSReadOnly is CheckFS without the XFS log replay: a log that needs
// replaying makes the result Unavailable (unknown), for the operator.
func (c *Checker) CheckFSReadOnly(ctx context.Context, device, fs string) Result {
	return c.check(ctx, device, fs, false, true)
}

// CheckFSNoWrite is CheckFSReadOnly for a device assembled read-only: ext is
// checked with -n, because a preen cannot open the device.
func (c *Checker) CheckFSNoWrite(ctx context.Context, device, fs string) Result {
	return c.check(ctx, device, fs, false, false)
}

func (c *Checker) check(ctx context.Context, device, fs string, replay, preen bool) Result {
	switch fs {
	case "ext2", "ext3", "ext4":
		return c.checkExt(ctx, device, fs, preen)
	case "xfs":
		return c.checkXFS(ctx, device, replay)
	case "vfat", "fat", "fat32":
		return c.checkVfat(ctx, device, fs)
	case "swap":
		// The LABEL/UUID check lives in the orchestrator; reaching
		// CheckFS for swap means "I trust the label was set" and we
		// return OK unconditionally.
		c.log("swap label check OK: %s", device)
		return Result{Device: device, FS: fs, OK: true}
	case "tmpfs":
		return Result{Device: device, FS: fs, OK: true}
	default:
		return Result{
			Device: device, FS: fs, OK: false,
			Reason: fmt.Sprintf("unknown fs type %q on %s — cannot verify", fs, device),
		}
	}
}

// checkExt runs e2fsck -f -p (preen): journal replay plus fixes e2fsck
// proves safe; it refuses (rc>1) anything that could lose data, unlike -y
// which answers yes to destructive questions too. rc 0 (clean) and rc 1
// (safe fixes applied) are healthy; anything else is unhealthy. Without
// preen it runs e2fsck -f -n, where only rc 0 is healthy.
func (c *Checker) checkExt(ctx context.Context, device, fs string, preen bool) Result {
	mode := "-n"
	if preen {
		mode = "-p"
	}
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "e2fsck", Args: []string{"-f", mode, device},
		CaptureStdout: true, CaptureStderr: true,
	})
	if checkerUnavailable(res, err) {
		return unavailable(device, fs, "e2fsck", err)
	}
	rc := exitCode(res, err)
	if rc == 0 || (preen && rc == 1) {
		c.log("fsck OK (rc=%d): %s (%s)", rc, device, fs)
		return Result{Device: device, FS: fs, OK: true}
	}
	return Result{
		Device: device, FS: fs, OK: false,
		Reason: fmt.Sprintf("e2fsck %s rc=%d on %s (needs operator attention; no destructive auto-repair)", mode, rc, device),
	}
}

// xfsCheckTimeout bounds xfs_repair -n: it has hung on a dirty log, and a
// probe that never returns stalls the install.
const xfsCheckTimeout = 300 * time.Second

// checkXFS runs xfs_repair -n (check only). A dirty log, a crash or a timeout
// means the log has to be replayed before the filesystem can be judged: with
// replay that is done by mount+umount (the filesystem's own recovery, which
// keeps every committed transaction) and the check runs once more; without
// it the result is Unavailable. xfs_repair -L (zero the log) is never run by a
// health probe (FTA BR-7 / MCS-4).
func (c *Checker) checkXFS(ctx context.Context, device string, replay bool) Result {
	// a checker that cannot run is unknown, never broken: probe the binary first
	if res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "xfs_repair", Args: []string{"-V"},
		CaptureStdout: true, CaptureStderr: true,
	}); checkerUnavailable(res, err) {
		return unavailable(device, "xfs", "xfs_repair", err)
	}
	res, err := c.xfsRepairN(ctx, device)
	rc := exitCode(res, err)
	if rc == 0 {
		c.log("xfs_repair -n OK: %s", device)
		return Result{Device: device, FS: "xfs", OK: true}
	}
	if !replay {
		if dirtyXFSLog(res) || rc == -1 {
			return Result{Device: device, FS: "xfs", OK: false, Unavailable: true,
				Reason: fmt.Sprintf("the XFS log on %s needs replaying before it can be checked (xfs_repair -n rc=%d); not replayed on this set -- operator review", device, rc)}
		}
		return Result{Device: device, FS: "xfs", OK: false,
			Reason: fmt.Sprintf("xfs_repair -n rc=%d on %s (NOT running xfs_repair -L -- operator decision)", rc, device)}
	}

	c.warn("xfs_repair -n rc=%d on %s; attempting safe journal replay (mount+umount)", rc, device)
	mnt, err := c.replayMountDir()
	if err != nil {
		return Result{Device: device, FS: "xfs", OK: false,
			Reason: fmt.Sprintf("xfs_repair -n rc=%d on %s and no replay mountpoint: %v", rc, device, err)}
	}
	if _, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "mount", Args: []string{"-t", "xfs", device, mnt},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return Result{Device: device, FS: "xfs", OK: false,
			Reason: fmt.Sprintf("xfs_repair -n rc=%d on %s; journal-replay mount failed: %v (NOT running xfs_repair -L -- operator decision)", rc, device, err)}
	}
	_, _ = c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "umount", Args: []string{mnt},
		CaptureStdout: true, CaptureStderr: true,
	})
	res, err = c.xfsRepairN(ctx, device)
	if rc2 := exitCode(res, err); rc2 != 0 {
		return Result{Device: device, FS: "xfs", OK: false,
			Reason: fmt.Sprintf("xfs_repair -n rc=%d on %s after journal replay (NOT running xfs_repair -L -- operator decision)", rc2, device)}
	}
	c.log("xfs journal replayed, xfs_repair -n OK: %s", device)
	return Result{Device: device, FS: "xfs", OK: true}
}

func (c *Checker) xfsRepairN(ctx context.Context, device string) (executor.CommandResult, error) {
	return c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "xfs_repair", Args: []string{"-n", device},
		CaptureStdout: true, CaptureStderr: true, Timeout: xfsCheckTimeout,
	})
}

// dirtyXFSLog reports whether xfs_repair's output says the log holds
// changes it ignored because of -n (the filesystem was not unmounted).
func dirtyXFSLog(res executor.CommandResult) bool {
	out := string(res.Stderr) + string(res.Stdout)
	return strings.Contains(out, "metadata changes in a log")
}

// checkVfat runs the chosen FAT checker with -n (check only). rc 0 is
// healthy; rc 1 is healthy only when the dirty bit is the sole finding.
func (c *Checker) checkVfat(ctx context.Context, device, fs string) Result {
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: c.o.FsckVfatCmd, Args: []string{"-n", device},
		CaptureStdout: true, CaptureStderr: true,
	})
	if checkerUnavailable(res, err) {
		return unavailable(device, fs, c.o.FsckVfatCmd, err)
	}
	rc := exitCode(res, err)
	if rc == 0 {
		c.log("fsck.vfat -n OK: %s", device)
		return Result{Device: device, FS: fs, OK: true}
	}
	if rc == 1 && dirtyBitOnly(string(res.Stdout)+"\n"+string(res.Stderr)) {
		c.log("fsck.vfat -n: %s only has the FAT dirty bit set (not unmounted cleanly: hard reset, power loss or crash) -- healthy, left unchanged", device)
		return Result{Device: device, FS: fs, OK: true}
	}
	return Result{
		Device: device, FS: fs, OK: false,
		Reason: fmt.Sprintf("%s -n rc=%d on %s (check-only; FAT auto-repair can truncate files, operator decision)", c.o.FsckVfatCmd, rc, device),
	}
}

// replayMountDir returns the directory used for the xfs journal-replay
// mount, creating the default when unset. Tests inject MountDir.
func (c *Checker) replayMountDir() (string, error) {
	if c.o.MountDir != "" {
		return c.o.MountDir, nil
	}
	// created through the runner like every other command (the replay now
	// runs on every XFS check, so the fake runners of the tests must see it)
	dir := "/run/lpart-fscheck-replay"
	if _, err := c.o.Runner.Run(context.Background(), executor.CommandSpec{
		Path: "mkdir", Args: []string{"-p", "-m", "0700", dir},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return "", err
	}
	return dir, nil
}

// exitCode resolves the exit code from a CommandResult + the matching
// error. Mirrors the rc=$? pattern in bash by treating "err==nil"
// as "exit 0" when CommandResult.ExitCode wasn't populated explicitly.
func exitCode(res executor.CommandResult, err error) int {
	if err == nil {
		return res.ExitCode
	}
	if res.ExitCode != 0 {
		return res.ExitCode
	}
	// err != nil but ExitCode==0 — treat as a generic non-zero so
	// callers see "unhealthy".
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (c *Checker) log(format string, args ...any) {
	if c.o.Logger != nil {
		c.o.Logger.Info(fmt.Sprintf(format, args...))
	}
}

func (c *Checker) warn(format string, args ...any) {
	if c.o.Logger != nil {
		c.o.Logger.Warn(fmt.Sprintf(format, args...))
	}
}

// FAT dirty-bit-only detection. fsck.fat -n exits 1 for ANY finding, and the
// dirty bit -- set by Linux for as long as the filesystem is mounted -- is one.
// Every hard reset, power loss, crash or fence of a node therefore made a
// healthy ESP "unhealthy": sync and local parked the node and auto wiped the
// whole os set. The output is accepted as dirty-bit-only when every line is one
// of the lines below and the dirty-bit line is present; anything else (FATs
// differ, lost clusters, bad directory entries, ...) stays unhealthy.
//
// dosfstools 4.1 (el8):  "0x41: Dirty bit is set. ..." (0x25 on FAT16)
// dosfstools 4.2 (el9):  "Dirty bit is set. ..." and, on FAT32, the boot
// sector/backup difference at byte 65 -- the dirty flag itself, which only the
// primary boot sector carries ("  65:01/00").
var (
	vfatDirtyLine   = regexp.MustCompile(`^(0x[0-9a-fA-F]+: )?Dirty bit is set\. Fs was not properly unmounted and some data may be corrupt\.$`)
	vfatBenignLines = []*regexp.Regexp{
		regexp.MustCompile(`^fsck\.(fat|vfat|msdos) [0-9][^ ]* \(.*\)$`),
		regexp.MustCompile(`^Automatically removing dirty bit\.$`),
		regexp.MustCompile(`^Leaving filesystem unchanged\.$`),
		regexp.MustCompile(`^\S.*: [0-9]+ files, [0-9]+/[0-9]+ clusters$`),
	}
	vfatBackupHeader = []string{
		"There are differences between boot sector and its backup.",
		"This is mostly harmless. Differences: (offset:original/backup)",
		"Not automatically fixing this.",
	}
	vfatBackupDiff = regexp.MustCompile(`^([0-9]+):([0-9a-fA-F]{2})/([0-9a-fA-F]{2})$`)
)

// fat32DirtyFlagOffset is the boot-sector byte (0x41) that holds the FAT32
// dirty flag; FAT16 keeps it at 0x25 and has no backup boot sector.
const fat32DirtyFlagOffset = "65"

func dirtyBitOnly(output string) bool {
	sawDirty := false
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if vfatDirtyLine.MatchString(line) {
			sawDirty = true
			continue
		}
		if m := vfatBackupDiff.FindStringSubmatch(line); m != nil {
			if m[1] != fat32DirtyFlagOffset {
				return false
			}
			continue
		}
		benign := false
		for _, h := range vfatBackupHeader {
			if line == h {
				benign = true
				break
			}
		}
		for _, re := range vfatBenignLines {
			if !benign && re.MatchString(line) {
				benign = true
			}
		}
		if !benign {
			return false
		}
	}
	return sawDirty
}
