package fscheck

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// A checker missing from the ramdisk (exec failure, no exit status) is NOT a
// verdict on the filesystem: it must come back Unavailable, so the data fence
// can never read "e2fsck is not installed" as "the os set is broken" and wipe
// it. One case per checker family.
func TestMissingCheckerIsUnavailableNotBroken(t *testing.T) {
	notFound := fmt.Errorf("e2fsck -f -p /dev/x: %w", &exec.Error{Name: "e2fsck", Err: exec.ErrNotFound})
	cases := []struct {
		fs, tool string
		err      error
	}{
		{"ext4", "e2fsck", notFound},
		{"xfs", "xfs_repair", &exec.Error{Name: "xfs_repair", Err: exec.ErrNotFound}},
		{"vfat", "fsck.vfat", errors.New("signal: killed")}, // no exit status
	}
	for _, c := range cases {
		t.Run(c.fs, func(t *testing.T) {
			f := executor.NewFakeRunner(executor.FakeResponse{})
			f.AddMatch(executor.FakeMatch{Path: c.tool, Response: executor.FakeResponse{Err: c.err}})
			got := New(Options{Runner: f}).CheckFS(context.Background(), "/dev/x", c.fs)
			if got.OK || !got.Unavailable {
				t.Fatalf("missing %s must be Unavailable (not OK, not a plain failure); got %+v", c.tool, got)
			}
			for _, call := range f.Calls() {
				if call.Path == "mount" {
					t.Fatalf("an unavailable checker must not trigger the xfs replay mount: %v", call)
				}
			}
		})
	}
}

// An exit code is a verdict, even an ugly one: rc=4 from e2fsck stays a plain
// unhealthy result, never Unavailable.
func TestExitCodeIsAVerdictNotUnavailable(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "e2fsck", Response: stubExit(4)})
	got := New(Options{Runner: f}).CheckFS(context.Background(), "/dev/x", "ext4")
	if got.OK || got.Unavailable {
		t.Fatalf("rc=4 must be unhealthy and NOT unavailable; got %+v", got)
	}
}
