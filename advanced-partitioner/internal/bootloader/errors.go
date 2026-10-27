package bootloader

import (
	"errors"
	"fmt"
)

// FatalError marks a failure that is the same on every attempt: the OS image
// lacks something the boot flow needs (Kind "image"), or the node's
// configuration / storage result cannot be booted as asked (Kind "config").
// lpart-bootloader-finalise maps it to exit 3 (park: cli.ExitFatalConfig);
// every other error stays exit 1, because the wrapper retries exit 1 every
// 30 s for ever and in full mode each retry re-wipes the disks and downloads
// the image again -- right for a transient command or I/O failure, pointless
// for a missing package.
type FatalError struct {
	Kind   string // "image" or "config"
	Reason string // short machine-readable token, e.g. "image-missing-tool"
	Err    error
}

func (e *FatalError) Error() string { return e.Err.Error() }

func (e *FatalError) Unwrap() error { return e.Err }

// imageErrorf returns a FatalError for something the OS image is missing.
func imageErrorf(reason, format string, args ...any) error {
	return &FatalError{Kind: "image", Reason: reason, Err: fmt.Errorf(format, args...)}
}

// configErrorf returns a FatalError for a configuration that cannot work.
func configErrorf(reason, format string, args ...any) error {
	return &FatalError{Kind: "config", Reason: reason, Err: fmt.Errorf(format, args...)}
}

// AsFatal reports whether err (or anything it wraps) is a FatalError.
func AsFatal(err error) (*FatalError, bool) {
	var fe *FatalError
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}
