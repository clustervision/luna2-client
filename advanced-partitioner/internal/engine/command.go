package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
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

// ioWaitDelay bounds how long RunCommand waits for a command's output after
// the command itself has exited (or was killed) while some descendant still
// holds its stdout/stderr open. Without the bound a daemon a step left behind
// hung the step, and with it the phase, for ever.
var ioWaitDelay = 10 * time.Second

// groupDrainTimeout bounds how long RunCommand waits, after an interrupted
// command's process group was killed, for every member of the group to be
// gone. A process in uninterruptible I/O (a long BLKZEROOUT) dies only when
// the kernel returns to it; that is reported, not waited on for hours.
var groupDrainTimeout = 30 * time.Second

// RunCommand executes c, emits command.{started,stdout,stderr,done,failed},
// and returns the exit code. It is intended for Go-driven commands
// (lsblk probes, mounts during a Go-native stage, etc.), not for the
// top-level child installer (which goes through Runner).
//
// The command runs in its own process group. When ctx is cancelled or times
// out the WHOLE group is killed -- the command and every child it started
// (mkfs, sgdisk, tar, blkdiscard) -- and RunCommand returns only once the
// group is gone (bounded by groupDrainTimeout). Killing only the direct
// child, exec's default, left those grandchildren running into the next
// install attempt.
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = ioWaitDelay
	// Writers rather than StdoutPipe: exec then owns the copy, and WaitDelay
	// can cut it off when a descendant keeps the pipe open after the command
	// exited. The pumps read the other end until the writers are closed.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

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
		_ = stdoutW.Close()
		_ = stderrW.Close()
		out <- InstallerEvent{
			Time:    time.Now(),
			Type:    EvCommandFailed,
			Stage:   c.Stage,
			Stream:  StreamCommand,
			Message: err.Error(),
		}
		return -1, err
	}
	pgid := cmd.Process.Pid

	done := make(chan struct{}, 2)
	go pumpCmd(stdoutR, EvCommandStdout, StreamStdout, c, out, done)
	go pumpCmd(stderrR, EvCommandStderr, StreamStderr, c, out, done)

	waitErr := cmd.Wait()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	<-done
	<-done

	if ctx.Err() != nil {
		// Interrupted (timeout or cancel): exec has killed the group via
		// cmd.Cancel; make sure no member outlives this call.
		if left := drainGroup(pgid, groupDrainTimeout); len(left) > 0 {
			out <- InstallerEvent{
				Time:   time.Now(),
				Type:   EvCommandStderr,
				Stage:  c.Stage,
				Stream: StreamStderr,
				Message: fmt.Sprintf("%s: killed, but %s still running after %s (uninterruptible I/O?)",
					filepath.Base(c.Argv[0]), strings.Join(left, ", "), groupDrainTimeout),
			}
		}
	}

	exit := 0
	if waitErr != nil {
		var ee *exec.ExitError
		switch {
		case errors.As(waitErr, &ee):
			exit = ee.ExitCode()
		case errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success():
			// The command exited 0; only a descendant it left behind kept
			// the output pipe open. The exit status decides the step.
			waitErr = nil
		default:
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

// killGroup sends SIGKILL to the process group led by pid. An empty group is
// reported as os.ErrProcessDone, which exec treats as "already exited".
func killGroup(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// drainGroup kills process group pgid until no live member is left, or the
// timeout passes. It returns the members still running then ("comm[pid]").
func drainGroup(pgid int, timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	for {
		left := groupMembers(pgid)
		if len(left) == 0 {
			return nil
		}
		_ = killGroup(pgid)
		if time.Now().After(deadline) {
			return left
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// groupMembers lists the live (non-zombie) processes in group pgid from
// /proc. A zombie is already dead: it only waits for its parent to reap it.
func groupMembers(pgid int) []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// No /proc: fall back to the kernel's answer for the whole group.
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return nil
		}
		return []string{"process group " + strconv.Itoa(pgid)}
	}
	var out []string
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		comm, state, pgrp, ok := parseProcStat(string(b))
		if !ok || pgrp != pgid || state == "Z" || state == "X" {
			continue
		}
		out = append(out, fmt.Sprintf("%s[%d]", comm, pid))
	}
	return out
}

// parseProcStat extracts comm, state and pgrp from a /proc/<pid>/stat line:
// "pid (comm) state ppid pgrp ...". comm may itself hold spaces and parens,
// so the fields are taken after its LAST closing paren.
func parseProcStat(line string) (comm, state string, pgrp int, ok bool) {
	open := strings.IndexByte(line, '(')
	end := strings.LastIndexByte(line, ')')
	if open < 0 || end < open {
		return "", "", 0, false
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) < 3 {
		return "", "", 0, false
	}
	pg, err := strconv.Atoi(fields[2])
	if err != nil {
		return "", "", 0, false
	}
	return line[open+1 : end], fields[0], pg, true
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
	// A scan error (a line over the 1 MiB cap) stops the scanner, not the
	// writer: keep draining so the command never blocks on a full pipe.
	_, _ = io.Copy(io.Discard, rdr)
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
