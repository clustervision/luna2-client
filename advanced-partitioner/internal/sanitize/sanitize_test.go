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

// stubBlank pins the read-back seam for a test and restores it after.
func stubBlank(t *testing.T, blank bool, err error) {
	t.Helper()
	orig := readBlankFn
	readBlankFn = func(string) (bool, error) { return blank, err }
	t.Cleanup(func() { readBlankFn = orig })
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
	if len(got) != 1 || got[0] != "blkdiscard -f /dev/sda" {
		t.Fatalf("expected only blkdiscard, got %v", got)
	}
}

func TestEraseDiskFallsBackToWipeZap(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// blkdiscard fails; wipefs + sgdisk succeed.
	f.AddMatch(executor.FakeMatch{
		Path: "blkdiscard", ArgsPrefix: []string{"-f", "/dev/sdb"},
		Response: executor.FakeResponse{ExitCode: 1, Err: context.DeadlineExceeded},
	})
	s := New(Options{Runner: f})
	r, err := s.EraseDisk(context.Background(), "/dev/sdb")
	if err != nil {
		t.Fatalf("EraseDisk: %v", err)
	}
	if r.Method != "wipe+zap" || r.Discarded {
		t.Fatalf("expected wipe+zap fallback, got %+v", r)
	}
	got := calls(f)
	if len(got) != 3 || got[1] != "wipefs -a /dev/sdb" || got[2] != "sgdisk --zap-all /dev/sdb" {
		t.Fatalf("expected blkdiscard then wipefs then sgdisk, got %v", got)
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

func TestEraseDiskDiscardIgnoredFallsBackToWipeZap(t *testing.T) {
	// blkdiscard exits 0 but the device silently ignored the unmap (advisory
	// discard): read-back finds data -> the erase MUST fall back to wipe+zap
	// instead of reporting a false sanitize.
	stubBlank(t, false, nil)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	s := New(Options{Runner: f})
	r, err := s.EraseDisk(context.Background(), "/dev/sdc")
	if err != nil {
		t.Fatalf("EraseDisk: %v", err)
	}
	if r.Method != "wipe+zap" || r.Discarded {
		t.Fatalf("expected wipe+zap after ignored discard, got %+v", r)
	}
	got := calls(f)
	if len(got) != 3 || got[1] != "wipefs -a /dev/sdc" || got[2] != "sgdisk --zap-all /dev/sdc" {
		t.Fatalf("expected blkdiscard then wipefs then sgdisk, got %v", got)
	}
}

func TestEraseDiskVerifyErrorFallsBackToWipeZap(t *testing.T) {
	stubBlank(t, false, os.ErrPermission)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	s := New(Options{Runner: f})
	r, err := s.EraseDisk(context.Background(), "/dev/sdd")
	if err != nil {
		t.Fatalf("EraseDisk: %v", err)
	}
	if r.Method != "wipe+zap" {
		t.Fatalf("expected wipe+zap when verification cannot run, got %+v", r)
	}
}
