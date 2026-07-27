// Package log provides the structured logger used by every installer-go
// binary. It is a thin wrapper around log/slog with three deliberate
// affordances that the Bash tree relied on:
//
//  1. A "stage" attribute that prefixes every log line until it is changed.
//     The Bash equivalent is CURRENT_STAGE / set_stage in lib/common.sh.
//  2. UserInfo / StageBanner output that goes to stderr in plain text
//     regardless of the configured handler, mirroring print_user_info /
//     print_stage_banner in lib/logging.sh. The same lines also reach the
//     log file (prefixed with "USER:") so operator-visible output is
//     captured for post-mortem inspection.
//  3. A duplicate sink: every record written to the logger is also written
//     to a per-tool log file under LogDir, the same convention the Bash
//     tools used (/tmp/install_files/logs/<tool>.log).
//
// This package has no external dependencies and does not import os/exec —
// only the executor package may do that.
package log

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Format controls how records are serialised.
type Format int

const (
	// Text is human-readable, slog's TextHandler.
	Text Format = iota
	// JSON is one JSON object per line, slog's JSONHandler.
	JSON
)

// Options controls how Init constructs the logger.
type Options struct {
	// Tool is the name of the binary, used to derive the log file name
	// (e.g. "lpart-storage-prepare" => "<LogDir>/lpart-storage-prepare.log").
	// If empty, no log file is written.
	Tool string

	// LogDir is the directory for the per-tool log file. Defaults to
	// /tmp/install_files/logs when empty (matching the Bash default).
	LogDir string

	// LogFile, if non-empty, overrides LogDir/Tool.log entirely.
	LogFile string

	// Format selects the on-disk and stderr handler format.
	Format Format

	// Level is the lowest level that gets emitted.
	Level slog.Level

	// Stderr, when true, also writes records to os.Stderr. Defaults to
	// true via Init when zero-valued; callers that want a silent logger
	// must build it directly.
	Stderr bool
}

// DefaultLogDir is the directory the Bash tools also used.
const DefaultLogDir = "/tmp/install_files/logs"

// Logger wraps a *slog.Logger with stage state and helpers for the
// user-visible banner output.
type Logger struct {
	mu       sync.Mutex
	stage    string
	slog     *slog.Logger // active logger (carries the current stage= attr)
	baseSlog *slog.Logger // stage-less base — SetStage rebuilds slog from here
	out      io.Writer    // stderr-equivalent for UserInfo / StageBanner
	logFile  *os.File     // duplicate sink, may be nil
}

// Init creates a Logger from Options. The returned Logger's Close method
// flushes any backing log file.
func Init(opts Options) (*Logger, error) {
	if !opts.Stderr {
		// Default to stderr on; callers wanting silence build directly.
		opts.Stderr = true
	}

	logDir := opts.LogDir
	if logDir == "" {
		logDir = DefaultLogDir
	}

	var logFile *os.File
	logPath := opts.LogFile
	if logPath == "" && opts.Tool != "" {
		logPath = filepath.Join(logDir, opts.Tool+".log")
	}
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
		// G304: logPath is operator-supplied via --log-file. The binary
		// already runs as root in the dracut installer environment, so
		// path-traversal mitigations would be theatre. Surface intent
		// here so future readers don't try to "fix" this.
		//
		//nolint:gosec // operator-supplied log path; binary runs as root
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return nil, fmt.Errorf("open log file %s: %w", logPath, err)
		}
		logFile = f
	}

	// Stderr gets a compact human-friendly handler ([stage] msg key=val)
	// in the default text mode. File keeps a full text or JSON sink for
	// post-mortem grep. When --json-log is on, stderr also goes JSON so
	// the operator's programmatic consumer gets the structured stream
	// they asked for.
	handlerOpts := &slog.HandlerOptions{Level: opts.Level}
	var children []slog.Handler
	if opts.Stderr {
		if opts.Format == JSON {
			children = append(children, slog.NewJSONHandler(os.Stderr, handlerOpts))
		} else {
			children = append(children, newCompactHandler(os.Stderr, opts.Level))
		}
	}
	if logFile != nil {
		switch opts.Format {
		case JSON:
			children = append(children, slog.NewJSONHandler(logFile, handlerOpts))
		default:
			children = append(children, slog.NewTextHandler(logFile, handlerOpts))
		}
	}
	var handler slog.Handler
	switch len(children) {
	case 0:
		handler = slog.NewTextHandler(io.Discard, handlerOpts)
	case 1:
		handler = children[0]
	default:
		handler = &fanoutHandler{children: children}
	}

	base := slog.New(handler)
	l := &Logger{
		slog:     base,
		baseSlog: base,
		out:      os.Stderr,
		logFile:  logFile,
	}
	return l, nil
}

// Close flushes and releases the log file, if any. Safe to call with nil
// receiver.
func (l *Logger) Close() error {
	if l == nil || l.logFile == nil {
		return nil
	}
	err := l.logFile.Close()
	l.logFile = nil
	return err
}

// Slog returns the underlying *slog.Logger. Use this when integrating
// with code that expects a slog.Logger directly.
func (l *Logger) Slog() *slog.Logger { return l.slog }

