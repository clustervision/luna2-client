package v2

import (
	"fmt"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raidlevel"
)

// SelectorType is one of the device-selector styles the installer supports.
type SelectorType string

const (
	SelectorByID        SelectorType = "by_id_paths"
	SelectorPhysical    SelectorType = "physical_paths" // /dev/disk/by-path/...
	SelectorByUUID      SelectorType = "by_uuid_paths"
	SelectorDevicePaths SelectorType = "device_paths" // bare /dev/sd*, /dev/nvme*, ...
)

func selectorTypeOf(dev string) (SelectorType, error) {
	switch {
	case strings.HasPrefix(dev, "/dev/disk/by-id/"):
		return SelectorByID, nil
	case strings.HasPrefix(dev, "/dev/disk/by-path/"):
		return SelectorPhysical, nil
	case strings.HasPrefix(dev, "/dev/disk/by-uuid/"):
		return SelectorByUUID, nil
	case strings.HasPrefix(dev, "/dev/"):
		return SelectorDevicePaths, nil
	default:
		return "", fmt.Errorf("device path must start with /dev/: %s", dev)
	}
}

// SelectorTypeOf returns the selector style for a single device path.
func SelectorTypeOf(dev string) (SelectorType, error) {
	return selectorTypeOf(dev)
}

// Boot is UEFI only under v1.4 (decisions/0003); these accessors return the
// fixed values the storage stack and bootloader finaliser expect, so the
// downstream builders need no special-casing. The os set's bootloader key is
// the one thing an operator can vary — see Set.BootMethodOrNone and
// Set.BootOrderOrLast.

// BootMethod is the layout-wide default; the per-set Set.BootMethodOrNone is
// what the installer reads.
func (c *Config) BootMethod() string { return "grub" }

// BootMethodOrNone returns "none" when no bootloader is installed — a memory
// (memboot RAM-root) set, or an os set whose bootloader key says none — and
// "grub" otherwise.
func (s *Set) BootMethodOrNone() string {
	if s.StorageType() == StorageMemory || s.Bootloader == BootloaderNone {
		return "none"
	}
	return "grub"
}

// BootOrderOrLast returns where the node's own disk sits in the UEFI boot
// order: "first" only when the os set asks for it, else "last". An absent key
// means last (today's behaviour); an explicit false means none, which has no
// boot order to arrange and so reads as last here too.
func (s *Set) BootOrderOrLast() string {
	if s.Bootloader == BootloaderFirst {
		return string(BootloaderFirst)
	}
	return string(BootloaderLast)
}

// BootFirmware is always "uefi".
func (c *Config) BootFirmware() string { return "uefi" }

// RegisterNVRAM is always "auto" (register the NVRAM entry when UEFI).
func (c *Config) RegisterNVRAM() string { return "auto" }

// OSSet returns the set with role=os, or nil when there is none (a data-only
// disklayout for a memboot node). Validate guarantees at most one.
func (c *Config) OSSet() *Set {
	for i := range c.Sets {
		if c.Sets[i].Role == "os" {
			return &c.Sets[i]
		}
	}
	return nil
}

// ExecutionOrder returns indices into c.Sets in processing order: the OS set
// first, then the remaining sets in their JSON order.
func (c *Config) ExecutionOrder() []int {
	order := make([]int, 0, len(c.Sets))
	for i, s := range c.Sets {
		if s.Role == "os" {
			order = append(order, i)
		}
	}
	for i, s := range c.Sets {
		if s.Role != "os" {
			order = append(order, i)
		}
	}
	return order
}

// EffectiveDevices returns the set's explicit device list (selection=manual).
// selection=discover sets carry no devices until the resolver fills them in.
func (s *Set) EffectiveDevices() []string {
	return s.Devices
}

// RaidLevel returns the typed raid level. It falls back to RaidNone for an
// unparseable value (accessors run after Validate, which rejects bad raids).
func (s *Set) RaidLevel() raidlevel.Raid {
	r, err := raidlevel.ParseRaid(s.Raid)
	if err != nil {
		return raidlevel.RaidNone
	}
	return r
}

// RAIDMode returns the legacy "raidN"/"none" form the partition/raid/lvm/fs
// builders consume, mapped from the flat raid field.
func (s *Set) RAIDMode() string {
	return s.RaidLevel().RAIDMode()
}

// SelectionMode returns the set's selection mode ("discover" or "manual").
func (s *Set) SelectionMode() string { return s.Selection }

