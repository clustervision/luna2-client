package osimage

import (
	"os"
	"path/filepath"
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

// NormalizeFilterPath turns an operator-written filter path into the
// sysroot-relative form every matcher here expects ("var/log", "etc/ssh/*"):
// surrounding space, leading "/" and "./" are stripped and runs of "/" are
// collapsed. Operators naturally write absolute paths ("/var/log"), and Luna
// shows them that way; before this, such a path matched nothing (the tar
// exclude became ".//var/log" and PrepareSysroot never saw it as preserved),
// so the "preserved" data was deleted without a warning. It returns "" for a
// path that is empty or "/" (that would mean "everything") or that contains a
// ".." component (it could point outside the sysroot); callers drop those.
func NormalizeFilterPath(raw string) string {
	p := strings.TrimSpace(raw)
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	for {
		switch {
		case strings.HasPrefix(p, "/"):
			p = p[1:]
		case strings.HasPrefix(p, "./"):
			p = p[2:]
		default:
			goto done
		}
	}
done:
	if p == "" || p == "." {
		return ""
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return ""
		}
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
