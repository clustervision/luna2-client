package osimage

// The converge-to-image refresh for sync/auto on a healthy disk: one
// decompression feeds GNU tar and an in-process header reader, then a sweep
// deletes what is neither an image member nor protected. Extracting first means
// an interruption leaves too many files, never too few.
//
// The sweep's protection is built from the same patterns as the tar excludes,
// so nothing tar leaves alone is deleted (TestSweepNeverDeletesTarExcluded).
// Type conflicts are resolved before tar sees the member: tar's own
// --recursive-unlink would remove protected trees wholesale.

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Protection decides what the sync sweep must never delete.
type Protection struct {
	Preserve      []Pattern // excluded from tar, kept on disk as is
	PreserveEmpty []Pattern // directory kept, its contents removed (tar excludes base/*)
	NoNewFiles    []Pattern // excluded from tar, kept on disk as is
	KeepExtra     []Pattern // NOT excluded from tar; files the image lacks are kept
	// SkipMounts are sysroot-relative mountpoints the sweep never enters.
	SkipMounts []string
}

// protectedSubtree reports whether the sweep leaves rel and everything below it alone.
func (p Protection) protectedSubtree(rel string) bool {
	if matchAny(p.Preserve, rel) || matchAny(p.NoNewFiles, rel) || matchAny(p.KeepExtra, rel) {
		return true
	}
	return slices.ContainsFunc(p.SkipMounts, func(m string) bool { return within(rel, m) })
}

// mayContainProtected reports whether directory rel could hold a protected
// path, so the sweep decides child by child instead of removing it whole. A
// wildcard pattern counts for anything under its literal prefix.
func (p Protection) mayContainProtected(rel string) bool {
	below := rel + "/"
	for _, set := range [][]Pattern{p.Preserve, p.NoNewFiles, p.KeepExtra, p.PreserveEmpty} {
		for _, pt := range set {
			lit := literalPrefix(pt.Raw)
			if strings.HasPrefix(pt.Base, below) || (lit != "" && (strings.HasPrefix(lit, below) || strings.HasPrefix(below, lit))) {
				return true
			}
		}
	}
	return slices.ContainsFunc(p.SkipMounts, func(m string) bool { return strings.HasPrefix(m, below) })
}

// literalPrefix returns the part of a pattern before its first wildcard,
// trimmed to a whole path component ("var/*/keep" -> "var/").
func literalPrefix(raw string) string {
	i := strings.IndexAny(raw, "*?")
	if i < 0 {
		return ""
	}
	pre := raw[:i]
	if j := strings.LastIndexByte(pre, '/'); j >= 0 {
		return pre[:j+1]
	}
	return ""
}

// ExcludedByTar reports whether the --exclude flags (as BuildTarExcludes
// builds them) skip member rel: "./X" excludes X and everything below it,
// "./X/*" only what is below X.
func ExcludedByTar(rel string, excludes []string) bool {
	for _, ex := range excludes {
		pat := strings.TrimPrefix(strings.TrimPrefix(ex, "--exclude="), "./")
		if base, ok := strings.CutSuffix(pat, "/*"); ok {
			if strings.HasPrefix(rel, base+"/") {
				return true
			}
		} else if pat != "" && (globMatch(pat, rel) || within(rel, pat)) {
			return true
		}
	}
	return false
}

// SyncOptions configures SyncExtract.
type SyncOptions struct {
	Logger      *txlog.Logger
	Sysroot     string
	TarballPath string
	ACLs        bool
	TarExcludes []string // the full --exclude list (filter + persistent mountpoints)
	Protect     Protection
	// OSMounts are the os set's own mountpoints, sysroot-relative ("boot",
	// "boot/efi"). The sweep never enters any other mount below the sysroot.
	OSMounts []string
	// ImageWins are extracted even inside a preserved tree (a second tar on
	// the same stream).
	ImageWins []Pattern
	// Verify: "none" (default), "metadata", or "content" (also sha256).
	// Mismatches are warnings only.
	Verify      string
	DryRun      bool
	Details     bool
	RemoveAfter bool
	// test seams
	decompress func(ctx context.Context, path string) (io.ReadCloser, func() error, error)
	tarCmd     func(ctx context.Context, args ...string) *exec.Cmd
	// conflictDelay slows the type-conflict check (test only): without the
	// hold, tar would then reach the member first and fail.
	conflictDelay time.Duration
	mounts        []string // sysroot-relative mountpoints below the sysroot
}

