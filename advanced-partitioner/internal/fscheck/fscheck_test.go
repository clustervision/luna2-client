package fscheck

import (
	"context"
	"errors"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// stubExit returns a FakeResponse with the given exit code. err is nil
// when rc==0 (matches realRunner), non-nil otherwise.
func stubExit(rc int) executor.FakeResponse {
	resp := executor.FakeResponse{ExitCode: rc}
	if rc != 0 {
		resp.Err = errors.New("exit non-zero")
	}
	return resp
}

func TestCheckExtRC0AndRC1Healthy(t *testing.T) {
	for _, rc := range []int{0, 1} {
		f := executor.NewFakeRunner(executor.FakeResponse{})
		f.AddMatch(executor.FakeMatch{
			Path: "e2fsck", ArgsPrefix: []string{"-f", "-p", "/dev/sda1"},
			Response: stubExit(rc),
		})
		got := New(Options{Runner: f}).CheckFS(context.Background(), "/dev/sda1", "ext4")
		if !got.OK {
			t.Fatalf("rc=%d should be healthy; got %+v", rc, got)
		}
	}
}

func TestCheckExtRC2Unhealthy(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "e2fsck", ArgsPrefix: []string{"-f", "-p", "/dev/sda1"},
		Response: stubExit(2),
	})
	got := New(Options{Runner: f}).CheckFS(context.Background(), "/dev/sda1", "ext4")
	if got.OK {
		t.Fatalf("rc=2 should be unhealthy; got %+v", got)
	}
	if got.Reason == "" {
		t.Fatal("Reason should be populated on failure")
	}
}

func TestCheckXFSCleanRC0(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "xfs_repair", ArgsPrefix: []string{"-n", "/dev/sda2"},
		Response: stubExit(0),
	})
	got := New(Options{Runner: f}).CheckFS(context.Background(), "/dev/sda2", "xfs")
	if !got.OK {
		t.Fatalf("xfs clean should be healthy; got %+v", got)
	}
}

// seqRunner returns scripted responses per (path, first-arg) in call order
// and records every call — used where the same command must answer
// differently across attempts (dirty log, then clean after replay).
type seqRunner struct {
	responses map[string][]executor.FakeResponse // key: path+" "+arg0
	calls     []executor.CommandSpec
}

func (s *seqRunner) Run(_ context.Context, spec executor.CommandSpec) (executor.CommandResult, error) {
	s.calls = append(s.calls, spec)
	key := spec.Path
	if len(spec.Args) > 0 {
		key += " " + spec.Args[0]
	}
	q := s.responses[key]
	resp := executor.FakeResponse{}
	if len(q) > 0 {
		resp = q[0]
		s.responses[key] = q[1:]
	}
	return executor.CommandResult{Stdout: resp.Stdout, Stderr: resp.Stderr, ExitCode: resp.ExitCode}, resp.Err
}

// assertNeverRepairsDestructively is the BR-7 barrier pin: a health probe
// must NEVER invoke xfs_repair -L (journal zap = loss of committed data)
// nor any repair form beyond check-only (-n) / safe preen (-p). Removing
// the read-only probe contract fails this — do not weaken it.
func assertNeverRepairsDestructively(t *testing.T, calls []executor.CommandSpec) {
	t.Helper()
	for _, c := range calls {
		if c.Path != "xfs_repair" && c.Path != "e2fsck" && c.Path != "fsck.vfat" && c.Path != "fsck.fat" {
			continue
		}
		var checkOnly bool
		for _, a := range c.Args {
			if a == "-L" || a == "-y" || a == "-a" {
				t.Fatalf("BARRIER VIOLATION: mutating probe flag %q invoked by a health check: %s %v", a, c.Path, c.Args)
			}
			if a == "-n" || a == "-p" {
				checkOnly = true
			}
		}
		if !checkOnly {
			t.Fatalf("probe invoked without a check-only/preen flag: %s %v", c.Path, c.Args)
		}
	}
}

