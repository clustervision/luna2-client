package osimage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Phase 11 re-entrant scenarios for PrepareSysroot. Running prepare a
// second time on an already-prepared sysroot must not fail: the
// preserved paths are still present, the wipe pass finds nothing new
// to remove, and the preserve-empty dirs are re-cleared (no-op since
// they're already empty).

func TestReentrantPrepareSysrootOnAlreadyCleanState(t *testing.T) {
	sysroot := t.TempDir()
	// Stage a "previously prepared" sysroot:
	// - etc/hosts preserved file is present
	// - home is empty (preserve-empty)
	// - proc is present (no-new-files target)
	// - no extra top-level entries (everything else was already wiped)
	for _, sub := range []string{"etc", "home", "proc"} {
		if err := os.MkdirAll(filepath.Join(sysroot, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc/hosts"), []byte("127.0.0.1 localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := PrepareSysroot(context.Background(), PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveOnly:   ParsePatterns([]string{"etc/hosts"}),
		PreserveEmpty:  ParsePatterns([]string{"home"}),
		NoNewFiles:     ParsePatterns([]string{"proc"}),
	}); err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}

	// All three targets should still be present.
	for _, p := range []string{"etc", "etc/hosts", "home", "proc"} {
		if _, err := os.Stat(filepath.Join(sysroot, p)); err != nil {
			t.Fatalf("%s lost: %v", p, err)
		}
	}
}

func TestReentrantPrepareSysrootTwiceInARow(t *testing.T) {
	// Two consecutive PrepareSysroot calls with the same filter set.
	// The second call should succeed and leave the preserved tree
	// untouched.
	sysroot := t.TempDir()
	for _, sub := range []string{"etc", "var/log/sub", "home/u1", "junk"} {
		if err := os.MkdirAll(filepath.Join(sysroot, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc/hosts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "var/log/messages"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := PrepareOptions{
		Sysroot:        sysroot,
		FindmntTargets: testMounts(sysroot),
		PreserveOnly:   ParsePatterns([]string{"etc/hosts", "var/log/***"}),
		PreserveEmpty:  ParsePatterns([]string{"home"}),
	}
	for i := 0; i < 2; i++ {
		if err := PrepareSysroot(context.Background(), opts); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}
	// First-pass wipes junk; second-pass nothing-to-do.
	if _, err := os.Stat(filepath.Join(sysroot, "junk")); err == nil {
		t.Fatal("junk should be removed after first pass")
	}
	// Preserved tree intact.
	for _, p := range []string{"etc/hosts", "var/log/messages"} {
		if _, err := os.Stat(filepath.Join(sysroot, p)); err != nil {
			t.Fatalf("preserved %s lost: %v", p, err)
		}
	}
	// home is still empty.
	entries, err := os.ReadDir(filepath.Join(sysroot, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preserve-empty home should be empty; got %v", entries)
	}
}
