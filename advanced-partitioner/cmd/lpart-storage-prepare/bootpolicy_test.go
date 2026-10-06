package main

import (
	"errors"
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
		// The operator's bootloader key only ever takes GRUB away; it cannot
		// put it on a firmware that has none.
		{"auto os uefi bootloader last", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderLast), "uefi",
			bootPlan{DiskOS: true, InstallsGRUB: true}},
		{"auto os uefi bootloader first", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderFirst), "uefi",
			bootPlan{DiskOS: true, InstallsGRUB: true}},
		{"auto os uefi bootloader none", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderNone), "uefi",
			bootPlan{DiskOS: true, BootloaderNone: true}},
		{"auto os bios bootloader none", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderNone), "bios",
			bootPlan{DiskOS: true, BootloaderNone: true}},
		{"auto os bios bootloader first", installcontext.ModeAuto, osCfgBootloader(v2.BootloaderFirst), "bios",
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
