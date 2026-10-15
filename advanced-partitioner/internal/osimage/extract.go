package osimage

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// SysrootFSType returns the filesystem type currently mounted at
// sysroot by reading procMountsPath (defaults to /proc/mounts).
// Returns "" when sysroot is not mounted.
//
// The /proc/mounts line format is:
//
//	<source> <target> <fstype> <opts> <dump> <pass>
//
// We pick the LAST matching line: /proc/mounts lists mounts in mount
// order, so with stacked mounts at one target the last entry is the
// effective (topmost) filesystem. Since the propagation firewall
// (lpart-propagation-firewall, a tmpfs mounted at the sysroot under the
// real root fs) every disk install has TWO sysroot entries — first-match
// (the old bash `grep -m1` behaviour this used to replicate) reported
// tmpfs for every disk sysroot, silently disabling tar ACLs and
// inverting the auto stream policy.
func SysrootFSType(procMountsPath, sysroot string) (string, error) {
	if procMountsPath == "" {
		procMountsPath = "/proc/mounts"
	}
	f, err := os.Open(procMountsPath) //nolint:gosec // operator-supplied path; tests inject
	if err != nil {
		return "", fmt.Errorf("read %s: %w", procMountsPath, err)
	}
	defer func() { _ = f.Close() }()

	prefix := " " + sysroot + " "
	fstype := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			fstype = fields[2]
		}
	}
	return fstype, nil
}

// PrepareOptions configures PrepareSysroot.
type PrepareOptions struct {
	Sysroot string
	Logger  *txlog.Logger

	// DryRun disables every filesystem mutation (RemoveAll/MkdirAll) while
	// still computing and logging each decision with a "(dry-run)" prefix.
	// This is the native-fs-ops analog of the executor's dry-run gate: a
	// dry run must leave the sysroot byte-identical. (Safety barrier; a
	// regression test pins it.)
	DryRun bool

	// Resolver is used to walk first-component symlinks before any
	// filter-list lookup. Defaults to FirstSymlinkResolver{Sysroot:
	// opts.Sysroot}.
	Resolver FirstSymlinkResolver

	// FindmntTargets returns the live mount targets reported by
	// findmnt -rn -o TARGET. REQUIRED. An error is fatal: the mount guard
	// refuses to delete blind (fail-closed) — with an unknown mount state a
	// RemoveAll could recurse into a mounted (possibly persistent) volume.
	// The returned list must include the Sysroot itself: the install target
	// is always a mount in the installer environment, so its absence means
	// the tool output cannot be trusted.
	FindmntTargets func() ([]string, error)

	// PersistentMountpoints are the persistent sets' mountpoints as recorded
	// in storage-result.json (absolute, e.g. "/data"). They are preserved by
	// IDENTITY in every wipe step — regardless of what findmnt reports —
	// because mount state is an unreliable proxy for "holds protected data"
	// (the volume may be unmounted at this point in the run and must still
	// never be wiped or have its mountpoint deleted).
	PersistentMountpoints []string

	// PreserveOnly, PreserveEmpty, NoNewFiles are the resolved Patterns.
	PreserveOnly  []Pattern
	PreserveEmpty []Pattern
	NoNewFiles    []Pattern

	// Details prints per-path keep/remove decisions to the Logger when
	// true.
	Details bool
}

// normalizeMountpoints converts absolute mountpoints ("/data") to cleaned
// sysroot-relative form ("data"), dropping empty/"/" entries.
func normalizeMountpoints(mps []string) []string {
	out := make([]string, 0, len(mps))
	for _, mp := range mps {
		clean := strings.Trim(filepath.Clean(mp), "/")
		if clean == "" || clean == "." {
			continue
		}
		out = append(out, clean)
	}
	return out
}

