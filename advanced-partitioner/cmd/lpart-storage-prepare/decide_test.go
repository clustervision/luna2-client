package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/storagehealth"
)

func silentLogger(t *testing.T) *txlog.Logger {
	t.Helper()
	l, err := txlog.Init(txlog.Options{LogFile: "", Stderr: false})
	if err != nil {
		t.Fatalf("Init logger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// TestDecideActionDataFence pins the full mode x verdict x role matrix — the
// data fence (spec section 5). These are the paths we changed: sync never
// wipes (halts on broken), auto recreates only a broken OS set, full always
// wipes, local/persistent only mount.
func TestDecideActionDataFence(t *testing.T) {
	H, B, K := storagehealth.VerdictHealthy, storagehealth.VerdictBlank, storagehealth.VerdictBroken
	U := storagehealth.VerdictUnknown
	cases := []struct {
		name       string
		mode       installcontext.Mode
		role       string
		persistent bool
		verdict    storagehealth.Verdict
		wantAct    setAction
		wantHalt   bool
	}{
		// persistent: hands-off in every mode.
		{"persistent/full", installcontext.ModeFull, "data", true, B, actMount, false},
		{"persistent/sync-broken", installcontext.ModeSync, "data", true, K, actMount, false},
		{"persistent/auto-broken", installcontext.ModeAuto, "data", true, K, actMount, false},
		// full: always wipe+create, verdict irrelevant.
		{"full/blank", installcontext.ModeFull, "os", false, B, actCreate, false},
		{"full/healthy", installcontext.ModeFull, "data", false, H, actCreate, false},
		{"full/broken", installcontext.ModeFull, "data", false, K, actCreate, false},
		// local: mount existing.
		{"local", installcontext.ModeLocal, "os", false, H, actMount, false},
		// sync: create-on-blank, refresh-on-healthy, HALT-on-broken (never wipe).
		{"sync/blank", installcontext.ModeSync, "os", false, B, actCreate, false},
		{"sync/healthy", installcontext.ModeSync, "os", false, H, actMount, false},
		{"sync/broken-os", installcontext.ModeSync, "os", false, K, actMount, true},
		{"sync/broken-data", installcontext.ModeSync, "data", false, K, actMount, true},
		// auto: like sync, but recreate a broken OS set (no user data); data halts.
		{"auto/blank", installcontext.ModeAuto, "os", false, B, actCreate, false},
		{"auto/healthy", installcontext.ModeAuto, "data", false, H, actMount, false},
		{"auto/broken-os", installcontext.ModeAuto, "os", false, K, actCreate, false},
		{"auto/broken-data", installcontext.ModeAuto, "data", false, K, actMount, true},
		// unknown (a checker did not run): never mount, never wipe -- not
		// even the os set under auto, which recreates a BROKEN os set.
		{"auto/unknown-os", installcontext.ModeAuto, "os", false, U, actMount, true},
		{"auto/unknown-data", installcontext.ModeAuto, "data", false, U, actMount, true},
		{"sync/unknown-os", installcontext.ModeSync, "os", false, U, actMount, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			act, halt := decideAction(c.mode, c.role, c.persistent, c.verdict)
			if halt != c.wantHalt {
				t.Fatalf("halt=%v want %v", halt, c.wantHalt)
			}
			if !halt && act != c.wantAct {
				t.Fatalf("act=%v want %v", act, c.wantAct)
			}
		})
	}
}

func TestRunDecisionLegacyErrors(t *testing.T) {
	sp := &setProcessor{logger: silentLogger(t)}
	if _, _, err := sp.runDecision(context.Background(), installcontext.ModeLegacy, nil, "uefi", true, false); err == nil {
		t.Fatal("legacy must error on the client")
	}
}

func TestRunDecisionSyncRequiresConfig(t *testing.T) {
	sp := &setProcessor{logger: silentLogger(t)}
	if _, _, err := sp.runDecision(context.Background(), installcontext.ModeSync, nil, "uefi", true, false); err == nil {
		t.Fatal("sync with no disklayout must error")
	}
	if _, _, err := sp.runDecision(context.Background(), installcontext.ModeFull, &v2.Config{}, "uefi", true, false); err == nil {
		t.Fatal("full with empty disklayout must error")
	}
}

func TestRunSanitizeNoDisks(t *testing.T) {
	sp := &setProcessor{logger: silentLogger(t)}
	res, _, err := sp.runDecision(context.Background(), installcontext.ModeSanitize, nil, "uefi", true, false)
	if err != nil {
		t.Fatalf("sanitize with no disks should be a no-op, got %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("sanitize produces no storage results, got %d", len(res))
	}
}

// Sanitize must release every storage stack on the target disks BEFORE
// erasing them: without the teardown, live LVM/md mappings survive the erase
// and — on the advisory-discard fallback (wipe+zap) — keep the tenant
// filesystem fully reachable through the surviving /dev/mapper node
// (measured live: battery 3.0 S26, an intact xfs behind os-root after a
// "successful" sanitize). Pins that the cleanup cascade runs, and runs first.
func TestRunSanitizeTearsDownStacksBeforeErase(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	tmp := t.TempDir()
	mdstat := filepath.Join(tmp, "mdstat")
	if err := os.WriteFile(mdstat, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	cl := cleanup.New(cleanup.Options{
		Runner:           f,
		CommandAvailable: func(string) bool { return true },
		Sleep:            func(time.Duration) {},
		MDStatPath:       mdstat,
		Stat:             func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		DevDir:           tmp,
	})
	sp := &setProcessor{
		logger:  silentLogger(t),
		runner:  f,
		cleaner: cl,
		pool: []discover.Device{
			{Path: "/dev/vda", KernelPath: "/dev/vda", Size: gib(20)},
		},
		sanitizeReadBack: func(string) (bool, error) { return true, nil },
	}
	if _, _, err := sp.runSanitize(context.Background(), false); err != nil {
		t.Fatalf("runSanitize: %v", err)
	}
	teardownIdx, eraseIdx := -1, -1
	for i, c := range f.Calls() {
		if teardownIdx == -1 && (c.Path == "findmnt" || c.Path == "vgs" || c.Path == "dmsetup") {
			teardownIdx = i
		}
		if eraseIdx == -1 && c.Path == "blkdiscard" {
			eraseIdx = i
		}
	}
	if teardownIdx == -1 {
		t.Fatal("no stack-teardown (cleanup cascade) commands ran")
	}
	if eraseIdx == -1 {
		t.Fatal("no blkdiscard ran")
	}
	if teardownIdx > eraseIdx {
		t.Fatalf("teardown (call %d) ran AFTER the erase (call %d)", teardownIdx, eraseIdx)
	}
}

func gib(n int64) int64 { return n * 1024 * 1024 * 1024 }

func TestResolveDiscoverSelectsByTagsAndConsumesPool(t *testing.T) {
	sp := &setProcessor{
		logger: silentLogger(t),
		pool: []discover.Device{
			{Path: "/dev/sda", KernelPath: "/dev/sda", Size: gib(8000), Tags: []string{"rotary"}},
			{Path: "/dev/nvme0n1", KernelPath: "/dev/nvme0n1", Size: gib(2000), Tags: []string{"ssd", "nvme"}},
			{Path: "/dev/nvme1n1", KernelPath: "/dev/nvme1n1", Size: gib(2000), Tags: []string{"ssd", "nvme"}},
		},
	}
	set := &v2.Set{
		Name: "scratch", Role: "data", Selection: "discover", Raid: "0",
		Match: &v2.Match{Tags: []string{"nvme"}},
		Volumes: []v2.Volume{
			{Name: "scratch", Mountpoint: "/scratch", FS: "xfs", Provider: "lvm", Size: "100%"},
		},
	}
	if err := sp.resolveDiscover(set); err != nil {
		t.Fatalf("resolveDiscover: %v", err)
	}
	if len(set.Devices) != 2 {
		t.Fatalf("raid0 count=2 should select 2 nvme disks, got %v", set.Devices)
	}
	for _, d := range set.Devices {
		if d == "/dev/sda" {
			t.Fatalf("rotary disk must not be selected by an nvme match: %v", set.Devices)
		}
	}
	// The chosen disks are consumed from the pool (only sda remains).
	if len(sp.pool) != 1 || sp.pool[0].Path != "/dev/sda" {
		t.Fatalf("pool should retain only the unselected rotary disk, got %+v", sp.pool)
	}
}

func TestResolveDiscoverHaltsWhenUnmet(t *testing.T) {
	sp := &setProcessor{
		logger: silentLogger(t),
		pool:   []discover.Device{{Path: "/dev/sda", KernelPath: "/dev/sda", Size: gib(500), Tags: []string{"ssd"}}},
	}
	set := &v2.Set{
		Name: "os", Role: "os", Selection: "discover", Raid: "1", // needs 2
		Match:   &v2.Match{Tags: []string{"ssd"}},
		Volumes: []v2.Volume{{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"}},
	}
	if err := sp.resolveDiscover(set); err == nil {
		t.Fatal("raid1 with one eligible disk must halt (not enough disks)")
	}
}
