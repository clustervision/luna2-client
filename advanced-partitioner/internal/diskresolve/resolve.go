package diskresolve

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
)

// DefaultRAIDTolerancePercent is the default size spread allowed between
// members of one array. Salvaged from intent.DefaultRAIDTolerancePercent.
const DefaultRAIDTolerancePercent = 5

// Resolve selects the disks for one selection=discover set. It filters the pool
// by the match filter, sorts the survivors smallest-first, takes count+spares
// of them, and (when more than one disk is chosen) checks they are within
// tolerancePct of the smallest. A tolerancePct of 0 means "use the default".
//
// It returns the chosen devices (smallest-first). The caller removes them
// from the pool before resolving the next set.
func Resolve(pool []discover.Device, m Match, count, spares, tolerancePct int) ([]discover.Device, error) {
	if count < 1 {
		return nil, fmt.Errorf("diskresolve: count must be >= 1, got %d", count)
	}
	if spares < 0 {
		return nil, fmt.Errorf("diskresolve: spares must be >= 0, got %d", spares)
	}
	if tolerancePct <= 0 {
		tolerancePct = DefaultRAIDTolerancePercent
	}
	need := count + spares

	candidates := filter(pool, m)
	if len(candidates) < need {
		return nil, notEnoughError(pool, candidates, m, need)
	}
	selected := candidates[:need]

	if need > 1 {
		sizes := make([]int64, 0, len(selected))
		for _, d := range selected {
			sizes = append(sizes, d.Size)
		}
		if err := CheckRAIDTolerance(sizes, tolerancePct); err != nil {
			return nil, toleranceError(selected, tolerancePct)
		}
	}
	return selected, nil
}

// filter applies a Match to a pool and returns survivors sorted smallest-first,
// equal sizes by the disk's stable identity (WWN / serial / by-path slot), NOT
// by kernel name: kernel names follow enumeration order, so equal disks used
// to swap between sets whenever enumeration changed (a disk added, a slow
// controller), and auto then saw another set's disks as "broken" and wiped
// them.
func filter(pool []discover.Device, m Match) []discover.Device {
	out := make([]discover.Device, 0, len(pool))
	for _, d := range pool {
		if m.Matches(d) {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Size != out[j].Size {
			return out[i].Size < out[j].Size
		}
		return out[i].IdentityKey() < out[j].IdentityKey()
	})
	return out
}

// CheckRAIDTolerance returns nil if every size is within tolerancePct of the
// smallest. Salvaged verbatim from intent.CheckRAIDTolerance.
func CheckRAIDTolerance(sizes []int64, tolerancePct int) error {
	if len(sizes) == 0 {
		return nil
	}
	smallest := sizes[0]
	for _, s := range sizes[1:] {
		if s < smallest {
			smallest = s
		}
	}
	if smallest <= 0 {
		return fmt.Errorf("invalid smallest size: %d", smallest)
	}
	for _, s := range sizes {
		diff := (s - smallest) * 100 / smallest
		if diff > int64(tolerancePct) {
			return fmt.Errorf("device exceeds %d%% size tolerance", tolerancePct)
		}
	}
	return nil
}

func notEnoughError(pool, candidates []discover.Device, m Match, need int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "disk match unsatisfied: need %d device(s)", need)
	if len(m.Tags) > 0 {
		fmt.Fprintf(&b, " tags=%s", strings.Join(m.Tags, ","))
	}
	if m.MinSize > 0 || m.MaxSize > 0 {
		fmt.Fprintf(&b, " size=%s-%s", gibStr(m.MinSize), gibStr(m.MaxSize))
	}
	if m.Model != "" {
		fmt.Fprintf(&b, " model~=%q", m.Model)
	}
	fmt.Fprintf(&b, ", but only %d eligible device(s) match.", len(candidates))
	if len(pool) > 0 {
		b.WriteString(" Available pool:")
		for _, d := range pool {
			fmt.Fprintf(&b, " %s(%s,%s)", d.Path, gibStr(d.Size), strings.Join(d.Tags, "+"))
		}
	}
	return fmt.Errorf("%s", b.String())
}

func toleranceError(selected []discover.Device, tolerancePct int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "raid: devices exceed %d%% size tolerance.", tolerancePct)
	smallest := selected[0].Size
	for _, d := range selected {
		if d.Size < smallest {
			smallest = d.Size
		}
	}
	b.WriteString("\n  Selected candidates (smallest first):")
	for _, d := range selected {
		pct := int64(0)
		if smallest > 0 {
			pct = (d.Size - smallest) * 100 / smallest
		}
		fmt.Fprintf(&b, "\n    %s  %s  %d%% variance", d.Path, gibStr(d.Size), pct)
	}
	b.WriteString("\n  Widen match.min_size/max_size or raise the raid tolerance.")
	return fmt.Errorf("%s", b.String())
}

// gibStr renders a byte count as a whole-GiB string; "0" means unbounded.
func gibStr(bytes int64) string {
	if bytes <= 0 {
		return "0"
	}
	return fmt.Sprintf("%dG", bytes/(1024*1024*1024))
}
