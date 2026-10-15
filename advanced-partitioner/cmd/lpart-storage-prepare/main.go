// lpart-storage-prepare wires every Phase 1–6 module into the destructive
// installer binary. Behavioural reference: /root/NEWTREE/bin/lpart-storage-prepare.
//
// Flow:
//  1. Parse flags, init logger, installer-env guard, command preflight
//  2. Load + validate the v2 storage JSON
//  3. Wipe + host guards
//  4. Unmount any stale /sysroot tree (once)
//  5. Plan EVERY set in OS-first execution order, touching nothing: resolve
//     devices, health probe (sync/auto), data fence, missing-device rule,
//     and the deterministic create preflights (raid count, disk fit,
//     another set's signature, a foreign same-name VG). Any refusal here
//     parks the node (exit 2/3) with every disk untouched.
//  6. Execute: identity re-check + cleanup of every set that is
//     (re)created, then per set either
//     - memory: build memory filesystems, mount, assemble result
//     - create: partition -> raid -> lvm -> fs -> mount ->
//     RecordBlockUUIDs -> assemble result
//     - mount:  activate (md --run, VG by uuid) -> mount -> assemble result
//  7. Emit storage-result.json (single object or multi-set wrapper)
//     and storage-resolved.json; capture final inventory state.
//
// Exit codes: 0 ok; 1 retryable (environmental: I/O, udev, a probe that may
// clear); 2 safety refusal and 3 fatal config error -- both deterministic,
// both park the node for an operator instead of retrying every 30 s.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/inventory"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	fsmod "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sanitize"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/storagehealth"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

const toolName = "lpart-storage-prepare"

func main() {
	defer cli.ExitOnPanic(os.Stderr, toolName)
	cli.EnsureInstallerPath()
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func errln(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, toolName+": "+format+"\n", args...)
}

// flags is the parsed CLI surface — tool-specific flags only; common
// flags live on the embedded cli.Common.
type flags struct {
	cli.Common
	ConfigPath         string
	Wipe               bool
	ExpectHost         string
	ResultOut          string
	ResolvedOut        string
	Details            bool
	InstallContextPath string
	// LunaInstallScript is where the save write-back reads the install token
	// (decisions/0008); empty uses osimage.LunaInstallScript.
	LunaInstallScript string
	// Test affordances; never set from production CLI.
	CmdlinePath        string
	HostnameFunc       func() (string, error)
	SkipCommandCheck   bool
	SkipFinalInventory bool
}

// defaultPaths matches the bash defaults from /root/NEWTREE/lib/common.sh.
const (
	defaultRuntimeDir = "/tmp/install_files/runtime"
	defaultResultOut  = defaultRuntimeDir + "/storage-result.json"
	defaultResolved   = defaultRuntimeDir + "/storage-resolved.json"
)

