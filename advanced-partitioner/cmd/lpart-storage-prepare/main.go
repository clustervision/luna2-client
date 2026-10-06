// lpart-storage-prepare wires every Phase 1–6 module into the destructive
// installer binary. Behavioural reference: /root/NEWTREE/bin/lpart-storage-prepare.
//
// Flow:
//  1. Parse flags, init logger, installer-env guard, command preflight
//  2. Load + validate the v2 storage JSON
//  3. Wipe + host guards
//  4. Unmount any stale /sysroot tree (once)
//  5. For each set in OS-first execution order:
//     - resolve devices via selector
//     - run safety check (if .safety is present)
//     - cleanup the selected disks
//     - memory: build memory filesystems, mount, assemble result
//     - disk:   partition -> raid -> lvm -> fs -> mount ->
//     RecordBlockUUIDs -> assemble result
//  6. Emit storage-result.json (single object or multi-set wrapper)
//     and storage-resolved.json; capture final inventory state.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

const toolName = "lpart-storage-prepare"

func main() {
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
		return 2
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

	ic, err := installcontext.LoadFile(f.InstallContextPath)
	if err != nil {
		errln(stderr, "install-context: %v", err)
		return 1
	}
	logger.KV("install_mode", string(ic.InstallMode))

	// Installer-env guard.
	logger.SetStage("installer_check")
	if err := sysenv.RequireInstallerEnv(sysenv.Options{
		CmdlinePath:       f.CmdlinePath,
		AllowNonInstaller: f.AllowNonInstaller,
	}); err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	// Command preflight (the FAT tool name comes out of this).
	logger.SetStage("command_checks")
	fatTool := "mkfs.fat"
	if !f.SkipCommandCheck {
		chosen, err := sysenv.RequireStoragePrepareCommands()
		if err != nil {
			errln(stderr, "%v", err)
			return 1
		}
		fatTool = chosen
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
			errln(stderr, "%v", lerr)
			return 1
		}
		if verr := loaded.ValidateRaw(raw); verr != nil {
			errln(stderr, "%v", verr)
			return 1
		}
		cfg = loaded
	}
	logger.KV("config_path", f.ConfigPath)
	logger.KV("dry_run", f.DryRun)
	logger.KV("wipe_approved", f.Wipe)
	logger.KV("details", f.Details)

	if !f.DryRun && !f.Wipe {
		errln(stderr, "--wipe is required for destructive execution")
		return 1
	}
	if !f.DryRun {
		logger.SetStage("expected_host_check")
		if err := sysenv.RequireExpectedHost(f.ExpectHost, sysenv.Options{
			Hostname: f.HostnameFunc,
		}); err != nil {
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
	// exactly like a UEFI one; only GRUB, shim and the UEFI boot order are
	// skipped, because BIOS has no NVRAM boot-entry management and no mirrored
	// boot chain. Nothing is lost by that: Luna PXE-loads the kernel on every
	// boot anyway. The one case that must stop is a bootloader-less disk OS
	// behind an install ramdisk that reboots instead of pivoting -- refuse
	// before any destructive action, via the safety_refusal contract so the
	// wrapper parks for operator review instead of retrying for ever.
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
	case detectedFirmware != "uefi" && plan.DiskOS:
		logger.Warn(fmt.Sprintf("firmware is %q: GRUB, shim and the UEFI boot order are skipped (UEFI-only) -- the OS is installed to disk but the node PXE-boots every time", detectedFirmware))
		if plan.BootloaderNone || (cfg != nil && cfg.OSSet() != nil && cfg.OSSet().BootOrderOrLast() == "first") {
			logger.UserInfo("the OS set's bootloader key is ignored on %q firmware: there is no UEFI boot order to arrange", detectedFirmware)
		}
	case detectedFirmware != "uefi":
		logger.UserInfo("firmware is %q: allowed -- this run installs no bootloader (the node boots via PXE)", detectedFirmware)
	case plan.BootloaderNone:
		logger.Warn(`the OS set asks for bootloader "none": no GRUB, no shim and no UEFI boot entry, and any existing bootloader on the disk is removed -- the node PXE-boots every time`)
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
		selector: selector.New(selector.Options{Runner: runner, Logger: logger, CandidatesOut: stderr}),
		cleaner:  cleanup.New(cleanup.Options{Runner: runner, Logger: logger}),
		part:     partition.New(partition.Options{Runner: runner, Logger: logger, DryRun: f.DryRun}),
		raid:     raid.New(raid.Options{Runner: runner, Logger: logger}),
		lvm:      lvm.New(lvm.Options{Runner: runner, Logger: logger}),
		fs:       fsmod.New(fsmod.Options{Runner: runner, Logger: logger, MkfsFatName: fatTool}),
		mount:    mounter,
		checker:  fscheck.New(fscheck.Options{Runner: runner, Logger: logger}),
	}

	// Discover the disk pool once: selection=discover resolution and sanitize
	// both draw from it. No size fences — the per-set match does sizing.
	if pool, derr := discover.ListDevices(context.Background(), discover.Options{
		Runner: runner, Names: discover.NamesKernel,
	}); derr != nil {
		logger.Warn("device discovery failed", "err", derr.Error())
	} else {
		sp.pool = pool
	}

	mode := ic.InstallMode
	results, resolvedSets, err := sp.runDecision(context.Background(), mode, cfg, detectedFirmware, f.DryRun, f.Wipe && !f.DryRun)
	if err != nil {
		// Safety halts (data fence, persistent protection, device overlap)
		// use the repo's exit-code-2 safety_refusal contract so the wrapper
		// parks for operator review instead of retrying a permanent refusal.
		var se *safetyError
		if errors.As(err, &se) {
			return cli.ExitSafetyRefusal(stderr, toolName, se.reason, "%s", se.msg)
		}
		errln(stderr, "%v", err)
		return 1
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
		logger.UserInfo("sanitize completed: all discovered disks erased; no storage results by design")
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
	// protected holds the canonical kernel paths of every PERSISTENT set's
	// disks (filled by reserveManualSets). It is handed to cleanup as an
	// independent barrier: no destructive cleanup step may touch these
	// disks, whatever the selection logic decided.
	protected map[string]struct{}
	// resolveFn overrides selector.ResolveAll for reservation; tests inject
	// it (the real selector needs /dev nodes). nil = use sp.selector.
	resolveFn func(ctx context.Context, devs []string) ([]selector.Resolved, error)
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

// process runs the per-set cascade for the decided action and returns the
// StorageResult + ResolvedSet that go into the two output JSONs. Device
// resolution for selection=discover sets is done by the caller (applySet), which
// fills set.Devices before calling process.
func (sp *setProcessor) process(ctx context.Context, set *v2.Set, detectedFirmware string, act setAction, dryRun, firstInstall bool) (result.StorageResult, result.ResolvedSet, error) {
	storageType := string(set.StorageType())
	rmode := set.RAIDMode()

	var (
		selectedDisks []selector.Resolved
		selectorType  string
	)
	if storageType == "memory" {
		selectorType = "memory"
	} else {
		st, err := set.SelectorType()
		if err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("selector: %w", err)
		}
		selectorType = string(st)
		sp.logger.SetStage("resolve_set")
		resolved, err := sp.selector.ResolveAll(ctx, set.EffectiveDevices())
		if err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, err
		}
		selectedDisks = resolved
		// Serial-audit (BE-N5): an empty or duplicated serial makes the
		// pre-write identity re-check below vacuous. Warn, never fail —
		// serial-less disks are normal on plain virtio.
		for _, w := range serialCollisionWarnings(resolved) {
			sp.logger.Warn(w)
		}
		if err := partition.ValidateRAID(rmode, len(resolved)); err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, err
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
			FirstInstall: firstInstall,
		})
		return res, resolvedRecord, nil
	}

	// Mount-existing branch: persistent sets (hands-off) and healthy refreshes
	// (sync/auto on an existing layout) mount their volumes against the
	// predicted device paths with no cleanup and no create. first_install
	// stays false because the layout was not rebuilt.
	if set.Persistent || act == actMount {
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
		fs := fsmod.PredictFilesystemsForSet(set)
		sp.logger.SetStage("mounts")
		mt, err := sp.mount.MountSysrootTree(ctx, fs)
		if err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("mount: %w", err)
		}
		res := result.Assemble(result.AssembleInput{
			Set: set, FirmwareMode: firmwareMode,
			Selected: selectedDisks, Filesystems: fs, Mount: mt,
			FirstInstall: false,
			Persistent:   set.Persistent,
		})
		if !dryRun {
			if err := result.RecordBlockUUIDs(ctx, sp.runner, &res); err != nil {
				return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("record uuids: %w", err)
			}
		}
		return res, resolvedRecord, nil
	}

	// Create branch (actCreate): cleanup the selected disks then build the
	// layout from scratch.
	//
	// Identity re-verification first (FTA BR-9/MCS-6, TOCTOU): the serial
	// captured at resolve time is re-read immediately before the first
	// destructive step. A udev rescan / hotplug between resolve and here can
	// re-point a kernel name at a DIFFERENT physical disk; a mismatch is a
	// halt, not a guess. Disks that report no serial (common on virtio)
	// verify vacuously — the mount/cleanup guards remain for those.
	sp.logger.SetStage("identity_check")
	if !dryRun {
		for _, d := range selectedDisks {
			if err := sp.verifyDiskIdentity(ctx, d); err != nil {
				return result.StorageResult{}, result.ResolvedSet{}, err
			}
		}
	}

	sp.logger.SetStage("cleanup")
	disks := make([]string, 0, len(selectedDisks))
	for _, d := range selectedDisks {
		disks = append(disks, d.ResolvedPath)
	}
	if _, err := sp.cleaner.CleanupSelectedSet(ctx, cleanup.Set{
		Name:          set.VGName(),
		SelectedDisks: disks,
		OwnsSysroot:   set.HasRootVolume(),
		Persistent:    set.Persistent,
		Protected:     cleanup.SelectedDisksSet(protectedPaths(sp.protected)),
	}); err != nil {
		return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("cleanup: %w", err)
	}

	sp.logger.SetStage("partitioning")
	layout, err := sp.part.BuildLayout(ctx, set, selectedDisks, firmwareMode)
	if err != nil {
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
		FirstInstall: firstInstall,
	})
	if !dryRun {
		if err := result.RecordBlockUUIDs(ctx, sp.runner, &res); err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("record uuids: %w", err)
		}
	}
	return res, resolvedRecord, nil
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
	run := func(path string, args ...string) {
		if _, err := sp.runner.Run(ctx, executor.CommandSpec{
			Path: path, Args: args,
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			sp.logger.Info("activate (best-effort) " + path + " " + strings.Join(args, " ") + ": " + err.Error())
		}
	}
	if rmode != "" && rmode != "none" {
		mdName := raid.MDNameForSet(set.Name)
		// Assemble with EXPLICIT members. EnsureNoAutoAssemble writes an
		// mdadm.conf with no ARRAY lines and auto-assembly disabled for the
		// whole install, so the previous name-only `mdadm --assemble
		// /dev/md/<name>` had no way to find its members and ALWAYS failed
		// (exit 1) — the persistent-RAID mount path was dead on arrival
		// (regression 3.51 caught it: assemble fails → VG never appears →
		// mount /dev/<vg>/<lv> exits 32 → retry-park). Members are found by
		// examining each partition on THIS set's resolved disks, so nothing
		// outside the set is ever assembled — consistent with the
		// no-auto-assemble safety posture.
		sp.assembleExplicit(ctx, mdName, disks, run)
		if set.Role == "os" {
			sp.assembleExplicit(ctx, mdName+"-efi", disks, run)
			sp.assembleExplicit(ctx, mdName+"-boot", disks, run)
		}
		run("udevadm", "settle")
	}
	if set.StorageType() == v2.StorageLVM {
		// Manual activation is unaffected by the disabled event-based
		// autoactivation (auto_activation_volume_list only governs udev's
		// pvscan) — this is the sanctioned way to bring the VG up.
		run("vgchange", "-ay", set.VGName())
		run("udevadm", "settle")
	}
}

