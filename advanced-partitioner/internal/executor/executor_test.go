package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// silentLogger builds a Logger writing to io.Discard for test runs.
func silentLogger(t *testing.T) *txlog.Logger {
	t.Helper()
	l, err := txlog.Init(txlog.Options{
		LogFile: "",
		Stderr:  false,
	})
	if err != nil {
		t.Fatalf("Init logger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestFormatArgvQuoting(t *testing.T) {
	cases := []struct {
		name string
		path string
		args []string
		want string
	}{
		{"plain", "sgdisk", []string{"--zap-all", "/dev/sda"}, "sgdisk --zap-all /dev/sda"},
		{"spaces", "echo", []string{"hello world"}, "echo 'hello world'"},
		{"empty", "true", []string{""}, "true ''"},
		{"single-quote", "bash", []string{"-c", "echo it's fine"}, `bash -c 'echo it'\''s fine'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatArgv(tc.path, tc.args...)
			if got != tc.want {
				t.Fatalf("FormatArgv(%q, %v) = %q, want %q", tc.path, tc.args, got, tc.want)
			}
		})
	}
}

func TestRealRunnerSuccess(t *testing.T) {
	r := New(Options{Logger: silentLogger(t)})
	res, err := r.Run(context.Background(), CommandSpec{
		Path:          "/usr/bin/true",
		CaptureStdout: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.TimedOut {
		t.Fatalf("TimedOut = true, want false")
	}
}

func TestRealRunnerFailure(t *testing.T) {
	r := New(Options{Logger: silentLogger(t)})
	_, err := r.Run(context.Background(), CommandSpec{
		Path: "/usr/bin/false",
	})
	if err == nil {
		t.Fatal("expected error from /usr/bin/false")
	}
	var exitErr *exec.ExitError
	// The wrapped error should still be inspectable as *exec.ExitError.
	if !errors.As(err, &exitErr) {
		t.Fatalf("error chain missing *exec.ExitError: %v", err)
	}
}

func TestRealRunnerCapture(t *testing.T) {
	r := New(Options{Logger: silentLogger(t)})
	res, err := r.Run(context.Background(), CommandSpec{
		Path:          "/usr/bin/printf",
		Args:          []string{"%s", "hello"},
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "hello" {
		t.Fatalf("Stdout = %q, want %q", string(res.Stdout), "hello")
	}
}

func TestRealRunnerStdin(t *testing.T) {
	r := New(Options{Logger: silentLogger(t)})
	res, err := r.Run(context.Background(), CommandSpec{
		Path:          "/usr/bin/cat",
		Stdin:         bytes.NewBufferString("ping"),
		CaptureStdout: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "ping" {
		t.Fatalf("Stdout = %q, want %q", string(res.Stdout), "ping")
	}
}

func TestRealRunnerTimeoutKillsCommand(t *testing.T) {
	r := New(Options{Logger: silentLogger(t)})
	start := time.Now()
	res, err := r.Run(context.Background(), CommandSpec{
		Path:    "/usr/bin/sleep",
		Args:    []string{"5"},
		Timeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !res.TimedOut {
		t.Fatalf("TimedOut = false; want true (err: %v)", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("runner did not honour timeout; took %v", time.Since(start))
	}
}

func TestDryRunRunnerDoesNotExecute(t *testing.T) {
	r := New(Options{DryRun: true, Logger: silentLogger(t)})
	res, err := r.Run(context.Background(), CommandSpec{
		// /usr/bin/false would fail if actually invoked.
		Path: "/usr/bin/false",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("DryRun ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestFakeRunnerRecordsAndMatches(t *testing.T) {
	fake := NewFakeRunner(FakeResponse{Stdout: []byte("default")})
	fake.AddMatch(FakeMatch{
		Path:       "lsblk",
		ArgsPrefix: []string{"-J"},
		Response:   FakeResponse{Stdout: []byte(`{"blockdevices":[]}`)},
	})
	fake.AddMatch(FakeMatch{
		Path:       "sgdisk",
		ArgsPrefix: []string{"--zap-all"},
		Response:   FakeResponse{Err: errors.New("boom")},
	})

	ctx := context.Background()

	res, err := fake.Run(ctx, CommandSpec{Path: "lsblk", Args: []string{"-J", "-d"}})
	if err != nil {
		t.Fatalf("lsblk: %v", err)
	}
	if !bytes.Contains(res.Stdout, []byte("blockdevices")) {
		t.Fatalf("lsblk match missed; got %q", res.Stdout)
	}

	_, err = fake.Run(ctx, CommandSpec{Path: "sgdisk", Args: []string{"--zap-all", "/dev/sda"}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("sgdisk match should fail with boom, got %v", err)
	}

	res, err = fake.Run(ctx, CommandSpec{Path: "blkid"})
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if string(res.Stdout) != "default" {
		t.Fatalf("default response not returned: %q", res.Stdout)
	}

	calls := fake.Calls()
	if len(calls) != 3 {
		t.Fatalf("Calls() len = %d, want 3", len(calls))
	}
	if calls[0].Path != "lsblk" || calls[1].Path != "sgdisk" || calls[2].Path != "blkid" {
		t.Fatalf("recorded paths wrong: %+v", calls)
	}
}

func TestChrootRewritesArgv(t *testing.T) {
	fake := NewFakeRunner(FakeResponse{})
	fake.AddMatch(FakeMatch{
		Path:       "chroot",
		ArgsPrefix: []string{"/sysroot", "/usr/sbin/efibootmgr"},
		Response:   FakeResponse{Stdout: []byte("ok")},
	})

	res, err := fake.Run(context.Background(), CommandSpec{
		Path:   "/usr/sbin/efibootmgr",
		Args:   []string{"--bootnum", "0001", "--delete-bootnum"},
		Chroot: "/sysroot",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "ok" {
		t.Fatalf("chroot match did not fire; got %q", res.Stdout)
	}

	got := fake.Calls()[0]
	// The recorded spec preserves the original Path/Chroot; effective
	// rewriting is internal. Verify that the matcher saw the rewritten
	// argv by inspecting the response, not the call recording.
	if got.Chroot != "/sysroot" {
		t.Fatalf("recorded Chroot lost: %q", got.Chroot)
	}
}

func TestRedactArgsMasksToken(t *testing.T) {
	in := []string{"--interface", "eth0", "-H", "x-access-tokens: eyJhbG.secret.sig", "-s", "https://host/x"}
	out := redactArgs(in)
	for _, a := range out {
		if strings.Contains(a, "eyJhbG") || strings.Contains(a, "secret") {
			t.Fatalf("token leaked through redaction: %q", a)
		}
	}
	if out[3] != "x-access-tokens: REDACTED" {
		t.Fatalf("redacted form wrong: %q", out[3])
	}
	// Non-sensitive args unchanged.
	if out[0] != "--interface" || out[1] != "eth0" || out[5] != "https://host/x" {
		t.Fatalf("non-sensitive args mutated: %v", out)
	}
}

// A failed command's log entry must carry the HEAD of stderr as well as the
// tail. The first error is normally the cause and the tail the cascade: a tar
// that runs out of space prints hundreds of ENOSPC lines and then a handful of
// deferred-ACL warnings, and a tail-only excerpt sent every reader to the ACLs.
func TestRunCmdFailedLogsStderrHeadAndTail(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "executor.log")
	l, err := txlog.Init(txlog.Options{LogFile: logPath, Stderr: false})
	if err != nil {
		t.Fatalf("Init logger: %v", err)
	}
	r := New(Options{Logger: l})
	script := `echo FIRST-ENOSPC >&2
i=0
while [ $i -lt 400 ]; do echo "tar: /mnt/sysimage/f$i: Cannot write: No space left on device" >&2; i=$((i+1)); done
echo LAST-ACL >&2
exit 2`
	if _, err := r.Run(context.Background(), CommandSpec{
		Path:          "/bin/sh",
		Args:          []string{"-c", script},
		CaptureStderr: true,
	}); err == nil {
		t.Fatal("expected the script's exit 2 to be an error")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close logger: %v", err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(b)
	for _, want := range []string{"FIRST-ENOSPC", "LAST-ACL", "bytes omitted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log entry lost %q; got:\n%s", want, got)
		}
	}
}

// A short stderr is logged whole, with no omission marker.
func TestRunCmdFailedLogsShortStderrWhole(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "executor.log")
	l, err := txlog.Init(txlog.Options{LogFile: logPath, Stderr: false})
	if err != nil {
		t.Fatalf("Init logger: %v", err)
	}
	r := New(Options{Logger: l})
	if _, err := r.Run(context.Background(), CommandSpec{
		Path:          "/bin/sh",
		Args:          []string{"-c", "echo pvcreate: device is busy >&2; exit 5"},
		CaptureStderr: true,
	}); err == nil {
		t.Fatal("expected the script's exit 5 to be an error")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close logger: %v", err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "pvcreate: device is busy") {
		t.Fatalf("short stderr missing from the log entry:\n%s", got)
	}
	if strings.Contains(got, "bytes omitted") {
		t.Fatalf("short stderr must not be abridged:\n%s", got)
	}
}
