package cli

import (
	"bytes"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
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

// A panic exits 3 with the stack and an internal-error verdict as the last
// line, never the runtime's exit 2 (the safety-refusal code).
func TestExitOnPanicExitsFatal(t *testing.T) {
	code := -1
	osExit = func(c int) { code = c }
	defer func() { osExit = os.Exit }()
	var stderr bytes.Buffer
	func() {
		defer ExitOnPanic(&stderr, "lpart-x")
		panic("boom")
	}()
	if code != ExitFatalConfigCode {
		t.Fatalf("exit = %d, want %d", code, ExitFatalConfigCode)
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if want := "lpart-x: fatal_config=internal-error internal error: boom"; lines[len(lines)-1] != want {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], want)
	}
	if !strings.Contains(stderr.String(), "goroutine ") {
		t.Errorf("no stack in stderr:\n%s", stderr.String())
	}
}

// Every lpart tool defers ExitOnPanic as the first statement of main.
func TestEveryToolMainExitsFatalOnPanic(t *testing.T) {
	mains, err := filepath.Glob("../../cmd/*/main.go")
	if err != nil || len(mains) == 0 {
		t.Fatalf("no tool mains found: %v", err)
	}
	for _, path := range mains {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ok := false
		for _, d := range f.Decls {
			fn, isFn := d.(*ast.FuncDecl)
			if !isFn || fn.Name.Name != "main" || fn.Recv != nil || len(fn.Body.List) == 0 {
				continue
			}
			if ds, isDefer := fn.Body.List[0].(*ast.DeferStmt); isDefer {
				if sel, isSel := ds.Call.Fun.(*ast.SelectorExpr); isSel && sel.Sel.Name == "ExitOnPanic" {
					ok = true
				}
			}
		}
		if !ok {
			t.Errorf("%s: main does not start with defer cli.ExitOnPanic", path)
		}
	}
}
