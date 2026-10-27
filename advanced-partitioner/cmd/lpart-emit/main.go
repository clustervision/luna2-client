// Command lpart-emit is the bash-facing producer for the install event bus
// (ADR 0009). The non-Go steps in install.sh — bmcsetup, the pre/part/post admin
// columns, the legacy tail — call it to report coarse progress to the same bus
// the lpart-* workers and lpart-tui use, so the dashboard spans the whole
// install, not just the Go pipeline.
//
// Usage:
//
//	lpart-emit start <stage> [message...]   # a stage began
//	lpart-emit done  <stage> [message...]   # a stage finished ok
//	lpart-emit fail  <stage> [message...]   # a stage failed
//	lpart-emit log   [stage] <message...>   # a plain log line into the pane
//	lpart-emit <engine.EventType> <stage> [message...]   # full type, e.g. command.stdout
//
//	worker | lpart-emit --pipe --stage <stage>   # stream a worker's output:
//	                                              # each stdin line → a bus event,
//	                                              # teed onward to stdout unchanged
//
//	--bus PATH   bus to write to (default /run/lpart/events)
//
// It is strictly best-effort: a missing or reader-less bus is not an error
// (exit 0), because a progress marker must never abort an install. Only a usage
// mistake exits non-zero.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/eventbus"
)

func main() {
	defer cli.ExitOnPanic(os.Stderr, "lpart-emit")
	bus := flag.String("bus", eventbus.DefaultPath, "event bus path to write to")
	pipe := flag.Bool("pipe", false, "read stdin lines and emit each as a bus event (teed to stdout)")
	stage := flag.String("stage", "", "stage tag for --pipe events")
	typ := flag.String("type", string(engine.EvRawStdout), "event type for --pipe lines")
	flag.Parse()

	// Best-effort: open once, but never fail the caller on a missing reader.
	w, _ := eventbus.Open(*bus)
	defer func() { _ = w.Close() }()

	// Pipe mode bridges a worker's output onto the bus while teeing it onward to
	// install.sh's normal stdout (so the line still reaches the log file/console).
	if *pipe {
		runPipe(w, os.Stdin, os.Stdout, *stage, engine.EventType(*typ))
		return
	}

	ev, err := parseArgs(flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "lpart-emit:", err)
		fmt.Fprintln(os.Stderr, "usage: lpart-emit start|done|fail|log <stage> [message...]")
		fmt.Fprintln(os.Stderr, "       worker | lpart-emit --pipe --stage <stage>")
		os.Exit(2)
	}
	_ = w.Emit(ev)
}

// emitter is the slice of eventbus.Writer runPipe needs (for testing).
type emitter interface {
	Emit(engine.InstallerEvent) error
}

// runPipe reads in line by line, emits each as a bus event tagged with stage,
// and tees the raw line onward to out so the worker's output still flows into
// install.sh's stdout. Best-effort: emit errors are dropped; the tee is what
// must not be lost.
func runPipe(w emitter, in io.Reader, out io.Writer, stage string, typ engine.EventType) {
	br := bufio.NewReader(in)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			_, _ = io.WriteString(out, line) // tee through unchanged first
			_ = w.Emit(engine.InstallerEvent{
				Time:    time.Now(),
				Type:    typ,
				Stage:   stage,
				Message: strings.TrimRight(line, "\r\n"),
				Stream:  engine.StreamStdout,
			})
		}
		if err != nil {
			return
		}
	}
}

// verbAlias maps the short verbs a bash step uses to the engine event types.
var verbAlias = map[string]engine.EventType{
	"start": engine.EvStageStarted,
	"done":  engine.EvStageDone,
	"fail":  engine.EvStageFailed,
	"log":   engine.EvRawStdout,
}

// parseArgs builds the event from the command-line words. It is pure so the
// mapping is unit-tested without touching the bus.
func parseArgs(args []string) (engine.InstallerEvent, error) {
	if len(args) == 0 {
		return engine.InstallerEvent{}, fmt.Errorf("need a verb (start|done|fail|log) or an event type")
	}
	verb := args[0]
	rest := args[1:]

	typ, ok := verbAlias[verb]
	if !ok {
		// Allow a full engine event type ("command.stdout", "stage.done", …).
		if !strings.Contains(verb, ".") {
			return engine.InstallerEvent{}, fmt.Errorf("unknown verb %q", verb)
		}
		typ = engine.EventType(verb)
	}

	ev := engine.InstallerEvent{Time: time.Now(), Type: typ, Stream: engine.StreamSystem}

	// `log` is the one verb whose stage is optional: `log <message...>` or
	// `log <stage> <message...>`. Everything else needs a stage first.
	if typ == engine.EvRawStdout {
		ev.Stream = engine.StreamStdout
		if len(rest) >= 2 {
			ev.Stage, ev.Message = rest[0], strings.Join(rest[1:], " ")
		} else {
			ev.Message = strings.Join(rest, " ")
		}
		return ev, nil
	}

	if len(rest) == 0 {
		return engine.InstallerEvent{}, fmt.Errorf("%q needs a stage", verb)
	}
	ev.Stage = rest[0]
	ev.Message = strings.Join(rest[1:], " ")
	return ev, nil
}
