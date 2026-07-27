// Package engine starts the existing installer, captures its output,
// and emits typed events the TUI consumes. The engine never blocks the
// Bubble Tea Update() goroutine: it pushes events into a buffered
// channel and the TUI drains them in its own time.
package engine

import (
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// EventType is the discriminator for InstallerEvent. Values match the
// names in tui-design.plan §"Event types".
type EventType string

const (
	EvStageStarted     EventType = "stage.started"
	EvStageDone        EventType = "stage.done"
	EvStageFailed      EventType = "stage.failed"
	EvCommandStarted   EventType = "command.started"
	EvCommandStdout    EventType = "command.stdout"
	EvCommandStderr    EventType = "command.stderr"
	EvCommandDone      EventType = "command.done"
	EvCommandFailed    EventType = "command.failed"
	EvRawStdout        EventType = "raw.stdout"
	EvRawStderr        EventType = "raw.stderr"
	EvDiscoveryUpdated EventType = "discovery.updated"
	EvStorageUpdated   EventType = "storage.updated"
	EvTargetUpdated    EventType = "target.updated"
	EvNodeUpdated      EventType = "node.updated"
	EvLunaUpdated      EventType = "luna.updated"
	EvInstallerDone    EventType = "installer.done"
	EvInstallerFailed  EventType = "installer.failed"
	EvKernel           EventType = "kernel"
)

// Stream tags where a line came from. "system" is for events the engine
// itself synthesised (e.g. a stage banner).
type Stream string

const (
	StreamStdout    Stream = "stdout"
	StreamStderr    Stream = "stderr"
	StreamSystem    Stream = "system"
	StreamDiscovery Stream = "discovery"
	StreamCommand   Stream = "command"
	StreamKernel    Stream = "kernel"
)

// InstallerEvent is the bus payload. Data is optional and depends on
// Type — see stages.go / runner.go for concrete shapes.
type InstallerEvent struct {
	Time    time.Time
	Type    EventType
	Stage   string
	Message string
	Stream  Stream
	Data    any
	// SyslogOnly marks an event that should reach the rsyslog forward (and the
	// log file) but NOT the live TUI/plain console. Used for the boot dmesg
	// backlog, which the console already printed before the installer's TUI
	// took over, so re-printing it in the TUI is just noise.
	SyslogOnly bool
}

// CommandEvent describes one external command the Go side ran. It is
// embedded in InstallerEvent.Data for EvCommand* events.
type CommandEvent struct {
	ID          string
	Stage       string
	Argv        []string
	Cwd         string
	StartedAt   time.Time
	FinishedAt  time.Time
	ExitCode    int
	Destructive bool
	Sensitive   bool
}

// LunaUpdate / NodeUpdate / StorageUpdate are payload types for the
// matching event kinds. Carrying a value type lets the TUI replace its
// state wholesale without having to merge fields.
type LunaUpdate model.Luna
type NodeUpdate model.Node
type StorageUpdate model.Storage
