// lpart-storage-check verifies whether existing storage matches the v2
// intent. Exit codes follow the repo-wide contract (internal/cli), which
// lpart-node-installer maps to retry (1) or park (2/3):
//
//	0  all sets healthy; filesystems mounted under /sysroot
//	1  retryable: an operational failure that may pass on the next attempt
//	   (log init, unmounting a stale /sysroot, writing the result file)
//	2  safety refusal: a set is unhealthy, or the healthy-looking existing
//	   layout cannot be mounted -- local mode never rebuilds, so it needs
//	   an operator (safety_refusal=<reason>)
//	3  fatal configuration error: identical on every attempt (bad flags,
//	   unreadable install context, not the installer env, a missing fsck
//	   tool, a missing/invalid disklayout, no resolved discover disks)
//	   (fatal_config=<reason>)
//
// Behavioural reference: /root/NEWTREE/bin/lpart-storage-check.
//
// Flow:
//  1. Parse flags, init logger, installer-env guard, command preflight
//  2. Load + validate the v2 storage JSON
//  3. Unmount any stale /sysroot tree
//  4. For each set:
//     - memory sets are always healthy
//     - disk sets: resolve devices → vgchange -ay (LVM) → per-volume
//     block-device exists + blkid FS-type match + fscheck.CheckFS
//  5. If any unhealthy: exit 2
//  6. Otherwise: compute filesystems from the config, mount the tree,
//     write storage-check-result.json with healthy=true.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	fsmod "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/storagehealth"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

const toolName = "lpart-storage-check"

func main() {
	defer cli.ExitOnPanic(os.Stderr, toolName)
	cli.EnsureInstallerPath()
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func errln(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, toolName+": "+format+"\n", args...)
}

type flags struct {
	cli.Common
	ConfigPath         string
	Sysroot            string
	Details            bool
	ResultOut          string
	InstallContextPath string
	ResolvedPath       string
	// test affordances
	CmdlinePath      string
	SkipCommandCheck bool
}

// checkResult is the JSON storage-check-result.json shape that
// lpart-storage-check emits on the happy path.
type checkResult struct {
	MountedTargetRoot string `json:"mounted_target_root"`
	Healthy           bool   `json:"healthy"`
	SetsChecked       int    `json:"sets_checked"`
}

const (
	defaultRuntimeDir = "/tmp/install_files/runtime"
	defaultResultOut  = defaultRuntimeDir + "/storage-check-result.json"
)

