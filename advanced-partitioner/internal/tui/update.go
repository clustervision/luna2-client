package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/uiutil"
)

// Update is the Bubble Tea reducer. It must return quickly; any
// long-running work belongs in a tea.Cmd executed off the main loop.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()

	case shellResumeMsg:
		// After F2 returns from /bin/bash, force a redraw.
		return m, tea.ClearScreen

	case eventMsg:
		m.applyEvent(engine.InstallerEvent(msg))
		var cmds []tea.Cmd
		cmds = append(cmds, m.pollEvent())
		if m.State.Done && !m.finalised {
			m.finalised = true
			m.finalCode = m.State.ExitCode
			cmds = append(cmds, tea.Quit)
		}
		return m, tea.Batch(cmds...)

	case eventClosedMsg:
		// Channel closed without an installer.done/failed event. Under
		// strict exit semantics (node-installer) that is a FAILURE: the
		// pipeline was interrupted before reaching a terminal state, and
		// reporting success would reboot a half-provisioned node. The
		// standalone viewer (lpart-tui) keeps the lenient reading.
		if !m.finalised {
			m.finalised = true
			m.State.Done = true
			if m.strictExit {
				m.finalCode = 1
				if m.State.Err == "" {
					m.State.Err = "event stream ended without a terminal installer event (interrupted?)"
				}
			}
		}
		return m, tea.Quit
	}
	return m, nil
}

// handleKey routes keyboard input. Movement keys mutate Cursor/ScrollTop;
// Up/Dn scroll the Run-output panel; F-keys trigger sub-commands.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any key other than F10 disarms a pending reboot confirmation.
	if m.rebootArmed && !keyMatch(msg, m.keys.Reboot) {
		m.rebootArmed = false
		m.appendSystemLine("reboot cancelled")
	}
	switch {
	case keyMatch(msg, m.keys.Quit):
		m.cancel()
		return m, tea.Quit

	case keyMatch(msg, m.keys.Up):
		m.cursorUp()
		return m, nil

	case keyMatch(msg, m.keys.Down):
		m.cursorDown()
		return m, nil

	case keyMatch(msg, m.keys.PageUp):
		for i := 0; i < 5; i++ {
			m.cursorUp()
		}
		return m, nil

	case keyMatch(msg, m.keys.PageDn):
		for i := 0; i < 5; i++ {
			m.cursorDown()
		}
		return m, nil

	case keyMatch(msg, m.keys.Redraw):
		return m, tea.ClearScreen

	case keyMatch(msg, m.keys.Shell):
		// Hand off to /bin/bash. The install keeps running in the background
		// (the installer is a separate process); the dashboard redraws on
		// return. Type 'exit' to come back.
		m.appendSystemLine("shell: install continues in the background; type 'exit' to return")
		return m, tea.Sequence(
			tea.ExecProcess(execBash(), func(err error) tea.Msg {
				return shellResumeMsg{}
			}),
		)

	case keyMatch(msg, m.keys.Reboot):
		if m.reboot == nil {
			m.appendSystemLine("reboot is not available in this mode")
			return m, nil
		}
		if !m.rebootArmed {
			m.rebootArmed = true
			m.appendSystemLine("press F10 again to REBOOT the node now (any other key cancels)")
			return m, nil
		}
		m.rebootArmed = false
		m.appendSystemLine("rebooting...")
		rb := m.reboot
		return m, func() tea.Msg { rb(); return nil }
	}
	return m, nil
}

// cursorUp/cursorDown scroll the Run-output panel: Up pages back into the
// install log, Down moves toward the live tail (OutputScroll==0 follows). The
// disk table is informational and almost always fits, so the keys drive the
// one panel that actually overflows.
func (m *Model) cursorUp()   { m.scrollOutput(1) }
func (m *Model) cursorDown() { m.scrollOutput(-1) }

func (m *Model) scrollOutput(delta int) {
	m.OutputScroll += delta
	if m.OutputScroll < 0 {
		m.OutputScroll = 0
	}
	if max := len(m.State.Output.Lines) - 1; max < 0 {
		m.OutputScroll = 0
	} else if m.OutputScroll > max {
		m.OutputScroll = max
	}
}

