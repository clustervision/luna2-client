// Package mount is the Go port of /root/NEWTREE/lib/mounts.sh. It owns
// the sysroot mount/unmount logic: bring filesystems online (shallow
// first), or take them offline (deepest first), and produce a typed
// result that downstream code (lpart-bootloader-finalise, lpart-storage-check)
// can consume.
//
// Cross-platform notes: mount/umount/findmnt semantics are identical
// across the supported distros and architectures.
package mount

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// DefaultSysroot is the install target root used throughout the
// installer. Same value the Bash tree pins via LPART_TARGET_SYSROOT.
const DefaultSysroot = "/sysroot"

// firewallSource is the mount source name of the propagation firewall: a tiny
// private tmpfs mounted at the sysroot BEFORE the target root filesystem, so
// the root fs (and every mount created below it — /boot, the ESP, the
// bootloader bind mounts) inherits PRIVATE propagation instead of joining the
// initramfs root's shared peer group.
//
// Why this exists (runtime-proven on the rocky9 install initramfs, 2026-07-16):
// systemd sandboxes some initramfs services (systemd-udevd, haveged) in slave
// mount namespaces. A shared /sysroot mount propagates INTO those namespaces;
// the bootloader's `--make-rslave` bind mounts then break umount propagation
// back OUT, so the namespace copies survive `umount -R /sysroot` and pin the
// root LV open (dm open_count > 0) for the REST OF THE BOOT — no userspace
// cleanup can release it (proven: even `umount -N` in every holder namespace
// leaves the superblock live). Consequences: the health check sees a
// permanently-dirty, still-active xfs (false BROKEN), and the retry path can
// never `vgchange -an`/`dmsetup remove` its own leftovers (the historic
// "install retry loops forever in one boot" defect). With the firewall the
// mount never propagates anywhere, one umount releases the device, and
// same-boot re-runs work. The name is deliberately self-describing so it is
// obvious in findmnt/mountinfo output.
const firewallSource = "lpart-propagation-firewall"

// EffectiveSysroot is the install target root for the *running* initramfs.
// initramfs-tools (Debian/Ubuntu) exports rootmnt (e.g. /root) and pivots into
// it via `run-init "${rootmnt}"`; dracut (RHEL/SUSE) leaves rootmnt unset and
// pivots into /sysroot. Honour rootmnt so the installer mounts and extracts the
// OS where the running initramfs will actually pivot — otherwise a dracut-pinned
// /sysroot leaves Ubuntu's /root empty ("No init found"). Mirrors the daemon
// install template's `${rootmnt:-/sysroot}`.
func EffectiveSysroot() string {
	if r := strings.TrimSpace(os.Getenv("rootmnt")); r != "" {
		return r
	}
	return DefaultSysroot
}

// Result is what MountSysrootTree returns. mounted_target_root + root_device
// match the JSON keys lib/mounts.sh writes so the storage-result.json
// assembly can splice this in unchanged.
type Result struct {
	MountedTargetRoot string `json:"mounted_target_root"`
	RootDevice        string `json:"root_device,omitempty"`
}

// Options configures a Mounter.
type Options struct {
	Runner  executor.Runner
	Logger  *txlog.Logger
	Sysroot string // defaults to EffectiveSysroot() ($rootmnt, else /sysroot)
}

// Mounter performs sysroot mount/unmount operations.
type Mounter struct {
	o Options
}

// New returns a Mounter.
func New(o Options) *Mounter {
	if o.Sysroot == "" {
		o.Sysroot = EffectiveSysroot()
	}
	return &Mounter{o: o}
}

// SymlinkError refuses a mount target with a symlink below the sysroot: mkdir -p
// and mount(8) would follow it, out of the sysroot when it is absolute.
type SymlinkError struct{ Target, Link string }

func (e *SymlinkError) Error() string {
	return fmt.Sprintf("mount target %s: %s is a symlink in the existing root, and a mount never goes through one -- set the mountpoint to the real directory, or remove the link", e.Target, e.Link)
}

