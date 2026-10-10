package osimage

// sync.go -- the converge-to-image refresh for sync/auto on a healthy disk.
//
// One decompression of the image tarball feeds, at the same time,
//
//   - GNU tar, which extracts the image over the existing tree with the same
//     --exclude list as a full extract (preserve, no_new_files,
//     preserve_empty contents, persistent mountpoints), and
//   - an in-process tar header reader, which records every member path and
//     its metadata without parsing tar's text output.
//
// Then a sweep deletes every path on the os set's filesystems that is neither
// an image member (or a parent of one) nor protected by the filter. The
// protection the sweep honours is built from the SAME parsed patterns as the
// tar excludes (Protection), so nothing tar leaves alone is ever deleted:
// TestSweepNeverDeletesTarExcluded pins that.
//
// Compared with the former PrepareSysroot-then-extract refresh:
//   - extract first, sweep second: a failure or power loss mid-way leaves too
//     many files, never too few;
//   - keep_extra: image files under a path are refreshed, files the image does
//     not have are kept (logs, local state);
//   - image_wins: selected paths inside a preserved tree still come from the
//     image;
//   - /boot and /boot/efi are converged too (the old prepare skipped any
//     top-level directory with a mount below it, so old kernels and stale ESP
//     files stayed for ever);
//   - verify: every extracted member is compared with its tar header after the
//     sweep (type, permissions, owner, size, mtime, link target; "content" also
//     compares sha256), so "the disk matches the image" is checked, not assumed.
//
// Type conflicts (the image has a file where the disk has a directory, or the
// other way round) are resolved by the header reader BEFORE tar sees the member:
// the bytes of a member are only passed on to tar after the conflicting path
// has been removed, and only when that path is not protected. GNU tar's own
// --recursive-unlink is never used: it removes existing directory trees
// wholesale, protected content included.

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Protection decides what the sync sweep must never delete. It is built from
// the same patterns as BuildTarExcludes plus the persistent mountpoints, so
// the sweep and tar agree on what is protected.
type Protection struct {
	Preserve      []Pattern // excluded from tar, kept on disk as is
	PreserveEmpty []Pattern // directory kept, its contents removed (tar excludes base/*)
	NoNewFiles    []Pattern // excluded from tar, kept on disk as is
	KeepExtra     []Pattern // NOT excluded from tar; files the image lacks are kept
	// Mountpoints the sweep must not enter (persistent sets, non-os sets) --
	// sysroot-relative, without leading "/".
	SkipMounts []string
}

// protectedSubtree reports whether rel lies in (or is) a kept subtree: the
// sweep leaves rel and everything below it alone.
func (p Protection) protectedSubtree(rel string) bool {
	for _, set := range [][]Pattern{p.Preserve, p.NoNewFiles, p.KeepExtra} {
		for _, pt := range set {
			if pt.Match(rel) {
				return true
			}
		}
	}
	for _, m := range p.SkipMounts {
		if rel == m || strings.HasPrefix(rel, m+"/") {
			return true
		}
	}
	return false
}

// mayContainProtected reports whether a directory rel could hold a protected
// path below it, so the sweep must descend into it instead of removing it
// whole. A pattern with a wildcard protects anything under its literal prefix
// (conservative: the sweep then decides child by child).
func (p Protection) mayContainProtected(rel string) bool {
	under := func(base string) bool { return base != "" && strings.HasPrefix(base, rel+"/") }
	for _, set := range [][]Pattern{p.Preserve, p.NoNewFiles, p.KeepExtra, p.PreserveEmpty} {
		for _, pt := range set {
			lit := literalPrefix(pt.Raw)
			if under(pt.Base) || under(lit) || (lit != "" && strings.HasPrefix(rel+"/", lit)) && strings.ContainsAny(pt.Raw, "*?") {
				return true
			}
		}
	}
	for _, m := range p.SkipMounts {
		if strings.HasPrefix(m, rel+"/") {
			return true
		}
	}
	return false
}