// applyEvent folds an engine event into the model snapshot. Each event
// only touches the fields it owns - the TUI never reaches back into the
// engine state directly.
func (m *Model) applyEvent(ev engine.InstallerEvent) {
	// SyslogOnly events (the boot dmesg backlog) go to rsyslog + the log file
	// but never into the dashboard — the console already showed them.
	if ev.SyslogOnly {
		return
	}
	switch ev.Type {
	case engine.EvRawStdout, engine.EvRawStderr, engine.EvCommandStdout, engine.EvCommandStderr:
		stream := "stdout"
		if ev.Type == engine.EvRawStderr || ev.Type == engine.EvCommandStderr {
			stream = "stderr"
		}
		m.appendOutput(ev.Message, stream, "")

	case engine.EvCommandStarted:
		m.appendOutput(ev.Message, "system", model.ClassCommand)

	case engine.EvCommandDone:
		// silence: the next line typically reports success; no banner.

	case engine.EvCommandFailed:
		m.appendOutput("command failed: "+ev.Message, "system", model.ClassError)

	case engine.EvStageStarted:
		// A bus-driven renderer has no pre-seeded plan: reveal the stage the
		// first time it starts (no-op for the pre-seeded in-process pipeline).
		m.State.EnsureStage(ev.Stage, ev.Stage)
		m.State.MarkStage(ev.Stage, model.StageRunning, ev.Message)
		m.appendOutput("── "+ev.Stage+" ─────────────────────────", "system", model.ClassStage)

	case engine.EvStageDone:
		m.State.MarkStage(ev.Stage, model.StageDone, ev.Message)

	case engine.EvStageFailed:
		m.State.MarkStage(ev.Stage, model.StageFailed, ev.Message)

	case engine.EvLunaUpdated:
		if l, ok := ev.Data.(engine.LunaUpdate); ok {
			m.State.Luna = model.Luna(l)
		}

	case engine.EvNodeUpdated:
		if n, ok := ev.Data.(engine.NodeUpdate); ok {
			m.State.Node = model.Node(n)
		}

	case engine.EvStorageUpdated:
		if s, ok := ev.Data.(engine.StorageUpdate); ok {
			m.State.Storage = model.Storage(s)
		}

	case engine.EvKernel:
		// Kernel messages (ipmi/driver errors) captured from /dev/kmsg, shown
		// in the output panel in warning/error colour instead of corrupting
		// the console. Level <= 3 (err and worse) renders as an error.
		class := model.ClassWarning
		if lvl, ok := ev.Data.(int); ok && lvl <= 3 {
			class = model.ClassError
		}
		m.appendOutput("[kernel] "+ev.Message, "kernel", class)

	case engine.EvInstallerDone:
		m.State.Done = true
		m.State.ExitCode = 0

	case engine.EvInstallerFailed:
		m.State.Done = true
		m.State.ExitCode = FailureExitCode(ev.Data)
		m.State.Err = ev.Message
	}
}

// FailureExitCode maps the failure class carried on EvInstallerFailed.Data
// (any type exposing Fatal() bool — pipeline.FailureClass does) to the
// process exit code: fatal/safety-refusal failures exit 3 — the code
// luna-installer.sh PARKS on — everything else exits 1 (retry). FTA Cluster B
// (B1): the class used to be flattened to a hardcoded 1 here.
func FailureExitCode(data any) int {
	type classed interface{ Fatal() bool }
	if c, ok := data.(classed); ok && c.Fatal() {
		return 3
	}
	return 1
}

func (m *Model) appendOutput(line, stream string, force model.LineClass) {
	class := uiutil.Classify(uiutil.ClassifyInput{Line: line, Stream: stream, Force: force})
	m.State.Output.Lines = append(m.State.Output.Lines, model.OutputLine{
		Time:   time.Now(),
		Stream: stream,
		Raw:    line,
		Class:  class,
	})
	if len(m.State.Output.Lines) > MaxOutputLines {
		drop := len(m.State.Output.Lines) - MaxOutputLines
		m.State.Output.Lines = m.State.Output.Lines[drop:]
	}
}

func (m *Model) appendSystemLine(line string) {
	m.appendOutput(line, "system", model.ClassNormal)
}

func keyMatch(msg tea.KeyMsg, b interface {
	Keys() []string
}) bool {
	s := msg.String()
	for _, k := range b.Keys() {
		if k == s {
			return true
		}
	}
	return false
}
