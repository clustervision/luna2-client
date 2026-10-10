// Package discover enumerates the block devices on a Linux host and
// resolves each one to either a /dev/disk/by-path/ symlink (stable across
// reboots) or a bare kernel path (/dev/sda, /dev/nvme0n1, ...). The
// returned []Device is the input to intent.AssignDevices.
//
// This is the Go port of intent_list_devices + _intent_resolve_by_path
// in lib/intent.sh, plus the diagnostic table that lives in
// lib/discovery.sh::print_device_inventory.
//
// Cross-distribution notes:
//   - lsblk -J -d -b -o <lsblkDiscoverColumns> behaves identically on
//     RHEL 8/9/10, Rocky, Alma, Ubuntu, Debian, SUSE (util-linux >= 2.27);
//     see the lsblkDiscoverColumns comment for why PATH is excluded.
//   - rota can be a JSON bool (true/false) on newer lsblk or a string
//     ("1"/"0") on older lsblk; ListDevices handles both.
//   - /dev/disk/by-path/ is populated by udev on every Linux distro we
//     target. On VMs with virtio-blk or NVMe-only systems an entry may
//     be missing for some kernel paths — ListDevices falls back to the
//     bare kernel path with a warning, matching the Bash behaviour.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// DefaultByPathDir is where udev populates physical-port symlinks.
const DefaultByPathDir = "/dev/disk/by-path"

// lsblkDiscoverColumns is the ONE lsblk column set used for device
// discovery on every distro. Deliberately universal — do not add
// version-gated columns: PATH was only added in util-linux 2.33, so
// including it makes el8 (2.32) error the whole call ("lsblk: exit
// status 1" -> 0 eligible devices, install fails). The kernel path is
// derived from NAME instead (/dev/<name>), which is identical for whole
// disks (-d) across every lsblk version we target (>= 2.27). SERIAL and WWN
// (the disk's stable identity, used to order equal disks) exist on 2.32.
const lsblkDiscoverColumns = "NAME,TYPE,SIZE,ROTA,TRAN,MODEL,SERIAL,WWN"

// DefaultSysBlockDir is where the kernel lists block devices and their
// holders (the multipath-member probe).
const DefaultSysBlockDir = "/sys/block"

// NamesMode selects how Device.Path is rendered.
type NamesMode string

const (
	// NamesByPath renders Device.Path as the by-path symlink (preferred).
	NamesByPath NamesMode = "by-path"
	// NamesKernel renders Device.Path as the bare /dev/<name> path.
	NamesKernel NamesMode = "kernel"
)

// Device is one enumerated block device after filtering and path rendering.
type Device struct {
	// Name is the kernel name (sda, nvme0n1, vda, ...).
	Name string
	// Path is the operator-visible device path. With NamesByPath this is
	// the /dev/disk/by-path/... symlink when available; with NamesKernel
	// it is the bare /dev/<Name> path. If by-path resolution fell back
	// to kernel, ByPathMissing is true and ListDevices emits a warning.
	Path string
	// KernelPath is always /dev/<Name>.
	KernelPath string
	// Size in bytes.
	Size int64
	// Rotational is true for HDDs, false for SSD/NVMe/virtio with
	// queue/rotational=0.
	Rotational bool
	// Transport is the bus transport reported by lsblk (sata, sas,
	// nvme, virtio, mmc, usb, ...). May be empty on some platforms.
	Transport string
	// Model is the device model string reported by lsblk (e.g.
	// "Samsung SSD 990", "VMware Virtual S"). May be empty. Used by the
	// selection=discover match.model substring filter.
	Model string
	// Tags are the auto-derived media tags used by selection=discover match
	// filters: "rotary" for a spinning disk, "ssd" for any solid-state
	// disk, and additionally "nvme" for an NVMe disk (so an NVMe carries
	// both "ssd" and "nvme"). Derived at list time from Rotational +
	// Transport; see DeriveTags.
	Tags []string
	// ByPathMissing is true when NamesByPath was requested but no
	// /dev/disk/by-path symlink resolved to this device.
	ByPathMissing bool
	// Serial and WWN are the disk's identity as lsblk reports it (may be
	// empty, e.g. virtio without serial=).
	Serial string
	WWN    string
	// ByPath is the /dev/disk/by-path link of the disk (its slot), whatever
	// the Names mode; "" when udev made none.
	ByPath string
}

