package selector

import (
	"context"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// LateDeviceWait is how long a configured device that is not there is waited
// for before it counts as missing: a slow controller can present a disk after
// the installer has started.
const LateDeviceWait = 30 * time.Second

const lateDevicePoll = 2 * time.Second

// WaitWhileMissing calls resolve until it reports no missing device, returns
// an error, or wait has passed, letting udev settle between attempts.
func WaitWhileMissing(ctx context.Context, runner executor.Runner, wait time.Duration, resolve func() (missing int, err error)) error {
	deadline := time.Now().Add(wait)
	for {
		n, err := resolve()
		if err != nil || n == 0 || !time.Now().Before(deadline) {
			return err
		}
		_, _ = runner.Run(ctx, executor.CommandSpec{
			Path: "udevadm", Args: []string{"settle", "--timeout=10"},
			CaptureStdout: true, CaptureStderr: true,
		})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lateDevicePoll):
		}
	}
}
