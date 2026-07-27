package devices

import "testing"

func TestPartitionPath(t *testing.T) {
	cases := map[[2]string]string{
		{"/dev/sda", "1"}:      "/dev/sda1",
		{"/dev/sda", "2"}:      "/dev/sda2",
		{"/dev/sda", "10"}:     "/dev/sda10",
		{"/dev/vda", "1"}:      "/dev/vda1",
		{"/dev/hda", "5"}:      "/dev/hda5",
		{"/dev/xvda", "3"}:     "/dev/xvda3",
		{"/dev/nvme0n1", "1"}:  "/dev/nvme0n1p1",
		{"/dev/nvme1n2", "12"}: "/dev/nvme1n2p12",
		{"/dev/mmcblk0", "3"}:  "/dev/mmcblk0p3",
		{"/dev/loop0", "1"}:    "/dev/loop0p1",
		{"/dev/md0", "1"}:      "/dev/md0p1",
		// udev persistent-name symlinks use a "-part<N>" suffix. Regression
		// pin: appending a bare number produced "…pci-0000:03:00.02" which
		// does not exist and broke single-disk manual-by-path OS mounts.
		{"/dev/disk/by-path/pci-0000:03:00.0", "2"}:     "/dev/disk/by-path/pci-0000:03:00.0-part2",
		{"/dev/disk/by-path/pci-0000:03:00.0", "1"}:     "/dev/disk/by-path/pci-0000:03:00.0-part1",
		{"/dev/disk/by-id/wwn-0x55cd2e4152071916", "3"}: "/dev/disk/by-id/wwn-0x55cd2e4152071916-part3",
		{"/dev/disk/by-id/nvme-Samsung_SSD_970", "12"}:  "/dev/disk/by-id/nvme-Samsung_SSD_970-part12",
	}
	for in, want := range cases {
		var n int
		for _, ch := range in[1] {
			n = n*10 + int(ch-'0')
		}
		got := PartitionPath(in[0], n)
		if got != want {
			t.Fatalf("PartitionPath(%q, %s) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestVGLVPath(t *testing.T) {
	cases := []struct {
		vg, lv, want string
	}{
		{"os", "root", "/dev/mapper/os-root"},
		{"os", "boot", "/dev/mapper/os-boot"},
		{"os-data", "log", "/dev/mapper/os--data-log"},
		{"foo", "bar-baz", "/dev/mapper/foo-bar--baz"},
		{"a-b-c", "x-y-z", "/dev/mapper/a--b--c-x--y--z"},
	}
	for _, c := range cases {
		got := VGLVPath(c.vg, c.lv)
		if got != c.want {
			t.Fatalf("VGLVPath(%q,%q) = %q, want %q", c.vg, c.lv, got, c.want)
		}
	}
}

func TestItoaNegativeAndZero(t *testing.T) {
	// itoa is internal but exercised by partition numbers >9; cover
	// the cold path explicitly.
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0) = %q", got)
	}
	if got := itoa(123); got != "123" {
		t.Fatalf("itoa(123) = %q", got)
	}
	if got := itoa(-5); got != "-5" {
		t.Fatalf("itoa(-5) = %q", got)
	}
}
