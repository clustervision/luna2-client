package bootloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// debianAdapter ports lib/bootloader_debian.sh for Debian/Ubuntu.
// The flow mirrors the RHEL adapter — same `StageSignedShim` for
// UEFI, family-specific commands everywhere else:
//
//	Prepare:    require update-initramfs + update-grub; require
//	            grub-install only when BIOS (UEFI uses the signed
//	            shim+grub from /EFI/<distro>/ instead).
//	Initramfs:  update-initramfs -u -k all
//	Install:    write /etc/default/grub (Debian-flavoured keys);
//	            BIOS path runs grub-install --target=i386-pc per disk
//	            then update-grub; UEFI path calls StageSignedShim
//	            (with GrubDirName="grub"), then RegisterUEFIEntries,
//	            then update-grub; normalises grub.cfg when /boot is
//	            a separate filesystem.
//	Validate:   check grub.cfg present, fstab non-empty, EFI dir
//	            if UEFI.
type debianAdapter struct{ deps AdapterDeps }

func (a *debianAdapter) Family() Family { return FamilyDebian }

func (a *debianAdapter) Prepare(_ context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	// Initramfs generator: update-initramfs (initramfs-tools) OR dracut. Which
	// one an Ubuntu image carries depends on the release: 26.04/resolute keeps
	// initramfs-tools-core alongside dracut, but 24.04/noble's `dracut` package
	// Conflicts initramfs-tools and removes it (dracut installed so luna can
	// pack the 95luna netboot initramfs). Accept either — RegenerateInitramfs
	// dispatches on whichever is present.
	if !a.hasUpdateInitramfs() && !a.hasDracut() {
		return imageErrorf("image-missing-tool", "missing required command: update-initramfs or dracut")
	}
	if !anyExist(sysroot, "usr/sbin/update-grub", "usr/bin/update-grub") {
		return imageErrorf("image-missing-tool", "missing required command: update-grub")
	}
	if len(a.deps.BootContext.StorageResult.BootDevices) == 0 {
		return configErrorf("no-boot-devices", "storage-result boot_devices must contain at least one disk")
	}
	if a.deps.BootContext.FirmwareMode == "uefi" {
		if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi")); err != nil {
			return configErrorf("missing-esp-mount", "missing EFI mount directory: %s/boot/efi", sysroot)
		}
	}
	return requireBIOSGRUB(a.deps.BootContext, "grub-install", "grub2-common", "grub-pc-bin", "usr/lib/grub/i386-pc")
}

func (a *debianAdapter) hasUpdateInitramfs() bool {
	return anyExist(a.deps.BootContext.Sysroot, "usr/sbin/update-initramfs", "usr/bin/update-initramfs")
}

func (a *debianAdapter) hasDracut() bool {
	return anyExist(a.deps.BootContext.Sysroot, "usr/bin/dracut", "usr/sbin/dracut")
}

