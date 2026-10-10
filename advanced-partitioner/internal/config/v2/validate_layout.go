package v2

import (
	"fmt"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raidlevel"
)

// isBootMount reports whether a mountpoint is one the partition builder lays
// down itself, ahead of every other partition, for an os set.
func isBootMount(mountpoint string) bool {
	return mountpoint == "/boot" || mountpoint == "/boot/efi"
}

// partitionOrder renders the set's partitions in the order
// partition.BuildLayout creates them, for messages:
//
//	os set:   [EF02] -> /boot/efi -> /boot -> other partition volumes (JSON order)
//	data set: other partition volumes (JSON order)
//	then:     one rest-of-disk storage partition when the set has an lvm
//	          volume (or is a data set without direct partitions).
func (s *Set) partitionOrder() string {
	var parts []string
	add := func(v Volume) { parts = append(parts, fmt.Sprintf("%s (%s, %s)", v.Name, v.Mountpoint, v.Size)) }
	if s.Role == "os" {
		for _, mp := range []string{"/boot/efi", "/boot"} {
			for _, v := range s.Volumes {
				if v.Provider == "partition" && v.Mountpoint == mp {
					add(v)
					break
				}
			}
		}
	}
	generic, lvm := 0, 0
	for _, v := range s.Volumes {
		switch {
		case v.Provider == "lvm":
			lvm++
		case v.Provider == "partition" && !isBootMount(v.Mountpoint):
			generic++
			add(v)
		}
	}
	if lvm > 0 || (s.Role == "data" && generic == 0) {
		parts = append(parts, "lvm storage (rest of disk)")
	}
	return strings.Join(parts, " -> ")
}

// validatePartitionOrder refuses partition layouts partition.BuildLayout
// cannot lay down. A size=100% partition is created with `sgdisk -n N:0:0`
// and takes the rest of the disk, so any partition the builder creates after
// it — a later direct partition, or the rest-of-disk storage partition it
// adds for lvm volumes — finds no space and the install fails after the disk
// was zapped. The os set's ESP and /boot are created first whatever their JSON
// position, so they need a fixed size and never "follow" anything.
func (s *Set) validatePartitionOrder(idx int, r raidlevel.Raid) error {
	ref := setRef(idx, s.Name)
	var generic []int
	var lvmNames []string
	for j, v := range s.Volumes {
		switch {
		case v.Provider == "lvm":
			lvmNames = append(lvmNames, fmt.Sprintf("%q", v.Name))
		case v.Provider == "partition" && isBootMount(v.Mountpoint):
			if s.Role == "os" && v.Size == sizeRest {
				return fmt.Errorf(
					"config_validation: %s: the %s partition needs a fixed size, not 100%%: the builder creates it ahead of every other partition (partition order: %s)",
					volRef(idx, s.Name, j, v.Name), v.Mountpoint, s.partitionOrder())
			}
		case v.Provider == "partition":
			generic = append(generic, j)
		}
	}
	if len(generic) == 0 {
		return nil
	}
	if r != raidlevel.RaidNone {
		return fmt.Errorf(
			"config_validation: %s: direct partition volumes require raid=none (volume %q is provider=partition; use provider=lvm on a raid set)",
			ref, s.Volumes[generic[0]].Name)
	}
	for k, j := range generic {
		v := s.Volumes[j]
		if v.Size != sizeRest {
			continue
		}
		vref := volRef(idx, s.Name, j, v.Name)
		if k < len(generic)-1 {
			return fmt.Errorf(
				"config_validation: %s: size=100%% takes the rest of the disk, but direct partition %q is created after it (partition order: %s); only the last direct partition may use 100%%",
				vref, s.Volumes[generic[k+1]].Name, s.partitionOrder())
		}
		if len(lvmNames) > 0 {
			return fmt.Errorf(
				"config_validation: %s: size=100%% takes the rest of the disk, but lvm volume(s) %s need a storage partition after the direct partitions (partition order: %s); give %q a fixed size",
				vref, strings.Join(lvmNames, ", "), s.partitionOrder(), v.Name)
		}
	}
	return nil
}
