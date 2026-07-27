package cleanup

import "testing"

func TestDiskBasename(t *testing.T) {
	cases := map[string]string{
		"/dev/sda":          "sda",
		"/dev/sda1":         "sda",
		"/dev/sdb12":        "sdb",
		"/dev/vda":          "vda",
		"/dev/vda1":         "vda",
		"/dev/hda5":         "hda",
		"/dev/xvda3":        "xvda",
		"/dev/nvme0n1":      "nvme0n1",
		"/dev/nvme0n1p1":    "nvme0n1",
		"/dev/nvme0n2p15":   "nvme0n2",
		"/dev/mmcblk0":      "mmcblk0",
		"/dev/mmcblk0p3":    "mmcblk0",
		"/dev/md0":          "md0",
		"/dev/md125":        "md125",
		"/dev/md0p1":        "md0",
		"/dev/loop3":        "loop3",
		"/dev/loop3p1":      "loop3",
		"/dev/disk/by-id/x": "x",
	}
	for in, want := range cases {
		if got := DiskBasename(in); got != want {
			t.Fatalf("DiskBasename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectedDisksSet(t *testing.T) {
	set := SelectedDisksSet([]string{"/dev/sda", "/dev/nvme0n1", "/dev/sdb"})
	for _, k := range []string{"sda", "nvme0n1", "sdb"} {
		if _, ok := set[k]; !ok {
			t.Fatalf("missing %q in selected set", k)
		}
	}
	if len(set) != 3 {
		t.Fatalf("set size = %d, want 3", len(set))
	}
}

func TestPartitionOnSelectedDisk(t *testing.T) {
	selected := SelectedDisksSet([]string{"/dev/sda", "/dev/nvme0n1"})
	cases := map[string]bool{
		"/dev/sda1":       true,
		"/dev/sda12":      true,
		"/dev/sda":        true,
		"/dev/sdb1":       false,
		"/dev/nvme0n1":    true,
		"/dev/nvme0n1p1":  true,
		"/dev/nvme0n1p15": true,
		"/dev/nvme0n2p1":  false,
		"/dev/md0":        false,
	}
	for in, want := range cases {
		if got := PartitionOnSelectedDisk(in, selected); got != want {
			t.Fatalf("PartitionOnSelectedDisk(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsMDDevice(t *testing.T) {
	cases := map[string]bool{
		"md0":   true,
		"md125": true,
		// Bash glob md* matches "md" itself with a zero-length wildcard;
		// IsMDDevice mirrors that.
		"md":    true,
		"sda":   false,
		"mdadm": true, // matches the md* glob in practice
		"":      false,
	}
	for in, want := range cases {
		if got := IsMDDevice(in); got != want {
			t.Fatalf("IsMDDevice(%q) = %v, want %v", in, got, want)
		}
	}
}