// resolveWithinSysroot resolves rel below sysroot with CHROOT semantics:
// symlinks with absolute targets are re-rooted at sysroot (an absolute link
// on the previous disk means "relative to that system's root", which at
// install time IS the sysroot), relative targets resolve in place, and ".."
// clamps at sysroot. The result therefore can NEVER escape the sysroot —
// this is the barrier against a stale/hostile symlink on prior-disk content
// redirecting the wipe into the running initramfs. exists=false when some
// component does not exist (the returned path is the would-be location).
func resolveWithinSysroot(sysroot, rel string) (path string, exists bool, err error) {
	const maxLinks = 40
	links := 0
	rest := strings.Split(filepath.Clean(rel), "/")
	cur := sysroot
	for len(rest) > 0 {
		comp := rest[0]
		rest = rest[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			if cur != sysroot {
				cur = filepath.Dir(cur)
			}
			continue
		}
		next := filepath.Join(cur, comp)
		info, lerr := os.Lstat(next)
		if lerr != nil {
			if os.IsNotExist(lerr) {
				return filepath.Join(append([]string{next}, rest...)...), false, nil
			}
			return "", false, fmt.Errorf("lstat %s: %w", next, lerr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > maxLinks {
				return "", false, fmt.Errorf("too many symlinks resolving %s under %s", rel, sysroot)
			}
			target, rerr := os.Readlink(next)
			if rerr != nil {
				return "", false, fmt.Errorf("readlink %s: %w", next, rerr)
			}
			tcomps := strings.Split(filepath.Clean(target), "/")
			if filepath.IsAbs(target) {
				cur = sysroot
			}
			rest = append(tcomps, rest...)
			continue
		}
		cur = next
	}
	return cur, true, nil
}

// PrepareSysroot wipes /sysroot in preparation for tar extract, while
// preserving paths the operator requested. Mirrors steps 1-3 of the
// bash script: top-level delete, sibling deletion for non-top-level
// preserves, and emptying preserve-empty dirs.
//
// Safety barriers (each pinned by a test in extract_barriers_test.go):
//   - persistent mountpoints are preserved by identity in all three steps;
//   - the findmnt mount guard fails CLOSED and applies in all three steps;
//   - every enumerated/deleted path is resolved with chroot semantics
//     (resolveWithinSysroot) so deletion can never leave the sysroot;
//   - DryRun performs no mutation at all.
//
//nolint:gocyclo // mirrors the bash flow section-by-section for diff'ability
func PrepareSysroot(ctx context.Context, opts PrepareOptions) error {
	if opts.Sysroot == "" {
		return fmt.Errorf("osimage.PrepareSysroot: Sysroot is required")
	}
	opts.Sysroot = filepath.Clean(opts.Sysroot)
	if opts.Resolver.Sysroot == "" {
		opts.Resolver = FirstSymlinkResolver{Sysroot: opts.Sysroot}
	}
	if opts.FindmntTargets == nil {
		return fmt.Errorf("osimage.PrepareSysroot: FindmntTargets is required (the mount guard must not run blind)")
	}
	targets, err := opts.FindmntTargets()
	if err != nil {
		return fmt.Errorf("osimage.PrepareSysroot: cannot determine active mounts, refusing to delete blind (fail-closed): %w", err)
	}
	mountSet := buildMountSet(targets)
	if _, ok := mountSet[opts.Sysroot]; !ok {
		return fmt.Errorf("osimage.PrepareSysroot: mount list does not include %s itself — findmnt output is not trustworthy, refusing (fail-closed)", opts.Sysroot)
	}
	hasMountUnder := func(entry string) bool {
		for t := range mountSet {
			if t == entry || strings.HasPrefix(t, entry+"/") {
				return true
			}
		}
		return false
	}

	persistent := normalizeMountpoints(opts.PersistentMountpoints)
	// protectedPersistent: rel is a persistent mountpoint, lives under one,
	// or is a parent of one (a parent must survive so the mountpoint path
	// itself survives). Identity-based: independent of mount state.
	protectedPersistent := func(rel string) bool {
		rel = strings.Trim(filepath.Clean(rel), "/")
		for _, mp := range persistent {
			if rel == mp || strings.HasPrefix(rel, mp+"/") || strings.HasPrefix(mp, rel+"/") {
				return true
			}
		}
		return false
	}

	removeAll := func(abs, what, rel string) error {
		if opts.DryRun {
			opts.detail("  (dry-run) would remove %s: %s", what, rel)
			return nil
		}
		opts.detail("  removing %s: %s", what, rel)
		if err := os.RemoveAll(abs); err != nil {
			return fmt.Errorf("rm -rf %s: %w", abs, err)
		}
		return nil
	}

	// Step 1: top-level delete.
	opts.info("preparing %s for extract", opts.Sysroot)
	entries, err := os.ReadDir(opts.Sysroot)
	if err != nil {
		return fmt.Errorf("read %s: %w", opts.Sysroot, err)
	}
	for _, e := range entries {
		entry := filepath.Join(opts.Sysroot, e.Name())
		rel := opts.Resolver.Resolve(e.Name())
		if IsNeededByPreserve(rel, opts.PreserveOnly, opts.NoNewFiles) ||
			IsNeededByPreserveEmpty(rel, opts.PreserveEmpty) {
			opts.detail("  keeping top-level: %s", rel)
			continue
		}
		if protectedPersistent(e.Name()) || protectedPersistent(rel) {
			opts.detail("  keeping top-level (persistent mountpoint): %s", e.Name())
			continue
		}
		if hasMountUnder(entry) {
			opts.detail("  skipping (has mounts): %s", rel)
			continue
		}
		if err := removeAll(entry, "top-level", rel); err != nil {
			return err
		}
	}

	// Step 2: sibling deletion for non-top-level preserves.
	for _, p := range opts.PreserveOnly {
		base := p.Base
		if !strings.Contains(base, "/") {
			continue // top-level already handled
		}
		parent, exists, err := resolveWithinSysroot(opts.Sysroot, filepath.Dir(base))
		if err != nil {
			return fmt.Errorf("resolve preserve parent %s: %w", filepath.Dir(base), err)
		}
		if !exists {
			continue
		}
		if info, err := os.Lstat(parent); err != nil || !info.IsDir() {
			continue
		}
		siblings, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, sib := range siblings {
			sibAbs := filepath.Join(parent, sib.Name())
			litRel := strings.TrimPrefix(sibAbs, opts.Sysroot+"/")
			sibRel := opts.Resolver.Resolve(litRel)
			if IsNeededByPreserve(sibRel, opts.PreserveOnly, opts.NoNewFiles) {
				continue
			}
			if protectedPersistent(litRel) || protectedPersistent(sibRel) {
				opts.detail("  keeping sibling (persistent mountpoint): %s", litRel)
				continue
			}
			if hasMountUnder(sibAbs) {
				opts.detail("  skipping sibling (has mounts): %s", litRel)
				continue
			}
			if err := removeAll(sibAbs, "sibling", sibRel); err != nil {
				return err
			}
		}
	}

	// Step 3: wipe contents of preserve-empty dirs. A persistent mountpoint
	// at or under the dir is protected per-child below; the dir itself is
	// still cleared of its non-persistent content.
	for _, p := range opts.PreserveEmpty {
		dir, exists, err := resolveWithinSysroot(opts.Sysroot, p.Base)
		if err != nil {
			return fmt.Errorf("resolve preserve-empty %s: %w", p.Base, err)
		}
		var notDir bool
		if exists {
			info, lerr := os.Lstat(dir)
			notDir = lerr != nil || !info.IsDir()
		}
		if !exists || notDir {
			if opts.DryRun {
				opts.detail("  (dry-run) would create empty dir: %s", p.Base)
				continue
			}
			opts.detail("  creating empty dir: %s", p.Base)
			// 0o755 is the bash-version default; it's what every other
			// directory under /sysroot uses (consider /home, /tmp).
			// Tightening to 0o750 would break ordinary user access on
			// the final OS.
			//nolint:gosec // intentional 0o755 to match /sysroot defaults
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("mkdir -p %s: %w", dir, err)
			}
			continue
		}
		if _, isMount := mountSet[dir]; isMount && dir != opts.Sysroot {
			// The directory is itself a mounted filesystem (a data set on /home,
			// /scratch, /data): its contents belong to that set, not to the os
			// image. Wiping it deleted every file of a healthy data set.
			opts.detail("  keeping preserve-empty dir (a mounted filesystem, not part of the os set): %s", p.Base)
			continue
		}
		children, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, child := range children {
			abs := filepath.Join(dir, child.Name())
			litRel := strings.TrimPrefix(abs, opts.Sysroot+"/")
			if protectedPersistent(litRel) {
				opts.detail("  keeping in preserve-empty (persistent mountpoint): %s", litRel)
				continue
			}
			if hasMountUnder(abs) {
				opts.detail("  skipping in preserve-empty (has mounts): %s", litRel)
				continue
			}
			if err := removeAll(abs, "from preserve-empty", litRel); err != nil {
				return err
			}
		}
		opts.detail("  preserve-empty dir cleared: %s", p.Base)
	}

	return nil
}

