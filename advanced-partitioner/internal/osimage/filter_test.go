package osimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstSymlinkResolverNoLink(t *testing.T) {
	r := FirstSymlinkResolver{Sysroot: t.TempDir()}
	for _, in := range []string{"var/log/***", "etc/hosts", "boot"} {
		if got := r.Resolve(in); got != in {
			t.Fatalf("Resolve(%q) = %q, want %q (no link)", in, got, in)
		}
	}
}

func TestFirstSymlinkResolverLibToUsrLib(t *testing.T) {
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "usr", "lib"), 0o750); err != nil {
		t.Fatal(err)
	}
	// /sysroot/lib -> usr/lib
	if err := os.Symlink("usr/lib", filepath.Join(sysroot, "lib")); err != nil {
		t.Fatal(err)
	}
	r := FirstSymlinkResolver{Sysroot: sysroot}
	cases := map[string]string{
		"lib":             "usr/lib",
		"lib/modules":     "usr/lib/modules",
		"lib/modules/***": "usr/lib/modules/***",
		"bin/foo":         "bin/foo", // /sysroot/bin is not a symlink
	}
	for in, want := range cases {
		if got := r.Resolve(in); got != want {
			t.Fatalf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstSymlinkResolverAbsoluteTarget(t *testing.T) {
	// /sysroot/usr/lib symlinked to /usr/lib (absolute target). The
	// bash version strips the leading slash; verify we do too.
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "real"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/real/destination", filepath.Join(sysroot, "abslink")); err != nil {
		t.Fatal(err)
	}
	r := FirstSymlinkResolver{Sysroot: sysroot}
	if got := r.Resolve("abslink/child"); got != "real/destination/child" {
		t.Fatalf("absolute target not normalised: got %q", got)
	}
}

func TestIsNeededByPreserveExactAndSubtree(t *testing.T) {
	preserves := ParsePatterns([]string{"var/log/***", "etc/hosts"})
	cases := map[string]bool{
		"var/log":         true,
		"var/log/foo":     true,
		"var/log/foo/bar": true,
		"etc/hosts":       true,
		"etc/host":        false,
		"etc":             true, // parent of etc/hosts → must keep
		"var":             true, // parent of var/log/***
		"unrelated":       false,
	}
	for in, want := range cases {
		got := IsNeededByPreserve(in, preserves, nil)
		if got != want {
			t.Fatalf("IsNeededByPreserve(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsNeededByPreserveMergesNoNewFiles(t *testing.T) {
	preserves := ParsePatterns([]string{"var/log/***"})
	noNew := ParsePatterns([]string{"proc"})
	// Both the target itself and any subtree path are needed; matches
	// the bash $pbase/* case that fires for every child.
	for _, ok := range []string{"proc", "proc/cpuinfo", "proc/sys/foo"} {
		if !IsNeededByPreserve(ok, preserves, noNew) {
			t.Fatalf("%q should be 'needed' (preserve+no-new-files merge)", ok)
		}
	}
	// Unrelated paths are not needed.
	for _, bad := range []string{"unrelated", "procd"} {
		if IsNeededByPreserve(bad, preserves, noNew) {
			t.Fatalf("%q should NOT be 'needed'", bad)
		}
	}
}

func TestIsNeededByPreserveEmpty(t *testing.T) {
	pe := ParsePatterns([]string{"data", "home"})
	cases := map[string]bool{
		"data": true,
		"home": true,
		// Children are NOT needed (they get wiped to leave the dir empty).
		"data/x": false,
		"home/u": false,
		// Parent of a preserve-empty target IS needed (so we don't
		// rm -rf the directory tree leading down to it).
		"":          false, // empty rel — no preserve-empty entries are inside
		"unrelated": false,
	}
	for in, want := range cases {
		if got := IsNeededByPreserveEmpty(in, pe); got != want {
			t.Fatalf("IsNeededByPreserveEmpty(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsNeededByPreserveEmptyParent(t *testing.T) {
	pe := ParsePatterns([]string{"var/lib/myapp"})
	if !IsNeededByPreserveEmpty("var", pe) {
		t.Fatal("'var' should be needed (parent of 'var/lib/myapp')")
	}
	if !IsNeededByPreserveEmpty("var/lib", pe) {
		t.Fatal("'var/lib' should be needed (parent of 'var/lib/myapp')")
	}
}

func TestPatternsBase(t *testing.T) {
	got := PatternsBase(ParsePatterns([]string{"var/log/***", "etc/hosts", "home/"}))
	want := []string{"var/log", "etc/hosts", "home"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] %q != %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeFilterPath(t *testing.T) {
	for in, want := range map[string]string{
		"/var/log":              "var/log",
		"var/log":               "var/log",
		"./var/log/":            "var/log/",
		"//var//log/***":        "var/log/***",
		" /etc/ssh/ssh_host_* ": "etc/ssh/ssh_host_*",
		"/":                     "",
		"":                      "",
		".":                     "",
		"/var/../etc":           "",
		"../outside":            "",
	} {
		if got := NormalizeFilterPath(in); got != want {
			t.Errorf("NormalizeFilterPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// An absolute preserve path must protect its data exactly like the relative
// form (lab: preserve ["/keeptop", "/var/log/keepdir"] was deleted, the same
// paths without "/" were kept).
func TestAbsolutePreservePathIsNeeded(t *testing.T) {
	paths, dropped := NormalizeFilterPaths([]string{"/keeptop", "/var/log/keepdir", "/"})
	if len(dropped) != 1 || dropped[0] != "/" {
		t.Fatalf("dropped = %v, want [/]", dropped)
	}
	pats := ParsePatterns(paths)
	for _, rel := range []string{"keeptop", "keeptop/m", "var", "var/log", "var/log/keepdir", "var/log/keepdir/m"} {
		if !IsNeededByPreserve(rel, pats, nil) {
			t.Errorf("%s must be kept for preserve %v", rel, paths)
		}
	}
	for _, rel := range []string{"root", "var/log/m2", "var/tmp"} {
		if IsNeededByPreserve(rel, pats, nil) {
			t.Errorf("%s must NOT be kept for preserve %v", rel, paths)
		}
	}
	ex := BuildTarExcludes(pats, nil, nil)
	for _, e := range ex {
		if strings.Contains(e, ".//") {
			t.Errorf("malformed tar exclude %q", e)
		}
	}
}
