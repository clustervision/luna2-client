package bootloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// suseAdapter implements the SUSE family (openSUSE Leap/Tumbleweed, SLES)
// by composing the RHEL flow: both families ship the identical toolchain
// this installer drives - dracut for the initramfs, grub2-mkconfig with
// the real config at /boot/grub2/grub.cfg, /etc/default/grub with the
// same variable names, signed shim + grubx64.efi under
// /boot/efi/EFI/<distro>/ (opensuse/sles - found by the generic
// findSignedShim scan), and grub2-install for BIOS. The differences
// SUSE is known for (update-bootloader, perl-bootloader) are wrappers
// around exactly these tools and are NOT used here - calling the tools
// directly keeps one code path per action across families.
//
// UEFI note (same as RHEL): never `grub2-install --target=x86_64-efi`
// (module-stripping bug, see StageSignedShim) - the signed shim + grub
// from the image are staged instead, with GrubDirName="grub2".
//
// SUSE's BLS-style entries (when a future image uses them) are handled by
// the shared normalizeBLSEntries, which is a no-op when
// /boot/loader/entries does not exist - true for classic grub2 images.
type suseAdapter struct{ rhel rhelAdapter }

func newSUSEAdapter(deps AdapterDeps) *suseAdapter {
	return &suseAdapter{rhel: rhelAdapter{deps: deps}}
}

func (a *suseAdapter) Family() Family { return FamilySUSE }

// Prepare mirrors the RHEL preflight with a SUSE-flavoured error message
// so an operator on a minimal image knows which packages are missing.
func (a *suseAdapter) Prepare(_ context.Context) error {
	sysroot := a.rhel.deps.BootContext.Sysroot
	if !anyExist(sysroot, "usr/bin/dracut", "usr/sbin/dracut") {
		return fmt.Errorf("missing required command: dracut (SUSE image must ship the dracut package)")
	}
	if !anyExist(sysroot, "usr/sbin/grub2-mkconfig", "usr/bin/grub2-mkconfig") {
		return fmt.Errorf("missing required command: grub2-mkconfig (SUSE image must ship grub2)")
	}
	if len(a.rhel.deps.BootContext.StorageResult.BootDevices) == 0 {
		return fmt.Errorf("storage-result boot_devices must contain at least one disk")
	}
	if a.rhel.deps.BootContext.FirmwareMode == "uefi" {
		if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi")); err != nil {
			return fmt.Errorf("missing EFI mount directory: %s/boot/efi", sysroot)
		}
	}
	return nil
}

// RegenerateInitramfs is dracut, exactly as on RHEL.
func (a *suseAdapter) RegenerateInitramfs(ctx context.Context) error {
	return a.rhel.RegenerateInitramfs(ctx)
}

// Install writes /etc/default/grub then runs the shared RHEL flow: the UEFI
// one (StageSignedShim + efibootmgr + grub2-mkconfig), or the BIOS one
// (grub2-install per disk) when the os set asked for a bootloader there.
// SUSE's only deviation —
// the image keeps the signed shim+grub under /usr/lib64/efi and leaves the
// ESP's EFI/ ABSENT on a fresh disk install — is data, not code: the shared
// StageSignedShim pre-stages the pair from the FamilySUSE row of
// signedPairSources (BootContext.Family flows through the embedded RHEL
// flow). The degraded-safe grub.cfg rewrite (BE-W5,
// makeGrubCfgDegradedSafe) is inherited too — SUSE uses the same
// /boot/grub2/grub.cfg.
func (a *suseAdapter) Install(ctx context.Context) error {
	return a.rhel.Install(ctx)
}

// Validate checks the same artefacts as RHEL: /boot/grub2/grub.cfg,
// a non-empty fstab, and (UEFI) the ESP loader directories.
func (a *suseAdapter) Validate(ctx context.Context) error {
	return a.rhel.Validate(ctx)
}
