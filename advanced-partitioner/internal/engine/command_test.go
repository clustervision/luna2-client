package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// drain consumes RunCommand's events so it never blocks on the channel.
func drain(t *testing.T) (chan InstallerEvent, func() []InstallerEvent) {
	t.Helper()
	ch := make(chan InstallerEvent, 64)
	got := make(chan []InstallerEvent, 1)
	go func() {
		var evs []InstallerEvent
		for ev := range ch {
			evs = append(evs, ev)
		}
		got <- evs
	}()
	return ch, func() []InstallerEvent { close(ch); return <-got }
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

// alive reports whether pid is a live (non-zombie) process.
func alive(pid int) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	_, state, _, ok := parseProcStat(string(b))
	return ok && state != "Z" && state != "X"
}

// A timed-out step must take its whole tree with it: a grandchild (here a
// sleep standing in for blkdiscard -z) used to survive the kill of the direct
// child and keep running into the next install attempt.
func TestRunCommandTimeoutKillsTheProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, collect := drain(t)
	start := time.Now()
	code, err := RunCommand(ctx, Command{Argv: []string{"/bin/sh", "-c",
		"sleep 60 & echo $! > " + pidFile + "; wait"}}, out)
	collect()
	if err == nil || code == 0 {
		t.Fatalf("a killed step must fail; code=%d err=%v", code, err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("RunCommand took %s; the held pipe must not keep it waiting", d)
	}
	pid := readPid(t, pidFile)
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("grandchild %d survived the step timeout", pid)
	}
}

// A command that exits 0 while a descendant keeps its stdout open must not
// hang the step for ever: the exit status decides, after ioWaitDelay.
func TestRunCommandDescendantHoldingThePipe(t *testing.T) {
	old := ioWaitDelay
	ioWaitDelay = 300 * time.Millisecond
	defer func() { ioWaitDelay = old }()
	pidFile := filepath.Join(t.TempDir(), "pid")
	out, collect := drain(t)
	done := make(chan struct{})
	var (
		code int
		err  error
	)
	go func() {
		code, err = RunCommand(context.Background(), Command{Argv: []string{"/bin/sh", "-c",
			"sleep 60 & echo $! > " + pidFile + "; echo done"}}, out)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("RunCommand hung on a pipe held by a descendant")
	}
	collect()
	_ = syscall.Kill(readPid(t, pidFile), syscall.SIGKILL)
	if code != 0 || err != nil {
		t.Fatalf("exit 0 must stay success; code=%d err=%v", code, err)
	}
}

func TestParseProcStat(t *testing.T) {
	comm, state, pgrp, ok := parseProcStat("4242 (odd) name) S 1 4240 4240 0 -1")
	if !ok || comm != "odd) name" || state != "S" || pgrp != 4240 {
		t.Fatalf("got comm=%q state=%q pgrp=%d ok=%v", comm, state, pgrp, ok)
	}
	if _, _, _, ok := parseProcStat("garbage"); ok {
		t.Fatal("garbage must not parse")
	}
}
