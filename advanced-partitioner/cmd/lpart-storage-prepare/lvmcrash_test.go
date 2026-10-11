package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
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
