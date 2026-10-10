package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/pipeline"
)

// TestRefusesOutsideInstallerEnv locks in the installer-env guard on the
// orchestrator: with a non-Luna cmdline and no --allow-non-installer, run()
// must refuse before doing any phase work (it drives the destructive lpart-*
// tools). run() uses the global flag set, so reset it around the call.
func TestRefusesOutsideInstallerEnv(t *testing.T) {
	tmp := t.TempDir()
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=/dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
	flag.CommandLine = flag.NewFlagSet("lpart-node-installer", flag.ContinueOnError)
	os.Args = []string{"lpart-node-installer", "--phase", "pre", "--cmdline-path", cmdline}

	err := run()
	if err == nil {
		t.Fatal("expected refusal outside the installer environment")
	}
	if !strings.Contains(err.Error(), "root=luna") {
		t.Errorf("error should mention the root=luna marker, got: %v", err)
	}
}

// TestLegacyRefusal locks in "legacy means legacy: no TUI": when the
// daemon-written install-context.json says install_mode=legacy, the orchestrator
// must refuse up-front (non-empty message -> exit exitFatalConfig, no TUI). Any
// other mode, or a missing file, proceeds (empty message).
func TestLegacyRefusal(t *testing.T) {
	tests := []struct {
		name       string
		mode       string // "" => write no file at all
		wantRefuse bool
	}{
		{"legacy refuses", "legacy", true},
		{"auto proceeds", "auto", false},
		{"full proceeds", "full", false},
		{"missing file proceeds", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.mode != "" {
				p := filepath.Join(dir, "install-context.json")
				body := []byte(`{"install_mode":"` + tc.mode + `"}`)
				if err := os.WriteFile(p, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			msg := legacyRefusal(dir)
			if (msg != "") != tc.wantRefuse {
				t.Fatalf("legacyRefusal(mode=%q) = %q; wantRefuse=%v", tc.mode, msg, tc.wantRefuse)
			}
			if tc.wantRefuse && !strings.Contains(msg, "no TUI") {
				t.Errorf("refusal message should mention 'no TUI', got: %q", msg)
			}
		})
	}
}

// fakePhase stands in for a pipeline whose step only ends when cancelled.
type fakePhase struct {
	ctx    context.Context
	events chan engine.InstallerEvent
	done   chan struct{}
}

func (f *fakePhase) Result() pipeline.Result              { return pipeline.Result{} }
func (f *fakePhase) Events() <-chan engine.InstallerEvent { return f.events }
func (f *fakePhase) Wait()                                { <-f.done }

// The process must not exit while a phase step is still running: the
// renderer returning (quit, render error) cancels the phase and waits for it.
func TestStopPipelineCancelsAndWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakePhase{ctx: ctx, events: make(chan engine.InstallerEvent), done: make(chan struct{})}
	stepEnded := false
	go func() {
		// A step blocked on the (undrained) event channel until cancelled.
		f.events <- engine.InstallerEvent{Message: "step output"}
		<-ctx.Done()
		stepEnded = true
		close(f.events)
		close(f.done)
	}()
	var errBuf bytes.Buffer
	finished := make(chan struct{})
	go func() { stopPipeline(cancel, f, &errBuf); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("stopPipeline did not cancel the phase")
	}
	if !stepEnded {
		t.Fatal("stopPipeline returned before the phase had finished")
	}
	if !strings.Contains(errBuf.String(), "stopping the unfinished phase") {
		t.Fatalf("the operator must be told: %q", errBuf.String())
	}
}
