package main

// Tests for the plan-then-execute decision engine: every deterministic
// refusal (missing device, too-small disk, no os set, another set's
// signature, raid count) happens while planning, before ANY disk is touched,
// and maps onto the exit contract (2 safety refusal / 3 fatal config).

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	fsmod "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/storagehealth"
)

// testProcessor is a setProcessor whose every module runs on one FakeRunner.
// absent lists configured devices the fake resolver reports as not present.
func testProcessor(t *testing.T, absent ...string) (*setProcessor, *executor.FakeRunner) {
	t.Helper()
	f := executor.NewFakeRunner(executor.FakeResponse{})
	logger := silentLogger(t)
	gone := map[string]bool{}
	for _, a := range absent {
		gone[a] = true
	}
	tmp := t.TempDir()
	mdstat := tmp + "/mdstat"
	if err := os.WriteFile(mdstat, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sp := &setProcessor{
		runner: f,
		logger: logger,
		cleaner: cleanup.New(cleanup.Options{Runner: f, Logger: logger, CommandAvailable: func(string) bool { return true },
			Sleep: func(time.Duration) {}, MDStatPath: mdstat, DevDir: tmp,
			Stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }}),
		part:  partition.New(partition.Options{Runner: f, Logger: logger, DryRun: true}),
		raid:  raid.New(raid.Options{Runner: f, Logger: logger}),
		lvm:   lvm.New(lvm.Options{Runner: f, Logger: logger}),
		fs:    fsmod.New(fsmod.Options{Runner: f, Logger: logger}),
		mount: mount.New(mount.Options{Runner: f, Logger: logger, Sysroot: "/sysroot"}),
		resolveFn: func(_ context.Context, devs []string) ([]selector.Resolved, error) {
			out := make([]selector.Resolved, 0, len(devs))
			for _, d := range devs {
				if gone[d] {
					return nil, &selector.ResolveError{Kind: selector.KindNotFound, Device: d,
						Msg: "device_resolution: device not found: " + d}
				}
				out = append(out, selector.Resolved{ConfiguredPath: d, ResolvedPath: d})
			}
			return out, nil
		},
	}
	return sp, f
}

func withVerdict(sp *setProcessor, v storagehealth.Verdict) {
	sp.classifyFn = func(context.Context, *v2.Set, []selector.Resolved) storagehealth.Verdict { return v }
}

// destructive reports the first disk-mutating command the fake runner saw.
func destructive(f *executor.FakeRunner) string {
	for _, c := range f.Calls() {
		switch c.Path {
		case "sgdisk", "wipefs", "pvcreate", "vgcreate", "lvcreate", "vgremove", "lvremove", "pvremove",
			"mkfs.xfs", "mkfs.ext4", "mkfs.fat", "mkswap", "dmsetup", "blkdiscard":
			return c.Path + " " + strings.Join(c.Args, " ")
		case "mdadm":
			for _, a := range c.Args {
				if a == "--create" || a == "--zero-superblock" || a == "--stop" {
					return c.Path + " " + strings.Join(c.Args, " ")
				}
			}
		}
	}
	return ""
}

func wantFatal(t *testing.T, err error, reason string) {
	t.Helper()
	var fe *fatalConfigError
	if !errors.As(err, &fe) || fe.reason != reason {
		t.Fatalf("want fatal_config=%s (exit 3), got %T: %v", reason, err, err)
	}
}

func wantSafety(t *testing.T, err error, reason string) {
	t.Helper()
	var se *safetyError
	if !errors.As(err, &se) || se.reason != reason {
		t.Fatalf("want safety_refusal=%s (exit 2), got %T: %v", reason, err, err)
	}
}

func raidOSSet(raid string, devs ...string) v2.Set {
	return v2.Set{Name: "os", Role: "os", Selection: "manual", Raid: raid, Devices: devs,
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		}}
}

// --- item 5 (refined): a dead raid member ---------------------------------

