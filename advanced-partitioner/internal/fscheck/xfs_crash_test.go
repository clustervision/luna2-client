package fscheck

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// xfsSeqRunner answers successive xfs_repair calls from a list and records
// every command; every other command succeeds unless mountErr is set.
type xfsSeqRunner struct {
	xfs      []executor.CommandResult
	errs     []error
	mountErr error
	calls    []string
}

func (r *xfsSeqRunner) Run(_ context.Context, spec executor.CommandSpec) (executor.CommandResult, error) {
	r.calls = append(r.calls, spec.Path)
	switch {
	case spec.Path == "mount" && r.mountErr != nil:
		return executor.CommandResult{ExitCode: 32}, r.mountErr
	case spec.Path != "xfs_repair" || len(spec.Args) == 0 || spec.Args[0] != "-n" || len(r.xfs) == 0:
		return executor.CommandResult{}, nil
	}
	res, err := r.xfs[0], r.errs[0]
	r.xfs, r.errs = r.xfs[1:], r.errs[1:]
	return res, err
}

const dirtyLogAlert = "Phase 2 - using internal log\nALERT: The filesystem has valuable metadata changes in a log which is being\nignored because the -n option was used.\n"

// Lab: a raid1 node hard reset while running; xfs_repair -n /dev/os/root on
// the dirty log crashed (el8 SIGABRT, Ubuntu SIGSEGV) or hung (el9). The log
// is replayed BEFORE xfs_repair -n ever runs.
func TestXFSLogIsReplayedBeforeTheCheck(t *testing.T) {
	r := &xfsSeqRunner{xfs: []executor.CommandResult{{ExitCode: 0}}, errs: []error{nil}}
	got := New(Options{Runner: r, MountDir: t.TempDir()}).checkXFS(context.Background(), "/dev/os/root")
	if !got.OK {
		t.Fatalf("result %+v, want OK", got)
	}
	if strings.Join(r.calls, " ") != "xfs_repair mount umount xfs_repair" {
		t.Fatalf("calls %v, want xfs_repair -V, mount, umount, then xfs_repair -n", r.calls)
	}
}

// A crash without a failed replay stays "checker unavailable".
func TestXFSCrashIsUnavailable(t *testing.T) {
	r := &xfsSeqRunner{
		xfs:  []executor.CommandResult{{ExitCode: -1, Stderr: []byte("Phase 1 - find and verify superblock...\n")}},
		errs: []error{errors.New("signal: segmentation fault")},
	}
	if got := New(Options{Runner: r, MountDir: t.TempDir()}).checkXFS(context.Background(), "/dev/os/root"); !got.Unavailable {
		t.Fatalf("result %+v, want Unavailable", got)
	}
}

// The log cannot be replayed (mount fails) and the check crashes on the
// dirty log: the filesystem does not come up -- unhealthy, not unknown.
func TestXFSUnmountableDirtyLogIsUnhealthy(t *testing.T) {
	r := &xfsSeqRunner{
		xfs:      []executor.CommandResult{{ExitCode: -1, Stderr: []byte(dirtyLogAlert)}},
		errs:     []error{errors.New("signal: aborted (core dumped)")},
		mountErr: errors.New("exit status 32"),
	}
	got := New(Options{Runner: r, MountDir: t.TempDir()}).checkXFS(context.Background(), "/dev/os/root")
	if got.OK || got.Unavailable || !strings.Contains(got.Reason, "does not mount") {
		t.Fatalf("result %+v, want unhealthy (does not mount)", got)
	}
}
