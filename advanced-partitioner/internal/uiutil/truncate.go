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

// Wrap soft-wraps s to width visible cells, breaking at the last space in a
// row rather than mid-word unless the row holds a single long token. Used for
// the lines a reader must see whole; everything else goes through Truncate.
// Always returns at least one row.
func Wrap(s string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	runes := []rune(s)
	var rows []string
	for len(runes) > 0 {
		w, cut, space := 0, 0, -1
		for cut < len(runes) {
			rw := lipgloss.Width(string(runes[cut]))
			if w+rw > width {
				break
			}
			if runes[cut] == ' ' {
				space = cut
			}
			w += rw
			cut++
		}
		if cut == len(runes) {
			rows = append(rows, string(runes))
			break
		}
		if cut == 0 {
			cut = 1 // a rune wider than the whole row; never loop
		} else if space > 0 {
			cut = space
		}
		rows = append(rows, string(runes[:cut]))
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == ' ' {
			runes = runes[1:]
		}
	}
	if len(rows) == 0 {
		rows = append(rows, "")
	}
	return rows
}