// SyncResult reports what the sync did.
type SyncResult struct {
	Members     int
	Removed     int
	TypeChanges int
	Verified    int
	Mismatches  []string
}

type memberInfo struct {
	typ      byte
	mode     int64
	uid, gid int
	size     int64
	mtime    time.Time
	link     string
	sum      string
}

// SyncExtract converges the sysroot to the image (see the file comment).
func SyncExtract(ctx context.Context, o SyncOptions) (SyncResult, error) {
	var res SyncResult
	if o.Sysroot == "" || o.TarballPath == "" {
		return res, fmt.Errorf("osimage.SyncExtract: Sysroot and TarballPath are required")
	}
	o.Sysroot = filepath.Clean(o.Sysroot)
	if o.Verify == "" {
		o.Verify = "none"
	}
	if o.decompress == nil {
		o.decompress = lbzip2Decompress
	}
	if o.tarCmd == nil {
		o.tarCmd = func(ctx context.Context, args ...string) *exec.Cmd { return exec.CommandContext(ctx, "tar", args...) }
	}
	if o.DryRun {
		o.info("(dry-run) would sync %s to %s: extract with %d excludes, then sweep", o.Sysroot, o.TarballPath, len(o.TarExcludes))
		return res, nil
	}

	var err error
	if o.mounts, err = mountsBelow(o.Sysroot); err != nil {
		return res, err
	}
	o.Protect.SkipMounts = slices.Clone(o.Protect.SkipMounts)
	for _, m := range o.mounts {
		if !slices.Contains(o.OSMounts, m) {
			o.Protect.SkipMounts = append(o.Protect.SkipMounts, m)
		}
	}

	src, waitSrc, err := o.decompress(ctx, o.TarballPath)
	if err != nil {
		return res, fmt.Errorf("sync: decompress %s: %w", o.TarballPath, err)
	}
	defer func() { _ = src.Close() }()

	acl := "--no-acls"
	if o.ACLs {
		acl = "--acls"
	}
	base := append([]string{acl, "--numeric-owner"}, CapabilityXattrArgs...)
	var tar1Err, tar2Err strings.Builder
	tar1, in1, err := o.startTar(ctx, append(append(slices.Clone(base), o.TarExcludes...), "-xf", "-", "-C", o.Sysroot), &tar1Err, "sync: start tar")
	if err != nil {
		return res, err
	}
	sinks := []io.Writer{in1}
	closers := []io.Closer{in1}

	var tar2 *exec.Cmd
	if len(o.ImageWins) > 0 {
		args := append(slices.Clone(base), "--wildcards", "-xf", "-", "-C", o.Sysroot)
		for _, w := range o.ImageWins {
			args = append(args, "./"+w.Raw)
		}
		var in2 io.WriteCloser
		if tar2, in2, err = o.startTar(ctx, args, &tar2Err, "sync: start tar (image_wins)"); err != nil {
			return res, err
		}
		sinks = append(sinks, in2)
		closers = append(closers, in2)
	}

	fw := &holdWriter{w: io.MultiWriter(sinks...)}
	br := bufio.NewReaderSize(src, 1<<20)
	tr := tar.NewReader(io.TeeReader(br, fw))
	members := map[string]memberInfo{}
	// hard links to a member tar excluded: tar cannot create them, so they are
	// linked to the preserved on-disk file after the extract
	type pendingLink struct{ link, target string }
	var pending []pendingLink
	var streamErr error
	for {
		fw.hold = true
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			streamErr = fmt.Errorf("sync: read image tar: %w", err)
			break
		}
		rel := memberPath(hdr.Name)
		if rel != "" {
			if o.extracted(rel) {
				fixed, ferr := o.resolveTypeConflict(rel, hdr)
				if ferr != nil {
					streamErr = ferr
					break
				}
				if fixed {
					res.TypeChanges++
				}
				if hdr.Typeflag == tar.TypeLink {
					if tgt := memberPath(hdr.Linkname); !o.extracted(tgt) {
						pending = append(pending, pendingLink{rel, tgt})
					}
				}
				members[rel] = memberInfo{typ: hdr.Typeflag, mode: hdr.Mode, uid: hdr.Uid, gid: hdr.Gid, size: hdr.Size, mtime: hdr.ModTime, link: hdr.Linkname}
			} else {
				members[rel] = memberInfo{typ: 'x'} // excluded: kept by the sweep, not verified
			}
		}
		fw.release()
		var h hash.Hash
		sink := io.Discard
		if o.Verify == "content" && hdr.Typeflag == tar.TypeReg && members[rel].typ != 'x' {
			h = sha256.New()
			sink = h
		}
		if _, err := io.Copy(sink, tr); err != nil {
			streamErr = fmt.Errorf("sync: read %s: %w", rel, err)
			break
		}
		if h != nil {
			mi := members[rel]
			mi.sum = hex.EncodeToString(h.Sum(nil))
			members[rel] = mi
		}
	}
	fw.release()
	if streamErr == nil {
		// end-of-archive blocks and record padding, from the buffered reader so
		// no buffered byte is lost
		if _, err := io.Copy(fw, br); err != nil {
			streamErr = err
		}
	}
	for _, c := range closers {
		_ = c.Close()
	}
	werr1 := tar1.Wait()
	var werr2 error
	if tar2 != nil {
		werr2 = tar2.Wait()
	}
	if streamErr != nil {
		_ = src.Close() // the decompressor blocks on a full pipe until its reader goes
	}
	srcErr := waitSrc()
	if streamErr != nil {
		return res, streamErr
	}
	if srcErr != nil {
		return res, fmt.Errorf("sync: decompress: %w", srcErr)
	}
	// GNU tar exits 2 both for a hard link it cannot create and for a fatal
	// error; only its stderr tells them apart.
	if werr1 != nil && len(pending) > 0 && exitStatus(werr1) == 2 && onlyHardlinkErrors(tar1Err.String(), len(pending)) {
		werr1 = nil // the pending links are created below
	}
	if werr1 != nil {
		return res, fmt.Errorf("sync: tar extract: %v: %s", werr1, strings.TrimSpace(tar1Err.String()))
	}
	for _, pl := range pending {
		dst, src := filepath.Join(o.Sysroot, pl.link), filepath.Join(o.Sysroot, pl.target)
		if _, err := os.Lstat(src); err != nil {
			o.warn("sync: %s is a hard link to the preserved %s, which is not on disk: link not created", pl.link, pl.target)
			delete(members, pl.link)
			continue
		}
		_ = os.Remove(dst)
		if err := os.Link(src, dst); err != nil {
			return res, fmt.Errorf("sync: hard link %s -> %s: %w", pl.link, pl.target, err)
		}
		o.info("sync: %s linked to the preserved %s (hard link into a preserved path)", pl.link, pl.target)
	}
	if werr2 != nil {
		// tar exits 2 when a requested member is not in the archive: an
		// image_wins entry the image does not carry. Report, do not fail.
		o.warn("image_wins: %v: %s", werr2, strings.TrimSpace(tar2Err.String()))
	}
	res.Members = len(members)
	o.info("sync: image extracted over %s (%d members, %d type changes)", o.Sysroot, res.Members, res.TypeChanges)

	keepDirs := map[string]bool{}
	for rel := range members {
		for d := filepath.Dir(rel); d != "." && d != "/" && !keepDirs[d]; d = filepath.Dir(d) {
			keepDirs[d] = true
		}
	}
	removed, err := o.sweep(members, keepDirs)
	res.Removed = removed
	if err != nil {
		return res, err
	}
	o.info("sync: removed %d path(s) that are not in the image and not protected by the filter", removed)

	if o.Verify != "none" {
		res.Verified, res.Mismatches = o.verify(members)
		for i, m := range res.Mismatches {
			if i >= 20 {
				o.warn("... %d more mismatches", len(res.Mismatches)-20)
				break
			}
			o.warn("verify: %s", m)
		}
		if len(res.Mismatches) > 0 {
			o.warn("sync: %d member(s) on disk do not match the image after the sync", len(res.Mismatches))
		} else {
			o.info("sync: verified %d member(s) against the image (%s): disk matches the image", res.Verified, o.Verify)
		}
	}

	if o.RemoveAfter {
		if err := os.Remove(o.TarballPath); err == nil {
			o.info("osimage tarball removed from %s", filepath.Dir(o.TarballPath))
		}
	}
	return res, nil
}

