package v2

import (
	"fmt"
	"math"
	"regexp"
	"strconv"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/diskresolve"
)

// sizeRest is the one percentage the builders understand: the rest of the
// disk (a partition: sgdisk -n N:0:0) or of the volume group (an LV:
// lvcreate -l 100%FREE).
const sizeRest = "100%"

// volumeSizeRE is the fixed-size grammar for a volume: a whole number and a
// binary unit. The value is handed verbatim to `sgdisk -n N:0:+<size>` and
// `lvcreate -L <size>`, which disagree on everything else: a bare number is
// sectors to sgdisk and MiB to lvcreate, decimals and "GB" are read
// differently or refused, and any percentage other than 100% is neither.
var volumeSizeRE = regexp.MustCompile(`^([0-9]+)([KMGT])$`)

// parseVolumeSize parses a volume size: sizeRest (rest == true), or a whole
// number of K, M, G or T (binary units) returned in bytes. Every value it
// accepts is also read by diskresolve.ParseSize to the same byte count (the
// partition builder's disk-fit preflight).
func parseVolumeSize(size string) (bytes int64, rest bool, err error) {
	if size == sizeRest {
		return 0, true, nil
	}
	m := volumeSizeRE.FindStringSubmatch(size)
	if m == nil {
		return 0, false, fmt.Errorf(
			"size %q is not valid: use \"100%%\" (the rest of the disk or volume group) or a whole number with a K, M, G or T suffix, e.g. 600M or 20G (no decimals, other percentages, \"GB\" suffixes or bare numbers)",
			size)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("size %q is too large", size)
	}
	if n == 0 {
		return 0, false, fmt.Errorf("size %q must be greater than zero", size)
	}
	var mult int64
	switch m[2] {
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	case "T":
		mult = 1 << 40
	}
	if n > math.MaxInt64/mult {
		return 0, false, fmt.Errorf("size %q is too large", size)
	}
	return n * mult, false, nil
}

// validateVolumeSize enforces that every disk-backed volume has a size the
// builders accept. lvm volumes used to be allowed without one, and lvm.Build
// then failed "lv <name> has no size" after the disks were wiped.
func validateVolumeSize(setIdx int, setName string, volIdx int, v *Volume) error {
	if v.Provider != "partition" && v.Provider != "lvm" {
		return nil
	}
	ref := volRef(setIdx, setName, volIdx, v.Name)
	if v.Size == "" {
		return fmt.Errorf(
			"config_validation: %s: size is required for provider=%s (a fixed size such as 20G, or \"100%%\" for the rest of the %s)",
			ref, v.Provider, restOf(v.Provider))
	}
	if _, _, err := parseVolumeSize(v.Size); err != nil {
		return fmt.Errorf("config_validation: %s: %v", ref, err)
	}
	return nil
}

func restOf(provider string) string {
	if provider == "lvm" {
		return "volume group"
	}
	return "disk"
}

// validateMatch parses the discover filter with the resolver's own parser
// (diskresolve.ParseMatch), so a size the resolver cannot read is refused here
// instead of failing every boot at resolve time, and refuses a range no disk
// can fall into.
func (s *Set) validateMatch(idx int) error {
	if s.Match == nil {
		return nil
	}
	ref := setRef(idx, s.Name)
	m, err := diskresolve.ParseMatch(s.Match.Tags, s.Match.MinSize, s.Match.MaxSize, s.Match.Model)
	if err != nil {
		return fmt.Errorf("config_validation: %s: match.%v", ref, err)
	}
	if m.MinSize > 0 && m.MaxSize > 0 && m.MinSize > m.MaxSize {
		return fmt.Errorf(
			"config_validation: %s: match.min_size %q is larger than match.max_size %q, so no disk can match",
			ref, s.Match.MinSize, s.Match.MaxSize)
	}
	return nil
}
