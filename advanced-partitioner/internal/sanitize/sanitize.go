// Package sanitize securely erases local disks for tenant turnover — the
// install_mode=sanitize path. It is the highest-risk destructive module, so
// it is dry-runnable (every command goes through the central executor),
// operates only on fully-qualified disk paths the caller resolved, and logs
// exactly what it did to each disk.
//
// Erase strategy per disk, in order; every step that reports success is
// verified by reading the disk back:
//
//  1. blkdiscard             — discard every block; on SSD/NVMe/thin-provisioned
//     devices this is the fast path. Advisory: a device may claim success and
//     keep the data, or honour only part of the range, hence the read-back of
//     the WHOLE device (a discarded range reads back fast, as zeroes).
//  2. blkdiscard -z          — zero-out: the kernel writes zeroes over the whole
//     device (WRITE ZEROES / WRITE SAME where the device offers it, plain
//     writes otherwise). Works on every block device, including rotational
//     disks and virtio; it takes as long as writing the disk once. Verified by
//     samples (start, middle, end): a write is not advisory.
//  3. neither verified       — the disk is NOT reported erased: signatures and
//     the partition table are still cleared (wipefs -a + sgdisk --zap-all) to
//     limit the damage, and EraseDisk returns an error so the node parks for an
//     operator instead of reporting a false sanitize.
//
// No "-f": util-linux 2.32 (el8) has no --force for blkdiscard ("invalid
// option -- 'f'"), so with it discard was never even attempted on el8 and
// every el8 sanitize fell back to signatures-only. The pre-erase stack
// teardown releases the disks, so the in-use check that -f disables does not
// apply. True crypto-erase (nvme format --ses, hdparm --security-erase) is a
// future enhancement.
package sanitize

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Options configures a Sanitizer.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger
	// DryRun: commands are only logged by the executor, so there is nothing
	// to read back; the first method is reported without verification.
	DryRun bool
	// ReadBack overrides the read-back verification (tests); nil reads the
	// real device.
	ReadBack func(disk string) (bool, error)
	// Progress records every disk erased and verified, so a retry in the same
	// boot skips it; "" keeps no record.
	Progress string
}

// Sanitizer erases disks through the central executor.
type Sanitizer struct {
	runner   executor.Runner
	logger   *txlog.Logger
	dryRun   bool
	readBack func(disk string) (bool, error)
	progress string
	mu       sync.Mutex // serialises the Progress appends of parallel erases
}

// New builds a Sanitizer. Runner is required.
func New(o Options) *Sanitizer {
	return &Sanitizer{runner: o.runner(), logger: o.Logger, dryRun: o.DryRun, readBack: o.ReadBack, progress: o.Progress}
}

// ProgressFile is the Progress file's name in the install runtime dir.
const ProgressFile = "sanitize.progress"

// Disk is one disk to erase. Size is in bytes, 0 when unknown.
type Disk struct {
	Path string
	Size int64
}

// WriteRate is the speed a disk bound assumes: well under what a large HDD
// sustains, so a healthy erase never meets its bound and a wedged one ends.
const WriteRate = 50 * 1000 * 1000 // bytes per second

// UnknownSizeBound bounds the erase of a disk whose size is not known.
const UnknownSizeBound = 24 * time.Hour

// minDiskBound keeps a small disk's bound clear of command start-up costs.
const minDiskBound = 10 * time.Minute

// DiskBound is how long erasing one disk may take: writing it once and
// reading it back whole, both at WriteRate.
func DiskBound(size int64) time.Duration {
	if size <= 0 {
		return UnknownSizeBound
	}
	bound := time.Duration(2*(size/WriteRate+1)) * time.Second
	if bound < minDiskBound {
		return minDiskBound
	}
	return bound
}

func (o Options) runner() executor.Runner { return o.Runner }

// Result records what happened to one disk.
type Result struct {
	Disk        string
	Method      string // "blkdiscard" or "zeroout"
	Discarded   bool   // true when blkdiscard succeeded and read back blank
	Overwritten bool   // true when the zero-out succeeded and read back blank
}

