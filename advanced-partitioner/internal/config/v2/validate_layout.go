package v2

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/diskresolve"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raidlevel"
)

// sizeRest is the one percentage the builders understand: the rest of the
// disk (a partition: sgdisk -n N:0:0) or of the volume group (an LV:
// lvcreate -l 100%FREE).
const sizeRest = "100%"

// volumeSizeRE is the fixed-size grammar sgdisk and lvcreate agree on: a
// number and a binary unit, also written as "GB" or "GiB" and in any case.
// A bare number is sectors to sgdisk and MiB to lvcreate, so it is not in.
// A fraction is read correctly by lvcreate; sgdisk silently makes a wrong
// partition of it.
var volumeSizeRE = regexp.MustCompile(`(?i)^[0-9]+(\.[0-9]+)?[KMGT](IB|B)?$`)

// isBootMount reports whether a mountpoint is one the partition builder lays
// down itself, ahead of every other partition, for an os set.
func isBootMount(mountpoint string) bool {
	return mountpoint == "/boot" || mountpoint == "/boot/efi"
}

// validateLayout refuses volume layouts partition.BuildLayout and lvm.Build
// cannot lay down. A size=100% partition is created with `sgdisk -n N:0:0`
// and takes the rest of the disk, so any partition the builder creates after
// it — a later direct partition, or the rest-of-disk storage partition it
// adds for lvm volumes — finds no space and the install fails after the disk
// was zapped. The os set's ESP and /boot are created first whatever their JSON
// position, so they need a fixed size. Direct partitions need a single
// un-RAIDed disk, and one LV per volume group may take 100%FREE.
func validateLayout(idx int, s *Set, r raidlevel.Raid) error {
	ref := setRef(idx, s.Name)
	var generic []int
	var lvmNames []string
	lvmRest := 0
	for j, v := range s.Volumes {
		switch {
		case v.Provider == "lvm":
			lvmNames = append(lvmNames, strconv.Quote(v.Name))
			if v.Size == sizeRest {
				lvmRest++
			}
		case v.Provider != "partition":
		case isBootMount(v.Mountpoint):
			if s.Role == "os" && v.Size == sizeRest {
				return fmt.Errorf(
					"config_validation: %s: the %s partition needs a fixed size, not 100%%: the builder creates it ahead of every other partition",
					volRef(idx, s.Name, j, v.Name), v.Mountpoint)
			}
		default:
			generic = append(generic, j)
		}
	}
	if lvmRest > 1 {
		return fmt.Errorf("config_validation: %s: at most one lvm volume per set may use size=100%%", ref)
	}
	if len(generic) > 0 && r != raidlevel.RaidNone {
		return fmt.Errorf(
			"config_validation: %s: direct partition volumes require raid=none (volume %q is provider=partition; use provider=lvm on a raid set)",
			ref, s.Volumes[generic[0]].Name)
	}
	for k, j := range generic {
		v := s.Volumes[j]
		vref := volRef(idx, s.Name, j, v.Name)
		switch {
		case v.Size != sizeRest:
		case k < len(generic)-1:
			return fmt.Errorf(
				"config_validation: %s: size=100%% takes the rest of the disk, but direct partition %q is created after it; only the last direct partition may use 100%%",
				vref, s.Volumes[generic[k+1]].Name)
		case len(lvmNames) > 0:
			return fmt.Errorf(
				"config_validation: %s: size=100%% takes the rest of the disk, but lvm volume(s) %s need a storage partition after the direct partitions; give %q a fixed size",
				vref, strings.Join(lvmNames, ", "), v.Name)
		}
	}
	return nil
}