// emptiedDir reports whether rel is a preserve_empty base: the directory itself
// is kept, its children are swept (tar does not refill them).
func (p Protection) emptiedDir(rel string) bool {
	for _, pt := range p.PreserveEmpty {
		if rel == pt.Base {
			return true
		}
	}
	return false
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

// ExcludedByTar reports whether a member path is excluded from the tar
// extract by the given exclude flags (as built by BuildTarExcludes and the
// persistent-mountpoint excludes): "--exclude=./X" excludes X and everything
// below it, "--exclude=./X/*" everything below X. Patterns follow tar's
// wildcard rules as used here (no wildcards in our bases except in preserve
// globs, where * may match /).
func ExcludedByTar(rel string, excludes []string) bool {
	for _, ex := range excludes {
		pat := strings.TrimPrefix(strings.TrimPrefix(ex, "--exclude="), "./")
		if pat == "" {
			continue
		}
		if strings.HasSuffix(pat, "/*") {
			base := strings.TrimSuffix(pat, "/*")
			if strings.HasPrefix(rel, base+"/") {
				return true
			}
			continue
		}
		if globMatch(pat, rel) || rel == pat || strings.HasPrefix(rel, pat+"/") {
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
	// TarExcludes is the full --exclude list (filter + persistent mountpoints).
	TarExcludes []string
	Protect     Protection
	// ImageWins are member patterns extracted from the image even inside a
	// preserved tree (a second tar on the same stream).
	ImageWins []Pattern
	// Verify: "metadata" (default), "content" (also sha256), or "none".
	Verify      string
	DryRun      bool
	Details     bool
	RemoveAfter bool
	// test seams
	decompress func(ctx context.Context, path string) (io.ReadCloser, func() error, error)
	tarCmd     func(ctx context.Context, args ...string) *exec.Cmd
	// conflictDelay slows the type-conflict check down (test only): without the
	// hold, tar would then reach the member first and fail.
	conflictDelay time.Duration
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
		o.Verify = "metadata"
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

	src, waitSrc, err := o.decompress(ctx, o.TarballPath)
	if err != nil {
		return res, fmt.Errorf("sync: decompress %s: %w", o.TarballPath, err)
	}
	defer func() { _ = src.Close() }()

	base := append([]string{"--numeric-owner"}, CapabilityXattrArgs...)
	if o.ACLs {
		base = append([]string{"--acls"}, base...)
	} else {
		base = append([]string{"--no-acls"}, base...)
	}
	tar1 := o.tarCmd(ctx, append(append(append([]string{}, base...), o.TarExcludes...), "-xf", "-", "-C", o.Sysroot)...)
	var tar1Err strings.Builder
	tar1.Stderr = &limitedWriter{b: &tar1Err, max: 64 << 10}
	in1, err := tar1.StdinPipe()
	if err != nil {
		return res, err
	}
	if err := tar1.Start(); err != nil {
		return res, fmt.Errorf("sync: start tar: %w", err)
	}
	sinks := []io.Writer{in1}
	closers := []io.Closer{in1}

	var tar2 *exec.Cmd
	var tar2Err strings.Builder
	if len(o.ImageWins) > 0 {
		args := append(append([]string{}, base...), "--wildcards", "-xf", "-", "-C", o.Sysroot)
		for _, w := range o.ImageWins {
			args = append(args, "./"+w.Raw)
		}
		tar2 = o.tarCmd(ctx, args...)
		tar2.Stderr = &limitedWriter{b: &tar2Err, max: 64 << 10}
		in2, err := tar2.StdinPipe()
		if err != nil {
			return res, err
		}
		if err := tar2.Start(); err != nil {
			return res, fmt.Errorf("sync: start tar (image_wins): %w", err)
		}
		sinks = append(sinks, in2)
		closers = append(closers, in2)
	}

	fw := &holdWriter{w: io.MultiWriter(sinks...)}
	br := bufio.NewReaderSize(src, 1<<20)
	tr := tar.NewReader(io.TeeReader(br, fw))
	members := map[string]memberInfo{}
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
		rel := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+hdr.Name)), "/"), "/")
		if rel != "" {
			mi := memberInfo{typ: hdr.Typeflag, mode: hdr.Mode, uid: hdr.Uid, gid: hdr.Gid, size: hdr.Size, mtime: hdr.ModTime, link: hdr.Linkname}
			excluded := ExcludedByTar(rel, o.TarExcludes)
			if !excluded || winsMatch(o.ImageWins, rel) {
				if fixed, ferr := o.resolveTypeConflict(rel, hdr); ferr != nil {
					streamErr = ferr
					break
				} else if fixed {
					res.TypeChanges++
				}
			}
			if !excluded || winsMatch(o.ImageWins, rel) {
				members[rel] = mi
			} else {
				members[rel] = memberInfo{typ: 'x'} // excluded: kept by the sweep, not verified
			}
		}
		fw.release()
		if o.Verify == "content" && hdr.Typeflag == tar.TypeReg && members[rel].typ != 'x' {
			h := sha256.New()
			if _, err := io.Copy(h, tr); err != nil {
				streamErr = fmt.Errorf("sync: read %s: %w", rel, err)
				break
			}
			mi := members[rel]
			mi.sum = hex.EncodeToString(h.Sum(nil))
			members[rel] = mi
		} else if _, err := io.Copy(io.Discard, tr); err != nil {
			streamErr = fmt.Errorf("sync: read %s: %w", rel, err)
			break
		}
	}
	fw.release()
	if streamErr == nil {
		// end-of-archive blocks and record padding after the last header,
		// read from the buffered reader so no buffered byte is lost
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
	srcErr := waitSrc()
	if streamErr != nil {
		return res, streamErr
	}
	if srcErr != nil {
		return res, fmt.Errorf("sync: decompress: %w", srcErr)
	}
	if werr1 != nil {
		return res, fmt.Errorf("sync: tar extract: %v: %s", werr1, strings.TrimSpace(tar1Err.String()))
	}
	if werr2 != nil {
		// tar exits 2 when a requested member is not in the archive: an
		// image_wins entry the image does not carry. Report, do not fail.
		o.warn("image_wins: %v: %s", werr2, strings.TrimSpace(tar2Err.String()))
	}
	res.Members = len(members)
	o.info("sync: image extracted over %s (%d members, %d type changes)", o.Sysroot, res.Members, res.TypeChanges)

	// Sweep.
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
		if len(res.Mismatches) > 0 {
			for i, m := range res.Mismatches {
				if i >= 20 {
					o.warn("... %d more mismatches", len(res.Mismatches)-20)
					break
				}
				o.warn("verify: %s", m)
			}
			return res, &SyncVerifyError{Count: len(res.Mismatches)}
		}
		o.info("sync: verified %d member(s) against the image (%s): disk matches the image", res.Verified, o.Verify)
	}

	if o.RemoveAfter {
		if err := os.Remove(o.TarballPath); err == nil {
			o.info("osimage tarball removed from %s", filepath.Dir(o.TarballPath))
		}
	}
	return res, nil
}

