package main

import (
	"os"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
)

// bootPlan is what this run will do to the node's boot path.
type bootPlan struct {
	// DiskOS: an OS set is laid down / mounted on local disk this run.
	DiskOS bool
	// InstallsGRUB: a disk OS on UEFI firmware. GRUB, shim and the NVRAM boot
	// order are UEFI-only (BIOS has no boot-entry management and no mirrored
	// boot chain), so a disk OS on BIOS gets no bootloader at all.
	InstallsGRUB bool
}

// bootEnv is the node-side facts the boot preflight needs.
type bootEnv struct {
	Firmware      string // detectFirmware(): "uefi" | "bios"
	RamdiskPivots bool   // detectRamdiskPivots(): install ramdisk is dracut-based
}

// planBoot derives the boot plan from the install mode, the (validated)
// disklayout and the detected firmware. Modes that never produce a disk OS
// (legacy, sanitize, memboot) and a layout-less or data-only run plan no disk
// OS; auto then falls back to memboot and the other modes fail later in
// runDecision with their own error.
func planBoot(mode installcontext.Mode, cfg *v2.Config, env bootEnv) bootPlan {
	// The disk-based OS modes are exactly auto/sync/full/local.
	if !mode.InstallsBootloader() {
		return bootPlan{}
	}
	if cfg == nil || len(cfg.Sets) == 0 || cfg.OSSet() == nil {
		return bootPlan{}
	}
	return bootPlan{DiskOS: true, InstallsGRUB: env.Firmware == "uefi"}
}

// preflightBoot is the boot-handoff gate. A disk OS that gets no bootloader
// relies on the install ramdisk pivoting to /sysroot; a ramdisk that instead
// reboots into the disk finds nothing to boot there and loops back into PXE
// forever. Returns a *safetyError (exit-2 safety_refusal contract) or nil.
func preflightBoot(p bootPlan, env bootEnv) error {
	if p.DiskOS && !p.InstallsGRUB && !env.RamdiskPivots {
		return safetyRefusalf("bootloader-none-needs-dracut-ramdisk",
			"node firmware is %q, so this install writes no bootloader (GRUB, shim and the UEFI boot order are UEFI-only), but the install ramdisk is not dracut-based -- after the install it would reboot into a disk with nothing to boot and loop back into PXE forever; pack the osimage's Luna ramdisk with dracut and the 95luna module, or enable UEFI boot in the node's firmware and reprovision",
			env.Firmware)
	}
	return nil
}

// detectRamdiskPivots mirrors luna-installer.sh's post-install test: a dracut
// install ramdisk pivots to /sysroot after install.sh and needs no bootloader,
// anything else reboots into the installed disk. Package var (not a plain
// func) so tests can pin the result independently of the host the suite runs
// on.
var detectRamdiskPivots = func() bool {
	for _, p := range []string{"/lib/dracut-lib.sh", "/usr/lib/dracut/dracut-lib.sh"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
