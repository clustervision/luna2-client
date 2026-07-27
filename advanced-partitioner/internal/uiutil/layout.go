package uiutil

// SplitHorizontal divides total cells between two panels by ratio
// (0..1). Returns left/right widths; min ensures neither panel collapses.
func SplitHorizontal(total int, ratio float64, min int) (int, int) {
	if total < min*2 {
		// Degenerate: not enough room for two panels. Give them
		// whatever we can; the renderer can clip further.
		l := total / 2
		return l, total - l
	}
	l := int(float64(total) * ratio)
	if l < min {
		l = min
	}
	if total-l < min {
		l = total - min
	}
	return l, total - l
}
