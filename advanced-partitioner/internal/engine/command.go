package engine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync/atomic"
	"time"
)

// Command is a single externally-run command Go drives directly (as
// opposed to the child installer's own subprocesses, which we only see
// via raw stdout/stderr).
//
// The TUI renders the "$ argv..." line from EvCommandStarted, then
// classifies each Stdout/Stderr line via the normal output pipeline,
// then renders a single done/failed status from EvCommandDone/Failed.
type Command struct {
	ID          string
	Stage       string
	Argv        []string
	Cwd         string
	Destructive bool
	Sensitive   bool
}

var commandSeq uint64

func nextCommandID() string {
	n := atomic.AddUint64(&commandSeq, 1)
	return formatID(n)
}

func formatID(n uint64) string {
	// short, monotonic, no time leak; enough for one install run.
	const hex = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = hex[n&0xf]
		n >>= 4
	}
	return string(buf[i:])
}

// RunCommand executes c, emits command.{started,stdout,stderr,done,failed},
// and returns the exit code. It is intended for Go-driven commands
// (lsblk probes, mounts during a Go-native stage, etc.), not for the
// top-level child installer (which goes through Runner).
func RunCommand(ctx context.Context, c Command, out chan<- InstallerEvent) (int, error) {
	if len(c.Argv) == 0 {
		return -1, errors.New("command: empty argv")
	}
	if c.ID == "" {
		c.ID = nextCommandID()
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...) //nolint:gosec // operator-driven
	if c.Cwd != "" {
		cmd.Dir = c.Cwd
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}

	started := time.Now()
	out <- InstallerEvent{
		Time:    started,
		Type:    EvCommandStarted,
		Stage:   c.Stage,
		Stream:  StreamCommand,
		Message: shellPreview(c.Argv),
		Data: CommandEvent{
			ID:          c.ID,
			Stage:       c.Stage,
			Argv:        c.Argv,
			Cwd:         c.Cwd,
			StartedAt:   started,
			Destructive: c.Destructive,
			Sensitive:   c.Sensitive,
		},
	}

	if err := cmd.Start(); err != nil {
		out <- InstallerEvent{
			Time:    time.Now(),
			Type:    EvCommandFailed,
			Stage:   c.Stage,
			Stream:  StreamCommand,
			Message: err.Error(),
		}
		return -1, err
	}

	done := make(chan struct{}, 2)
	go pumpCmd(stdout, EvCommandStdout, StreamStdout, c, out, done)
	go pumpCmd(stderr, EvCommandStderr, StreamStderr, c, out, done)
	<-done
	<-done

	waitErr := cmd.Wait()
	exit := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}

	evType := EvCommandDone
	if exit != 0 {
		evType = EvCommandFailed
	}
	out <- InstallerEvent{
		Time:    time.Now(),
		Type:    evType,
		Stage:   c.Stage,
		Stream:  StreamCommand,
		Message: c.ID,
		Data: CommandEvent{
			ID:          c.ID,
			Stage:       c.Stage,
			Argv:        c.Argv,
			Cwd:         c.Cwd,
			StartedAt:   started,
			FinishedAt:  time.Now(),
			ExitCode:    exit,
			Destructive: c.Destructive,
			Sensitive:   c.Sensitive,
		},
	}
	return exit, waitErr
}

func pumpCmd(rdr io.Reader, ev EventType, stream Stream, c Command, out chan<- InstallerEvent, done chan<- struct{}) {
	sc := bufio.NewScanner(rdr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		out <- InstallerEvent{
			Time:    time.Now(),
			Type:    ev,
			Stage:   c.Stage,
			Stream:  stream,
			Message: sc.Text(),
		}
	}
	done <- struct{}{}
}

// shellPreview renders argv as a "$ a b c" banner line for EvCommandStarted.
func shellPreview(argv []string) string {
	out := "$"
	for _, a := range argv {
		out += " " + a
	}
	return out
}
