package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// TeeToFile copies raw stdout/stderr/command-output events to path while
// forwarding every event unchanged to the returned channel. The file is
// opened append-only; intermediate directories are created.
//
// Closes the output channel when src closes; the file handle is closed
// at that point too. Cancels via ctx.
func TeeToFile(ctx context.Context, path string, src <-chan InstallerEvent) (<-chan InstallerEvent, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640) //nolint:gosec // operator path
	if err != nil {
		return nil, err
	}

	out := make(chan InstallerEvent, cap(src)+16)
	go func() {
		defer close(out)
		defer func() { _ = f.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				if shouldLog(ev) {
					_, _ = fmt.Fprintf(f, "%s\n", ev.Message)
				}
				out <- ev
			}
		}
	}()
	return out, nil
}

func shouldLog(ev InstallerEvent) bool {
	switch ev.Type {
	case EvRawStdout, EvRawStderr,
		EvCommandStdout, EvCommandStderr,
		EvCommandStarted, EvCommandDone, EvCommandFailed,
		EvInstallerDone, EvInstallerFailed:
		return true
	}
	return false
}