// matches bin/lpart-storage-prepare for easy diffing.
//
//nolint:gocyclo // command-flow function intentionally linear; structure
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		argv = []string{toolName}
	}

	var f flags
	fs := cli.NewToolFlagSet(toolName)
	f.Bind(fs)
	fs.StringVar(&f.ConfigPath, "config", "", "Path to v2 storage JSON")
	fs.BoolVar(&f.Wipe, "wipe", false, "Required to actually touch disks; --dry-run is the alternative")
	fs.StringVar(&f.ExpectHost, "expect-host", "", "Refuse to run unless `hostname -s` matches this value")
	fs.StringVar(&f.ResultOut, "result-out", defaultResultOut, "Where to write storage-result.json")
	fs.StringVar(&f.ResolvedOut, "resolved-out", defaultResolved, "Where to write storage-resolved.json")
	fs.BoolVar(&f.Details, "details", true, "Verbose: extended device inventory at startup + the final blkid/pvs/vgs/lvs/mdadm evidence (default on; --details=false to quieten)")
	fs.StringVar(&f.InstallContextPath, "install-context", installcontext.DefaultJSONPath,
		"Path to install-context.json (mode/osimage-sync flags; informational)")
	fs.StringVar(&f.LunaInstallScript, "luna-install-script", "",
		"Path to Luna's install.sh (source of the install token for save write-back)")
	// Hidden test affordances:
	fs.StringVar(&f.CmdlinePath, "cmdline-path", "", "(test) override /proc/cmdline path")
	fs.BoolVar(&f.SkipCommandCheck, "skip-command-check", false, "(test) skip the storage-prepare command preflight")
	fs.BoolVar(&f.SkipFinalInventory, "skip-final-inventory", false, "(test) skip the blkid/pvs/vgs/lvs/mdadm capture")

	if err := fs.Parse(argv[1:]); err != nil {
		// A bad command line is identical on every retry: park (exit 3).
		return cli.ExitFatalConfig(stderr, toolName, "usage", "%v", err)
	}
	if f.ShowVersion {
		_, _ = fmt.Fprintln(stdout, cli.Version())
		return 0
	}

	logger, err := f.NewLogger(toolName)
	if err != nil {
		errln(stderr, "log init: %v", err)
		return 1
	}
	defer func() { _ = logger.Close() }()
	logger.SetStage("startup")
	logger.StageBanner("Starting lpart-storage-prepare")

	// A missing install-context reads as the defaults; one that exists but
	// does not parse fails identically on every retry: park (exit 3).
	ic, err := installcontext.LoadFile(f.InstallContextPath)
	if err != nil {
		return cli.ExitFatalConfig(stderr, toolName, "install-context", "%v", err)
	}
	logger.KV("install_mode", string(ic.InstallMode))

	// Installer-env guard.
	logger.SetStage("installer_check")
	// Outside the Luna installer this tool must never touch a disk; the
	// condition does not change on retry, so it is a refusal (exit 2).
	if err := sysenv.RequireInstallerEnv(sysenv.Options{
		CmdlinePath:       f.CmdlinePath,
		AllowNonInstaller: f.AllowNonInstaller,
	}); err != nil {
		return cli.ExitSafetyRefusal(stderr, toolName, "not-installer-env", "%v", err)
	}

	// Command preflight (the FAT tool names come out of this). A tool missing
	// from the install ramdisk is missing on every retry: park (exit 3) instead
	// of looping -- and the fs checkers are part of it, so the health probe can
	// never read "checker not installed" as "set broken".
	logger.SetStage("command_checks")
	tools := sysenv.StoragePrepareTools{MkfsFat: "mkfs.fat", FsckVfat: "fsck.vfat"}
	if !f.SkipCommandCheck {
		chosen, err := sysenv.RequireStoragePrepareCommands()
		if err != nil {
			return cli.ExitFatalConfig(stderr, toolName, "missing-commands",
				"%v -- the install ramdisk lacks required storage tools; rebuild it with them", err)
		}
		tools = chosen
	}

	// Load + validate config. The disklayout is optional: sanitize needs no
	// layout, and memboot / auto-with-no-layout synthesize a RAM root.
	logger.SetStage("config_validation")
	var cfg *v2.Config
	if f.ConfigPath != "" {
		loaded, raw, lerr := v2.LoadFile(f.ConfigPath)
		if lerr != nil {
			if errors.Is(lerr, os.ErrNotExist) {
				errln(stderr, "config not found: %s", f.ConfigPath)
				return 1
			}
			// Unparsable layout: identical on every retry -> park, do not loop.
			return cli.ExitFatalConfig(stderr, toolName, "config-load", "%v", lerr)
		}
		if verr := loaded.ValidateRaw(raw); verr != nil {
			// An invalid disklayout fails identically on every retry: park for
			// an operator (exit 3) instead of retrying every 30 s for ever.
			return cli.ExitFatalConfig(stderr, toolName, "config-validation", "%v", verr)
		}
		for _, w := range loaded.Warnings {
			logger.Warn(w)
		}
		cfg = loaded
	}
	logger.KV("config_path", f.ConfigPath)
	logger.KV("dry_run", f.DryRun)
	logger.KV("wipe_approved", f.Wipe)
	logger.KV("details", f.Details)

	if !f.DryRun && !f.Wipe {
		return cli.ExitFatalConfig(stderr, toolName, "usage", "--wipe is required for destructive execution")
	}
	if !f.DryRun {
		logger.SetStage("expected_host_check")
		if err := sysenv.RequireExpectedHost(f.ExpectHost, sysenv.Options{
			Hostname: f.HostnameFunc,
		}); err != nil {
			// Wrong node: never touch its disks, and do not retry for ever.
			if errors.Is(err, sysenv.ErrHostMismatch) {
				return cli.ExitSafetyRefusal(stderr, toolName, "host-mismatch", "%v", err)
			}
			errln(stderr, "%v", err)
			return 1
		}
	}

	runner := executor.New(executor.Options{DryRun: f.DryRun, Logger: logger})

	// Detect firmware mode once up-front: it is recorded on every set and it
	// decides whether this run installs GRUB.
	detectedFirmware := detectFirmware()
	logger.KV("detected_firmware", detectedFirmware)

	// Boot-path preflight. A BIOS/legacy-CSM node is partitioned and installed
	// much like a UEFI one; the shim and the UEFI boot order are always
	// skipped, and GRUB too unless the os set asks for it, because BIOS has no
	// NVRAM boot-entry management. Little is lost by that: Luna PXE-loads the
	// kernel on every boot anyway. The one case that must stop is a
	// bootloader-less disk OS behind an install ramdisk that reboots instead of
	// pivoting -- refuse before any destructive action, via the safety_refusal
	// contract so the wrapper parks for operator review instead of retrying for
	// ever.
	env := bootEnv{Firmware: detectedFirmware, RamdiskPivots: detectRamdiskPivots()}
	plan := planBoot(ic.InstallMode, cfg, env)
	logger.KV("boot_plan_disk_os", plan.DiskOS)
	logger.KV("boot_plan_installs_grub", plan.InstallsGRUB)
	logger.KV("boot_plan_bootloader_none", plan.BootloaderNone)
	logger.KV("ramdisk_pivots", env.RamdiskPivots)
	if err := preflightBoot(plan, env); err != nil {
		var se *safetyError
		if errors.As(err, &se) {
			return cli.ExitSafetyRefusal(stderr, toolName, se.reason, "%s", se.msg)
		}
		errln(stderr, "%v", err)
		return 1
	}
	switch {
	case detectedFirmware != "uefi" && plan.InstallsGRUB:
		logger.Warn(fmt.Sprintf("firmware is %q: GRUB is installed on every boot disk, but the shim and the UEFI boot order are skipped (UEFI-only) -- the node boots its own disk, so the controller no longer decides", detectedFirmware))
	case detectedFirmware != "uefi" && plan.DiskOS:
		logger.Warn(fmt.Sprintf("firmware is %q: GRUB, shim and the UEFI boot order are skipped (UEFI-only) -- the OS is installed to disk but the node PXE-boots every time", detectedFirmware))
		if !plan.BootloaderNone {
			logger.UserInfo(`set the OS set's bootloader key to "first" for a bootloader on %q firmware: there is no UEFI boot order to arrange, so "last" installs nothing`, detectedFirmware)
		}
	case detectedFirmware != "uefi":
		logger.UserInfo("firmware is %q: allowed -- this run installs no bootloader (the node boots via PXE)", detectedFirmware)
	case plan.BootloaderNone:
		logger.Warn(`the OS set asks for bootloader "none": no GRUB, no shim and no UEFI boot entry, and any existing bootloader on the disk is removed -- the node PXE-boots every time`)
	}

	// Every os set declares an ESP, because the schema requires one, so a BIOS
	// node builds one it will never read. Say so: it is deliberate, and a node
	// that was meant to boot UEFI looks exactly like this.
	if detectedFirmware != "uefi" && plan.DiskOS {
		logger.UserInfo("the layout's ESP at /boot/efi is created and formatted on %q firmware as well, so a node switched to UEFI later needs no repartition; nothing reads it meanwhile -- if this node was meant to boot UEFI, its firmware is in legacy/CSM mode", detectedFirmware)
	}

	// Stop udev from auto-assembling stale md arrays from surviving superblocks
	// while we clean/partition/(re)create — the re-assembly race that otherwise
	// leaves a member busy and breaks re-partition / mdadm --create. No-op for
	// arrays already assembled at boot, so mount-existing paths are unaffected.
	// Skipped on dry-run (it writes a config file).
	if !f.DryRun {
		if err := cleanup.EnsureNoAutoAssemble("/etc/mdadm.conf", "/etc/mdadm/mdadm.conf"); err != nil {
			logger.Warn("could not disable md auto-assembly", "err", err.Error())
		}
		// LVM analog: stop udev's pvscan from re-activating a stale VG during
		// cleanup/recreate, which would re-create the mapper node teardown just
		// removed and break the next lvcreate (HW regression, node001).
		if err := cleanup.EnsureNoLVMAutoActivate("/etc/lvm/lvmlocal.conf"); err != nil {
			logger.Warn("could not disable lvm auto-activation", "err", err.Error())
		}
		// Belt to the use_devicesfile=0 override above: on SLE 15.x that override
		// did not stop a pre-seeded devices file from rejecting pvcreate on a
		// serialed partition (exit 5). Remove the devices file outright so no
		// distro's lvm can enforce it during the install.
		if err := cleanup.RemoveLVMDevicesFile("/etc/lvm/devices/system.devices"); err != nil {
			logger.Warn("could not remove lvm devices file", "err", err.Error())
		}
	}

	// Step: unmount any stale /sysroot tree once before any set.
	logger.SetStage("unmount_sysroot")
	mounter := mount.New(mount.Options{Runner: runner, Logger: logger})
	if err := mounter.UnmountSysrootTree(context.Background()); err != nil {
		errln(stderr, "unmount sysroot tree: %v", err)
		return 1
	}

	// Build the per-set processor with every Phase 1–6 module hooked in.
	sp := &setProcessor{
		runner:   runner,
		logger:   logger,
		lateWait: selector.LateDeviceWait,
		selector: selector.New(selector.Options{Runner: runner, Logger: logger, CandidatesOut: stderr}),
		cleaner:  cleanup.New(cleanup.Options{Runner: runner, Logger: logger}),
		part:     partition.New(partition.Options{Runner: runner, Logger: logger, DryRun: f.DryRun}),
		raid:     raid.New(raid.Options{Runner: runner, Logger: logger}),
		lvm:      lvm.New(lvm.Options{Runner: runner, Logger: logger}),
		fs:       fsmod.New(fsmod.Options{Runner: runner, Logger: logger, MkfsFatName: tools.MkfsFat}),
		mount:    mounter,
		checker:  fscheck.New(fscheck.Options{Runner: runner, Logger: logger, FsckVfatCmd: tools.FsckVfat}),

		sanitizeProgress: filepath.Join(filepath.Dir(f.ResultOut), sanitize.ProgressFile),
	}

	// Discover the disk pool once: selection=discover resolution and sanitize
	// both draw from it. No size fences — the per-set match does sizing.
	mode := ic.InstallMode
	if pool, derr := listDevices(context.Background(), discover.Options{
		Runner: runner, Names: discover.NamesKernel,
	}); derr != nil {
		// Sanitize erases exactly the pool: an unlisted pool would "succeed"
		// having erased nothing, and the node would power off as if erased.
		// That is an error (it may be transient: exit 1), never a success.
		if mode.IsSanitize() {
			errln(stderr, "sanitize: device discovery failed, nothing was erased: %v", derr)
			return 1
		}
		logger.Warn("device discovery failed", "err", derr.Error())
		sp.poolErr = derr
	} else {
		sp.pool = pool
	}

	results, resolvedSets, err := sp.runDecision(context.Background(), mode, cfg, detectedFirmware, f.DryRun, f.Wipe && !f.DryRun)
	if err != nil {
		return exitForError(stderr, err)
	}

	logger.SetStage("final_output")
	// Sanitize is erase-only by design: it produces no per-set storage results
	// and the pipeline installs no OS image or bootloader after it. An empty
	// result set is therefore SUCCESS here, not a write error -- previously it
	// fell through to WriteAuto's "no results" error and exited 1, so the
	// wrapper re-erased the (already blank) disks every 30s forever
	// (FTA v2 BE-Z1 / MCS-18, barrier NB-1). Other modes keep the fail-closed
	// behaviour: for them an empty result set means something went wrong.
	if mode.IsSanitize() && len(results) == 0 {
		if sp.sanitized > 0 {
			logger.UserInfo("sanitize completed: %d local disk(s) erased; no storage results by design", sp.sanitized)
		} else {
			logger.UserInfo("sanitize completed: no local disks found -- nothing was erased; no storage results by design")
		}
		return 0
	}
	if err := result.WriteResolved(f.ResolvedOut, resolvedSets); err != nil {
		errln(stderr, "write resolved: %v", err)
		return 1
	}
	if err := result.WriteAuto(f.ResultOut, results); err != nil {
		errln(stderr, "write result: %v", err)
		return 1
	}
	logger.KV("resolved_out", f.ResolvedOut)
	logger.KV("result_out", f.ResultOut)
	logger.UserInfo("storage-resolved: %s", f.ResolvedOut)
	logger.UserInfo("storage-result:   %s", f.ResultOut)

	// Always emit the concrete layout as a paste-ready selection=manual
	// disklayout so an operator can pin this node by hand (decisions/0007).
	emitPinnableLayout(logger, f.ExpectHost, buildPinnableConfig(cfg, resolvedSets, nil))

	// When a discover set asked for it (save:true), also write the resolved
	// layout back to the node-level disklayout so the next boot reproduces it
	// (decisions/0008). Non-fatal: a failure is logged, not install-aborting.
	if err := maybeWriteBack(context.Background(), writeBackParams{
		cfg: cfg, resolved: resolvedSets, dryRun: f.DryRun,
		cmdlinePath: f.CmdlinePath, tokenSource: f.LunaInstallScript,
		insecure: true, logger: logger,
	}); err != nil {
		logger.Error("save: disklayout write-back failed (layout is on disk; paste block above remains the fallback)", "err", err.Error())
	}

	if !f.SkipFinalInventory {
		if _, err := inventory.FinalState(context.Background(), inventory.FinalStateOptions{
			Runner:    runner,
			Dir:       defaultRuntimeDir,
			HasMDStat: inventory.MDStatProbe(),
		}); err != nil {
			logger.Warn("final inventory capture failed", "err", err.Error())
		}
	}

	return 0
}