// EraseAll erases every disk at once, each within its own DiskBound, and
// returns the results of the disks that were erased. A disk that cannot be
// erased fails the run once the others have finished; disks recorded in
// Progress by an earlier attempt are not erased again.
func (s *Sanitizer) EraseAll(ctx context.Context, disks []Disk) ([]Result, error) {
	if s.runner == nil {
		return nil, fmt.Errorf("sanitize: runner is required")
	}
	if len(disks) == 0 {
		return nil, fmt.Errorf("sanitize: no disks to erase")
	}
	done := s.erasedEarlier()
	results := make([]Result, len(disks))
	errs := make([]error, len(disks))
	var wg sync.WaitGroup
	for i, d := range disks {
		if method, ok := done[d.Path]; ok {
			results[i] = s.skipErased(d.Path, method)
			continue
		}
		wg.Add(1)
		go func(i int, d Disk) {
			defer wg.Done()
			results[i], errs[i] = s.eraseBounded(ctx, d)
		}(i, d)
	}
	wg.Wait()
	out := make([]Result, 0, len(disks))
	for i, r := range results {
		if errs[i] == nil {
			out = append(out, r)
		}
	}
	return out, errors.Join(errs...)
}

// eraseBounded erases one disk within its DiskBound and records it in
// Progress once it is verified.
func (s *Sanitizer) eraseBounded(ctx context.Context, d Disk) (Result, error) {
	bound := DiskBound(d.Size)
	s.info("%s: may take up to %s", d.Path, bound)
	dctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	r, err := s.eraseDisk(dctx, ctx, d.Path)
	if err == nil && !s.dryRun {
		s.record(r)
	}
	return r, err
}

// erasedEarlier reads the disks a previous attempt erased and verified.
func (s *Sanitizer) erasedEarlier() map[string]string {
	done := map[string]string{}
	if s.progress == "" || s.dryRun {
		return done
	}
	f, err := os.Open(s.progress)
	if err != nil {
		return done
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 2 {
			done[fields[0]] = fields[1]
		}
	}
	return done
}

// skipErased reports a disk erased by an earlier attempt in the words of the
// erase itself, so the sanitize report counts it.
func (s *Sanitizer) skipErased(disk, method string) Result {
	if method == "blkdiscard" {
		s.info("%s: blkdiscard succeeded (blocks discarded, the whole device read back blank) in an earlier attempt", disk)
		return Result{Disk: disk, Method: method, Discarded: true}
	}
	s.info("%s: zero-filled (read-back verified blank) in an earlier attempt", disk)
	return Result{Disk: disk, Method: method, Overwritten: true}
}

func (s *Sanitizer) record(r Result) {
	if s.progress == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.progress, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = fmt.Fprintf(f, "%s %s\n", r.Disk, r.Method)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		s.warn("%s: could not record the erase in %s (%v); a retry erases it again", r.Disk, s.progress, err)
	}
}

// EraseDisk erases a single disk via the layered strategy. An error means the
// disk could not be verified erased (signatures and partition table are
// cleared anyway); the caller must not report the sanitize as done.
func (s *Sanitizer) EraseDisk(ctx context.Context, disk string) (Result, error) {
	return s.eraseDisk(ctx, ctx, disk)
}