// BuildTarExcludes constructs the `--exclude=./<path>` flags that tar
// should receive. Mirrors step 4 of the bash flow:
//
//	preserve:       --exclude=./<base>           (don't overwrite preserved file/dir)
//	preserve_empty: --exclude=./<base>/*         (don't extract contents)
//	no_new_files:   --exclude=./<base>           (don't create at all)
//
// Returns the list of `--exclude=` strings in deterministic order
// (preserve, preserve-empty, no-new-files) so tests can compare slices.
func BuildTarExcludes(preserveOnly, preserveEmpty, noNewFiles []Pattern) []string {
	out := []string{}
	for _, p := range preserveOnly {
		out = append(out, "--exclude=./"+p.Base)
	}
	for _, p := range preserveEmpty {
		out = append(out, "--exclude=./"+p.Base+"/*")
	}
	for _, p := range noNewFiles {
		out = append(out, "--exclude=./"+p.Base)
	}
	return out
}

// ExtractOptions configures Extract.
type ExtractOptions struct {
	Runner executor.Runner
	Logger *txlog.Logger
	// Sysroot is the extract target.
	Sysroot string
	// TarballPath is the local tarball produced by Download.
	TarballPath string
	// TarExcludes are pre-built --exclude=./<path> entries.
	TarExcludes []string
	// ACLs selects between --acls and --no-acls. Set this from
	// SysrootFSType (tmpfs → false, anything else → true).
	ACLs bool
	// LogDir is where the tar stdout/stderr capture file goes.
	// Defaults to /tmp.
	LogDir string
	// RemoveAfter, when true, rm's the tarball after a successful
	// extract. Matches the production behaviour of deleting
	// /tmp/<image>.tar.bz2 once /sysroot is populated.
	RemoveAfter bool
}

