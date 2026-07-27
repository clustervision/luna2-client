package main

// Barrier-pinning tests for the block-level protection chain (FTA BR-1/BR-8,
// cut sets MCS-1/8/12): manual/persistent disks are reserved out of the
// discover pool BEFORE any set is processed; cross-set aliases refuse;
// sanitize refuses when a persistent set is declared. Removing a barrier
// fails these tests — do not weaken them to make a refactor pass.

import (
	"context"
	"errors"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
)

// identityResolve maps configured paths to canonical ones via a table
// (default: identity), standing in for selector.ResolveAll.
func identityResolve(aliases map[string]string) func(context.Context, []string) ([]selector.Resolved, error) {
	return func(_ context.Context, devs []string) ([]selector.Resolved, error) {
		out := make([]selector.Resolved, 0, len(devs))
		for _, d := range devs {
			r := d
			if a, ok := aliases[d]; ok {
				r = a
			}
			out = append(out, selector.Resolved{ConfiguredPath: d, ResolvedPath: r})
		}
		return out, nil
	}
}

// twoSetConfig is the MCS-1 shape: an OS set that discovers one disk, and a
// persistent manual data set pinned to /dev/sdb.
func twoSetConfig() *v2.Config {
	return &v2.Config{
		Version: v2.SchemaVersion,
		Sets: []v2.Set{
			{
				Name: "os", Role: "os", Selection: "discover", Raid: "none", Count: 1,
				Volumes: []v2.Volume{
					{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"},
					{Name: "efi", Mountpoint: "/boot/efi", FS: "vfat", Provider: "partition", Size: "512M"},
				},
			},
			{
				Name: "data", Role: "data", Selection: "manual", Raid: "none",
				Devices: []string{"/dev/sdb"}, Persistent: true,
				Volumes: []v2.Volume{
					{Name: "scratch", Mountpoint: "/data", FS: "xfs", Provider: "lvm", Size: "100%"},
				},
			},
		},
	}
}

// TestReservationKeepsDiscoverOffPersistentDisk pins the MCS-1 fix end to
// end at the decision layer: with the persistent disk being the SMALLEST in
// the pool (the exact shape that destroyed it before), discover must select
// the other disk, and the persistent disk must be recorded as protected.
func TestReservationKeepsDiscoverOffPersistentDisk(t *testing.T) {
	sp := &setProcessor{
		logger: silentLogger(t),
		pool: []discover.Device{
			{Name: "sda", Path: "/dev/sda", KernelPath: "/dev/sda", Size: gib(20)},
			{Name: "sdb", Path: "/dev/sdb", KernelPath: "/dev/sdb", Size: gib(8)}, // smallest = persistent
		},
		resolveFn: identityResolve(nil),
	}
	cfg := twoSetConfig()
	if err := sp.reserveManualSets(context.Background(), cfg); err != nil {
		t.Fatalf("reserveManualSets: %v", err)
	}
	if _, ok := sp.protected["/dev/sdb"]; !ok {
		t.Fatal("persistent disk /dev/sdb must be in the protected set")
	}
	if len(sp.pool) != 1 || sp.pool[0].KernelPath != "/dev/sda" {
		t.Fatalf("pool must have only /dev/sda left, got %+v", sp.pool)
	}
	// Now the discover resolution for the OS set cannot see sdb at all.
	if err := sp.resolveDiscover(&cfg.Sets[0]); err != nil {
		t.Fatalf("resolveDiscover: %v", err)
	}
	if len(cfg.Sets[0].Devices) != 1 || cfg.Sets[0].Devices[0] != "/dev/sda" {
		t.Fatalf("discover must select /dev/sda (never the reserved sdb), got %v", cfg.Sets[0].Devices)
	}
}

// TestReservationRefusesAliasedOverlap pins the canonical-path overlap
// check: two sets naming the same physical disk through different selector
// styles must refuse with a safety error.
func TestReservationRefusesAliasedOverlap(t *testing.T) {
	cfg := &v2.Config{
		Version: v2.SchemaVersion,
		Sets: []v2.Set{
			{
				Name: "a", Role: "os", Selection: "manual", Raid: "none",
				Devices: []string{"/dev/disk/by-id/wwn-0xdeadbeef"},
				Volumes: []v2.Volume{
					{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"},
					{Name: "efi", Mountpoint: "/boot/efi", FS: "vfat", Provider: "partition", Size: "512M"},
				},
			},
			{
				Name: "b", Role: "data", Selection: "manual", Raid: "none",
				Devices: []string{"/dev/sdb"},
				Volumes: []v2.Volume{
					{Name: "d", Mountpoint: "/d", FS: "xfs", Provider: "lvm", Size: "100%"},
				},
			},
		},
	}
	sp := &setProcessor{
		logger:    silentLogger(t),
		resolveFn: identityResolve(map[string]string{"/dev/disk/by-id/wwn-0xdeadbeef": "/dev/sdb"}),
	}
	err := sp.reserveManualSets(context.Background(), cfg)
	if err == nil {
		t.Fatal("aliased cross-set overlap must refuse")
	}
	var se *safetyError
	if !errors.As(err, &se) {
		t.Fatalf("overlap refusal must be a safetyError (exit-2 class), got %T: %v", err, err)
	}
}

// TestReservationFailsClosedOnResolveError pins fail-closed reservation: if
// a manual set's device cannot be resolved, the run halts — we cannot know
// which disk to protect.
func TestReservationFailsClosedOnResolveError(t *testing.T) {
	sp := &setProcessor{
		logger: silentLogger(t),
		resolveFn: func(context.Context, []string) ([]selector.Resolved, error) {
			return nil, errors.New("no such device")
		},
	}
	if err := sp.reserveManualSets(context.Background(), twoSetConfig()); err == nil {
		t.Fatal("reservation must fail closed on a resolve error")
	}
}

// TestSanitizeRefusesWithPersistentSet pins BR-8 (MCS-8): sanitize + a
// declared persistent set is a contradiction and must be a safety refusal,
// before any disk is touched.
func TestSanitizeRefusesWithPersistentSet(t *testing.T) {
	sp := &setProcessor{
		logger: silentLogger(t),
		pool: []discover.Device{
			{Name: "sda", Path: "/dev/sda", KernelPath: "/dev/sda", Size: gib(20)},
		},
		resolveFn: identityResolve(nil),
	}
	_, _, err := sp.runDecision(context.Background(), installcontext.ModeSanitize, twoSetConfig(), "uefi", false, true)
	if err == nil {
		t.Fatal("sanitize with a persistent set must refuse")
	}
	var se *safetyError
	if !errors.As(err, &se) {
		t.Fatalf("sanitize refusal must be a safetyError, got %T: %v", err, err)
	}
}

// TestFenceHaltIsSafetyError pins the fence's error class: the data-fence
// halt must map to the exit-2 safety_refusal contract, not a generic
// retryable failure (the retry-forever half of FTA Cluster B).
func TestFenceHaltIsSafetyError(t *testing.T) {
	err := safetyRefusalf("data-fence", "set %q is broken", "data")
	var se *safetyError
	if !errors.As(err, &se) {
		t.Fatalf("safetyRefusalf must produce *safetyError, got %T", err)
	}
	if se.reason != "data-fence" {
		t.Fatalf("reason = %q", se.reason)
	}
}