// eraseDisk erases within ctx; the damage limiting after a failed erase runs
// under parent, so it still runs when ctx is the disk's expired bound.
func (s *Sanitizer) eraseDisk(ctx, parent context.Context, disk string) (Result, error) {
	if disk == "" {
		return Result{}, fmt.Errorf("sanitize: empty disk path")
	}
	s.info("sanitizing %s", disk)

	// 1. discard, verified
	if _, err := s.runner.Run(ctx, executor.CommandSpec{
		Path: "blkdiscard", Args: []string{disk},
		CaptureStderr: true,
	}); err == nil {
		if s.dryRun {
			s.info("%s: dry-run: would discard, verify by read-back, and zero-fill if needed", disk)
			return Result{Disk: disk, Method: "blkdiscard"}, nil
		}
		// Discard is ADVISORY: a device may report success while the
		// hypervisor/firmware silently ignored the unmap (seen live on
		// qcow2-backed virtio: "blkdiscard succeeded" with the filesystem
		// fully intact — a FALSE SANITIZE, the worst possible outcome for
		// tenant turnover), or honour only part of the range (lab, 12 Oct
		// 2026: the three 1 MiB samples read back zero while data was left
		// next to the middle sample). Trust nothing: read the WHOLE device
		// back and require zeroes before reporting the disk erased.
		if blank, verr := s.readsBackBlankWhole(ctx, disk); verr == nil && blank {
			s.info("%s: blkdiscard succeeded (blocks discarded, the whole device read back blank)", disk)
			return Result{Disk: disk, Method: "blkdiscard", Discarded: true}, nil
		} else if verr != nil {
			s.warn("%s: blkdiscard reported success but read-back verification failed (%v); zero-filling the device", disk, verr)
		} else {
			s.warn("%s: blkdiscard reported success but data persists (the device ignored the discard or honoured only part of it); zero-filling the device", disk)
		}
	} else {
		s.warn("%s: blkdiscard unsupported/failed (%v); zero-filling the device", disk, err)
	}

	// 2. zero-out, verified
	s.info("%s: overwriting the whole device with zeroes (blkdiscard -z); this takes as long as writing the disk once", disk)
	missing := ""
	if _, err := s.runner.Run(ctx, executor.CommandSpec{
		Path: "blkdiscard", Args: []string{"-z", disk},
		CaptureStderr: true,
	}); err == nil {
		if blank, verr := s.readsBackBlank(disk); verr == nil && blank {
			s.info("%s: zero-filled (read-back verified blank)", disk)
			return Result{Disk: disk, Method: "zeroout", Overwritten: true}, nil
		} else if verr != nil {
			s.warn("%s: zero-fill reported success but read-back verification failed (%v)", disk, verr)
		} else {
			s.warn("%s: zero-fill reported success but data persists", disk)
		}
	} else {
		s.warn("%s: zero-fill failed (%v)", disk, err)
		if errors.Is(err, exec.ErrNotFound) {
			missing = "; blkdiscard is not installed in this ramdisk"
		}
	}

	// 3. not erased: limit the damage, then fail closed.
	if ctx.Err() != nil {
		s.warn("%s: the erase did not finish within its bound", disk)
	}
	_, wipeErr := s.runner.Run(parent, executor.CommandSpec{
		Path: "wipefs", Args: []string{"-a", disk},
		CaptureStderr: true,
	})
	_, zapErr := s.runner.Run(parent, executor.CommandSpec{
		Path: "sgdisk", Args: []string{"--zap-all", disk},
		CaptureStderr: true,
	})
	return Result{}, fmt.Errorf("sanitize %s: the device could not be discarded or zero-filled and verified%s; only its signatures and partition table were cleared (wipefs: %v; sgdisk: %v) -- the data is still on the disk", disk, missing, wipeErr, zapErr)
}

// verifySampleMiB is the size of each read-back sample. Three samples
// (start / middle / end) catch both an entirely-ignored discard (superblocks
// and GPT live at the edges) and a partially-honoured one.
const verifySampleMiB = 1

// readBlankFn and readWholeFn are test seams: they default to sampling and
// to reading the whole real block device.
var (
	readBlankFn = readsBackBlankDevice
	readWholeFn = readsBackBlankWholeDevice
)

func (s *Sanitizer) readsBackBlank(disk string) (bool, error) {
	if s.readBack != nil {
		return s.readBack(disk)
	}
	return readBlankFn(disk)
}

func (s *Sanitizer) readsBackBlankWhole(ctx context.Context, disk string) (bool, error) {
	if s.readBack != nil {
		return s.readBack(disk)
	}
	return readWholeFn(ctx, disk, s.info)
}

// wholeReadChunk is the read size of the whole-device verification.
const wholeReadChunk = 8 << 20

// readsBackBlankWholeDevice reads the device from start to end and reports
// whether every byte is zero; the first non-zero offset is logged. Progress is
// logged once a minute (a discarded range reads back at the device's full
// speed; a multi-TB disk still takes minutes).
func readsBackBlankWholeDevice(ctx context.Context, disk string, info func(string, ...any)) (bool, error) {
	f, err := os.Open(disk) //nolint:gosec // fully-qualified disk path resolved by the caller
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return false, err
	}
	buf := make([]byte, wholeReadChunk)
	zero := make([]byte, wholeReadChunk)
	last := time.Now()
	for off := int64(0); off < size; {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, rerr := f.ReadAt(buf, off)
		if n > 0 && !bytes.Equal(buf[:n], zero[:n]) {
			first := 0
			for first < n && buf[first] == 0 {
				first++
			}
			info("%s: read-back found data at offset %d of %d", disk, off+int64(first), size)
			return false, nil
		}
		off += int64(n)
		if rerr == io.EOF || n == 0 {
			break
		}
		if rerr != nil {
			return false, rerr
		}
		if time.Since(last) >= time.Minute {
			info("%s: read-back %d%% (%d of %d MiB)", disk, off*100/size, off>>20, size>>20)
			last = time.Now()
		}
	}
	return true, nil
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