// A raid1 os set with one dead member and allow_degraded, mounted (sync,
// healthy verdict): the node keeps booting on the surviving disk, degraded,
// with a loud warning.
func TestDegradedRaidMountContinuesWithPresentMember(t *testing.T) {
	sp, f := testProcessor(t, "/dev/vdb")
	withVerdict(sp, storagehealth.VerdictHealthy)
	osSet := raidOSSet("1", "/dev/vda", "/dev/vdb")
	osSet.AllowDegraded = true
	cfg := &v2.Config{Version: 2, Sets: []v2.Set{osSet}}
	if err := sp.reserveManualSets(context.Background(), cfg); err != nil {
		t.Fatalf("a raid1 set with one member present must be tolerated: %v", err)
	}
	plans, err := sp.planSets(context.Background(), installcontext.ModeSync, cfg, false, "uefi", false, true)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	p := plans[0]
	if p.act != actMount || len(p.disks) != 1 || p.disks[0].ResolvedPath != "/dev/vda" ||
		len(p.missing) != 1 || p.missing[0] != "/dev/vdb" {
		t.Fatalf("want a degraded mount on /dev/vda (missing /dev/vdb), got %+v", p)
	}
	if d := destructive(f); d != "" {
		t.Fatalf("planning touched a disk: %s", d)
	}
}

// Without allow_degraded the same dead member parks the node: a safety
// refusal (reason degraded) in every mode, before any disk is touched.
func TestDegradedRaidWithoutAllowParks(t *testing.T) {
	for _, mode := range []installcontext.Mode{installcontext.ModeSync, installcontext.ModeAuto, installcontext.ModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			sp, f := testProcessor(t, "/dev/vdb")
			withVerdict(sp, storagehealth.VerdictHealthy)
			cfg := &v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("1", "/dev/vda", "/dev/vdb")}}
			_, _, err := sp.runDecision(context.Background(), mode, cfg, "uefi", false, true)
			wantSafety(t, err, "degraded")
			if d := destructive(f); d != "" {
				t.Fatalf("a refused degraded set touched a disk: %s", d)
			}
		})
	}
}

// An array the probe found missing a member (disk present, member gone) parks
// the node like a missing disk does, before any disk is touched.
func TestDegradedVerdictParks(t *testing.T) {
	sp, f := testProcessor(t)
	withVerdict(sp, storagehealth.VerdictDegraded)
	cfg := &v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("1", "/dev/vda", "/dev/vdb")}}
	_, _, err := sp.runDecision(context.Background(), installcontext.ModeSync, cfg, "uefi", false, true)
	wantSafety(t, err, "degraded")
	if d := destructive(f); d != "" {
		t.Fatalf("a degraded set touched a disk: %s", d)
	}
}

// The degraded refusal advises allow_degraded only when the set lacks it.
func TestDegradedVerdictAdvisesOnlyWhatIsMissing(t *testing.T) {
	for _, allow := range []bool{false, true} {
		sp, _ := testProcessor(t)
		withVerdict(sp, storagehealth.VerdictDegraded)
		set := raidOSSet("1", "/dev/vda", "/dev/vdb")
		set.AllowDegraded = allow
		_, _, err := sp.runDecision(context.Background(), installcontext.ModeSync, &v2.Config{Version: 2, Sets: []v2.Set{set}}, "uefi", false, true)
		wantSafety(t, err, "degraded")
		if advised := strings.Contains(err.Error(), "set allow_degraded"); advised == allow {
			t.Errorf("allow_degraded=%v: advice to set it = %v: %v", allow, advised, err)
		}
	}
}

// With allow_degraded, the same dead member under a create (full, or auto on a
// broken os set) is still a fatal config error naming the device, before any
// disk is touched: a create needs every device.
func TestDegradedRaidCreateIsFatalBeforeTouchingDisks(t *testing.T) {
	for _, c := range []struct {
		mode    installcontext.Mode
		verdict storagehealth.Verdict
	}{
		{installcontext.ModeFull, ""},
		{installcontext.ModeAuto, storagehealth.VerdictBroken},
		{installcontext.ModeAuto, storagehealth.VerdictBlank},
		// sync on a degraded set that did not come up: name the missing
		// disk (exit 3), not a generic data-fence halt.
		{installcontext.ModeSync, storagehealth.VerdictBroken},
	} {
		t.Run(string(c.mode)+"/"+string(c.verdict), func(t *testing.T) {
			sp, f := testProcessor(t, "/dev/vdb")
			withVerdict(sp, c.verdict)
			osSet := raidOSSet("1", "/dev/vda", "/dev/vdb")
			osSet.AllowDegraded = true
			cfg := &v2.Config{Version: 2, Sets: []v2.Set{osSet}}
			_, _, err := sp.runDecision(context.Background(), c.mode, cfg, "uefi", false, true)
			wantFatal(t, err, "device-missing")
			if !strings.Contains(err.Error(), "/dev/vdb") {
				t.Fatalf("the error must name the missing device: %v", err)
			}
			if d := destructive(f); d != "" {
				t.Fatalf("a refused create touched a disk: %s", d)
			}
		})
	}
}