// startTar starts a tar reading the image from its stdin.
func (o SyncOptions) startTar(ctx context.Context, args []string, stderr *strings.Builder, what string) (*exec.Cmd, io.WriteCloser, error) {
	cmd := o.tarCmd(ctx, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C") // the hard-link tolerance reads tar's English messages
	cmd.Stderr = &limitedWriter{b: stderr, max: 64 << 10}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", what, err)
	}
	return cmd, in, nil
}

// extracted reports whether tar writes member rel: not excluded, or image_wins.
func (o SyncOptions) extracted(rel string) bool {
	return !ExcludedByTar(rel, o.TarExcludes) || matchAny(o.ImageWins, rel)
}

// memberPath turns a member name into a sysroot-relative path ("./etc/" -> "etc").
func memberPath(name string) string {
	return strings.TrimPrefix(filepath.Clean("/"+name), "/")
}

func matchAny(pats []Pattern, rel string) bool {
	return slices.ContainsFunc(pats, func(p Pattern) bool { return p.Match(rel) })
}

// within reports whether rel is base or lies below it.
func within(rel, base string) bool {
	return rel == base || strings.HasPrefix(rel, base+"/")
}

// exitStatus is a command's exit status, or -1 when it did not exit normally.
func exitStatus(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

var conflictKinds = map[byte]string{tar.TypeReg: "file", tar.TypeSymlink: "symlink", tar.TypeLink: "hardlink"}

// resolveTypeConflict removes an unprotected on-disk directory where the image
// has a file, symlink or hard link, before tar sees the member. tar replaces a
// non-directory itself; devices and fifos are left to tar.
func (o SyncOptions) resolveTypeConflict(rel string, hdr *tar.Header) (bool, error) {
	abs := filepath.Join(o.Sysroot, rel)
	fi, err := os.Lstat(abs)
	if err != nil {
		return false, nil
	}
	if o.conflictDelay > 0 {
		time.Sleep(o.conflictDelay)
	}
	kind, ok := conflictKinds[hdr.Typeflag]
	if !ok || !fi.IsDir() {
		return false, nil
	}
	if o.Protect.protectedSubtree(rel) || o.Protect.mayContainProtected(rel) || o.hasMount(rel) {
		return false, fmt.Errorf("sync: the image has a non-directory at %s, but the disk has a directory there that holds protected content or a mount; not removing it -- operator review required", rel)
	}
	o.info("sync: type change at %s (directory on disk, %s in the image): removing the directory", rel, kind)
	return true, os.RemoveAll(abs)
}

// sweep actions
const (
	actKeep     = iota // keep this path, descend if it is a directory
	actKeepTree        // keep this path and everything below it
	actDescend         // not kept itself, but may hold protected paths: decide child by child
	actRemove          // delete (a directory with everything below it)
)

// underEmptied reports whether rel lies strictly below a preserve_empty base.
func (p Protection) underEmptied(rel string) bool {
	return slices.ContainsFunc(p.PreserveEmpty, func(pt Pattern) bool { return pt.Base != "" && strings.HasPrefix(rel, pt.Base+"/") })
}

// sweepAction is the sweep's decision for one on-disk path, apart from the walk
// so it can be tested exhaustively. inImage: rel is a member; excluded: tar
// excluded that member; keepDir: rel is a parent directory of a member.
func (p Protection) sweepAction(rel string, isDir, inImage, excluded, keepDir bool) int {
	switch {
	case p.protectedSubtree(rel):
		return actKeepTree
	case slices.ContainsFunc(p.PreserveEmpty, func(pt Pattern) bool { return pt.Base == rel }):
		return actKeep // the preserve_empty directory itself stays; its children are emptied
	case p.underEmptied(rel):
		if isDir && p.mayContainProtected(rel) {
			return actDescend
		}
		return actRemove
	case inImage && excluded:
		// tar left it alone for a reason this Protection does not name
		return actKeepTree
	case inImage || keepDir:
		return actKeep
	case isDir && p.mayContainProtected(rel):
		return actDescend
	}
	return actRemove
}

func (o SyncOptions) sweep(members map[string]memberInfo, keepDirs map[string]bool) (int, error) {
	removed := 0
	root := o.Sysroot
	rootDev := devOf(root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) {
				return nil
			}
			return werr
		}
		if path == root {
			return nil
		}
		rel := strings.TrimPrefix(path, root+"/")
		m, inImage := members[rel]
		switch o.Protect.sweepAction(rel, d.IsDir(), inImage, m.typ == 'x', keepDirs[rel]) {
		case actKeepTree:
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case actKeep, actDescend:
			return nil
		}
		if d.IsDir() && (devOf(path) != rootDev || o.hasMount(rel)) {
			// another filesystem that the image does not own: never delete into it
			o.detail("  sync: keeping mountpoint %s (not part of the image)", rel)
			return filepath.SkipDir
		}
		o.detail("  sync: removing %s", rel)
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("sync: remove %s: %w", rel, err)
		}
		removed++
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return removed, err
}

