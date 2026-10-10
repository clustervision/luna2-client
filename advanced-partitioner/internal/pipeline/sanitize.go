package pipeline

import "time"

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
