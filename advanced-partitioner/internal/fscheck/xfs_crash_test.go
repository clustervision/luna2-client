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

// A clean filesystem is checked read-only: xfs_repair -n, no mount.
func TestXFSCleanIsNeverMounted(t *testing.T) {
	r := &xfsSeqRunner{xfs: []executor.CommandResult{{ExitCode: 0}}, errs: []error{nil}}
	got := New(Options{Runner: r, MountDir: t.TempDir()}).checkXFS(context.Background(), "/dev/os/root", true)
	if !got.OK {
		t.Fatalf("result %+v, want OK", got)
	}
	if strings.Join(r.calls, " ") != "xfs_repair xfs_repair" {
		t.Fatalf("calls %v, want xfs_repair -V then xfs_repair -n only", r.calls)
	}
}

// A check that crashes or hangs on a dirty log is replayed and re-checked
// when replay is allowed (the os set).
func TestXFSCrashIsReplayedOnTheOSSet(t *testing.T) {
	r := &xfsSeqRunner{
		xfs:  []executor.CommandResult{{ExitCode: -1, Stderr: []byte(dirtyLogAlert)}, {ExitCode: 0}},
		errs: []error{errors.New("signal: aborted (core dumped)"), nil},
	}
	got := New(Options{Runner: r, MountDir: t.TempDir()}).checkXFS(context.Background(), "/dev/os/root", true)
	if !got.OK || strings.Join(r.calls, " ") != "xfs_repair xfs_repair mount umount xfs_repair" {
		t.Fatalf("result %+v calls %v, want OK after replay", got, r.calls)
	}
}

// Without replay (a data set) a log that needs replaying is unknown, and the
// filesystem is never mounted.
func TestXFSDirtyLogWithoutReplayIsUnknown(t *testing.T) {
	r := &xfsSeqRunner{
		xfs:  []executor.CommandResult{{ExitCode: 1, Stderr: []byte(dirtyLogAlert)}},
		errs: []error{errors.New("exit status 1")},
	}
	got := New(Options{Runner: r, MountDir: t.TempDir()}).checkXFS(context.Background(), "/data/lv", false)
	if got.OK || !got.Unavailable || !strings.Contains(got.Reason, "needs replaying") {
		t.Fatalf("result %+v, want unknown (needs replaying)", got)
	}
	for _, c := range r.calls {
		if c == "mount" {
			t.Fatalf("a data set's filesystem was mounted: %v", r.calls)
		}
	}
}

// The log cannot be replayed (mount fails): unhealthy, not unknown.
func TestXFSUnmountableDirtyLogIsUnhealthy(t *testing.T) {
	r := &xfsSeqRunner{
		xfs:      []executor.CommandResult{{ExitCode: -1, Stderr: []byte(dirtyLogAlert)}},
		errs:     []error{errors.New("signal: aborted (core dumped)")},
		mountErr: errors.New("exit status 32"),
	}
	got := New(Options{Runner: r, MountDir: t.TempDir()}).checkXFS(context.Background(), "/dev/os/root", true)
	if got.OK || got.Unavailable || !strings.Contains(got.Reason, "journal-replay mount failed") {
		t.Fatalf("result %+v, want unhealthy (replay mount failed)", got)
	}
}

// xfs_repair -n carries the time limit, so a hang ends as "needs replay".
func TestXFSCheckIsTimeBounded(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	New(Options{Runner: f, MountDir: t.TempDir()}).checkXFS(context.Background(), "/dev/os/root", true)
	for _, c := range f.Calls() {
		if c.Path == "xfs_repair" && len(c.Args) > 0 && c.Args[0] == "-n" && c.Timeout != xfsCheckTimeout {
			t.Fatalf("xfs_repair -n timeout = %v, want %v", c.Timeout, xfsCheckTimeout)
		}
	}
}
