// Package sanitize securely erases local disks for tenant turnover — the
// install_mode=sanitize path. It is the highest-risk destructive module, so
// it is dry-runnable (every command goes through the central executor),
// operates only on fully-qualified disk paths the caller resolved, and logs
// exactly what it did to each disk.
//
// Erase strategy per disk, in order:
//
//  1. blkdiscard -f          — discard every block; on SSD/NVMe/thin-provisioned
//     devices this is the fast, effectively-secure path.
//  2. wipefs -a + sgdisk --zap-all — fallback when discard is unsupported
//     (e.g. plain rotational disks): clears all filesystem/RAID signatures and
//     the partition table. This removes the layout but is NOT a cryptographic
//     erase; the caller is warned. True crypto-erase (nvme format --ses,
//     hdparm --security-erase) is a future enhancement.
package sanitize

import (
	"context"
	"fmt"
	"io"
	"os"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Options configures a Sanitizer.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger
}

// Sanitizer erases disks through the central executor.
type Sanitizer struct {
	runner executor.Runner
	logger *txlog.Logger
}

// New builds a Sanitizer. Runner is required.
func New(o Options) *Sanitizer {
	return &Sanitizer{runner: o.runner(), logger: o.Logger}
}

func (o Options) runner() executor.Runner { return o.Runner }

// Result records what happened to one disk.
type Result struct {
	Disk      string
	Method    string // "blkdiscard" or "wipe+zap"
	Discarded bool   // true when blkdiscard succeeded (secure on SSD/NVMe)
}

// EraseAll erases every disk and returns the per-disk results. It is
// all-or-nothing only in the sense that a disk that cannot be erased by any
// method is a hard error; disks before it are already erased (sanitize is
// inherently destructive and not re-entrant in the "undo" sense).
func (s *Sanitizer) EraseAll(ctx context.Context, disks []string) ([]Result, error) {
	if s.runner == nil {
		return nil, fmt.Errorf("sanitize: runner is required")
	}
	if len(disks) == 0 {
		return nil, fmt.Errorf("sanitize: no disks to erase")
	}
	out := make([]Result, 0, len(disks))
	for _, d := range disks {
		r, err := s.EraseDisk(ctx, d)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// EraseDisk erases a single disk via the layered strategy.
func (s *Sanitizer) EraseDisk(ctx context.Context, disk string) (Result, error) {
	if disk == "" {
		return Result{}, fmt.Errorf("sanitize: empty disk path")
	}
	s.info("sanitizing %s", disk)

	_, err := s.runner.Run(ctx, executor.CommandSpec{
		Path: "blkdiscard", Args: []string{"-f", disk},
		CaptureStderr: true,
	})
	if err == nil {
		// Discard is ADVISORY: a device may report success while the
		// hypervisor/firmware silently ignored the unmap (seen live on
		// qcow2-backed virtio: "blkdiscard succeeded" with the filesystem
		// fully intact — a FALSE SANITIZE, the worst possible outcome for
		// tenant turnover). Trust nothing: read back samples and require
		// zeroes before reporting the disk erased.
		if blank, verr := s.readsBackBlank(disk); verr == nil && blank {
			s.info("%s: blkdiscard succeeded (blocks discarded, read-back verified blank)", disk)
			return Result{Disk: disk, Method: "blkdiscard", Discarded: true}, nil
		} else if verr != nil {
			s.warn("%s: blkdiscard reported success but read-back verification failed (%v); falling back to wipe+zap", disk, verr)
		} else {
			s.warn("%s: blkdiscard reported success but data persists (advisory discard ignored by the device); falling back to wipe+zap", disk)
		}
	} else {
		s.warn("%s: blkdiscard unsupported/failed (%v); falling back to wipe+zap", disk, err)
	}

	// Fallback: clear signatures + partition table. Best-effort each, but at
	// least one must succeed or the disk is reported as un-erasable.
	_, wipeErr := s.runner.Run(ctx, executor.CommandSpec{
		Path: "wipefs", Args: []string{"-a", disk},
		CaptureStderr: true,
	})
	_, zapErr := s.runner.Run(ctx, executor.CommandSpec{
		Path: "sgdisk", Args: []string{"--zap-all", disk},
		CaptureStderr: true,
	})
	if wipeErr != nil && zapErr != nil {
		return Result{}, fmt.Errorf("sanitize %s: blkdiscard, wipefs, and sgdisk all failed (wipefs: %v; sgdisk: %v)", disk, wipeErr, zapErr)
	}
	s.warn("%s: erased via wipe+zap (signatures + partition table cleared); NOT a cryptographic erase", disk)
	return Result{Disk: disk, Method: "wipe+zap", Discarded: false}, nil
}

// verifySampleMiB is the size of each read-back sample. Three samples
// (start / middle / end) catch both an entirely-ignored discard (superblocks
// and GPT live at the edges) and a partially-honoured one.
const verifySampleMiB = 1

// readBlankFn is a test seam: it defaults to sampling the real block device.
var readBlankFn = readsBackBlankDevice

func (s *Sanitizer) readsBackBlank(disk string) (bool, error) {
	return readBlankFn(disk)
}

func readsBackBlankDevice(disk string) (bool, error) {
	f, err := os.Open(disk) //nolint:gosec // fully-qualified disk path resolved by the caller
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return false, err
	}
	sample := int64(verifySampleMiB) * 1024 * 1024
	if sample > size {
		sample = size
	}
	offsets := []int64{0, size/2 - sample/2, size - sample}
	buf := make([]byte, sample)
	for _, off := range offsets {
		if off < 0 {
			off = 0
		}
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return false, err
		}
		for _, b := range buf {
			if b != 0 {
				return false, nil
			}
		}
	}
	return true, nil
}

func (s *Sanitizer) info(format string, args ...any) {
	if s.logger != nil {
		s.logger.UserInfo(format, args...)
	}
}

func (s *Sanitizer) warn(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(fmt.Sprintf(format, args...))
	}
}
