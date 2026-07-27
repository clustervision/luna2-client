// Package fsops is the dry-run-aware chokepoint for NATIVE filesystem
// mutations — the Go-side analog of the executor's dry-run gate for
// subprocesses. The FTA of this installer (and of its ancestor) found the
// same defect class twice: `--dry-run` gates every external command through
// the executor, while direct os.WriteFile/MkdirAll/RemoveAll/copy calls run
// for real, so a "preview" mutates the target (CCF-1/CCF-2; the dry-run
// firewall was lost once already in the port). Routing every mutating
// native call through this package makes the invariant structural instead
// of per-call-site discipline.
//
// Read-only calls (Stat, ReadFile, ReadDir, Open) intentionally have no
// wrappers — reads are always safe and wrapping them would only add noise.
package fsops

import (
	"fmt"
	"io"
	"os"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// FS performs native filesystem mutations, honouring DryRun. The zero
// value is a real (non-dry) FS with no logger, so plumbing it through
// existing structs cannot accidentally turn destructive paths off.
type FS struct {
	DryRun bool
	Logger *txlog.Logger
}

func (f FS) log(format string, args ...any) {
	if f.Logger != nil {
		f.Logger.Info(fmt.Sprintf(format, args...))
	}
}

// WriteFile mirrors os.WriteFile.
func (f FS) WriteFile(path string, data []byte, perm os.FileMode) error {
	if f.DryRun {
		f.log("(dry-run) would write %s (%d bytes)", path, len(data))
		return nil
	}
	return os.WriteFile(path, data, perm) //nolint:gosec // chokepoint: callers own path provenance; installer runs as root by design
}

// MkdirAll mirrors os.MkdirAll.
func (f FS) MkdirAll(path string, perm os.FileMode) error {
	if f.DryRun {
		f.log("(dry-run) would mkdir -p %s", path)
		return nil
	}
	return os.MkdirAll(path, perm) //nolint:gosec // chokepoint: callers own path provenance
}

// RemoveAll mirrors os.RemoveAll.
func (f FS) RemoveAll(path string) error {
	if f.DryRun {
		f.log("(dry-run) would rm -rf %s", path)
		return nil
	}
	return os.RemoveAll(path)
}

// Remove mirrors os.Remove.
func (f FS) Remove(path string) error {
	if f.DryRun {
		f.log("(dry-run) would rm %s", path)
		return nil
	}
	return os.Remove(path)
}

// CopyFile copies src to dst (creating/truncating dst) with the given
// permissions.
func (f FS) CopyFile(src, dst string, perm os.FileMode) error {
	if f.DryRun {
		f.log("(dry-run) would copy %s -> %s", src, dst)
		return nil
	}
	in, err := os.Open(src) //nolint:gosec // chokepoint: callers own path provenance
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm) //nolint:gosec // chokepoint
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
