// Package lvm builds the LVM stack on top of a partition + md layout.
// It is the Go port of /root/NEWTREE/lib/lvm.sh.
//
// Cross-distribution / arch notes: the LVM2 userspace ships everywhere
// with the same flag set; no per-platform tweaks here.
package lvm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
)

// lvcreateBaseArgs is the ONE lvcreate flag set used on every distro.
// Deliberately universal — do not reintroduce per-version variants:
//   - --noudevsync: in some install-initramfs environments (observed on
//     Ubuntu 26.04 / kernel 7.0, newer lvm2) lvcreate hangs indefinitely
//     on a udev cookie that never arrives, freezing the install.
//   - -Z n -W n: with --noudevsync, lvcreate's own zero/wipe of the new
//     LV can't find the /dev/mapper node udev would have made ("device
//     not cleared"). The LV sits on a partition lpart already wipefs'd
//     and the next step mkfs's it (overwriting the start), so zeroing/
//     wiping here is a redundant no-op — disabled everywhere.
//
// Build() materialises the mapper nodes itself right after each lvcreate
// (vgmknodes + dmsetup mknodes — no-ops where udev already made them),
// so every distro reaches the same end state.
var lvcreateBaseArgs = []string{"--noudevsync", "-y", "-Z", "n", "-W", "n"}

// State captures the small amount of information later steps need from
// the LVM build: the VG name and the PV device it sits on. Mirrors the
// JSON shape lib/lvm.sh emits.
type State struct {
	VGName   string `json:"vg_name"`
	PVDevice string `json:"pv_device"`
}

// Options configures a Builder.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger
}

// Builder builds the LVM stack.
type Builder struct {
	o Options
}

// New returns a Builder.
func New(o Options) *Builder { return &Builder{o: o} }

// Validate is the Go port of storage_lvm_validate in lib/lvm.sh. It is
// invoked by lpart-storage-prepare before any destructive work begins.
func Validate(set *v2.Set) error {
	if set == nil {
		return fmt.Errorf("lvm.Validate: nil set")
	}
	lvmVols := set.LVMVolumes()
	if len(lvmVols) == 0 {
		return fmt.Errorf("volumes[] must contain at least one lvm volume")
	}
	hundred := 0
	for _, v := range lvmVols {
		if v.Size == "100%" {
			hundred++
		}
	}
	if hundred > 1 {
		return fmt.Errorf("only one LV per VG may use 100%%")
	}
	if set.Role == "os" {
		rootCount := 0
		for _, v := range lvmVols {
			if v.Mountpoint == "/" {
				rootCount++
			}
		}
		if rootCount != 1 {
			return fmt.Errorf("OS set requires an lvm volume with mountpoint=/")
		}
	}
	return nil
}

