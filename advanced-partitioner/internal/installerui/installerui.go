// Package installerui is the shared live-render wiring for the installer: it
// takes a source of engine.InstallerEvent, fans in kernel messages, tees to a
// log file, forwards to the controller rsyslog, and renders either the Bubble
// Tea dashboard or plain scrolling output. Both entry points use it:
//
//   - lpart-node-installer (the in-process --phase pipeline), and
//   - lpart-tui (ADR 0009: the standalone renderer that reads the event bus).
//
// The only difference between them is where the events come from; everything
// from "fan in kmsg" onward is identical, so it lives here once.
package installerui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/tui"
)

// Config tunes the sinks and the renderer. Loghost/NodeName are the raw flag
// values; Render resolves them against the kernel cmdline.
type Config struct {
	LogFile      string // append raw captured output here (no ANSI); "" disables
	Loghost      string // controller rsyslog host[:port]; "" → luna.loghost from cmdline
	NodeName     string // node FQDN for the syslog HOSTNAME; "" → luna.hostname/luna.node
	Phase        string // pre|part|post, tags forwarded syslog lines
	Plain        bool   // stream plain scrolling output instead of the TUI
	NoAlt        bool   // disable the Bubble Tea alt-screen
	QuietConsole bool   // lower kernel printk while the TUI owns the console
	// StrictExit makes "stream ended / cancelled without a terminal
	// installer event" render as FAILURE instead of success. Set by
	// lpart-node-installer (its exit gates the provisioning wrapper); the
	// standalone lpart-tui viewer keeps the lenient default. Note the
	// node-installer's authoritative exit source is pipeline.Result() —
	// this only keeps the rendered outcome consistent with it.
	StrictExit bool
}

