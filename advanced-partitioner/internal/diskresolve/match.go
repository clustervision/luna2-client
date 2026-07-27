package diskresolve

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
)

// Match is the selection=discover filter for a set. A disk qualifies when it
// carries every listed tag, falls within [MinSize, MaxSize] (a zero bound
// means "no bound"), and — when Model is set — its model string contains
// Model as a substring. The zero Match matches every eligible disk.
type Match struct {
	Tags    []string
	MinSize int64
	MaxSize int64
	Model   string
}

// Matches reports whether a single device satisfies the filter.
func (m Match) Matches(d discover.Device) bool {
	for _, t := range m.Tags {
		if !d.HasTag(t) {
			return false
		}
	}
	if m.MinSize > 0 && d.Size < m.MinSize {
		return false
	}
	if m.MaxSize > 0 && d.Size > m.MaxSize {
		return false
	}
	if m.Model != "" && !strings.Contains(strings.ToLower(d.Model), strings.ToLower(m.Model)) {
		return false
	}
	return true
}

// sizeRE accepts both the v1.4 single-letter form ("8T", "400G", "512M",
// "100K", "2048B") and the legacy two-letter form ("8TB", "400GB"). The
// suffix is case-insensitive; the bare unit "B" or no unit means bytes.
var sizeRE = regexp.MustCompile(`^([0-9]+)\s*([TGMK]?)B?$`)

// ParseSize converts a size string into a byte count using binary units
// (1T = 1024^4 bytes). It tolerates a leading/trailing space and an
// optional trailing "B". Salvaged and extended from intent.ParseSize.
func ParseSize(raw string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	m := sizeRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("cannot parse size %q; use a form like 400G, 8T, 512M", raw)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse size %q: %w", raw, err)
	}
	switch m[2] {
	case "T":
		return n * 1024 * 1024 * 1024 * 1024, nil
	case "G":
		return n * 1024 * 1024 * 1024, nil
	case "M":
		return n * 1024 * 1024, nil
	case "K":
		return n * 1024, nil
	default: // "" — bytes
		return n, nil
	}
}

// ParseMatch builds a Match from raw config strings. MinSize/MaxSize are
// parsed via ParseSize; empty strings leave the bound at zero. tags and
// model are passed through.
func ParseMatch(tags []string, minSize, maxSize, model string) (Match, error) {
	m := Match{Tags: tags, Model: model}
	if minSize != "" {
		v, err := ParseSize(minSize)
		if err != nil {
			return Match{}, fmt.Errorf("min_size: %w", err)
		}
		m.MinSize = v
	}
	if maxSize != "" {
		v, err := ParseSize(maxSize)
		if err != nil {
			return Match{}, fmt.Errorf("max_size: %w", err)
		}
		m.MaxSize = v
	}
	return m, nil
}
