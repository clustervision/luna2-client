// Package vgselect tells the volume group a set owns apart from same-named
// volume groups elsewhere on the node.
//
// The installer names a set's VG after the set ("os", "data"). A disk that is
// not part of the set -- an old OS disk still attached, a disk from another
// node -- can carry a VG of the same name. Every by-name LVM command is then
// either ambiguous (`vgchange -ay os` refuses, so the health probe judged a
// healthy set broken and auto recreated it) or aimed at the wrong disk
// (`vgremove os` removed the foreign VG). The fix is to address VGs by UUID,
// choosing the VG whose physical volumes sit on THIS set's disks.
//
// Only util-linux 2.32 / lvm2 2.03 features are used (el8 install ramdisk):
// `lsblk -ln -o KNAME` and `pvs --noheadings -o vg_uuid,vg_name,pv_name`.
package vgselect

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// VG is one volume group instance.
type VG struct {
	Name string
	UUID string
	// PVs are the physical volume paths as pvs prints them.
	PVs []string
}

// Select is the --select argument that addresses exactly this VG.
func (v VG) Select() string { return "vg_uuid=" + v.UUID }

// Located splits the VGs of one name by where their PVs are.
type Located struct {
	// Own are the VGs with at least one PV on the given disks.
	Own []VG
	// Foreign are the same-named VGs with no PV on the given disks.
	Foreign []VG
}

// EvalSymlinks resolves a PV path (/dev/md/os, /dev/mapper/x) to its kernel
// node; a test seam.
var EvalSymlinks = filepath.EvalSymlinks

// List returns every VG the node's LVM knows, with its PVs, in UUID order.
func List(ctx context.Context, runner executor.Runner) ([]VG, error) {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "pvs", Args: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("pvs: %w", err)
	}
	byUUID := map[string]*VG{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue // an orphan PV (no VG) prints only its name
		}
		vg := byUUID[f[0]]
		if vg == nil {
			vg = &VG{UUID: f[0], Name: f[1]}
			byUUID[f[0]] = vg
		}
		vg.PVs = append(vg.PVs, f[2])
	}
	out := make([]VG, 0, len(byUUID))
	for _, v := range byUUID {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	return out, nil
}

// StackedKernelNames returns the kernel names of every block device on the
// given disks: the disks, their partitions, and what is stacked on them (md
// arrays, dm mappings). lsblk prints a holder under each disk it spans.
func StackedKernelNames(ctx context.Context, runner executor.Runner, disks []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, d := range disks {
		res, err := runner.Run(ctx, executor.CommandSpec{
			Path: "lsblk", Args: []string{"-ln", "-o", "KNAME", d},
			CaptureStdout: true, CaptureStderr: true,
		})
		if err != nil {
			return nil, fmt.Errorf("lsblk %s: %w", d, err)
		}
		for _, line := range strings.Split(string(res.Stdout), "\n") {
			if k := strings.TrimSpace(line); k != "" {
				out[k] = struct{}{}
			}
		}
	}
	return out, nil
}

// kernelName maps a PV path to the kernel name lsblk prints for it.
func kernelName(pv string) string {
	if r, err := EvalSymlinks(pv); err == nil {
		pv = r
	}
	return filepath.Base(pv)
}

// onDisks reports whether any of the VG's PVs is one of the stacked devices.
func (v VG) onDisks(stacked map[string]struct{}) bool {
	for _, pv := range v.PVs {
		if _, ok := stacked[kernelName(pv)]; ok {
			return true
		}
	}
	return false
}

// OnlyOn reports whether EVERY PV of the VG is one of the stacked devices (a
// VG with no known PV, or a missing "[unknown]" PV, is not).
func (v VG) OnlyOn(stacked map[string]struct{}) bool {
	if len(stacked) == 0 || len(v.PVs) == 0 {
		return false
	}
	for _, pv := range v.PVs {
		if _, ok := stacked[kernelName(pv)]; !ok {
			return false
		}
	}
	return true
}

// OnDisks returns every VG (any name) with at least one PV on the disks.
func OnDisks(ctx context.Context, runner executor.Runner, disks []string) ([]VG, error) {
	all, err := List(ctx, runner)
	if err != nil {
		return nil, err
	}
	stacked, err := StackedKernelNames(ctx, runner, disks)
	if err != nil {
		return nil, err
	}
	var out []VG
	for _, v := range all {
		if v.onDisks(stacked) {
			out = append(out, v)
		}
	}
	return out, nil
}

// Locate finds the VGs named name and splits them into the ones on the given
// disks and the foreign ones elsewhere.
func Locate(ctx context.Context, runner executor.Runner, name string, disks []string) (Located, error) {
	all, err := List(ctx, runner)
	if err != nil {
		return Located{}, err
	}
	var named []VG
	for _, v := range all {
		if v.Name == name {
			named = append(named, v)
		}
	}
	if len(named) == 0 {
		return Located{}, nil
	}
	stacked, err := StackedKernelNames(ctx, runner, disks)
	if err != nil {
		return Located{}, err
	}
	var loc Located
	for _, v := range named {
		if v.onDisks(stacked) {
			loc.Own = append(loc.Own, v)
		} else {
			loc.Foreign = append(loc.Foreign, v)
		}
	}
	return loc, nil
}

// DMUUIDPrefix is the device-mapper UUID prefix of this VG's LV mappings:
// LVM sets a mapping's dm uuid to "LVM-" + VG uuid + LV uuid, both without
// their dashes.
func (v VG) DMUUIDPrefix() string { return "LVM-" + strings.ReplaceAll(v.UUID, "-", "") }
