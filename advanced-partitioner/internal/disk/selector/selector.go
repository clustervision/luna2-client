// Package selector validates and resolves operator-supplied device paths
// (e.g. /dev/sda, /dev/disk/by-id/..., /dev/disk/by-path/..., /dev/disk/by-uuid/...)
// into the typed records that lpart-storage-prepare carries around in
// resolved-set.json. It is the Go port of /root/NEWTREE/lib/disk_selectors.sh
// — the only place in the installer where a string device path is turned
// into a confirmed-block-device + metadata tuple.
//
// Cross-distribution notes:
//   - lsblk, findmnt, and the /dev/disk/by-* udev trees are identical
//     across RHEL 8/9/10, Rocky, Alma, Ubuntu, Debian, and arm64 Linux.
//   - The resolver does NOT shell out for "is this a block device?" —
//     it stat()s the resolved path so the test suite can drive it with
//     regular files in t.TempDir() and bypass the device check via the
//     injectable IsBlockDevice predicate.
package selector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Resolved is one device after path validation + metadata capture. Field
// names match the keys lib/disk_selectors.sh writes into the resolved-set
// JSON (configured_path, resolved_path, selector_type, size_bytes,
// model, serial) so downstream Go code can produce byte-equivalent JSON.
type Resolved struct {
	ConfiguredPath string          `json:"configured_path"`
	ResolvedPath   string          `json:"resolved_path"`
	SelectorType   v2.SelectorType `json:"selector_type"`
	SizeBytes      int64           `json:"size_bytes"`
	Model          string          `json:"model"`
	Serial         string          `json:"serial"`
}

// ErrorKind classifies a resolution failure for the caller's exit contract.
type ErrorKind int

const (
	// KindProbe: a probe command failed (lsblk error): environmental, may
	// clear on retry.
	KindProbe ErrorKind = iota
	// KindNotFound: the configured device does not exist on this node.
	KindNotFound
	// KindInvalid: the device exists but cannot be used as configured (not
	// a whole disk, read-only, malformed path): deterministic.
	KindInvalid
	// KindInUse: the disk (or something on it) is mounted or active swap.
	KindInUse
)

// ErrNotFound is matched (errors.Is) by a KindNotFound ResolveError.
var ErrNotFound = errors.New("device not found")

// ResolveError is every failure Resolve returns; Kind tells a missing disk
// from a deterministic misconfiguration and from a transient probe failure.
type ResolveError struct {
	Kind ErrorKind
	// Device is the configured path the error is about.
	Device string
	Msg    string
}

func (e *ResolveError) Error() string { return e.Msg }

// Is makes errors.Is(err, ErrNotFound) true for a missing device.
func (e *ResolveError) Is(target error) bool { return target == ErrNotFound && e.Kind == KindNotFound }

func resolveErr(kind ErrorKind, device, format string, args ...any) error {
	return &ResolveError{Kind: kind, Device: device, Msg: fmt.Sprintf(format, args...)}
}

// Options configures a Resolver. Tests typically pin every Dir to a
// t.TempDir() with synthetic symlinks.
type Options struct {
	// Runner runs lsblk + findmnt. Required.
	Runner executor.Runner

	// Logger receives the "device_paths is lab-use only" warning and
	// any operator-visible info. nil is OK.
	Logger *txlog.Logger

	// CandidatesOut is where the "device not found" diagnostic dump
	// goes — typically os.Stderr. nil suppresses the dump but still
	// returns the structured error.
	CandidatesOut io.Writer

	// Candidate directories listed when a path is missing. Defaults
	// follow the udev convention.
	ByIDDir   string
	ByPathDir string
	ByUUIDDir string

	// IsBlockDevice answers "is this resolved path a block device?".
	// Defaults to a real os.Stat-based check; tests inject a stub
	// because t.TempDir() can't host /dev nodes.
	IsBlockDevice func(string) bool
}

// Resolver is the public type. Construct via New.
type Resolver struct {
	o Options
}

// New returns a Resolver populated with sensible defaults for any
// unset Options field.
func New(o Options) *Resolver {
	if o.ByIDDir == "" {
		o.ByIDDir = "/dev/disk/by-id"
	}
	if o.ByPathDir == "" {
		o.ByPathDir = "/dev/disk/by-path"
	}
	if o.ByUUIDDir == "" {
		o.ByUUIDDir = "/dev/disk/by-uuid"
	}
	if o.IsBlockDevice == nil {
		o.IsBlockDevice = defaultIsBlockDevice
	}
	return &Resolver{o: o}
}

