package engine

import (
	"testing"
	"time"
)

// TestFanInMergesMultipleAux is the regression test for the bug where FanIn
// only watched aux[0] and silently dropped every other auxiliary channel
// (so kmsg events, added as a 2nd aux, never reached the TUI).
func TestFanInMergesMultipleAux(t *testing.T) {
	src := make(chan InstallerEvent, 1)
	aux1 := make(chan InstallerEvent, 1)
	aux2 := make(chan InstallerEvent, 1)
	out := FanIn(16, src, aux1, aux2)

	aux1 <- InstallerEvent{Message: "a1"}
	aux2 <- InstallerEvent{Message: "a2"} // the old FanIn dropped this
	src <- InstallerEvent{Message: "s"}

	recv := func() string {
		select {
		case ev := <-out:
			return ev.Message
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for merged event")
			return ""
		}
	}
	got := map[string]bool{}
	for i := 0; i < 3; i++ {
		got[recv()] = true
	}
	for _, want := range []string{"s", "a1", "a2"} {
		if !got[want] {
			t.Errorf("missing merged event %q (got %v)", want, got)
		}
	}
	close(src) // src close terminates the merge goroutine
}

// TestFanInClosesWhenSrcCloses confirms the output channel closes after src.
func TestFanInClosesWhenSrcCloses(t *testing.T) {
	src := make(chan InstallerEvent)
	out := FanIn(16, src)
	close(src)
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected closed out channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("out did not close after src closed")
	}
}
