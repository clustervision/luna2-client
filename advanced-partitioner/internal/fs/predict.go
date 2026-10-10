package fs

import (
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/devices"
)

// PredictFilesystemsFromConfig builds the Filesystems list that lpart-
// storage-check would mount under /sysroot when every set is healthy.
// It is a "compute-only" sibling of Builder.MakeFilesystems — no mkfs,
// no wipefs, just the deterministic device-path mapping that mirrors
// build_filesystems_json in /root/NEWTREE/bin/lpart-storage-check.
//
// Mapping rules:
//
//	memory  → device "tmpfs", FS "tmpfs", carry over Options
//	partition → /dev/<disk>[p]N where N is the number the partition
//	             builder gave the volume (Set.PartitionNumber: [EF02], ESP,
//	             /boot, then the other partition volumes in JSON order)
//	lvm      → /dev/<vg>/<lv>  (the LVM-managed symlink the bash uses;
//	            equivalent to /dev/mapper/<vg>-<lv>)
//
// swap and empty mountpoints are skipped so the mount-tree builder
// never tries to mount them.
//
// The partition counter is anchored at the FIRST resolved disk. For a
// boot-mirroring set (os+raid1) the ESP and /boot live on md arrays, not
// raw partitions, so those volumes resolve to the md device via
// Set.BootMirrorDevice — otherwise the mount-existing path would try to
// mount a raw partition (a linux_raid_member) and fail. LVM volumes
// resolve to their LV regardless of the underlying raid.
//
// biosBoot reports per set whether its first disk leads with a BIOS boot
// partition (storagehealth.HasBIOSBootPartition); nil means none does.
func PredictFilesystemsFromConfig(cfg *v2.Config, biosBoot func(*v2.Set) bool) Filesystems {
	out := Filesystems{}
	for i := range cfg.Sets {
		bb := biosBoot != nil && biosBoot(&cfg.Sets[i])
		out.Filesystems = append(out.Filesystems, PredictFilesystemsForSet(&cfg.Sets[i], bb).Filesystems...)
	}
	return out
}

// PredictFilesystemsForSet is the per-set sibling of
// PredictFilesystemsFromConfig. lpart-storage-prepare's persistent fast-path
// uses it to build a mount tree against an existing layout without
// re-running mkfs.
//
// biosBoot tells whether the set's first disk leads with a BIOS boot partition.
func PredictFilesystemsForSet(set *v2.Set, biosBoot bool) Filesystems {
	out := Filesystems{}
	var diskPath string
	if devs := set.EffectiveDevices(); len(devs) > 0 {
		diskPath = devs[0]
	}

	for _, v := range set.Volumes {
		var device string
		switch v.Provider {
		case "memory":
			device = "tmpfs"
		case "partition":
			// A boot-mirroring set (os+raid1) carries its ESP and /boot on md
			// arrays, not the raw partitions; mounting the raw partition fails
			// (it is a linux_raid_member). Resolve the md device so the
			// mount-existing path remounts the right device.
			if md := set.BootMirrorDevice(v); md != "" {
				device = md
				break
			}
			n := set.PartitionNumber(v, biosBoot)
			if diskPath == "" || n == 0 {
				continue
			}
			device = devices.PartitionPath(diskPath, n)
		case "lvm":
			device = "/dev/" + set.VGName() + "/" + v.Name
		default:
			continue
		}

		if v.Mountpoint == "" || v.Mountpoint == "swap" {
			continue
		}

		options := v.Options
		if options == "" && v.Provider == "memory" {
			options = "defaults"
		}

		out.Filesystems = append(out.Filesystems, Filesystem{
			Mount:          v.Mountpoint,
			Device:         device,
			FS:             v.FS,
			Options:        options,
			ClearUEFINVRAM: v.ClearUEFINVRAM,
		})
	}
	return out
}
