package pipeline

// Barrier-pinning tests for the exit-code integrity chain (FTA Cluster B).
// The invariants pinned here:
//   - a sub-tool's numeric exit survives as a typed stepError (B1);
//   - exit 2 classifies as a safety refusal and exit 3 as fatal config, both
//     non-retryable; everything else retryable (B1);
//   - the terminal outcome is recorded on the Pipeline itself before the
//     event channel closes, so consumers never depend on the lossy display
//     bus (B2).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestStepErrorPreservesExitCode(t *testing.T) {
	err := error(&stepError{Bin: "lpart-storage-prepare", Stage: "storage-prepare", Code: 2})
	var se *stepError
	if !errors.As(err, &se) || se.Code != 2 {
		t.Fatalf("stepError must carry the numeric code; got %+v", se)
	}
	if se.Error() != "lpart-storage-prepare exited 2" {
		t.Fatalf("message: %s", se.Error())
	}
	// Wrapping (as phase code does with fmt.Errorf) must not lose the code.
	wrapped := fmt.Errorf("storage phase: %w", err)
	se = nil
	if !errors.As(wrapped, &se) || se.Code != 2 {
		t.Fatal("wrapping must preserve the typed step error")
	}
}

func TestClassifyFatalCodes(t *testing.T) {
	cases := []struct {
		err  error
		want FailureClass
	}{
		{&stepError{Code: 2}, FailSafety},    // safety refusal (data fence)
		{&stepError{Code: 3}, FailFatal},     // fatal config
		{&stepError{Code: 1}, FailRetryable}, // generic failure
		{&stepError{Code: 5}, FailRetryable},
		{errors.New("plain"), FailRetryable},
		{fmt.Errorf("wrap: %w", &stepError{Code: 2}), FailSafety},
	}
	for _, c := range cases {
		if got := classify(c.err); got != c.want {
			t.Errorf("classify(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestFailureClassFatal(t *testing.T) {
	if !FailFatal.Fatal() || !FailSafety.Fatal() || FailRetryable.Fatal() {
		t.Fatal("Fatal() mapping wrong")
	}
}

// TestResultRecordedBeforeChannelClose pins B2: by the time the event
// channel is closed, Result() must already hold the terminal outcome — the
// exit code never depends on a (droppable) terminal event being seen.
func TestResultRecordedBeforeChannelClose(t *testing.T) {
	p := New(context.Background(), Options{Phase: Phase("bogus")}) // unknown phase -> immediate failure
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	// Drain nothing: wait only for the channel to close, then read Result.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-p.Events():
			if !ok {
				res := p.Result()
				if !res.Done || !res.Failed {
					t.Fatalf("Result must be recorded before close; got %+v", res)
				}
				return
			}
		case <-deadline:
			t.Fatal("pipeline did not finish")
		}
	}
}

// TestZeroResultMeansInterrupted pins the default: a Pipeline whose run
// never reached a terminal state reports Done=false, which callers must
// treat as failure.
func TestZeroResultMeansInterrupted(t *testing.T) {
	p := New(context.Background(), Options{Phase: PhasePart})
	if res := p.Result(); res.Done {
		t.Fatal("un-run pipeline must not report Done")
	}
}

// Barrier pin for the RunCommand->runStepArgv seam (found by the real PXE
// regression): a sub-tool exiting non-zero must surface as a *stepError
// carrying that numeric code, so classify() can map exit 2/3 -> fatal.
// Before the fix, RunCommand's (code, exitError) made runStepArgv return the
// raw err and the code was lost -> a safety refusal retry-looped instead of
// parking. Drives a real child process end to end.
func TestRunStepArgvPreservesExitCode(t *testing.T) {
	p := New(context.Background(), Options{Phase: PhasePart, StepTimeout: 30 * time.Second})
	// consume events so the (buffered) channel never blocks the step
	go func() {
		for range p.Events() {
		}
	}()
	for _, code := range []int{1, 2, 3, 5} {
		err := p.runStepArgv("test", []string{"/bin/sh", "-c", "exit " + itoa(code)})
		var se *stepError
		if !errors.As(err, &se) {
			t.Fatalf("exit %d: expected *stepError, got %T (%v)", code, err, err)
		}
		if se.Code != code {
			t.Fatalf("exit %d: stepError.Code = %d", code, se.Code)
		}
		if code == 2 || code == 3 {
			if !classify(err).Fatal() {
				t.Fatalf("exit %d must park, got retryable", code)
			}
		} else if classify(err) != FailRetryable {
			t.Fatalf("exit %d must classify FailRetryable", code)
		}
	}
	// a successful child returns nil
	if err := p.runStepArgv("test", []string{"/bin/sh", "-c", "exit 0"}); err != nil {
		t.Fatalf("exit 0 should be nil, got %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// Post-phase pin for the sanitize seam (found live on node001 the moment the
// sanitize exit-0 fix let the post phase run): runPost must resolve the
// install mode BEFORE requiring storage-result.json, because sanitize (no
// bootloader, no results by design) must SKIP the finalise cleanly -- not
// fail on the missing result file and retry-loop. FTA v2 CCF-7 class.
func TestPostPhaseSkipsBootloaderForSanitize(t *testing.T) {
	rt := t.TempDir()
	// install-context selects sanitize; NO storage-result.json exists.
	if err := os.WriteFile(rt+"/install-context.json", []byte(`{"install_mode":"sanitize"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New(context.Background(), Options{Phase: PhasePost, RuntimeDir: rt, StepTimeout: 10 * time.Second})
	go func() {
		for range p.Events() {
		}
	}()
	if err := p.runPost(); err != nil {
		t.Fatalf("sanitize post phase must skip the bootloader finalise, got: %v", err)
	}
	// Control: a bootloader-installing mode with no result file still fails.
	if err := os.WriteFile(rt+"/install-context.json", []byte(`{"install_mode":"auto"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.runPost(); err == nil {
		t.Fatal("auto post phase without storage-result.json must fail")
	}
}
