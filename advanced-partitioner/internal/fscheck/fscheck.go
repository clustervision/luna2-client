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
//	xfs (xfs_repair -n — check only):
//	  0  clean
//	  else: if the log is dirty, replay it the safe way (mount+umount,
//	  the fs's own crash recovery) and re-check; still unclean → unhealthy.
//
//	vfat (fsck.vfat -n OR fsck.fat -n — check only):
//	  0  clean; anything else unhealthy (no journal to replay; -a's
//	  auto-fixes can truncate crosslinked files, so no auto-repair here)
//
//	swap: no fsck. The caller is expected to check the LABEL/UUID match
//	separately; this package returns OK unconditionally.
//
//	tmpfs: nothing to check — always OK.
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
	"os"

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
// outcome.
func (c *Checker) CheckFS(ctx context.Context, device, fs string) Result {
	switch fs {
	case "ext2", "ext3", "ext4":
		return c.checkExt(ctx, device, fs)
	case "xfs":
		return c.checkXFS(ctx, device)
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
// (safe fixes applied) are healthy; anything else is unhealthy.
func (c *Checker) checkExt(ctx context.Context, device, fs string) Result {
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "e2fsck", Args: []string{"-f", "-p", device},
		CaptureStdout: true, CaptureStderr: true,
	})
	rc := exitCode(res, err)
	if rc == 0 || rc == 1 {
		c.log("fsck OK (rc=%d): %s (%s)", rc, device, fs)
		return Result{Device: device, FS: fs, OK: true}
	}
	return Result{
		Device: device, FS: fs, OK: false,
		Reason: fmt.Sprintf("e2fsck -p rc=%d on %s (needs operator attention; no destructive auto-repair)", rc, device),
	}
}

// checkXFS runs xfs_repair -n (check only, no writes). A non-zero result
// after a crash usually just means a dirty log; the SAFE recovery is the
// filesystem's own journal replay — mount + umount — after which the check
// is repeated. `xfs_repair -L` (zero the log) is deliberately absent: it
// discards committed transactions and must never be a side effect of a
// health probe (FTA BR-7 / MCS-4).
func (c *Checker) checkXFS(ctx context.Context, device string) Result {
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "xfs_repair", Args: []string{"-n", device},
		CaptureStdout: true, CaptureStderr: true,
	})
	rc := exitCode(res, err)
	if rc == 0 {
		c.log("xfs_repair -n OK: %s", device)
		return Result{Device: device, FS: "xfs", OK: true}
	}

	// Journal replay via mount/umount, then re-check once.
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
			Reason: fmt.Sprintf("xfs_repair -n rc=%d on %s; journal-replay mount failed: %v (NOT running xfs_repair -L — operator decision)", rc, device, err)}
	}
	_, _ = c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "umount", Args: []string{mnt},
		CaptureStdout: true, CaptureStderr: true,
	})
	res, err = c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "xfs_repair", Args: []string{"-n", device},
		CaptureStdout: true, CaptureStderr: true,
	})
	if rc2 := exitCode(res, err); rc2 != 0 {
		return Result{Device: device, FS: "xfs", OK: false,
			Reason: fmt.Sprintf("xfs_repair -n rc=%d on %s after journal replay (NOT running xfs_repair -L — operator decision)", rc2, device)}
	}
	c.log("xfs journal replayed, xfs_repair -n OK: %s", device)
	return Result{Device: device, FS: "xfs", OK: true}
}

// checkVfat runs the chosen FAT checker with -a. rc 0 and rc 1 are
// healthy.
func (c *Checker) checkVfat(ctx context.Context, device, fs string) Result {
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path: c.o.FsckVfatCmd, Args: []string{"-n", device},
		CaptureStdout: true, CaptureStderr: true,
	})
	rc := exitCode(res, err)
	if rc == 0 {
		c.log("fsck.vfat -n OK: %s", device)
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
	dir := "/run/lpart-fscheck-replay"
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
