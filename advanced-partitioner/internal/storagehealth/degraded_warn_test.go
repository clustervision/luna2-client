package storagehealth

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fscheck"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

const mdDetailDegraded = `/dev/md/os-boot:
           Version : 1.0
        Raid Level : raid1
      Raid Devices : 2
     Total Devices : 1
             State : clean, degraded
    Active Devices : 1
   Working Devices : 1
`

const mdDetailClean = `/dev/md/os-boot:
      Raid Devices : 2
             State : clean
    Active Devices : 2
`

// activateWith runs ActivateExisting for a raid1 os set whose os-boot array
// already runs as detail says, and returns what it reported and logged.
func activateWith(t *testing.T, detail string, allowDegraded bool) (Activation, string) {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "lpart.log")
	logger, err := txlog.Init(txlog.Options{LogFile: logFile})
	if err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--detail", "/dev/md/os-boot"},
		Response: executor.FakeResponse{Stdout: []byte(detail)}})
	set := &v2.Set{Name: "os", Role: "os", Selection: "manual", Raid: "1", Devices: []string{"/dev/sda", "/dev/sdb"},
		AllowDegraded: allowDegraded,
		Volumes:       []v2.Volume{{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"}}}
	act := ActivateExisting(context.Background(), f, set, set.Devices, logger)
	_ = logger.Close()
	b, _ := os.ReadFile(logFile)
	return act, string(b)
}

// A mirror the installer's ramdisk already started on one member (the other
// lost its superblock, the disk is still there) is degraded like a missing
// member: without allow_degraded the set is refused, not used.
func TestARunningDegradedArrayIsDegradedWithoutAllowDegraded(t *testing.T) {
	act, log := activateWith(t, mdDetailDegraded, false)
	if !slices.Contains(act.Degraded, "os-boot") {
		t.Fatalf("running degraded array not reported degraded: %+v", act)
	}
	if strings.Contains(log, "used as it is") {
		t.Fatalf("a set without allow_degraded must not be used degraded:\n%s", log)
	}
}

// With allow_degraded the array is used as it is, loudly.
func TestARunningDegradedArrayIsUsedWithAllowDegraded(t *testing.T) {
	act, log := activateWith(t, mdDetailDegraded, true)
	if len(act.Degraded) != 0 || !strings.Contains(log, "array os-boot runs DEGRADED (1 of 2 members active") {
		t.Fatalf("allow_degraded: degraded=%v, log:\n%s", act.Degraded, log)
	}
	if act, log := activateWith(t, mdDetailClean, false); len(act.Degraded) != 0 || strings.Contains(log, "DEGRADED") {
		t.Fatalf("clean array reported degraded: %v\n%s", act.Degraded, log)
	}
}

// A degraded refusal leaves the disks as it found them: the probe takes down
// the volume group it activated, or the operator repairing the parked node
// finds the array held by it ("Cannot get exclusive access").
func TestDegradedProbeReleasesTheVGItActivated(t *testing.T) {
	withAllDevices(t)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--detail", "/dev/md/os-boot"},
		Response: executor.FakeResponse{Stdout: []byte(mdDetailDegraded)}})
	addVG(f, "  OSUUID os /dev/md127\n", map[string]string{"/dev/sda": "sda\nsda4\nmd127\n", "/dev/sdb": "sdb\nsdb4\nmd127\n"})
	set := &v2.Set{Name: "os", Role: "os", Selection: "manual", Raid: "1", Devices: []string{"/dev/sda", "/dev/sdb"},
		Volumes: []v2.Volume{{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"}}}
	checker := fscheck.New(fscheck.Options{Runner: f})

	fails := VerifyDiskSet(context.Background(), f, checker, set, resolved("/dev/sda", "/dev/sdb"), nil)
	if len(fails) != 1 || !fails[0].Degraded {
		t.Fatalf("want one degraded verdict, got %+v", fails)
	}
	var up, down string
	for _, c := range f.Calls() {
		if c.Path == "vgchange" && len(c.Args) == 3 {
			switch c.Args[0] {
			case "-ay":
				up = c.Args[2]
			case "-an":
				down = c.Args[2]
			}
		}
	}
	if up == "" || down != up {
		t.Fatalf("probe activated VG %q but deactivated %q", up, down)
	}
}