//nolint:gocyclo // command flow is intentionally linear; matches the bash predecessor section by section.
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		argv = []string{toolName}
	}

	var f flags
	fs := cli.NewToolFlagSet(toolName)
	f.Bind(fs)
	fs.StringVar(&f.ConfigPath, "config", "", "Path to v2 storage JSON")
	fs.StringVar(&f.Sysroot, "sysroot", mount.EffectiveSysroot(), "Mount target root (defaults to $rootmnt, else /sysroot)")
	fs.BoolVar(&f.Details, "details", false, "Print extended device inventory at startup")
	fs.StringVar(&f.ResultOut, "result-out", defaultResultOut, "Where to write storage-check-result.json")
	fs.StringVar(&f.InstallContextPath, "install-context", installcontext.DefaultJSONPath,
		"Path to install-context.json (mode/osimage-sync flags)")
	fs.StringVar(&f.ResolvedPath, "resolved", "",
		"Path to lpart-storage-prepare's storage-resolved.json: selection=discover sets are checked on the disks it selected")
	fs.StringVar(&f.CmdlinePath, "cmdline-path", "", "(test) override /proc/cmdline path")
	fs.BoolVar(&f.SkipCommandCheck, "skip-command-check", false, "(test) skip the storage-check command preflight")

	if err := fs.Parse(argv[1:]); err != nil {
		// A bad command line is the same on every attempt; exit 2 is reserved
		// for safety refusals.
		return cli.ExitFatalConfigCode
	}
	if f.ShowVersion {
		_, _ = fmt.Fprintln(stdout, cli.Version())
		return 0
	}
	// --dry-run cannot make storage-check read-only: the probes assemble
	// arrays, replay an XFS log by mounting and run e2fsck -p. What
	// --dry-run WOULD do is stub every probe to rc 0, making Classify
	// fabricate a verdict from evidence it never gathered: a false health
	// report an operator may act on (FTA v2 BE-F7, TE-D). Fail closed: refuse
	// the combination instead of emitting fiction. The pipeline never passes
	// --dry-run to this tool.
	if f.DryRun {
		return cli.ExitFatalConfig(stderr, toolName, "dry-run",
			"--dry-run is not supported: stubbing the storage-check probes "+
				"would fabricate health verdicts; run without --dry-run")
	}

	logger, err := f.NewLogger(toolName)
	if err != nil {
		errln(stderr, "log init: %v", err)
		return 1
	}
	defer func() { _ = logger.Close() }()
	logger.SetStage("startup")
	logger.StageBanner("Starting lpart-storage-check")

	ic, err := installcontext.LoadFile(f.InstallContextPath)
	if err != nil {
		return cli.ExitFatalConfig(stderr, toolName, "install-context", "install-context: %v", err)
	}
	logger.KV("install_mode", string(ic.InstallMode))
	if !ic.InstallMode.IsLocal() {
		logger.UserInfo("install_mode=%s — lpart-storage-check is the local-mode verifier; proceeding anyway",
			ic.InstallMode)
	}

	logger.SetStage("installer_check")
	if err := sysenv.RequireInstallerEnv(sysenv.Options{
		CmdlinePath:       f.CmdlinePath,
		AllowNonInstaller: f.AllowNonInstaller,
	}); err != nil {
		errln(stderr, "%v", err) // exit 1, as in every lpart tool
		return 1
	}

	logger.SetStage("command_checks")
	fsckVfat := "fsck.vfat"
	if !f.SkipCommandCheck {
		// A tool missing from the install initramfs (e2fsck, xfs_repair,
		// fsck.vfat/fsck.fat) is missing on every retry too.
		if err := sysenv.RequireCommands(sysenv.StorageCheckCommands...); err != nil {
			return cli.ExitFatalConfig(stderr, toolName, "missing-command", "%v", err)
		}
		chosen, err := sysenv.RequireCommandsWithFallback("fsck.vfat", "fsck.fat")
		if err != nil {
			return cli.ExitFatalConfig(stderr, toolName, "missing-command", "%v", err)
		}
		fsckVfat = chosen
	}

	// The disklayout is the daemon's input for this node: missing, unparsable
	// or invalid, it is the same on every retry -- park, do not loop.
	if f.ConfigPath == "" {
		return cli.ExitFatalConfig(stderr, toolName, "config-missing", "--config is required")
	}
	logger.SetStage("config_validation")
	cfg, raw, err := v2.LoadFile(f.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cli.ExitFatalConfig(stderr, toolName, "config-missing", "config not found: %s", f.ConfigPath)
		}
		return cli.ExitFatalConfig(stderr, toolName, "config-load", "%v", err)
	}
	if err := cfg.ValidateRaw(raw); err != nil {
		return cli.ExitFatalConfig(stderr, toolName, "config-validation", "%v", err)
	}
	for _, w := range cfg.Warnings {
		logger.Warn(w)
	}
	// A selection=discover set names no devices in the config: storage-prepare
	// picks them from the disk pool and records them in storage-resolved.json
	// only. Without them the check resolves an empty device list, reports
	// "set has no resolved devices" and parks every install_mode=local boot.
	if err := applyResolved(cfg, f.ResolvedPath); err != nil {
		return cli.ExitFatalConfig(stderr, toolName, "resolved-devices", "%v", err)
	}
	logger.KV("config_path", f.ConfigPath)
	logger.KV("resolved_path", f.ResolvedPath)
	logger.KV("sysroot", f.Sysroot)

	runner := executor.New(executor.Options{DryRun: f.DryRun, Logger: logger})
	mounter := mount.New(mount.Options{Runner: runner, Logger: logger, Sysroot: f.Sysroot})

	// Unconditional unmount of /sysroot tree before any verification.
	logger.SetStage("unmount_sysroot")
	if err := mounter.UnmountSysrootTree(context.Background()); err != nil {
		errln(stderr, "unmount sysroot: %v", err)
		return 1
	}

	logger.SetStage("check_sets")
	checker := fscheck.New(fscheck.Options{
		Runner: runner, Logger: logger, FsckVfatCmd: fsckVfat,
	})
	resolver := selector.New(selector.Options{
		Runner:        runner,
		Logger:        logger,
		CandidatesOut: stderr,
	})

	var failures []storagehealth.Unhealthy
	// First PRESENT device per set: the BIOS boot partition probe below must
	// not look at a configured member that is missing (degraded raid).
	presentFirst := map[string]string{}
	resolvedBySet := map[string][]selector.Resolved{}
	for i := range cfg.Sets {
		set := &cfg.Sets[i]
		storageType := set.StorageType()
		logger.UserInfo("Checking set %q (storage_type=%s)", set.Name, storageType)

		if storageType == "memory" {
			continue
		}

		var resolved []selector.Resolved
		var missing []string
		var err error
		if werr := selector.WaitWhileMissing(context.Background(), runner, selector.LateDeviceWait, func() (int, error) {
			resolved, missing, err = resolvePresent(context.Background(), resolver, set.EffectiveDevices())
			if err != nil && len(missing) == 0 {
				return 0, err // not a missing device: no point waiting
			}
			return len(missing), nil
		}); werr != nil {
			err = werr
		}
		if resolveRetryable(err) {
			errln(stderr, "set %q: %v", set.Name, err)
			return 1
		}
		if msg := degradedRefusal(set, len(resolved), missing); err == nil && msg != "" {
			return cli.ExitSafetyRefusal(stderr, toolName, "degraded", "%s", msg)
		}
		if err == nil && len(missing) > 0 && !set.RunsDegradedWith(len(resolved)) {
			err = fmt.Errorf("device(s) %s not present on this node, and raid %s cannot run without them (%d of %d device(s) present)",
				strings.Join(missing, ", "), set.Raid, len(resolved), len(set.EffectiveDevices()))
		}
		if err != nil {
			failures = append(failures, storagehealth.Unhealthy{
				SetName: set.Name, VolumeName: "(devices)",
				Reason: fmt.Sprintf("device resolution failed: %v", err),
			})
			logger.KV("verdict", set.Name+"=broken")
			continue
		}
		for _, m := range missing {
			// Same rule and wording as lpart-storage-prepare: a redundant
			// raid set that still assembles boots degraded, loudly.
			logger.Warn(fmt.Sprintf("set %s: running degraded, member %s missing; replace the disk and reinstall with install_mode full to rebuild", set.Name, m))
		}
		presentFirst[set.Name] = resolved[0].ResolvedPath
		verdict, fs := storagehealth.Classify(context.Background(), runner, checker, set, resolved, logger)
		logger.KV("verdict", set.Name+"="+string(verdict))
		if verdict == storagehealth.VerdictDegraded {
			return cli.ExitSafetyRefusal(stderr, toolName, "degraded", "%s", storagehealth.DegradedRefusal(set))
		}
		resolvedBySet[set.Name] = resolved
		failures = append(failures, fs...)
	}

	logger.SetStage("decision")
	if len(failures) > 0 {
		// Identify which failing sets were persistent. A persistent-set
		// failure is a safety refusal: the future UI can pattern-match
		// the marker to distinguish "things broke, see logs" from "do
		// NOT continue without operator review of the pinned set."
		persistentByName := map[string]bool{}
		for i := range cfg.Sets {
			if cfg.Sets[i].Persistent {
				persistentByName[cfg.Sets[i].Name] = true
			}
		}
		persistentFailed := false
		for _, u := range failures {
			if persistentByName[u.SetName] {
				persistentFailed = true
				break
			}
		}
		if persistentFailed {
			logger.Warn(fmt.Sprintf("Storage check FAILED: %d unhealthy volume(s) safety_refusal=persistent", len(failures)))
		} else {
			logger.Warn(fmt.Sprintf("Storage check FAILED: %d unhealthy volume(s)", len(failures)))
		}
		for _, u := range failures {
			marker := ""
			if persistentByName[u.SetName] {
				marker = " safety_refusal=persistent"
			}
			logger.Warn(fmt.Sprintf("  - set=%s vol=%s: %s%s", u.SetName, u.VolumeName, u.Reason, marker))
		}
		if persistentFailed {
			return cli.ExitSafetyRefusal(stderr, toolName, "persistent",
				"%d persistent set volume(s) unhealthy — operator review required", len(failures))
		}
		logger.UserInfo("Storage unhealthy — lpart-storage-prepare required")
		return cli.ExitSafetyRefusal(stderr, toolName, "unhealthy",
			"%d volume(s) unhealthy; install_mode=local does not rebuild -- operator review required (install_mode full rebuilds the layout)", len(failures))
	}

	logger.SetStage("mount")
	logger.UserInfo("All sets healthy — mounting storage tree")
	// the probe released what it activated: bring the sets up read-write
	for i := range cfg.Sets {
		set := &cfg.Sets[i]
		paths := make([]string, 0, len(resolvedBySet[set.Name]))
		for _, r := range resolvedBySet[set.Name] {
			paths = append(paths, r.ResolvedPath)
		}
		if len(paths) > 0 {
			storagehealth.ActivateExisting(context.Background(), runner, set, paths, logger, false)
		}
	}
	predicted := fsmod.PredictFilesystemsFromConfig(cfg, func(set *v2.Set) bool {
		disk, ok := presentFirst[set.Name]
		if set.Role != "os" || !ok {
			return false
		}
		return storagehealth.HasBIOSBootPartition(context.Background(), runner, disk)
	})
	mt, err := mounter.MountSysrootTree(context.Background(), predicted)
	if err != nil {
		// Every set probed healthy, yet the existing layout does not mount: it
		// is not rebuilt in this mode, so this fails identically on every
		// retry. Park for an operator, as lpart-storage-prepare does.
		return cli.ExitSafetyRefusal(stderr, toolName, "mount-existing",
			"the existing layout cannot be mounted (%v); it is not repartitioned in this mode -- operator review required (install_mode full rebuilds it)", err)
	}

	// Write storage-check-result.json with the healthy/sets_checked
	// augmentation the bash version produced.
	result := checkResult{
		MountedTargetRoot: mt.MountedTargetRoot,
		Healthy:           true,
		SetsChecked:       len(cfg.Sets),
	}
	if err := writeJSON(f.ResultOut, result); err != nil {
		errln(stderr, "write result: %v", err)
		return 1
	}
	logger.KV("result_out", f.ResultOut)
	logger.UserInfo("Storage healthy — mounted under %s", f.Sysroot)
	logger.UserInfo("storage-check-result: %s", f.ResultOut)
	return 0
}

