// lpart-bootloader-finalise makes a transferred OS under /sysroot bootable.
// Behavioural reference: /root/NEWTREE/bin/lpart-bootloader-finalise.
//
// Flow:
//  1. Parse flags, init logger, installer-env guard, command preflight
//  2. Load + validate storage-result.json (extract the OS set when the
//     file contains the multi-set wrapper)
//  3. Build BootContext from the storage result + detected firmware /
//     OS family
//  4. Write /sysroot/etc/fstab from the filesystem list
//  5. If clear_uefi_nvram=true on UEFI: bind-mount + ClearUEFINVRAMEntries
//  6. If boot_method=="none": stop (fstab-only path), after removing any
//     existing bootloader of a disk OS -- TrinityX NVRAM entries + ESP loaders
//     on UEFI, MBR boot code + BIOS boot partition on BIOS; past that
//     point a non-UEFI firmware proceeds only for an OS set that asked for a
//     bootloader, and only with a BIOS boot partition to install into; else:
//     bind mounts + family.Prepare → RegenerateInitramfs → Install → Validate
//  7. Always cleanup bind mounts before returning
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/bootloader"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

const toolName = "lpart-bootloader-finalise"

func main() {
	cli.EnsureInstallerPath()
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func errln(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, toolName+": "+format+"\n", args...)
}

type flags struct {
	cli.Common
	ResultPath         string
	BootContextOut     string
	Details            bool
	InstallContextPath string
	// test affordances
	CmdlinePath      string
	SkipCommandCheck bool
	ArchOverride     string
}

const (
	defaultRuntimeDir = "/tmp/install_files/runtime"
	defaultResultPath = defaultRuntimeDir + "/storage-result.json"
	defaultContextOut = defaultRuntimeDir + "/boot-context.json"
)

