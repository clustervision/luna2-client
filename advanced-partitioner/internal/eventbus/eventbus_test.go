package eventbus

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
)

func TestWireRoundTrip(t *testing.T) {
	ev := engine.InstallerEvent{
		Time:    time.Unix(1700000000, 0).UTC(),
		Type:    engine.EvStageStarted,
		Stage:   "storage-prepare",
		Message: "Prepare storage",
		Stream:  engine.StreamSystem,
	}
	b, err := json.Marshal(FromEvent(ev))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var w WireEvent
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := w.ToEvent()
	if got.Type != ev.Type || got.Stage != ev.Stage || got.Message != ev.Message || got.Stream != ev.Stream {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, ev)
	}
	if !got.Time.Equal(ev.Time) {
		t.Fatalf("time mismatch: %v vs %v", got.Time, ev.Time)
	}
}

func TestFromEventStampsZeroTime(t *testing.T) {
	w := FromEvent(engine.InstallerEvent{Type: engine.EvKernel})
	if w.T.IsZero() {
		t.Fatal("zero Time should be stamped with now")
	}
}

func TestWriterToReader(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	ctx := context.Background()
	out := Read(ctx, pr)

	want := []engine.EventType{engine.EvStageStarted, engine.EvCommandStdout, engine.EvStageDone}
	go func() {
		for _, ty := range want {
			_ = w.Emit(engine.InstallerEvent{Type: ty, Stage: "s", Message: "m"})
		}
		_ = pw.Close()
	}()

	var got []engine.EventType
	for ev := range out {
		got = append(got, ev.Type)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadSkipsMalformedLines(t *testing.T) {
	pr, pw := io.Pipe()
	ctx := context.Background()
	out := Read(ctx, pr)
	go func() {
		_, _ = pw.Write([]byte("not json\n"))
		_, _ = pw.Write([]byte(`{"type":"stage.done","stage":"x"}` + "\n"))
		_, _ = pw.Write([]byte("{partial\n"))
		_ = pw.Close()
	}()

	var got []engine.InstallerEvent
	for ev := range out {
		got = append(got, ev)
	}
	if len(got) != 1 || got[0].Type != engine.EvStageDone || got[0].Stage != "x" {
		t.Fatalf("expected exactly the one valid event, got %+v", got)
	}
}

func TestTeePassesThroughAndEmits(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan engine.InstallerEvent)
	passed := w.Tee(ctx, src)
	busOut := Read(ctx, pr)

	// Drain the bus concurrently: a synchronous io.Pipe write only completes once
	// the reader consumes it, so the bus must be read while Tee emits.
	var onBus int
	busDone := make(chan struct{})
	go func() {
		for ev := range busOut {
			if ev.Stage == "a" {
				onBus++
			}
		}
		close(busDone)
	}()

	go func() {
		src <- engine.InstallerEvent{Type: engine.EvStageStarted, Stage: "a"}
		close(src)
	}()

	// Drain the pass-through (proves the chain link forwards unchanged). When this
	// completes, Tee has emitted every event and the reader has consumed it.
	var passthrough int
	for range passed {
		passthrough++
	}
	if passthrough != 1 {
		t.Fatalf("pass-through forwarded %d events, want 1", passthrough)
	}

	// Tee is done emitting — now signal EOF so the bus reader drains and stops.
	_ = pw.Close()
	<-busDone
	if onBus != 1 {
		t.Fatalf("bus saw %d events, want 1", onBus)
	}
}

func TestRehydrateConcreteTypes(t *testing.T) {
	// A panel-update event crosses the bus as raw JSON; Rehydrate must restore
	// the concrete type the TUI model type-asserts on.
	ev := engine.InstallerEvent{Type: engine.EvStorageUpdated, Data: engine.StorageUpdate{}}
	crossed := FromEvent(ev).ToEvent() // Data is now json.RawMessage, as off the wire
	if _, ok := crossed.Data.(engine.StorageUpdate); ok {
		t.Fatal("precondition: crossed event should carry raw JSON, not the concrete type")
	}
	got := Rehydrate(crossed)
	if _, ok := got.Data.(engine.StorageUpdate); !ok {
		t.Fatalf("Rehydrate did not restore StorageUpdate, Data is %T", got.Data)
	}

	// A kernel level int round-trips too.
	k := Rehydrate(FromEvent(engine.InstallerEvent{Type: engine.EvKernel, Data: 3}).ToEvent())
	if lvl, ok := k.Data.(int); !ok || lvl != 3 {
		t.Fatalf("Rehydrate kernel level = %v (%T), want int 3", k.Data, k.Data)
	}

	// An event with no payload passes through untouched.
	plain := Rehydrate(engine.InstallerEvent{Type: engine.EvStageStarted, Stage: "s"})
	if plain.Stage != "s" || plain.Data != nil {
		t.Fatalf("Rehydrate mangled a payload-less event: %+v", plain)
	}
}

func TestDecodeData(t *testing.T) {
	type payload struct {
		Disk string `json:"disk"`
	}
	ev := engine.InstallerEvent{Type: engine.EvStorageUpdated, Data: payload{Disk: "sda"}}
	// Project to the wire and back so Data is a json.RawMessage, as the renderer sees it.
	on := FromEvent(ev)
	got := on.ToEvent()

	var p payload
	if !DecodeData(got, &p) {
		t.Fatal("DecodeData returned false for a present payload")
	}
	if p.Disk != "sda" {
		t.Fatalf("decoded %q, want sda", p.Disk)
	}
	// No payload → false.
	if DecodeData(engine.InstallerEvent{Type: engine.EvStageDone}, &p) {
		t.Fatal("DecodeData should be false when there is no Data")
	}
}
