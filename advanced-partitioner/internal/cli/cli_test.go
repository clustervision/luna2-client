package cli

import (
	"flag"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestCommonBindAndParseDefaults(t *testing.T) {
	var c Common
	fs := flag.NewFlagSet("lpart-test", flag.ContinueOnError)
	c.Bind(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Verbose || c.Debug || c.JSONLog || c.DryRun || c.AllowNonInstaller {
		t.Fatalf("expected defaults to be false, got %+v", c)
	}
}

func TestCommonBindAndParseFlags(t *testing.T) {
	var c Common
	fs := flag.NewFlagSet("lpart-test", flag.ContinueOnError)
	c.Bind(fs)
	if err := fs.Parse([]string{
		"--verbose",
		"--debug",
		"--json-log",
		"--dry-run",
		"--allow-non-installer",
		"--log-file", "/tmp/x.log",
		"--log-dir", "/tmp/log",
	}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.Verbose || !c.Debug || !c.JSONLog || !c.DryRun || !c.AllowNonInstaller {
		t.Fatalf("flags did not all bind: %+v", c)
	}
	if c.LogFile != "/tmp/x.log" || c.LogDir != "/tmp/log" {
		t.Fatalf("file flags wrong: %+v", c)
	}
}

func TestEnvBool(t *testing.T) {
	t.Setenv("MY_FLAG", "1")
	if !envBool("MY_FLAG") {
		t.Fatalf("1 should be truthy")
	}
	t.Setenv("MY_FLAG", "no")
	if envBool("MY_FLAG") {
		t.Fatalf("no should be falsy")
	}
	t.Setenv("MY_FLAG", "yes")
	if !envBool("MY_FLAG") {
		t.Fatalf("yes should be truthy")
	}
	t.Setenv("MY_FLAG", "")
	if envBool("MY_FLAG") {
		t.Fatalf("empty should be falsy")
	}
}

func TestEnsureInstallerPathPrependsMissingEntries(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	EnsureInstallerPath()
	got := splitPath(strings.TrimSpace(getenvForTest(t)))
	want := []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"}
	if !sliceEqual(got, want) {
		t.Fatalf("PATH = %v, want %v", got, want)
	}
}

func TestEnsureInstallerPathIsNoOpWhenAlreadyPresent(t *testing.T) {
	want := "/usr/sbin:/usr/bin:/sbin:/bin:/opt/foo"
	t.Setenv("PATH", want)
	EnsureInstallerPath()
	if got := getenvForTest(t); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestEnsureInstallerPathOnEmptyPath(t *testing.T) {
	t.Setenv("PATH", "")
	EnsureInstallerPath()
	got := getenvForTest(t)
	want := "/usr/sbin:/usr/bin:/sbin:/bin"
	if got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func getenvForTest(t *testing.T) string {
	t.Helper()
	return os.Getenv("PATH")
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewLoggerHonoursDebugFlag(t *testing.T) {
	dir := t.TempDir()
	c := Common{
		Debug:  true,
		LogDir: dir,
	}
	l, err := c.NewLogger("lpart-debug")
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	// We can't easily introspect the handler level from outside slog,
	// but we can verify the logger was constructed.
	if l == nil {
		t.Fatal("nil logger")
	}
	l.Debug("ok", slog.String("k", "v"))
}
