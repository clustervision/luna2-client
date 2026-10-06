// Package fs is the Go port of /root/NEWTREE/lib/filesystems.sh. It
// runs mkfs on the partition / md / lvm devices the earlier modules
// produced and returns a typed Filesystems struct describing every
// filesystem to mount under /sysroot.
//
// Cross-platform notes:
//   - mkfs.xfs, mkfs.ext4, mkswap, and mkfs.fat/mkfs.vfat exist on every
//     supported distro and architecture. The vfat/fat tool name varies
//     a little (Debian historically shipped only mkfs.vfat; newer
//     util-linux on RHEL adds mkfs.fat), so MkfsFatCommand picks whichever
//     is available.
//   - tmpfs is mounted, not formatted; the filesystem entry for a tmpfs
//     volume goes straight to internal/mount with no mkfs step.
package fs

import (
	"context"
	"fmt"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/devices"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
)

// Filesystem is one mountpoint after mkfs has run.
type Filesystem struct {
	Mount   string `json:"mount"`
	Device  string `json:"device"`
	FS      string `json:"fs"`
	Options string `json:"options,omitempty"`
	// ClearUEFINVRAM is propagated through for memory volumes so
	// lpart-bootloader-finalise can act on it without re-reading storage.json.
	ClearUEFINVRAM bool `json:"clear_uefi_nvram,omitempty"`
}

// Filesystems is the typed result of MakeFilesystems.
type Filesystems struct {
	Role         string       `json:"role"`
	FirmwareMode string       `json:"firmware_mode"`
	RAIDMode     string       `json:"raid_mode"`
	Filesystems  []Filesystem `json:"filesystems"`
}

// Options configures a Builder.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger

	// MkfsFatName overrides the FAT tool. Defaults to "mkfs.fat" with a
	// fallback to "mkfs.vfat" probed via CommandAvailable.
	MkfsFatName string

	// CommandAvailable defaults to a "yes" probe; tests override.
	CommandAvailable func(string) bool
}

// Builder builds the filesystems described by the partition + raid + lvm
// state.
type Builder struct {
	o Options
}

// New returns a Builder.
func New(o Options) *Builder {
	if o.CommandAvailable == nil {
		o.CommandAvailable = func(string) bool { return true }
	}
	if o.MkfsFatName == "" {
		switch {
		case o.CommandAvailable("mkfs.fat"):
			o.MkfsFatName = "mkfs.fat"
		case o.CommandAvailable("mkfs.vfat"):
			o.MkfsFatName = "mkfs.vfat"
		default:
			o.MkfsFatName = "mkfs.fat" // best-effort default; build will fail at command time
		}
	}
	return &Builder{o: o}
}