// fsMagicNoPOSIX are statfs f_type values of filesystems that store no Unix
// owner/mode and keep mtimes in 2-second steps: msdos/vfat and exfat.
var fsMagicNoPOSIX = map[int64]bool{0x4d44: true, 0x2011bab0: true}

// statfsType is a test seam: the filesystem type (statfs f_type) of path.
var statfsType = func(path string) (int64, error) {
	var sfs syscall.Statfs_t
	if err := syscall.Statfs(path, &sfs); err != nil {
		return 0, err
	}
	return int64(sfs.Type), nil //nolint:unconvert // Type is int64 on amd64, int32 elsewhere
}

// posixMetaFS reports whether the filesystem of path (device dev) keeps Unix
// owner, mode and 1-second mtimes. Cached per device; an unreadable statfs
// counts as POSIX, so the strict comparison stays the default.
func posixMetaFS(dev uint64, path string, cache map[uint64]bool) bool {
	if v, ok := cache[dev]; ok {
		return v
	}
	t, err := statfsType(path)
	cache[dev] = err != nil || !fsMagicNoPOSIX[t]
	return cache[dev]
}

// mtimeMatches compares mtimes to the second on a POSIX filesystem, within
// 2 seconds on vfat (FAT rounds write times down to an even second).
func mtimeMatches(disk, image time.Time, posix bool) bool {
	if posix {
		return disk.Truncate(time.Second).Equal(image.Truncate(time.Second))
	}
	d := disk.Sub(image)
	if d < 0 {
		d = -d
	}
	return d < 2*time.Second
}

