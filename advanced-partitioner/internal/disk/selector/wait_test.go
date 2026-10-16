package selector

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// A device that appears during the wait is found, after udev settled.
func TestWaitWhileMissingFindsALateDevice(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	attempts := 0
	err := WaitWhileMissing(context.Background(), f, 10*time.Second, func() (int, error) {
		attempts++
		if attempts < 2 {
			return 1, nil
		}
		return 0, nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d, want the second attempt to find it", err, attempts)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0].Path != "udevadm" {
		t.Fatalf("calls = %+v, want one udevadm settle between the attempts", calls)
	}
}

// Without wait (or once it has passed) a missing device stays missing, and a
// resolve error ends the wait at once.
func TestWaitWhileMissingIsBounded(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	attempts := 0
	if err := WaitWhileMissing(context.Background(), f, 0, func() (int, error) { attempts++; return 1, nil }); err != nil || attempts != 1 {
		t.Fatalf("err=%v attempts=%d, want one attempt and no wait", err, attempts)
	}
	boom := errors.New("lsblk failed")
	if err := WaitWhileMissing(context.Background(), f, time.Minute, func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("err=%v, want the resolve error at once", err)
	}
}
