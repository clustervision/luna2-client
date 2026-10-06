package main

import (
	"fmt"
	"os"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
)

// bootPlan is what this run will do to the node's boot path.
type bootPlan struct {
	// DiskOS: an OS set is laid down / mounted on local disk this run.
	DiskOS bool
	// InstallsGRUB: a disk OS whose set wants a bootloader, on firmware that
	// has one to install. The shim and the NVRAM boot order are UEFI-only, so
	// a disk OS on BIOS gets GRUB alone, and only when the set asks for
	// "first" -- "last" is a boot-order request BIOS cannot honour.
	InstallsGRUB bool
	// BootloaderNone: the OS set asked for no bootloader. Separate from
	// InstallsGRUB because the two reasons for having none need different
	// words: one is the node's firmware, the other is the operator's choice.
	BootloaderNone bool
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
	none := cfg.OSSet().Bootloader == v2.BootloaderNone
	first := cfg.OSSet().Bootloader == v2.BootloaderFirst
	return bootPlan{
		DiskOS:         true,
		InstallsGRUB:   !none && (env.Firmware == "uefi" || first),
		BootloaderNone: none,
	}
}

// preflightBoot is the boot-handoff gate. A disk OS that gets no bootloader
// relies on the install ramdisk pivoting to /sysroot; a ramdisk that instead
// reboots into the disk finds nothing to boot there and loops back into PXE
// forever. Returns a *safetyError (exit-2 safety_refusal contract) or nil.
func preflightBoot(p bootPlan, env bootEnv) error {
	if !p.DiskOS || p.InstallsGRUB || env.RamdiskPivots {
		return nil
	}
	// Name the reason this install has no bootloader: a BIOS node needs a
	// firmware change, an operator's bootloader=none needs a config change.
	cause := fmt.Sprintf("node firmware is %q, so this install writes no bootloader (GRUB, shim and the UEFI boot order are UEFI-only)", env.Firmware)
	alternative := "enable UEFI boot in the node's firmware and reprovision"
	if env.Firmware == "uefi" {
		cause = `the OS set asks for bootloader "none", so this install writes no bootloader`
		alternative = `remove the set's bootloader key (or set it to "last") so GRUB is installed`
	}
	return safetyRefusalf("bootloader-none-needs-dracut-ramdisk",
		"%s, but the install ramdisk is not dracut-based -- after the install it would reboot into a disk with nothing to boot and loop back into PXE forever; pack the osimage's Luna ramdisk with dracut and the 95luna module, or %s",
		cause, alternative)
}

// ramdiskPivotMarkers identify an install ramdisk that hands off to /sysroot
// after install.sh instead of rebooting into the disk, so a disk OS on it needs
// no bootloader. Two families qualify: dracut, via its library; and Luna's own
// initramfs-tools hook, which Debian and Ubuntu images use -- it mounts the
// installed root at /sysroot and returns to the boot scripts, exactly as the
// dracut 95luna module does. Testing for dracut alone refuses every Ubuntu
// node a bootloader="none" layout it can in fact honour.
var ramdiskPivotMarkers = []string{
	// Luna's own handoff script, in each ramdisk family: these name the thing
	// that actually pivots, so they are the markers that mean what the question
	// asks. Verified present in a built ramdisk of each kind.
	"/usr/share/luna/luna-installer.sh",
	"/scripts/local-premount/luna-start",
	// The dracut library, kept because it is what this test has always used on
	// EL and SUSE. It identifies the framework rather than the handoff, so it
	// also matches a dracut ramdisk built without the luna module -- which does
	// not pivot. Narrowing that is a behaviour change for working nodes, so it
	// is left alone here.
	"/lib/dracut-lib.sh",
	"/usr/lib/dracut/dracut-lib.sh",
}

// detectRamdiskPivots mirrors the installers' post-install test. Package var
// (not a plain func) so tests can pin the result independently of the host the
// suite runs on.
var detectRamdiskPivots = func() bool {
	for _, p := range ramdiskPivotMarkers {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