// listDevices enumerates the discover pool; a test seam (the real one runs
// lsblk against the host the suite runs on).
var listDevices = discover.ListDevices

// exitForError maps a runDecision failure onto the exit contract: safety halts
// (data fence, persistent protection, device overlap, ...) exit 2 and
// deterministic configuration errors exit 3 -- both park the node for an
// operator -- and only what may clear on retry exits 1.
func exitForError(stderr io.Writer, err error) int {
	var se *safetyError
	if errors.As(err, &se) {
		return cli.ExitSafetyRefusal(stderr, toolName, se.reason, "%s", se.msg)
	}
	var fe *fatalConfigError
	if errors.As(err, &fe) {
		return cli.ExitFatalConfig(stderr, toolName, fe.reason, "%s", fe.msg)
	}
	if tool := lvmCrashed(err); tool != "" {
		return cli.ExitSafetyRefusal(stderr, toolName, "lvm-crashed",
			"%s was killed by a signal (%v): lvm2 itself crashed while reading the disks, which usually means damaged LVM metadata remnants on one of them (an lvm2 bug); it crashes the same way on every attempt -- find the device with `pvs -vvvv`, wipe its first MiBs, then retry; no further disk was touched", tool, err)
	}
	errln(stderr, "%v", err)
	return 1
}

