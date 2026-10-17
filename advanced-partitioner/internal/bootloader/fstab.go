package bootloader

import (
	"bufio"
	"context"
	"fmt"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// WriteFstab writes /sysroot/etc/fstab from a StorageResult, based on
// lib/bootloader_common.sh::write_fstab_from_result with one deliberate
// change (see below):
//
//   - Existing fstab lines are preserved — EXCEPT lines whose mount point
//     is one this install just created. Those are REPLACED by the entry
//     for the node's real filesystem. The predecessor skipped them
//     ("already present"), which preserved the golden IMAGE's line — e.g.
//     a cloud image's `LABEL=cloudimg-rootfs /` — so the installed node
//     tried to mount / by a label/UUID that does not exist on its disks
//     and dropped to the emergency shell (ancestor FTA TE-3 MCS-6;
//     reproduced against a real Ubuntu 24.04 rootfs during Phase 9
//     validation). Replace-by-mountpoint keeps re-runs idempotent (the
//     second run replaces an identical line) AND corrects stale image
//     entries.
//   - dump/pass per fs type:
//     ext2/3/4 → dump=1, pass=1 for /, pass=2 elsewhere
//     xfs/btrfs/swap/vfat (and everything else) → 0 0
//   - /boot/efi uses options "umask=0077,shortname=winnt"
//   - swap line: UUID=<u> none swap defaults 0 0
//   - tmpfs:     tmpfs <mount> tmpfs <options> 0 0  (options from the FS entry)
//   - Anything else: UUID=<u> <mount> <fs> defaults <dump> <pass>
//
// The boot-context wrapper produces sr; pass sr.MountedTargetRoot as
// sysroot here so test fixtures don't have to mirror the /sysroot prefix.
// runner reads back the UUID of a filesystem the result has none for.
func WriteFstab(fs fsops.FS, runner executor.Runner, sysroot string, sr result.StorageResult) error {
	if sysroot == "" {
		return fmt.Errorf("bootloader.WriteFstab: sysroot is required")
	}
	fstabPath := filepath.Join(sysroot, "etc", "fstab")
	if err := fs.MkdirAll(filepath.Dir(fstabPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", fstabPath, err)
	}
	existing := readExistingFstab(fstabPath)

	// Render our lines first so a malformed entry errors before any
	// filtering decision.
	ours := make([]string, 0, len(sr.Filesystems))
	ourMounts := make([]string, 0, len(sr.Filesystems))
	for _, fsEnt := range sr.Filesystems {
		if fsEnt.UUID == "" && fsEnt.FS != "tmpfs" {
			fsEnt.UUID = recoverUUID(runner, fs.Logger, fsEnt)
		}
		line, err := fstabLineFor(fsEnt)
		if err != nil {
			return err
		}
		ours = append(ours, line)
		ourMounts = append(ourMounts, mountKey(fsEnt))
	}

	// Keep existing lines that do not conflict with a mount we own.
	kept := make([]string, 0, len(existing))
	for _, line := range existing {
		conflict := false
		for _, m := range ourMounts {
			if fstabAlreadyHasMount([]string{line}, m) {
				conflict = true
				break
			}
		}
		if !conflict {
			kept = append(kept, line)
		}
	}

	out := bytes(append(kept, ours...))
	return fs.WriteFile(fstabPath, out, 0o644)
}

// fstabLineFor returns the fstab line for one filesystem. err is
// non-nil only when the entry is malformed (e.g. a non-tmpfs entry
// missing its UUID).
func fstabLineFor(fs result.Filesystem) (string, error) {
	if fs.FS == "tmpfs" {
		opts := fs.Options
		if opts == "" {
			opts = "defaults"
		}
		return fmt.Sprintf("tmpfs %s tmpfs %s 0 0", fs.Mount, opts), nil
	}
	if fs.UUID == "" {
		return "", configErrorf("fstab-missing-uuid", "missing UUID for mount %s", fs.Mount)
	}
	switch fs.Mount {
	case "swap":
		return fmt.Sprintf("UUID=%s none swap defaults 0 0", fs.UUID), nil
	case "/boot/efi":
		// nofail: a mirrored ESP that is not assembled must not stop the boot
		return fmt.Sprintf("UUID=%s %s %s umask=0077,shortname=winnt,nofail,x-systemd.device-timeout=10s 0 0",
			fs.UUID, fs.Mount, fs.FS), nil
	}
	dump, pass := dumpAndPassFor(fs.FS, fs.Mount)
	return fmt.Sprintf("UUID=%s %s %s defaults %d %d",
		fs.UUID, fs.Mount, fs.FS, dump, pass), nil
}

// recoverUUID reads the UUID of a filesystem the storage result recorded
// without one, after giving udev a bounded time to catch up with the new
// filesystem. "" when blkid does not report one either.
func recoverUUID(runner executor.Runner, logger *txlog.Logger, fs result.Filesystem) string {
	if fs.Device == "" {
		return ""
	}
	ctx := context.Background()
	_, _ = runner.Run(ctx, executor.CommandSpec{
		Path: "udevadm", Args: []string{"settle", "--timeout=10"},
		CaptureStdout: true, CaptureStderr: true, Quiet: true,
	})
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "blkid", Args: []string{"-p", "-s", "UUID", "-o", "value", fs.Device},
		CaptureStdout: true, CaptureStderr: true, Quiet: true,
	})
	if err != nil {
		return ""
	}
	uuid := strings.TrimSpace(string(res.Stdout))
	if uuid != "" && logger != nil {
		logger.Warn(fmt.Sprintf("storage result has no UUID for %s (%s); recovered %s from blkid", fs.Mount, fs.Device, uuid))
	}
	return uuid
}

func dumpAndPassFor(fs, mount string) (int, int) {
	switch fs {
	case "ext2", "ext3", "ext4":
		if mount == "/" {
			return 1, 1
		}
		return 1, 2
	default:
		return 0, 0
	}
}

// fstabAlreadyHasMount checks whether any existing line mentions
// `mount` as a whitespace-bounded token. Matches the
// `grep -qsE "(^|[[:space:]])${grep_target}([[:space:]]|$)"` pattern.
func fstabAlreadyHasMount(existing []string, mount string) bool {
	re := regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(mount) + `(\s|$)`)
	for _, line := range existing {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func mountKey(fs result.Filesystem) string {
	if fs.Mount == "swap" {
		return "swap"
	}
	return fs.Mount
}

func readExistingFstab(path string) []string {
	f, err := os.Open(path) //nolint:gosec // sysroot path
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// bytes serialises existing fstab content back to a byte slice with
// trailing newlines per line.
func bytes(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