// SyncVerifyError means the disk does not match the image after the sync.
type SyncVerifyError struct{ Count int }

func (e *SyncVerifyError) Error() string {
	return fmt.Sprintf("sync: %d member(s) on disk do not match the image after the sync", e.Count)
}

func winsMatch(wins []Pattern, rel string) bool {
	for _, w := range wins {
		if w.Match(rel) {
			return true
		}
	}
	return false
}

// resolveTypeConflict removes an unprotected on-disk path whose type differs
// from the member's (so tar can create the member), before tar sees it.
func (o SyncOptions) resolveTypeConflict(rel string, hdr *tar.Header) (bool, error) {
	abs := filepath.Join(o.Sysroot, rel)
	fi, err := os.Lstat(abs)
	if err != nil {
		return false, nil
	}
	if o.conflictDelay > 0 {
		time.Sleep(o.conflictDelay)
	}
	wantDir := hdr.Typeflag == tar.TypeDir
	isDir := fi.IsDir()
	if wantDir == isDir {
		return false, nil
	}
	if !wantDir && hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeSymlink && hdr.Typeflag != tar.TypeLink {
		return false, nil // devices/fifos: let tar decide
	}
	if !isDir {
		// disk has a file/symlink where the image has a directory: tar replaces
		// a non-directory itself; a symlink to a directory is replaced too.
		return false, nil
	}
	// disk has a directory where the image has a file/symlink/hardlink.
	if o.Protect.protectedSubtree(rel) || o.Protect.mayContainProtected(rel) || isMountOrHasMount(abs) {
		return false, fmt.Errorf("sync: the image has a non-directory at %s, but the disk has a directory there that holds protected content or a mount; not removing it -- operator review required", rel)
	}
	o.info("sync: type change at %s (directory on disk, %s in the image): removing the directory", rel, typeName(hdr.Typeflag))
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
	for _, pt := range p.PreserveEmpty {
		if pt.Base != "" && strings.HasPrefix(rel, pt.Base+"/") {
			return true
		}
	}
	return false
}