// assembleExplicit assembles /dev/md/<mdName> from the member partitions it
// finds on the given disks (mdadm --examine, superblock Name match). No
// members found is a no-op: the array may already be running, and the
// subsequent mount stays the hard gate.
func (sp *setProcessor) assembleExplicit(ctx context.Context, mdName string, disks []selector.Resolved, run func(string, ...string)) {
	var members []string
	for _, d := range disks {
		for _, p := range sp.partitionsOn(ctx, d.ResolvedPath) {
			if sp.mdSuperblockName(ctx, p) == mdName {
				members = append(members, p)
			}
		}
	}
	if len(members) == 0 {
		sp.logger.Info("activate: no member partitions with superblock name %q on the set's disks (array already running, or not raid)", mdName)
		return
	}
	run("mdadm", append([]string{"--assemble", "/dev/md/" + mdName}, members...)...)
}

// partitionsOn lists the partition device paths on one disk. KNAME, not
// PATH: the PATH column arrived in util-linux 2.33 and el8 ships 2.32 — with
// PATH the call fails there and activateExisting saw zero partitions, so a
// persistent md could never be assembled on el8 (regression 3.51 "did not
// boot", second site of the same bug as cleanup.partitionsOf; discover.go's
// lsblkDiscoverColumns comment documents the rule).
func (sp *setProcessor) partitionsOn(ctx context.Context, disk string) []string {
	res, err := sp.runner.Run(ctx, executor.CommandSpec{
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
// ("Name : any:data (...)" → "data"), or "" when the partition has none.
func (sp *setProcessor) mdSuperblockName(ctx context.Context, part string) string {
	res, err := sp.runner.Run(ctx, executor.CommandSpec{
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
