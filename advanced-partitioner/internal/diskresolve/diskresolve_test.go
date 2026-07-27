package diskresolve

import (
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
)

const (
	gib = int64(1024 * 1024 * 1024)
	tib = 1024 * gib
)

func dev(path string, sizeGiB int64, tags ...string) discover.Device {
	return discover.Device{Path: path, KernelPath: path, Size: sizeGiB * gib, Tags: tags}
}

// --- tag derivation + ssd/nvme overlap -----------------------------------

func TestDeriveTags(t *testing.T) {
	cases := []struct {
		name, tran string
		rota       bool
		want       []string
	}{
		{"sda", "sata", true, []string{"rotary"}},
		{"sdb", "sata", false, []string{"ssd"}},
		{"nvme0n1", "nvme", false, []string{"ssd", "nvme"}},
		{"nvme0n1", "", false, []string{"ssd", "nvme"}}, // name-based nvme detection
		{"vda", "virtio", false, []string{"ssd"}},
	}
	for _, c := range cases {
		got := discover.DeriveTags(c.name, c.tran, c.rota)
		if len(got) != len(c.want) {
			t.Errorf("%s: tags=%v want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: tags=%v want %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestNVMeCarriesSSDTag(t *testing.T) {
	d := dev("/dev/nvme0n1", 500, "ssd", "nvme")
	if !d.HasTag("ssd") || !d.HasTag("nvme") {
		t.Error("nvme disk must carry both ssd and nvme tags")
	}
	if !(Match{Tags: []string{"ssd"}}).Matches(d) {
		t.Error("ssd match must include an nvme disk")
	}
}

// --- size parser ---------------------------------------------------------

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"8T", 8 * tib, true},
		{"400G", 400 * gib, true},
		{"512M", 512 * 1024 * 1024, true},
		{"100K", 100 * 1024, true},
		{"2048B", 2048, true},
		{"2048", 2048, true},
		{"8TB", 8 * tib, true}, // legacy two-letter form
		{"  16G ", 16 * gib, true},
		{"", 0, false},
		{"big", 0, false},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseSize(%q) unexpected error: %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("ParseSize(%q) should fail", c.in)
		}
		if c.ok && got != c.want {
			t.Errorf("ParseSize(%q)=%d want %d", c.in, got, c.want)
		}
	}
}

// --- match filtering -----------------------------------------------------

func TestMatchFiltering(t *testing.T) {
	ssd := dev("/dev/sdb", 500, "ssd")
	nvme := dev("/dev/nvme0n1", 2000, "ssd", "nvme")
	hdd := dev("/dev/sda", 8000, "rotary")
	hdd.Model = "Seagate ST8000"

	if (Match{Tags: []string{"nvme"}}).Matches(ssd) {
		t.Error("nvme match should reject a plain ssd")
	}
	if !(Match{Tags: []string{"nvme"}}).Matches(nvme) {
		t.Error("nvme match should accept an nvme")
	}
	if !(Match{MinSize: 1 * tib}).Matches(nvme) {
		t.Error("min_size=1T should accept a 2T disk")
	}
	if (Match{MaxSize: 1 * tib}).Matches(nvme) {
		t.Error("max_size=1T should reject a 2T disk")
	}
	if !(Match{Model: "seagate"}).Matches(hdd) {
		t.Error("model substring (case-insensitive) should match")
	}
	if (Match{Model: "samsung"}).Matches(hdd) {
		t.Error("non-matching model should reject")
	}
}

// --- resolve -------------------------------------------------------------

func TestResolveSmallestFirstWithinTolerance(t *testing.T) {
	pool := []discover.Device{
		dev("/dev/sdc", 1000, "ssd"),
		dev("/dev/sda", 500, "ssd"),
		dev("/dev/sdb", 500, "ssd"),
	}
	got, err := Resolve(pool, Match{Tags: []string{"ssd"}}, 2, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Path != "/dev/sda" || got[1].Path != "/dev/sdb" {
		t.Fatalf("expected the two 500G disks smallest-first, got %+v", got)
	}
}

func TestResolveCountPlusSpares(t *testing.T) {
	pool := []discover.Device{
		dev("/dev/sda", 500, "ssd"),
		dev("/dev/sdb", 500, "ssd"),
		dev("/dev/sdc", 500, "ssd"),
	}
	got, err := Resolve(pool, Match{}, 2, 1, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("count=2 spares=1 should pick 3 disks, got %d", len(got))
	}
}

func TestResolveNotEnough(t *testing.T) {
	pool := []discover.Device{dev("/dev/sda", 500, "ssd")}
	if _, err := Resolve(pool, Match{Tags: []string{"ssd"}}, 2, 0, 0); err == nil {
		t.Fatal("expected not-enough-disks error")
	}
}

func TestResolveToleranceViolation(t *testing.T) {
	pool := []discover.Device{
		dev("/dev/sda", 500, "ssd"),
		dev("/dev/sdb", 2000, "ssd"), // 4x larger -> exceeds 5%
	}
	if _, err := Resolve(pool, Match{}, 2, 0, 5); err == nil {
		t.Fatal("expected tolerance violation error")
	}
}

func TestResolveTagFilterExcludes(t *testing.T) {
	pool := []discover.Device{
		dev("/dev/sda", 8000, "rotary"),
		dev("/dev/nvme0n1", 2000, "ssd", "nvme"),
	}
	got, err := Resolve(pool, Match{Tags: []string{"nvme"}}, 1, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/dev/nvme0n1" {
		t.Fatalf("nvme match should select only the nvme disk, got %+v", got)
	}
}
