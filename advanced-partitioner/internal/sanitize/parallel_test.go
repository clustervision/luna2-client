package sanitize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func TestDiskBound(t *testing.T) {
	if got := DiskBound(0); got != UnknownSizeBound {
		t.Fatalf("unknown size: got %s, want %s", got, UnknownSizeBound)
	}
	if got := DiskBound(1e9); got != minDiskBound {
		t.Fatalf("1 GB: got %s, want the %s minimum", got, minDiskBound)
	}
	// A 2 TB HDD written and read back at 50 MB/s: 80000 s, ~22 h.
	if got := DiskBound(2e12); got < 22*time.Hour || got > 23*time.Hour {
		t.Fatalf("2 TB: got %s, want ~22h", got)
	}
}

// The disks are erased at once: each read-back waits until every disk has
// reached its own, which a one-after-the-other erase never does.
func TestEraseAllErasesTheDisksInParallel(t *testing.T) {
	disks := []Disk{{Path: "/dev/sda"}, {Path: "/dev/sdb"}, {Path: "/dev/sdc"}}
	var mu sync.Mutex
	arrived := 0
	all := make(chan struct{})
	readBack := func(string) (bool, error) {
		mu.Lock()
		arrived++
		if arrived == len(disks) {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
			return true, nil
		case <-time.After(5 * time.Second):
			return false, errors.New("the other disks never started")
		}
	}
	s := New(Options{Runner: executor.NewFakeRunner(executor.FakeResponse{}), ReadBack: readBack})
	got, err := s.EraseAll(context.Background(), disks)
	if err != nil {
		t.Fatalf("EraseAll: %v", err)
	}
	if len(got) != len(disks) {
		t.Fatalf("erased %d of %d disks", len(got), len(disks))
	}
}

// A disk that cannot be erased fails the run, but only after the others
// were erased and recorded.
func TestEraseAllFinishesTheOtherDisksWhenOneFails(t *testing.T) {
	progress := filepath.Join(t.TempDir(), ProgressFile)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	s := New(Options{Runner: f, Progress: progress,
		ReadBack: func(disk string) (bool, error) { return disk != "/dev/sdb", nil }})
	got, err := s.EraseAll(context.Background(), []Disk{{Path: "/dev/sda"}, {Path: "/dev/sdb"}})
	if err == nil || !strings.Contains(err.Error(), "/dev/sdb") {
		t.Fatalf("want the /dev/sdb failure, got %v", err)
	}
	if len(got) != 1 || got[0].Disk != "/dev/sda" {
		t.Fatalf("want /dev/sda erased, got %+v", got)
	}
	b, rerr := os.ReadFile(progress) //nolint:gosec // the test's own temp file
	if rerr != nil || string(b) != "/dev/sda blkdiscard\n" {
		t.Fatalf("progress = %q (%v), want only /dev/sda", b, rerr)
	}
}

// A retry in the same boot does not erase a recorded disk again.
func TestEraseAllSkipsTheDisksAnEarlierAttemptErased(t *testing.T) {
	progress := filepath.Join(t.TempDir(), ProgressFile)
	if err := os.WriteFile(progress, []byte("/dev/sda zeroout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	s := New(Options{Runner: f, Progress: progress, ReadBack: func(string) (bool, error) { return true, nil }})
	got, err := s.EraseAll(context.Background(), []Disk{{Path: "/dev/sda"}, {Path: "/dev/sdb"}})
	if err != nil {
		t.Fatalf("EraseAll: %v", err)
	}
	if len(got) != 2 || !got[0].Overwritten || got[0].Method != "zeroout" {
		t.Fatalf("want /dev/sda reported zero-filled from the record, got %+v", got)
	}
	for _, c := range calls(f) {
		if strings.Contains(c, "/dev/sda") {
			t.Fatalf("/dev/sda erased again: %s", c)
		}
	}
}

// ctxRunner refuses to start a command under a finished context, as the real
// executor does.
type ctxRunner struct{ *executor.FakeRunner }

func (r ctxRunner) Run(ctx context.Context, spec executor.CommandSpec) (executor.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return executor.CommandResult{}, err
	}
	return r.FakeRunner.Run(ctx, spec)
}

// Past its bound a disk still gets its signatures and partition table cleared.
func TestAnEraseOutOfTimeStillClearsTheSignatures(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	s := New(Options{Runner: ctxRunner{f}})
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.eraseDisk(expired, context.Background(), "/dev/sdx"); err == nil {
		t.Fatal("an erase out of time must fail")
	}
	want := []string{"wipefs -a /dev/sdx", "sgdisk --zap-all /dev/sdx"}
	if got := calls(f); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}
