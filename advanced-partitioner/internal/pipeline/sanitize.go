package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sanitizeWriteRate is the zero-fill speed the sanitize step bound assumes:
// well under what a large HDD sustains (~150-250 MB/s), so a healthy erase
// never meets the bound and a wedged one still ends.
const sanitizeWriteRate = 50 * 1000 * 1000 // bytes per second

// sanitizeTimeout bounds a sanitize run's storage-prepare step. The disks are
// zero-filled one after the other, so the bound is their total size at
// sanitizeWriteRate, never below floor (the normal step bound). With no disk
// sizes known (lsblk failed) it returns 0: no bound, rather than a guess that
// kills a long erase part-way and starts it again from the first disk.
func sanitizeTimeout(disks []disk, floor time.Duration) time.Duration {
	var total int64
	for _, d := range disks {
		if d.Size > 0 {
			total += d.Size
		}
	}
	if total == 0 {
		return 0
	}
	est := time.Duration(total/sanitizeWriteRate+1) * time.Second
	if est < floor {
		return floor
	}
	return est
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