// raid none/0 cannot run without a member, and a raid5 missing two of four
// cannot run at all: fatal config while planning, before anything is touched,
// even when the set would only be mounted.
func TestMissingDeviceNonRedundantIsFatal(t *testing.T) {
	for _, c := range []struct {
		name   string
		set    v2.Set
		absent []string
	}{
		{"raid none", raidOSSet("none", "/dev/vda"), []string{"/dev/vda"}},
		{"raid0", raidOSSet("0", "/dev/vda", "/dev/vdb"), []string{"/dev/vdb"}},
		{"raid5 two of four gone", v2.Set{Name: "data", Role: "data", Selection: "manual", Raid: "5",
			Devices: []string{"/dev/vda", "/dev/vdb", "/dev/vdc", "/dev/vdd"},
			Volumes: []v2.Volume{{Name: "d", Mountpoint: "/data", FS: "xfs", Size: "100%", Provider: "lvm"}}},
			[]string{"/dev/vdc", "/dev/vdd"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sp, f := testProcessor(t, c.absent...)
			withVerdict(sp, storagehealth.VerdictHealthy)
			cfg := &v2.Config{Version: 2, Sets: []v2.Set{c.set}}
			if c.set.Role != "os" {
				cfg.Sets = append([]v2.Set{raidOSSet("none", "/dev/vdz")}, cfg.Sets...)
			}
			// sync on a healthy set = a mount: still fatal, the array cannot run.
			_, _, err := sp.runDecision(context.Background(), installcontext.ModeSync, cfg, "uefi", false, true)
			wantFatal(t, err, "device-missing")
			if !strings.Contains(err.Error(), c.absent[0]) {
				t.Fatalf("the error must name the missing device: %v", err)
			}
			if d := destructive(f); d != "" {
				t.Fatalf("touched a disk: %s", d)
			}
		})
	}
}

// A degraded PERSISTENT raid set is mounted on its present member, and that
// member stays protected.
func TestDegradedPersistentSetStaysProtected(t *testing.T) {
	sp, _ := testProcessor(t, "/dev/vdc")
	data := v2.Set{Name: "data", Role: "data", Selection: "manual", Raid: "1", Persistent: true, AllowDegraded: true,
		Devices: []string{"/dev/vdb", "/dev/vdc"},
		Volumes: []v2.Volume{{Name: "d", Mountpoint: "/data", FS: "xfs", Size: "100%", Provider: "lvm"}}}
	cfg := &v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("none", "/dev/vda"), data}}
	withVerdict(sp, storagehealth.VerdictHealthy)
	if err := sp.reserveManualSets(context.Background(), cfg); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, ok := sp.protected["/dev/vdb"]; !ok {
		t.Fatal("the present member of a degraded persistent set must stay protected")
	}
	plans, err := sp.planSets(context.Background(), installcontext.ModeFull, cfg, false, "uefi", false, true)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if p := plans[1]; p.act != actMount || len(p.missing) != 1 {
		t.Fatalf("persistent degraded set must be mounted degraded, got %+v", p)
	}
}

// --- item 8: no os set -----------------------------------------------------

func dataOnlyConfig() *v2.Config {
	return &v2.Config{Version: 2, Sets: []v2.Set{{
		Name: "data", Role: "data", Selection: "manual", Raid: "none", Devices: []string{"/dev/vdb"},
		Volumes: []v2.Volume{{Name: "d", Mountpoint: "/data", FS: "xfs", Size: "100%", Provider: "lvm"}},
	}}}
}

