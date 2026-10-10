// Package bootloader is the Go port of /root/NEWTREE/lib/bootloader_*.sh
// and the orchestrator at /root/NEWTREE/bin/lpart-bootloader-finalise.
//
// The package is split for diff-ability:
//
//	context.go      BootContext + BuildBootContext + family/arch detection
//	fstab.go        WriteFstab (preserves existing entries)
//	bindmount.go    Recursive-bind /dev, /proc, /sys, /run into /sysroot
//	kernel_args.go  rd.auto / rd.md / rd.lvm / rootfstype assembly
//	nvram.go        UEFI NVRAM clear + register + validate + snapshot
//	remove.go       Strip an existing bootloader (bootloader=none)
//	adapter.go      Adapter interface; family dispatch
//	rhel.go         RHEL/Rocky/Alma/CentOS/Fedora path (dracut + grub2-*)
//	uefi_signed.go  Signed shim + grubx64.efi staging shared by every
//	                 GRUB-family UEFI install (RHEL family with
//	                 /boot/grub2/, Debian with /boot/grub/); replaces
//	                 the broken `grub*-install --target=x86_64-efi`
//	                 rebuild that produced rescue-prompt-only binaries
//	debian.go       Debian/Ubuntu path (update-initramfs + grub-install + update-grub)
//	suse.go         SUSE stub — fails with a clear "not implemented" error
//
// Cross-distribution / architecture notes:
//   - The Adapter interface fans out by detected family. Adding a new
//     family means a new file + adapter constructor; no other code
//     changes are required.
//   - UEFI target codes (x86_64-efi / arm64-efi) and fallback loader
//     names (BOOTX64.EFI / BOOTAA64.EFI) live in ArchToUEFITarget /
//     ArchToUEFIFallbackLoader.
//   - The bootloader tools (dracut, grub2-mkconfig, update-grub,
//     efibootmgr, and on BIOS grub2-install / grub-install) only
//     exist after the OS image has been extracted, so every
//     invocation runs via chroot /sysroot. The executor's
//     CommandSpec.Chroot field does this transparently. The UEFI
//     shim and grubx64.efi are NOT executed in chroot — they are
//     EFI PE binaries copied straight from
//     /sysroot/boot/efi/EFI/<distro>/ to /sysroot/boot/efi/EFI/BOOT/
//     by StageSignedShim.
package bootloader

