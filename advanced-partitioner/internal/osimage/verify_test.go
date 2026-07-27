package osimage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyExtract(t *testing.T) {
	good := t.TempDir()
	mustMk(t, filepath.Join(good, "etc"))
	if err := os.WriteFile(filepath.Join(good, "etc/os-release"),
		[]byte("NAME=Ubuntu\nPRETTY_NAME=\"Ubuntu 26.04 LTS\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustMk(t, filepath.Join(good, "usr"))
	if err := VerifyExtract(nil, good); err != nil {
		t.Fatalf("populated sysroot should verify, got: %v", err)
	}

	// Empty sysroot (the silent Ubuntu failure) must be caught.
	if err := VerifyExtract(nil, t.TempDir()); err == nil {
		t.Fatal("empty sysroot must fail verification")
	}
}

func mustMk(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
}
