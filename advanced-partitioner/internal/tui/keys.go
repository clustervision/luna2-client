// Package tui is the Bubble Tea front-end. It receives engine events,
// updates its model copy, and renders the single-screen dashboard. It
// never performs destructive work directly - that's the engine's job.
package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap holds the application's keybindings. Footer hints are rendered
// from this map so they can't drift out of sync.
type KeyMap struct {
	Up     key.Binding
	Down   key.Binding
	PageUp key.Binding
	PageDn key.Binding
	Shell  key.Binding
	Reboot key.Binding
	Redraw key.Binding
	Quit   key.Binding
}

// DefaultKeys returns the keymap from tui-design.plan §"Footer keys".
func DefaultKeys() KeyMap {
	return KeyMap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "up")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
		PageUp: key.NewBinding(key.WithKeys("pgup")),
		PageDn: key.NewBinding(key.WithKeys("pgdown")),
		Shell:  key.NewBinding(key.WithKeys("f2"), key.WithHelp("F2", "shell")),
		Reboot: key.NewBinding(key.WithKeys("f10"), key.WithHelp("F10", "reboot")),
		Redraw: key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("Ctrl+L", "redraw")),
		Quit:   key.NewBinding(key.WithKeys("ctrl+c", "q"), key.WithHelp("Ctrl+C/q", "quit")),
	}
}
