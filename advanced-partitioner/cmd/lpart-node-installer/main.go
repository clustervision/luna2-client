// Command lpart-node-installer runs one Luna install phase (pre|part|post)
// in-process, driving the lpart-* binaries via the engine, and renders a
// single-screen Bubble Tea dashboard (or plain scrolling output under
// `lpart.notui`). The Luna pre/part/post hook scripts are one-line execs of
// this command with the matching --phase.
//
// Usage:
//
//	lpart-node-installer --phase part [--bin-dir /usr/bin] [--no-tui]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installerui"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/pipeline"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

// exitFatalConfig is the exit code for a non-retryable configuration error: one
// that will fail identically on every retry, so the install wrapper
// (luna2-start.sh) must stop the retry loop rather than spin forever. Distinct
// from the generic exit 1 (a transient/operational failure that may be worth a
// retry).
const exitFatalConfig = 3

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lpart-node-installer:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		phase        = flag.String("phase", "", "install phase to run in-process: pre|part|post")
		runtimeDir   = flag.String("runtime", "/tmp/install_files/runtime", "runtime dir for tx_* JSON + results")
		logDir       = flag.String("log-dir", "/tmp/install_files/logs", "per-binary log dir passed to the lpart-* tools")
		binDir       = flag.String("bin-dir", "/usr/bin", "directory holding the lpart-* binaries")
		noAlt        = flag.Bool("no-alt-screen", false, "disable the Bubble Tea alt-screen (useful for piping)")
		logFile      = flag.String("log-file", "", "append captured stdout/stderr to this file (raw, no ANSI)")
		quietConsole = flag.Bool("quiet-console", true, "lower /proc/sys/kernel/printk while the TUI runs so kernel printk stops scrolling the dashboard; kernel messages are still captured from /dev/kmsg and forwarded to rsyslog regardless (Linux only)")
		noTUI        = flag.Bool("no-tui", false, "stream plain scrolling boot output instead of the TUI (also kernel cmdline lpart.notui / $LPART_NOTUI)")
		loghost      = flag.String("loghost", "", "controller rsyslog target host[:port] for live forwarding (default: luna.loghost from cmdline; :514 if no port)")
		nodeName     = flag.String("node-name", "", "node FQDN stamped as the syslog hostname (default: luna.hostname or luna.node from cmdline)")
		allowNonInst = flag.Bool("allow-non-installer", false, "run outside the Luna installer environment (escape hatch; the install phases drive destructive lpart-* tools)")
		cmdlinePath  = flag.String("cmdline-path", "", "(test) override /proc/cmdline path for the installer-env guard")
	)
	flag.Parse()

	if *phase == "" {
		return fmt.Errorf("--phase pre|part|post is required")
	}
	ph := pipeline.Phase(*phase)
	switch ph {
	case pipeline.PhasePre, pipeline.PhasePart, pipeline.PhasePost:
	default:
		return fmt.Errorf("invalid --phase %q (want pre|part|post)", *phase)
	}

	// Refuse to run outside the Luna installer environment. lpart-node-installer
	// orchestrates the destructive phases (partition, image extract, bootloader);
	// the sub-tools guard too, but the orchestrator refuses up-front for clarity.
	if err := sysenv.RequireInstallerEnv(sysenv.Options{
		CmdlinePath:       *cmdlinePath,
		AllowNonInstaller: *allowNonInst,
	}); err != nil {
		return err
	}

	// Legacy is daemon-only: the node runs its old bash pre/part/post and must
	// not invoke the lpart client at all -- and emphatically without the TUI
	// ("legacy means legacy: no TUI"). If we are nonetheless started under
	// install_mode=legacy, that is a daemon routing misconfiguration (the node's
	// part/post are lpart shims instead of the bash legacy template). Refuse here,
	// up-front and plainly -- before the pipeline or TUI -- so the failure is a
	// clear console message, not an error buried in a dashboard, and exit a
	// non-retryable code so the install wrapper does not loop. The mode is a cheap
	// read of the daemon-written install-context.json.
	if msg := legacyRefusal(*runtimeDir); msg != "" {
		fmt.Fprintln(os.Stderr, "lpart-node-installer: "+msg)
		os.Exit(exitFatalConfig)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	state := engine.NewState()
	initial := state.Snapshot()

	// In-process pipeline: the lpart-* orchestration runs here and the Luna
	// pre/part/post scripts are reduced to a one-line exec.
	pl := pipeline.New(ctx, pipeline.Options{
		Phase:      ph,
		BinDir:     *binDir,
		RuntimeDir: *runtimeDir,
		LogDir:     *logDir,
	})
	initial.Stages = pl.Stages()
	if err := pl.Start(); err != nil {
		return fmt.Errorf("start pipeline: %w", err)
	}

	// The pipeline's events are the source; installerui handles the rest
	// (kmsg fan-in, file tee, rsyslog forward, TUI or plain render) — shared
	// verbatim with lpart-tui. StrictExit: this binary's exit code gates the
	// provisioning wrapper, so an interrupted stream must render as failure.
	renderExit, err := installerui.Render(ctx, cancel, pl.Events(), initial, installerui.Reboot, installerui.Config{
		LogFile:      *logFile,
		Loghost:      *loghost,
		NodeName:     *nodeName,
		Phase:        *phase,
		Plain:        installerui.PlainMode(*noTUI),
		NoAlt:        *noAlt,
		QuietConsole: *quietConsole,
		StrictExit:   true,
	})
	if err != nil {
		return err
	}

	// The AUTHORITATIVE outcome is pipeline.Result(), recorded out-of-band
	// of the lossy display bus (FTA Cluster B): a dropped terminal event or
	// an operator quitting the TUI mid-install can never turn a failed or
	// unfinished install into exit 0, and a fatal/safety-refusal class maps
	// to exit 3 so luna2-start.sh parks instead of retrying forever.
	res := pl.Result()
	switch {
	case res.Done && !res.Failed:
		// Success per the pipeline; keep any non-zero renderer verdict
		// (e.g. UI crash) visible rather than masking it.
		if renderExit != 0 {
			os.Exit(renderExit)
		}
	case res.Done && res.Class.Fatal():
		os.Exit(exitFatalConfig)
	case res.Done:
		os.Exit(1)
	default:
		// No terminal state: the pipeline was interrupted (quit/SIGINT/
		// render exit while steps were still running). Never success.
		fmt.Fprintln(os.Stderr, "lpart-node-installer: install interrupted before completion")
		os.Exit(1)
	}
	return nil
}

// legacyRefusal returns a fatal message if install_mode=legacy reached the
// client (a daemon-only mode misrouted to the lpart pipeline), else "". The
// mode is read from the daemon-written install-context.json under runtimeDir; a
// missing/unreadable file or any non-legacy mode yields "" (proceed normally).
// lpart-storage-prepare guards the same condition deeper in, but refusing here
// keeps legacy from ever starting the TUI.
func legacyRefusal(runtimeDir string) string {
	ic, err := installcontext.LoadFile(filepath.Join(runtimeDir, "install-context.json"))
	if err != nil || !ic.InstallMode.IsLegacy() {
		return ""
	}
	return "install_mode=legacy is daemon-only; the lpart client must not run (no TUI). " +
		"This is a daemon routing misconfiguration: legacy must render the bash install " +
		"template and run the node's legacy pre/part/post scripts, not the lpart shims. Refusing to start."
}
