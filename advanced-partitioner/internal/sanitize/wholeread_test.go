package sanitize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// A discard honoured for most of the disk but not next to its middle (lab, 12
// Oct 2026, qcow2-backed virtio): the three 1 MiB samples read back zero, so
// sampling alone reported the disk erased with data left on it.
func makePartlyDiscardedDisk(t *testing.T) (string, int64) {
	t.Helper()
	const size = 64 << 20
	b := make([]byte, size)
	at := int64(size/2 + 600<<10) // just past the middle sample [size/2-512K, size/2+512K)
	for i := int64(0); i < 300<<10; i++ {
		b[at+i] = 0x5a
	}
	p := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, at
}

func TestWholeReadFindsDataTheSamplesMiss(t *testing.T) {
	disk, at := makePartlyDiscardedDisk(t)
	if blank, err := readsBackBlankDevice(disk); err != nil || !blank {
		t.Fatalf("precondition: the samples miss the data (blank=%v err=%v)", blank, err)
	}
	var logged []string
	info := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	blank, err := readsBackBlankWholeDevice(context.Background(), disk, info)
	if err != nil || blank {
		t.Fatalf("whole read: blank=%v err=%v, want data found", blank, err)
	}
	if want := fmt.Sprintf("offset %d of", at); len(logged) != 1 || !strings.Contains(logged[0], want) {
		t.Fatalf("log = %v, want %q", logged, want)
	}
}

func TestWholeReadBlankDisk(t *testing.T) {
	p := filepath.Join(t.TempDir(), "zero.img")
	if err := os.WriteFile(p, make([]byte, 20<<20+12345), 0o600); err != nil { // not a multiple of the chunk
		t.Fatal(err)
	}
	if blank, err := readsBackBlankWholeDevice(context.Background(), p, func(string, ...any) {}); err != nil || !blank {
		t.Fatalf("blank=%v err=%v", blank, err)
	}
}

// End to end through EraseDisk: the discard "succeeds", the whole read finds
// the data, so the disk is zero-filled instead of reported discarded.
func TestEraseDiskPartlyHonouredDiscardZeroFills(t *testing.T) {
	disk, _ := makePartlyDiscardedDisk(t)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	r, err := New(Options{Runner: f}).EraseDisk(context.Background(), disk)
	if err != nil {
		t.Fatalf("EraseDisk: %v", err)
	}
	if r.Discarded || r.Method != "zeroout" {
		t.Fatalf("a partly honoured discard must not be reported as the erase: %+v", r)
	}
	if got := calls(f); len(got) != 2 || got[1] != "blkdiscard -z "+disk {
		t.Fatalf("calls = %v", got)
	}
}

func TestWholeReadStopsOnCancel(t *testing.T) {
	disk, _ := makePartlyDiscardedDisk(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readsBackBlankWholeDevice(ctx, disk, func(string, ...any) {}); err == nil {
		t.Fatal("a cancelled read-back must return the context error")
	}
}