// defaultIsBlockDevice uses os.Lstat after EvalSymlinks; a real block
// device has ModeDevice set and ModeCharDevice unset.
func defaultIsBlockDevice(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0
}

// ResolveAll validates and resolves a slice of device paths. The first
// failure short-circuits the rest; all error strings are prefixed with
// "device_resolution:" so lpart-storage-prepare can produce uniform output.
func (r *Resolver) ResolveAll(ctx context.Context, paths []string) ([]Resolved, error) {
	out := make([]Resolved, 0, len(paths))
	for _, p := range paths {
		rec, err := r.Resolve(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Resolve validates one device path and returns its Resolved record.
//
// Mirrors _resolve_selector_paths in lib/disk_selectors.sh step by step:
//
//  1. Derive selector type from the path prefix.
//  2. Emit the "device_paths is lab-use only" warning when applicable.
//  3. Stat the path; if missing, write the candidate dump and fail.
//  4. EvalSymlinks; the symlink must resolve to a real block device.
//  5. Call lsblk to confirm the resolved path is a whole disk.
//  6. Refuse if findmnt sees the disk mounted.
//  7. Refuse if lsblk reports RO=1.
//  8. Capture SIZE / MODEL / SERIAL in one combined lsblk call.
func (r *Resolver) Resolve(ctx context.Context, configured string) (Resolved, error) {
	selType, err := v2.SelectorTypeOf(configured)
	if err != nil {
		return Resolved{}, resolveErr(KindInvalid, configured, "device_resolution: %v", err)
	}

	if selType == v2.SelectorDevicePaths && r.o.Logger != nil {
		r.o.Logger.Warn("device_paths selector is intended for lab/test use only")
	}

	// Existence check (via Lstat — a dangling symlink also fails this).
	if _, err := os.Lstat(configured); err != nil {
		return Resolved{}, r.notFoundError(ctx, configured, selType, "devices[]")
	}

	resolved, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return Resolved{}, r.notFoundError(ctx, configured, selType,
			fmt.Sprintf("devices[] (resolve symlink: %v)", err))
	}

	if !r.o.IsBlockDevice(resolved) {
		return Resolved{}, r.notFoundError(ctx, configured, selType,
			fmt.Sprintf("devices[] (resolved: %s)", resolved))
	}

	// Whole-disk check via lsblk -dn -o TYPE.
	typ, err := r.lsblkScalar(ctx, resolved, "TYPE", false)
	if err != nil {
		return Resolved{}, resolveErr(KindProbe, configured, "device_resolution: lsblk TYPE %s: %v", resolved, err)
	}
	if typ != "disk" {
		return Resolved{}, resolveErr(KindInvalid, configured,
			"device_resolution: selected path is not a whole disk: %s -> %s (type=%q)",
			configured, resolved, typ)
	}

	// In-use check over the WHOLE disk subtree: `lsblk -o MOUNTPOINT`
	// lists the disk plus every descendant (partitions, array holders,
	// LVs layered on partitions), so a mounted partition or a mounted LV
	// on this disk refuses the disk - the old `findmnt -S <whole-disk>`
	// probe saw only whole-disk mounts and happily passed a disk whose
	// PARTITION held a live filesystem (ancestor FTA BE-C2). FAIL-CLOSED:
	// a probe error refuses the disk, because "cannot tell whether it is
	// in use" must never authorise destruction (BE-C1: the old code read
	// findmnt failure as not-mounted).
	mounted, mountErr := r.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "lsblk",
		Args:          []string{"-ln", "-o", "MOUNTPOINT", resolved},
		CaptureStdout: true, CaptureStderr: true,
	})
	if mountErr != nil {
		return Resolved{}, resolveErr(KindProbe, configured,
			"device_resolution: cannot determine whether %s is in use (lsblk MOUNTPOINT: %v) - refusing (fail-closed)",
			resolved, mountErr)
	}
	for _, line := range strings.Split(string(mounted.Stdout), "\n") {
		mp := strings.TrimSpace(line)
		if mp == "" {
			continue
		}
		if mp == "[SWAP]" {
			return Resolved{}, resolveErr(KindInUse, configured,
				"device_resolution: selected disk %s (or a partition on it) is active swap - refusing", resolved)
		}
		return Resolved{}, resolveErr(KindInUse, configured,
			"device_resolution: selected disk %s (or a partition/volume on it) is mounted at %s - refusing",
			resolved, mp)
	}

	// Read-only check.
	ro, err := r.lsblkScalar(ctx, resolved, "RO", false)
	if err != nil {
		return Resolved{}, resolveErr(KindProbe, configured, "device_resolution: lsblk RO %s: %v", resolved, err)
	}
	if ro != "0" {
		return Resolved{}, resolveErr(KindInvalid, configured, "device_resolution: selected disk is read-only: %s", resolved)
	}

	rec := Resolved{
		ConfiguredPath: configured,
		ResolvedPath:   resolved,
		SelectorType:   selType,
	}

	sizeStr, err := r.lsblkScalar(ctx, resolved, "SIZE", true)
	if err != nil {
		return Resolved{}, resolveErr(KindProbe, configured, "device_resolution: lsblk SIZE %s: %v", resolved, err)
	}
	if _, err := fmt.Sscanf(sizeStr, "%d", &rec.SizeBytes); err != nil {
		return Resolved{}, resolveErr(KindProbe, configured,
			"device_resolution: lsblk SIZE %s: unexpected output %q: %v",
			resolved, sizeStr, err)
	}

	if rec.Model, err = r.lsblkScalar(ctx, resolved, "MODEL", false); err != nil {
		return Resolved{}, resolveErr(KindProbe, configured, "device_resolution: lsblk MODEL %s: %v", resolved, err)
	}
	if rec.Serial, err = r.lsblkScalar(ctx, resolved, "SERIAL", false); err != nil {
		return Resolved{}, resolveErr(KindProbe, configured, "device_resolution: lsblk SERIAL %s: %v", resolved, err)
	}

	return rec, nil
}

