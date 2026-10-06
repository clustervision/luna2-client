package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
)

// osCfg is a one-set disklayout with an OS set, the shape that makes a run a
// disk OS. dataCfg has no OS set, so no bootloader is ever involved.
func osCfg() *v2.Config {
	return &v2.Config{Version: 2, Sets: []v2.Set{{Name: "os", Role: "os"}}}
}

// osCfgBootloader is an OS-set layout whose operator set the bootloader key.
func osCfgBootloader(bl v2.Bootloader) *v2.Config {
	return &v2.Config{Version: 2, Sets: []v2.Set{{Name: "os", Role: "os", Bootloader: bl}}}
}

func dataCfg() *v2.Config {
	return &v2.Config{Version: 2, Sets: []v2.Set{{Name: "data", Role: "data"}}}
}

// The whole boot-plan table: which modes and layouts put an OS on local disk,
// and when that disk OS gets GRUB (UEFI only).
func TestPlanBoot(t *testing.T) {
	cases := []struct {
		name     string
		mode     installcontext.Mode
		cfg      *v2.Config
		firmware string
		want     bootPlan
	}{
		{"auto os uefi", installcontext.ModeAuto, osCfg(), "uefi", bootPlan{DiskOS: true, InstallsGRUB: true}},
		{"auto os bios", installcontext.ModeAuto, osCfg(), "bios", bootPlan{DiskOS: true}},
		{"sync os bios", installcontext.ModeSync, osCfg(), "bios", bootPlan{DiskOS: true}},
		{"full os bios", installcontext.ModeFull, osCfg(), "bios", bootPlan{DiskOS: true}},
		{"local os bios", installcontext.ModeLocal, osCfg(), "bios", bootPlan{DiskOS: true}},
		{"auto data-only bios", installcontext.ModeAuto, dataCfg(), "bios", bootPlan{}},
		{"auto no layout bios", installcontext.ModeAuto, nil, "bios", bootPlan{}},
		{"auto empty layout bios", installcontext.ModeAuto, &v2.Config{Version: 2}, "bios", bootPlan{}},
		{"memboot os bios", installcontext.ModeMemboot, osCfg(), "bios", bootPlan{}},
		{"sanitize os bios", installcontext.ModeSanitize, osCfg(), "bios", bootPlan{}},
		{"legacy os bios", installcontext.ModeLegacy, osCfg(), "bios", bootPlan{}},
		{"memboot os uefi", installcontext.ModeMemboot, osCfg(), "uefi", bootPlan{}},
		// The operator's bootloader key decides whether a disk OS gets GRUB.
		// On BIOS only "first" asks for one; "last" has no boot order to
		// arrange there and keeps the no-bootloader path.
		{"auto os uefi bootloader last", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderLast), "uefi",
			bootPlan{DiskOS: true, InstallsGRUB: true}},
		{"auto os uefi bootloader first", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderFirst), "uefi",
			bootPlan{DiskOS: true, InstallsGRUB: true}},
		{"auto os uefi bootloader none", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderNone), "uefi",
			bootPlan{DiskOS: true, BootloaderNone: true}},
		{"auto os bios bootloader none", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderNone), "bios",
			bootPlan{DiskOS: true, BootloaderNone: true}},
		{"auto os bios bootloader first", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderFirst), "bios",
			bootPlan{DiskOS: true, InstallsGRUB: true}},
		{"auto os bios bootloader last", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderLast), "bios",
			bootPlan{DiskOS: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planBoot(c.mode, c.cfg, bootEnv{Firmware: c.firmware, RamdiskPivots: true})
			if got != c.want {
				t.Fatalf("planBoot = %+v, want %+v", got, c.want)
			}
		})
	}
}