// MatchFilter returns the set's discover match filter, or the zero
// Match when none was declared.
func (s *Set) MatchFilter() Match {
	if s.Match == nil {
		return Match{}
	}
	return *s.Match
}

// MemberCount returns the number of disks the set wants: len(devices) for
// manual (spares included), or the ACTIVE count (count, defaulted to the raid
// minimum) for discover, whose spares the resolver takes on top. Either way the
// array's active members are its disks minus SpareCount().
func (s *Set) MemberCount() int {
	if s.Selection == "manual" {
		return len(s.Devices)
	}
	return raidlevel.EffectiveCount(s.RaidLevel(), s.Count)
}

// SpareCount returns the requested hot-spare count.
func (s *Set) SpareCount() int { return s.Spares }

// VGName returns the LVM VG name for the set (the set name under v1.4).
func (s *Set) VGName() string { return s.Name }

// MDName returns the canonical md array name for the set: the set name with
// every non-alphanumeric rune folded to '_', trailing '_' trimmed. The raid
// builder creates "/dev/md/<MDName>" (root), "/dev/md/<MDName>-efi" and
// "-boot" (mirrored os boot); cleanup and the health probe must agree on it.
func (s *Set) MDName() string {
	b := make([]rune, 0, len(s.Name))
	for _, r := range s.Name {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
			b = append(b, r)
		default:
			b = append(b, '_')
		}
	}
	out := string(b)
	for len(out) > 0 && out[len(out)-1] == '_' {
		out = out[:len(out)-1]
	}
	return out
}

// MirrorsBoot reports whether this set mirrors its ESP and /boot onto md
// arrays rather than placing them as raw partitions. Single source for the
// rule: the partition builder, the health probe and the filesystem predictor
// all use it. Every redundant OS RAID mode mirrors the boot chain (raid1, 5, 6
// and 10: a redundant root must still BOOT after a disk loss); raid0 and none
// do not. It used to say raid1 only while the builder mirrored all four, so a
// healthy raid5/6/10 os set was probed at its raw ESP/boot partitions (md
// members), judged broken, and auto re-created it on every boot.
func (s *Set) MirrorsBoot() bool {
	if s.Role != "os" {
		return false
	}
	switch s.RAIDMode() {
	case "raid1", "raid5", "raid6", "raid10":
		return true
	}
	return false
}

// PartitionNumber returns the 1-based GPT partition number the partition
// builder gives a partition-provider volume of this set, or 0 for a volume
// that has no partition of its own (lvm, memory, or a /boot or /boot/efi
// volume of a non-os set). biosBoot tells whether the disk leads with a BIOS
// boot partition (EF02), which the builder creates on a BIOS node for an os set
// with a bootloader. It mirrors partition.BuildLayout exactly:
//
//	os set:   [EF02] -> /boot/efi -> /boot -> other partition volumes (JSON order)
//	data set: other partition volumes (JSON order)
//
// The health probe and the mount-existing predictor used to count partition
// volumes in JSON order instead, which put /boot on the ESP on every BIOS node
// (and on any layout that lists a volume before /boot or /boot/efi).
func (s *Set) PartitionNumber(vol Volume, biosBoot bool) int {
	if vol.Provider != "partition" {
		return 0
	}
	isBootVol := vol.Mountpoint == "/boot" || vol.Mountpoint == "/boot/efi"
	if isBootVol && s.Role != "os" {
		return 0
	}
	n := 0
	hasEFI, hasBoot := false, false
	for _, v := range s.Volumes {
		if v.Provider != "partition" {
			continue
		}
		switch v.Mountpoint {
		case "/boot/efi":
			hasEFI = true
		case "/boot":
			hasBoot = true
		}
	}
	if s.Role == "os" {
		if biosBoot {
			n++
		}
		if hasEFI {
			n++
			if vol.Mountpoint == "/boot/efi" {
				return n
			}
		}
		if hasBoot {
			n++
			if vol.Mountpoint == "/boot" {
				return n
			}
		}
	}
	for _, v := range s.Volumes {
		if v.Provider != "partition" || v.Mountpoint == "/boot" || v.Mountpoint == "/boot/efi" {
			continue
		}
		n++
		if v.Name == vol.Name && v.Mountpoint == vol.Mountpoint {
			return n
		}
	}
	return 0
}

