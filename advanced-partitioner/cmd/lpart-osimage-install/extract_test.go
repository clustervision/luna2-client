package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runFullExtract runs install_mode=full end to end on the classic path: the
// image is downloaded from a local server with the real curl and unpacked with
// the real tar and lbzip2 into a temporary sysroot.
func runFullExtract(t *testing.T, files map[string]string, extraArgs ...string) (stderr string, code int) {
	t.Helper()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "image")
	for rel, body := range files {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	tarball := filepath.Join(tmp, "image.tar.bz2")
	if out, err := exec.Command("tar", "-I", "lbzip2", "-cf", tarball, "-C", src, ".").CombinedOutput(); err != nil { //nolint:gosec // test paths
		t.Fatalf("build image: %v: %s", err, out)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, tarball)
	}))
	t.Cleanup(srv.Close)
	// The download lands in /tmp under the image's name (DownloadOptions.CacheDir).
	image := "lpart-test-" + filepath.Base(filepath.Dir(tmp)) + ".tar.bz2"
	t.Cleanup(func() { _ = os.Remove(filepath.Join("/tmp", image)) })

	ic := filepath.Join(tmp, "install-context.json")
	if err := os.WriteFile(ic, []byte(`{"install_mode":"full","provision_method":"http"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	// PrepareSysroot requires the sysroot in the mount list.
	stubs := filepath.Join(tmp, "bin")
	writeStub(t, stubs, "findmnt", "echo /; echo "+sysroot)
	args := append([]string{
		"--install-context", ic,
		"--sysroot", sysroot,
		"--skip-sysroot-check",
		"--luna-install-script", writeInstallScript(t, `LUNA_TOKEN="x"`+"\n"),
		"--luna-token", "x",
		"--osimage-file", image,
		"--luna-url", srv.URL,
		"--stream-extract", "never",
	}, extraArgs...)
	return runEnv(t, args...)
}

// An image without /etc/os-release fails the same way on every attempt: the
// classic path parks it (exit 3), as the sync path does.
func TestFullExtractWithoutOSReleaseIsFatal(t *testing.T) {
	stderr, code := runFullExtract(t, map[string]string{"usr/bin/true": "x", "etc/hostname": "n"})
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stderr:\n%s", code, stderr)
	}
}

// Control: the same path with os-release installs (exit 0).
func TestFullExtractWithOSReleaseSucceeds(t *testing.T) {
	stderr, code := runFullExtract(t, map[string]string{"usr/bin/true": "x", "etc/os-release": "ID=test\n"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
}

// writeStub puts an executable sh script named name first on PATH.
func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	if !strings.HasPrefix(os.Getenv("PATH"), dir+":") {
		t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	}
}
