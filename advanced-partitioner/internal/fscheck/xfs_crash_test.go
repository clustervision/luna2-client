package fscheck

import (
	"context"
	"errors"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// xfsSeqRunner answers successive xfs_repair calls from a list; every other
// command succeeds.
type xfsSeqRunner struct {
	xfs   []executor.CommandResult
	errs  []error
	calls []string
}

func (r *xfsSeqRunner) Run(_ context.Context, spec executor.CommandSpec) (executor.CommandResult, error) {
	r.calls = append(r.calls, spec.Path)
	if spec.Path != "xfs_repair" || len(r.xfs) == 0 {
		return executor.CommandResult{}, nil
	}
	res, err := r.xfs[0], r.errs[0]
	r.xfs, r.errs = r.xfs[1:], r.errs[1:]
	return res, err
}

const dirtyLogAlert = "Phase 2 - using internal log\\nALERT: The filesystem has valuable metadata changes in a log which is being\\nignored because the -n option was used.\\n"

// Lab: a raid1 node hard reset while running; xfs_repair -n /dev/os/root
// crashed (SIGABRT / SIGSEGV) on the dirty log, the set was judged "unknown"
// and sync parked. Replaying the log first makes the check pass.
func TestXFSCrashOnDirtyLogReplaysTheLog(t *testing.T) {
	r := &xfsSeqRunner{
		xfs:  []executor.CommandResult{{ExitCode: -1, Stderr: []byte(dirtyLogAlert)}, {ExitCode: 0}},
		errs: []error{errors.New("signal: aborted (core dumped)"), nil},
	}
	c := New(Options{Runner: r, MountDir: t.TempDir()})
	got := c.checkXFS(context.Background(), "/dev/os/root")
	if !got.OK || got.Unavailable {
		t.Fatalf("result %+v, want OK after the journal replay; calls %v", got, r.calls)
	}
	want := []string{"xfs_repair", "mount", "umount", "xfs_repair"}
	if len(r.calls) != len(want) {
		t.Fatalf("calls %v, want %v", r.calls, want)
	}
}

// A crash without the dirty-log alert stays "checker unavailable".
func TestXFSCrashWithoutDirtyLogIsUnavailable(t *testing.T) {
	r := &xfsSeqRunner{
		xfs:  []executor.CommandResult{{ExitCode: -1, Stderr: []byte("Phase 1 - find and verify superblock...\\n")}},
		errs: []error{errors.New("signal: segmentation fault")},
	}
	c := New(Options{Runner: r, MountDir: t.TempDir()})
	if got := c.checkXFS(context.Background(), "/dev/os/root"); !got.Unavailable {
		t.Fatalf("result %+v, want Unavailable", got)
	}
}