// BootMirrorDevice returns the md device that carries a boot-mirroring set's
// ESP (/boot/efi) or /boot volume — "/dev/md/<MDName>-efi" / "-boot" — or ""
// when the volume is not a mirrored boot partition (so callers fall back to the
// raw partition path). The raid builder places the real vfat/xfs filesystem on
// these md devices; the health probe and the filesystem predictor must agree,
// or a raid1 set's ESP/boot is checked/mounted at the raw partition (which is a
// linux_raid_member) and fails. Single source for that rule.
func (s *Set) BootMirrorDevice(vol Volume) string {
	if !s.MirrorsBoot() || vol.Provider != "partition" {
		return ""
	}
	switch vol.Mountpoint {
	case "/boot/efi":
		return "/dev/md/" + s.MDName() + "-efi"
	case "/boot":
		return "/dev/md/" + s.MDName() + "-boot"
	}
	return ""
}

// StorageType is the high-level layout for a set: memory, lvm, or partition.
type StorageType string

const (
	StorageMemory    StorageType = "memory"
	StorageLVM       StorageType = "lvm"
	StoragePartition StorageType = "partition"
)

// StorageType returns the derived storage layout from the volumes' providers.
// The memory case only arises for the decision engine's internally
// synthesized memboot set; operator configs never validate with memory
// volumes.
func (s *Set) StorageType() StorageType {
	memCount, lvmCount, partCount := 0, 0, 0
	for _, v := range s.Volumes {
		switch v.Provider {
		case "memory":
			memCount++
		case "lvm":
			lvmCount++
		case "partition":
			partCount++
		}
	}
	switch {
	case memCount > 0 && lvmCount == 0 && partCount == 0:
		return StorageMemory
	case lvmCount > 0:
		return StorageLVM
	default:
		return StoragePartition
	}
}

// SelectorType returns the SelectorType of the set's first device, or the
// "memory" sentinel for a memory set with no devices.
func (s *Set) SelectorType() (SelectorType, error) {
	devs := s.EffectiveDevices()
	if len(devs) == 0 {
		if s.StorageType() == StorageMemory {
			return "memory", nil
		}
		return "", fmt.Errorf("no devices to derive selector type from")
	}
	return selectorTypeOf(devs[0])
}

// LVMVolumes returns the LVM-backed volumes in the set.
func (s *Set) LVMVolumes() []Volume {
	out := make([]Volume, 0, len(s.Volumes))
	for _, v := range s.Volumes {
		if v.Provider == "lvm" {
			out = append(out, v)
		}
	}
	return out
}

// PartitionVolumes returns the direct-partition volumes in the set.
func (s *Set) PartitionVolumes() []Volume {
	out := make([]Volume, 0, len(s.Volumes))
	for _, v := range s.Volumes {
		if v.Provider == "partition" {
			out = append(out, v)
		}
	}
	return out
}

// MemoryVolumes returns the memory (tmpfs) volumes in the set (memboot only).
func (s *Set) MemoryVolumes() []Volume {
	out := make([]Volume, 0, len(s.Volumes))
	for _, v := range s.Volumes {
		if v.Provider == "memory" {
			out = append(out, v)
		}
	}
	return out
}

// HasRootVolume reports whether this set provides the root filesystem.
func (s *Set) HasRootVolume() bool {
	for _, v := range s.Volumes {
		if v.Mountpoint == "/" {
			return true
		}
	}
	return false
}

// ClearUEFINVRAM reports whether the set's RAM root requests clearing UEFI
// NVRAM entries before bootloader install (memboot only).
func (s *Set) ClearUEFINVRAM() bool {
	for _, v := range s.Volumes {
		if v.ClearUEFINVRAM && v.Provider == "memory" && v.FS == "tmpfs" && v.Mountpoint == "/" {
			return true
		}
	}
	return false
}

// BootEFISize returns the size of the /boot/efi partition (default "600M").
func (s *Set) BootEFISize() string {
	for _, v := range s.Volumes {
		if v.Provider == "partition" && v.Mountpoint == "/boot/efi" && v.Size != "" {
			return v.Size
		}
	}
	return "600M"
}

// BootSize returns the size of the /boot partition (default "1G").
func (s *Set) BootSize() string {
	for _, v := range s.Volumes {
		if v.Provider == "partition" && v.Mountpoint == "/boot" && v.Size != "" {
			return v.Size
		}
	}
	return "1G"
}

// BootFS returns the /boot filesystem (default "xfs").
func (s *Set) BootFS() string {
	for _, v := range s.Volumes {
		if v.Provider == "partition" && v.Mountpoint == "/boot" && v.FS != "" {
			return v.FS
		}
	}
	return "xfs"
}

// (The hands-off "persistent" flag is the exported Set.Persistent field;
// the decision engine, lpart-storage-check, and lpart-osimage-install key off it.)