func (o SyncOptions) verify(members map[string]memberInfo) (int, []string) {
	fsKind := map[uint64]bool{}
	var bad []string
	n := 0
	for _, rel := range slices.Sorted(maps.Keys(members)) {
		m := members[rel]
		if m.typ == 'x' {
			continue
		}
		abs := filepath.Join(o.Sysroot, rel)
		fi, err := os.Lstat(abs)
		if err != nil {
			bad = append(bad, rel+": missing on disk")
			continue
		}
		st, _ := fi.Sys().(*syscall.Stat_t)
		n++
		// vfat (the ESP) stores no owner or mode, so only POSIX filesystems compare them
		posix := st == nil || posixMetaFS(uint64(st.Dev), abs, fsKind) //nolint:unconvert // Dev is uint64 on amd64, uint32 elsewhere
		switch m.typ {
		case tar.TypeDir:
			if !fi.IsDir() {
				bad = append(bad, rel+": not a directory")
			}
		case tar.TypeSymlink:
			if fi.Mode()&os.ModeSymlink == 0 {
				bad = append(bad, rel+": not a symlink")
			} else if t, _ := os.Readlink(abs); t != m.link {
				bad = append(bad, fmt.Sprintf("%s: symlink -> %q, image -> %q", rel, t, m.link))
			}
			continue
		case tar.TypeReg, tar.TypeLink:
			if !fi.Mode().IsRegular() {
				bad = append(bad, rel+": not a regular file")
				continue
			}
			if m.typ == tar.TypeReg {
				if fi.Size() != m.size {
					bad = append(bad, fmt.Sprintf("%s: size %d, image %d", rel, fi.Size(), m.size))
				}
				if !mtimeMatches(fi.ModTime(), m.mtime, posix) {
					bad = append(bad, fmt.Sprintf("%s: mtime %s, image %s", rel, fi.ModTime().UTC().Format(time.RFC3339), m.mtime.UTC().Format(time.RFC3339)))
				}
				if m.sum != "" {
					if s, err := fileSHA256(abs); err != nil || s != m.sum {
						bad = append(bad, rel+": content differs from the image")
					}
				}
			}
		default:
			continue
		}
		if st != nil && m.typ != tar.TypeLink && posix {
			if int(st.Uid) != m.uid || int(st.Gid) != m.gid {
				bad = append(bad, fmt.Sprintf("%s: owner %d:%d, image %d:%d", rel, st.Uid, st.Gid, m.uid, m.gid))
			}
			if int64(st.Mode&07777) != m.mode&07777 {
				bad = append(bad, fmt.Sprintf("%s: mode %o, image %o", rel, st.Mode&07777, m.mode&07777))
			}
		}
	}
	return n, bad
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // sysroot path built from tar member names
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func devOf(path string) uint64 {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev) //nolint:unconvert // Dev is uint64 on amd64, uint32 elsewhere
	}
	return 0
}

