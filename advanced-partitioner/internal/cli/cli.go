// Package cli provides the flag set shared by every installer-go binary.
//
// Each tool builds a *flag.FlagSet via NewToolFlagSet and calls
// Common.Bind to attach the bookkeeping flags shared by every binary:
// --verbose, --debug, --json-log, --log-file, --log-dir, --dry-run,
// --version, --allow-non-installer. Tool-specific flags are added by the
// caller on the same FlagSet before Parse.
//
// The goal is one source of truth for "how does the operator turn on
// JSON logging?" and "how does the installer escape the installer-only
// guard during local testing?" — and a single place to update when we
// add another global knob.
package cli

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Common holds the parsed values of the flags every binary exposes.
type Common struct {
	Verbose           bool
	Debug             bool
	JSONLog           bool
	DryRun            bool
	ShowVersion       bool
	AllowNonInstaller bool
	LogFile           string
	LogDir            string
}

// Bind attaches the common flags to the supplied FlagSet. Callers must
// still call fs.Parse(os.Args[1:]).
func (c *Common) Bind(fs *flag.FlagSet) {
	fs.BoolVar(&c.Verbose, "verbose", false, "Verbose output")
	fs.BoolVar(&c.Debug, "debug", false, "Debug-level logging (implies --verbose)")
	fs.BoolVar(&c.JSONLog, "json-log", false, "Emit log records as JSON")
	fs.BoolVar(&c.DryRun, "dry-run", false, "Do not invoke any external command; log intended actions only")
	fs.BoolVar(&c.ShowVersion, "version", false, "Print version and exit")
	fs.BoolVar(&c.AllowNonInstaller, "allow-non-installer",
		envBool("NEWTREE_SKIP_INSTALLER_CHECK"),
		"Skip the 'root=luna' /proc/cmdline guard (testing only)")
	fs.StringVar(&c.LogFile, "log-file", "", "Override per-tool log file path")
	fs.StringVar(&c.LogDir, "log-dir", txlog.DefaultLogDir, "Directory for per-tool log files")
}

// NewLogger constructs a Logger for the named tool using the parsed
// common-flag values.
func (c *Common) NewLogger(tool string) (*txlog.Logger, error) {
	level := slog.LevelInfo
	switch {
	case c.Debug:
		level = slog.LevelDebug
	case c.Verbose:
		level = slog.LevelInfo
	}
	format := txlog.Text
	if c.JSONLog {
		format = txlog.JSON
	}
	return txlog.Init(txlog.Options{
		Tool:    tool,
		LogDir:  c.LogDir,
		LogFile: c.LogFile,
		Format:  format,
		Level:   level,
		Stderr:  true,
	})
}

// envBool returns true if the named env var is set to a truthy value
// ("1", "true", "yes"). Mirrors the Bash convention used throughout
// /root/NEWTREE.
func envBool(name string) bool {
	switch os.Getenv(name) {
	case "1", "true", "yes", "TRUE", "YES":
		return true
	default:
		return false
	}
}

// NewToolFlagSet returns a *flag.FlagSet configured for the named tool.
// Errors print to os.Stderr and parse errors return rather than exit the
// process; that gives main() a chance to surface a structured exit code.
func NewToolFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// installerPathPrefix mirrors the `export PATH=...` at the top of every
// bash binary under /root/NEWTREE/bin/. Luna's initramfs launches
// install.sh with PATH=/usr/bin:/bin only, so without this prefix the
// installer cannot find blkid/sgdisk/mdadm/etc. in /sbin or /usr/sbin.
const installerPathPrefix = "/usr/sbin:/usr/bin:/sbin:/bin"

// EnsureInstallerPath prepends the installer PATH prefix to the
// inherited PATH so that exec.LookPath finds blkid, sgdisk, mdadm and
// the rest of the storage tooling regardless of the launcher's PATH.
// Entries already present in PATH are not duplicated.
//
// Mirrors `export PATH="/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"` in
// /root/NEWTREE/bin/lpart-storage-prepare line 11 (and equivalents).
func EnsureInstallerPath() {
	existing := os.Getenv("PATH")
	have := map[string]bool{}
	for _, p := range splitPath(existing) {
		have[p] = true
	}
	var add []string
	for _, p := range splitPath(installerPathPrefix) {
		if !have[p] {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return
	}
	newPath := joinPath(append(add, splitPath(existing)...))
	_ = os.Setenv("PATH", newPath)
}

func splitPath(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func joinPath(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ":"
		}
		out += p
	}
	return out
}

// version is populated by Makefile via -ldflags at build time. It
// defaults to "dev" so unit tests and local builds work without ldflags.
var version = "dev"

// Version returns the binary's version string.
func Version() string { return version }

// ExitSafetyRefusal is the canonical (log line + exit code) helper for
// Phase 14's exit-code 2 = safety refusal contract. Tools call it when
// they would otherwise touch a persistent-pinned set; the message goes
// to stderr with the `safety_refusal=<reason>` marker the future Go
// installer UI uses to distinguish "things broke" (exit 1) from "do
// NOT continue without operator review" (exit 2).
//
//	0 = success
//	1 = generic failure
//	2 = safety refusal (persistent-related)
func ExitSafetyRefusal(stderr io.Writer, tool, reason, format string, args ...any) int {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(stderr, "%s: safety_refusal=%s %s\n", tool, reason, msg)
	return 2
}

// ExitFatalConfigCode is the exit code of a non-retryable configuration
// error: the same input fails identically on every attempt (an invalid
// disklayout, an unparsable filter). lpart-node-installer maps it, like 2, to
// its own exit 3 so the install wrapper parks instead of retrying every 30 s.
const ExitFatalConfigCode = 3

// ExitFatalConfig logs a non-retryable configuration error with the
// fatal_config=<reason> marker and returns ExitFatalConfigCode.
func ExitFatalConfig(stderr io.Writer, tool, reason, format string, args ...any) int {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(stderr, "%s: fatal_config=%s %s\n", tool, reason, msg)
	return ExitFatalConfigCode
}