// A diskful mode with a data-only layout is fatal BEFORE any data set is
// touched (it used to create the data sets, then loop on an unmounted
// /sysroot in lpart-osimage-install).
func TestDiskfulModeWithoutOSSetIsFatal(t *testing.T) {
	for _, mode := range []installcontext.Mode{installcontext.ModeFull, installcontext.ModeSync, installcontext.ModeLocal} {
		sp, f := testProcessor(t)
		withVerdict(sp, storagehealth.VerdictBlank)
		_, _, err := sp.runDecision(context.Background(), mode, dataOnlyConfig(), "uefi", false, true)
		wantFatal(t, err, "no-os-set")
		if len(f.Calls()) != 0 {
			t.Fatalf("%s: no command may run, ran %v", mode, f.Calls())
		}
	}
}

// auto with a data-only layout boots the memboot RAM root plus the data sets,
// like auto with no layout at all.
func TestAutoDataOnlyLayoutIsMembootPlusDataSets(t *testing.T) {
	sp, _ := testProcessor(t)
	withVerdict(sp, storagehealth.VerdictHealthy)
	res, rec, err := sp.runDecision(context.Background(), installcontext.ModeAuto, dataOnlyConfig(), "uefi", true, false)
	if err != nil {
		t.Fatalf("auto + data-only: %v", err)
	}
	if len(res) != 2 || res[0].StorageType != "memory" || res[0].Role != "os" || res[1].Name != "data" {
		t.Fatalf("want [memboot tmpfs root, data], got %+v", res)
	}
	if len(rec) != 2 || rec[1].SelectedDisks[0].ResolvedPath != "/dev/vdb" {
		t.Fatalf("resolved records wrong: %+v", rec)
	}
}

// --- item 7: deterministic preflights --------------------------------------

// A disk too small for the layout is refused while planning -- before the
// cleanup wiped it -- and is a fatal config error.
func TestDiskTooSmallIsFatalBeforeCleanup(t *testing.T) {
	sp, f := testProcessor(t)
	sp.resolveFn = func(_ context.Context, devs []string) ([]selector.Resolved, error) {
		return []selector.Resolved{{ConfiguredPath: devs[0], ResolvedPath: devs[0], SizeBytes: 8 << 30}}, nil
	}
	set := raidOSSet("none", "/dev/vda")
	set.Volumes[1].Size = "20G"
	_, _, err := sp.runDecision(context.Background(), installcontext.ModeFull, &v2.Config{Version: 2, Sets: []v2.Set{set}}, "uefi", false, true)
	wantFatal(t, err, "disk-too-small")
	if d := destructive(f); d != "" {
		t.Fatalf("disk-too-small must refuse before cleanup; ran %s", d)
	}
}

// The raid member count (spares excluded) is checked while planning: fatal.
func TestRaidCountCountsSparesOut(t *testing.T) {
	set := raidOSSet("1", "/dev/vda", "/dev/vdb", "/dev/vdc")
	set.Spares = 1
	if err := validateRAIDCount(&set); err != nil {
		t.Fatalf("raid1 + 1 spare on 3 disks is valid: %v", err)
	}
	set.Spares = 0
	wantFatal(t, validateRAIDCount(&set), "raid-count")
}