//nolint:gocyclo // command flow is linear and matches the bash predecessor.
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		argv = []string{toolName}
	}
	var f flags
	fs := cli.NewToolFlagSet(toolName)
	f.Bind(fs)
	fs.StringVar(&f.ResultPath, "result", defaultResultPath, "Path to storage-result.json")
	fs.StringVar(&f.BootContextOut, "boot-context-out", defaultContextOut, "Where to write boot-context.json")
	fs.BoolVar(&f.Details, "details", true, "Verbose: print per-step diagnostics + the verification evidence (default on; --details=false to quieten)")
	fs.StringVar(&f.InstallContextPath, "install-context", installcontext.DefaultJSONPath,
		"Path to install-context.json (mode/osimage-sync flags; informational)")
	fs.StringVar(&f.CmdlinePath, "cmdline-path", "", "(test) override /proc/cmdline path")
	fs.BoolVar(&f.SkipCommandCheck, "skip-command-check", false, "(test) skip the storage-prepare command preflight")
	fs.StringVar(&f.ArchOverride, "arch", "", "(test) override detected architecture")

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
	logger.StageBanner("Starting lpart-bootloader-finalise")

	ic, err := installcontext.LoadFile(f.InstallContextPath)
	if err != nil {
		errln(stderr, "install-context: %v", err)
		return 1
	}
	logger.KV("install_mode", string(ic.InstallMode))

	logger.SetStage("installer_check")
	if err := sysenv.RequireInstallerEnv(sysenv.Options{
		CmdlinePath:       f.CmdlinePath,
		AllowNonInstaller: f.AllowNonInstaller,
	}); err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	logger.SetStage("command_checks")
	if !f.SkipCommandCheck {
		if err := sysenv.RequireCommands(sysenv.BootloaderCommonCommands...); err != nil {
			errln(stderr, "%v", err)
			return 1
		}
	}

	logger.SetStage("result_extraction")
	sr, err := loadStorageResult(f.ResultPath)
	if err != nil {
		errln(stderr, "%v", err)
		return 1
	}
	if err := validateStorageResult(sr); err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	arch := f.ArchOverride
	if arch == "" {
		arch = runtimeArch()
	}

	logger.SetStage("build_context")
	bctx, err := bootloader.BuildBootContext(sr, arch, "")
	if err != nil {
		errln(stderr, "%v", err)
		return 1
	}
	if err := writeBootContext(f.BootContextOut, bctx); err != nil {
		errln(stderr, "write boot-context: %v", err)
		return 1
	}
	logger.KV("boot_method", bctx.BootMethod)
	logger.KV("bootloader_family", string(bctx.Family))
	logger.KV("firmware_mode", bctx.FirmwareMode)
	logger.KV("clear_uefi_nvram", bctx.ClearUEFINVRAM)
	logger.UserInfo("Boot method: %s", bctx.BootMethod)
	logger.UserInfo("Bootloader family: %s", bctx.Family)
	logger.UserInfo("Sysroot: %s", bctx.Sysroot)

	nativeFS := fsops.FS{DryRun: f.DryRun, Logger: logger}

	logger.SetStage("write_fstab")
	if err := bootloader.WriteFstab(nativeFS, bctx.Sysroot, bctx.StorageResult); err != nil {
		errln(stderr, "write fstab: %v", err)
		return 1
	}

	runner := executor.New(executor.Options{DryRun: f.DryRun, Logger: logger})

	cleanup := func() {
		bootloader.CleanupBindMounts(context.Background(), runner, bctx.Sysroot, nil)
	}
	defer cleanup()

	if bctx.ClearUEFINVRAM {
		// There is no NVRAM to clear outside UEFI, so skip the bind mount too
		// rather than mounting for a no-op.
		if bctx.FirmwareMode != "uefi" {
			logger.UserInfo("clear_uefi_nvram requested but firmware is %q: no UEFI NVRAM to clear", bctx.FirmwareMode)
		} else {
			logger.SetStage("bind_mounts")
			if err := bootloader.BindMountSysroot(context.Background(), runner, bctx.Sysroot, nil); err != nil {
				errln(stderr, "bind mount: %v", err)
				return 1
			}
			logger.SetStage("clear_uefi_nvram")
			if err := bootloader.ClearUEFINVRAMEntries(context.Background(), runner, bctx.Sysroot, bctx.FirmwareMode); err != nil {
				errln(stderr, "%v", err)
				return 1
			}
		}
	}

	if bctx.BootMethod == "none" {
		// Three ways to get here, and the operator has to be told which: a RAM
		// root with no disk, a firmware with no UEFI boot path, or the OS set
		// asking for none. Only the last leaves a previous install behind to
		// remove.
		switch {
		case bctx.StorageResult.StorageType == "memory":
			logger.UserInfo("No bootloader installed (memory root): fstab written; the node PXE-boots every time")
		case bctx.FirmwareMode != "uefi":
			// Only "first" installs GRUB for BIOS. An earlier "first" left
			// boot.img in each boot disk's MBR and core.img in the BIOS boot
			// partition; a BIOS that tries the disk would boot that stale
			// install, so take it away.
			logger.UserInfo(`No bootloader installed (firmware is %q and the OS set's bootloader is not "first", the only value that installs GRUB for BIOS): fstab written; the node PXE-boots every time`, bctx.FirmwareMode)
			logger.SetStage("remove_bootloader")
			if err := bootloader.RemoveBIOSBootloader(context.Background(), bootloader.RemoveBIOSBootloaderOptions{
				Runner: runner, Logger: logger, FS: nativeFS,
				BootDevices: bctx.StorageResult.BootDevices,
			}); err != nil {
				errln(stderr, "%v", err)
				return 1
			}
		default:
			logger.UserInfo(`No bootloader installed (the OS set asks for bootloader "none"): fstab written; the node PXE-boots every time`)
			logger.SetStage("bind_mounts")
			if err := bootloader.BindMountSysroot(context.Background(), runner, bctx.Sysroot, nil); err != nil {
				errln(stderr, "bind mount: %v", err)
				return 1
			}
			logger.SetStage("remove_bootloader")
			if err := bootloader.RemoveExistingBootloader(context.Background(), bootloader.RemoveExistingBootloaderOptions{
				Runner: runner, Logger: logger, FS: nativeFS,
				Sysroot: bctx.Sysroot, Arch: bctx.Architecture,
			}); err != nil {
				errln(stderr, "%v", err)
				return 1
			}
		}
		return 0
	}

	// Belt behind lpart-storage-prepare's boot preflight: outside UEFI, GRUB is
	// installed only for the os set that asked for it, so any other firmware
	// here is an upstream bug. Refuse before the bind mounts and dracut run for
	// nothing, with exit 2 so the wrapper parks instead of retrying a permanent
	// condition.
	if bctx.FirmwareMode != "uefi" {
		if !bctx.BIOSGRUBRequested() {
			return cli.ExitSafetyRefusal(stderr, toolName, "firmware-not-uefi",
				"boot_method=%s needs UEFI firmware but the node booted %q; lpart-storage-prepare should have refused this -- not running dracut/GRUB",
				bctx.BootMethod, bctx.FirmwareMode)
		}
		// GRUB for BIOS keeps its core image in a BIOS boot partition and
		// refuses to install without one. Only a layout rebuild creates it, so
		// say which install mode does that rather than failing mid-install. A
		// result that did not record the number (mount-existing) is checked
		// against the disks themselves.
		if bctx.StorageResult.BIOSBootPartition == 0 && !allHaveBIOSBootPartition(runner, bctx.StorageResult.BootDevices) {
			return cli.ExitSafetyRefusal(stderr, toolName, "missing-bios-boot-partition",
				"GRUB for BIOS needs a BIOS boot partition for its core image and this node's layout has none; reinstall the node with install_mode full so the partition is created -- not running dracut/GRUB")
		}
	}

	logger.SetStage("bind_mounts")
	if err := bootloader.BindMountSysroot(context.Background(), runner, bctx.Sysroot, nil); err != nil {
		errln(stderr, "bind mount: %v", err)
		return 1
	}

	adapter, err := bootloader.New(bootloader.AdapterDeps{
		Runner:      runner,
		Logger:      logger,
		FS:          nativeFS,
		BootContext: bctx,
		SnapshotDir: defaultRuntimeDir,
	})
	if err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	for _, step := range []struct {
		name string
		fn   func(context.Context) error
	}{
		{"bootloader_prepare", adapter.Prepare},
		{"initramfs", adapter.RegenerateInitramfs},
		{"bootloader_install", adapter.Install},
		{"bootloader_validate", adapter.Validate},
	} {
		logger.SetStage(step.name)
		if err := step.fn(context.Background()); err != nil {
			errln(stderr, "%v", err)
			return 1
		}
	}

	// Final proof: re-read NVRAM + ESP + grub and SHOW the evidence, so a
	// finalise on a new distro/arch ends by proving what actually landed (not a
	// silent "ok"). Runs every time; --details adds further diagnostics.
	logger.SetStage("bootloader_verify")
	if err := bootloader.VerifyAndReport(context.Background(), runner, logger, bctx); err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	// Checkpoint the target /boot so a NON-JOURNAL-REPLAYING reader (GRUB) can
	// see grub.cfg. This was THE rocky9 raid1 degraded-boot regression, and it
	// is NOT a durability/sync problem (a full sync — even cache=directsync —
	// does not fix it): grub2-mkconfig writes grub.cfg to the /boot xfs, but the
	// new directory entry lives in the xfs LOG and is not yet checkpointed to the
	// on-disk metadata tree. GRUB's xfs driver is read-only and never replays the
	// log, so when it reads the surviving raid1 member degraded it does not find
	// grub.cfg and drops to `grub>` ("file /grub2/grub.cfg not found"). The finalise
	// only ever unmounts the bind mounts (/dev,/proc,/sys,/run — CleanupBindMounts),
	// never /sysroot/boot, so nothing forces the checkpoint before the node reboots.
	// A CLEAN UNMOUNT of /sysroot/boot forces xfs to check point the log into the
	// on-disk tree (proven: after a clean unmount, a `mount -o norecovery` — grub's
	// exact view — shows grub.cfg; before it, it does not). `umount -R` takes the
	// vfat /boot/efi child down with it. Nothing after this phase writes /boot
	// (postscript touches /etc on the root fs), so leaving it unmounted until the
	// reboot is safe. Best-effort with a lazy fallback: a stuck unmount must not
	// fail the install, and a sync still runs as a backstop.
	syscall.Sync()
	bootMnt := bctx.Sysroot + "/boot"
	if _, err := runner.Run(context.Background(), executor.CommandSpec{
		Path: "umount", Args: []string{"-R", bootMnt}, CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		_, _ = runner.Run(context.Background(), executor.CommandSpec{
			Path: "umount", Args: []string{"-l", bootMnt}, CaptureStdout: true, CaptureStderr: true,
		})
	}
	syscall.Sync()

	logger.UserInfo("Bootloader finalisation completed successfully")
	return 0
}

