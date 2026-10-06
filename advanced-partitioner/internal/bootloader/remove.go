package bootloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// RemoveExistingBootloaderOptions configures RemoveExistingBootloader.
type RemoveExistingBootloaderOptions struct {
	Runner  executor.Runner
	Logger  *txlog.Logger
	FS      fsops.FS
	Sysroot string
	Arch    string
}

// RemoveExistingBootloader takes the local boot path away from a disk OS whose
// set now asks for bootloader "none": the TrinityX NVRAM entries and the
// loaders StageSignedShim put on the ESP. Without it a node switched from grub
// to none keeps a firmware-reachable install, and a PXE failure boots that old
// OS instead of whatever the controller would have served.
//
// The osimage extraction re-creates EFI/BOOT and EFI/<distro> from the image on
// every sync, so this runs on every none finalise by design.
//
// UEFI disk roots only: on BIOS there was never a loader firmware could reach,
// and a memory root has no disk to clean.
func RemoveExistingBootloader(ctx context.Context, opts RemoveExistingBootloaderOptions) error {
	entries := removeTrinityNVRAMEntries(ctx, opts)
	loaders, err := removeStagedLoaders(opts)
	if err != nil {
		return err
	}
	if opts.Logger == nil {
		return nil
	}
	if entries == 0 && loaders == 0 {
		opts.Logger.UserInfo("no existing bootloader to remove")
		return nil
	}
	opts.Logger.UserInfo("existing bootloader removed (NVRAM entries: %d, loaders: %d)", entries, loaders)
	return nil
}

// removeTrinityNVRAMEntries deletes the TrinityX boot entries and returns how
// many went. Only ours: the network entries are how the controller keeps
// control, and another OS's boot manager is not ours to delete. Mirrors
// RegisterUEFIEntries' skip ladder, including its loud warning, because the
// same two conditions make an efibootmgr call impossible.
func removeTrinityNVRAMEntries(ctx context.Context, opts RemoveExistingBootloaderOptions) int {
	if _, err := os.Stat(filepath.Join(opts.Sysroot, "sys", "firmware", "efi", "efivars")); err != nil {
		if opts.Logger != nil {
			opts.Logger.Warn("UEFI NVRAM cleanup SKIPPED: efivars not exposed under the sysroot -- " +
				"any stale TrinityX boot entry stays in NVRAM; the ESP loaders it points at are still removed")
		}
		return 0
	}
	eb := efibootmgrChrootPath(opts.Sysroot)
	if eb == "" {
		if opts.Logger != nil {
			opts.Logger.Warn("UEFI NVRAM cleanup SKIPPED: no efibootmgr in image -- " +
				"any stale TrinityX boot entry stays in NVRAM; the ESP loaders it points at are still removed")
		}
		return 0
	}
	current, err := runEFIBootmgr(ctx, opts.Runner, opts.Sysroot)
	if err != nil {
		if opts.Logger != nil {
			opts.Logger.Warn("UEFI NVRAM cleanup SKIPPED: " + err.Error())
		}
		return 0
	}
	removed := 0
	for _, num := range trinityNumsFromOutput(current) {
		if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
			Path: eb, Chroot: opts.Sysroot,
			Args:          []string{"--bootnum", num, "--delete-bootnum"},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			// Not fatal: the loaders the entry points at are removed below, so
			// a surviving entry has nothing left to boot.
			if opts.Logger != nil {
				opts.Logger.Warn(fmt.Sprintf("could not delete UEFI boot entry Boot%s: %v", num, err))
			}
			continue
		}
		if opts.Logger != nil {
			opts.Logger.UserInfo("removed UEFI boot entry Boot%s", num)
		}
		removed++
	}
	return removed
}

// removeStagedLoaders deletes the EFI binaries StageSignedShim wrote and
// returns how many went. The names come from the same arch tables the staging
// uses, so the two cannot disagree about which files are ours. Everything else
// on the ESP — the stub grub.cfg, the CSV, grubenv, fonts, the directories
// themselves — is left alone.
func removeStagedLoaders(opts RemoveExistingBootloaderOptions) (int, error) {
	shimName, grubName, err := archShimAndGrub(opts.Arch)
	if err != nil {
		return 0, err
	}
	fallbackName, err := fallbackLoaderBaseName(opts.Arch)
	if err != nil {
		return 0, err
	}
	efiRoot := filepath.Join(opts.Sysroot, "boot", "efi", "EFI")
	dirs, err := os.ReadDir(efiRoot)
	if err != nil {
		if opts.Logger != nil {
			opts.Logger.UserInfo("no ESP loaders to remove: %v", err)
		}
		return 0, nil
	}
	removed := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		names := []string{shimName, grubName, "grub.efi"}
		if strings.EqualFold(d.Name(), "BOOT") {
			names = append(names, fallbackName)
		}
		for _, n := range names {
			p := filepath.Join(efiRoot, d.Name(), n)
			if _, err := os.Stat(p); err != nil {
				continue
			}
			if err := opts.FS.Remove(p); err != nil {
				return removed, fmt.Errorf("remove ESP loader %s: %w", p, err)
			}
			if opts.Logger != nil {
				opts.Logger.UserInfo("removed ESP loader %s", p)
			}
			removed++
		}
	}
	return removed, nil
}