// Render wires the standard live sinks around src and renders. It installs a
// SIGINT/SIGTERM→cancel handler, quiets the console (TUI mode only), fans in
// kernel messages, optionally tees to a file and forwards to rsyslog, then runs
// the dashboard or the plain printer. Returns the installer exit code (0
// success, 1 failure) — the same contract for both renderers.
func Render(ctx context.Context, cancel context.CancelFunc, src <-chan engine.InstallerEvent, initial model.Installer, reboot func(), cfg Config) (int, error) {
	// Kernel printks scroll the framebuffer console and break the alt-screen
	// layout. Quiet them while the TUI runs; in plain mode we want the scroll.
	if cfg.QuietConsole && !cfg.Plain {
		if restore, err := engine.QuietConsole(); err == nil {
			defer restore()
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Capture kernel messages (ipmi/driver errors etc.) into the UI.
	kmsgEvents := make(chan engine.InstallerEvent, 256)
	go engine.PollKmsg(ctx, kmsgEvents)
	events := engine.FanIn(1024, src, kmsgEvents)

	if cfg.LogFile != "" {
		teed, err := engine.TeeToFile(ctx, cfg.LogFile, events)
		if err != nil {
			return 1, fmt.Errorf("log file: %w", err)
		}
		events = teed
	}
	if host := ResolveLoghost(cfg.Loghost); host != "" {
		if fwd, err := engine.ForwardSyslog(ctx, host, ResolveNodeName(cfg.NodeName), cfg.Phase, events); err == nil {
			events = fwd
		}
	}

	if cfg.Plain {
		return runPlain(ctx, events, cfg.StrictExit), nil
	}

	m := tui.NewModel(ctx, cancel, events, initial, reboot)
	if cfg.StrictExit {
		m = m.WithStrictExit()
	}
	opts := []tea.ProgramOption{}
	if !cfg.NoAlt {
		opts = append(opts, tea.WithAltScreen())
	}
	finalModel, err := tea.NewProgram(m, opts...).Run()
	if err != nil {
		return 1, err
	}
	if mm, ok := finalModel.(tui.Model); ok {
		return mm.FinalExitCode(), nil
	}
	return 0, nil
}

// Reboot performs the F10 sync + hard restart. Only meaningful as root in the
// install environment; harmless elsewhere (the syscall returns EPERM).
func Reboot() {
	syscall.Sync()
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
}

// PlainMode reports whether to skip the TUI: the --no-tui flag, the LPART_NOTUI
// env, the kernel cmdline lpart.notui token, OR a terminal too small for the
// TUI to render (BE-U1: a serial-only console often reports 0x0, on which the
// bubbletea TUI shows only "terminal too small" and the operator can never
// drive the install -- observed on SUSE/Ubuntu UEFI serial installs). Falling
// back to plain scrolling output keeps the install drivable everywhere.
func PlainMode(noTUIFlag bool) bool {
	return noTUIFlag || os.Getenv("LPART_NOTUI") != "" || cmdlineHasNoTUI() || terminalTooSmall()
}

// terminalTooSmall reports whether stdout's window is smaller than the TUI's
// hard minimum (60x18) -- including a 0x0 or unqueryable (non-tty / serial)
// console. Best-effort: if the size cannot be read AND stdout is not a
// character device we assume plain; a readable size >= the minimum keeps the TUI.
func terminalTooSmall() bool {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return true // cannot size it (pipe/serial with no winsize) -> plain
	}
	const hardMinCols, hardMinRows = 60, 18
	return int(ws.Col) < hardMinCols || int(ws.Row) < hardMinRows
}

// runPlain renders the install as plain scrolling output: stage banners, "$ argv"
// command lines, child stdout/stderr, and kernel warnings. Returns the installer
// exit code: 0 success, 1 failure, 3 fatal/safety-refusal failure. Under
// strict semantics a cancelled context (SIGINT/SIGTERM mid-install) or a
// stream that ends without a terminal installer event is a FAILURE — an
// interrupted install must never report success (FTA Cluster B, B3).
func runPlain(ctx context.Context, events <-chan engine.InstallerEvent, strict bool) int {
	exit := 0
	terminal := false
	for {
		select {
		case <-ctx.Done():
			if strict && !terminal {
				fmt.Println("installer interrupted before completion")
				return 1
			}
			return exit
		case ev, ok := <-events:
			if !ok {
				if strict && !terminal {
					fmt.Println("installer event stream ended without a terminal event")
					return 1
				}
				return exit
			}
			if ev.SyslogOnly {
				continue
			}
			switch ev.Type {
			case engine.EvStageStarted:
				fmt.Printf("==> %s\n", strings.TrimSpace(ev.Stage+" "+ev.Message))
			case engine.EvStageFailed:
				fmt.Printf("==> [FAILED] %s: %s\n", ev.Stage, ev.Message)
			case engine.EvCommandStarted:
				if ce, ok := ev.Data.(engine.CommandEvent); ok {
					fmt.Printf("$ %s\n", strings.Join(ce.Argv, " "))
				}
			case engine.EvCommandStdout, engine.EvCommandStderr,
				engine.EvRawStdout, engine.EvRawStderr:
				fmt.Println(ev.Message)
			case engine.EvKernel:
				fmt.Printf("[kernel] %s\n", ev.Message)
			case engine.EvInstallerDone:
				terminal = true
			case engine.EvInstallerFailed:
				terminal = true
				exit = tui.FailureExitCode(ev.Data)
				if ev.Message != "" {
					fmt.Printf("installer failed: %s\n", ev.Message)
				}
			}
		}
	}
}

// ResolveLoghost returns the explicit flag, else luna.loghost from the cmdline,
// ensuring a :514 default port.
func ResolveLoghost(flagVal string) string {
	h := flagVal
	if h == "" {
		h = cmdlineValue("luna.loghost")
	}
	if h == "" {
		return ""
	}
	if !strings.Contains(h, ":") {
		h += ":514"
	}
	return h
}

// ResolveNodeName returns the explicit flag, else the node FQDN from the cmdline
// (luna.hostname preferred, luna.node fallback).
func ResolveNodeName(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if h := cmdlineValue("luna.hostname"); h != "" {
		return h
	}
	return cmdlineValue("luna.node")
}

// cmdlineHasNoTUI reports whether the kernel cmdline carries lpart.notui (bare
// or lpart.notui=1). Best-effort: a missing /proc/cmdline means "no".
func cmdlineHasNoTUI() bool {
	for _, tok := range cmdlineTokens() {
		if tok == "lpart.notui" || strings.HasPrefix(tok, "lpart.notui=") {
			v := strings.TrimPrefix(tok, "lpart.notui=")
			return v != "0" && v != "off" && v != "false"
		}
	}
	return false
}

// cmdlineValue returns the value of key=val on /proc/cmdline, or "".
func cmdlineValue(key string) string {
	for _, tok := range cmdlineTokens() {
		if v, ok := strings.CutPrefix(tok, key+"="); ok {
			return v
		}
	}
	return ""
}

func cmdlineTokens() []string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return nil
	}
	return strings.Fields(string(data))
}