// lvmCrashed returns the lvm tool named in err when that tool was killed by a
// signal (SIGSEGV, SIGABRT), or "". A crashing lvm2 crashes again on the next
// attempt with the same disks, so retrying for ever helps nobody.
func lvmCrashed(err error) string {
	msg := err.Error()
	if !strings.Contains(msg, "signal: ") {
		return ""
	}
	for _, tool := range []string{"pvcreate", "vgcreate", "lvcreate", "vgchange", "vgremove", "pvremove", "lvremove", "pvs", "vgs", "lvs"} {
		if strings.Contains(msg, tool+" ") || strings.Contains(msg, tool+":") {
			return tool
		}
	}
	return ""
}

// setProcessor bundles the per-set module instances so process() reads
// like a checklist instead of a long stack of constructors.
type setProcessor struct {
	runner   executor.Runner
	logger   *txlog.Logger
	selector *selector.Resolver
	cleaner  *cleanup.Cleaner
	part     *partition.Builder
	raid     *raid.Builder
	lvm      *lvm.Builder
	fs       *fsmod.Builder
	mount    *mount.Mounter
	checker  *fscheck.Checker  // health probe for the data fence
	pool     []discover.Device // discovered disks for selection=discover + sanitize
	// sanitizeReadBack overrides the sanitize read-back verification (tests).
	sanitizeReadBack func(disk string) (bool, error)
	// sanitizeProgress records the disks a sanitize erased, for a retry.
	sanitizeProgress string
	// protected holds the canonical kernel paths of every PERSISTENT set's
	// disks (filled by reserveManualSets). It is handed to cleanup as an
	// independent barrier: no destructive cleanup step may touch these
	// disks, whatever the selection logic decided.
	protected map[string]struct{}
	// resolveFn overrides selector.ResolveAll for device resolution; tests
	// inject it (the real selector needs /dev nodes). nil = use sp.selector.
	resolveFn func(ctx context.Context, devs []string) ([]selector.Resolved, error)
	// manualDisks / missing are filled by reserveManualSets: each manual
	// set's PRESENT devices, and the configured devices absent from the node.
	manualDisks map[string][]selector.Resolved
	missing     map[string][]string
	// lateWait bounds the wait for a manual set's device that is not there
	// yet (selector.LateDeviceWait; zero in tests).
	lateWait time.Duration
	// classifyFn overrides the storagehealth.Classify health probe (tests).
	classifyFn func(ctx context.Context, set *v2.Set, disks []selector.Resolved) storagehealth.Verdict
	// poolErr is the device-discovery failure, when listing the pool failed.
	poolErr error
	// sanitized counts the disks sanitize erased.
	sanitized int
}

