package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// Phase 11 re-entrant scenarios. These tests prove that
// Cleaner.CleanupSelectedSet is idempotent: running it on a sysroot
// that has already been cleaned (or never had the VGs / md arrays in
// the first place) is a no-op, never an error.
//
// The bats predecessors (p08a/p08b, p09, p10, p11) drive the same
// behaviour over SSH against real disks. Here we exercise the
// machinery with a FakeRunner that returns "nothing is here" for
// every probe — that's exactly what a freshly-wiped system looks like.

func TestReentrantCleanupOnAlreadyCleanState(t *testing.T) {
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	// /proc/mdstat with no md devices listed.
	if err := os.WriteFile(mdstatPath, []byte("Personalities : [raid1]\nunused devices: <none>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	// findmnt: nothing under /sysroot or VG-owned.
	f.AddMatch(executor.FakeMatch{
		Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("")},
	})
	// dmsetup ls: no mapper devices.
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	// vgs <vg>: VG missing.
	f.AddMatch(executor.FakeMatch{Path: "vgs", ArgsPrefix: []string{"os"},
		Response: executor.FakeResponse{Err: errFail()}})
	// pvs: no PVs.
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	// lsblk for partition probe: disk only, no part rows.
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("sda disk\n")}})
	// lsblk -ln -o TYPE for the partition-wait: also no parts.
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})

	md := &fakeMDDetail{bySource: map[string][]string{}}
	c := New(Options{
		Runner:           f,
		Sysroot:          "/sysroot",
		MDStatPath:       mdstatPath,
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
		MDAdmDetail:      md.detail,
		Stat:             statNotExist,
	})
	res, err := c.CleanupSelectedSet(context.Background(), Set{
		Name:          "os",
		SelectedDisks: []string{"/dev/sda"},
		OwnsSysroot:   true,
	})
	if err != nil {
		t.Fatalf("idempotent cleanup failed: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want ok", res.Status)
	}

	// Critical: no vgremove / lvremove / mdadm --stop on the already-clean
	// system. wipefs / sgdisk runs unconditionally for safety (matches
	// bash), so don't assert against those.
	for _, c := range f.Calls() {
		switch c.Path {
		case "vgremove", "lvremove":
			t.Fatalf("unexpected %s call on already-clean state: %v", c.Path, c.Args)
		case "mdadm":
			if len(c.Args) >= 1 && c.Args[0] == "--stop" {
				t.Fatalf("unexpected mdadm --stop on already-clean state: %v", c.Args)
			}
		}
	}
}

func TestReentrantCleanupAfterPriorPreparePass(t *testing.T) {
	// Simulate a "second pass": disks already have partitions + a VG +
	// an md array from a previous lpart-storage-prepare run. The cleanup
	// must tear all of this down cleanly without error.
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	body := "Personalities : [raid1]\nmd0 : active raid1 sda3[0] sdb3[1]\n      102400 blocks super 1.2 [2/2] [UU]\n"
	if err := os.WriteFile(mdstatPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	// the array is lpart's own (homehost "any") from the previous install
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/sda3"},
		Response: executor.FakeResponse{Stdout: []byte("  Name : any:os\n")}})
	// findmnt -rn -o TARGET,SOURCE: /sysroot is mounted from the prior pass.
	f.AddMatch(executor.FakeMatch{
		Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/mapper/os-root xfs rw\n")},
	})
	// findmnt -rn -M for the unmount probe: target is mounted.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/mapper/os-root xfs\n")}})
	// umount succeeds.
	f.AddMatch(executor.FakeMatch{Path: "umount", ArgsPrefix: []string{"/sysroot"},
		Response: executor.FakeResponse{}})

	// dmsetup ls: prior VG exposed as mapper.
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("os-root\t(253:0)\n")}})
	// findmnt -S for the mapper path: no extra mount.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/mapper/os-root", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	// vgs <vg>: VG exists.
	f.AddMatch(executor.FakeMatch{Path: "vgs", ArgsPrefix: []string{"os"},
		Response: executor.FakeResponse{}})
	// lvs: returns the LV path.
	f.AddMatch(executor.FakeMatch{Path: "lvs", ArgsPrefix: []string{"--noheadings", "-o", "lv_path", "os"},
		Response: executor.FakeResponse{Stdout: []byte("  /dev/os/root\n")}})
	// pvs: PV on sda3 belongs to os.
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("  /dev/sda3 os\n")}})
	// lsblk for partitions:
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("sda  disk\nsda1  part\nsda3  part\n")}})
	// lsblk wait: post-zap shows no parts.
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})

	md := &fakeMDDetail{bySource: map[string][]string{
		"/dev/md0": {"/dev/sda3", "/dev/sdb3"},
	}}
	c := New(Options{
		Runner:           f,
		Sysroot:          "/sysroot",
		MDStatPath:       mdstatPath,
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
		MDAdmDetail:      md.detail,
		Stat:             statNotExist,
	})
	res, err := c.CleanupSelectedSet(context.Background(), Set{
		Name:          "os",
		SelectedDisks: []string{"/dev/sda"},
		OwnsSysroot:   true,
	})
	if err != nil {
		t.Fatalf("CleanupSelectedSet: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q", res.Status)
	}
	// Verify the cascade hit every checkpoint expected for a 2nd-pass
	// teardown: umount sysroot, vgremove, mdadm --stop, wipefs.
	expectedAnywhere := []string{
		"umount /sysroot",
		"vgremove --noudevsync -fy os",
		"mdadm --stop /dev/md0",
		"wipefs -af /dev/sda",
		"sgdisk --zap-all /dev/sda",
	}
	got := []string{}
	for _, c := range f.Calls() {
		got = append(got, c.Path+" "+strings.Join(c.Args, " "))
	}
	for _, w := range expectedAnywhere {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q in:\n  %s", w, strings.Join(got, "\n  "))
		}
	}
}

