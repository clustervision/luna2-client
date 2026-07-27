package engine

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// QuietConsole lowers the kernel's console log level to 1 (KERN_ALERT)
// so kernel printks stop scrolling on top of the TUI. The full ring
// buffer is unaffected — dmesg/journalctl still show every message.
//
// Returns a restore function that puts the previous console level
// back. Safe to call on systems without /proc/sys/kernel/printk: the
// restore function is a no-op there.
//
// Format of /proc/sys/kernel/printk is four space-separated ints:
//
//	<console_loglevel> <default_loglevel> <minimum> <default_console>
//
// We only touch the first field.
func QuietConsole() (restore func(), err error) {
	noop := func() {}
	if runtime.GOOS != "linux" {
		return noop, nil
	}
	const path = "/proc/sys/kernel/printk"
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return noop, nil
		}
		return noop, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return noop, errors.New("unexpected /proc/sys/kernel/printk format")
	}
	prev, err := strconv.Atoi(fields[0])
	if err != nil {
		return noop, fmt.Errorf("parse console loglevel: %w", err)
	}
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil { //nolint:gosec // sysctl path
		return noop, err
	}
	return func() {
		_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", prev)), 0o644) //nolint:gosec,errcheck
	}, nil
}
