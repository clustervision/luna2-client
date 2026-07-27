package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

func newTestModel() Model {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan engine.InstallerEvent, 1)
	initial := model.Installer{
		StartedAt: time.Now(),
		Stages: []model.Stage{
			{ID: "preflight", Title: "Preflight", Status: model.StagePending},
			{ID: "partition", Title: "Partition", Status: model.StagePending},
			{ID: "osimage", Title: "OS image", Status: model.StagePending},
		},
		Storage: model.Storage{},
	}
	m := NewModel(ctx, cancel, events, initial, nil)
	m.Width, m.Height = 120, 40
	return m
}

func TestApplyEventRawStdoutAppendsOutput(t *testing.T) {
	m := newTestModel()
	m.applyEvent(engine.InstallerEvent{
		Type:    engine.EvRawStdout,
		Stream:  engine.StreamStdout,
		Message: "mounted tmpfs at /mnt/osimg",
	})
	if got := len(m.State.Output.Lines); got != 1 {
		t.Fatalf("expected 1 output line, got %d", got)
	}
	if cl := m.State.Output.Lines[0].Class; cl != model.ClassSuccess {
		t.Errorf("class: got %q want success", cl)
	}
}

func TestApplyEventStderrIsStderrClass(t *testing.T) {
	m := newTestModel()
	m.applyEvent(engine.InstallerEvent{
		Type:    engine.EvRawStderr,
		Stream:  engine.StreamStderr,
		Message: "extracting tarball, please wait",
	})
	if cl := m.State.Output.Lines[0].Class; cl != model.ClassStderr {
		t.Errorf("class: got %q want stderr", cl)
	}
}

func TestApplyEventStageBanner(t *testing.T) {
	m := newTestModel()
	m.applyEvent(engine.InstallerEvent{
		Type:    engine.EvStageStarted,
		Stage:   "partition",
		Message: "",
	})
	if got := stageStatus(m.State.Stages, "partition"); got != model.StageRunning {
		t.Errorf("partition: got %q want running", got)
	}
	if len(m.State.Output.Lines) != 1 {
		t.Fatalf("expected 1 banner line, got %d", len(m.State.Output.Lines))
	}
	if cl := m.State.Output.Lines[0].Class; cl != model.ClassStage {
		t.Errorf("banner class: got %q want stage", cl)
	}
}

func TestApplyEventOutputRingBuffer(t *testing.T) {
	m := newTestModel()
	for i := 0; i < MaxOutputLines+50; i++ {
		m.applyEvent(engine.InstallerEvent{
			Type:    engine.EvRawStdout,
			Stream:  engine.StreamStdout,
			Message: "line",
		})
	}
	if got := len(m.State.Output.Lines); got != MaxOutputLines {
		t.Fatalf("ring buffer: got %d want %d", got, MaxOutputLines)
	}
}

func TestUpdateKeyQuitCancelsContext(t *testing.T) {
	m := newTestModel()
	cancelled := false
	parentCancel := m.cancel
	m.cancel = func() { cancelled = true; parentCancel() }

	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !cancelled {
		t.Errorf("Ctrl+C should cancel context")
	}
	if newModel == nil {
		t.Errorf("Update should return a model")
	}
	if cmd == nil {
		t.Errorf("expected tea.Quit cmd")
	}
}

// --- helpers ---

func stageStatus(stages []model.Stage, id string) model.StageStatus {
	for _, s := range stages {
		if s.ID == id {
			return s.Status
		}
	}
	return ""
}

func TestOutputScrollUpDownFollow(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 50; i++ {
		m.appendOutput("line", "stdout", "")
	}
	if m.OutputScroll != 0 {
		t.Fatalf("starts following the tail, got %d", m.OutputScroll)
	}
	for i := 0; i < 6; i++ {
		m.cursorUp() // page back into history
	}
	if m.OutputScroll != 6 {
		t.Fatalf("6 ups -> 6, got %d", m.OutputScroll)
	}
	m.cursorDown()
	if m.OutputScroll != 5 {
		t.Fatalf("down -> 5, got %d", m.OutputScroll)
	}
}

func TestOutputScrollClamps(t *testing.T) {
	m := newTestModel()
	m.appendOutput("a", "stdout", "")
	m.appendOutput("b", "stdout", "")
	for i := 0; i < 20; i++ {
		m.cursorUp()
	}
	if max := len(m.State.Output.Lines) - 1; m.OutputScroll > max {
		t.Fatalf("over-scrolled past history: %d > %d", m.OutputScroll, max)
	}
	for i := 0; i < 20; i++ {
		m.cursorDown()
	}
	if m.OutputScroll != 0 {
		t.Fatalf("down should clamp to 0 (follow tail), got %d", m.OutputScroll)
	}
}

func TestUpdateKeyScrollsOutput(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 10; i++ {
		m.appendOutput("l", "stdout", "")
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}) // k = Up
	if got := out.(Model).OutputScroll; got != 1 {
		t.Fatalf("k -> OutputScroll 1, got %d", got)
	}
}
