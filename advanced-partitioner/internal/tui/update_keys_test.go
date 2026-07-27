package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

func TestKernelEventRendersInOutput(t *testing.T) {
	m := newTestModel()
	m.applyEvent(engine.InstallerEvent{Type: engine.EvKernel, Message: "ipmi_si: could not find device", Data: 3})
	lines := m.State.Output.Lines
	if len(lines) == 0 {
		t.Fatal("no output line added for kernel event")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last.Raw, "[kernel] ipmi_si: could not find device") {
		t.Errorf("kernel line not rendered: %q", last.Raw)
	}
	if last.Class != model.ClassError {
		t.Errorf("level-3 kernel line should be error class, got %s", last.Class)
	}

	// A warning-level kernel line renders as a warning, not an error.
	m.applyEvent(engine.InstallerEvent{Type: engine.EvKernel, Message: "ata1: link slow", Data: 4})
	last = m.State.Output.Lines[len(m.State.Output.Lines)-1]
	if last.Class != model.ClassWarning {
		t.Errorf("level-4 kernel line should be warning class, got %s", last.Class)
	}
}

func TestRebootConfirm(t *testing.T) {
	m := newTestModel()
	rebooted := false
	m.reboot = func() { rebooted = true }

	// First F10 arms, runs no command, does not reboot.
	m2, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyF10})
	if !m2.(Model).rebootArmed || cmd != nil || rebooted {
		t.Fatal("first F10 should only arm the confirmation")
	}
	// Second F10 disarms and returns a command that reboots.
	m3, cmd2 := m2.(Model).handleKey(tea.KeyMsg{Type: tea.KeyF10})
	if m3.(Model).rebootArmed {
		t.Fatal("second F10 should disarm")
	}
	if cmd2 == nil {
		t.Fatal("second F10 should return a reboot command")
	}
	cmd2()
	if !rebooted {
		t.Fatal("reboot command should call the reboot func")
	}
}

func TestRebootDisarmOnOtherKey(t *testing.T) {
	m := newTestModel()
	m.reboot = func() {}
	m2, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyF10}) // arm
	if !m2.(Model).rebootArmed {
		t.Fatal("should be armed")
	}
	m3, _ := m2.(Model).handleKey(tea.KeyMsg{Type: tea.KeyF2}) // any other key disarms
	if m3.(Model).rebootArmed {
		t.Fatal("a non-F10 key should cancel the pending reboot")
	}
}

func TestRebootUnsupported(t *testing.T) {
	m := newTestModel() // reboot is nil
	m2, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyF10})
	if m2.(Model).rebootArmed || cmd != nil {
		t.Fatal("nil reboot must not arm or run a command")
	}
}

func TestFooterDropsDiscovery(t *testing.T) {
	m := newTestModel()
	f := stripANSI(m.renderFooter(200))
	if strings.Contains(f, "Discovery") {
		t.Errorf("footer still advertises the non-functional Discovery key: %q", f)
	}
	for _, want := range []string{"Shell", "Reboot", "Redraw", "Quit"} {
		if !strings.Contains(f, want) {
			t.Errorf("footer missing %q: %q", want, f)
		}
	}
}
