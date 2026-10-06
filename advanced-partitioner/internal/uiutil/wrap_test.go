package uiutil

import (
	"strings"
	"testing"
)

func TestWrap(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"fits", "short line", 20, []string{"short line"}},
		{"breaks at the last space", "aaa bbb ccc ddd", 8, []string{"aaa bbb", "ccc ddd"}},
		{"long token is cut mid-word", "/dev/mapper/averylongvolumename", 10, []string{"/dev/mappe", "r/averylon", "gvolumenam", "e"}},
		{"empty", "", 10, []string{""}},
		{"no width", "anything", 0, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Wrap(tc.in, tc.width)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("Wrap(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
			for _, row := range got {
				if len(row) > tc.width && tc.width > 0 {
					t.Fatalf("row %q exceeds width %d", row, tc.width)
				}
			}
		})
	}
}

// Nothing may be dropped: every non-space rune of the input survives.
func TestWrapLosesNothing(t *testing.T) {
	in := `run_cmd failed path=tar exit_code=2 stderr="tar: /mnt/x: No space left on device"`
	joined := strings.Join(Wrap(in, 17), " ")
	for _, field := range strings.Fields(in) {
		if !strings.Contains(joined, field) {
			t.Fatalf("Wrap dropped %q from %q", field, joined)
		}
	}
}