// validateMountpoints refuses a data set carrying the os set's boot chain or
// root (partition.BuildLayout creates /boot and /boot/efi on the os set's
// disks only, so on a data set they used to vanish without a word) and a
// mountpoint declared by two volumes, within a set or across sets (the second
// mount hides the first; for /boot/efi the builder formats only the first).
// swap is not a mountpoint and may repeat. Every builder compares mountpoints
// as exact strings, so a mountpoint that is not canonical is warned about.
func (c *Config) validateMountpoints() error {
	seen := map[string]string{} // mountpoint -> its first owner
	for i := range c.Sets {
		s := &c.Sets[i]
		for j, v := range s.Volumes {
			ref := volRef(i, s.Name, j, v.Name)
			mp := v.Mountpoint
			switch {
			case mp == "swap":
				continue
			case path.Clean(mp) != mp:
				c.warnf("%s: mountpoint %q is not in canonical form; write it as %q", ref, mp, path.Clean(mp))
			case s.Role == "data" && isBootMount(mp):
				return fmt.Errorf(
					"config_validation: %s: a data set cannot carry %s: the builder creates /boot and /boot/efi on the os set's disks only (declare it on the os set)",
					ref, mp)
			case s.Role == "data" && mp == "/":
				return fmt.Errorf(
					"config_validation: %s: a data set cannot be mounted at \"/\": the root filesystem belongs to the os set (or, on a memboot node, the RAM root)",
					ref)
			}
			owner := fmt.Sprintf("set %q volume %q (sets[%d].volumes[%d])", s.Name, v.Name, i, j)
			if prev, dup := seen[mp]; dup {
				return fmt.Errorf(
					"config_validation: mountpoint %q is declared twice: %s and %s; each mountpoint may be used once",
					mp, prev, owner)
			}
			seen[mp] = owner
		}
	}
	return nil
}

// checkVolumeSize enforces that every disk-backed volume has a size the
// builders accept. lvm volumes used to be allowed without one, and lvm.Build
// then failed "lv <name> has no size" after the disks were wiped. The value
// is handed verbatim to `sgdisk -n N:0:+<size>` or `lvcreate -L <size>`.
func (c *Config) checkVolumeSize(ref string, v *Volume) error {
	if v.Size == "" {
		return fmt.Errorf(
			"config_validation: %s: size is required for provider=%s (a fixed size such as 20G, or \"100%%\" for the rest of the %s)",
			ref, v.Provider, map[string]string{"lvm": "volume group", "partition": "disk"}[v.Provider])
	}
	if v.Size == sizeRest {
		return nil
	}
	if !volumeSizeRE.MatchString(v.Size) {
		return fmt.Errorf(
			"config_validation: %s: size %q cannot be built: write a number with a K, M, G or T suffix (600M, 20G; 1.5G on lvm only), or \"100%%\" for the rest; other forms make sgdisk create a wrong partition (\"50%%\" becomes 50 sectors) or lvcreate fail after the disks were wiped",
			ref, v.Size)
	}
	if v.Provider == "partition" && strings.Contains(v.Size, ".") {
		return fmt.Errorf(
			"config_validation: %s: size %q has a fraction, which sgdisk silently turns into a wrong partition (+1.5G makes a 1-sector partition, +0.5G takes the whole disk, both with exit 0); write it in a smaller unit, e.g. 1536M",
			ref, v.Size)
	}
	n, err := diskresolve.ParseSize(v.Size)
	if err != nil {
		return fmt.Errorf("config_validation: %s: %v", ref, err)
	}
	if n == 0 {
		return fmt.Errorf("config_validation: %s: size %q must be greater than zero", ref, v.Size)
	}
	return nil
}

// checkMatch reads the discover filter with the resolver's own parser
// (diskresolve.ParseMatch): a size it cannot read, or a range no disk can
// fall into, resolves no disk on every boot, so the operator is told here.
func (c *Config) checkMatch(ref string, m *Match) {
	f, err := diskresolve.ParseMatch(m.Tags, m.MinSize, m.MaxSize, m.Model)
	switch {
	case err != nil:
		c.warnf("%s: match.%v, so no disk can match", ref, err)
	case f.MinSize > 0 && f.MaxSize > 0 && f.MinSize > f.MaxSize:
		c.warnf("%s: match.min_size %q is larger than match.max_size %q, so no disk can match", ref, m.MinSize, m.MaxSize)
	}
}
