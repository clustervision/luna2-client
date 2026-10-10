package vgselect

import (
	"context"
	"errors"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func noSymlinks(t *testing.T) {
	t.Helper()
	orig := EvalSymlinks
	EvalSymlinks = func(p string) (string, error) {
		if p == "/dev/md/os" {
			return "/dev/md127", nil
		}
		return "", errors.New("not a link")
	}
	t.Cleanup(func() { EvalSymlinks = orig })
}

// Two VGs named "os": ours on the set's raid1 (md127 on sda+sdb, PV printed
// through the /dev/md/os link) and a foreign one on an old OS disk (sdc3).
func TestLocateSplitsOwnFromForeign(t *testing.T) {
	noSymlinks(t)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		Response: executor.FakeResponse{Stdout: []byte(
			"  AAAA-1111 os   /dev/md/os\n  BBBB-2222 os   /dev/sdc3\n  CCCC-3333 data /dev/sdd1\n  /dev/sde1\n")}})
	for d, out := range map[string]string{
		"/dev/sda": "sda\nsda1\nsda2\nmd127\ndm-0\n",
		"/dev/sdb": "sdb\nsdb1\nsdb2\nmd127\ndm-0\n",
	} {
		f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", d},
			Response: executor.FakeResponse{Stdout: []byte(out)}})
	}
	loc, err := Locate(context.Background(), f, "os", []string{"/dev/sda", "/dev/sdb"})
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(loc.Own) != 1 || loc.Own[0].UUID != "AAAA-1111" {
		t.Fatalf("own = %+v, want the md-backed AAAA-1111", loc.Own)
	}
	if len(loc.Foreign) != 1 || loc.Foreign[0].UUID != "BBBB-2222" {
		t.Fatalf("foreign = %+v, want the sdc3 BBBB-2222", loc.Foreign)
	}
	if loc.Own[0].Select() != "vg_uuid=AAAA-1111" || loc.Own[0].DMUUIDPrefix() != "LVM-AAAA1111" {
		t.Fatalf("selectors wrong: %s %s", loc.Own[0].Select(), loc.Own[0].DMUUIDPrefix())
	}

	vgs, err := OnDisks(context.Background(), f, []string{"/dev/sda"})
	if err != nil || len(vgs) != 1 || vgs[0].Name != "os" {
		t.Fatalf("OnDisks(sda) = %+v, %v", vgs, err)
	}
}

func TestLocateNoSuchVGRunsNoLsblk(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	loc, err := Locate(context.Background(), f, "os", []string{"/dev/sda"})
	if err != nil || len(loc.Own)+len(loc.Foreign) != 0 {
		t.Fatalf("no VG -> empty result, got %+v %v", loc, err)
	}
	for _, c := range f.Calls() {
		if c.Path == "lsblk" {
			t.Fatal("no VG of the name: no disk probe needed")
		}
	}
}

func TestLocatePropagatesProbeErrors(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{Err: errors.New("pvs exploded")})
	if _, err := Locate(context.Background(), f, "os", []string{"/dev/sda"}); err == nil {
		t.Fatal("a pvs failure must be an error, not 'no VG'")
	}
}