// lsblkScalar runs `lsblk -dn [-b] -o <col> <dev>` and returns the trimmed
// scalar value. `bytes` adds -b for raw byte counts.
func (r *Resolver) lsblkScalar(ctx context.Context, dev, col string, bytesMode bool) (string, error) {
	args := []string{"-dn"}
	if bytesMode {
		args = append(args, "-b")
	}
	args = append(args, "-o", col, dev)
	res, err := r.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "lsblk",
		Args:          args,
		CaptureStdout: true,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// notFoundError writes the structured candidate dump (matching the
// _selector_device_not_found_error helper in lib/disk_selectors.sh) to
// the configured stderr writer and returns a structured error.
func (r *Resolver) notFoundError(ctx context.Context, requested string, selType v2.SelectorType, jsonLabel string) error {
	if r.o.CandidatesOut != nil {
		w := r.o.CandidatesOut
		emit(w, "ERROR: device not found")
		emit(w, fmt.Sprintf("  requested:     %s", requested))
		emit(w, fmt.Sprintf("  selector_type: %s", selType))
		emit(w, fmt.Sprintf("  json_path:     %s", jsonLabel))
		writeCandidates(w, "by-id", r.o.ByIDDir)
		writeCandidates(w, "by-path", r.o.ByPathDir)
		writeCandidates(w, "by-uuid", r.o.ByUUIDDir)
		r.writeLsblkCandidates(ctx, w)
	}
	return resolveErr(KindNotFound, requested,
		"device_resolution: device not found: %s (selector=%s, json=%s)",
		requested, selType, jsonLabel)
}

func emit(w io.Writer, s string) {
	_, _ = fmt.Fprintln(w, s)
}

func writeCandidates(w io.Writer, label, dir string) {
	emit(w, fmt.Sprintf("  candidates %s:", label))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		if errors.Is(err, os.ErrNotExist) || len(entries) == 0 {
			emit(w, "    (none)")
			return
		}
		emit(w, "    (unreadable)")
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		emit(w, fmt.Sprintf("    %s", n))
	}
}

func (r *Resolver) writeLsblkCandidates(ctx context.Context, w io.Writer) {
	emit(w, "  lsblk disks:")
	if r.o.Runner == nil {
		emit(w, "    (no runner)")
		return
	}
	res, err := r.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "lsblk",
		Args:          []string{"-dn", "-o", "NAME,SIZE,MODEL"},
		CaptureStdout: true,
	})
	if err != nil {
		emit(w, fmt.Sprintf("    (lsblk failed: %v)", err))
		return
	}
	out := strings.TrimRight(string(res.Stdout), "\n")
	if out == "" {
		emit(w, "    (none)")
		return
	}
	for _, line := range strings.Split(out, "\n") {
		emit(w, fmt.Sprintf("    %s", line))
	}
}