// selection=discover that the pool cannot meet, or a bad match, is fatal --
// unless the pool itself could not be listed (that may clear: exit 1).
func TestDiscoverUnmetIsFatalUnlessPoolFailed(t *testing.T) {
	set := &v2.Set{Name: "os", Role: "os", Selection: "discover", Raid: "1", Match: &v2.Match{Tags: []string{"ssd"}},
		Volumes: []v2.Volume{{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"}}}
	sp := &setProcessor{logger: silentLogger(t), runner: executor.NewFakeRunner(executor.FakeResponse{}), pool: []discover.Device{{Path: "/dev/sda", KernelPath: "/dev/sda", Size: gib(100), Tags: []string{"ssd"}}}}
	wantFatal(t, sp.resolveDiscover(context.Background(), set), "discover-unmet")

	sp.poolErr = errors.New("lsblk: exit status 1")
	err := sp.resolveDiscover(context.Background(), set)
	var fe *fatalConfigError
	if err == nil || errors.As(err, &fe) {
		t.Fatalf("with a failed pool listing the shortfall is retryable, got %v", err)
	}

	bad := &v2.Set{Name: "os", Role: "os", Selection: "discover", Raid: "none", Match: &v2.Match{MinSize: "lots"},
		Volumes: set.Volumes}
	wantFatal(t, (&setProcessor{logger: silentLogger(t)}).resolveDiscover(context.Background(), bad), "match-invalid")
}

// Resolution failures keep the exit contract: absent / invalid device -> 3,
// mounted -> 2, a probe error -> retryable.
func TestClassifyResolveErr(t *testing.T) {
	wantFatal(t, classifyResolveErr("os", &selector.ResolveError{Kind: selector.KindNotFound, Device: "/dev/x", Msg: "gone"}), "device-missing")
	wantFatal(t, classifyResolveErr("os", &selector.ResolveError{Kind: selector.KindInvalid, Device: "/dev/x", Msg: "read-only"}), "device-invalid")
	wantSafety(t, classifyResolveErr("os", &selector.ResolveError{Kind: selector.KindInUse, Device: "/dev/x", Msg: "mounted"}), "device-in-use")
	err := classifyResolveErr("os", &selector.ResolveError{Kind: selector.KindProbe, Device: "/dev/x", Msg: "lsblk failed"})
	var fe *fatalConfigError
	var se *safetyError
	if errors.As(err, &fe) || errors.As(err, &se) {
		t.Fatalf("a probe failure must stay retryable: %v", err)
	}
}

// --- item 3b: discover picked another set's disks --------------------------

// auto, os set discovers disks that are not blank and carry the data set's VG
// (or md array): refuse instead of recreating -- that would wipe the data.
func TestDiscoverCreateRefusesAnotherSetsSignature(t *testing.T) {
	for _, c := range []struct {
		name       string
		persistent bool
		stub       func(f *executor.FakeRunner)
		reason     string
	}{
		{"vg", false, func(f *executor.FakeRunner) {
			f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
				Response: executor.FakeResponse{Stdout: []byte("  DATAUUID scratch /dev/sdb1\n")}})
			f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", "/dev/sdb"},
				Response: executor.FakeResponse{Stdout: []byte("sdb\nsdb1\n")}})
		}, "foreign-set-signature"},
		{"md", false, func(f *executor.FakeRunner) {
			f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/sdb"},
				Response: executor.FakeResponse{Stdout: []byte("sdb disk\nsdb1 part\n")}})
			f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/sdb1"},
				Response: executor.FakeResponse{Stdout: []byte("           Name : any:scratch  (local to host any)\n")}})
			f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
				Response: executor.FakeResponse{Err: errors.New("no md superblock")}})
		}, "foreign-set-signature"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sp, f := testProcessor(t)
			c.stub(f)
			withVerdict(sp, storagehealth.VerdictBroken)
			sp.pool = []discover.Device{{Name: "sdb", Path: "/dev/sdb", KernelPath: "/dev/sdb", Size: gib(100)}}
			cfg := &v2.Config{Version: 2, Sets: []v2.Set{
				{Name: "os", Role: "os", Selection: "discover", Raid: "none", Count: 1,
					Volumes: raidOSSet("none").Volumes},
				{Name: "scratch", Role: "data", Selection: "discover", Raid: "none", Count: 1,
					Volumes: []v2.Volume{{Name: "s", Mountpoint: "/scratch", FS: "xfs", Size: "100%", Provider: "lvm"}}},
			}}
			_, _, err := sp.runDecision(context.Background(), installcontext.ModeAuto, cfg, "uefi", false, true)
			wantSafety(t, err, c.reason)
			if d := destructive(f); d != "" {
				t.Fatalf("refusal must come before any wipe; ran %s", d)
			}
		})
	}
}

// The signature check only guards non-blank disks: a blank discover set is
// planned for create as before.
func TestDiscoverCreateOnBlankDisksIsPlanned(t *testing.T) {
	sp, _ := testProcessor(t)
	withVerdict(sp, storagehealth.VerdictBlank)
	sp.pool = []discover.Device{{Name: "sdb", Path: "/dev/sdb", KernelPath: "/dev/sdb", Size: gib(100)}}
	cfg := &v2.Config{Version: 2, Sets: []v2.Set{{Name: "os", Role: "os", Selection: "discover", Raid: "none", Count: 1,
		Volumes: raidOSSet("none").Volumes}}}
	plans, err := sp.planSets(context.Background(), installcontext.ModeAuto, cfg, false, "uefi", false, true)
	if err != nil || len(plans) != 1 || plans[0].act != actCreate {
		t.Fatalf("blank discover set must plan a create: %+v %v", plans, err)
	}
}

