package uiutil

import "github.com/charmbracelet/lipgloss"

// Truncate cuts s to width visible cells, appending "~" when cut. The
// marker is ASCII on purpose: the node's VGA console renders "…" as a ■.
// Width<=0 returns "".
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "~"
	}
	// Walk runes until we'd overflow.
	w := 0
	out := make([]rune, 0, len(s))
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "~"
}