// resolveForReservation resolves a manual set's configured devices to
// canonical records, via the test seam when set.
func (sp *setProcessor) resolveForReservation(ctx context.Context, devs []string) ([]selector.Resolved, error) {
	if sp.resolveFn != nil {
		return sp.resolveFn(ctx, devs)
	}
	return sp.selector.ResolveAll(ctx, devs)
}

// setAction is what process() should do with a resolved, non-persistent,
// non-memory set: create the layout from scratch, or mount an existing one.
type setAction int

const (
	actCreate setAction = iota // cleanup + partition/raid/lvm/fs + mount
	actMount                   // mount an existing layout, no cleanup/create
)

// prepareCreate is the destructive first half of a create: the pre-write
// identity re-check and the cleanup of the set's disks. executePlans runs it
// for every create set before any set is built.
func (sp *setProcessor) prepareCreate(ctx context.Context, p plannedSet, dryRun bool) error {
	set := p.set
	// Identity re-verification first (FTA BR-9/MCS-6, TOCTOU): the serial
	// captured at resolve time is re-read immediately before the first
	// destructive step. A udev rescan / hotplug between resolve and here can
	// re-point a kernel name at a DIFFERENT physical disk; a mismatch is a
	// halt, not a guess. Disks that report no serial (common on virtio)
	// verify vacuously — the mount/cleanup guards remain for those.
	sp.logger.SetStage("identity_check")
	if !dryRun {
		for _, d := range p.disks {
			if err := sp.verifyDiskIdentity(ctx, d); err != nil {
				return err
			}
		}
	}

	sp.logger.SetStage("cleanup")
	if _, err := sp.cleaner.CleanupSelectedSet(ctx, cleanup.Set{
		Name:          set.VGName(),
		SelectedDisks: diskPaths(p.disks),
		OwnsSysroot:   set.HasRootVolume(),
		Persistent:    set.Persistent,
		Protected:     cleanup.SelectedDisksSet(protectedPaths(sp.protected)),
	}); err != nil {
		return cleanupError(set.Name, err)
	}
	return nil
}

