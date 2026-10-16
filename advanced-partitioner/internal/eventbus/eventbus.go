// Package eventbus is the on-node wire between the install steps (the lpart-*
// workers and the bash steps in install.sh) and the live dashboard renderer
// (lpart-tui). Per ADR 0009, the renderer is a separate process from the work,
// so it can span the whole install — including the admin pre/part/post columns
// — instead of being welded into one --phase process.
//
// The bus is a stream of NDJSON lines (one WireEvent per line) at a well-known
// path (DefaultPath, normally a FIFO). Producers Open the path and Emit; the
// renderer Reads it and feeds the events into the existing TUI model. The wire
// vocabulary is engine.EventType / engine.Stream — the same types the in-process
// pipeline already emits — so a WireEvent maps 1:1 to an engine.InstallerEvent.
//
// Producing is strictly best-effort: the dashboard is cosmetic, so a missing or
// reader-less bus must never block or fail an install. Open with no reader, a
// broken pipe, or a full FIFO all degrade to dropped events, never an error that
// reaches the worker's real work.
package eventbus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
)

// DefaultPath is the on-node bus FIFO. luna-installer.sh creates it and launches
// lpart-tui to read it before running install.sh; every step emits to it.
const DefaultPath = "/run/lpart/events"

// WireEvent is one line on the bus — the serialisable projection of an
// engine.InstallerEvent. Field names are short because command.stdout makes
// this a high-volume stream. Data is the opaque payload (panel updates etc.),
// carried verbatim so the renderer can decode the kinds it knows and ignore the
// rest.
type WireEvent struct {
	T          time.Time        `json:"t"`
	Type       engine.EventType `json:"type"`
	Stage      string           `json:"stage,omitempty"`
	Message    string           `json:"msg,omitempty"`
	Stream     engine.Stream    `json:"stream,omitempty"`
	Data       json.RawMessage  `json:"data,omitempty"`
	SyslogOnly bool             `json:"syslog_only,omitempty"`
}

// FromEvent projects an engine.InstallerEvent onto the wire, marshalling Data if
// present. A Data value that will not marshal is dropped (the event still goes
// out without it) rather than failing the emit.
func FromEvent(ev engine.InstallerEvent) WireEvent {
	w := WireEvent{
		T:          ev.Time,
		Type:       ev.Type,
		Stage:      ev.Stage,
		Message:    ev.Message,
		Stream:     ev.Stream,
		SyslogOnly: ev.SyslogOnly,
	}
	if ev.Time.IsZero() {
		w.T = time.Now()
	}
	if ev.Data != nil {
		if b, err := json.Marshal(ev.Data); err == nil {
			w.Data = b
		}
	}
	return w
}

// ToEvent rebuilds an engine.InstallerEvent. Data stays as the raw JSON payload;
// the renderer decodes the concrete kinds it cares about (see DecodeData).
func (w WireEvent) ToEvent() engine.InstallerEvent {
	return engine.InstallerEvent{
		Time:       w.T,
		Type:       w.Type,
		Stage:      w.Stage,
		Message:    w.Message,
		Stream:     w.Stream,
		Data:       w.Data,
		SyslogOnly: w.SyslogOnly,
	}
}

// Writer serialises events onto the bus as NDJSON. Safe for concurrent Emit.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
	c  io.Closer
}

// NewWriter wraps an arbitrary sink (used in tests and in-process wiring).
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Open opens the bus at path for writing. The path is normally a FIFO created by
// luna-installer.sh; it is opened non-blocking so that a not-yet-attached (or
// absent) reader does not block the producer. A regular file or a FIFO with a
// reader both work. When the path cannot be opened at all, Open returns a Writer
// that silently drops events, plus the error — callers that want best-effort can
// ignore it.
func Open(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NONBLOCK, 0o600) //nolint:gosec // bus path is the fixed DefaultPath / operator flag; runs as root in the installer
	if err != nil {
		return &Writer{w: io.Discard}, err
	}
	return &Writer{w: f, c: f}, nil
}

// EnsureFIFO creates path as a FIFO if it does not already exist. A pre-existing
// FIFO (or any existing node) is left as-is. The renderer calls this before
// OpenReader so the bus exists even if luna-installer.sh has not made it.
func EnsureFIFO(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// OpenReader opens the bus for reading by the renderer. It opens O_RDWR (not
// O_RDONLY) deliberately: on a FIFO that keeps a writer reference alive for the
// fd's whole lifetime, so the stream never hits EOF when the many short-lived
// producer processes (each lpart-emit / worker) open→write→close. Reads block
// until data; the renderer ends the stream by closing this fd (e.g. on ctx
// cancel), which unblocks the Read loop. No O_NONBLOCK — a blocking read is what
// the streaming reader wants.
func OpenReader(path string) (*os.File, error) {
	if err := EnsureFIFO(path); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR, 0o600) //nolint:gosec // bus path is the fixed DefaultPath / operator flag; runs as root in the installer
}

