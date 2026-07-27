package discover

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

const lsblkSampleModern = `{
  "blockdevices": [
    {"name": "sda", "path": "/dev/sda", "type": "disk", "size": 107374182400, "rota": true,  "tran": "sata"},
    {"name": "sdb", "path": "/dev/sdb", "type": "disk", "size": 107374182400, "rota": false, "tran": "sata"},
    {"name": "nvme0n1", "path": "/dev/nvme0n1", "type": "disk", "size": 1000204886016, "rota": false, "tran": "nvme"},
    {"name": "sdc", "path": "/dev/sdc", "type": "disk", "size": 32212254720,  "rota": true,  "tran": "usb"},
    {"name": "loop0", "path": "/dev/loop0", "type": "loop", "size": 1048576,    "rota": false, "tran": ""},
    {"name": "ram0", "path": "/dev/ram0", "type": "disk", "size": 0,          "rota": false, "tran": ""}
  ]
}`

const lsblkSampleLegacy = `{
  "blockdevices": [
    {"name": "sda", "path": "/dev/sda", "type": "disk", "size": "53687091200", "rota": "1", "tran": "sata"},
    {"name": "sdb", "path": "/dev/sdb", "type": "disk", "size": "53687091200", "rota": "0", "tran": "sata"}
  ]
}`

func newFake(stdout string) executor.Runner {
	r := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(stdout)})
	r.AddMatch(executor.FakeMatch{
		Path:       "lsblk",
		ArgsPrefix: []string{"-J", "-d", "-b"},
		Response:   executor.FakeResponse{Stdout: []byte(stdout)},
	})
	return r
}

func TestListDevicesFiltersDiskAndExcludesUSBLoopRam(t *testing.T) {
	devs, err := ListDevices(context.Background(), Options{
		Runner:    newFake(lsblkSampleModern),
		Names:     NamesKernel,
		ByPathDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	// Expect sda, sdb, nvme0n1. USB sdc, loop0, ram0 with size=0 excluded.
	if len(devs) != 3 {
		t.Fatalf("got %d devices, want 3: %+v", len(devs), devs)
	}
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		names = append(names, d.Name)
	}
	want := []string{"nvme0n1", "sda", "sdb"} // sorted by Path
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("device[%d] name = %s, want %s (full: %v)", i, names[i], n, names)
		}
	}
}