// applyResolved fills each selection=discover disk set's devices from
// storage-resolved.json (the disks lpart-storage-prepare selected for it, in
// its order). Manual and memory sets keep their configured devices.
func applyResolved(cfg *v2.Config, path string) error {
	var discover []*v2.Set
	for i := range cfg.Sets {
		set := &cfg.Sets[i]
		if set.SelectionMode() == "discover" && set.StorageType() != v2.StorageMemory && len(set.Devices) == 0 {
			discover = append(discover, set)
		}
	}
	if len(discover) == 0 {
		return nil
	}
	if path == "" {
		return fmt.Errorf("set %q is selection=discover: pass --resolved with lpart-storage-prepare's storage-resolved.json (the config names no devices)", discover[0].Name)
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return fmt.Errorf("read resolved devices: %w", err)
	}
	var agg result.ResolvedAggregate
	if err := json.Unmarshal(body, &agg); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	byName := make(map[string]result.ResolvedSet, len(agg.Sets))
	for _, rs := range agg.Sets {
		byName[rs.Name] = rs
	}
	for _, set := range discover {
		rs, ok := byName[set.Name]
		if !ok {
			return fmt.Errorf("set %q (selection=discover) is not in %s", set.Name, path)
		}
		devs := make([]string, 0, len(rs.SelectedDisks))
		for _, d := range rs.SelectedDisks {
			p := d.ConfiguredPath
			if p == "" {
				p = d.ResolvedPath
			}
			if p != "" {
				devs = append(devs, p)
			}
		}
		if len(devs) == 0 {
			return fmt.Errorf("set %q (selection=discover): %s lists no selected disks", set.Name, path)
		}
		set.Devices = devs
	}
	return nil
}