// Emit writes one event as a single NDJSON line. Best-effort: a broken or
// reader-less pipe (EPIPE/ENXIO/EAGAIN) is swallowed so the dashboard never
// affects the install. Other write errors are returned for the caller to log.
func (x *Writer) Emit(ev engine.InstallerEvent) error {
	line, err := json.Marshal(FromEvent(ev))
	if err != nil {
		return err
	}
	line = append(line, '\n')

	x.mu.Lock()
	defer x.mu.Unlock()
	if _, err := x.w.Write(line); err != nil {
		if isDroppable(err) {
			return nil
		}
		return err
	}
	return nil
}

// Tee returns a channel that forwards every event from src unchanged while
// emitting each onto the bus — the chain link that slots beside TeeToFile /
// ForwardSyslog (events -> TeeToFile -> Tee(bus) -> ...). Emit errors are
// dropped: the bus is best-effort.
func (x *Writer) Tee(ctx context.Context, src <-chan engine.InstallerEvent) <-chan engine.InstallerEvent {
	out := make(chan engine.InstallerEvent)
	go func() {
		defer close(out)
		for ev := range src {
			_ = x.Emit(ev)
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Close releases the underlying sink if Open created one.
func (x *Writer) Close() error {
	if x.c != nil {
		return x.c.Close()
	}
	return nil
}

// Read streams events from r (the read end of the bus) until EOF or ctx is
// cancelled. Malformed lines are skipped — a partial write from a crashed
// producer must not kill the renderer. Long command.stdout lines are handled
// (no fixed token cap).
func Read(ctx context.Context, r io.Reader) <-chan engine.InstallerEvent {
	out := make(chan engine.InstallerEvent)
	go func() {
		defer close(out)
		br := bufio.NewReader(r)
		for {
			if ctx.Err() != nil {
				return
			}
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				var w WireEvent
				if json.Unmarshal(trimNL(line), &w) == nil && w.Type != "" {
					select {
					case out <- Rehydrate(w.ToEvent()):
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return // EOF or read error: the writer closed / went away
			}
		}
	}()
	return out
}

// Rehydrate converts a bus event's raw json.RawMessage Data back into the
// concrete payload type the TUI model type-asserts on (engine.LunaUpdate,
// CommandEvent, the kernel level int, …). In-process events already carry the
// concrete type, so Rehydrate only acts on the raw-JSON form that crosses the
// bus. Unknown/empty payloads pass through unchanged. Read applies this so the
// renderer sees the same shapes whether events came in-process or over the bus.
func Rehydrate(ev engine.InstallerEvent) engine.InstallerEvent {
	raw, ok := ev.Data.(json.RawMessage)
	if !ok || len(raw) == 0 {
		return ev
	}
	switch ev.Type {
	case engine.EvLunaUpdated:
		var v engine.LunaUpdate
		if json.Unmarshal(raw, &v) == nil {
			ev.Data = v
		}
	case engine.EvNodeUpdated:
		var v engine.NodeUpdate
		if json.Unmarshal(raw, &v) == nil {
			ev.Data = v
		}
	case engine.EvStorageUpdated:
		var v engine.StorageUpdate
		if json.Unmarshal(raw, &v) == nil {
			ev.Data = v
		}
	case engine.EvCommandStarted, engine.EvCommandDone, engine.EvCommandFailed:
		var v engine.CommandEvent
		if json.Unmarshal(raw, &v) == nil {
			ev.Data = v
		}
	case engine.EvKernel:
		var lvl int
		if json.Unmarshal(raw, &lvl) == nil {
			ev.Data = lvl
		}
	}
	return ev
}

// DecodeData unmarshals a WireEvent's raw Data payload into v (e.g. a
// model.Storage for EvStorageUpdated). Returns false when there is no payload.
func DecodeData(ev engine.InstallerEvent, v any) bool {
	raw, ok := ev.Data.(json.RawMessage)
	if !ok || len(raw) == 0 {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// isDroppable reports whether a write error means "no reader" rather than a real
// failure — the best-effort cases the bus swallows.
func isDroppable(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ENXIO) ||
		errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, os.ErrClosed)
}
