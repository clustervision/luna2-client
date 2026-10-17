package main

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

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