// A VG with the set's name on a disk outside every set this run recreates is
// refused while planning (it used to be vgremove'd by name).
func TestForeignSameNameVGRefusedBeforeCleanup(t *testing.T) {
	sp, f := testProcessor(t)
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		Response: executor.FakeResponse{Stdout: []byte("  OLDOS os /dev/sdz3\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", "/dev/vda"},
		Response: executor.FakeResponse{Stdout: []byte("vda\n")}})
	_, _, err := sp.runDecision(context.Background(), installcontext.ModeFull,
		&v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("none", "/dev/vda")}}, "uefi", false, true)
	wantSafety(t, err, "foreign-vg")
	if d := destructive(f); d != "" {
		t.Fatalf("foreign VG refusal must precede cleanup; ran %s", d)
	}
}

// A failed foreign-VG probe is retried (exit 1) before any set is cleaned,
// not skipped: skipping left the refusal to the builder, after the wipe.
func TestForeignVGProbeErrorRetriesBeforeCleanup(t *testing.T) {
	sp, f := testProcessor(t)
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		Response: executor.FakeResponse{Err: errors.New("exit status 5")}})
	_, _, err := sp.runDecision(context.Background(), installcontext.ModeFull,
		&v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("none", "/dev/vda")}}, "uefi", false, true)
	if err == nil {
		t.Fatal("a failed pvs probe must fail the run")
	}
	if code := exitForError(io.Discard, err); code != 1 {
		t.Fatalf("exit = %d, want 1 (retryable): %v", code, err)
	}
	if d := destructive(f); d != "" {
		t.Fatalf("probe failure must precede cleanup; ran %s", d)
	}
}

// A stale same-named VG on another create set's disks is judged by listing
// those disks; when that listing fails the run retries rather than refusing.
func TestStaleVGDiskProbeErrorRetriesBeforeCleanup(t *testing.T) {
	sp, f := testProcessor(t)
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
		Response: executor.FakeResponse{Stdout: []byte("  OLDOS os /dev/vdb1\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", "/dev/vda"},
		Response: executor.FakeResponse{Stdout: []byte("vda\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", "/dev/vdb"},
		Response: executor.FakeResponse{Err: errors.New("exit status 32")}})
	_, _, err := sp.runDecision(context.Background(), installcontext.ModeFull,
		&v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("none", "/dev/vda"), dataOnlyConfig().Sets[0]}}, "uefi", false, true)
	if err == nil {
		t.Fatal("a failed lsblk probe must fail the run")
	}
	if code := exitForError(io.Discard, err); code != 1 {
		t.Fatalf("exit = %d, want 1 (retryable): %v", code, err)
	}
	if d := destructive(f); d != "" {
		t.Fatalf("probe failure must precede cleanup; ran %s", d)
	}
}

// A VG reaching from the second create set's disk onto another disk refuses
// while planning: the first set's disks are not wiped first.
func TestSpanRefusalOnSecondSetBeforeFirstIsCleaned(t *testing.T) {
	sp, f := testProcessor(t)
	f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "pv_name,vg_name"},
		Response: executor.FakeResponse{Stdout: []byte("  /dev/vdb2 scratch\n  /dev/vdc2 scratch\n")}})
	_, _, err := sp.runDecision(context.Background(), installcontext.ModeFull,
		&v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("none", "/dev/vda"), dataOnlyConfig().Sets[0]}}, "uefi", false, true)
	wantSafety(t, err, "protected-disk")
	if d := destructive(f); d != "" {
		t.Fatalf("the span refusal must precede every cleanup; ran %s", d)
	}
}

// --- item 1: mount-existing records the boot partition numbers -------------

