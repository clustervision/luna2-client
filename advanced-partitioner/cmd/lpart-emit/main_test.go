package main

import (
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
)

type captureEmitter struct{ events []engine.InstallerEvent }

func (c *captureEmitter) Emit(ev engine.InstallerEvent) error {
	c.events = append(c.events, ev)
	return nil
}

func TestRunPipeTeesAndEmits(t *testing.T) {
	in := strings.NewReader("found 3 disks\nbuilding raid1\n")
	var out strings.Builder
	cap := &captureEmitter{}

	runPipe(cap, in, &out, "storage-prepare", engine.EvRawStdout)

	// Tee: stdout gets the lines verbatim (so install.sh's log still sees them).
	if out.String() != "found 3 disks\nbuilding raid1\n" {
		t.Fatalf("tee output = %q", out.String())
	}
	// Emit: one event per line, tagged with the stage, newline trimmed.
	if len(cap.events) != 2 {
		t.Fatalf("emitted %d events, want 2", len(cap.events))
	}
	if cap.events[0].Stage != "storage-prepare" || cap.events[0].Message != "found 3 disks" || cap.events[0].Type != engine.EvRawStdout {
		t.Fatalf("event[0] wrong: %+v", cap.events[0])
	}
	if cap.events[1].Message != "building raid1" {
		t.Fatalf("event[1] message = %q", cap.events[1].Message)
	}
}

func TestRunPipeHandlesNoTrailingNewline(t *testing.T) {
	in := strings.NewReader("last line no newline")
	var out strings.Builder
	cap := &captureEmitter{}
	runPipe(cap, in, &out, "osimage", engine.EvRawStdout)
	if out.String() != "last line no newline" {
		t.Fatalf("tee = %q", out.String())
	}
	if len(cap.events) != 1 || cap.events[0].Message != "last line no newline" {
		t.Fatalf("events = %+v", cap.events)
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantType engine.EventType
		wantStg  string
		wantMsg  string
		wantErr  bool
	}{
		{"start with message", []string{"start", "bmcsetup", "configuring", "BMC"}, engine.EvStageStarted, "bmcsetup", "configuring BMC", false},
		{"done no message", []string{"done", "prescript"}, engine.EvStageDone, "prescript", "", false},
		{"fail", []string{"fail", "partscript", "bad layout"}, engine.EvStageFailed, "partscript", "bad layout", false},
		{"log stage+message", []string{"log", "osimage", "extracting tarball"}, engine.EvRawStdout, "osimage", "extracting tarball", false},
		{"log message only", []string{"log", "hello"}, engine.EvRawStdout, "", "hello", false},
		{"full event type", []string{"command.stdout", "osimage", "line of output"}, engine.EvCommandStdout, "osimage", "line of output", false},
		{"no args", []string{}, "", "", "", true},
		{"unknown verb", []string{"frobnicate", "x"}, "", "", "", true},
		{"verb without stage", []string{"start"}, "", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := parseArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got event %+v", ev)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ev.Type != c.wantType {
				t.Errorf("type = %q, want %q", ev.Type, c.wantType)
			}
			if ev.Stage != c.wantStg {
				t.Errorf("stage = %q, want %q", ev.Stage, c.wantStg)
			}
			if ev.Message != c.wantMsg {
				t.Errorf("message = %q, want %q", ev.Message, c.wantMsg)
			}
			if ev.Time.IsZero() {
				t.Error("Time should be stamped")
			}
		})
	}
}