// allHaveBIOSBootPartition reports whether every boot disk carries a BIOS boot
// (EF02) partition; grub2-install needs one on each disk it installs to.
func allHaveBIOSBootPartition(runner executor.Runner, disks []string) bool {
	if len(disks) == 0 {
		return false
	}
	for _, d := range disks {
		if !bootloader.HasBIOSBootPartition(context.Background(), runner, d) {
			return false
		}
	}
	return true
}

// loadStorageResult decodes a storage-result.json file. The bash
// predecessor handles two shapes:
//
//	{"version":2,"name":"os",...}                   single-set
//	{"version":2,"sets":[{"role":"os",...},...]}    multi-set
//
// For the multi-set form we extract the OS set and merge any non-OS
// filesystems into its filesystems[] so fstab covers data mounts too.
// Mirrors extract_os_result_if_needed.
func loadStorageResult(path string) (result.StorageResult, error) {
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result.StorageResult{}, fmt.Errorf("storage-result.json not found: %s", path)
		}
		return result.StorageResult{}, fmt.Errorf("read %s: %w", path, err)
	}

	// Try multi-set first: when the JSON has a top-level "sets" array,
	// we extract the OS-role member and merge data filesystems.
	var multi result.MultiSet
	if err := json.Unmarshal(body, &multi); err == nil && len(multi.Sets) > 0 {
		var osSet *result.StorageResult
		var dataFS []result.Filesystem
		for i := range multi.Sets {
			s := &multi.Sets[i]
			if s.Role == "os" {
				osSet = s
				continue
			}
			dataFS = append(dataFS, s.Filesystems...)
		}
		if osSet == nil {
			return result.StorageResult{}, fmt.Errorf("expected exactly one OS role set in storage-result sets[]")
		}
		osSet.Filesystems = append(osSet.Filesystems, dataFS...)
		return *osSet, nil
	}

	// Single-set shape.
	var single result.StorageResult
	if err := json.Unmarshal(body, &single); err != nil {
		return result.StorageResult{}, fmt.Errorf("decode storage-result: %w", err)
	}
	return single, nil
}

