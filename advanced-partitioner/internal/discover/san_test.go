package discover

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const lsblkSampleSAN = `{
  "blockdevices": [
    {"name": "sda", "type": "disk", "size": 480103981056, "rota": false, "tran": "sata", "serial": " S4EVNX0 ", "wwn": "0x5002538e00000001"},
    {"name": "sdb", "type": "disk", "size": 1099511627776, "rota": true, "tran": "fc", "serial": "FC1", "wwn": "0x600a0b80"},
    {"name": "sdc", "type": "disk", "size": 1099511627776, "rota": true, "tran": "iscsi", "serial": "IS1", "wwn": null},
    {"name": "sdd", "type": "disk", "size": 1099511627776, "rota": true, "tran": "sas", "serial": "MP1", "wwn": "0x600a0b81"},
    {"name": "sde", "type": "disk", "size": 1099511627776, "rota": true, "tran": "sas", "serial": "MP1", "wwn": "0x600a0b81"},
    {"name": "sdf", "type": "disk", "size": 1099511627776, "rota": true, "tran": "sas", "serial": "LV1", "wwn": null}
  ]
}`

func writeSys(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The discover pool (and so sanitize's scope) holds LOCAL disks only: FC and
// iSCSI LUNs and the paths of a multipath map are shared SAN storage. A disk
// held by an LVM mapping (dm uuid LVM-...) is still a local disk.
func TestListDevicesExcludesSANAndMultipathPaths(t *testing.T) {
	sys := t.TempDir()
	for _, d := range []string{"sdd", "sde"} {
		if err := os.MkdirAll(filepath.Join(sys, d, "holders", "dm-3"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSys(t, sys, "dm-3/dm/uuid", "mpath-3600a0b81\n")
	if err := os.MkdirAll(filepath.Join(sys, "sdf", "holders", "dm-0"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSys(t, sys, "dm-0/dm/uuid", "LVM-abcdef\n")

	devs, err := ListDevices(context.Background(), Options{
		Runner: newFake(lsblkSampleSAN), Names: NamesKernel, ByPathDir: t.TempDir(), SysBlockDir: sys,
	})
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	var names []string
	for _, d := range devs {
		names = append(names, d.Name)
	}
	if len(devs) != 2 || devs[0].Name != "sda" || devs[1].Name != "sdf" {
		t.Fatalf("pool = %v, want [sda sdf] (fc, iscsi and multipath paths excluded)", names)
	}
	if devs[0].Serial != "S4EVNX0" || devs[0].WWN != "0x5002538e00000001" {
		t.Fatalf("identity not captured: %+v", devs[0])
	}
	if devs[0].IdentityKey() != "wwn:0x5002538e00000001" || devs[1].IdentityKey() != "serial:LV1" {
		t.Fatalf("identity keys: %q %q", devs[0].IdentityKey(), devs[1].IdentityKey())
	}
}

// In kernel-name mode the by-path slot is still recorded as identity.
func TestListDevicesRecordsByPathInKernelMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sda")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	byPath := filepath.Join(dir, "by-path")
	if err := os.MkdirAll(byPath, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(byPath, "pci-0000:00:1f.2-ata-1")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	stdout := `{"blockdevices":[{"name":"sda","path":"` + target + `","type":"disk","size":1000,"rota":false,"tran":"sata"}]}`
	devs, err := ListDevices(context.Background(), Options{
		Runner: newFake(stdout), Names: NamesKernel, ByPathDir: byPath, SysBlockDir: t.TempDir(),
	})
	if err != nil || len(devs) != 1 {
		t.Fatalf("ListDevices: %v %+v", err, devs)
	}
	if devs[0].Path != target || devs[0].ByPath != link || devs[0].IdentityKey() != "path:"+link {
		t.Fatalf("kernel path with by-path identity expected, got %+v", devs[0])
	}
}
