package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sanitize"
)

// sanitizeTimeout bounds a sanitize run's storage-prepare step. The disks are
// erased in parallel, each within its own sanitize.DiskBound, so the step may
// take the largest of those on top of floor (the normal step bound, for the
// stack teardown before the erase): a disk's own bound always ends first and
// names the disk. With no disk sizes known (lsblk failed) the largest bound is
// sanitize.UnknownSizeBound.
func sanitizeTimeout(disks []disk, floor time.Duration) time.Duration {
	var largest int64
	for _, d := range disks {
		if d.Size > largest {
			largest = d.Size
		}
	}
	return floor + sanitize.DiskBound(largest)
}

// sanitizeReport collects what lpart-storage-prepare reported about the
// erase, from the lines it logs (internal/sanitize, cmd/lpart-storage-prepare):
//
//	sanitize: erasing N local disk(s): [...]
//	<disk>: zero-filled (read-back verified blank)
//	<disk>: blkdiscard succeeded (blocks discarded, read-back verified blank)
//	[sanitize: ]no local disks ... nothing to erase / nothing was erased
type sanitizeReport struct {
	announced int  // N from "erasing N local disk(s)"
	seen      bool // the "erasing N" line was logged
	nothing   bool
	erased    []string // "<disk> (<method>)", in erase order
}

var (
	reSanitizeErasing = regexp.MustCompile(`sanitize: erasing (\d+) local disk\(s\)`)
	reSanitizeZeroed  = regexp.MustCompile(`(\S+): zero-filled \(read-back verified blank\)`)
	reSanitizeDiscard = regexp.MustCompile(`(\S+): blkdiscard succeeded`)
)

func (r *sanitizeReport) observe(line string) {
	switch {
	case reSanitizeErasing.MatchString(line):
		m := reSanitizeErasing.FindStringSubmatch(line)
		if n, err := strconv.Atoi(m[1]); err == nil {
			r.announced, r.seen = n, true
		}
	case reSanitizeZeroed.MatchString(line):
		r.erased = append(r.erased, reSanitizeZeroed.FindStringSubmatch(line)[1]+" (zero-filled)")
	case reSanitizeDiscard.MatchString(line):
		r.erased = append(r.erased, reSanitizeDiscard.FindStringSubmatch(line)[1]+" (discarded)")
	case strings.Contains(line, "nothing to erase") || strings.Contains(line, "nothing was erased"):
		r.nothing = true
	}
}

// summary is the sanitize.done content: a headline, then one line per disk.
// It states only what storage-prepare reported.
func (r *sanitizeReport) summary() string {
	var b strings.Builder
	switch {
	case len(r.erased) > 0:
		count := strconv.Itoa(len(r.erased))
		if r.seen && r.announced != len(r.erased) {
			count = fmt.Sprintf("%d of %d", len(r.erased), r.announced)
		}
		fmt.Fprintf(&b, "%s local disk(s) erased and read-back verified blank:\n", count)
		for _, d := range r.erased {
			b.WriteString("  " + d + "\n")
		}
	case r.nothing:
		b.WriteString("no local disks found -- nothing was erased\n")
	default:
		b.WriteString("lpart-storage-prepare reported no erased disk -- check the install log before reusing this node\n")
	}
	return b.String()
}