// process runs the per-set cascade for the planned action and returns the
// StorageResult + ResolvedSet that go into the two output JSONs. The plan
// carries the set's resolved disks (planSet); a create's identity check and
// cleanup already ran (prepareCreate).
func (sp *setProcessor) process(ctx context.Context, p plannedSet, detectedFirmware string, dryRun bool) (result.StorageResult, result.ResolvedSet, error) {
	set := p.set
	storageType := string(set.StorageType())
	rmode := set.RAIDMode()
	selectedDisks := p.disks

	selectorType := "memory"
	if storageType != "memory" {
		st, err := set.SelectorType()
		if err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fatalConfigf("selector", "set %q: selector: %v", set.Name, err)
		}
		selectorType = string(st)
		// Serial-audit (BE-N5): an empty or duplicated serial makes the
		// pre-write identity re-check vacuous. Warn, never fail —
		// serial-less disks are normal on plain virtio.
		for _, w := range serialCollisionWarnings(selectedDisks) {
			sp.logger.Warn(w)
		}
	}

	// The detected firmware mode is recorded on the set; whether it installs a
	// bootloader was decided up front by preflightBoot (GRUB is UEFI-only).
	firmwareMode := detectedFirmware

	resolvedRecord := result.ResolvedSet{
		Name:          set.Name,
		Role:          set.Role,
		FirmwareMode:  firmwareMode,
		SelectorType:  selectorType,
		StorageType:   storageType,
		RAIDMode:      rmode,
		SelectedDisks: selectedDisks,
	}

	// Memory branch: no partition/raid/lvm; just mount the tmpfs.
	if storageType == "memory" {
		sp.logger.SetStage("filesystems")
		fs := fsmod.MemoryFilesystems(set)
		sp.logger.SetStage("mounts")
		mt, err := sp.mount.MountSysrootTree(ctx, fs)
		if err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("mount: %w", err)
		}
		res := result.Assemble(result.AssembleInput{
			Set: set, FirmwareMode: firmwareMode,
			Selected: selectedDisks, Filesystems: fs, Mount: mt,
			FirstInstall: p.firstInstall,
		})
		return res, resolvedRecord, nil
	}

	// Mount-existing branch: persistent sets (hands-off) and healthy refreshes
	// (sync/auto on an existing layout) mount their volumes against the
	// predicted device paths with no cleanup and no create. first_install
	// stays false because the layout was not rebuilt.
	if set.Persistent || p.act == actMount {
		// Activate the existing layout first (FTA D1): event-based LVM
		// autoactivation is deliberately disabled for the whole install
		// (EnsureNoLVMAutoActivate), and persistent/local sets skip the
		// health probe that used to activate as a side effect — so without
		// an explicit assemble + vgchange the predicted device paths never
		// appear and the mount fails. Best-effort (already-active is fine);
		// the mount below remains the hard gate.
		sp.logger.SetStage("activate_existing")
		sp.activateExisting(ctx, set, rmode, selectedDisks)
		sp.logger.SetStage("filesystems")
		// Raw partition paths are anchored on the first PRESENT disk: a
		// degraded raid set may be missing its first configured member.
		var firstDisk, firstConfigured string
		if len(selectedDisks) > 0 {
			firstDisk = selectedDisks[0].ResolvedPath
			firstConfigured = selectedDisks[0].ConfiguredPath
			if firstConfigured == "" {
				firstConfigured = firstDisk
			}
		}
		biosBoot := set.Role == "os" && storagehealth.HasBIOSBootPartition(ctx, sp.runner, firstDisk)
		fs := fsmod.PredictFilesystemsOnDisk(set, firstConfigured, biosBoot)
		sp.logger.SetStage("mounts")
		mt, err := sp.mount.MountSysrootTree(ctx, fs)
		if err != nil {
			// The layout already exists and is not rebuilt here, so a mount
			// that fails now fails identically on every retry (local on a
			// damaged disk looped "retrying in 30s" for ever). Park for an
			// operator: safety refusal, exit 2.
			return result.StorageResult{}, result.ResolvedSet{}, safetyRefusalf("mount-existing",
				"set %q: the existing layout cannot be mounted (%v); %s", set.Name, err, mountExistingAdvice(set))
		}
		res := result.Assemble(result.AssembleInput{
			Set: set, FirmwareMode: firmwareMode,
			Selected: selectedDisks, Filesystems: fs, Mount: mt,
			FirstInstall: false,
			Persistent:   set.Persistent,
		})
		// No Layout was built, so record the boot partition numbers the
		// builder gave this disk: lpart-bootloader-finalise reads them (BIOS
		// GRUB refuses without bios_boot_partition; efibootmgr --create needs
		// the ESP number, which is 2 on a disk laid out under BIOS).
		res.ESPPartition, res.BIOSBootPartition = existingBootPartitions(set, biosBoot)
		if !dryRun {
			if err := result.RecordBlockUUIDs(ctx, sp.runner, &res); err != nil {
				return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("record uuids: %w", err)
			}
		}
		return res, resolvedRecord, nil
	}

	// Create branch (actCreate): the disks were identity-checked and cleaned
	// by prepareCreate; build the layout from scratch.
	sp.logger.SetStage("partitioning")
	layout, err := sp.part.BuildLayout(ctx, set, selectedDisks, firmwareMode)
	if err != nil {
		var tooSmall *partition.DiskTooSmallError
		if errors.As(err, &tooSmall) {
			return result.StorageResult{}, result.ResolvedSet{}, fatalConfigf("disk-too-small", "set %q: %v", set.Name, err)
		}
		return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("partition: %w", err)
	}

	var mdState raid.State
	var lvmState lvm.State
	if storageType == "lvm" {
		sp.logger.SetStage("raid")
		mdState, err = sp.raid.Build(ctx, set, layout)
		if err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("raid: %w", err)
		}
		sp.logger.SetStage("lvm")
		lvmState, err = sp.lvm.Build(ctx, set, layout, mdState)
		if err != nil {
			var fe *lvm.ForeignVGError
			if errors.As(err, &fe) {
				return result.StorageResult{}, result.ResolvedSet{}, safetyRefusalf("foreign-vg", "set %q: %v", set.Name, err)
			}
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("lvm: %w", err)
		}
	}

	sp.logger.SetStage("filesystems")
	fs, err := sp.fs.MakeFilesystems(ctx, set, layout, mdState, lvmState)
	if err != nil {
		return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("fs: %w", err)
	}

	sp.logger.SetStage("mounts")
	mt, err := sp.mount.MountSysrootTree(ctx, fs)
	if err != nil {
		return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("mount: %w", err)
	}

	sp.logger.SetStage("output_json")
	res := result.Assemble(result.AssembleInput{
		Set: set, FirmwareMode: firmwareMode,
		Selected: selectedDisks, Layout: layout, RAID: mdState, LVM: lvmState,
		Filesystems: fs, Mount: mt,
		FirstInstall: p.firstInstall,
	})
	if !dryRun {
		if err := result.RecordBlockUUIDs(ctx, sp.runner, &res); err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("record uuids: %w", err)
		}
	}
	return res, resolvedRecord, nil
}

