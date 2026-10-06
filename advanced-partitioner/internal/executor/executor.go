// Package executor is the single point of truth for running external
// commands. It is the only package in installer-go that imports os/exec.
// Every other package — partitioning, lvm, mounts, bootloader, etc. —
// must obtain a Runner and call Run.
//
// This package mirrors the responsibilities that lib/commands.sh has in
// the Bash tree: run_cmd, capture_cmd, run_chroot_cmd, dry-run
// semantics, structured logging. Three runners are provided:
//
//   - Real:  invokes the command through os/exec.
//   - DryRun: logs the intended invocation and returns success without
//     executing.
//   - Fake:   records every call and returns canned results; used in
//     unit tests.
//
// Chroot handling: if CommandSpec.Chroot is non-empty, the runner
// transparently prepends "chroot <path>" — matching the Bash
// run_chroot_cmd contract. We do not use syscall.Chroot because the
// installer needs to invoke chroot-only binaries (efibootmgr, dracut)
// that live under /sysroot/usr/sbin, and an in-process chroot would
// affect every goroutine and the running process for the rest of its
// life.
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// CommandSpec is a fully-described command invocation. Callers must fill
// Path (with PATH lookup not yet performed) and Args (which excludes the
// command name); the runner constructs the argv with Path as argv[0].
type CommandSpec struct {
	// Path is the executable name or absolute path. Lookups follow the
	// process PATH unless Path contains a slash.
	Path string

	// Args are the arguments excluding argv[0].
	Args []string

	// Env, when non-nil, replaces the inherited environment. Use nil to
	// inherit the runner process's env (the common case).
	Env []string

	// Stdin, when non-nil, is connected to the child's stdin.
	Stdin io.Reader

	// WorkDir is the child's working directory (defaults to the runner's).
	WorkDir string

	// Chroot, when non-empty, makes the runner invoke
	// "chroot <Chroot> <Path> <Args...>" instead. Required when the
	// command being run lives only inside the /sysroot tree.
	Chroot string

	// Timeout caps the wall-clock duration of the command. Zero means no
	// runner-imposed cap; the caller's context.Context still applies.
	Timeout time.Duration

	// CaptureStdout / CaptureStderr opt the runner into recording the
	// child's output streams into CommandResult. When false the streams
	// are still consumed (to avoid blocking the child) but discarded.
	CaptureStdout bool
	CaptureStderr bool

	// Quiet demotes the "run_cmd failed" log entry from WARN to DEBUG
	// when the command exits non-zero. Use for probes that are expected
	// to fail in normal flow (e.g. findmnt -M <target> "is this mount
	// point in use yet?" before a fresh bind-mount). The error is still
	// returned to the caller — only the log level changes.
	Quiet bool
}

// CommandResult is what every Runner returns to the caller.
type CommandResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Duration time.Duration
	// TimedOut is true if the command was killed because Timeout or the
	// caller's ctx fired before the child exited.
	TimedOut bool
}

// Runner is what every installer-go package uses to run external commands.
type Runner interface {
	Run(ctx context.Context, spec CommandSpec) (CommandResult, error)
}

// New returns a Runner appropriate for the given Options. When DryRun is
// true, the returned Runner runs observational commands (lsblk, findmnt,
// blkid, pvs/vgs/lvs, mdadm --detail/--examine, etc.) for real and stubs
// every destructive command with an INFO log line. This matches the
// shell predecessor's behaviour, where read-only probes are direct
// `$(...)` substitutions and only run_cmd invocations honour DRY_RUN.
func New(opts Options) Runner {
	real := &realRunner{log: opts.Logger}
	if opts.DryRun {
		return &dryRunRunner{log: opts.Logger, real: real}
	}
	return real
}

// Options controls Runner construction.
type Options struct {
	// DryRun selects the no-op runner.
	DryRun bool
	// Logger receives one record per Run. Required; passing nil is a bug.
	Logger *txlog.Logger
}

// redactArgs returns a copy of args with credential values masked, for safe
// logging/error messages. Surfaced by the live rsyslog forward: the LUNA_TOKEN
// (a JWT) was reaching cluster-messages verbatim inside curl `-H
// "x-access-tokens: <jwt>"` args. Never log a secret.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = redactSecret(a)
	}
	return out
}

