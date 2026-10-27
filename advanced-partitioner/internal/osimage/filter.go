package osimage

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// FirstSymlinkResolver looks at <sysroot>/<first-component-of-path>;
// if it is a symlink, the result substitutes the link target for the
// first component. Mirrors resolve_first_symlink in
// /root/NEWTREE/bin/lpart-osimage-install.
//
//	sysroot=/sysroot, /sysroot/lib -> usr/lib
//	"lib/modules/***"  -> "usr/lib/modules/***"
//	"lib"              -> "usr/lib"
//	"var/log/***"      -> "var/log/***"  (not a symlink target)
type FirstSymlinkResolver struct {
	Sysroot string
}

// Resolve walks the first path component and substitutes the symlink
// target when present. Path components must use '/' separators.
func (r FirstSymlinkResolver) Resolve(rel string) string {
	if rel == "" {
		return rel
	}
	first := rel
	rest := ""
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
		rest = rel[i+1:]
	}
	link := filepath.Join(r.Sysroot, first)
	target, err := os.Readlink(link)
	if err != nil {
		return rel
	}
	target = strings.TrimPrefix(target, "/")
	if rest != "" {
		return target + "/" + rest
	}
	return target
}

// ResolveAll applies Resolve to every entry in paths and returns the
// resulting slice. Useful when constructing tar --exclude lists.
func (r FirstSymlinkResolver) ResolveAll(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, r.Resolve(p))
	}
	return out
}

// IsNeededByPreserve returns true if `rel` is either a preserved
// target itself, a child of one, or a parent of one (because we must
// not delete a directory that holds preserved content). Mirrors
// is_needed_by_preserve.
//
// `preserves` should already be resolved through FirstSymlinkResolver;
// `noNewFiles` are merged into the same check (the bash version uses
// the same logic for both).
func IsNeededByPreserve(rel string, preserves, noNewFiles []Pattern) bool {
	all := make([]Pattern, 0, len(preserves)+len(noNewFiles))
	all = append(all, preserves...)
	all = append(all, noNewFiles...)
	for _, p := range all {
		if p.Match(rel) {
			return true
		}
		// rel is a parent of the preserved target → keep so we don't
		// rm -rf a directory that has preserved children inside.
		if p.Base != "" && (p.Base == rel || strings.HasPrefix(p.Base, rel+"/")) {
			return true
		}
	}
	return false
}

// IsNeededByPreserveEmpty returns true if `rel` is one of the
// preserve-empty targets itself or a parent of one. Children of a
// preserve-empty dir are NOT needed — they get wiped. Mirrors
// is_needed_by_preserve_empty.
func IsNeededByPreserveEmpty(rel string, preserveEmpty []Pattern) bool {
	for _, p := range preserveEmpty {
		base := p.Base
		if base == rel {
			return true
		}
		// rel is a parent of the preserve-empty target.
		if base != "" && strings.HasPrefix(base, rel+"/") {
			return true
		}
	}
	return false
}

// ParsePatterns is a convenience that maps a slice of raw strings into
// Patterns in one call.
func ParsePatterns(raw []string) []Pattern {
	out := make([]Pattern, 0, len(raw))
	for _, r := range raw {
		out = append(out, ParsePattern(r))
	}
	return out
}

// PatternsBase returns the .Base field of every Pattern. Useful when
// constructing tar --exclude entries since the excludes refer to the
// base path (not the original glob suffix).
func PatternsBase(patterns []Pattern) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, p.Base)
	}
	return out
}

// NormalizeFilterPath turns an operator-written filter path ("/var/log") into
// the sysroot-relative form every matcher expects ("var/log"); an absolute path
// would otherwise match nothing and its data be deleted. It returns "" for an
// empty or "/" path (everything) or one with a ".." component; callers drop those.
func NormalizeFilterPath(raw string) string {
	p := strings.TrimSpace(raw)
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	for strings.HasPrefix(p, "/") || strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(strings.TrimPrefix(p, "/"), "./")
	}
	if p == "" || p == "." || slices.Contains(strings.Split(p, "/"), "..") {
		return ""
	}
	return p
}

// NormalizeFilterPaths applies NormalizeFilterPath to every entry and returns
// the usable paths plus the raw entries that were dropped.
func NormalizeFilterPaths(raw []string) (paths, dropped []string) {
	for _, r := range raw {
		if n := NormalizeFilterPath(r); n != "" {
			paths = append(paths, n)
		} else {
			dropped = append(dropped, r)
		}
	}
	return paths, dropped
}

// ResolveInSysroot follows symlinks in every component of rel inside sysroot
// (absolute targets relative to the sysroot), up to the first wildcard
// component. Following only the first component protected a "var/mail" link
// but not the mail it points to.
func ResolveInSysroot(sysroot, rel string) string {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	var done []string
	hops := 0
	for i := 0; i < len(parts); i++ {
		c := parts[i]
		if strings.ContainsAny(c, "*?") {
			done = append(done, parts[i:]...)
			break
		}
		if c == "" || c == "." {
			continue
		}
		if c == ".." {
			if len(done) > 0 {
				done = done[:len(done)-1]
			}
			continue
		}
		cand := append(append([]string{}, done...), c)
		target, err := os.Readlink(filepath.Join(sysroot, filepath.Join(cand...)))
		if err != nil || hops > 40 {
			done = cand
			continue
		}
		hops++
		var tparts []string
		if strings.HasPrefix(target, "/") {
			done = nil
			tparts = strings.Split(strings.Trim(target, "/"), "/")
		} else {
			tparts = strings.Split(target, "/")
		}
		// resolve the target's components next, then the rest of rel
		parts = append(append(append([]string{}, parts[:i]...), tparts...), parts[i+1:]...)
		i--
	}
	return strings.Join(done, "/")
}

// ExpandFilterPaths returns every path as written plus, when it differs, the
// same path with all symlinks inside the sysroot resolved: protecting both keeps
// a link and what it points to.
func ExpandFilterPaths(sysroot string, paths []string) []string {
	out := make([]string, 0, len(paths)*2)
	seen := map[string]bool{}
	for _, p := range paths {
		for _, q := range []string{p, ResolveInSysroot(sysroot, p)} {
			if q != "" && !seen[q] {
				seen[q] = true
				out = append(out, q)
			}
		}
	}
	return out
}

// ValidateFilterPaths refuses filter paths whose meaning differs between this
// package's matcher and GNU tar ('[', ']', '\'), that would exclude the whole
// image (a wildcard in the first component), or a glob in preserve_empty
// (which takes literal directories).
func ValidateFilterPaths(kind string, paths []string) error {
	for _, p := range paths {
		if strings.ContainsAny(p, "[]\\") {
			return fmt.Errorf("osimage_filter %s %q: the characters [ ] \\ are not supported", kind, p)
		}
		first := strings.SplitN(p, "/", 2)[0]
		if strings.ContainsAny(first, "*?") {
			return fmt.Errorf("osimage_filter %s %q: a wildcard in the first path component matches every top-level directory", kind, p)
		}
		if kind == "preserve_empty" && strings.ContainsAny(p, "*?") {
			return fmt.Errorf("osimage_filter preserve_empty %q: takes a literal directory, not a pattern", p)
		}
	}
	return nil
}