func TestMultiSetCleanupSkipsSysrootWhenNotOwned(t *testing.T) {
	// Regression: in the data-raid5 scenario (memory OS + RAID5 data),
	// the OS set mounts tmpfs at /sysroot first. When the data set's
	// per-set cleanup runs next, it must NOT unmount /sysroot — that
	// mount belongs to a different set. Before the fix, the cleanup's
	// findmnt scan blindly included /sysroot under "things under
	// sysroot" and umount'd it, leaving the OS-set's tmpfs root gone.
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	if err := os.WriteFile(mdstatPath, []byte("Personalities : []\nunused devices: <none>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	// /sysroot is tmpfs from the OS set. No other mounts.
	f.AddMatch(executor.FakeMatch{
		Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot tmpfs\n")},
	})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "vgs", ArgsPrefix: []string{"data"},
		Response: executor.FakeResponse{Err: errFail()}})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sdc"},
		Response: executor.FakeResponse{Stdout: []byte("sdc disk\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sdc"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})

	c := New(Options{
		Runner: f, Sysroot: "/sysroot",
		MDStatPath:       mdstatPath,
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
		MDAdmDetail:      (&fakeMDDetail{}).detail,
	})

	// Data set does NOT own /sysroot.
	if _, err := c.CleanupSelectedSet(context.Background(), Set{
		Name:          "data",
		SelectedDisks: []string{"/dev/sdc"},
		OwnsSysroot:   false,
	}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, call := range f.Calls() {
		if call.Path == "umount" && len(call.Args) >= 1 && call.Args[len(call.Args)-1] == "/sysroot" {
			t.Fatalf("data set's cleanup must not umount /sysroot, but did: %v", call.Args)
		}
	}
}

func TestSingleSetCleanupUnmountsSysrootWhenOwned(t *testing.T) {
	// Sanity-check that OwnsSysroot=true still unmounts /sysroot on a
	// re-entrant OS-set pass. Without this, the fix would have
	// inadvertently broken single-disk-direct / raid1-os re-entrancy.
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	if err := os.WriteFile(mdstatPath, []byte("Personalities : []\nunused devices: <none>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/sda4\n")},
	})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot /dev/sda4 xfs\n")}})
	f.AddMatch(executor.FakeMatch{Path: "umount", ArgsPrefix: []string{"/sysroot"},
		Response: executor.FakeResponse{}})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "vgs", ArgsPrefix: []string{"os"},
		Response: executor.FakeResponse{Err: errFail()}})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("sda disk\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sda"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})

	c := New(Options{
		Runner: f, Sysroot: "/sysroot",
		MDStatPath:       mdstatPath,
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
		MDAdmDetail:      (&fakeMDDetail{}).detail,
	})
	if _, err := c.CleanupSelectedSet(context.Background(), Set{
		Name:          "os",
		SelectedDisks: []string{"/dev/sda"},
		OwnsSysroot:   true,
	}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	sawUmountSysroot := false
	for _, call := range f.Calls() {
		if call.Path == "umount" && len(call.Args) >= 1 && call.Args[len(call.Args)-1] == "/sysroot" {
			sawUmountSysroot = true
			break
		}
	}
	if !sawUmountSysroot {
		t.Fatal("OS set with OwnsSysroot=true must unmount /sysroot on re-entrant pass")
	}
}

func TestReentrantCleanupTwiceInARowSameInput(t *testing.T) {
	// Quick "did running it twice corrupt anything?" smoke test. We use
	// the same Cleaner instance + FakeRunner across two CleanupSelectedSet
	// calls and verify the second one succeeds. FakeRunner accumulates
	// calls; both passes should run the same cascade against the same
	// canned responses.
	tmp := t.TempDir()
	mdstatPath := filepath.Join(tmp, "mdstat")
	if err := os.WriteFile(mdstatPath, []byte("Personalities : []\nunused devices: <none>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET,SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "vgs", ArgsPrefix: []string{"data"},
		Response: executor.FakeResponse{Err: errFail()}})
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sdc"},
		Response: executor.FakeResponse{Stdout: []byte("sdc disk\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "TYPE", "/dev/sdc"},
		Response: executor.FakeResponse{Stdout: []byte("disk\n")}})

	c := New(Options{
		Runner:           f,
		Sysroot:          "/sysroot",
		MDStatPath:       mdstatPath,
		CommandAvailable: alwaysAvailable,
		Sleep:            noSleep,
		MDAdmDetail:      (&fakeMDDetail{}).detail,
	})
	for i := 0; i < 2; i++ {
		if _, err := c.CleanupSelectedSet(context.Background(), Set{
			Name:          "data",
			SelectedDisks: []string{"/dev/sdc"},
		}); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}
}
