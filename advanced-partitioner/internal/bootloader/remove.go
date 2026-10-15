package bootloader

import (
	"context"
	"fmt"
	"io"
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
// UEFI disk roots only; a BIOS node goes through RemoveBIOSBootloader (an
// earlier "first" install left GRUB in the MBR and the BIOS boot partition),
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

// RemoveBIOSBootloaderOptions configures RemoveBIOSBootloader.
type RemoveBIOSBootloaderOptions struct {
	Runner      executor.Runner
	Logger      *txlog.Logger
	FS          fsops.FS // only DryRun is used: the writes are raw block I/O
	BootDevices []string
}

// mbrBootCodeLen is the boot-code area of the MBR (and of GPT's protective
// MBR). The disk signature (440-445) and the partition table (446-509) come
// after it and are never touched.
const mbrBootCodeLen = 440

// biosBootWipeMax caps how much of a BIOS boot partition is zeroed. GRUB's
// core.img is well under 1 MiB; the partition is 1 MiB in lpart's layout.
const biosBootWipeMax = 8 << 20

// RemoveBIOSBootloader takes the local boot path away from a BIOS node whose
// OS set asks for bootloader "none". An earlier "first" install left GRUB's
// boot.img in the MBR of every boot disk and its core.img in the BIOS boot
// (EF02) partition; with those in place, a BIOS that tries the disk before (or
// after a failed) PXE boots the stale install instead of asking the
// controller. So: zero the MBR boot code (the first 440 bytes; the partition
// table is never touched) and the EF02 partition contents -- only when both are
// GRUB's; any other boot code is warned about and left. Idempotent (already-zero
// areas are not rewritten), dry-run aware, and it refuses anything that is not
// a whole disk.
func RemoveBIOSBootloader(ctx context.Context, opts RemoveBIOSBootloaderOptions) error {
	cleared, wiped := 0, 0
	for _, disk := range opts.BootDevices {
		parts, err := biosBootPartitions(ctx, opts.Runner, disk)
		if err != nil {
			if opts.Logger != nil {
				opts.Logger.Warn(fmt.Sprintf("cannot list partitions of %s (%v): its boot code is left as it is", disk, err))
			}
			continue
		}
		why, err := notGRUBInstall(disk, parts)
		if err != nil {
			return err
		}
		if why != "" {
			if opts.Logger != nil {
				opts.Logger.Warn(fmt.Sprintf("%s: %s; its boot code is left as it is", disk, why))
			}
			continue
		}
		did, err := clearMBRBootCode(opts, disk)
		if err != nil {
			return err
		}
		if did {
			cleared++
		}
		for _, p := range parts {
			did, err := wipeBIOSBootPartition(opts, p.Name)
			if err != nil {
				return err
			}
			if did {
				wiped++
			}
		}
	}
	if opts.Logger != nil {
		if cleared == 0 && wiped == 0 {
			opts.Logger.UserInfo("no BIOS boot code to remove")
		} else {
			opts.Logger.UserInfo("BIOS boot code removed (MBR boot code cleared on %d disk(s), %d BIOS boot partition(s) wiped)", cleared, wiped)
		}
	}
	return nil
}

// sysClassBlock is where the kernel says whether a block device is a
// partition; a package variable so tests can point it elsewhere.
var sysClassBlock = "/sys/class/block"

// openRawTarget opens a whole disk or BIOS boot partition for raw I/O. Block
// devices and regular files (disk images, tests) only; wantWholeDisk refuses
// partitions, md and dm devices, whose first sector is filesystem data.
func openRawTarget(path string, wantWholeDisk, write bool) (*os.File, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	isBlock := fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice == 0
	if !isBlock && !fi.Mode().IsRegular() {
		return nil, configErrorf("not-a-disk", "%s is neither a block device nor a disk image", path)
	}
	if isBlock && wantWholeDisk {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		name := filepath.Base(real)
		if _, err := os.Stat(filepath.Join(sysClassBlock, name, "partition")); err == nil ||
			strings.HasPrefix(name, "md") || strings.HasPrefix(name, "dm-") {
			return nil, configErrorf("not-a-disk", "boot device %s (%s) is not a whole disk: refusing to clear its first sector", path, name)
		}
	}
	flag := os.O_RDONLY
	if write {
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(path, flag, 0) //nolint:gosec // boot device from the storage result
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// clearMBRBootCode zeroes the MBR boot code of disk and reports whether there
// was any. A first sector without the 0x55AA signature is no MBR -- BIOS will
// not boot it either -- and is left alone.
func clearMBRBootCode(opts RemoveBIOSBootloaderOptions, disk string) (bool, error) {
	f, err := openRawTarget(disk, true, !opts.FS.DryRun)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	sector := make([]byte, 512)
	if n, err := f.ReadAt(sector, 0); n < len(sector) {
		if err == nil || err == io.EOF {
			return false, nil // smaller than a sector: nothing to clear
		}
		return false, fmt.Errorf("read the MBR of %s: %w", disk, err)
	}
	if sector[510] != 0x55 || sector[511] != 0xAA {
		return false, nil
	}
	if allZero(sector[:mbrBootCodeLen]) {
		return false, nil
	}
	if opts.FS.DryRun {
		if opts.Logger != nil {
			opts.Logger.Info("(dry-run) would clear the MBR boot code of " + disk)
		}
		return true, nil
	}
	if _, err := f.WriteAt(make([]byte, mbrBootCodeLen), 0); err != nil {
		return false, fmt.Errorf("clear the MBR boot code of %s: %w", disk, err)
	}
	if err := f.Sync(); err != nil {
		return false, fmt.Errorf("sync %s: %w", disk, err)
	}
	if opts.Logger != nil {
		opts.Logger.UserInfo("cleared the MBR boot code of %s (partition table untouched)", disk)
	}
	return true, nil
}

// wipeBIOSBootPartition zeroes a BIOS boot partition (GRUB's core.img) and
// reports whether it held anything.
func wipeBIOSBootPartition(opts RemoveBIOSBootloaderOptions, part string) (bool, error) {
	f, err := openRawTarget(part, false, !opts.FS.DryRun)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return false, fmt.Errorf("size of %s: %w", part, err)
	}
	if size > biosBootWipeMax {
		size = biosBootWipeMax
	}
	buf := make([]byte, size)
	if n, err := f.ReadAt(buf, 0); int64(n) < size && err != nil && err != io.EOF {
		return false, fmt.Errorf("read %s: %w", part, err)
	}
	if allZero(buf) {
		return false, nil
	}
	if opts.FS.DryRun {
		if opts.Logger != nil {
			opts.Logger.Info("(dry-run) would wipe the BIOS boot partition " + part)
		}
		return true, nil
	}
	if _, err := f.WriteAt(make([]byte, size), 0); err != nil {
		return false, fmt.Errorf("wipe the BIOS boot partition %s: %w", part, err)
	}
	if err := f.Sync(); err != nil {
		return false, fmt.Errorf("sync %s: %w", part, err)
	}
	if opts.Logger != nil {
		opts.Logger.UserInfo("wiped the BIOS boot partition %s (GRUB core image)", part)
	}
	return true, nil
}

// GRUB's i386-pc boot.img (the MBR) and diskboot.img (the first sector of
// core.img) carry these strings; their offsets differ between GRUB builds.
const (
	grubBootImgMark = "GRUB \x00Geom"
	grubCoreImgMark = "loading\x00"
)

// notGRUBInstall says why the boot code on disk is not a GRUB install to remove,
// or "" when it is: boot.img in the MBR and core.img at the start of every BIOS
// boot partition. An MBR without boot code, or no MBR at all, is "" too: there
// is nothing to remove and clearMBRBootCode leaves it alone.
func notGRUBInstall(disk string, parts []blockDev) (string, error) {
	mbr, err := readFirstSector(disk, true)
	if err != nil || mbr == nil || mbr[510] != 0x55 || mbr[511] != 0xAA || allZero(mbr[:mbrBootCodeLen]) {
		return "", err
	}
	if !strings.Contains(string(mbr[:mbrBootCodeLen]), grubBootImgMark) {
		return "the MBR boot code is not GRUB's", nil
	}
	if len(parts) == 0 {
		return "GRUB is in the MBR but there is no BIOS boot partition with its core image", nil
	}
	for _, p := range parts {
		s, err := readFirstSector(p.Name, false)
		if err != nil {
			return "", err
		}
		if s == nil || !strings.Contains(string(s), grubCoreImgMark) {
			return fmt.Sprintf("the BIOS boot partition %s does not start with GRUB's core image", p.Name), nil
		}
	}
	return "", nil
}

// readFirstSector returns the first 512 bytes of path, or nil when it is shorter.
func readFirstSector(path string, wholeDisk bool) ([]byte, error) {
	f, err := openRawTarget(path, wholeDisk, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sector := make([]byte, 512)
	if n, err := f.ReadAt(sector, 0); n < len(sector) {
		if err == nil || err == io.EOF {
			return nil, nil
		}
		return nil, fmt.Errorf("read the first sector of %s: %w", path, err)
	}
	return sector, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
