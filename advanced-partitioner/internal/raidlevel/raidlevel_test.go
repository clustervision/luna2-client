package raidlevel

import "testing"

func TestRaidMinMaxDisks(t *testing.T) {
	cases := []struct {
		r        Raid
		min, max int
	}{
		{RaidNone, 1, 1},
		{Raid0, 2, 0},
		{Raid1, 2, 2},
		{Raid5, 4, 0},
		{Raid6, 5, 0},
		{Raid10, 4, 0},
	}
	for _, c := range cases {
		if got := c.r.MinDisks(); got != c.min {
			t.Errorf("raid %s MinDisks=%d want %d", c.r, got, c.min)
		}
		if got := c.r.MaxDisks(); got != c.max {
			t.Errorf("raid %s MaxDisks=%d want %d", c.r, got, c.max)
		}
	}
}

func TestRaidAllowsCount(t *testing.T) {
	cases := []struct {
		r    Raid
		n    int
		want bool
	}{
		{RaidNone, 1, true},
		{RaidNone, 2, false},
		{Raid1, 2, true},
		{Raid1, 3, false},
		{Raid5, 3, false},
		{Raid5, 4, true},
		{Raid5, 9, true},
		{Raid6, 4, false},
		{Raid6, 5, true},
		{Raid10, 4, true},
		// FTA D8: raid10 even-member rule enforced at config validation,
		// not as a late runtime abort after disks were selected.
		{Raid10, 5, false},
		{Raid10, 6, true},
	}
	for _, c := range cases {
		if got := c.r.AllowsCount(c.n); got != c.want {
			t.Errorf("raid %s AllowsCount(%d)=%v want %v", c.r, c.n, got, c.want)
		}
	}
}

func TestRaidMappers(t *testing.T) {
	if RaidNone.MDLevel() != "" {
		t.Errorf("none MDLevel should be empty, got %q", RaidNone.MDLevel())
	}
	if Raid5.MDLevel() != "5" {
		t.Errorf("raid5 MDLevel=%q want 5", Raid5.MDLevel())
	}
	if RaidNone.RAIDMode() != "none" {
		t.Errorf("none RAIDMode=%q want none", RaidNone.RAIDMode())
	}
	if Raid1.RAIDMode() != "raid1" {
		t.Errorf("raid1 RAIDMode=%q want raid1", Raid1.RAIDMode())
	}
}

func TestParseRaid(t *testing.T) {
	if _, err := ParseRaid("10"); err != nil {
		t.Errorf("ParseRaid(10) unexpected error: %v", err)
	}
	if _, err := ParseRaid("7"); err == nil {
		t.Error("ParseRaid(7) should fail")
	}
}

func TestEffectiveCount(t *testing.T) {
	if got := EffectiveCount(Raid1, 0); got != 2 {
		t.Errorf("EffectiveCount(raid1, 0)=%d want 2", got)
	}
	if got := EffectiveCount(Raid5, 6); got != 6 {
		t.Errorf("EffectiveCount(raid5, 6)=%d want 6", got)
	}
	if got := EffectiveCount(RaidNone, 0); got != 1 {
		t.Errorf("EffectiveCount(none, 0)=%d want 1", got)
	}
}