// existingBootPartitions returns the ESP and BIOS boot partition NUMBERS of an
// existing os-set layout, as the partition builder numbered them: the BIOS
// boot partition (EF02) is partition 1 when the disk has one, and the ESP
// follows it (Set.PartitionNumber). 0 = none.
// mountExistingAdvice is the operator hint of a mount-existing refusal. A
// persistent set is never repartitioned or formatted, in any mode: pointing
// at install_mode full (which the node may already run) sent the operator in
// a circle.
func mountExistingAdvice(set *v2.Set) string {
	if set.Persistent {
		return "a persistent set is never repartitioned or formatted, in any install_mode -- operator review required: check its disks and that the layout names the disks that hold it, or remove \"persistent\" from the set so install_mode full recreates it (its data is then lost)"
	}
	return "it is not repartitioned in this mode -- operator review required (install_mode full rebuilds the set)"
}

func existingBootPartitions(set *v2.Set, biosBoot bool) (esp, bios int) {
	if set.Role != "os" {
		return 0, 0
	}
	if biosBoot {
		bios = 1
	}
	for _, v := range set.Volumes {
		if v.Provider == "partition" && v.Mountpoint == "/boot/efi" {
			esp = set.PartitionNumber(v, biosBoot)
			break
		}
	}
	return esp, bios
}

