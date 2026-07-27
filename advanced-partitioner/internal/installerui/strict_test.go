package installerui

// Barrier-pinning tests for the strict renderer semantics (FTA Cluster B,
// B2/B3): under StrictExit, an interrupted or truncated event stream must
// never render as success, and a fatal-classed failure exits 3.

import (
	"context"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/pipeline"
)

func TestPlainStrictStreamEndWithoutTerminalIsFailure(t *testing.T) {
	events := make(chan engine.InstallerEvent, 4)
	events <- engine.InstallerEvent{Type: engine.EvStageStarted, Stage: "storage-prepare"}
	close(events) // stream ends with no installer.done/failed
	if got := runPlain(context.Background(), events, true); got != 1 {
		t.Fatalf("strict: truncated stream must exit 1, got %d", got)
	}
}

func TestPlainStrictCtxCancelIsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()                                   // SIGINT mid-install
	events := make(chan engine.InstallerEvent) // nothing arrives
	if got := runPlain(ctx, events, true); got != 1 {
		t.Fatalf("strict: cancelled context must exit 1, got %d", got)
	}
}

func TestPlainLenientKeepsViewerSemantics(t *testing.T) {
	events := make(chan engine.InstallerEvent, 1)
	close(events)
	if got := runPlain(context.Background(), events, false); got != 0 {
		t.Fatalf("lenient (lpart-tui viewer): truncated stream stays 0, got %d", got)
	}
}

func TestPlainSuccessAfterTerminalDone(t *testing.T) {
	events := make(chan engine.InstallerEvent, 2)
	events <- engine.InstallerEvent{Type: engine.EvInstallerDone}
	close(events)
	if got := runPlain(context.Background(), events, true); got != 0 {
		t.Fatalf("done stream must exit 0, got %d", got)
	}
}

func TestPlainFatalClassExitsThree(t *testing.T) {
	events := make(chan engine.InstallerEvent, 2)
	events <- engine.InstallerEvent{
		Type: engine.EvInstallerFailed, Message: "data fence",
		Data: pipeline.FailFatal,
	}
	close(events)
	if got := runPlain(context.Background(), events, true); got != 3 {
		t.Fatalf("fatal-classed failure must exit 3 (park), got %d", got)
	}
}

func TestPlainRetryableClassExitsOne(t *testing.T) {
	events := make(chan engine.InstallerEvent, 2)
	events <- engine.InstallerEvent{
		Type: engine.EvInstallerFailed, Message: "transient",
		Data: pipeline.FailRetryable,
	}
	close(events)
	if got := runPlain(context.Background(), events, true); got != 1 {
		t.Fatalf("retryable failure must exit 1, got %d", got)
	}
}