// sweepAction is the sweep's decision for one on-disk path, independent of
// the filesystem walk so it can be tested exhaustively. inImage: rel is an
// image member; excluded: tar excluded that member; keepDir: rel is a parent
// directory of a member.
func (p Protection) sweepAction(rel string, isDir, inImage, excluded, keepDir bool) int {
	switch {
	case p.protectedSubtree(rel):
		// preserve / no_new_files / keep_extra / persistent or non-os mount
		return actKeepTree
	case p.emptiedDir(rel):
		return actKeep // the preserve_empty directory itself stays; its children are emptied
	case p.underEmptied(rel):
		if isDir && p.mayContainProtected(rel) {
			return actDescend
		}
		return actRemove // preserve_empty: "keep the directory, wipe its contents"
	case inImage && excluded:
		// tar left it alone for a reason this Protection does not name: keep it
		// exactly as it is (never delete what tar excluded)
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
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+"/"))
		m, inImage := members[rel]
		act := o.Protect.sweepAction(rel, d.IsDir(), inImage, m.typ == 'x', keepDirs[rel])
		switch act {
		case actKeepTree:
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case actKeep, actDescend:
			return nil
		}
		if d.IsDir() && (devOf(path) != rootDev || isMountOrHasMount(path)) {
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

func (o SyncOptions) verify(members map[string]memberInfo) (int, []string) {
	var bad []string
	n := 0
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rel := range keys {
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
				if !fi.ModTime().Truncate(time.Second).Equal(m.mtime.Truncate(time.Second)) {
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
		if st != nil && m.typ != tar.TypeLink {
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

// isMountOrHasMount reports whether path is a mountpoint or has a mount below
// it, from /proc/self/mounts.
func isMountOrHasMount(path string) bool {
	body, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return true // fail closed: do not delete what we cannot prove is not a mount
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		if mp == path || strings.HasPrefix(mp, path+"/") {
			return true
		}
	}
	return false
}

func typeName(t byte) string {
	switch t {
	case tar.TypeReg:
		return "file"
	case tar.TypeSymlink:
		return "symlink"
	case tar.TypeLink:
		return "hardlink"
	case tar.TypeDir:
		return "directory"
	}
	return fmt.Sprintf("type %q", t)
}

// holdWriter buffers while hold is set and passes bytes straight on otherwise,
// so a member's header reaches tar only after the type-conflict check.
type holdWriter struct {
	w    io.Writer
	hold bool
	buf  []byte
	mu   sync.Mutex
	err  error
}

func (h *holdWriter) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return 0, h.err
	}
	if h.hold {
		h.buf = append(h.buf, p...)
		return len(p), nil
	}
	if len(h.buf) > 0 {
		if _, err := h.w.Write(h.buf); err != nil {
			h.err = err
			return 0, err
		}
		h.buf = h.buf[:0]
	}
	if _, err := h.w.Write(p); err != nil {
		h.err = err
		return 0, err
	}
	return len(p), nil
}

func (h *holdWriter) release() {
	h.mu.Lock()
	h.hold = false
	if len(h.buf) > 0 && h.err == nil {
		if _, err := h.w.Write(h.buf); err != nil {
			h.err = err
		}
		h.buf = h.buf[:0]
	}
	h.mu.Unlock()
}

type limitedWriter struct {
	b   *strings.Builder
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.b.Len() < l.max {
		rest := l.max - l.b.Len()
		if len(p) > rest {
			l.b.Write(p[:rest])
		} else {
			l.b.Write(p)
		}
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
