package engine

// Barrier pin for the /dev/kmsg drain hang (found on node001, pre phase):
// ReadKmsgBacklog MUST return promptly — it drains the existing ring buffer
// and stops on EAGAIN. Wrapping an O_NONBLOCK /dev/kmsg in an *os.File hands
// it to Go's netpoller, which parks forever after the drain waiting for the
// next kernel message; the raw-syscall implementation returns instead. This
// test fails (times out) if that regression comes back.

import (
	"testing"
	"time"
)

func TestReadKmsgBacklogDoesNotHang(t *testing.T) {
	done := make(chan int, 1)
	go func() {
		evs := ReadKmsgBacklog(800)
		done <- len(evs)
	}()
	select {
	case n := <-done:
		// On Linux CI /dev/kmsg exists and yields records; on a sandbox
		// without it we get 0. Either way the call must RETURN — that is
		// the whole point. n is informational.
		t.Logf("ReadKmsgBacklog returned %d record(s)", n)
	case <-time.After(10 * time.Second):
		t.Fatal("ReadKmsgBacklog hung — the /dev/kmsg poller-park regression is back")
	}
}

// Draining is bounded by the limit even if the ring is enormous.
func TestReadKmsgBacklogRespectsLimit(t *testing.T) {
	done := make(chan int, 1)
	go func() { done <- len(ReadKmsgBacklog(5)) }()
	select {
	case n := <-done:
		if n > 5 {
			t.Fatalf("returned %d > limit 5", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadKmsgBacklog hung with a small limit")
	}
}