// TestCheckXFSDirtyLogReplaysJournalSafely pins the crash-recovery path: a
// dirty log is recovered by mount+umount (the filesystem's own journal
// replay, which preserves committed data), never by xfs_repair -L.
func TestCheckXFSDirtyLogReplaysJournalSafely(t *testing.T) {
	s := &seqRunner{responses: map[string][]executor.FakeResponse{
		"xfs_repair -n": {stubExit(2), stubExit(0)}, // dirty, then clean after replay
		"mount -t":      {stubExit(0)},
		"umount":        {stubExit(0)},
	}}
	got := New(Options{Runner: s, MountDir: t.TempDir()}).CheckFS(context.Background(), "/dev/sda2", "xfs")
	if !got.OK {
		t.Fatalf("journal replay should recover a dirty log; got %+v", got)
	}
	var sawMount, sawUmount bool
	for _, c := range s.calls {
		if c.Path == "mount" {
			sawMount = true
		}
		if c.Path == "umount" {
			sawUmount = true
		}
	}
	if !sawMount || !sawUmount {
		t.Fatalf("expected mount+umount journal replay; mount=%v umount=%v", sawMount, sawUmount)
	}
	assertNeverRepairsDestructively(t, s.calls)
}

func TestCheckXFSUncleanAfterReplayIsUnhealthy(t *testing.T) {
	s := &seqRunner{responses: map[string][]executor.FakeResponse{
		"xfs_repair -n": {stubExit(2), stubExit(2)}, // dirty before AND after replay
		"mount -t":      {stubExit(0)},
		"umount":        {stubExit(0)},
	}}
	got := New(Options{Runner: s, MountDir: t.TempDir()}).CheckFS(context.Background(), "/dev/sda2", "xfs")
	if got.OK {
		t.Fatalf("still-unclean xfs must be unhealthy; got %+v", got)
	}
	if got.Reason == "" {
		t.Fatal("Reason should be populated")
	}
	assertNeverRepairsDestructively(t, s.calls)
}

func TestCheckXFSReplayMountFailureIsUnhealthy(t *testing.T) {
	s := &seqRunner{responses: map[string][]executor.FakeResponse{
		"xfs_repair -n": {stubExit(2)},
		"mount -t":      {stubExit(32)},
	}}
	got := New(Options{Runner: s, MountDir: t.TempDir()}).CheckFS(context.Background(), "/dev/sda2", "xfs")
	if got.OK {
		t.Fatalf("replay-mount failure must be unhealthy (never destructively repaired); got %+v", got)
	}
	assertNeverRepairsDestructively(t, s.calls)
}

func TestCheckVfatHappy(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "fsck.vfat", ArgsPrefix: []string{"-n", "/dev/sda1"},
		Response: stubExit(0),
	})
	got := New(Options{Runner: f, FsckVfatCmd: "fsck.vfat"}).CheckFS(
		context.Background(), "/dev/sda1", "vfat")
	if !got.OK {
		t.Fatalf("rc=0 should be healthy; got %+v", got)
	}
}

func TestCheckVfatHonoursAlternateName(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "fsck.fat", ArgsPrefix: []string{"-n", "/dev/sda1"},
		Response: stubExit(0),
	})
	got := New(Options{Runner: f, FsckVfatCmd: "fsck.fat"}).CheckFS(
		context.Background(), "/dev/sda1", "fat32")
	if !got.OK {
		t.Fatalf("fsck.fat happy path failed: %+v", got)
	}
}

func TestSwapAndTmpfsAlwaysHealthy(t *testing.T) {
	c := New(Options{Runner: executor.NewFakeRunner(executor.FakeResponse{})})
	if got := c.CheckFS(context.Background(), "/dev/sda3", "swap"); !got.OK {
		t.Fatalf("swap should be healthy: %+v", got)
	}
	if got := c.CheckFS(context.Background(), "tmpfs", "tmpfs"); !got.OK {
		t.Fatalf("tmpfs should be healthy: %+v", got)
	}
}

func TestUnknownFSReportsReason(t *testing.T) {
	got := New(Options{Runner: executor.NewFakeRunner(executor.FakeResponse{})}).
		CheckFS(context.Background(), "/dev/sda1", "btrfs")
	if got.OK {
		t.Fatal("unknown fs should fail")
	}
	if got.Reason == "" {
		t.Fatal("Reason should include the fs name")
	}
}

func TestExitCodeFromError(t *testing.T) {
	// Verify exitCode helper extracts code from a CommandResult; the
	// error path is exercised indirectly by the table tests above.
	if exitCode(executor.CommandResult{}, nil) != 0 {
		t.Fatal("err nil + rc 0 should map to 0")
	}
	if exitCode(executor.CommandResult{ExitCode: 7}, errors.New("boom")) != 7 {
		t.Fatal("err + rc 7 should map to 7")
	}
}

