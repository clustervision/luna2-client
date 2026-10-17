package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/discover"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
)

// Lab, RHEL 8: lvm2 2.03.14 crashed (SIGSEGV) in pvcreate on a new raid0 that
// began with remnants of an older LVM; every retry crashed the same way in
// pvs, and the node retried for ever. A crashing lvm tool now parks the node.
func TestLVMCrashParks(t *testing.T) {
	err := fmt.Errorf("set %q: %w", "data", fmt.Errorf("pvcreate /dev/md/data: %w", errors.New("signal: segmentation fault (core dumped)")))
	var stderr bytes.Buffer
	if rc := exitForError(&stderr, err); rc != 2 {
		t.Fatalf("exit %d, want 2 (park); stderr %s", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "safety_refusal=lvm-crashed") || !strings.Contains(stderr.String(), "pvcreate") {
		t.Fatalf("stderr does not name the crash: %s", stderr.String())
	}
	// an ordinary lvm failure stays retryable
	stderr.Reset()
	if rc := exitForError(&stderr, errors.New("pvcreate /dev/md/data: exit status 5")); rc != 1 {
		t.Fatalf("plain lvm failure: exit %d, want 1", rc)
	}
}

// The same crash comes back if the LVM builder loses its PV zeroing: a real
// run zeroes with lvm.ZeroStart, and a dry run never writes the device.
func TestLVMOptionsZeroThePVOnlyOutsideDryRun(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	logger := silentLogger(t)
	o := lvmOptions(f, logger, false)
	if o.Runner != executor.Runner(f) || o.Logger != logger {
		t.Fatalf("runner or logger not passed through: %+v", o)
	}
	if o.ZeroDevice == nil || reflect.ValueOf(o.ZeroDevice).Pointer() != reflect.ValueOf(lvm.ZeroStart).Pointer() {
		t.Fatal("a real run must zero the PV start with lvm.ZeroStart")
	}
	if o := lvmOptions(f, logger, true); o.ZeroDevice != nil {
		t.Fatal("a dry run must not zero the PV start")
	}
}

// crashingPVsProcessor is a set processor on one disk whose pvs is killed by a
// signal, as lvm2 is by some old LVM remnants.
func crashingPVsProcessor(t *testing.T) (*setProcessor, *executor.FakeRunner) {
	t.Helper()
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "pvs", Response: executor.FakeResponse{
		ExitCode: -1, Err: errors.New("pvs --noheadings -o pv_name,vg_name: signal: segmentation fault (core dumped)")}})
	tmp := t.TempDir()
	mdstat := filepath.Join(tmp, "mdstat")
	if err := os.WriteFile(mdstat, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cl := cleanup.New(cleanup.Options{
		Runner: f, CommandAvailable: func(string) bool { return true }, Sleep: func(time.Duration) {},
		MDStatPath: mdstat, Stat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }, DevDir: tmp,
	})
	return &setProcessor{
		logger: silentLogger(t), runner: f, cleaner: cl,
		pool:             []discover.Device{{Path: "/dev/vda", KernelPath: "/dev/vda", Size: gib(20)}},
		sanitizeReadBack: func(string) (bool, error) { return true, nil },
	}, f
}

// Sanitize erases every local disk, so a crashing pvs has nothing left to
// protect there: the erase still runs.
func TestSanitizeErasesWhenPVsCrashes(t *testing.T) {
	sp, f := crashingPVsProcessor(t)
	if _, _, err := sp.runSanitize(context.Background(), false); err != nil {
		t.Fatalf("sanitize stopped on a crashing pvs: %v", err)
	}
	erased := false
	for _, c := range f.Calls() {
		erased = erased || c.Path == "blkdiscard"
	}
	if !erased || sp.sanitized != 1 {
		t.Fatalf("disk not erased (blkdiscard ran: %v, sanitized %d)", erased, sp.sanitized)
	}
}

// A full install still parks on the same crash before anything is wiped.
func TestCreateCleanupParksWhenPVsCrashes(t *testing.T) {
	sp, f := crashingPVsProcessor(t)
	set := &v2.Set{Name: "os", Role: "os", Raid: "none", Devices: []string{"/dev/vda"}}
	err := sp.prepareCreate(context.Background(), plannedSet{set: set, disks: []selector.Resolved{{ResolvedPath: "/dev/vda"}}}, true)
	if err == nil {
		t.Fatal("a full install must stop on a crashing pvs")
	}
	var stderr bytes.Buffer
	if rc := exitForError(&stderr, err); rc != 2 || !strings.Contains(stderr.String(), "safety_refusal=lvm-crashed") {
		t.Fatalf("exit %d, want 2 lvm-crashed; stderr %s", rc, stderr.String())
	}
	for _, c := range f.Calls() {
		if c.Path == "wipefs" || c.Path == "sgdisk" || c.Path == "dd" || c.Path == "blkdiscard" {
			t.Fatalf("%s ran after the crash: %v", c.Path, c.Args)
		}
	}
}