func TestListDevicesLegacyLsblkScalarTypes(t *testing.T) {
	devs, err := ListDevices(context.Background(), Options{
		Runner:    newFake(lsblkSampleLegacy),
		Names:     NamesKernel,
		ByPathDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2", len(devs))
	}
	if devs[0].Size != 53687091200 {
		t.Fatalf("size string parsing broken: %d", devs[0].Size)
	}
	// rota="1" must be true, rota="0" must be false.
	if devs[0].Rotational != true || devs[1].Rotational != false {
		t.Fatalf("rota string parsing broken: %v %v", devs[0].Rotational, devs[1].Rotational)
	}
}

func TestListDevicesSizeFence(t *testing.T) {
	devs, err := ListDevices(context.Background(), Options{
		Runner:    newFake(lsblkSampleModern),
		Names:     NamesKernel,
		ByPathDir: t.TempDir(),
		MinBytes:  64 * 1024 * 1024 * 1024, // 64 GiB; sda/sdb pass at 100 GiB
		MaxBytes:  500 * 1024 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	// nvme0n1 (1 TB) is over MaxBytes; only sda + sdb remain.
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2: %+v", len(devs), devs)
	}
}

func TestListDevicesByPathResolution(t *testing.T) {
	// Build a temp /dev/disk/by-path tree with symlinks to two of the
	// devices in the lsblk sample. nvme0n1 is intentionally absent to
	// exercise the fallback warning.
	tmp := t.TempDir()
	// Use real targets via /tmp files so EvalSymlinks resolves them.
	targetSDA := filepath.Join(t.TempDir(), "sda")
	if err := os.WriteFile(targetSDA, []byte{}, 0o600); err != nil { //nolint:gosec // test file in t.TempDir
		t.Fatalf("create target sda: %v", err)
	}
	targetSDB := filepath.Join(t.TempDir(), "sdb")
	if err := os.WriteFile(targetSDB, []byte{}, 0o600); err != nil { //nolint:gosec // test file in t.TempDir
		t.Fatalf("create target sdb: %v", err)
	}
	if err := os.Symlink(targetSDA, filepath.Join(tmp, "pci-0000:00:1f.2-ata-1.0")); err != nil {
		t.Fatalf("symlink sda: %v", err)
	}
	if err := os.Symlink(targetSDB, filepath.Join(tmp, "pci-0000:00:1f.2-ata-2.0")); err != nil {
		t.Fatalf("symlink sdb: %v", err)
	}
	// Also include a partition entry — should be ignored.
	if err := os.Symlink(targetSDA, filepath.Join(tmp, "pci-0000:00:1f.2-ata-1.0-part1")); err != nil {
		t.Fatalf("symlink part: %v", err)
	}

	// Tweak the lsblk sample so paths point at our temp targets.
	stdout := `{
		"blockdevices": [
			{"name": "sda", "path": "` + targetSDA + `", "type": "disk", "size": 107374182400, "rota": true, "tran": "sata"},
			{"name": "sdb", "path": "` + targetSDB + `", "type": "disk", "size": 107374182400, "rota": false, "tran": "sata"},
			{"name": "nvme0n1", "path": "/dev/nvme0n1", "type": "disk", "size": 1000204886016, "rota": false, "tran": "nvme"}
		]
	}`

	warns := []string{}
	devs, err := ListDevices(context.Background(), Options{
		Runner:    newFake(stdout),
		Names:     NamesByPath,
		ByPathDir: tmp,
		Warn:      func(format string, args ...any) { warns = append(warns, format) },
	})
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 3 {
		t.Fatalf("got %d devices, want 3", len(devs))
	}
	// nvme0n1 should have ByPathMissing=true and a warning emitted.
	var nvme *Device
	for i := range devs {
		if devs[i].Name == "nvme0n1" {
			nvme = &devs[i]
		}
	}
	if nvme == nil {
		t.Fatal("nvme0n1 missing from output")
		return
	}
	if !nvme.ByPathMissing {
		t.Fatal("nvme0n1 expected ByPathMissing=true")
	}
	if len(warns) != 1 {
		t.Fatalf("expected one warning, got %d: %v", len(warns), warns)
	}

	// sda/sdb should have non-bare paths.
	for _, d := range devs {
		if d.Name == "sda" || d.Name == "sdb" {
			if d.ByPathMissing {
				t.Fatalf("%s expected by-path resolution, got missing", d.Name)
			}
			if d.Path == d.KernelPath {
				t.Fatalf("%s path not resolved to by-path: %+v", d.Name, d)
			}
		}
	}
}

func TestTransportLabelFallback(t *testing.T) {
	cases := []struct{ name, tran, want string }{
		{"sda", "", "scsi"},
		{"sda", "sata", "sata"},
		{"nvme0n1", "", "nvme"},
		{"vda", "", "virtio"},
		{"vda", "virtio", "virtio"},
		{"mmcblk0", "", "-"}, // intentionally unmapped; covered by lsblk on real systems
		{"sda", "-", "scsi"},
	}
	for _, tc := range cases {
		got := TransportLabel(tc.name, tc.tran)
		if got != tc.want {
			t.Fatalf("TransportLabel(%q,%q)=%q, want %q", tc.name, tc.tran, got, tc.want)
		}
	}
}

func TestMediaLabel(t *testing.T) {
	if MediaLabel(true) != "spindle" {
		t.Fatal("rotational=true should be spindle")
	}
	if MediaLabel(false) != "flash" {
		t.Fatal("rotational=false should be flash")
	}
}
