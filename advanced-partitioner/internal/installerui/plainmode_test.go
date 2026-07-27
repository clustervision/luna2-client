package installerui

import (
	"os"
	"testing"
)

// PlainMode must fall back to plain when stdout has no queryable window size
// (the go-test stdout is a pipe, so TIOCGWINSZ fails -> terminalTooSmall=true).
// This is the BE-U1 serial-console (0x0) fallback: an unusable terminal must
// never leave the operator stuck on the "terminal too small" TUI screen.
func TestPlainModeFallsBackOnUnsizedTerminal(t *testing.T) {
	if !terminalTooSmall() {
		t.Skip("test stdout unexpectedly reports a usable TTY size; fallback path not exercised here")
	}
	if !PlainMode(false) {
		t.Fatal("PlainMode should be true when the terminal is too small / unsized")
	}
}

// The explicit opt-outs still force plain regardless of terminal size.
func TestPlainModeExplicitFlags(t *testing.T) {
	if !PlainMode(true) {
		t.Fatal("--no-tui flag must force plain")
	}
	t.Setenv("LPART_NOTUI", "1")
	if !PlainMode(false) {
		t.Fatal("LPART_NOTUI env must force plain")
	}
	_ = os.Unsetenv("LPART_NOTUI")
}
