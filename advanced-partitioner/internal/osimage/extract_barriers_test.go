package osimage

// Barrier-pinning tests for PrepareSysroot's safety properties. Each test
// exists so that REMOVING a safety barrier makes the suite fail — this is
// the direct answer to the port having silently dropped previously-fixed
// barriers (dry-run firewall, symlink-safe wipe). Do not weaken these tests
// to make a refactor pass; if one fails, the barrier is gone.
//
// Barriers pinned here:
//  1. persistent mountpoints are preserved by IDENTITY in all three steps;
//  2. the findmnt guard fails CLOSED (error, or output missing the sysroot);
//  3. deletion never escapes the sysroot through symlinked path components;
//  4. DryRun leaves the tree byte-identical;
//  5. the mount guard applies in steps 2/3, not only step 1.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// treeFingerprint walks root and returns a deterministic digest of every
// path, file type, and file content — the "byte-identical" oracle for the
// dry-run barrier.
func treeFingerprint(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		line := rel + "|" + info.Mode().String()
		if info.Mode().IsRegular() {
			f, err := os.Open(path) //nolint:gosec // test walks its own tempdir
			if err != nil {
				return err
			}
			h := sha256.New()
			if _, err := io.Copy(h, f); err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
			line += "|" + fmt.Sprintf("%x", h.Sum(nil))
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprint %s: %v", root, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- barrier 1: identity-based persistent preserve ------------------------

func TestPersistentMountpointSurvivesAllSteps(t *testing.T) {
	sysroot := t.TempDir()
	// /data is a persistent mountpoint holding customer files. It is NOT
	// mounted (the volume may be unmounted at this point in the run) and is
	// NOT on any preserve list — identity alone must protect it.
	writeFile(t, sysroot, "data/customer.db", "precious")
	writeFile(t, sysroot, "junk/x", "x")
	// step-2 exposure: a preserve pattern whose parent contains /data.
	writeFile(t, sysroot, "var/keep/k", "k")
	writeFile(t, sysroot, "var/data2/customer2.db", "precious2") // persistent, sibling of a preserve
	// step-3 exposure: a preserve-empty dir containing a persistent mountpoint.
	writeFile(t, sysroot, "scratch/data3/customer3.db", "precious3")
	writeFile(t, sysroot, "scratch/tmpfile", "wipe me")

	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:               sysroot,
		FindmntTargets:        testMounts(sysroot),
		PersistentMountpoints: []string{"/data", "/var/data2", "/scratch/data3"},
		PreserveOnly:          ParsePatterns([]string{"var/keep"}),
		PreserveEmpty:         ParsePatterns([]string{"scratch"}),
	})
	if err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	for _, p := range []string{"data/customer.db", "var/data2/customer2.db", "scratch/data3/customer3.db", "var/keep/k"} {
		if _, err := os.Stat(filepath.Join(sysroot, p)); err != nil {
			t.Fatalf("persistent/preserved path lost: %s (%v)", p, err)
		}
	}
	for _, p := range []string{"junk", "scratch/tmpfile"} {
		if _, err := os.Stat(filepath.Join(sysroot, p)); err == nil {
			t.Fatalf("expected %s to be removed", p)
		}
	}
}

func TestPersistentParentDirectorySurvives(t *testing.T) {
	sysroot := t.TempDir()
	// Mountpoint /srv/data — the top-level parent "srv" must survive step 1
	// or the mountpoint path itself is destroyed.
	writeFile(t, sysroot, "srv/data/d", "d")
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:               sysroot,
		FindmntTargets:        testMounts(sysroot),
		PersistentMountpoints: []string{"/srv/data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "srv/data/d")); err != nil {
		t.Fatalf("persistent path under a non-persistent parent lost: %v", err)
	}
}

// --- barrier 2: fail-closed findmnt --------------------------------------

func TestFindmntErrorRefusesToDelete(t *testing.T) {
	sysroot := t.TempDir()
	writeFile(t, sysroot, "junk/x", "x")
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: func() ([]string, error) { return nil, fmt.Errorf("findmnt: not found") },
	})
	if err == nil {
		t.Fatal("expected fail-closed error when findmnt fails")
	}
	if _, serr := os.Stat(filepath.Join(sysroot, "junk/x")); serr != nil {
		t.Fatalf("nothing may be deleted on a findmnt failure; junk/x gone: %v", serr)
	}
}

func TestFindmntMissingSysrootRefuses(t *testing.T) {
	sysroot := t.TempDir()
	writeFile(t, sysroot, "junk/x", "x")
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot: sysroot,
		// Non-empty list that does NOT include the sysroot: untrustworthy.
		FindmntTargets: func() ([]string, error) { return []string{"/", "/proc"}, nil },
	})
	if err == nil {
		t.Fatal("expected refusal when the mount list misses the sysroot itself")
	}
	if _, serr := os.Stat(filepath.Join(sysroot, "junk/x")); serr != nil {
		t.Fatalf("nothing may be deleted; junk/x gone: %v", serr)
	}
}

func TestNilFindmntTargetsRefused(t *testing.T) {
	sysroot := t.TempDir()
	err := PrepareSysroot(context.Background(), PrepareOptions{Sysroot: sysroot})
	if err == nil {
		t.Fatal("expected error: FindmntTargets is required")
	}
}

// --- barrier 3: symlink-safe deletion -------------------------------------

