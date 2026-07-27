package bootloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// BindMountSysroot recursively bind-mounts /dev, /proc, /sys, /run
// into sysroot so that chroot-driven bootloader commands
// (dracut, grub2-mkconfig / update-grub, efibootmgr — and on BIOS,
// grub2-install / grub-install) see a complete environment.
//
// --rbind is required (vs plain --bind) so that nested submounts come
// along. In particular efivarfs is mounted on /sys/firmware/efi/efivars
// as a separate filesystem; with --bind it stays at the host's
// namespace and the chroot sees an empty sysfs directory, so
// efibootmgr writes go nowhere and TrinityX NVRAM entries never reach
// real hardware NVRAM. /dev also relies on this (devpts, mqueue,
// hugepages, shm submounts).
//
// Each rbind is then marked rslave so propagation flows one-way (host
// to chroot, never back). Without this, the host's mounts inherit the
// `shared` propagation typical of systemd and a subsequent
// `umount -R /sysroot/sys` would tear down the host's
// /sys/firmware/efi/efivars too, breaking the installer environment
// after finalise returns.
//
// findmntMatch checks whether a target is already mounted; pass nil to
// run the real findmnt via runner. Tests inject a stub.
func BindMountSysroot(ctx context.Context, runner executor.Runner, sysroot string, isMounted func(string) bool) error {
	if isMounted == nil {
		isMounted = func(target string) bool {
			_, err := runner.Run(ctx, executor.CommandSpec{
				Path: "findmnt", Args: []string{"-rn", "-M", target},
				CaptureStdout: true, CaptureStderr: true,
				Quiet: true, // "not mounted" is the expected probe answer
			})
			return err == nil
		}
	}
	for _, src := range []string{"/dev", "/proc", "/sys", "/run"} {
		target := filepath.Join(sysroot, src)
		if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // sysroot defaults
			return fmt.Errorf("mkdir %s: %w", target, err)
		}
		if isMounted(target) {
			continue
		}
		if _, err := runner.Run(ctx, executor.CommandSpec{
			Path: "mount", Args: []string{"--rbind", src, target},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			return fmt.Errorf("mount --rbind %s -> %s: %w", src, target, err)
		}
		if _, err := runner.Run(ctx, executor.CommandSpec{
			Path: "mount", Args: []string{"--make-rslave", target},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			return fmt.Errorf("mount --make-rslave %s: %w", target, err)
		}
	}
	return nil
}

// CleanupBindMounts undoes BindMountSysroot in reverse order, falling
// back to a lazy unmount on failure. Uses `umount -R` first so the
// nested submounts brought in by --rbind come down with the parent;
// a plain umount would fail on the parent while children are still
// mounted.
func CleanupBindMounts(ctx context.Context, runner executor.Runner, sysroot string, isMounted func(string) bool) {
	if isMounted == nil {
		isMounted = func(target string) bool {
			_, err := runner.Run(ctx, executor.CommandSpec{
				Path: "findmnt", Args: []string{"-rn", "-M", target},
				CaptureStdout: true, CaptureStderr: true,
				Quiet: true, // "not mounted" is the expected probe answer
			})
			return err == nil
		}
	}
	for _, src := range []string{"/run", "/sys", "/proc", "/dev"} {
		target := filepath.Join(sysroot, src)
		if !isMounted(target) {
			continue
		}
		if _, err := runner.Run(ctx, executor.CommandSpec{
			Path: "umount", Args: []string{"-R", target},
			CaptureStdout: true, CaptureStderr: true,
		}); err == nil {
			continue
		}
		// Lazy fallback. Best-effort; failure here logs only.
		_, _ = runner.Run(ctx, executor.CommandSpec{
			Path: "umount", Args: []string{"-l", target},
			CaptureStdout: true, CaptureStderr: true,
		})
	}
}