// validateStorageResult mirrors validate_storage_result: sysroot
// non-empty + absolute + directory; etc/os-release present when
// boot_method != none; filesystems and boot_devices are populated.
func validateStorageResult(sr result.StorageResult) error {
	if sr.MountedTargetRoot == "" {
		return fmt.Errorf("storage-result missing mounted_target_root")
	}
	if sr.MountedTargetRoot[0] != '/' {
		return fmt.Errorf("storage-result mounted_target_root must be an absolute path: %s", sr.MountedTargetRoot)
	}
	if info, err := os.Stat(sr.MountedTargetRoot); err != nil || !info.IsDir() {
		return fmt.Errorf("missing sysroot directory: %s", sr.MountedTargetRoot)
	}
	bm := sr.BootMethod
	if bm == "" {
		bm = "grub"
	}
	if bm != "none" {
		if _, err := os.Stat(sr.MountedTargetRoot + "/etc/os-release"); err != nil {
			return fmt.Errorf("missing %s/etc/os-release", sr.MountedTargetRoot)
		}
	}
	if sr.Filesystems == nil {
		return fmt.Errorf("storage-result filesystems must be an array")
	}
	if sr.BootDevices == nil && bm != "none" {
		return fmt.Errorf("storage-result boot_devices must be an array")
	}
	return nil
}

func writeBootContext(path string, ctx bootloader.BootContext) error {
	if err := os.MkdirAll(parentDir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	body, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	//nolint:gosec // operator-supplied output path
	return os.WriteFile(path, append(body, '\n'), 0o640)
}

func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// runtimeArch returns the architecture string `uname -m` would produce.
// The Go runtime exposes GOARCH (amd64, arm64); we map to the bash
// strings (x86_64, aarch64) so downstream lookups in BootContext work.
func runtimeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}
