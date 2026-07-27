package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// MaxOutputLines is how many recent installer lines the output panel
// keeps. Higher = more scrollback, more redraw cost.
const MaxOutputLines = 1024

// Model is the Bubble Tea model. It owns:
//
//   - a snapshot of installer state (model.Installer)
//   - the Run-output scroll position (OutputScroll)
//   - terminal dimensions
//   - the latest event channel from engine
//   - styles + keymap
//
// Update() never blocks; long-running work happens in engine goroutines
// and arrives via eventMsg.
type Model struct {
	State model.Installer
	// OutputScroll is how many lines the Run-output panel is scrolled back
	// from the live tail (0 = following the latest output). Up/Dn drive it —
	// the install log is the panel worth scrolling; the disk table almost
	// always fits.
	OutputScroll int

	Width  int
	Height int

	now time.Time

	keys   KeyMap
	styles Styles

	ctx         context.Context
	cancel      context.CancelFunc
	events      <-chan engine.InstallerEvent
	reboot      func() // reboots the node; nil when unsupported
	rebootArmed bool   // F10 pressed once; a second press reboots
	finalCode   int
	finalised   bool
	// strictExit makes an event stream that ends WITHOUT a terminal
	// installer.done/failed event — or an operator quit mid-install —
	// finalise as FAILURE (exit 1) instead of success. Set for the
	// node-installer (whose exit code gates provisioning); the standalone
	// lpart-tui viewer keeps the lenient default. FTA Cluster B (B2/B3):
	// "stream ended" must never be read as "install succeeded".
	strictExit bool
}

// WithStrictExit returns a copy of the model with strict exit semantics on.
func (m Model) WithStrictExit() Model {
	m.strictExit = true
	return m
}

// NewModel builds a Model bound to an engine event source. reboot reboots the
// node; pass nil when the running mode can't do it (e.g. unit tests).
func NewModel(ctx context.Context, cancel context.CancelFunc, events <-chan engine.InstallerEvent, initial model.Installer, reboot func()) Model {
	return Model{
		State:  initial,
		now:    time.Now(),
		keys:   DefaultKeys(),
		styles: NewStyles(),
		ctx:    ctx,
		cancel: cancel,
		events: events,
		reboot: reboot,
	}
}

// FinalExitCode returns the installer's final exit code (0 on success,
// non-zero on failure, -1 if it never ran).
func (m Model) FinalExitCode() int { return m.finalCode }

// Init wires up the initial Bubble Tea commands: clock tick + first
// event pull.
func (m Model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), m.pollEvent())
}

// eventMsg / tickMsg / shellResumeMsg / installerDoneMsg are the
// message types Update() reacts to.
type eventMsg engine.InstallerEvent
type eventClosedMsg struct{}
type tickMsg time.Time
type shellResumeMsg struct{}

// tickCmd is the 1Hz clock used by the header.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// pollEvent pulls one event from the engine channel and turns it into a
// tea.Msg. Returns eventClosedMsg when the channel is closed.
func (m Model) pollEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.events
		if !ok {
			return eventClosedMsg{}
		}
		return eventMsg(ev)
	}
}