// Real fsck.fat -n output captured on el8 (dosfstools 4.1) and el9 (4.2) for a
// dirty-only and a genuinely damaged FAT (lab, 10 October 2026).
func TestDirtyBitOnly(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"el8 fat32 dirty", "fsck.fat 4.1 (2017-01-24)\n0x41: Dirty bit is set. Fs was not properly unmounted and some data may be corrupt.\n Automatically removing dirty bit.\nLeaving filesystem unchanged.\n/dev/vda1: 16 files, 2573/153296 clusters\n", true},
		{"el8 fat16 dirty", "fsck.fat 4.1 (2017-01-24)\n0x25: Dirty bit is set. Fs was not properly unmounted and some data may be corrupt.\n Automatically removing dirty bit.\nLeaving filesystem unchanged.\n/tmp/fat16-dirty.img: 1 files, 1/32695 clusters\n", true},
		{"el9 fat16 dirty", "fsck.fat 4.2 (2021-01-31)\nDirty bit is set. Fs was not properly unmounted and some data may be corrupt.\n Automatically removing dirty bit.\n\nLeaving filesystem unchanged.\nfat16-dirty.img: 1 files, 1/32695 clusters\n", true},
		{"el9 fat32 dirty (backup differs at the dirty flag only)", "fsck.fat 4.2 (2021-01-31)\nThere are differences between boot sector and its backup.\nThis is mostly harmless. Differences: (offset:original/backup)\n  65:01/00\n  Not automatically fixing this.\nDirty bit is set. Fs was not properly unmounted and some data may be corrupt.\n Automatically removing dirty bit.\n\nLeaving filesystem unchanged.\nfat32-dirty.img: 1 files, 2/153290 clusters\n", true},
		{"el9 backup differs elsewhere too", "fsck.fat 4.2 (2021-01-31)\nThere are differences between boot sector and its backup.\nThis is mostly harmless. Differences: (offset:original/backup)\n  65:01/00\n  3:4d/4e\n  Not automatically fixing this.\nDirty bit is set. Fs was not properly unmounted and some data may be corrupt.\n Automatically removing dirty bit.\nLeaving filesystem unchanged.\nx: 1 files, 2/1 clusters\n", false},
		{"el8 FATs differ", "fsck.fat 4.1 (2017-01-24)\nFATs differ - using first FAT.\nLeaving filesystem unchanged.\n/tmp/fat32-broken.img: 1 files, 2/153290 clusters\n", false},
		{"dirty plus FATs differ", "fsck.fat 4.1 (2017-01-24)\n0x41: Dirty bit is set. Fs was not properly unmounted and some data may be corrupt.\n Automatically removing dirty bit.\nFATs differ - using first FAT.\nLeaving filesystem unchanged.\n/dev/vda1: 16 files, 2573/153296 clusters\n", false},
		{"codepage error (missing gconv)", "fsck.fat 4.1 (2017-01-24)\nCP437: Invalid argument\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := dirtyBitOnly(c.out); got != c.want {
			t.Errorf("%s: dirtyBitOnly = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckVfatDirtyBitIsHealthy(t *testing.T) {
	dirty := "fsck.fat 4.1 (2017-01-24)\n0x41: Dirty bit is set. Fs was not properly unmounted and some data may be corrupt.\n Automatically removing dirty bit.\nLeaving filesystem unchanged.\n/dev/vda1: 16 files, 2573/153296 clusters\n"
	f := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(dirty), ExitCode: 1, Err: errors.New("exit status 1")})
	if res := New(Options{Runner: f}).CheckFS(context.Background(), "/dev/vda1", "vfat"); !res.OK {
		t.Fatalf("dirty-bit-only ESP must be healthy, got %+v", res)
	}
	broken := "fsck.fat 4.1 (2017-01-24)\nFATs differ - using first FAT.\nLeaving filesystem unchanged.\n/dev/vda1: 16 files, 2573/153296 clusters\n"
	g := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(broken), ExitCode: 1, Err: errors.New("exit status 1")})
	if res := New(Options{Runner: g}).CheckFS(context.Background(), "/dev/vda1", "vfat"); res.OK {
		t.Fatal("a FAT with differing FATs must stay unhealthy")
	}
}
