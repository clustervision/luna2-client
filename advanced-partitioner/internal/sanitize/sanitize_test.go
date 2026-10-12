package sanitize

import (
	"context"
	"os"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func calls(f *executor.FakeRunner) []string {
	out := make([]string, 0, len(f.Calls()))
	for _, c := range f.Calls() {
		out = append(out, c.Path+" "+strings.Join(c.Args, " "))
	}
	return out
}

// stubBlank pins both read-back seams (whole device after a discard, samples
// after a zero-out) for a test and restores them after.
func stubBlank(t *testing.T, blank bool, err error) {
	t.Helper()
	origS, origW := readBlankFn, readWholeFn
	readBlankFn = func(string) (bool, error) { return blank, err }
	readWholeFn = func(context.Context, string, func(string, ...any)) (bool, error) { return blank, err }
	t.Cleanup(func() { readBlankFn, readWholeFn = origS, origW })
}

// stubBlankSeq returns the given read-back results in order (discard check,
// then zero-out check), whichever seam asks.
func stubBlankSeq(t *testing.T, results ...bool) {
	t.Helper()
	origS, origW := readBlankFn, readWholeFn
	i := 0
	next := func() bool {
		r := results[len(results)-1]
		if i < len(results) {
			r = results[i]
		}
		i++
		return r
	}
	readBlankFn = func(string) (bool, error) { return next(), nil }
	readWholeFn = func(context.Context, string, func(string, ...any)) (bool, error) { return next(), nil }
	t.Cleanup(func() { readBlankFn, readWholeFn = origS, origW })
}

func TestEraseDiskBlkdiscardHappyPath(t *testing.T) {
	stubBlank(t, true, nil)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	s := New(Options{Runner: f})
	r, err := s.EraseDisk(context.Background(), "/dev/sda")
	if err != nil {
		t.Fatalf("EraseDisk: %v", err)
	}
	if r.Method != "blkdiscard" || !r.Discarded {
		t.Fatalf("expected blkdiscard discard, got %+v", r)
	}
	got := calls(f)
	if len(got) != 1 || got[0] != "blkdiscard /dev/sda" {
		t.Fatalf("expected only blkdiscard (no -f: el8 has no --force), got %v", got)
	}
}

func TestEraseDiskDiscardUnsupportedZeroFills(t *testing.T) {
	stubBlank(t, true, nil)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "blkdiscard", ArgsPrefix: []string{"/dev/sdb"},
		Response: executor.FakeResponse{ExitCode: 1, Err: context.DeadlineExceeded},
	})
	r, err := New(Options{Runner: f}).EraseDisk(context.Background(), "/dev/sdb")
	if err != nil {
		t.Fatalf("EraseDisk: %v", err)
	}
	if r.Method != "zeroout" || !r.Overwritten || r.Discarded {
		t.Fatalf("expected verified zero-fill, got %+v", r)
	}
	got := calls(f)
	if len(got) != 2 || got[1] != "blkdiscard -z /dev/sdb" {
		t.Fatalf("expected blkdiscard then blkdiscard -z, got %v", got)
	}
}

func TestEraseDiskDiscardIgnoredZeroFills(t *testing.T) {
	// blkdiscard exits 0 but the device silently ignored the unmap: the
	// read-back finds data, so the disk must be zero-filled, not reported erased.
	stubBlankSeq(t, false, true)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	r, err := New(Options{Runner: f}).EraseDisk(context.Background(), "/dev/sdc")
	if err != nil {
		t.Fatalf("EraseDisk: %v", err)
	}
	if r.Method != "zeroout" || !r.Overwritten {
		t.Fatalf("expected zero-fill after ignored discard, got %+v", r)
	}
}

func TestEraseDiskNotVerifiedFailsClosed(t *testing.T) {
	// Neither discard nor zero-fill reads back blank: signatures and the
	// partition table are still cleared, but the disk is NOT reported erased.
	stubBlankSeq(t, false, false)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	_, err := New(Options{Runner: f}).EraseDisk(context.Background(), "/dev/sdd")
	if err == nil {
		t.Fatal("an unverified erase must be an error, never a false sanitize")
	}
	got := calls(f)
	if len(got) != 4 || got[2] != "wipefs -a /dev/sdd" || got[3] != "sgdisk --zap-all /dev/sdd" {
		t.Fatalf("expected discard, zero-fill, wipefs, sgdisk; got %v", got)
	}
}

func TestEraseDiskVerifyErrorFailsClosed(t *testing.T) {
	stubBlank(t, false, os.ErrPermission)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	if _, err := New(Options{Runner: f}).EraseDisk(context.Background(), "/dev/sde"); err == nil {
		t.Fatal("when the read-back cannot run the disk must not be reported erased")
	}
}

func TestEraseAllRequiresDisks(t *testing.T) {
	s := New(Options{Runner: executor.NewFakeRunner(executor.FakeResponse{})})
	if _, err := s.EraseAll(context.Background(), nil); err == nil {
		t.Fatal("expected error for no disks")
	}
}

func TestEraseAllRequiresRunner(t *testing.T) {
	s := New(Options{})
	if _, err := s.EraseAll(context.Background(), []string{"/dev/sda"}); err == nil {
		t.Fatal("expected error for nil runner")
	}
}
