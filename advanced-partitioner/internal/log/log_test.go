package log

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCapturedLogger builds a Logger writing to an in-memory buffer for
// assertions. Avoids the os.Stderr noise of Init() during tests.
func newCapturedLogger(t *testing.T) (*Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	base := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := &Logger{
		slog:     base,
		baseSlog: base,
		out:      buf,
	}
	return l, buf
}

func TestSetStageAttachesStageAttribute(t *testing.T) {
	l, buf := newCapturedLogger(t)
	l.SetStage("partition")
	l.Info("ready")

	got := buf.String()
	if !strings.Contains(got, "stage=partition") {
		t.Fatalf("expected stage=partition in output, got: %s", got)
	}
	if l.Stage() != "partition" {
		t.Fatalf("Stage() = %q, want %q", l.Stage(), "partition")
	}
}

func TestCompactHandlerFormat(t *testing.T) {
	buf := &bytes.Buffer{}
	h := newCompactHandler(buf, slog.LevelDebug)
	l := slog.New(h).With(slog.String("stage", "cleanup"))

	l.Info("run_cmd", slog.String("path", "findmnt"), slog.Int("exit_code", 0))
	l.Warn("run_cmd failed", slog.String("path", "findmnt"), slog.Int("exit_code", 1))

	got := buf.String()
	wantLines := []string{
		"[cleanup] run_cmd path=findmnt exit_code=0",
		"[cleanup] WARN: run_cmd failed path=findmnt exit_code=1",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// No structured prefix labels.
	for _, forbidden := range []string{"time=", "level=", "msg="} {
		if strings.Contains(got, forbidden) {
			t.Errorf("compact output must not contain %q; got:\n%s", forbidden, got)
		}
	}
}

func TestCompactHandlerSuppressesStageUpdated(t *testing.T) {
	buf := &bytes.Buffer{}
	h := newCompactHandler(buf, slog.LevelDebug)
	l := slog.New(h)
	// SetStage emits a "stage updated" Info — verify it is dropped.
	l.Info("stage updated", slog.String("stage", "partitioning"))
	l.Info("payload", slog.String("stage", "partitioning"))

	got := buf.String()
	if strings.Contains(got, "stage updated") {
		t.Errorf(`compact handler must suppress the "stage updated" record; got:\n%s`, got)
	}
	if !strings.Contains(got, "[partitioning] payload") {
		t.Errorf("expected [partitioning] prefix on next record; got:\n%s", got)
	}
}

func TestCompactHandlerQuotesValueWithSpaces(t *testing.T) {
	buf := &bytes.Buffer{}
	h := newCompactHandler(buf, slog.LevelDebug)
	l := slog.New(h)
	l.Info("run_cmd", slog.String("args", "-rn -M /sysroot/dev"))
	got := buf.String()
	if !strings.Contains(got, `args="-rn -M /sysroot/dev"`) {
		t.Errorf("expected quoted value; got:\n%s", got)
	}
}

func TestSetStageDoesNotAccumulate(t *testing.T) {
	// Regression: consecutive SetStage calls must replace, not stack.
	// Previously every call did `l.slog = l.slog.With(stage=...)` so the
	// emitted record carried stage=startup stage=installer_check ...
	l, buf := newCapturedLogger(t)
	l.SetStage("startup")
	l.SetStage("installer_check")
	l.SetStage("command_checks")
	buf.Reset()
	l.Info("after stages")

	got := buf.String()
	if strings.Count(got, "stage=") != 1 {
		t.Fatalf("expected exactly one stage= attribute, got: %s", got)
	}
	if !strings.Contains(got, "stage=command_checks") {
		t.Fatalf("expected stage=command_checks, got: %s", got)
	}
}

func TestStageBannerAndUserInfoReachUserOutput(t *testing.T) {
	l, buf := newCapturedLogger(t)
	l.StageBanner("Cleaning selected storage")
	l.UserInfo("Resolved %d disk(s) for set '%s'", 2, "os")

	got := buf.String()
	if !strings.Contains(got, "==> Cleaning selected storage") {
		t.Fatalf("missing stage banner in output: %s", got)
	}
	if !strings.Contains(got, "Resolved 2 disk(s) for set 'os'") {
		t.Fatalf("missing user info in output: %s", got)
	}
}

func TestKVConvenience(t *testing.T) {
	l, buf := newCapturedLogger(t)
	l.KV("config_path", "/tmp/storage.json")

	got := buf.String()
	if !strings.Contains(got, "config_path=/tmp/storage.json") {
		t.Fatalf("expected config_path=... in output, got: %s", got)
	}
}

func TestInitWritesToFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Init(Options{
		Tool:   "lpart-test",
		LogDir: dir,
		Stderr: false, // suppress test noise
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	l.UserInfo("hello world")
	l.Info("ready", slog.String("key", "value"))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "lpart-test.log")) //nolint:gosec // test file in t.TempDir
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "USER: hello world") {
		t.Fatalf("expected USER: prefix in file, got: %s", s)
	}
	if !strings.Contains(s, "ready") {
		t.Fatalf("expected ready record in file, got: %s", s)
	}
}

func TestInitWithLogFileOverride(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom.log")
	l, err := Init(Options{
		LogFile: custom,
		Stderr:  false,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	l.Info("after override")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("custom log file not created: %v", err)
	}
}

func TestCloseNilLoggerSafe(t *testing.T) {
	var l *Logger
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil: %v", err)
	}
}

// Ensure the discard sink path doesn't blow up when both stderr and file
// are off — a Logger that only writes to io.Discard is a legitimate
// configuration for tools that only care about explicit UserInfo output.
func TestInitDiscardSink(t *testing.T) {
	l, err := Init(Options{
		Stderr: false,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		_ = l.Close()
	})
	// slog should not panic with the discard handler.
	l.Info("noop")
}

// Sanity: the slog handler honours the level filter.
func TestLevelFilter(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	l := &Logger{
		slog: slog.New(handler),
		out:  io.Discard,
	}
	l.Info("hidden")
	l.Warn("visible")

	got := buf.String()
	if strings.Contains(got, "hidden") {
		t.Fatalf("info should be filtered at warn level: %s", got)
	}
	if !strings.Contains(got, "visible") {
		t.Fatalf("warn should pass at warn level: %s", got)
	}
	_ = context.Background()
}