// MakeFilesystems is the public entry. Order:
//
//  1. EFI partition (OS + UEFI): wipefs + mkfs.fat -F 32
//  2. /boot partition (OS): wipefs + mkfs.<bootFS> (-f for xfs, -F for ext4)
//  3. Each direct-partition volume (excluding /boot, /boot/efi): wipefs
//     + mkfs/mkswap
//  4. Each LVM volume: wipefs the LV mapper path + mkfs/mkswap
//
// The Filesystems result carries every entry needed by internal/mount.
// Mirrors lib/filesystems.sh::make_filesystems.
func (b *Builder) MakeFilesystems(ctx context.Context, set *v2.Set, layout partition.Layout, md raid.State, lvmState lvm.State) (Filesystems, error) {
	if b.o.Runner == nil {
		return Filesystems{}, fmt.Errorf("fs.MakeFilesystems: Runner is required")
	}

	out := Filesystems{
		Role:         set.Role,
		FirmwareMode: layout.FirmwareMode,
		RAIDMode:     layout.RAIDMode,
	}

	// EFI partition. Formatted whatever the firmware: the health probe
	// (storagehealth) and the mount-existing prediction (predict.go) both
	// expect a vfat ESP on every OS set that declares one, so skipping it on
	// BIOS would make the next boot classify the set as broken.
	if set.Role == "os" && len(layout.EFIPartitions) > 0 {
		efiDev := preferMD(md.EFIMDDevice, layout.EFIPartitions[0])
		if err := b.wipeAndFormat(ctx, efiDev, "vfat"); err != nil {
			return Filesystems{}, err
		}
		out.Filesystems = append(out.Filesystems, Filesystem{
			Mount: "/boot/efi", Device: efiDev, FS: "vfat",
		})
	}

	// /boot partition.
	if set.Role == "os" && len(layout.BootPartitions) > 0 {
		bootDev := preferMD(md.BootMDDevice, layout.BootPartitions[0])
		bootFS := layout.BootFS
		if bootFS == "" {
			bootFS = "xfs"
		}
		if err := b.wipeAndFormat(ctx, bootDev, bootFS); err != nil {
			return Filesystems{}, err
		}
		out.Filesystems = append(out.Filesystems, Filesystem{
			Mount: "/boot", Device: bootDev, FS: bootFS,
		})
	}

	// Direct-partition volumes.
	for _, pv := range layout.PartitionVolumes {
		if err := b.wipeAndFormat(ctx, pv.Device, pv.FS); err != nil {
			return Filesystems{}, err
		}
		out.Filesystems = append(out.Filesystems, Filesystem{
			Mount: pv.Mount, Device: pv.Device, FS: pv.FS,
		})
	}

	// LVM volumes.
	if lvmState.VGName != "" {
		for _, v := range set.LVMVolumes() {
			dev := devices.VGLVPath(lvmState.VGName, v.Name)
			if err := b.wipeAndFormat(ctx, dev, v.FS); err != nil {
				return Filesystems{}, err
			}
			out.Filesystems = append(out.Filesystems, Filesystem{
				Mount: v.Mountpoint, Device: dev, FS: v.FS,
			})
		}
	}

	return out, nil
}

// MemoryFilesystems returns the typed Filesystems for a memory (tmpfs)
// set. Mirrors lib/json_v2.sh::v2_memory_filesystems_json plus the
// memory branch in bin/lpart-storage-prepare's process_set.
func MemoryFilesystems(set *v2.Set) Filesystems {
	out := Filesystems{
		Role:         set.Role,
		FirmwareMode: "memory",
		RAIDMode:     "none",
	}
	for _, v := range set.MemoryVolumes() {
		opts := v.Options
		if opts == "" {
			opts = "defaults"
		}
		out.Filesystems = append(out.Filesystems, Filesystem{
			Mount: v.Mountpoint, Device: "tmpfs", FS: "tmpfs",
			Options:        opts,
			ClearUEFINVRAM: v.ClearUEFINVRAM,
		})
	}
	return out
}

// wipeAndFormat wipefs's the device then runs the appropriate mkfs.
func (b *Builder) wipeAndFormat(ctx context.Context, dev, fs string) error {
	if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "wipefs", Args: []string{"-af", dev},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("wipefs -af %s: %w", dev, err)
	}
	_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{Path: "udevadm", Args: []string{"settle"}})

	var path string
	var args []string
	switch fs {
	case "xfs":
		path, args = "mkfs.xfs", []string{"-f", dev}
	case "ext4":
		path, args = "mkfs.ext4", []string{"-F", dev}
	case "vfat":
		path, args = b.o.MkfsFatName, []string{"-F", "32", dev}
	case "swap":
		path, args = "mkswap", []string{dev}
	case "tmpfs":
		// tmpfs has no mkfs step; mount does the work.
		return nil
	default:
		return fmt.Errorf("unsupported filesystem: %s", fs)
	}
	if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: path, Args: args,
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("%s %s: %w", path, dev, err)
	}
	return nil
}

// preferMD picks the md device when one is set (mirrored boot/efi) and
// falls back to the first partition otherwise.
func preferMD(mdDevice, partition string) string {
	if mdDevice != "" {
		return mdDevice
	}
	return partition
}