// A refreshed os set (no Layout built) must still report its ESP and BIOS
// boot partition numbers: lpart-bootloader-finalise refuses BIOS GRUB without
// bios_boot_partition, and registers NVRAM on the ESP number.
func TestMountExistingRecordsBootPartitionNumbers(t *testing.T) {
	const ef02 = "21686148-6449-6e6f-744e-656564454649"
	for _, c := range []struct {
		name               string
		efi02              bool
		wantESP, wantBIOS  int
		wantESPDevice      string
		firmware, bootload string
	}{
		{"laid out under BIOS", true, 2, 1, "/dev/vda2", "bios", "first"},
		{"BIOS layout, node now UEFI", true, 2, 1, "/dev/vda2", "uefi", "first"},
		{"UEFI layout", false, 1, 0, "/dev/vda1", "uefi", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			sp, f := testProcessor(t)
			if c.efi02 {
				f.AddMatch(executor.FakeMatch{Path: "blkid", ArgsPrefix: []string{"-p", "-s", "PART_ENTRY_TYPE", "-o", "value", "/dev/vda1"},
					Response: executor.FakeResponse{Stdout: []byte(ef02 + "\n")}})
			}
			set := raidOSSet("none", "/dev/vda")
			set.Bootloader = v2.Bootloader(c.bootload)
			set.Volumes = append(set.Volumes[:1], v2.Volume{Name: "boot", Mountpoint: "/boot", FS: "xfs", Size: "1G", Provider: "partition"}, set.Volumes[1])
			res, _, err := sp.process(context.Background(), plannedSet{set: &set, act: actMount,
				disks: []selector.Resolved{{ConfiguredPath: "/dev/vda", ResolvedPath: "/dev/vda"}}}, c.firmware, true)
			if err != nil {
				t.Fatalf("process: %v", err)
			}
			if res.ESPPartition != c.wantESP || res.BIOSBootPartition != c.wantBIOS {
				t.Fatalf("esp=%d bios=%d, want esp=%d bios=%d", res.ESPPartition, res.BIOSBootPartition, c.wantESP, c.wantBIOS)
			}
			for _, fs := range res.Filesystems {
				if fs.Mount == "/boot/efi" && fs.Device != c.wantESPDevice {
					t.Fatalf("ESP device %s, want %s", fs.Device, c.wantESPDevice)
				}
			}
		})
	}
}

// A degraded mount anchors raw partition paths on the first PRESENT disk.
func TestMountExistingDegradedUsesPresentDisk(t *testing.T) {
	sp, _ := testProcessor(t)
	set := v2.Set{Name: "data", Role: "data", Selection: "manual", Raid: "1", Devices: []string{"/dev/vda", "/dev/vdb"},
		Volumes: []v2.Volume{{Name: "d", Mountpoint: "/data", FS: "xfs", Size: "100%", Provider: "partition"}}}
	res, _, err := sp.process(context.Background(), plannedSet{set: &set, act: actMount, missing: []string{"/dev/vda"},
		disks: []selector.Resolved{{ConfiguredPath: "/dev/vdb", ResolvedPath: "/dev/vdb"}}}, "uefi", true)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(res.Filesystems) != 1 || res.Filesystems[0].Device != "/dev/vdb1" {
		t.Fatalf("want /data on the present disk /dev/vdb1, got %+v", res.Filesystems)
	}
}

// Under memboot the declared os set is ignored, so its absent device must not
// fail the run.
func TestMembootIgnoresMissingOSSetDevice(t *testing.T) {
	sp, _ := testProcessor(t, "/dev/vda")
	withVerdict(sp, storagehealth.VerdictHealthy)
	cfg := &v2.Config{Version: 2, Sets: []v2.Set{raidOSSet("none", "/dev/vda")}}
	res, _, err := sp.runDecision(context.Background(), installcontext.ModeMemboot, cfg, "uefi", true, false)
	if err != nil || len(res) != 1 || res[0].StorageType != "memory" {
		t.Fatalf("memboot must ignore the os set's devices: %+v %v", res, err)
	}
}

