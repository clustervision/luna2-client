package cleanup

// Barrier-pinning tests for the protected-disk fence (FTA BR-2, cut sets
// MCS-1 second barrier / MCS-7): cleanup must refuse — with a hard error and
// without running a single destructive command — whenever the work would
// touch a persistent set's disk. Do not weaken these to make a refactor
// pass; a failure here means the barrier is gone.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// destructivePaths are commands that mutate disks; used to assert a refusal
// ran none of them.
var destructivePaths = map[string]bool{
	"vgremove": true, "lvremove": true, "pvremove": true, "vgchange": true,
	"wipefs": true, "sgdisk": true, "dmsetup": true, "zpool": true,
}

func assertNoDestruction(t *testing.T, f *executor.FakeRunner) {
	t.Helper()
	for _, call := range f.Calls() {
		if call.Path == "mdadm" {
			for _, a := range call.Args {
				if a == "--stop" || a == "--zero-superblock" {
					t.Fatalf("destructive mdadm call after refusal: %v", call)
				}
			}
			continue
		}
		if destructivePaths[call.Path] {
			t.Fatalf("destructive call after refusal: %s %v", call.Path, call.Args)
		}
	}
}

func TestCleanupRefusesSelectedProtectedDisk(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	_, err := c.CleanupSelectedSet(context.Background(), Set{
		Name:          "os",
		SelectedDisks: []string{"/dev/sdb"},
		Protected:     SelectedDisksSet([]string{"/dev/sdb"}),
	})
	if err == nil {
		t.Fatal("cleanup must refuse a selected disk that is protected")
	}
	if !strings.Contains(err.Error(), "protected-disk barrier") {
		t.Fatalf("error should name the barrier: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("refusal must run ZERO commands, ran: %v", f.Calls())
	}
}

func TestTeardownVGRefusesVGOnProtectedDisk(t *testing.T) {
	// A VG named like the set's VG lives (partly) on a protected disk:
	// teardownVG must refuse before any vgchange/vgremove.
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("  /dev/sdb1 os\n")}})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	err := c.teardownVG(context.Background(), "os", SelectedDisksSet([]string{"/dev/sdb"}))
	if err == nil {
		t.Fatal("teardownVG must refuse a VG with a PV on a protected disk")
	}
	assertNoDestruction(t, f)
}

func TestTeardownSameDiskVGsRefusesSpanningVG(t *testing.T) {
	// VG "data" spans sda1 (selected) and sdb1 (protected/persistent):
	// removing it would destroy the persistent half → refuse, no vgremove.
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("  /dev/sda1 data\n  /dev/sdb1 data\n")}})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	err := c.teardownSameDiskVGs(context.Background(),
		SelectedDisksSet([]string{"/dev/sda"}),
		SelectedDisksSet([]string{"/dev/sdb"}))
	if err == nil {
		t.Fatal("spanning VG must refuse")
	}
	assertNoDestruction(t, f)
}

func TestTeardownSameDiskVGsRefusesSpanningMDVG(t *testing.T) {
	// VG "data" on an md PV whose members span selected sda and protected sdb.
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("  /dev/md0 data\n")}})
	md := &fakeMDDetail{bySource: map[string][]string{
		"/dev/md0": {"/dev/sda2", "/dev/sdb2"},
	}}
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep, MDAdmDetail: md.detail})
	err := c.teardownSameDiskVGs(context.Background(),
		SelectedDisksSet([]string{"/dev/sda"}),
		SelectedDisksSet([]string{"/dev/sdb"}))
	if err == nil {
		t.Fatal("md-backed spanning VG must refuse")
	}
	assertNoDestruction(t, f)
}

func TestStopMDArraysRefusesSpanningArray(t *testing.T) {
	// md0 has members on selected sda and protected sdb: stopping and
	// zeroing it would destroy the persistent mirror half → refuse.
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	body := "md0 : active raid1 sda1[0] sdb1[1]\n      102400 blocks super 1.2 [2/2] [UU]\n"
	if err := writeFileTest(mdstatPath, body); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	c := New(Options{Runner: f, MDStatPath: mdstatPath, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	err := c.stopMDArrays(context.Background(), "os",
		SelectedDisksSet([]string{"/dev/sda"}),
		SelectedDisksSet([]string{"/dev/sdb"}))
	if err == nil {
		t.Fatal("spanning md array must refuse")
	}
	assertNoDestruction(t, f)
}

func TestStopMDArraysRefusesDormantSpanningArray(t *testing.T) {
	// The 3.47 live gap: the spanning array is STOPPED (invisible in
	// /proc/mdstat) but its superblocks are on-disk -- one member on the
	// selected sda, its sibling on the protected sdb. The dormant barrier
	// must find the shared Array UUID via --examine and refuse before the
	// zero sweep destroys the selected member.
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	if err := writeFileTest(mdstatPath, "Personalities : [raid1]\nunused devices: <none>\n"); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	for _, d := range []string{"sda", "sdb"} {
		f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/" + d},
			Response: executor.FakeResponse{Stdout: []byte(d + " disk\n" + d + "1 part\n")}})
		f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/" + d + "1"},
			Response: executor.FakeResponse{Stdout: []byte("     Array UUID : aaaa:bbbb:cccc:dddd\n")}})
		f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/" + d},
			Response: executor.FakeResponse{ExitCode: 1, Err: errFail()}})
	}
	c := New(Options{Runner: f, MDStatPath: mdstatPath, CommandAvailable: alwaysAvailable, Sleep: noSleep, Stat: statNotExist})
	err := c.stopMDArrays(context.Background(), "os",
		SelectedDisksSet([]string{"/dev/sda"}),
		SelectedDisksSet([]string{"/dev/sdb"}))
	if err == nil {
		t.Fatal("dormant spanning md array must refuse")
	}
	if !strings.Contains(err.Error(), "dormant") {
		t.Fatalf("want the dormant-barrier error, got: %v", err)
	}
	assertNoDestruction(t, f)
}

func TestMatchesVGSourceExactness(t *testing.T) {
	cases := []struct {
		source, vg string
		want       bool
	}{
		{"/dev/os/root", "os", true},
		{"/dev/mapper/os-root", "os", true},
		{"/dev/mapper/bigdata-lv", "data", false}, // the substring bug
		{"/dev/bigdata/lv", "data", false},
		{"/dev/mapper/a--b-x", "a-b", true}, // vg "a-b" escapes to a--b
		{"/dev/mapper/a--b-x", "a", false},  // '--' = literal dash inside a name
		{"/dev/mapper/data-vol", "data", true},
		{"/dev/sda1", "os", false},
		{"/dev/mapper/os-root", "", false},
	}
	for _, c := range cases {
		if got := matchesVGSource(c.source, c.vg); got != c.want {
			t.Errorf("matchesVGSource(%q, %q) = %v, want %v", c.source, c.vg, got, c.want)
		}
	}
}