// CapabilityXattrArgs restores file capabilities from the image. Only
// security.capability: SELinux labels come from the template's restorecon, and
// the el8 kernel's tmpfs (the memboot root) does not take user.* xattrs.
var CapabilityXattrArgs = []string{"--xattrs", "--xattrs-include=security.capability"}

// Extract runs tar -I lbzip2 --acls/--no-acls --numeric-owner with the
// pre-built --exclude list. After a successful extract it verifies
// that /sysroot/etc/os-release exists (the bash predecessor's
// sanity check) and optionally removes the tarball.
//
// Mirrors the final tar block in bin/lpart-osimage-install.
func Extract(ctx context.Context, opts ExtractOptions) error {
	if opts.Runner == nil {
		return fmt.Errorf("osimage.Extract: Runner is required")
	}
	if opts.Sysroot == "" {
		return fmt.Errorf("osimage.Extract: Sysroot is required")
	}
	if opts.TarballPath == "" {
		return fmt.Errorf("osimage.Extract: TarballPath is required")
	}

	args := []string{"-I", "lbzip2"}
	if opts.ACLs {
		args = append(args, "--acls")
	} else {
		args = append(args, "--no-acls")
	}
	args = append(args, "--numeric-owner")
	args = append(args, CapabilityXattrArgs...)
	args = append(args, opts.TarExcludes...)
	args = append(args, "-xf", opts.TarballPath, "-C", opts.Sysroot)

	opts.info("extracting osimage into " + opts.Sysroot)
	if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
		Path: "tar", Args: args,
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("tar extract: %w", err)
	}

	// No os-release check here: VerifyExtract makes it, and refuses (exit 3)
	// an image without one, which fails the same way on every retry.
	opts.info("osimage extracted into " + opts.Sysroot + " successfully")

	if opts.RemoveAfter {
		if err := os.Remove(opts.TarballPath); err != nil && !os.IsNotExist(err) {
			opts.warn("could not remove tarball: " + err.Error())
		} else {
			opts.info("osimage tarball removed from " + filepath.Dir(opts.TarballPath))
		}
	}
	return nil
}

func buildMountSet(targets []string) map[string]struct{} {
	out := make(map[string]struct{}, len(targets))
	sort.Strings(targets)
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out[filepath.Clean(t)] = struct{}{}
	}
	return out
}

func (o PrepareOptions) info(format string, args ...any) {
	if o.Logger != nil {
		o.Logger.Info(fmt.Sprintf(format, args...))
	}
}

func (o PrepareOptions) detail(format string, args ...any) {
	if !o.Details {
		return
	}
	if o.Logger != nil {
		o.Logger.Info(fmt.Sprintf(format, args...))
	}
}

func (o ExtractOptions) info(msg string) {
	if o.Logger != nil {
		o.Logger.Info(msg)
	}
}

func (o ExtractOptions) warn(msg string) {
	if o.Logger != nil {
		o.Logger.Warn(msg)
	}
}
