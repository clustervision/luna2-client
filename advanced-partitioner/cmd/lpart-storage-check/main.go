// lpart-storage-check verifies whether existing storage matches the v2
// intent. Exit codes (matching the bash predecessor):
//
//	0  all sets healthy; filesystems mounted under /sysroot
//	2  one or more sets unhealthy; caller must run lpart-storage-prepare
//	1  script error (bad args, missing file, unexpected failure)
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

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	fsmod "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/storagehealth"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

const toolName = "lpart-storage-check"

func main() {
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

// bash predecessor section by section.
//
//nolint:gocyclo // command flow is intentionally linear; matches the
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		argv = []string{toolName}
	}

	var f flags
	fs := cli.NewToolFlagSet(toolName)
	f.Bind(fs)
	fs.StringVar(&f.ConfigPath, "config", "", "Path to v2 storage JSON")
	fs.StringVar(&f.Sysroot, "sysroot", mount.DefaultSysroot, "Mount target root")
	fs.BoolVar(&f.Details, "details", false, "Print extended device inventory at startup")
	fs.StringVar(&f.ResultOut, "result-out", defaultResultOut, "Where to write storage-check-result.json")
	fs.StringVar(&f.InstallContextPath, "install-context", installcontext.DefaultJSONPath,
		"Path to install-context.json (mode/osimage-sync flags)")
	fs.StringVar(&f.CmdlinePath, "cmdline-path", "", "(test) override /proc/cmdline path")
	fs.BoolVar(&f.SkipCommandCheck, "skip-command-check", false, "(test) skip the storage-check command preflight")

	if err := fs.Parse(argv[1:]); err != nil {
		return 2
	}
	if f.ShowVersion {
		_, _ = fmt.Fprintln(stdout, cli.Version())
		return 0
	}
	// storage-check's probes are already read-only (BR-7: fsck -n variants,
	// lsblk/blkid reads) -- there is nothing for --dry-run to protect. What
	// --dry-run WOULD do is stub every probe to rc 0, making Classify
	// fabricate a verdict from evidence it never gathered: a false health
	// report an operator may act on (FTA v2 BE-F7, TE-D). Fail closed: refuse
	// the combination instead of emitting fiction. The pipeline never passes
	// --dry-run to this tool.
	if f.DryRun {
		errln(stderr, "--dry-run is not supported: storage-check probes are already read-only, "+
			"and stubbing them would fabricate health verdicts; run without --dry-run")
		return 1
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
		errln(stderr, "install-context: %v", err)
		return 1
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
		errln(stderr, "%v", err)
		return 1
	}

	logger.SetStage("command_checks")
	fsckVfat := "fsck.vfat"
	if !f.SkipCommandCheck {
		if err := sysenv.RequireCommands(sysenv.StorageCheckCommands...); err != nil {
			errln(stderr, "%v", err)
			return 1
		}
		chosen, err := sysenv.RequireCommandsWithFallback("fsck.vfat", "fsck.fat")
		if err != nil {
			errln(stderr, "%v", err)
			return 1
		}
		fsckVfat = chosen
	}

	if f.ConfigPath == "" {
		errln(stderr, "--config is required")
		return 1
	}
	logger.SetStage("config_validation")
	cfg, raw, err := v2.LoadFile(f.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			errln(stderr, "config not found: %s", f.ConfigPath)
			return 1
		}
		errln(stderr, "%v", err)
		return 1
	}
	if err := cfg.ValidateRaw(raw); err != nil {
		errln(stderr, "%v", err)
		return 1
	}
	logger.KV("config_path", f.ConfigPath)
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
	for i := range cfg.Sets {
		set := &cfg.Sets[i]
		storageType := set.StorageType()
		logger.UserInfo("Checking set %q (storage_type=%s)", set.Name, storageType)

		if storageType == "memory" {
			continue
		}

		resolved, err := resolver.ResolveAll(context.Background(), set.EffectiveDevices())
		if err != nil {
			failures = append(failures, storagehealth.Unhealthy{
				SetName: set.Name, VolumeName: "(devices)",
				Reason: fmt.Sprintf("device resolution failed: %v", err),
			})
			logger.KV("verdict", set.Name+"=broken")
			continue
		}
		verdict, fs := storagehealth.Classify(context.Background(), runner, checker, set, resolved, logger)
		logger.KV("verdict", set.Name+"="+string(verdict))
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
		return 2
	}

	logger.SetStage("mount")
	logger.UserInfo("All sets healthy — mounting storage tree")
	predicted := fsmod.PredictFilesystemsFromConfig(cfg, func(set *v2.Set) bool {
		devs := set.EffectiveDevices()
		if set.Role != "os" || len(devs) == 0 {
			return false
		}
		r, err := resolver.Resolve(context.Background(), devs[0])
		if err != nil {
			return false
		}
		return storagehealth.HasBIOSBootPartition(context.Background(), runner, r.ResolvedPath)
	})
	mt, err := mounter.MountSysrootTree(context.Background(), predicted)
	if err != nil {
		errln(stderr, "mount: %v", err)
		return 1
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
