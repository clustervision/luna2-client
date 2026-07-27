// Package raidlevel models the v1.4 per-set RAID topology and its member-count
// rules. It is a zero-dependency leaf package so both internal/config/v2
// (count validation, RAIDMode mapping) and internal/diskresolve / the
// decision engine (resolution, mdadm level) share one source of truth
// without dragging in the discover/executor stack.
package raidlevel

import "fmt"

// Raid is a v1.4 per-set RAID topology. The zero value is not valid; parse
// from config with ParseRaid. "none" means a single disk with no array.
type Raid string

const (
	RaidNone Raid = "none"
	Raid0    Raid = "0"
	Raid1    Raid = "1"
	Raid5    Raid = "5"
	Raid6    Raid = "6"
	Raid10   Raid = "10"
)

// AllRaids is every accepted raid value, in config order.
var AllRaids = []Raid{RaidNone, Raid0, Raid1, Raid5, Raid6, Raid10}

// ParseRaid validates a raw raid string and returns the typed value.
func ParseRaid(s string) (Raid, error) {
	r := Raid(s)
	if !r.Valid() {
		return "", fmt.Errorf("unsupported raid %q (allowed: none, 0, 1, 5, 6, 10)", s)
	}
	return r, nil
}

// Valid reports whether r is one of the accepted raid values.
func (r Raid) Valid() bool {
	switch r {
	case RaidNone, Raid0, Raid1, Raid5, Raid6, Raid10:
		return true
	default:
		return false
	}
}

// MinDisks is the minimum member count for the level. Per the v1.4 spec
// (section 2.2): none=1, 0=2, 1=2, 5=4, 6=5, 10=4.
func (r Raid) MinDisks() int {
	switch r {
	case RaidNone:
		return 1
	case Raid0, Raid1:
		return 2
	case Raid5, Raid10:
		return 4
	case Raid6:
		return 5
	default:
		return 0
	}
}

// MaxDisks is the maximum member count, or 0 for "unbounded" (N). Per the
// spec: none=1, 1=2 (a mirror is exactly two members); 0, 5, 6, 10 are
// unbounded.
func (r Raid) MaxDisks() int {
	switch r {
	case RaidNone:
		return 1
	case Raid1:
		return 2
	default:
		return 0 // unbounded
	}
}

// AllowsCount reports whether n members is valid for this level.
func (r Raid) AllowsCount(n int) bool {
	if n < r.MinDisks() {
		return false
	}
	if max := r.MaxDisks(); max != 0 && n > max {
		return false
	}
	// raid10 (near-2 layout) needs an even member count. Enforced here so
	// an odd count fails at CONFIG VALIDATION, not as a late runtime abort
	// after disks were already selected (FTA D8 — this rule previously
	// lived only in partition.ValidateRAID).
	if r == Raid10 && n%2 != 0 {
		return false
	}
	return true
}

// MDLevel is the mdadm "--level" value, or "" for none (no array).
func (r Raid) MDLevel() string {
	if r == RaidNone {
		return ""
	}
	return string(r)
}

// RAIDMode returns the legacy "raidN"/"none" form consumed by the
// partition/raid/lvm/fs builders (and mirrored by v2.Set.RAIDMode).
func (r Raid) RAIDMode() string {
	if r == RaidNone {
		return "none"
	}
	return "raid" + string(r)
}

// EffectiveCount returns the member count to resolve for a set: the
// operator's count if it is set and valid for the level, otherwise the
// level's minimum. Spares are counted separately.
func EffectiveCount(r Raid, count int) int {
	if count > 0 {
		return count
	}
	return r.MinDisks()
}