func writeJSON(path string, body any) error {
	enc, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	//nolint:gosec // operator-supplied output path
	return os.WriteFile(path, append(enc, '\n'), 0o640)
}

// resolvePresent resolves each configured device on its own. A device that
// does not exist on this node is returned in missing (the caller decides
// whether the set can run degraded); any other resolve error (mounted,
// read-only, not a whole disk) fails the set as before.
func resolvePresent(ctx context.Context, resolver *selector.Resolver, devices []string) (present []selector.Resolved, missing []string, err error) {
	for _, dev := range devices {
		r, rerr := resolver.Resolve(ctx, dev)
		if rerr != nil {
			if errors.Is(rerr, selector.ErrNotFound) {
				missing = append(missing, dev)
				continue
			}
			return nil, nil, rerr
		}
		present = append(present, r)
	}
	if len(present) == 0 && len(missing) > 0 {
		return nil, missing, fmt.Errorf("none of the set's devices (%s) is present on this node", strings.Join(missing, ", "))
	}
	return present, missing, nil
}

// resolveRetryable reports a device-resolution failure that may clear on
// retry (a failed lsblk probe): exit 1, as in lpart-storage-prepare, not a
// park for an operator.
func resolveRetryable(err error) bool {
	var re *selector.ResolveError
	return errors.As(err, &re) && re.Kind == selector.KindProbe
}

// degradedRefusal is the reason to park when a redundant raid set could run
// without its missing members but the layout does not allow it; "" otherwise.
// Same rule and wording as lpart-storage-prepare.
func degradedRefusal(set *v2.Set, present int, missing []string) string {
	if len(missing) == 0 || set.AllowDegraded || !set.RunsDegradedWith(present) {
		return ""
	}
	return fmt.Sprintf("set %q is degraded: device(s) %s not present on this node (%d of %d present); replace the disk and reinstall with install_mode full, or set allow_degraded on the set to install on it degraded",
		set.Name, strings.Join(missing, ", "), present, len(set.EffectiveDevices()))
}
