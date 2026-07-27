package engine

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestShouldForwardSelection(t *testing.T) {
	yes := []EventType{
		EvRawStdout, EvCommandStdout, EvCommandStderr, EvCommandStarted,
		EvCommandDone, EvCommandFailed, EvInstallerDone, EvInstallerFailed,
		EvStageStarted, EvStageDone, EvStageFailed, EvKernel,
	}
	for _, et := range yes {
		if !shouldForward(InstallerEvent{Type: et}) {
			t.Errorf("%s should forward", et)
		}
	}
	// A made-up/unhandled type must not forward (e.g. a TUI-only signal).
	if shouldForward(InstallerEvent{Type: EventType("tui.frame")}) {
		t.Error("unknown event type must not forward")
	}
}

func TestFormatRFC3164(t *testing.T) {
	ev := InstallerEvent{
		Time: time.Date(2026, 6, 6, 21, 5, 9, 0, time.UTC),
		Type: EvCommandStdout, Stage: "storage-prepare", Message: "creating raid1\nmember sda",
	}
	line := formatRFC3164(ev, "node001.cluster", "part")
	// PRI = local0(16)*8 + info(6) = 134.
	if !strings.HasPrefix(line, "<134>Jun  6 21:05:09 node001.cluster lpart-install[part/storage-prepare]: ") {
		t.Fatalf("framing wrong: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("datagram must be single-line: %q", line)
	}
	if !strings.HasSuffix(line, "creating raid1 member sda") {
		t.Fatalf("message not collapsed/appended: %q", line)
	}
}

func TestFormatRFC3164SeverityAndScope(t *testing.T) {
	fail := formatRFC3164(InstallerEvent{Type: EvStageFailed, Stage: "osimage", Message: "boom"}, "n1", "part")
	if !strings.HasPrefix(fail, "<131>") { // err(3): 16*8+3
		t.Fatalf("failed stage should be err severity: %q", fail)
	}
	if !strings.Contains(fail, "lpart-install[part/osimage]: stage FAILED: boom") {
		t.Fatalf("stage failed tag/msg wrong: %q", fail)
	}
	k := formatRFC3164(InstallerEvent{Type: EvKernel, Message: "ata1 error", Data: 3}, "n1", "part")
	if !strings.HasPrefix(k, "<131>") || !strings.Contains(k, "lpart-install[kernel]:") {
		t.Fatalf("kernel scope/severity wrong: %q", k)
	}
}

// ForwardSyslog sends one datagram per forwarded event to a UDP listener and
// passes every event through, including ones that are not forwarded.
func TestForwardSyslogSendsAndPassesThrough(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	addr := pc.LocalAddr().String()

	in := make(chan InstallerEvent, 4)
	out, err := ForwardSyslog(context.Background(), addr, "node001.cluster", "part", in)
	if err != nil {
		t.Fatal(err)
	}
	in <- InstallerEvent{Type: EvCommandStdout, Stage: "s", Message: "hello", Time: time.Now()}
	in <- InstallerEvent{Type: EventType("tui.frame"), Message: "FRAME"} // not forwarded
	close(in)

	// Drain pass-through: both events come out unchanged.
	var got []InstallerEvent
	for ev := range out {
		got = append(got, ev)
	}
	if len(got) != 2 {
		t.Fatalf("pass-through count = %d, want 2", len(got))
	}

	// Exactly one datagram should have arrived (the forwarded stdout).
	buf := make([]byte, 2048)
	_ = pc.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("expected a datagram: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "node001.cluster lpart-install[part/s]: hello") {
		t.Fatalf("datagram wrong: %q", string(buf[:n]))
	}
	// No second datagram (the frame was not forwarded).
	_ = pc.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if n2, _, err := pc.ReadFrom(buf); err == nil {
		t.Fatalf("unexpected second datagram: %q", string(buf[:n2]))
	}
}

func TestForwardSyslogBadAddrIsBestEffort(t *testing.T) {
	// An empty addr: no forwarding, but events still pass through.
	in := make(chan InstallerEvent, 1)
	out, err := ForwardSyslog(context.Background(), "", "n1", "part", in)
	if err != nil {
		t.Fatal(err)
	}
	in <- InstallerEvent{Type: EvCommandStdout, Message: "x"}
	close(in)
	cnt := 0
	for range out {
		cnt++
	}
	if cnt != 1 {
		t.Fatalf("pass-through with no addr = %d, want 1", cnt)
	}
}
