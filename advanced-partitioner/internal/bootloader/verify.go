package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/verify"
)

// VerifyAndReport proves the bootloader finalise actually succeeded and logs
// the evidence — the UEFI NVRAM boot entry (efibootmgr -v output), the staged
// ESP loaders, and the grub config — so a finalise on a new distro/arch shows
// exactly what landed instead of a silent "ok". It returns an error listing
// every missing artefact. boot_method=none (FamilyNone) has no bootloader and is
// reported as such -- a RAM/tmpfs root, or a disk OS on BIOS firmware.
//
// This is the proof step the operator relies on: when we touch NVRAM and grub,
// we re-read them at the end and show that the boot entry is really registered.
func VerifyAndReport(ctx context.Context, runner executor.Runner, logger *txlog.Logger, bc BootContext) error {
	if bc.Family == FamilyNone {
		if logger != nil {
			logger.UserInfo("verify: boot_method=none — no bootloader to verify")
		}
		return nil
	}
	r := verify.New(logger, "bootloader verification (NVRAM + ESP + grub)")
	sysroot := bc.Sysroot

	if strings.EqualFold(bc.FirmwareMode, "uefi") {
		// 1. UEFI NVRAM — efibootmgr -v must show our TrinityX entry. efibootmgr
		//    lives in the installed image, NOT the install initramfs, and the
		//    write path registers the entry via `chroot /sysroot efibootmgr`
		//    (nvram.go). The re-read MUST go the same way: efivars is
		//    firmware-global and is bind-mounted into the sysroot for the
		//    finalise, so the chrooted read sees the entry the chrooted write
		//    created. Running plain `efibootmgr` on the host fails — the
		//    initramfs has no efibootmgr (caught on real UEFI installs).
		if eb := efibootmgrChrootPath(sysroot); eb == "" {
			// No efibootmgr in the image (minimal image): the NVRAM write was
			// skipped too. For a SINGLE boot disk that is tolerable -- the
			// removable fallback loader boots the node and no redundancy claim
			// is being made. For a MULTI-disk (mirrored-boot) install the
			// per-leg NVRAM entries ARE the verified failover property;
			// letting this pass silently is "redundancy theatre" (FTA v2
			// BE-W3 / MCS-17, barrier NB-5): the install would report success
			// while the disk-loss failover it was configured for does not
			// exist. register_nvram=no is the deliberate opt-out.
			if bc.RegisterNVRAM != "no" && len(bc.StorageResult.BootDevices) > 1 {
				r.Missing("efibootmgr not present in image, but this multi-disk mirrored-boot " +
					"install requires per-leg NVRAM entries; add efibootmgr to the OS image " +
					"(or set register_nvram=no to accept fallback-only boot)")
			} else {
				r.OK("NVRAM check skipped: efibootmgr not present in image (single boot disk; removable fallback suffices)")
			}
		} else {
			res, err := runner.Run(ctx, executor.CommandSpec{
				Path: eb, Args: []string{"-v"}, Chroot: sysroot,
				CaptureStdout: true, CaptureStderr: true,
			})
			if err != nil {
				r.Missing("efibootmgr -v (NVRAM unreadable): " + err.Error())
			} else {
				out := string(res.Stdout)
				r.Show("UEFI NVRAM (efibootmgr -v)", verify.Lines(out))
				if bc.RegisterNVRAM == "no" {
					// register_nvram=no: the entry was deliberately not
					// created, so requiring it would fail every such
					// install (FTA D12). Informational only.
					r.OK("NVRAM entry not required (register_nvram=no)")
				} else {
					r.Require(strings.Contains(out, BootloaderID),
						"NVRAM boot entry '"+BootloaderID+"' is registered",
						"NVRAM boot entry '"+BootloaderID+"'")
				}
			}
		}

		// 2. ESP loaders — list EFI/BOOT and EFI/<BootloaderID>, and require the
		//    arch-correct removable fallback (BOOTX64.EFI / BOOTAA64.EFI).
		espRoot := filepath.Join(sysroot, "boot", "efi", "EFI")
		for _, sub := range []string{"BOOT", BootloaderID} {
			d := filepath.Join(espRoot, sub)
			ents, derr := os.ReadDir(d)
			if derr != nil {
				continue
			}
			names := make([]string, 0, len(ents))
			for _, e := range ents {
				names = append(names, e.Name())
			}
			r.Show("ESP "+d, names)
		}
		if fb, ferr := ArchToUEFIFallbackLoader(bc.Architecture); ferr == nil {
			rel := strings.ReplaceAll(strings.TrimPrefix(fb, `\`), `\`, "/")
			_, serr := os.Stat(filepath.Join(sysroot, "boot", "efi", rel))
			r.Require(serr == nil,
				"removable fallback loader "+rel+" present",
				"ESP fallback loader "+rel)
		}
	}

	// 3. grub.cfg — either grub or grub2 dir, whichever the distro uses.
	grub := firstExistingPath(sysroot, "boot/grub/grub.cfg", "boot/grub2/grub.cfg")
	r.Require(grub != "", "grub config present: "+grub, "grub.cfg")

	return r.Done()
}

func firstExistingPath(root string, rels ...string) string {
	for _, r := range rels {
		p := filepath.Join(root, r)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