// redactSecret masks the value after a known sensitive header/flag prefix in a
// single argument, leaving the rest intact.
func redactSecret(s string) string {
	for _, pfx := range []string{"x-access-tokens:", "Authorization:", "authorization:"} {
		if i := strings.Index(s, pfx); i >= 0 {
			return s[:i+len(pfx)] + " REDACTED"
		}
	}
	return s
}

// FormatArgv renders a command + args list as a shell-quoted string,
// matching format_argv() in lib/commands.sh. Useful when emitting log
// lines a human will read.
func FormatArgv(path string, args ...string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(path))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !needsQuoting(s) {
		return s
	}
	// Single-quote escape: ' -> '\''
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsQuoting(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == '/', r == ':', r == '=', r == '@', r == '+', r == ',':
		default:
			return true
		}
	}
	return false
}

// --- Real runner ----------------------------------------------------------

type realRunner struct {
	log *txlog.Logger
}

func (r *realRunner) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	path, args := effectiveArgv(spec)
	logFields := []any{
		slog.String("path", path),
		slog.Any("args", redactArgs(args)),
	}
	if spec.Chroot != "" {
		logFields = append(logFields, slog.String("chroot", spec.Chroot))
	}
	r.log.Slog().Info("run_cmd", logFields...)

	cctx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		cctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	//nolint:gosec // G204: the installer must launch sgdisk/mkfs/grub etc. by design.
	// The executor is the sanctioned chokepoint for that; callers are
	// trusted code that constructs commands from validated config.
	cmd := exec.CommandContext(cctx, path, args...)
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env
	}
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	if spec.CaptureStdout {
		cmd.Stdout = &stdoutBuf
	} else {
		cmd.Stdout = io.Discard
	}
	if spec.CaptureStderr {
		cmd.Stderr = &stderrBuf
	} else {
		cmd.Stderr = io.Discard
	}

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	res := CommandResult{
		Duration: elapsed,
	}
	if spec.CaptureStdout {
		res.Stdout = stdoutBuf.Bytes()
	}
	if spec.CaptureStderr {
		res.Stderr = stderrBuf.Bytes()
	}
	if cctx.Err() != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		level := slog.LevelWarn
		if spec.Quiet {
			level = slog.LevelDebug
		}
		// Include the captured stderr so command failures are diagnosable from
		// the install log (rsyslog stream). Without this, "exit status 5" gives
		// no clue why (e.g. an LVM/pvcreate rejection).
		r.log.Slog().Log(cctx, level, "run_cmd failed",
			slog.String("path", path),
			slog.Int("exit_code", res.ExitCode),
			slog.Duration("duration", elapsed),
			slog.Bool("timed_out", res.TimedOut),
			slog.String("err", runErr.Error()),
			slog.String("stderr", stderrExcerpt(res.Stderr)),
		)
		return res, fmt.Errorf("%s: %w", FormatArgv(path, redactArgs(args)...), runErr)
	}

	r.log.Slog().Debug("run_cmd ok",
		slog.String("path", path),
		slog.Duration("duration", elapsed),
	)
	return res, nil
}

// stderrExcerptEnd bounds each end of the excerpt kept in the log entry.
const stderrExcerptEnd = 1000

// stderrExcerpt trims the captured stderr and, when it is longer than both
// ends together, keeps the HEAD and the TAIL with a count of what was dropped.
// The head is what matters: a command's first error is normally the cause and
// the tail the cascade, so a tail-only excerpt points the reader at the wrong
// thing (a tar out of space prints hundreds of ENOSPC lines, then a handful of
// deferred-ACL warnings).
func stderrExcerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= 2*stderrExcerptEnd {
		return s
	}
	return fmt.Sprintf("%s\n... %d bytes omitted ...\n%s",
		s[:stderrExcerptEnd], len(s)-2*stderrExcerptEnd, s[len(s)-stderrExcerptEnd:])
}

// --- DryRun runner --------------------------------------------------------