// The gate itself: only a disk OS that gets no bootloader behind a ramdisk
// that reboots instead of pivoting is refused. Everything else proceeds.
func TestPreflightBoot(t *testing.T) {
	cases := []struct {
		name   string
		plan   bootPlan
		env    bootEnv
		reason string   // "" = must proceed
		says   []string // substrings the refusal must carry
	}{
		{"disk os uefi, dracut", bootPlan{DiskOS: true, InstallsGRUB: true}, bootEnv{Firmware: "uefi", RamdiskPivots: true}, "", nil},
		{"disk os uefi, non-dracut", bootPlan{DiskOS: true, InstallsGRUB: true}, bootEnv{Firmware: "uefi"}, "", nil},
		{"disk os bios, dracut", bootPlan{DiskOS: true}, bootEnv{Firmware: "bios", RamdiskPivots: true}, "", nil},
		// A BIOS node that does install GRUB has something to boot, so the
		// refusal that exists for a bootloader-less disk OS must not fire.
		{"disk os bios grub, non-dracut", bootPlan{DiskOS: true, InstallsGRUB: true}, bootEnv{Firmware: "bios"}, "", nil},
		// The operator has to be able to act on it, and the message must name
		// the reason this install has no bootloader -- firmware or choice.
		{"disk os bios, non-dracut", bootPlan{DiskOS: true}, bootEnv{Firmware: "bios"},
			"bootloader-none-needs-dracut-ramdisk", []string{"dracut", "95luna", "firmware is \"bios\"", "UEFI"}},
		{"disk os uefi bootloader none, dracut", bootPlan{DiskOS: true, BootloaderNone: true}, bootEnv{Firmware: "uefi", RamdiskPivots: true}, "", nil},
		{"disk os uefi bootloader none, non-dracut", bootPlan{DiskOS: true, BootloaderNone: true}, bootEnv{Firmware: "uefi"},
			"bootloader-none-needs-dracut-ramdisk", []string{"dracut", "95luna", "bootloader", "none"}},
		{"no disk os bios, non-dracut", bootPlan{}, bootEnv{Firmware: "bios"}, "", nil},
		{"no disk os uefi, non-dracut", bootPlan{}, bootEnv{Firmware: "uefi"}, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := preflightBoot(c.plan, c.env)
			if c.reason == "" {
				if err != nil {
					t.Fatalf("expected the run to proceed; got %v", err)
				}
				return
			}
			var se *safetyError
			if !errors.As(err, &se) {
				t.Fatalf("expected a safety refusal; got %v", err)
			}
			if se.reason != c.reason {
				t.Fatalf("reason = %q, want %q", se.reason, c.reason)
			}
			for _, want := range c.says {
				if !strings.Contains(se.msg, want) {
					t.Fatalf("refusal message missing %q: %s", want, se.msg)
				}
			}
		})
	}
}

// A bootloader=none refusal must not blame the firmware: the node is on UEFI
// and the remedy is the operator's own key, not a BIOS setting.
func TestPreflightBootNoneDoesNotBlameFirmware(t *testing.T) {
	err := preflightBoot(bootPlan{DiskOS: true, BootloaderNone: true}, bootEnv{Firmware: "uefi"})
	var se *safetyError
	if !errors.As(err, &se) {
		t.Fatalf("expected a safety refusal; got %v", err)
	}
	if strings.Contains(se.msg, "firmware is") {
		t.Fatalf("the firmware is not the reason here: %s", se.msg)
	}
}

// A UEFI disk OS must not be touched by the ramdisk rule: GRUB on the disk
// makes the post-install reboot work whatever the ramdisk does.
func TestPreflightBootIgnoresRamdiskOnUEFI(t *testing.T) {
	if err := preflightBoot(bootPlan{DiskOS: true, InstallsGRUB: true}, bootEnv{Firmware: "uefi"}); err != nil {
		t.Fatalf("a UEFI GRUB install must not depend on the ramdisk type: %v", err)
	}
}

// An Ubuntu/Debian install ramdisk carries Luna's initramfs-tools hook instead
// of dracut, and that hook pivots to /sysroot just as the dracut module does.
// Pinned because testing for dracut alone refused every Ubuntu node a
// bootloader="none" layout it can honour, and the node then reinstalled in a
// loop rather than reporting the refusal.
func TestRamdiskPivotsDetectsInitramfsToolsHook(t *testing.T) {
	// The fix itself: the hook has to be one of the markers. Asserted on the
	// default list, because a test that supplies its own list only exercises
	// the lookup and passes just as happily with the marker removed.
	for _, want := range []string{
		"/scripts/local-premount/luna-start", // initramfs-tools: Debian, Ubuntu
		"/usr/share/luna/luna-installer.sh",  // dracut, as module-setup.sh installs it
	} {
		found := false
		for _, m := range ramdiskPivotMarkers {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s must be a pivot marker; markers are %v", want, ramdiskPivotMarkers)
		}
	}

	dir := t.TempDir()
	hook := filepath.Join(dir, "luna-start")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the fake hook: %v", err)
	}

	saved := ramdiskPivotMarkers
	t.Cleanup(func() { ramdiskPivotMarkers = saved })

	ramdiskPivotMarkers = []string{filepath.Join(dir, "absent"), hook}
	if !detectRamdiskPivots() {
		t.Fatal("a ramdisk carrying the initramfs-tools luna hook pivots")
	}

	ramdiskPivotMarkers = []string{filepath.Join(dir, "absent")}
	if detectRamdiskPivots() {
		t.Fatal("a ramdisk carrying no marker does not pivot")
	}
}