import (
	"os"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// Family identifies a bootloader/initramfs adapter family.
type Family string

const (
	FamilyRHEL   Family = "rhel"
	FamilyDebian Family = "debian"
	FamilySUSE   Family = "suse"
	FamilyNone   Family = "none" // boot_method=none ⇒ no bootloader install
)

// BootContext bundles every piece of context the bootloader cascade
// needs. It mirrors the JSON written by lib/bootloader_common.sh::
// build_boot_context_json.
type BootContext struct {
	Sysroot        string               `json:"sysroot"`
	Family         Family               `json:"family"`
	FirmwareMode   string               `json:"firmware_mode"`
	Architecture   string               `json:"architecture"`
	KernelArgs     string               `json:"kernel_args"`
	BootMethod     string               `json:"boot_method"`
	BootOrder      string               `json:"boot_order"`
	BootFirmware   string               `json:"boot_firmware"`
	RegisterNVRAM  string               `json:"register_nvram"`
	ClearUEFINVRAM bool                 `json:"clear_uefi_nvram"`
	StorageResult  result.StorageResult `json:"storage_result"`
}

// BIOSGRUBRequested reports whether this node asked for a BIOS GRUB install:
// non-uefi firmware and an os set whose bootloader key says "first". "last"
// asks for a place in the UEFI boot order, which a BIOS node does not have,
// so it keeps the no-bootloader path.
func (c BootContext) BIOSGRUBRequested() bool {
	return c.FirmwareMode != "uefi" && c.BootOrder == bootOrderFirst
}

// BuildBootContext assembles a BootContext from a StorageResult plus a
// runtime architecture override (defaults to runtime.GOARCH-ish via
// uname -m; tests pin it). osReleasePath defaults to "<sysroot>/etc/os-release";
// tests inject a synthetic file.
//
// When BootMethod=="none" the family is set to FamilyNone unconditionally;
// the bash predecessor skips family detection in that case because the
// /etc/os-release file may not exist (e.g. tmpfs root with no extracted
// image).
func BuildBootContext(in result.StorageResult, arch, osReleasePath string) (BootContext, error) {
	ctx := BootContext{
		Sysroot:        in.MountedTargetRoot,
		FirmwareMode:   in.FirmwareMode,
		Architecture:   arch,
		BootMethod:     in.BootMethod,
		BootOrder:      in.BootOrder,
		BootFirmware:   in.BootFirmware,
		RegisterNVRAM:  in.RegisterNVRAM,
		ClearUEFINVRAM: in.ClearUEFINVRAM,
		StorageResult:  in,
		KernelArgs:     KernelArgs(in),
	}
	if ctx.BootMethod == "" {
		ctx.BootMethod = "grub"
	}
	if ctx.BootOrder == "" {
		ctx.BootOrder = "last"
	}
	if ctx.BootFirmware == "" {
		ctx.BootFirmware = "auto"
	}
	if ctx.RegisterNVRAM == "" {
		ctx.RegisterNVRAM = "auto"
	}
	if ctx.BootMethod == "none" {
		ctx.Family = FamilyNone
		return ctx, nil
	}
	if osReleasePath == "" {
		osReleasePath = ctx.Sysroot + "/etc/os-release"
	}
	fam, err := DetectFamily(osReleasePath)
	if err != nil {
		return BootContext{}, err
	}
	ctx.Family = fam
	return ctx, nil
}

// DetectFamily reads /etc/os-release in the target OS and decides which
// adapter family handles it. Mirrors bootloader_detect_os_family.
//
// Matching is case-insensitive on the ID + ID_LIKE fields:
//
//	rocky|rhel|alma|centos|fedora → FamilyRHEL
//	ubuntu|debian                 → FamilyDebian
//	sles|opensuse|suse            → FamilySUSE
//
// Unknown distros produce an explicit error so the operator sees
// exactly why finalisation refused to proceed.
func DetectFamily(osReleasePath string) (Family, error) {
	body, err := os.ReadFile(osReleasePath) //nolint:gosec // operator-controlled path
	if err != nil {
		return "", imageErrorf("image-missing-os-release", "missing %s: %v", osReleasePath, err)
	}
	id, idLike := "", ""
	for _, line := range strings.Split(string(body), "\n") {
		switch {
		case strings.HasPrefix(line, "ID="):
			id = strings.ToLower(unquote(strings.TrimPrefix(line, "ID=")))
		case strings.HasPrefix(line, "ID_LIKE="):
			idLike = strings.ToLower(unquote(strings.TrimPrefix(line, "ID_LIKE=")))
		}
	}
	combined := id + " " + idLike
	switch {
	case containsAny(combined, "rocky", "rhel", "alma", "centos", "fedora"):
		return FamilyRHEL, nil
	case containsAny(combined, "ubuntu", "debian"):
		return FamilyDebian, nil
	case containsAny(combined, "sles", "opensuse", "suse"):
		return FamilySUSE, nil
	default:
		return "", imageErrorf("unsupported-os-family",
			"unsupported target OS family from os-release: id=%q id_like=%q",
			id, idLike)
	}
}

// ArchToUEFITarget returns the GRUB target string for a given
// architecture, e.g. "x86_64-efi" for amd64 or "arm64-efi" for aarch64.
// Mirrors bootloader_arch_to_uefi_target.
func ArchToUEFITarget(arch string) (string, error) {
	switch arch {
	case "x86_64", "amd64":
		return "x86_64-efi", nil
	case "aarch64", "arm64":
		return "arm64-efi", nil
	default:
		return "", configErrorf("unsupported-architecture", "unsupported UEFI architecture target: %s", arch)
	}
}

// ArchToUEFIFallbackLoader returns the per-arch removable fallback
// loader path (`\\EFI\\BOOT\\BOOTX64.EFI` on amd64, `\\EFI\\BOOT\\BOOTAA64.EFI`
// on arm64). The path is in UEFI's `\\`-separated form because
// efibootmgr's --loader argument expects it.
func ArchToUEFIFallbackLoader(arch string) (string, error) {
	switch arch {
	case "x86_64", "amd64":
		return `\EFI\BOOT\BOOTX64.EFI`, nil
	case "aarch64", "arm64":
		return `\EFI\BOOT\BOOTAA64.EFI`, nil
	default:
		return "", configErrorf("unsupported-architecture", "unsupported UEFI fallback loader architecture: %s", arch)
	}
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
