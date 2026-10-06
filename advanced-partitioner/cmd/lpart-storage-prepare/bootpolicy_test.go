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
		reason string // "" = must proceed
	}{
		{"disk os uefi, dracut", bootPlan{DiskOS: true, InstallsGRUB: true}, bootEnv{Firmware: "uefi", RamdiskPivots: true}, ""},
		{"disk os uefi, non-dracut", bootPlan{DiskOS: true, InstallsGRUB: true}, bootEnv{Firmware: "uefi"}, ""},
		{"disk os bios, dracut", bootPlan{DiskOS: true}, bootEnv{Firmware: "bios", RamdiskPivots: true}, ""},
		{"disk os bios, non-dracut", bootPlan{DiskOS: true}, bootEnv{Firmware: "bios"}, "bootloader-none-needs-dracut-ramdisk"},
		{"no disk os bios, non-dracut", bootPlan{}, bootEnv{Firmware: "bios"}, ""},
		{"no disk os uefi, non-dracut", bootPlan{}, bootEnv{Firmware: "uefi"}, ""},
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
			// The operator has to be able to act on it: the message names
			// both the ramdisk fix and the firmware alternative.
			for _, want := range []string{"dracut", "95luna", "UEFI"} {
				if !strings.Contains(se.msg, want) {
					t.Fatalf("refusal message missing %q: %s", want, se.msg)
				}
			}
		})
	}
}

// A UEFI disk OS must not be touched by the ramdisk rule: GRUB on the disk
// makes the post-install reboot work whatever the ramdisk does.
func TestPreflightBootIgnoresRamdiskOnUEFI(t *testing.T) {
	if err := preflightBoot(bootPlan{DiskOS: true, InstallsGRUB: true}, bootEnv{Firmware: "uefi"}); err != nil {
		t.Fatalf("a UEFI GRUB install must not depend on the ramdisk type: %v", err)
	}
}
