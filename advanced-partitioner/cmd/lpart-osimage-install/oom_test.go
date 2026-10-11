package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProc points the OOM classification at temp /proc files.
func fakeProc(t *testing.T, oomKill string) {
	t.Helper()
	dir := t.TempDir()
	vm := filepath.Join(dir, "vmstat")
	mi := filepath.Join(dir, "meminfo")
	if oomKill != "" {
		if err := os.WriteFile(vm, []byte("pgfault 123\noom_kill "+oomKill+"\nnr_free_pages 5\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(mi, []byte("MemTotal:        4189448 kB\nMemFree:          100000 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldVM, oldMI := procVmstat, procMeminfo
	procVmstat, procMeminfo = vm, mi
	t.Cleanup(func() { procVmstat, procMeminfo = oldVM, oldMI })
}

var tarOOM = errors.New(`tar extract: tar -I lbzip2 -xf /tmp/ubuntu26-image.tar.bz2 -C /sysroot: exit status 2`)

func TestOOMKillsReadsVmstat(t *testing.T) {
	fakeProc(t, "7")
	if got := oomKills(); got != 7 {
		t.Fatalf("oomKills = %d, want 7", got)
	}
	if got := procField(procMeminfo, "MemTotal"); got != 4189448 {
		t.Fatalf("MemTotal = %d", got)
	}
	fakeProc(t, "") // no vmstat file: unknown, never "out of memory"
	if got := oomKills(); got != -1 {
		t.Fatalf("oomKills without vmstat = %d, want -1", got)
	}
	if outOfMemory(-1) {
		t.Fatal("an unknown counter must not count as out of memory")
	}
}

// The Ubuntu 26.04 memboot case: the OOM killer stopped lbzip2, tar failed
// "Unexpected EOF". The same image on the same RAM fails identically on every
// attempt, so this parks with the reason instead of retrying for ever.
func TestExtractKilledByOOMParks(t *testing.T) {
	fakeProc(t, "1")
	var buf bytes.Buffer
	if rc := extractFailure(&buf, tarOOM, 0, "tmpfs", ""); rc != 3 {
		t.Fatalf("rc = %d, want 3 (fatal_config)", rc)
	}
	out := buf.String()
	for _, want := range []string{"fatal_config=out-of-memory", "OOM killer", "node RAM 4091 MiB", "provision_method http"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr misses %q:\n%s", want, out)
		}
	}
}

func TestExtractFailureWithoutOOMStaysRetryable(t *testing.T) {
	fakeProc(t, "4")
	var buf bytes.Buffer
	if rc := extractFailure(&buf, tarOOM, 4, "tmpfs", ""); rc != 1 {
		t.Fatalf("rc = %d, want 1 (retryable): no OOM kill happened", rc)
	}
	if strings.Contains(buf.String(), "out-of-memory") {
		t.Fatalf("classified as out of memory without an OOM kill:\n%s", buf.String())
	}
}

func TestDownloadKilledByOOMParks(t *testing.T) {
	fakeProc(t, "3")
	var buf bytes.Buffer
	if rc := downloadFailure(&buf, errors.New("curl: signal: killed"), 2, "xfs"); rc != 3 {
		t.Fatalf("rc = %d, want 3", rc)
	}
	if out := buf.String(); !strings.Contains(out, "fatal_config=out-of-memory") || strings.Contains(out, "memboot") {
		t.Fatalf("disk install: want the generic out-of-memory advice:\n%s", out)
	}
	buf.Reset()
	if rc := downloadFailure(&buf, errors.New("curl: (28) timeout"), 3, "xfs"); rc != 1 {
		t.Fatalf("a download failure without an OOM kill must stay retryable, rc = %d", rc)
	}
}

func TestNoSpaceStillWinsOverOOM(t *testing.T) {
	fakeProc(t, "9")
	var buf bytes.Buffer
	if rc := extractFailure(&buf, errors.New("tar: ./usr/lib/x: No space left on device"), 1, "xfs", ""); rc != 3 {
		t.Fatalf("rc = %d, want 3", rc)
	}
	if !strings.Contains(buf.String(), "fatal_config=no-space") {
		t.Fatalf("want no-space:\n%s", buf.String())
	}
}

func TestOOMReportsTarballSize(t *testing.T) {
	fakeProc(t, "2")
	tb := filepath.Join(t.TempDir(), "img.tar.bz2")
	if err := os.WriteFile(tb, make([]byte, 3<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	extractFailure(&buf, tarOOM, 1, "tmpfs", tb)
	if !strings.Contains(buf.String(), "tarball 3 MiB in /tmp (RAM)") {
		t.Fatalf("tarball size missing:\n%s", buf.String())
	}
}