// RegenerateInitramfs regenerates the INSTALLED system's initramfs with
// whichever generator the image ships: `update-initramfs -u -k all`
// (initramfs-tools, Debian-native) when present, else `dracut -f --omit luna
// --kver <kver>` for every bootable kernel — identical to the RHEL/SUSE path —
// for dracut-only images
// (e.g. Ubuntu 24.04, whose dracut package displaced initramfs-tools). Omit
// the luna module: it is only for the netboot install initramfs, never the
// installed system's.
func (a *debianAdapter) RegenerateInitramfs(ctx context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	if err := removeImageLVMDevicesFile(a.deps); err != nil {
		return err
	}
	if a.hasUpdateInitramfs() {
		if _, err := a.deps.Runner.Run(ctx, executor.CommandSpec{
			Path: "/usr/bin/env", Chroot: sysroot,
			Args:          []string{"update-initramfs", "-u", "-k", "all"},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			return fmt.Errorf("update-initramfs: %w", err)
		}
		return nil
	}
	// dracut-only image: every bootable kernel, as on RHEL. GRUB_DEFAULT=0 is
	// update-grub's newest entry, so the default is the newest kernel here.
	_, err := dracutAllKernels(ctx, a.deps, false)
	return err
}

// Install writes /etc/default/grub, then runs the UEFI install -- or the BIOS
// one when the os set asked for a bootloader on non-uefi firmware. Any other
// non-UEFI firmware is refused (see the boot preflight in
// lpart-storage-prepare) rather than given a degraded install.
func (a *debianAdapter) Install(ctx context.Context) error {
	if err := a.writeGrubDefault(); err != nil {
		return err
	}
	if a.deps.BootContext.FirmwareMode != "uefi" {
		if !a.deps.BootContext.BIOSGRUBRequested() {
			return configErrorf("firmware-not-uefi", "unsupported firmware mode %q: lpart requires UEFI (BIOS/legacy boot is not supported)", a.deps.BootContext.FirmwareMode)
		}
		return a.installBIOS(ctx)
	}
	return a.installUEFI(ctx)
}

// installBIOS writes the GRUB core image into every boot disk's BIOS boot
// partition, so a mirrored set boots from either member. There is no shim for
// firmware to verify and no NVRAM to register an entry in outside UEFI, so the
// install is grub-install plus the same configuration generation.
//
// i386-pc names the legacy BIOS platform, not the word size: GRUB has no
// 64-bit BIOS target, and the kernel this loads is still the 64-bit one.
func (a *debianAdapter) installBIOS(ctx context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	for _, disk := range a.deps.BootContext.StorageResult.BootDevices {
		if _, err := a.deps.Runner.Run(ctx, executor.CommandSpec{
			Path: "/usr/bin/env", Chroot: sysroot,
			Args: []string{"grub-install", "--target=i386-pc",
				"--boot-directory=/boot", disk},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			return fmt.Errorf("grub-install %s: %w", disk, err)
		}
	}
	return a.generateGrubConfig(ctx)
}

// writeGrubDefault merges lpart's keys and root-storage kernel arguments into
// the image's /etc/default/grub (see grubdefault.go). The Debian keys differ
// from RHEL's: GRUB_DEFAULT=0 (numeric, not "saved"), no GRUB_DISABLE_SUBMENU.
func (a *debianAdapter) writeGrubDefault() error {
	return writeMergedGrubDefault(a.deps.FS, a.deps.BootContext.Sysroot,
		debianGrubDefaultKeys, a.deps.BootContext.KernelArgs)
}

func (a *debianAdapter) installUEFI(ctx context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	// Unlike RHEL — whose image tarball ships a populated EFI/<distro>/ in the
	// ESP — the Ubuntu/Debian image keeps the signed shim+grub only under
	// /usr/lib and leaves /boot/efi/EFI empty on a fresh disk install.
	// StageSignedShim pre-stages the pair from the family's source table
	// (signedPairSources) before wiring it.
	if err := StageSignedShim(StageSignedShimOpts{
		FS:            a.deps.FS,
		Sysroot:       sysroot,
		Arch:          a.deps.BootContext.Architecture,
		StorageResult: a.deps.BootContext.StorageResult,
		GrubDirName:   "grub",
		Family:        a.deps.BootContext.Family,
		Logger:        a.deps.Logger,
	}); err != nil {
		return err
	}

	if err := RegisterUEFIEntries(ctx, RegisterUEFIEntriesOptions{
		Runner:        a.deps.Runner,
		Logger:        a.deps.Logger,
		Sysroot:       sysroot,
		Arch:          a.deps.BootContext.Architecture,
		BootDevices:   a.deps.BootContext.StorageResult.BootDevices,
		RegisterNVRAM: a.deps.BootContext.RegisterNVRAM,
		ESPPartition:  a.deps.BootContext.StorageResult.ESPPartition,
		ESPDevice:     espDeviceOf(a.deps.BootContext.StorageResult),
		SnapshotDir:   a.deps.SnapshotDir,
		BootOrder:     a.deps.BootContext.BootOrder,
	}); err != nil {
		return err
	}

	return a.generateGrubConfig(ctx)
}

// generateGrubConfig regenerates grub.cfg and normalises it. Shared by the
// UEFI and BIOS installs: what firmware loads differs, what GRUB then reads
// does not.
func (a *debianAdapter) generateGrubConfig(ctx context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	if _, err := a.deps.Runner.Run(ctx, executor.CommandSpec{
		Path: "/usr/bin/env", Chroot: sysroot,
		Args:          []string{"update-grub"},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("update-grub: %w", err)
	}
	a.normalizeGrubCfgForSeparateBoot()
	// Same degraded-safe rewrite as RHEL (BE-W5): on a mirrored boot chain
	// the update-grub output locates /boot and root by md-array UUID, which
	// GRUB resolves non-deterministically when a mirror leg is missing —
	// dropping the mduuid set/hints forces the fs-uuid scan that finds a raw
	// metadata-1.0 member. No-op on non-RAID layouts (no mduuid references).
	makeGrubCfgDegradedSafeAt(a.deps.FS,
		filepath.Join(sysroot, "boot", "grub", "grub.cfg"),
		filepath.Join(sysroot, "boot", "grub2", "grub.cfg"),
	)
	return nil
}

// normalizeGrubCfgForSeparateBoot rewrites /boot-prefixed kernel /
// initrd lines in grub.cfg when /boot is a separate filesystem.
// Mirrors bootloader_debian_normalize_grub_for_separate_boot.
func (a *debianAdapter) normalizeGrubCfgForSeparateBoot() {
	if !a.hasBootMount() {
		return
	}
	sysroot := a.deps.BootContext.Sysroot
	for _, cfg := range []string{
		filepath.Join(sysroot, "boot", "grub", "grub.cfg"),
		filepath.Join(sysroot, "boot", "grub2", "grub.cfg"),
	} {
		body, err := os.ReadFile(cfg) //nolint:gosec // sysroot file
		if err != nil {
			continue
		}
		var out []string
		for _, line := range strings.Split(string(body), "\n") {
			switch {
			case strings.Contains(line, "linux") && strings.Contains(line, "/boot/vmlinuz"):
				out = append(out, strings.Replace(line, "/boot/vmlinuz-", "/vmlinuz-", 1))
			case strings.Contains(line, "initrd") && strings.Contains(line, "/boot/initrd"):
				out = append(out, strings.Replace(line, "/boot/initrd", "/initrd", 1))
			default:
				out = append(out, line)
			}
		}
		_ = a.deps.FS.WriteFile(cfg, []byte(strings.Join(out, "\n")), 0o644)
	}
}

func (a *debianAdapter) hasBootMount() bool {
	for _, fs := range a.deps.BootContext.StorageResult.Filesystems {
		if fs.Mount == "/boot" {
			return true
		}
	}
	return false
}

func (a *debianAdapter) Validate(_ context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	if !anyExist(sysroot, "boot/grub/grub.cfg", "boot/grub2/grub.cfg") {
		return fmt.Errorf("missing GRUB configuration under %s/boot", sysroot)
	}
	fstab := filepath.Join(sysroot, "etc", "fstab")
	info, err := os.Stat(fstab)
	if err != nil {
		return fmt.Errorf("missing or empty %s: %w", fstab, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("missing or empty %s", fstab)
	}
	if a.deps.BootContext.FirmwareMode == "uefi" {
		efiDir := filepath.Join(sysroot, "boot", "efi", "EFI")
		if _, err := os.Stat(efiDir); err != nil {
			return fmt.Errorf("missing EFI directory under %s", efiDir)
		}
		trinity := filepath.Join(efiDir, BootloaderID)
		fallback := filepath.Join(efiDir, "BOOT")
		if _, err := os.Stat(trinity); err != nil {
			if _, err2 := os.Stat(fallback); err2 != nil {
				return fmt.Errorf("missing %s or removable EFI boot directory under %s",
					BootloaderID, efiDir)
			}
		}
	}
	return nil
}