// procSelfMounts is a test seam.
var procSelfMounts = "/proc/self/mounts"

var mountUnescaper = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

// mountsBelow lists the mountpoints strictly below root, relative to it. An
// unreadable mount table is an error: the sweep must know what not to enter.
func mountsBelow(root string) ([]string, error) {
	body, err := os.ReadFile(procSelfMounts)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot list the mounts below %s, refusing to sweep: %w", root, err)
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			if rel, ok := strings.CutPrefix(mountUnescaper.Replace(f[1]), root+"/"); ok && rel != "" {
				out = append(out, rel)
			}
		}
	}
	return out, nil
}

// hasMount reports whether rel is a mountpoint or has one below it.
func (o SyncOptions) hasMount(rel string) bool {
	return slices.ContainsFunc(o.mounts, func(m string) bool { return within(m, rel) })
}

// holdWriter buffers while hold is set, so a member's header reaches tar only
// after the type-conflict check.
type holdWriter struct {
	w    io.Writer
	hold bool
	buf  []byte
	err  error
}

func (h *holdWriter) Write(p []byte) (int, error) {
	if h.hold && h.err == nil {
		h.buf = append(h.buf, p...)
		return len(p), nil
	}
	h.release()
	if h.err == nil {
		_, h.err = h.w.Write(p)
	}
	if h.err != nil {
		return 0, h.err
	}
	return len(p), nil
}

func (h *holdWriter) release() {
	h.hold = false
	if len(h.buf) > 0 && h.err == nil {
		_, h.err = h.w.Write(h.buf)
		h.buf = h.buf[:0]
	}
}

type limitedWriter struct {
	b   *strings.Builder
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if rest := l.max - l.b.Len(); rest > 0 {
		l.b.Write(p[:min(len(p), rest)])
	}
	return len(p), nil
}

func lbzip2Decompress(ctx context.Context, path string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, "lbzip2", "-dc", path)
	var errb strings.Builder
	cmd.Stderr = &limitedWriter{b: &errb, max: 16 << 10}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
		}
		return nil
	}, nil
}

func (o SyncOptions) info(format string, args ...any) {
	if o.Logger != nil {
		o.Logger.UserInfo(format, args...)
	}
}

func (o SyncOptions) warn(format string, args ...any) {
	if o.Logger != nil {
		o.Logger.Warn(fmt.Sprintf(format, args...))
	}
}

func (o SyncOptions) detail(format string, args ...any) {
	if o.Logger != nil && o.Details {
		o.Logger.UserInfo(format, args...)
	}
}

// onlyHardlinkErrors reports whether tar's stderr holds nothing but "Cannot
// hard link to" errors (at most max of them) and the closing exit summary.
func onlyHardlinkErrors(stderr string, max int) bool {
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.Contains(line, "Cannot hard link to"):
			n++
		case strings.Contains(line, "Exiting with failure status due to previous errors"):
		default:
			return false
		}
	}
	return n > 0 && n <= max
}