func TestSymlinkEscapeStepTwoRefused(t *testing.T) {
	victim := t.TempDir()
	writeFile(t, victim, "initramfs-file", "must survive")
	sysroot := t.TempDir()
	// Prior-disk content: /sysroot/opt is an ABSOLUTE symlink. With chroot
	// semantics it must resolve INSIDE the sysroot, never to the real /.
	if err := os.Symlink(victim, filepath.Join(sysroot, "opt")); err != nil {
		t.Fatal(err)
	}
	// A preserve below the symlinked dir drives step 2 (sibling deletion)
	// through the symlink.
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveOnly:   ParsePatterns([]string{"opt/keep"}),
	})
	if err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(victim, "initramfs-file")); serr != nil {
		t.Fatalf("SYMLINK ESCAPE: file outside sysroot deleted: %v", serr)
	}
}

func TestSymlinkEscapeStepThreeRefused(t *testing.T) {
	victim := t.TempDir()
	writeFile(t, victim, "keep-me", "must survive")
	sysroot := t.TempDir()
	if err := os.Symlink(victim, filepath.Join(sysroot, "cache")); err != nil {
		t.Fatal(err)
	}
	// preserve-empty on the symlinked dir drives step 3 (content wipe)
	// through the symlink.
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveEmpty:  ParsePatterns([]string{"cache"}),
	})
	if err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(victim, "keep-me")); serr != nil {
		t.Fatalf("SYMLINK ESCAPE: file outside sysroot deleted: %v", serr)
	}
}

func TestDotDotPatternCannotClimbOutOfSysroot(t *testing.T) {
	parent := t.TempDir()
	sysroot := filepath.Join(parent, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, parent, "outside/precious", "must survive")
	// Hostile/buggy filter: ".." components must clamp at the sysroot.
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveOnly:   ParsePatterns([]string{"../outside/keep"}),
		PreserveEmpty:  ParsePatterns([]string{"../outside"}),
	})
	if err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(parent, "outside/precious")); serr != nil {
		t.Fatalf("'..' ESCAPE: file outside sysroot deleted: %v", serr)
	}
}

// --- barrier 4: dry-run touches nothing ------------------------------------

func TestDryRunLeavesTreeByteIdentical(t *testing.T) {
	sysroot := stageSysroot(t)
	writeFile(t, sysroot, "data/customer.db", "precious")
	before := treeFingerprint(t, sysroot)
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:               sysroot,
		DryRun:                true,
		FindmntTargets:        testMounts(sysroot),
		PersistentMountpoints: []string{"/data"},
		PreserveOnly:          ParsePatterns([]string{"etc/hosts"}),
		PreserveEmpty:         ParsePatterns([]string{"home", "newdir"}),
		NoNewFiles:            ParsePatterns([]string{"proc"}),
	})
	if err != nil {
		t.Fatalf("PrepareSysroot dry-run: %v", err)
	}
	after := treeFingerprint(t, sysroot)
	if before != after {
		t.Fatalf("dry-run mutated the sysroot:\nBEFORE:\n%s\nAFTER:\n%s", before, after)
	}
}

// --- barrier 5: mount guard in steps 2/3 ----------------------------------

func TestMountedSiblingSurvivesStepTwo(t *testing.T) {
	sysroot := t.TempDir()
	writeFile(t, sysroot, "var/keep/k", "k")
	writeFile(t, sysroot, "var/mnt/on-volume", "mounted volume content")
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot, filepath.Join(sysroot, "var", "mnt")),
		PreserveOnly:   ParsePatterns([]string{"var/keep"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(sysroot, "var/mnt/on-volume")); serr != nil {
		t.Fatalf("mounted sibling deleted by step 2: %v", serr)
	}
}

func TestMountedChildSurvivesStepThree(t *testing.T) {
	sysroot := t.TempDir()
	writeFile(t, sysroot, "scratch/mnt/on-volume", "mounted volume content")
	writeFile(t, sysroot, "scratch/wipeme", "x")
	err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot, filepath.Join(sysroot, "scratch", "mnt")),
		PreserveEmpty:  ParsePatterns([]string{"scratch"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(sysroot, "scratch/mnt/on-volume")); serr != nil {
		t.Fatalf("mounted child deleted by step 3: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(sysroot, "scratch/wipeme")); serr == nil {
		t.Fatal("unmounted, non-persistent child should still be wiped")
	}
}

// --- resolveWithinSysroot unit coverage ------------------------------------

func TestResolveWithinSysrootChrootSemantics(t *testing.T) {
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "usr/lib/modules"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Absolute symlink re-rooted at sysroot: lib -> /usr/lib.
	if err := os.Symlink("/usr/lib", filepath.Join(sysroot, "lib")); err != nil {
		t.Fatal(err)
	}
	got, exists, err := resolveWithinSysroot(sysroot, "lib/modules")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("lib/modules should resolve to an existing dir")
	}
	want := filepath.Join(sysroot, "usr/lib/modules")
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestResolveWithinSysrootLoopBounded(t *testing.T) {
	sysroot := t.TempDir()
	// a -> b -> a: must terminate with an error, not hang.
	if err := os.Symlink("b", filepath.Join(sysroot, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(sysroot, "b")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveWithinSysroot(sysroot, "a/x"); err == nil {
		t.Fatal("symlink loop must error")
	}
}
