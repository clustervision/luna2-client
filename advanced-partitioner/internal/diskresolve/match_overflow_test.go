package diskresolve

import "testing"

// 16777216T used to wrap to 0 (no bound): a min_size that matched every disk.
func TestParseSizeOverflowIsAnError(t *testing.T) {
	if _, err := ParseSize("16777216T"); err == nil {
		t.Fatal("an overflowing size must be refused, not wrapped to 0")
	}
	if v, err := ParseSize("8T"); err != nil || v != 8<<40 {
		t.Fatalf("8T = %d, %v", v, err)
	}
	if _, err := ParseMatch(nil, "16777216T", "1G", ""); err == nil {
		t.Fatal("match with an overflowing min_size must be refused")
	}
}
