package lvm

import (
	"context"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
)

func TestValidateRequiresLVMVolume(t *testing.T) {
	set := &v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
		{Name: "boot", Provider: "partition", Mountpoint: "/boot", FS: "xfs", Size: "1G"},
	}}
	if err := Validate(set); err == nil {
		t.Fatal("Validate should require an LVM volume")
	}
}

func TestValidateOSNeedsRoot(t *testing.T) {
	set := &v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
		{Name: "var", Provider: "lvm", Mountpoint: "/var", FS: "xfs", Size: "10G"},
	}}
	if err := Validate(set); err == nil {
		t.Fatal("OS set without root LV should fail")
	}
}

func TestValidateOnlyOne100Pct(t *testing.T) {
	set := &v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
		{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"},
		{Name: "data", Provider: "lvm", Mountpoint: "/data", FS: "xfs", Size: "100%"},
	}}
	if err := Validate(set); err == nil {
		t.Fatal("two 100% LVs should fail")
	}
}

func TestBuildHappyPathNoRAID(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		Name: "os", Role: "os", Raid: "none",
		Volumes: []v2.Volume{
			{Name: "swap", Provider: "lvm", Mountpoint: "swap", FS: "swap", Size: "1G"},
			{Name: "var", Provider: "lvm", Mountpoint: "/var", FS: "xfs", Size: "10G"},
			{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"},
		},
	}
	state, err := New(Options{Runner: f}).Build(context.Background(), set,
		partition.Layout{StoragePartitions: []string{"/dev/sda3"}},
		raid.State{RAIDMode: "none"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if state.VGName != "os" || state.PVDevice != "/dev/sda3" {
		t.Fatalf("state wrong: %+v", state)
	}
	// Verify order: defensive superblock clear (FTA D7 - a stale md 1.2
	// superblock inside the re-created partition would otherwise be
	// re-assembled by udev and hold the device busy), then pvcreate,
	// vgcreate, fixed-size LVs in JSON order, 100% LV last.
	want := []string{
		"mdadm --zero-superblock /dev/sda3",
		"wipefs -a /dev/sda3",
		"pvcreate -ff -y /dev/sda3",
		"vgs --noheadings -o vg_uuid,vg_name",
		"vgcreate os /dev/sda3",
		"lvcreate --noudevsync -y -Z n -W n -n swap -L 1G os",
		"vgmknodes os",
		"dmsetup mknodes",
		"lvcreate --noudevsync -y -Z n -W n -n var -L 10G os",
		"vgmknodes os",
		"dmsetup mknodes",
		"lvcreate --noudevsync -y -Z n -W n -n root -l 100%FREE os",
		"vgmknodes os",
		"dmsetup mknodes",
	}
	gotAll := make([]string, 0, len(f.Calls()))
	for _, c := range f.Calls() {
		gotAll = append(gotAll, c.Path+" "+strings.Join(c.Args, " "))
	}
	// Filter to the calls we actually care about (skip udevadm settles).
	got := []string{}
	for _, g := range gotAll {
		if !strings.HasPrefix(g, "udevadm ") {
			got = append(got, g)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("call count = %d, want %d:\n  %s", len(got), len(want), strings.Join(got, "\n  "))
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("[%d] got %q, want %q", i, got[i], w)
		}
	}
}

func TestBuildUsesMDDeviceWhenRAID(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		Name: "os", Role: "os", Raid: "1",
		Volumes: []v2.Volume{
			{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"},
		},
	}
	_, err := New(Options{Runner: f}).Build(context.Background(), set,
		partition.Layout{StoragePartitions: []string{"/dev/sda3", "/dev/sdb3"}},
		raid.State{RAIDMode: "raid1", MDDevice: "/dev/md/os"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	mustHaveCall(t, f, "pvcreate -ff -y /dev/md/os")
	mustHaveCall(t, f, "vgcreate os /dev/md/os")
}

func TestBuildUsesSetNameAsVGName(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	set := &v2.Set{
		// Under v1.4 the VG name is the set name (no provider.vg_name override).
		Name: "yourdata", Role: "data", Raid: "none",
		Volumes: []v2.Volume{
			{Name: "data", Provider: "lvm", Mountpoint: "/data", FS: "xfs", Size: "100%"},
		},
	}
	_, err := New(Options{Runner: f}).Build(context.Background(), set,
		partition.Layout{StoragePartitions: []string{"/dev/sda1"}},
		raid.State{RAIDMode: "none"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	mustHaveCall(t, f, "vgcreate yourdata /dev/sda1")
}

func TestBuildPreflightRequiresRunner(t *testing.T) {
	_, err := New(Options{}).Build(context.Background(), &v2.Set{Name: "x"},
		partition.Layout{}, raid.State{})
	if err == nil {
		t.Fatal("expected runner-required preflight")
	}
}

func TestBuildFailsWhenNoPV(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	_, err := New(Options{Runner: f}).Build(context.Background(),
		&v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
			{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"},
		}},
		partition.Layout{}, raid.State{RAIDMode: "none"})
	if err == nil || !strings.Contains(err.Error(), "no storage partitions") {
		t.Fatalf("expected 'no storage partitions' error; got %v", err)
	}
}

func mustHaveCall(t *testing.T, f *executor.FakeRunner, want string) {
	t.Helper()
	for _, c := range f.Calls() {
		if c.Path+" "+strings.Join(c.Args, " ") == want {
			return
		}
	}
	got := make([]string, 0, len(f.Calls()))
	for _, c := range f.Calls() {
		got = append(got, c.Path+" "+strings.Join(c.Args, " "))
	}
	t.Fatalf("expected %q in:\n  %s", want, strings.Join(got, "\n  "))
}

// NB-9 (FTA v2 BE-A6b): a stale same-name VG left by a prior different-topology
// install must be purged (by UUID) BEFORE vgcreate, or the second same-name VG
// makes lvcreate ambiguous -> exit 5. Here `vgs` reports a stale "os" (UUID
// STALEUUID01) plus an unrelated VG that must be left alone.
func TestBuildPurgesStaleSameNameVG(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "vgs",
		Response: executor.FakeResponse{
			Stdout: []byte("  STALEUUID01 os\n  OTHERUUID99 keepme\n"),
		},
	})
	set := &v2.Set{
		Name: "os", Role: "os", Raid: "none",
		Volumes: []v2.Volume{{Name: "root", Provider: "lvm", Mountpoint: "/", FS: "xfs", Size: "100%"}},
	}
	if _, err := New(Options{Runner: f}).Build(context.Background(), set,
		partition.Layout{StoragePartitions: []string{"/dev/sda3"}},
		raid.State{RAIDMode: "none"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The stale "os" is deactivated + removed by UUID, before vgcreate.
	mustHaveCall(t, f, "vgchange -an --select vg_uuid=STALEUUID01")
	mustHaveCall(t, f, "vgremove -fy --select vg_uuid=STALEUUID01")
	// The unrelated VG (different name) is NEVER touched.
	for _, c := range f.Calls() {
		joined := c.Path + " " + strings.Join(c.Args, " ")
		if strings.Contains(joined, "OTHERUUID99") {
			t.Fatalf("must not touch the unrelated VG: %s", joined)
		}
	}
	// The purge happens BEFORE vgcreate.
	var purgeIdx, createIdx = -1, -1
	for i, c := range f.Calls() {
		j := c.Path + " " + strings.Join(c.Args, " ")
		if strings.Contains(j, "vgremove -fy --select vg_uuid=STALEUUID01") {
			purgeIdx = i
		}
		if j == "vgcreate os /dev/sda3" {
			createIdx = i
		}
	}
	if purgeIdx < 0 || createIdx < 0 || purgeIdx > createIdx {
		t.Fatalf("purge must precede vgcreate: purge=%d create=%d", purgeIdx, createIdx)
	}
}