// MountSysrootTree mounts every entry in filesystems under the sysroot.
// Root (/) first, then other mountpoints in shallow-first depth order.
// Swap entries and mountpoint=="" are skipped. Mirrors
// lib/mounts.sh::mount_storage_tree.
func (m *Mounter) MountSysrootTree(ctx context.Context, filesystems fs.Filesystems) (Result, error) {
	if m.o.Runner == nil {
		return Result{}, fmt.Errorf("mount.MountSysrootTree: Runner is required")
	}
	res := Result{MountedTargetRoot: m.o.Sysroot}

	// Root mount first, on top of the propagation firewall (see
	// firewallSource): the private layer keeps the whole sysroot tree out of
	// other mount namespaces so teardown can always release the devices.
	for _, fsEnt := range filesystems.Filesystems {
		if fsEnt.Mount == "/" {
			if err := m.ensureDir(m.o.Sysroot); err != nil {
				return Result{}, err
			}
			if err := m.ensurePropagationFirewall(ctx, m.o.Sysroot); err != nil {
				return Result{}, err
			}
			if err := m.mountIfNeeded(ctx, fsEnt.Device, m.o.Sysroot, fsEnt.FS, fsEnt.Options); err != nil {
				return Result{}, err
			}
			res.RootDevice = fsEnt.Device
			break
		}
	}

	// Child mountpoints sorted shallow-first (by '/' count, then lex).
	children := childMountPointsInOrder(filesystems)
	for _, mp := range children {
		for _, fsEnt := range filesystems.Filesystems {
			if fsEnt.Mount != mp {
				continue
			}
			target := m.o.Sysroot + mp
			if err := m.refuseSymlinkBelowSysroot(target); err != nil {
				return Result{}, err
			}
			if err := m.ensureDir(target); err != nil {
				return Result{}, err
			}
			if err := m.mountIfNeeded(ctx, fsEnt.Device, target, fsEnt.FS, fsEnt.Options); err != nil {
				return Result{}, err
			}
			break
		}
	}

	return res, nil
}

// UnmountSysrootTree unmounts everything currently under sysroot,
// deepest first. Mirrors lib/mounts.sh::unmount_sysroot_tree_if_mounted
// and the unmount_storage_tree_from_result helper called from
// bin/lpart-storage-prepare.
func (m *Mounter) UnmountSysrootTree(ctx context.Context) error {
	if m.o.Runner == nil {
		return fmt.Errorf("mount.UnmountSysrootTree: Runner is required")
	}
	targets := m.liveSysrootTargets(ctx)
	if len(targets) == 0 {
		return nil
	}
	// Deepest first by length, then lex descending for ties.
	sort.SliceStable(targets, func(i, j int) bool {
		if len(targets[i]) != len(targets[j]) {
			return len(targets[i]) > len(targets[j])
		}
		return targets[i] > targets[j]
	})
	for _, t := range targets {
		m.unmountIfMounted(ctx, t)
	}
	return nil
}

// UnmountFromResult unmounts the targets implied by a Filesystems
// result (used by lpart-storage-check before remounting). Mirrors
// lib/mounts.sh::unmount_storage_tree_from_result.
func (m *Mounter) UnmountFromResult(ctx context.Context, filesystems fs.Filesystems) error {
	if m.o.Runner == nil {
		return fmt.Errorf("mount.UnmountFromResult: Runner is required")
	}
	targets := targetsFromFilesystems(filesystems, m.o.Sysroot)
	if len(targets) == 0 {
		return nil
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if len(targets[i]) != len(targets[j]) {
			return len(targets[i]) > len(targets[j])
		}
		return targets[i] > targets[j]
	})
	for _, t := range targets {
		m.unmountIfMounted(ctx, t)
	}
	return nil
}

// ensurePropagationFirewall mounts the private firewall layer at target when
// nothing is mounted there yet (see firewallSource for the full rationale).
// Idempotent: an existing mount at target — the firewall from an earlier pass,
// or the target fs itself on a re-entrant run — means the layer must not be
// stacked again; mountIfNeeded then arbitrates the top of the stack as before.
func (m *Mounter) ensurePropagationFirewall(ctx context.Context, target string) error {
	if _, mounted := m.mountedSource(ctx, target); mounted {
		return nil
	}
	if _, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "mount",
		Args:          []string{"-t", "tmpfs", "-o", "size=64k,mode=0755", firewallSource, target},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("propagation firewall mount at %s: %w", target, err)
	}
	if _, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "mount",
		Args:          []string{"--make-private", target},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("propagation firewall --make-private %s: %w", target, err)
	}
	return nil
}

// mountIfNeeded refuses to layer two mounts at one target and refuses to
// re-mount a source onto a different target than the one already
// observed. Mirrors mount_if_needed in lib/mounts.sh. options is the
// per-fs mount option string from the volume config ("" = defaults) —
// dropping it silently broke tmpfs size=/mode= for the memboot root
// (FTA D6).
func (m *Mounter) mountIfNeeded(ctx context.Context, source, target, fsType, options string) error {
	if fsType != "tmpfs" {
		if err := m.ensureNotMountedElsewhere(ctx, source); err != nil {
			return err
		}
	}
	if curSource, mounted := m.mountedSource(ctx, target); mounted {
		if curSource == source {
			if m.o.Logger != nil {
				m.o.Logger.Info("mount already present",
					"target", target, "source", source)
			}
			return nil
		}
		if curSource != firewallSource {
			return fmt.Errorf("mount target %s already used by %s", target, curSource)
		}
		// The propagation firewall is the top of the stack: the target fs
		// mounts over it, inheriting private propagation.
	}
	if options == "" {
		options = "defaults"
	}
	if _, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "mount",
		Args:          []string{"-t", fsType, "-o", options, source, target},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("mount %s -> %s: %w", source, target, err)
	}
	return nil
}