// IdentityKey is a stable identity for ordering disks that are otherwise
// equal (same size): WWN, else serial, else the by-path slot, and only as a
// last resort the kernel name. Kernel names follow enumeration order, which
// changes between boots (a disk added, a controller probed later); ordering
// equal disks by name let a selection=discover set swap disks with another
// set when enumeration changed, and auto then wiped the other set's data.
func (d Device) IdentityKey() string {
	switch {
	case d.WWN != "":
		return "wwn:" + d.WWN
	case d.Serial != "":
		return "serial:" + d.Serial
	case d.ByPath != "":
		return "path:" + d.ByPath
	default:
		return "name:" + d.Name
	}
}

// DeriveTags returns the auto media tags for a device given its rotational
// flag and transport/name. A rotational disk is "rotary"; a non-rotational
// disk is "ssd"; an NVMe disk additionally gets "nvme" (a subset of "ssd",
// so an NVMe carries both). Matches the v1.4 spec section on disk tags.
func DeriveTags(name, transport string, rotational bool) []string {
	if rotational {
		return []string{"rotary"}
	}
	if TransportLabel(name, transport) == "nvme" {
		return []string{"ssd", "nvme"}
	}
	return []string{"ssd"}
}

// HasTag reports whether the device carries the given tag.
func (d Device) HasTag(tag string) bool {
	for _, t := range d.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Options controls a single ListDevices call.
type Options struct {
	// Runner runs lsblk. Required.
	Runner executor.Runner
	// Names selects the path style; defaults to NamesByPath.
	Names NamesMode
	// ByPathDir is where to look for by-path symlinks; defaults to
	// DefaultByPathDir. Tests override this with a temp dir.
	ByPathDir string
	// MinBytes / MaxBytes are size-fence defaults; devices outside the
	// range are dropped. A zero MaxBytes means "no upper bound".
	MinBytes int64
	MaxBytes int64
	// Warn is invoked once per device that falls back to a kernel path
	// because no by-path symlink was found. nil is OK.
	Warn func(format string, args ...any)
	// Timeout for the underlying lsblk call. Zero leaves it to the
	// context.
	Timeout time.Duration
	// SysBlockDir defaults to DefaultSysBlockDir; tests inject a tempdir.
	SysBlockDir string
}

// ListDevices enumerates eligible LOCAL disks. Eligibility mirrors
// intent_list_devices in lib/intent.sh: type=disk, transport not usb, fc or
// iscsi, not a path of a device-mapper multipath map, not loop/ram/zram,
// size>0, size in [MinBytes, MaxBytes] when set. USB disks are excluded by
// design (install media, keys); FC/iSCSI LUNs and multipath paths are shared
// SAN storage, never a node's own disk -- and every path of a multipath LUN
// is the same LUN. The returned slice is sorted by Path for display; the
// discover resolver orders equal disks by IdentityKey.
func ListDevices(ctx context.Context, opts Options) ([]Device, error) {
	if opts.Runner == nil {
		return nil, errors.New("discover.ListDevices: Runner is required")
	}
	if opts.Names == "" {
		opts.Names = NamesByPath
	}
	if opts.ByPathDir == "" {
		opts.ByPathDir = DefaultByPathDir
	}
	if opts.SysBlockDir == "" {
		opts.SysBlockDir = DefaultSysBlockDir
	}

	res, err := opts.Runner.Run(ctx, executor.CommandSpec{
		Path:          "lsblk",
		Args:          []string{"-J", "-d", "-b", "-o", lsblkDiscoverColumns},
		CaptureStdout: true,
		Timeout:       opts.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}

	parsed, err := parseLsblkJSON(res.Stdout)
	if err != nil {
		return nil, fmt.Errorf("parse lsblk output: %w", err)
	}

	cache := newByPathCache(opts.ByPathDir)

	out := make([]Device, 0, len(parsed))
	for _, e := range parsed {
		// type=disk
		if e.Type != "disk" {
			continue
		}
		// exclude USB (by design), and SAN LUNs: Fibre Channel, iSCSI
		switch e.Tran {
		case "usb", "fc", "iscsi":
			continue
		}
		// exclude loop, ram, zram by kernel name
		if isExcludedKernelName(e.Name) {
			continue
		}
		// size > 0
		if e.Size <= 0 {
			continue
		}
		// size fence
		if opts.MinBytes > 0 && e.Size < opts.MinBytes {
			continue
		}
		if opts.MaxBytes > 0 && e.Size > opts.MaxBytes {
			continue
		}
		// a path of a multipath map: the LUN is reached through the map
		if isMultipathMember(opts.SysBlockDir, e.Name) {
			continue
		}

		// Kernel path derived from NAME (the PATH column is unavailable on
		// util-linux < 2.33). lsblk emits a "path" field when present; prefer it,
		// else synthesise /dev/<name> -- identical for whole disks.
		kpath := e.Path
		if kpath == "" {
			kpath = "/dev/" + e.Name
		}

		dev := Device{
			Name:       e.Name,
			KernelPath: kpath,
			Size:       e.Size,
			Rotational: e.Rota,
			Transport:  e.Tran,
			Model:      strings.TrimSpace(e.Model),
			Tags:       DeriveTags(e.Name, e.Tran, e.Rota),
			Serial:     strings.TrimSpace(e.Serial),
			WWN:        strings.TrimSpace(e.WWN),
		}
		if bp, err := cache.lookup(kpath); err == nil {
			dev.ByPath = bp
		}

		switch opts.Names {
		case NamesByPath:
			bp, err := cache.lookup(kpath)
			if err != nil {
				return nil, fmt.Errorf("by-path lookup for %s: %w", kpath, err)
			}
			if bp != "" {
				dev.Path = bp
			} else {
				dev.Path = kpath
				dev.ByPathMissing = true
				if opts.Warn != nil {
					opts.Warn("no by-path entry for %s, falling back to kernel path", kpath)
				}
			}
		default:
			dev.Path = kpath
		}

		out = append(out, dev)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// MediaLabel returns "flash" or "spindle" for a rotational flag.
// Matches discovery_rotational_label in lib/discovery.sh.
func MediaLabel(rotational bool) string {
	if rotational {
		return "spindle"
	}
	return "flash"
}

// TransportLabel returns the bus transport, falling back to a kernel-name
// heuristic when lsblk left the field empty (nvme*, sd*, vd*).
// Matches discovery_transport_label in lib/discovery.sh.
func TransportLabel(name, tran string) string {
	if tran != "" && tran != "-" {
		return tran
	}
	switch {
	case strings.HasPrefix(name, "nvme"):
		return "nvme"
	case strings.HasPrefix(name, "sd"):
		return "scsi"
	case strings.HasPrefix(name, "vd"):
		return "virtio"
	default:
		return "-"
	}
}

// isMultipathMember reports whether a disk is one path of a device-mapper
// multipath map: one of its holders is a dm device whose dm uuid starts with
// "mpath-" (multipathd's naming). Unreadable sysfs reads as "not a member".
func isMultipathMember(sysBlockDir, name string) bool {
	holders, err := os.ReadDir(filepath.Join(sysBlockDir, name, "holders"))
	if err != nil {
		return false
	}
	for _, h := range holders {
		if !strings.HasPrefix(h.Name(), "dm-") {
			continue
		}
		uuid, err := os.ReadFile(filepath.Join(sysBlockDir, h.Name(), "dm", "uuid")) //nolint:gosec // sysfs path built from kernel names
		if err != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(string(uuid)), "mpath-") {
			return true
		}
	}
	return false
}

// isExcludedKernelName matches names that the Bash validator drops
// unconditionally: loop*, ram*, zram*.
func isExcludedKernelName(name string) bool {
	return strings.HasPrefix(name, "loop") ||
		strings.HasPrefix(name, "ram") ||
		strings.HasPrefix(name, "zram")
}

// --- lsblk JSON parsing --------------------------------------------------

// lsblkEntry is one row from `lsblk -J -d -b -o <lsblkDiscoverColumns>`.
// rota and size are decoded with a flexible type because lsblk versions
// vary in whether they emit bool/number or string.
type lsblkEntry struct {
	Name   string
	Path   string
	Type   string
	Size   int64
	Rota   bool
	Tran   string
	Model  string
	Serial string
	WWN    string
}

type lsblkRoot struct {
	Blockdevices []json.RawMessage `json:"blockdevices"`
}

// parseLsblkJSON consumes one `lsblk -J ...` invocation's output.
func parseLsblkJSON(in []byte) ([]lsblkEntry, error) {
	var root lsblkRoot
	if err := json.Unmarshal(in, &root); err != nil {
		return nil, err
	}
	out := make([]lsblkEntry, 0, len(root.Blockdevices))
	for i, raw := range root.Blockdevices {
		e, err := decodeLsblkEntry(raw)
		if err != nil {
			return nil, fmt.Errorf("blockdevices[%d]: %w", i, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func decodeLsblkEntry(raw json.RawMessage) (lsblkEntry, error) {
	// Use a flexible intermediate to absorb scalar-type drift.
	var loose struct {
		Name   json.RawMessage `json:"name"`
		Path   json.RawMessage `json:"path"`
		Type   json.RawMessage `json:"type"`
		Size   json.RawMessage `json:"size"`
		Rota   json.RawMessage `json:"rota"`
		Tran   json.RawMessage `json:"tran"`
		Model  json.RawMessage `json:"model"`
		Serial json.RawMessage `json:"serial"`
		WWN    json.RawMessage `json:"wwn"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		return lsblkEntry{}, err
	}
	var e lsblkEntry
	e.Name = asString(loose.Name)
	e.Path = asString(loose.Path)
	e.Type = asString(loose.Type)
	e.Tran = asString(loose.Tran)
	e.Size = asInt64(loose.Size)
	e.Rota = asBool(loose.Rota)
	e.Model = asString(loose.Model)
	e.Serial = asString(loose.Serial)
	e.WWN = asString(loose.WWN)
	return e, nil
}

func asString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), "\"")
}

func asInt64(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var i int64
	if err := json.Unmarshal(raw, &i); err == nil {
		return i
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := parseInt64(s); err == nil {
			return v
		}
	}
	return 0
}

func asBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(s) {
		case "1", "true", "yes":
			return true
		default:
			return false
		}
	}
	return false
}

func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

// --- by-path cache -------------------------------------------------------

// byPathCache walks dir once and indexes target → symlink path.
// Subsequent lookups are O(1). Partition entries (those whose link target
// includes a partition suffix) are excluded — only whole-disk entries
// participate. Matches the *-part* filter in _intent_resolve_by_path.
type byPathCache struct {
	dir      string
	indexed  bool
	resolved map[string]string
	err      error
}

func newByPathCache(dir string) *byPathCache {
	return &byPathCache{dir: dir}
}

func (c *byPathCache) index() {
	if c.indexed {
		return
	}
	c.indexed = true
	c.resolved = map[string]string{}

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		// Missing /dev/disk/by-path is a perfectly normal state on
		// systems without udev or in some VMs. Treat as "no entries"
		// rather than an error — callers fall back to kernel paths.
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		c.err = err
		return
	}
	for _, ent := range entries {
		// Skip partition symlinks; we only want whole-disk targets.
		if strings.Contains(ent.Name(), "-part") {
			continue
		}
		link := filepath.Join(c.dir, ent.Name())
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			continue
		}
		c.resolved[target] = link
	}
}

func (c *byPathCache) lookup(kernelPath string) (string, error) {
	c.index()
	if c.err != nil {
		return "", c.err
	}
	// EvalSymlinks above returned absolute paths; the kernelPath we get
	// from lsblk is already absolute (e.g. /dev/sda).
	return c.resolved[kernelPath], nil
}