// observationalCommands are commands that the dry-run runner forwards to
// the real exec rather than stubbing. They are read-only system probes —
// the installer's bash predecessor invokes them via direct `$(...)`
// shell substitution, bypassing its run_cmd dry-run gate, so a Go port
// that stubs them would produce a less-functional dry-run than bash.
//
// Anything not on this list is treated as destructive and only logged.
var observationalCommands = map[string]struct{}{
	"lsblk":    {},
	"findmnt":  {},
	"blkid":    {},
	"readlink": {},
	"pvs":      {},
	"vgs":      {},
	"lvs":      {},
	"hostname": {},
	"cat":      {},
	"stat":     {},
	"ls":       {},
	"true":     {},
	"false":    {},
}

// dryRunRunner runs observational commands through the real exec and
// only logs (without executing) destructive ones.
type dryRunRunner struct {
	log  *txlog.Logger
	real *realRunner
}

func (r *dryRunRunner) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	path, args := effectiveArgv(spec)

	// mdadm is a special case: --detail / --examine / --scan are
	// observational; --create / --stop / --zero-superblock /
	// --assemble / --grow / --manage are destructive. Distinguish by
	// the first argument.
	if path == "mdadm" && len(args) > 0 {
		switch args[0] {
		case "--detail", "--examine", "--scan", "-D", "-E", "-Q":
			return r.real.Run(ctx, spec)
		}
	}

	if _, ok := observationalCommands[path]; ok {
		return r.real.Run(ctx, spec)
	}

	r.log.Slog().Info("dry-run",
		slog.String("path", path),
		slog.Any("args", args),
		slog.String("chroot", spec.Chroot),
	)
	return CommandResult{}, nil
}

// --- Fake runner (for tests) ----------------------------------------------

// FakeResponse is one canned reply to feed into a FakeRunner.
type FakeResponse struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

// FakeMatch is the predicate side of a fake response. If both Path and
// ArgsPrefix are empty, the response matches any call. Otherwise the
// runner takes the first response whose Path matches the call's effective
// path and whose ArgsPrefix is a prefix of the call's effective args.
type FakeMatch struct {
	Path       string
	ArgsPrefix []string
	Response   FakeResponse
}

// FakeRunner records every call and returns canned results. Concurrency-safe.
type FakeRunner struct {
	mu       sync.Mutex
	matchers []FakeMatch
	defResp  FakeResponse
	calls    []CommandSpec
}

// NewFakeRunner returns a FakeRunner that returns the given default
// response for any call that does not match a registered FakeMatch.
func NewFakeRunner(def FakeResponse) *FakeRunner {
	return &FakeRunner{defResp: def}
}

// AddMatch registers a canned response for calls whose effective Path
// equals m.Path and whose effective args start with m.ArgsPrefix.
// Matches are tested in registration order; the first hit wins.
func (f *FakeRunner) AddMatch(m FakeMatch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matchers = append(f.matchers, m)
}

// Calls returns a copy of every recorded invocation.
func (f *FakeRunner) Calls() []CommandSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CommandSpec, len(f.calls))
	copy(out, f.calls)
	return out
}

// Run records the call and returns the matching canned response.
func (f *FakeRunner) Run(_ context.Context, spec CommandSpec) (CommandResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, spec)
	path, args := effectiveArgv(spec)
	resp := f.defResp
	for _, m := range f.matchers {
		if m.Path != "" && m.Path != path {
			continue
		}
		if !hasPrefix(args, m.ArgsPrefix) {
			continue
		}
		resp = m.Response
		break
	}
	f.mu.Unlock()

	return CommandResult{
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: resp.ExitCode,
	}, resp.Err
}

func hasPrefix(args, prefix []string) bool {
	if len(prefix) > len(args) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

// effectiveArgv resolves the (path, args) actually launched when honouring
// Chroot. Exposed only within the package.
func effectiveArgv(spec CommandSpec) (string, []string) {
	if spec.Chroot == "" {
		return spec.Path, append([]string(nil), spec.Args...)
	}
	args := make([]string, 0, 2+len(spec.Args))
	args = append(args, spec.Chroot, spec.Path)
	args = append(args, spec.Args...)
	return "chroot", args
}
