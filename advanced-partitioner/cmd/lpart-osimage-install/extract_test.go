package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/osimage"
)

// runFullExtract runs install_mode=full end to end on the classic path: the
// image is downloaded from a local server with the real curl and unpacked with
// the real tar and lbzip2 into a temporary sysroot.
func runFullExtract(t *testing.T, files map[string]string, extraArgs ...string) (stderr, logBody string, code int) {
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
	return runEnvWithLog(t, args...)
}

// An image without /etc/os-release fails the same way on every attempt: the
// classic path parks it (exit 3), as the sync path does.
func TestFullExtractWithoutOSReleaseIsFatal(t *testing.T) {
	stderr, _, code := runFullExtract(t, map[string]string{"usr/bin/true": "x", "etc/hostname": "n"})
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stderr:\n%s", code, stderr)
	}
}

// Control: the same path with os-release installs (exit 0).
func TestFullExtractWithOSReleaseSucceeds(t *testing.T) {
	stderr, _, code := runFullExtract(t, map[string]string{"usr/bin/true": "x", "etc/os-release": "ID=test\n"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
}

// A full os filesystem fails the same way on every attempt: the classic
// extract, and the streamed extract with its classic fallback, park (exit 3).
// tar's message is the only place ENOSPC shows.
func TestFullExtractOnAFullFilesystemIsFatal(t *testing.T) {
	realTar, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("no tar")
	}
	for _, stream := range []string{"never", "always"} {
		t.Run("stream-extract="+stream, func(t *testing.T) {
			writeStub(t, filepath.Join(t.TempDir(), "bin"), "tar",
				`for a; do [ "$a" = -xf ] && { echo "tar: ./usr/bin/true: Cannot write: No space left on device" >&2; exit 2; }; done; exec `+realTar+` "$@"`)
			stderr, logBody, code := runFullExtract(t, map[string]string{"usr/bin/true": "x", "etc/os-release": "ID=test\n"},
				"--stream-extract", stream)
			if stream == "always" && !strings.Contains(logBody, "streamed extract failed; falling back") {
				t.Fatalf("the streamed extract did not run and fail first; log:\n%s", logBody)
			}
			if code != 3 || !strings.Contains(stderr, "fatal_config=no-space") {
				t.Fatalf("exit = %d, want 3 (no-space); stderr:\n%s", code, stderr)
			}
		})
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

// A decoded target keeps its trailing space: a data set mounted at "data "
// is a mount like any other and keeps its files.
func TestPrepareSysrootKeepsMountEndingInSpace(t *testing.T) {
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "data "), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "data ", "precious"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing := "/\n" + sysroot + "\n" + sysroot + `/data\x20` + "\n"
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte(listing)}})
	if err := osimage.PrepareSysroot(context.Background(), osimage.PrepareOptions{
		Sysroot: sysroot, FindmntTargets: findmntTargets(f),
	}); err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "data ", "precious")); err != nil {
		t.Fatalf("the data set mounted at %q was wiped: %v", "data ", err)
	}
}

// findmnt -r prints a space, a backslash and non-ASCII bytes as \xNN: a data
// set mounted at such a path must still count as mounted and keep its files.
func TestPrepareSysrootKeepsMountsFindmntEscapes(t *testing.T) {
	sysroot := t.TempDir()
	dirs := []string{"my data", `a\b`, "dä ta"}
	listing := "/\n" + sysroot + "\n"
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(sysroot, d), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sysroot, d, "precious"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	listing += sysroot + `/my\x20data` + "\n" + sysroot + `/a\x5cb` + "\n" + sysroot + `/d\xc3\xa4\x20ta` + "\n"
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte(listing)}})
	if err := osimage.PrepareSysroot(context.Background(), osimage.PrepareOptions{
		Sysroot: sysroot, FindmntTargets: findmntTargets(f),
	}); err != nil {
		t.Fatalf("PrepareSysroot: %v", err)
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(sysroot, d, "precious")); err != nil {
			t.Errorf("mounted %q was wiped: %v", d, err)
		}
	}
}