// SetStage updates the active stage. All subsequent log records emitted
// through this Logger carry stage=<name>. Mirrors set_stage in
// lib/common.sh.
func (l *Logger) SetStage(name string) {
	l.mu.Lock()
	l.stage = name
	// Rebuild from baseSlog so consecutive SetStage calls do not stack
	// stage= attributes (bash set_stage just overwrites CURRENT_STAGE).
	l.slog = l.baseSlog.With(slog.String("stage", name))
	l.mu.Unlock()
	l.Info("stage updated")
}

// Stage returns the current stage name.
func (l *Logger) Stage() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stage
}

// Info logs at INFO level. Convenience around l.Slog().Info(...).
func (l *Logger) Info(msg string, args ...any) {
	l.slog.Log(context.Background(), slog.LevelInfo, msg, args...)
}

// Warn logs at WARN level.
func (l *Logger) Warn(msg string, args ...any) {
	l.slog.Log(context.Background(), slog.LevelWarn, msg, args...)
}

// Error logs at ERROR level.
func (l *Logger) Error(msg string, args ...any) {
	l.slog.Log(context.Background(), slog.LevelError, msg, args...)
}

// Debug logs at DEBUG level (typically gated by --debug).
func (l *Logger) Debug(msg string, args ...any) {
	l.slog.Log(context.Background(), slog.LevelDebug, msg, args...)
}

// KV is a shorthand for emitting a single key=value INFO record. Mirrors
// log_kv in lib/logging.sh.
func (l *Logger) KV(key string, value any) {
	l.Info(key, slog.Any(key, value))
}

// UserInfo writes operator-visible text to stderr and mirrors it into the
// log file with the "USER:" prefix the Bash tools used. Use this for
// messages that should appear in the partscript output even at the lowest
// verbosity.
func (l *Logger) UserInfo(format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	_, _ = fmt.Fprintln(l.out, msg)
	if l.logFile != nil {
		_, _ = fmt.Fprintln(l.logFile, "USER: "+msg)
	}
}

// StageBanner prints "==> <name>" to operator output. Mirrors
// print_stage_banner in lib/logging.sh.
func (l *Logger) StageBanner(name string) {
	l.UserInfo("==> %s", name)
}

// ─── slog handlers ──────────────────────────────────────────────────

// compactHandler is a slog.Handler that emits operator-friendly
// "[stage] msg key=val" lines to stderr without the time= / level= /
// msg= prefixes the default TextHandler uses. WARN and ERROR records
// get a leading level tag inline after [stage]. The synthetic
// "stage updated" record emitted by Logger.SetStage is suppressed
// here — the [stage] prefix that immediately follows on the next
// record already carries the same signal.
//
// The file sink keeps the full slog text/JSON format for post-mortem
// debugging; this handler is for the human reading partscript output
// while the installer runs.
type compactHandler struct {
	w        io.Writer
	minLevel slog.Level
	attrs    []slog.Attr
}

func newCompactHandler(w io.Writer, lvl slog.Level) *compactHandler {
	return &compactHandler{w: w, minLevel: lvl}
}

func (h *compactHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= h.minLevel
}

func (h *compactHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "stage updated" {
		return nil
	}
	var buf bytes.Buffer

	// Stage = accumulated With() attrs, overridden by record-level attrs.
	stage := ""
	for _, a := range h.attrs {
		if a.Key == "stage" {
			stage = a.Value.String()
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "stage" {
			stage = a.Value.String()
		}
		return true
	})
	if stage != "" {
		fmt.Fprintf(&buf, "[%s] ", stage)
	}
	if r.Level >= slog.LevelWarn {
		fmt.Fprintf(&buf, "%s: ", r.Level.String())
	}
	buf.WriteString(r.Message)

	writeKV := func(a slog.Attr) {
		if a.Key == "stage" {
			return
		}
		fmt.Fprintf(&buf, " %s=%s", a.Key, formatCompactValue(a.Value))
	}
	for _, a := range h.attrs {
		writeKV(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeKV(a)
		return true
	})
	buf.WriteByte('\n')
	_, err := h.w.Write(buf.Bytes())
	return err
}

func (h *compactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := *h
	nh.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	nh.attrs = append(nh.attrs, h.attrs...)
	nh.attrs = append(nh.attrs, attrs...)
	return &nh
}

func (h *compactHandler) WithGroup(_ string) slog.Handler { return h }

func formatCompactValue(v slog.Value) string {
	s := v.String()
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"='") {
		return strconv.Quote(s)
	}
	return s
}

// fanoutHandler delegates each record to several child handlers so the
// same Logger can feed both a compact stderr sink and a structured
// file sink without duplicating attrs / format selection.
type fanoutHandler struct {
	children []slog.Handler
}

func (h *fanoutHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, c := range h.children {
		if c.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, c := range h.children {
		if !c.Enabled(ctx, r.Level) {
			continue
		}
		// Clone so each child can iterate Attrs() independently;
		// slog.Record is otherwise single-pass.
		if err := c.Handle(ctx, r.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := &fanoutHandler{children: make([]slog.Handler, len(h.children))}
	for i, c := range h.children {
		nh.children[i] = c.WithAttrs(attrs)
	}
	return nh
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	nh := &fanoutHandler{children: make([]slog.Handler, len(h.children))}
	for i, c := range h.children {
		nh.children[i] = c.WithGroup(name)
	}
	return nh
}