func (m *Mounter) ensureNotMountedElsewhere(ctx context.Context, source string) error {
	res, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "findmnt", Args: []string{"-rn", "-S", source, "-o", "TARGET"},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return nil // not mounted anywhere
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		target := UnescapeFindmnt(strings.TrimSpace(line))
		if target == "" || target == m.o.Sysroot || strings.HasPrefix(target, m.o.Sysroot+"/") {
			continue
		}
		return fmt.Errorf("device already mounted outside %s: %s -> %s",
			m.o.Sysroot, source, target)
	}
	return nil
}

func (m *Mounter) mountedSource(ctx context.Context, target string) (string, bool) {
	res, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "findmnt", Args: []string{"-rn", "-M", target, "-o", "SOURCE"},
		CaptureStdout: true, CaptureStderr: true,
		Quiet: true, // probe: "not mounted" is fine
	})
	if err != nil {
		return "", false
	}
	source := UnescapeFindmnt(strings.TrimSpace(string(res.Stdout)))
	if source == "" {
		return "", false
	}
	return source, true
}

func (m *Mounter) unmountIfMounted(ctx context.Context, target string) {
	if !m.isMounted(ctx, target) {
		return
	}
	if _, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "umount", Args: []string{target},
		CaptureStdout: true, CaptureStderr: true,
	}); err == nil {
		return
	}
	if m.o.Logger != nil {
		m.o.Logger.Warn("normal unmount failed; trying lazy", "target", target)
	}
	_, _ = m.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "umount", Args: []string{"-l", target},
		CaptureStdout: true, CaptureStderr: true,
	})
}

func (m *Mounter) isMounted(ctx context.Context, target string) bool {
	_, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "findmnt", Args: []string{"-rn", "-M", target},
		CaptureStdout: true, CaptureStderr: true,
		Quiet: true, // probe: "not mounted" is fine
	})
	return err == nil
}

func (m *Mounter) liveSysrootTargets(ctx context.Context) []string {
	res, err := m.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "findmnt", Args: []string{"-rn", "-o", "TARGET"},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return nil
	}
	out := []string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		t := UnescapeFindmnt(strings.TrimSpace(line))
		if t == "" {
			continue
		}
		if t == m.o.Sysroot || strings.HasPrefix(t, m.o.Sysroot+"/") {
			out = append(out, t)
		}
	}
	return out
}

// refuseSymlinkBelowSysroot refuses the first symlinked component of target below
// the sysroot; a missing one is made by mkdir -p as a real directory.
func (m *Mounter) refuseSymlinkBelowSysroot(target string) error {
	p := filepath.Clean(m.o.Sysroot)
	for _, c := range strings.Split(strings.TrimPrefix(filepath.Clean(target), p+"/"), "/") {
		p = filepath.Join(p, c)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return &SymlinkError{Target: target, Link: p}
		}
	}
	return nil
}

func (m *Mounter) ensureDir(path string) error {
	// mkdir -p semantics via the Runner; we want this to land in the
	// dry-run logs too.
	if _, err := m.o.Runner.Run(context.Background(), executor.CommandSpec{
		Path: "mkdir", Args: []string{"-p", path},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("mkdir -p %s: %w", path, err)
	}
	return nil
}

// childMountPointsInOrder returns the child mount paths (everything
// except "/" and swap) sorted shallow-first by '/' count then lex.
// Mirrors storage_child_mount_points_in_order.
func childMountPointsInOrder(filesystems fs.Filesystems) []string {
	out := []string{}
	for _, f := range filesystems.Filesystems {
		if f.Mount == "/" || f.Mount == "swap" || f.Mount == "" {
			continue
		}
		out = append(out, f.Mount)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := strings.Count(out[i], "/"), strings.Count(out[j], "/")
		if ci != cj {
			return ci < cj
		}
		return out[i] < out[j]
	})
	return out
}

// targetsFromFilesystems renders the in-tree mount targets that
// UnmountFromResult should remove. Mirrors
// storage_result_mount_targets_descending (without the sort — caller
// does that).
func targetsFromFilesystems(filesystems fs.Filesystems, sysroot string) []string {
	out := []string{}
	for _, f := range filesystems.Filesystems {
		if f.Mount == "swap" || f.Mount == "" {
			continue
		}
		if f.Mount == "/" {
			out = append(out, sysroot)
			continue
		}
		out = append(out, sysroot+f.Mount)
	}
	return out
}

// UnescapeFindmnt undoes findmnt -r's \xNN escaping (a space, a backslash, any
// non-ASCII byte), so a target compares equal to the path on disk.
func UnescapeFindmnt(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