// cleanupError classifies a cleanup failure. A protected-disk barrier refusal
// is deterministic -- the layout touches a persistent set's disk and refuses
// identically on every retry -- so it is a safety refusal (exit 2, park for an
// operator), not a retryable exit 1 that loops every 30 s for ever.
func cleanupError(setName string, err error) error {
	if errors.Is(err, cleanup.ErrProtected) {
		return safetyRefusalf("protected-disk", "set %q: %v", setName, err)
	}
	return fmt.Errorf("cleanup: %w", err)
}

// serialCollisionWarnings audits the resolved disk set for identity
// ambiguity (BE-N5): a selected disk with no serial, or two selected disks
// sharing one non-empty serial, cannot be told apart by the pre-write
// identity re-check (verifyDiskIdentity), which then passes vacuously.
// Returns the warning strings so the audit is unit-testable; the caller
// logs them. Warn-only by design — the mount/cleanup guards still apply.
func serialCollisionWarnings(disks []selector.Resolved) []string {
	var warns []string
	firstBySerial := map[string]string{}
	for _, d := range disks {
		serial := strings.TrimSpace(d.Serial)
		if serial == "" {
			warns = append(warns, fmt.Sprintf(
				"disk %s reports no serial: the pre-write identity re-check cannot verify it (BE-N5)",
				d.ResolvedPath))
			continue
		}
		if first, ok := firstBySerial[serial]; ok {
			warns = append(warns, fmt.Sprintf(
				"disks %s and %s share serial %q: the pre-write identity re-check cannot tell them apart (BE-N5)",
				first, d.ResolvedPath, serial))
			continue
		}
		firstBySerial[serial] = d.ResolvedPath
	}
	return warns
}

// verifyDiskIdentity re-reads a resolved disk's SERIAL via lsblk and
// compares it to the value captured at resolve time. Empty captured serial
// (virtio without serial=) passes vacuously. A read failure or a mismatch
// is a safety refusal: the kernel name may now point at a different
// physical disk than the one the operator's config selected.
func (sp *setProcessor) verifyDiskIdentity(ctx context.Context, d selector.Resolved) error {
	if strings.TrimSpace(d.Serial) == "" {
		return nil
	}
	res, err := sp.runner.Run(ctx, executor.CommandSpec{
		Path: "lsblk", Args: []string{"-dn", "-o", "SERIAL", d.ResolvedPath},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return safetyRefusalf("identity",
			"cannot re-read serial of %s before destructive work (%v) — refusing (fail-closed)", d.ResolvedPath, err)
	}
	now := strings.TrimSpace(string(res.Stdout))
	if now != strings.TrimSpace(d.Serial) {
		return safetyRefusalf("identity",
			"disk %s changed identity between resolve and write: serial was %q, is now %q — device names moved (hotplug/rescan?); refusing",
			d.ResolvedPath, d.Serial, now)
	}
	return nil
}

// activateExisting brings a mount-existing set's block devices online:
// assemble this set's md arrays (storage + the os boot mirrors) by their
// deterministic names, then activate its VG. Every step is best-effort and
// idempotent — already-running arrays / active VGs are no-ops — because the
// subsequent mount is the hard gate with the meaningful error.
func (sp *setProcessor) activateExisting(ctx context.Context, set *v2.Set, rmode string, disks []selector.Resolved) {
	_ = rmode // the shared routine reads the raid mode from the set itself
	paths := make([]string, 0, len(disks))
	for _, d := range disks {
		paths = append(paths, d.ResolvedPath)
	}
	// Assemble with EXPLICIT members (md auto-assembly is disabled for the
	// whole install), then vgchange; shared with the health probe so both see
	// the same devices. See storagehealth.ActivateExisting.
	storagehealth.ActivateExisting(ctx, sp.runner, set, paths, sp.logger, false)
}

// protectedPaths renders the protected-disk set as a path slice for
// cleanup.SelectedDisksSet (which reduces to basenames).
func protectedPaths(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	return out
}

// detectFirmware mirrors /root/NEWTREE/lib/common.sh::detect_firmware_mode.
// It is a package var (not a plain func) so tests can pin the result
// independently of the host the suite runs on.
var detectFirmware = func() string {
	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		return "uefi"
	}
	return "bios"
}
