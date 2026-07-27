// Package inventory captures live block-device state into a directory
// of plain-text files. lpart-storage-prepare uses these snapshots in three
// places:
//
//  1. Before per-set cleanup, to record what was on the disks coming in
//     (lib/cleanup.sh::snapshot_storage_state "before").
//  2. After per-set creation, to record what we left behind
//     (snapshot_storage_state "after").
//  3. At the end of the full run, to record the live system view
//     (lib/output_json.sh-adjacent captures inside bin/lpart-storage-prepare:
//     blkid-after.txt, pvs-after.txt, vgs-after.txt, lvs-after.txt,
//     mdadm-detail-after.txt).
//
// All commands used here exist identically on RHEL 8/9/10, Rocky, Alma,
// Ubuntu, Debian, and on arm64. Anything LVM- or md-specific is
// guarded by Exists() so a node without those subsystems just gets the
// section skipped.
//
// The package never panics on a missing command — it just records a
// "command not available" line in the captured file. This is the same
// behaviour the Bash snapshot helpers exhibit.
package inventory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// Captured is one file written by Capture / Snapshot.
type Captured struct {
	Filename string // relative path under the destination dir
	Bytes    []byte
}

// SnapshotOptions controls Snapshot.
type SnapshotOptions struct {
	Runner executor.Runner

	// Dir is the directory to write files into. Created if missing.
	Dir string

	// Prefix is interpolated into the filenames the Bash version uses
	// ("before" / "after").
	Prefix string

	// Sysroot, when non-empty, gates the findmnt capture: if the
	// directory is missing the mount file is left empty (matching the
	// Bash helper).
	Sysroot string
}

// Snapshot writes the pre/post block-device snapshot to opts.Dir,
// returning the per-file content for callers that want to log or
// inspect it in-memory as well.
//
// Mirrors lib/cleanup.sh::snapshot_storage_state:
//
//	lsblk-<prefix>.txt    lsblk -o NAME,KNAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS,PKNAME,PATH,UUID
//	mount-<prefix>.txt    findmnt -R -o TARGET,SOURCE,FSTYPE,OPTIONS <sysroot>   (empty if sysroot not mounted)
func Snapshot(ctx context.Context, opts SnapshotOptions) ([]Captured, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("inventory.Snapshot: Runner is required")
	}
	if opts.Prefix == "" {
		return nil, fmt.Errorf("inventory.Snapshot: Prefix is required")
	}
	if err := os.MkdirAll(opts.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("inventory.Snapshot: mkdir %s: %w", opts.Dir, err)
	}

	out := make([]Captured, 0, 2)

	lsblkOut, err := capture(ctx, opts.Runner, executor.CommandSpec{
		Path: "lsblk",
		Args: []string{"-o", "NAME,KNAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS,PKNAME,PATH,UUID"},
	})
	if err != nil {
		// lsblk should always be present on a Linux installer; surface
		// the error rather than masking it.
		return nil, fmt.Errorf("inventory.Snapshot: lsblk: %w", err)
	}
	lsblkFile := fmt.Sprintf("lsblk-%s.txt", opts.Prefix)
	if err := writeFile(opts.Dir, lsblkFile, lsblkOut); err != nil {
		return nil, err
	}
	out = append(out, Captured{Filename: lsblkFile, Bytes: lsblkOut})

	// findmnt is only meaningful when sysroot is mounted. Match the
	// Bash branch: capture only if findmnt -R -M <sysroot> succeeds.
	mountFile := fmt.Sprintf("mount-%s.txt", opts.Prefix)
	var mountOut []byte
	if opts.Sysroot != "" {
		if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
			Path: "findmnt", Args: []string{"-R", "-M", opts.Sysroot},
		}); err == nil {
			res, err := opts.Runner.Run(ctx, executor.CommandSpec{
				Path:          "findmnt",
				Args:          []string{"-R", "-M", opts.Sysroot, "-o", "TARGET,SOURCE,FSTYPE,OPTIONS"},
				CaptureStdout: true,
			})
			if err == nil {
				mountOut = res.Stdout
			}
		}
	}
	if err := writeFile(opts.Dir, mountFile, mountOut); err != nil {
		return nil, err
	}
	out = append(out, Captured{Filename: mountFile, Bytes: mountOut})

	return out, nil
}

// FinalStateOptions controls FinalState.
type FinalStateOptions struct {
	Runner executor.Runner

	// Dir is the directory to write into.
	Dir string

	// HasMDStat indicates whether /proc/mdstat exists. lpart-storage-prepare
	// uses this to gate the mdadm capture. Tests inject false to keep
	// runs hermetic; production passes the real probe result.
	HasMDStat bool
}

// FinalState writes blkid-after.txt, pvs-after.txt, vgs-after.txt,
// lvs-after.txt, and (when /proc/mdstat exists) mdadm-detail-after.txt.
//
// Mirrors the capture_cmd block at the bottom of bin/lpart-storage-prepare's
// main().
func FinalState(ctx context.Context, opts FinalStateOptions) ([]Captured, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("inventory.FinalState: Runner is required")
	}
	if err := os.MkdirAll(opts.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("inventory.FinalState: mkdir %s: %w", opts.Dir, err)
	}

	captures := []struct {
		file string
		cmd  executor.CommandSpec
	}{
		{"blkid-after.txt", executor.CommandSpec{Path: "blkid"}},
		{"pvs-after.txt", executor.CommandSpec{Path: "pvs"}},
		{"vgs-after.txt", executor.CommandSpec{Path: "vgs"}},
		{"lvs-after.txt", executor.CommandSpec{Path: "lvs"}},
	}
	if opts.HasMDStat {
		captures = append(captures, struct {
			file string
			cmd  executor.CommandSpec
		}{"mdadm-detail-after.txt", executor.CommandSpec{Path: "mdadm", Args: []string{"--detail", "--scan"}}})
	}

	out := make([]Captured, 0, len(captures))
	for _, c := range captures {
		body, err := capture(ctx, opts.Runner, c.cmd)
		// Tolerant: a missing command or a non-zero exit means we
		// write a placeholder rather than fail. The Bash version
		// passes the error through capture_cmd → log + write empty.
		if err != nil {
			body = []byte(fmt.Sprintf("# %s failed or unavailable: %v\n", c.cmd.Path, err))
		}
		if err := writeFile(opts.Dir, c.file, body); err != nil {
			return nil, err
		}
		out = append(out, Captured{Filename: c.file, Bytes: body})
	}
	return out, nil
}

// MDStatProbe returns true when /proc/mdstat is present, matching the
// "[[ -f /proc/mdstat ]]" branch in the Bash equivalent. Exposed so
// tests can construct FinalStateOptions without touching /proc.
func MDStatProbe() bool {
	_, err := os.Stat("/proc/mdstat")
	return err == nil
}

func capture(ctx context.Context, runner executor.Runner, spec executor.CommandSpec) ([]byte, error) {
	spec.CaptureStdout = true
	res, err := runner.Run(ctx, spec)
	if err != nil {
		return res.Stdout, err
	}
	return res.Stdout, nil
}

func writeFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	if data == nil {
		data = []byte{}
	}
	if !strings.HasSuffix(string(data), "\n") && len(data) > 0 {
		data = append(data, '\n')
	}
	//nolint:gosec // capture file in operator-controlled runtime dir
	return os.WriteFile(path, data, 0o640)
}
