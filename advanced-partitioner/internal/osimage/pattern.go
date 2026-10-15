// Package osimage owns the OS-image-extraction pipeline that
// lpart-osimage-install runs after lpart-storage-prepare has mounted /sysroot.
// It is the Go port of /root/NEWTREE/bin/lpart-osimage-install plus the
// helpers that file inlines.
//
// The package is split into focused files for diff-ability against the
// bash predecessor:
//
//	pattern.go    glob matching with bash-case semantics (* matches /)
//	filter.go     resolve_first_symlink + is_needed_by_preserve etc.
//	luna.go       LUNA_TOKEN + image-filename detection
//	config.go     --config JSON shape
//	download.go   aria2c (torrent) + curl (HTTP) download paths
//	extract.go    PrepareSysroot + BuildTarExcludes + ExtractTarball
//
// Cross-platform notes:
//   - tar -I lbzip2 --acls/--no-acls --numeric-owner is identical on
//     RHEL/Debian/Ubuntu/arm64.
//   - aria2c is optional; the orchestrator falls back to curl when it
//     is not installed.
//   - The ACL flag flips on the sysroot filesystem type: tmpfs gets
//     --no-acls (tmpfs has no ACL support), real disks get --acls.
//   - /proc/mounts is universal across Linux kernels.
//   - The /lunatmp/install.sh handshake is Luna-specific; tests pin
//     the path so they don't need the production layout.
package osimage

import "strings"

// Pattern is one operator-supplied path expression: an exact path, a
// glob (with trailing or mid-path `*`), or a subtree (trailing `/***`).
// Matching mirrors path_matches_pattern in bin/lpart-osimage-install:
//
//	pat                   matches
//	------                -------
//	etc/hosts             exact   "etc/hosts"
//	etc/ssh/ssh_host_*    glob    "etc/ssh/ssh_host_rsa", "etc/ssh/ssh_host_dsa", ...
//	var/log/***           subtree "var/log", "var/log/anything", "var/log/deep/dir"
//
// The bash predecessor uses `case ... in` which in shell pattern syntax
// allows `*` to match `/`. Go's filepath.Match does not, so this file
// carries a hand-rolled matcher (globMatch) that follows the bash
// rules.
type Pattern struct {
	Raw  string // original string as written in the config
	Base string // Raw with trailing /***, /**, /* stripped
}

// ParsePattern returns a Pattern for the given raw string. Trailing
// /*+ (i.e. a run of stars preceded by a slash) is stripped from Base
// along with that slash. A trailing star that is not preceded by a
// slash is left in place — it's part of a file-glob like
// `etc/ssh/ssh_host_*`, not a subtree marker.
//
// Bash equivalent: `pbase="${pat%%/\**}"`.
func ParsePattern(raw string) Pattern {
	base := raw
	// Find the last slash that begins a "/*+" suffix and trim from
	// there. We scan right to left; the bash %% operator picks the
	// longest match, but `/*+` is unambiguous (every '*' from that
	// slash to end is part of the suffix).
	for i := len(base) - 1; i > 0; i-- {
		if base[i] == '*' {
			continue
		}
		if base[i] == '/' && i+1 < len(base) && base[i+1] == '*' {
			base = base[:i]
		}
		break
	}
	base = strings.TrimRight(base, "/")
	return Pattern{Raw: raw, Base: base}
}

// Match returns true when rel matches this pattern. The three branches
// mirror the bash case ladder:
//
//  1. globMatch(Raw, rel)    — direct glob match (covers etc/ssh/*).
//  2. rel == Base            — exact match of the trailing-glob-stripped pattern.
//  3. rel under Base         — rel == "<Base>/..." (subtree).
//
// `*` matches any character including `/` (bash case semantics).
func (p Pattern) Match(rel string) bool {
	if globMatch(p.Raw, rel) {
		return true
	}
	if rel == p.Base {
		return true
	}
	if p.Base != "" && strings.HasPrefix(rel, p.Base+"/") {
		return true
	}
	return false
}

// Reaches reports whether p can match dir or anything below it.
func (p Pattern) Reaches(dir string) bool {
	if p.Match(dir) || strings.HasPrefix(p.Base, dir+"/") {
		return true
	}
	lit := literalPrefix(p.Raw)
	return lit != "" && strings.HasPrefix(dir+"/", lit)
}

// globMatch implements bash-case style globbing. `?` matches one byte,
// `*` matches zero or more bytes including `/`. Anything else is a
// literal. Multiple consecutive `*`s collapse to one (bash treats `*`
// and `**` and `***` identically in pattern context).
func globMatch(pattern, s string) bool {
	// Fast path: no wildcards.
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == s
	}
	return matchHere(pattern, s)
}

// matchHere is a tiny recursive matcher. The algorithm is the textbook
// "advance both, special-case *" walk; it runs in O(len(pattern)*len(s))
// worst case which is fine for our tiny path strings.
func matchHere(pattern, s string) bool {
	for {
		if len(pattern) == 0 {
			return len(s) == 0
		}
		switch pattern[0] {
		case '*':
			// Collapse consecutive stars.
			rest := pattern
			for len(rest) > 0 && rest[0] == '*' {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return true // trailing * matches anything
			}
			// Try every split.
			for i := 0; i <= len(s); i++ {
				if matchHere(rest, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		default:
			if len(s) == 0 || pattern[0] != s[0] {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		}
	}
}