// selection=discover picks the disks that carry the set's own volume group or
// md array before the identity order: an upgrade that changed the order must
// not move an installed set to other disks.
func TestResolveDiscoverPrefersTheSetsOwnDisks(t *testing.T) {
	pool := func() []discover.Device {
		return []discover.Device{
			{Name: "sda", Path: "/dev/sda", KernelPath: "/dev/sda", Size: gib(100), WWN: "0xa"},
			{Name: "sdb", Path: "/dev/sdb", KernelPath: "/dev/sdb", Size: gib(100), WWN: "0xb"},
			{Name: "sdc", Path: "/dev/sdc", KernelPath: "/dev/sdc", Size: gib(100), WWN: "0xc"},
		}
	}
	for _, c := range []struct {
		name string
		set  v2.Set
		stub func(f *executor.FakeRunner)
		want []string
	}{
		{"vg", v2.Set{Name: "os", Role: "os", Selection: "discover", Raid: "none", Count: 1, Volumes: raidOSSet("none").Volumes},
			func(f *executor.FakeRunner) {
				f.AddMatch(executor.FakeMatch{Path: "pvs", ArgsPrefix: []string{"--noheadings", "-o", "vg_uuid,vg_name,pv_name"},
					Response: executor.FakeResponse{Stdout: []byte("  OSUUID os /dev/sdc3\n")}})
				f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME", "/dev/sdc"},
					Response: executor.FakeResponse{Stdout: []byte("sdc\nsdc3\n")}})
			}, []string{"/dev/sdc"}},
		{"md", v2.Set{Name: "scratch", Role: "data", Selection: "discover", Raid: "1", Count: 2,
			Volumes: []v2.Volume{{Name: "s", Mountpoint: "/scratch", FS: "xfs", Size: "100%", Provider: "lvm"}}},
			func(f *executor.FakeRunner) {
				for _, d := range []string{"sdb", "sdc"} {
					f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/" + d},
						Response: executor.FakeResponse{Stdout: []byte(d + " disk\n" + d + "1 part\n")}})
					f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/" + d + "1"},
						Response: executor.FakeResponse{Stdout: []byte("           Name : any:scratch  (local to host any)\n")}})
				}
				f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
					Response: executor.FakeResponse{Err: errors.New("no md superblock")}})
			}, []string{"/dev/sdb", "/dev/sdc"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sp, f := testProcessor(t)
			c.stub(f)
			sp.pool = pool()
			set := c.set
			if err := sp.resolveDiscover(context.Background(), &set); err != nil {
				t.Fatalf("resolveDiscover: %v", err)
			}
			if strings.Join(set.Devices, " ") != strings.Join(c.want, " ") {
				t.Fatalf("devices = %v, want %v (the set's own disks)", set.Devices, c.want)
			}
		})
	}
}

// FC/iSCSI LUNs are shared storage: a discover set takes one only with
// include_san, and sanitize never erases one.
func TestSANLunsOnlyWithIncludeSANAndNeverSanitized(t *testing.T) {
	pool := func() []discover.Device {
		return []discover.Device{
			{Name: "sda", Path: "/dev/sda", KernelPath: "/dev/sda", Size: gib(100), WWN: "0xb"},
			{Name: "sdb", Path: "/dev/sdb", KernelPath: "/dev/sdb", Size: gib(50), WWN: "0xa", Transport: "fc"},
		}
	}
	for _, include := range []bool{false, true} {
		sp, _ := testProcessor(t)
		sp.pool = pool()
		set := v2.Set{Name: "scratch", Role: "data", Selection: "discover", Raid: "none", Count: 1, IncludeSAN: include,
			Volumes: []v2.Volume{{Name: "s", Mountpoint: "/scratch", FS: "xfs", Size: "100%", Provider: "lvm"}}}
		if err := sp.resolveDiscover(context.Background(), &set); err != nil {
			t.Fatalf("include_san=%v: %v", include, err)
		}
		want := "/dev/sda"
		if include {
			want = "/dev/sdb" // smaller, so first once it is eligible
		}
		if len(set.Devices) != 1 || set.Devices[0] != want {
			t.Fatalf("include_san=%v: devices = %v, want [%s]", include, set.Devices, want)
		}
	}

	sp, f := testProcessor(t)
	sp.pool = pool()
	sp.sanitizeReadBack = func(string) (bool, error) { return true, nil }
	if _, _, err := sp.runSanitize(context.Background(), false); err != nil {
		t.Fatalf("runSanitize: %v", err)
	}
	for _, c := range f.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "/dev/sdb") && (c.Path == "blkdiscard" || c.Path == "wipefs" || c.Path == "sgdisk") {
			t.Fatalf("sanitize touched the SAN LUN: %s %v", c.Path, c.Args)
		}
	}
}