// Build creates the PV+VG+LV stack. Order is: pvcreate, vgcreate, then
// lvcreate for each LVM volume — fixed-size LVs first, 100%FREE last so
// the remaining space LV is allocated after every other LV has claimed
// its bytes. Mirrors lib/lvm.sh::create_lvm_stack.
func (b *Builder) Build(ctx context.Context, set *v2.Set, layout partition.Layout, md raid.State) (State, error) {
	if b.o.Runner == nil {
		return State{}, fmt.Errorf("lvm.Build: Runner is required")
	}

	pvDevice := md.MDDevice
	if pvDevice == "" {
		if len(layout.StoragePartitions) == 0 {
			return State{}, fmt.Errorf("lvm.Build: no storage partitions and no md device")
		}
		pvDevice = layout.StoragePartitions[0]
	}

	vgName := set.VGName()
	state := State{VGName: vgName, PVDevice: pvDevice}

	// Defensive superblock clear on the raid=none path (the md path is
	// wiped by raid.create): a stale md 1.2 superblock surviving inside a
	// re-created partition can be auto-assembled by udev between sgdisk
	// and here, leaving the device busy so `pvcreate -ff` fails on
	// reinstall (FTA D7). Best-effort - pvcreate remains the gate.
	_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "mdadm", Args: []string{"--zero-superblock", pvDevice},
		CaptureStdout: true, CaptureStderr: true,
	})
	_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "wipefs", Args: []string{"-a", pvDevice},
		CaptureStdout: true, CaptureStderr: true,
	})

	if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "pvcreate", Args: []string{"-ff", "-y", pvDevice},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return State{}, fmt.Errorf("pvcreate %s: %w", pvDevice, err)
	}
	// NB-9 belt (FTA v2 BE-A6b): a same-name VG left by a PRIOR install of a
	// DIFFERENT disk topology can survive cleanup (its md array was
	// disassembled at cleanup time, then reassembled by a later udev event),
	// so `vgcreate` here would make a SECOND VG of this name and the following
	// `lvcreate` can no longer disambiguate -> exit 5, install fails. Purge any
	// existing VG of this name by UUID immediately before creating ours (the
	// fresh pvDevice has no VG yet, so this only removes OTHER same-name
	// groups). Cleanup's protected-disk fence has already run; a same-name
	// collision with the OS VG is by construction a stale OS remnant, not a
	// persistent data set (those carry distinct VG names).
	b.purgeStaleVGsByName(ctx, vgName)

	if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "vgcreate", Args: []string{vgName, pvDevice},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return State{}, fmt.Errorf("vgcreate %s on %s: %w", vgName, pvDevice, err)
	}
	_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{Path: "udevadm", Args: []string{"settle"}})

	// Sort: fixed-size LVs first (preserve their relative order), 100%
	// last. Mirrors the jq sort in lib/lvm.sh.
	vols := set.LVMVolumes()
	type entry struct {
		v   v2.Volume
		idx int
	}
	entries := make([]entry, len(vols))
	for i, v := range vols {
		entries[i] = entry{v: v, idx: i}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		hundredI := entries[i].v.Size == "100%"
		hundredJ := entries[j].v.Size == "100%"
		if hundredI != hundredJ {
			return !hundredI
		}
		return entries[i].idx < entries[j].idx
	})

	remainingSeen := false
	for _, e := range entries {
		v := e.v
		// One universal flag set for every distro; see lvcreateBaseArgs.
		args := append(append([]string{}, lvcreateBaseArgs...), "-n", v.Name)
		if v.Size == "100%" {
			if remainingSeen {
				return State{}, fmt.Errorf("only one LV may consume 100%%")
			}
			remainingSeen = true
			args = append(args, "-l", "100%FREE", vgName)
		} else {
			size := v.Size
			if size == "" {
				return State{}, fmt.Errorf("lv %s has no size", v.Name)
			}
			args = append(args, "-L", size, vgName)
		}
		if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
			Path: "lvcreate", Args: args,
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			return State{}, fmt.Errorf("lvcreate %s: %w", v.Name, err)
		}
		// --noudevsync suppresses the udev rule that would populate
		// /dev/os/<lv> and /dev/mapper/os-<lv>, and in the Ubuntu 26.04
		// install initramfs udev never materialises them (the very reason
		// --noudevsync is needed). Create the nodes directly from the kernel
		// device-mapper state so the following filesystem step (wipefs +
		// mkfs on /dev/mapper/os-root) can open them. `dmsetup mknodes` is a
		// no-op where udev already made the nodes, so it is safe everywhere.
		_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{Path: "vgmknodes", Args: []string{vgName}})
		_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{Path: "dmsetup", Args: []string{"mknodes"}})
		_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{Path: "udevadm", Args: []string{"settle"}})
	}

	return state, nil
}

// purgeStaleVGsByName removes every existing VG named vgName, by UUID (the
// name may be ambiguous when duplicates exist). Best-effort: each command is
// allowed to fail without aborting the install (the subsequent vgcreate is
// the real gate). See the call site in Build for the rationale (FTA v2 BE-A6b,
// barrier NB-9).
func (b *Builder) purgeStaleVGsByName(ctx context.Context, vgName string) {
	res, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "vgs", Args: []string{"--noheadings", "-o", "vg_uuid,vg_name"},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[1] != vgName {
			continue
		}
		sel := "vg_uuid=" + f[0]
		_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{
			Path: "vgchange", Args: []string{"-an", "--select", sel},
			CaptureStdout: true, CaptureStderr: true,
		})
		_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{
			Path: "vgremove", Args: []string{"-fy", "--select", sel},
			CaptureStdout: true, CaptureStderr: true,
		})
	}
}
