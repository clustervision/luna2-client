// Command lpart-tui is the standalone install dashboard (ADR 0009). It is the
// renderer half of the decoupled TUI: luna-installer.sh launches it once, around
// the whole install.sh run, and it draws one live screen by reading the event
// bus that every step — the lpart-* workers and the bash steps (bmcsetup, the
// pre/part/post admin columns, the legacy tail) — emits to.
//
// It owns the console for the lifetime of the install; the work happens in
// other processes. Reading the bus over a held-open O_RDWR fd means the many
// short-lived producers can each open→write→close without ending the stream.
//
// Usage:
//
//	lpart-tui [--bus /run/lpart/events] [--log-file F] [--no-tui]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/eventbus"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installerui"
)

func main() {
	defer cli.ExitOnPanic(os.Stderr, "lpart-tui")
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lpart-tui:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		bus      = flag.String("bus", eventbus.DefaultPath, "event bus FIFO to read")
		noAlt    = flag.Bool("no-alt-screen", false, "disable the Bubble Tea alt-screen (useful for piping)")
		logFile  = flag.String("log-file", "", "append captured stdout/stderr to this file (raw, no ANSI)")
		quiet    = flag.Bool("quiet-console", true, "lower /proc/sys/kernel/printk while the TUI owns the console (Linux only)")
		noTUI    = flag.Bool("no-tui", false, "stream plain scrolling output instead of the TUI (also kernel cmdline lpart.notui / $LPART_NOTUI)")
		loghost  = flag.String("loghost", "", "controller rsyslog target host[:port] (default: luna.loghost from cmdline; :514 if no port)")
		nodeName = flag.String("node-name", "", "node FQDN stamped as the syslog hostname (default: luna.hostname or luna.node from cmdline)")
		phase    = flag.String("phase", "install", "tag for forwarded syslog lines")
	)
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open the bus held-open (O_RDWR) so the stream never hits EOF as individual
	// producers come and go; closing it on ctx cancel unblocks the read loop.
	reader, err := eventbus.OpenReader(*bus)
	if err != nil {
		return fmt.Errorf("open bus %s: %w", *bus, err)
	}
	defer func() { _ = reader.Close() }()
	go func() {
		<-ctx.Done()
		_ = reader.Close()
	}()

	events := eventbus.Read(ctx, reader)

	// No pre-seeded plan: the dashboard grows its stage list from the bus as
	// stages first start. (engine.NewState seeds the in-process pipeline's plan,
	// which does not apply here.)
	initial := engine.NewState().Snapshot()
	initial.Stages = nil

	exit, err := installerui.Render(ctx, cancel, events, initial, installerui.Reboot, installerui.Config{
		LogFile:      *logFile,
		Loghost:      *loghost,
		NodeName:     *nodeName,
		Phase:        *phase,
		Plain:        installerui.PlainMode(*noTUI),
		NoAlt:        *noAlt,
		QuietConsole: *quiet,
	})
	if err != nil {
		return err
	}
	if exit != 0 {
		os.Exit(exit)
	}
	return nil
}
