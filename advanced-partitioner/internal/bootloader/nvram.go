package bootloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// BootloaderID is the EFI bootloader-id stamp used everywhere. Mirrors
// LPART_BOOTLOADER_ID in /root/NEWTREE/lib/common.sh.
const BootloaderID = "TrinityX"

// efibootmgrChrootPath returns the in-image absolute path to efibootmgr,
// resolved across distro layouts — RHEL ships it in /usr/sbin, Ubuntu/Debian in
// /usr/bin — or "" when the image has no efibootmgr. The existence check and the
// chroot exec MUST use the SAME resolution: checking /usr/bin but executing the
// hardcoded /usr/sbin yields exit 127 on Ubuntu (HW-found on node001, the
// ubuntu disk install). Resolved against the OS image (sysroot), where the
// chroot runs it.
func efibootmgrChrootPath(sysroot string) string {
	for _, rel := range []string{"usr/sbin/efibootmgr", "usr/bin/efibootmgr", "sbin/efibootmgr", "bin/efibootmgr"} {
		if _, err := os.Stat(filepath.Join(sysroot, rel)); err == nil {
			return "/" + rel
		}
	}
	return ""
}

// pxeAllowlist mirrors the bash awk allow-pass in
// clear_uefi_nvram_entries: "anything matching PXE|Network|Luna|iPXE
// must NOT be deleted". This is the safety net that keeps the network
// boot path alive when an operator runs the clear_uefi_nvram path on
// the tmpfs OS.
var pxeAllowRE = regexp.MustCompile(`PXE|Network|Luna|iPXE`)

// bootEntryRE matches a line like "Boot0001* TrinityX sda" or
// "Boot0042  Linux Boot Manager". The first group is the four-hex
// boot-num; the asterisk that means "active" is stripped from the
// match.
var bootEntryRE = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})\*?\s+(.*)$`)

// bootOrderRE matches the "BootOrder: 0001,0042,..." line.
var bootOrderRE = regexp.MustCompile(`^BootOrder:\s*(.+)\s*$`)

// bootNextRE matches the "BootNext: 0042" line.
var bootNextRE = regexp.MustCompile(`^BootNext:\s*(.+)\s*$`)

// ClearUEFINVRAMEntries deletes UEFI boot entries whose label does NOT
// match PXE|Network|Luna|iPXE. Used by tmpfs-root + clear_uefi_nvram=true
// to wipe stale local-install entries before reboot. Mirrors
// lib/bootloader_common.sh::clear_uefi_nvram_entries.
func ClearUEFINVRAMEntries(ctx context.Context, runner executor.Runner, sysroot, firmwareMode string) error {
	if firmwareMode != "uefi" {
		return nil
	}
	eb := efibootmgrChrootPath(sysroot)
	if eb == "" {
		return fmt.Errorf(
			"clear_uefi_nvram requested but efibootmgr is not available in %s -- "+
				"osimage must be installed before calling lpart-bootloader-finalise",
			sysroot)
	}
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: eb, Chroot: sysroot,
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return fmt.Errorf("efibootmgr list: %w", err)
	}
	for _, num := range parseClearableBootNums(string(res.Stdout)) {
		// run_chroot_cmd swallows non-zero per-entry; we mirror that
		// (one broken delete shouldn't block the others).
		_, _ = runner.Run(ctx, executor.CommandSpec{
			Path: eb, Chroot: sysroot,
			Args:          []string{"--bootnum", num, "--delete-bootnum"},
			CaptureStdout: true, CaptureStderr: true,
		})
	}
	return nil
}

// parseClearableBootNums returns the four-hex boot numbers whose label
// is NOT in the PXE/Network/Luna/iPXE allowlist. Exported as an
// internal helper for tests.
func parseClearableBootNums(efibootmgrOutput string) []string {
	var out []string
	for _, line := range strings.Split(efibootmgrOutput, "\n") {
		m := bootEntryRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		num, label := m[1], m[2]
		if pxeAllowRE.MatchString(label) {
			continue
		}
		out = append(out, num)
	}
	return out
}

// trinityNumsFromOutput pulls every boot-num whose label contains
// BootloaderID (i.e. "TrinityX"). Used both to delete stale entries
// before re-creation and to find the entries to promote to the front
// of BootOrder.
func trinityNumsFromOutput(efibootmgrOutput string) []string {
	var out []string
	for _, line := range strings.Split(efibootmgrOutput, "\n") {
		m := bootEntryRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		num, label := m[1], m[2]
		if strings.Contains(label, BootloaderID) {
			out = append(out, num)
		}
	}
	return out
}

// bootOrderFromOutput returns the current BootOrder list (comma-
// separated boot-nums) and the BootNext value, if any.
func bootOrderFromOutput(out string) (order []string, bootNext string) {
	for _, line := range strings.Split(out, "\n") {
		if m := bootOrderRE.FindStringSubmatch(line); m != nil {
			for _, n := range strings.Split(strings.TrimSpace(m[1]), ",") {
				n = strings.TrimSpace(n)
				if n != "" {
					order = append(order, n)
				}
			}
		}
		if m := bootNextRE.FindStringSubmatch(line); m != nil {
			bootNext = strings.TrimSpace(m[1])
		}
	}
	return
}

// RegisterUEFIEntriesOptions configures RegisterUEFIEntries.
type RegisterUEFIEntriesOptions struct {
	Runner        executor.Runner
	Logger        *txlog.Logger
	Sysroot       string
	Arch          string
	BootDevices   []string
	RegisterNVRAM string // "auto"|"yes"|"no"
	// ESPPartition is the GPT partition number of the ESP on each boot
	// disk (from storage-result esp_partition); 0 falls back to 1.
	ESPPartition int
	SnapshotDir  string // optional; if non-empty, efibootmgr -v captures are written here
}

// RegisterUEFIEntries deletes any stale TrinityX entries, creates one
// per boot disk with the architecture-specific removable fallback
// loader, then re-orders BootOrder to put the TrinityX entries first
// and clears BootNext. Mirrors bootloader_rhel_register_uefi_nvram_entries
// and its Debian sibling; the only family-specific bit was the
// snapshot file name, which is parameterised via SnapshotDir.
//
// Returns nil and skips entirely when RegisterNVRAM == "no", when the
// sysroot doesn't expose /sys/firmware/efi/efivars, or when efibootmgr
// is not installed. Mirrors the bash early-return ladder.
//
//nolint:gocyclo // matches the bash function flow section-by-section
func RegisterUEFIEntries(ctx context.Context, opts RegisterUEFIEntriesOptions) error {
	if opts.RegisterNVRAM == "no" {
		if opts.Logger != nil {
			opts.Logger.Info("skipping UEFI NVRAM registration because register_nvram=no")
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(opts.Sysroot, "sys", "firmware", "efi", "efivars")); err != nil {
		// This skip must be LOUD: it means NO NVRAM boot entries will be
		// created, so the node boots only via the removable fallback loader
		// and the per-mirror-leg failover entries (the verified redundancy
		// property) silently do not exist. Was a silent `return nil`
		// (FTA v2 BE-W3 / MCS-17, barrier NB-5).
		if opts.Logger != nil {
			opts.Logger.Warn("UEFI NVRAM registration SKIPPED: " +
				"efivars not exposed under the sysroot -- no NVRAM boot entries will be created; " +
				"the node will rely on the removable fallback loader only")
		}
		return nil
	}
	eb := efibootmgrChrootPath(opts.Sysroot)
	if eb == "" {
		if opts.Logger != nil {
			opts.Logger.Info("skipping UEFI NVRAM registration: no efibootmgr in image")
		}
		return nil
	}
	fallback, err := ArchToUEFIFallbackLoader(opts.Arch)
	if err != nil {
		return err
	}

	// 1. Delete stale TrinityX entries first.
	current, err := runEFIBootmgr(ctx, opts.Runner, opts.Sysroot)
	if err != nil {
		return err
	}
	for _, num := range trinityNumsFromOutput(current) {
		_, _ = opts.Runner.Run(ctx, executor.CommandSpec{
			Path: eb, Chroot: opts.Sysroot,
			Args:          []string{"--bootnum", num, "--delete-bootnum"},
			CaptureStdout: true, CaptureStderr: true,
		})
	}

	// 2. Create one TrinityX entry per boot disk. The ESP partition number
	// comes from the storage result (FTA D11 — it used to be hardcoded 1,
	// an assumption of the current layout builder rather than a fact).
	espPart := opts.ESPPartition
	if espPart <= 0 {
		espPart = 1
	}
	for _, disk := range opts.BootDevices {
		label := fmt.Sprintf("%s %s", BootloaderID, filepath.Base(disk))
		if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
			Path: eb, Chroot: opts.Sysroot,
			Args: []string{
				"--create", "--disk", disk, "--part", fmt.Sprintf("%d", espPart),
				"--label", label, "--loader", fallback,
			},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			return fmt.Errorf("efibootmgr --create %s: %w", disk, err)
		}
	}

	// 3. Clear BootNext (matches the bash flow).
	clearBootNext(ctx, opts.Runner, opts.Sysroot)
	maybeSnapshot(ctx, opts.Runner, opts.Sysroot, opts.SnapshotDir, "efibootmgr-after-create.txt")

	// 4. Reorder BootOrder so the new TrinityX entries come first.
	current, err = runEFIBootmgr(ctx, opts.Runner, opts.Sysroot)
	if err != nil {
		return err
	}
	order, _ := bootOrderFromOutput(current)
	if len(order) == 0 {
		return nil
	}
	trinity := trinityNumsFromOutput(current)
	if len(trinity) == 0 {
		return nil
	}
	// Sort trinity reverse so re-created entries are tried last among
	// the trinity group (matches the bash `sort -r` step).
	for i := 0; i < len(trinity)/2; i++ {
		trinity[i], trinity[len(trinity)-1-i] = trinity[len(trinity)-1-i], trinity[i]
	}

	inTrinity := map[string]struct{}{}
	for _, t := range trinity {
		inTrinity[t] = struct{}{}
	}
	var residual []string
	for _, n := range order {
		if _, ok := inTrinity[n]; ok {
			continue
		}
		residual = append(residual, n)
	}
	newOrder := append([]string(nil), trinity...)
	newOrder = append(newOrder, residual...)
	joined := strings.Join(newOrder, ",")
	if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
		Path: eb, Chroot: opts.Sysroot,
		Args:          []string{"--bootorder", joined},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("efibootmgr --bootorder %s: %w", joined, err)
	}
	clearBootNext(ctx, opts.Runner, opts.Sysroot)
	maybeSnapshot(ctx, opts.Runner, opts.Sysroot, opts.SnapshotDir, "efibootmgr-after-bootorder.txt")

	// 5. Validate that TrinityX is first in BootOrder.
	return validateTrinityFirst(ctx, opts.Runner, opts.Sysroot)
}

func runEFIBootmgr(ctx context.Context, runner executor.Runner, sysroot string) (string, error) {
	eb := efibootmgrChrootPath(sysroot)
	if eb == "" {
		return "", fmt.Errorf("efibootmgr not found in %s", sysroot)
	}
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: eb, Chroot: sysroot,
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("efibootmgr: %w", err)
	}
	return string(res.Stdout), nil
}

func clearBootNext(ctx context.Context, runner executor.Runner, sysroot string) {
	out, err := runEFIBootmgr(ctx, runner, sysroot)
	if err != nil {
		return
	}
	if _, bn := bootOrderFromOutput(out); bn != "" {
		_, _ = runner.Run(ctx, executor.CommandSpec{
			Path: efibootmgrChrootPath(sysroot), Chroot: sysroot,
			Args:          []string{"--delete-bootnext"},
			CaptureStdout: true, CaptureStderr: true,
		})
	}
}

func maybeSnapshot(ctx context.Context, runner executor.Runner, sysroot, dir, name string) {
	if dir == "" {
		return
	}
	out, err := runEFIBootmgr(ctx, runner, sysroot)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	//nolint:gosec // diagnostic capture in operator-controlled dir
	_ = os.WriteFile(filepath.Join(dir, name), []byte(out), 0o640)
}

// validateTrinityFirst returns an error when the first entry in
// BootOrder is not a TrinityX entry, mirroring
// bootloader_uefi_validate_trinityx_first.
func validateTrinityFirst(ctx context.Context, runner executor.Runner, sysroot string) error {
	out, err := runEFIBootmgr(ctx, runner, sysroot)
	if err != nil {
		return err
	}
	trinity := trinityNumsFromOutput(out)
	if len(trinity) == 0 {
		return fmt.Errorf("no %s UEFI NVRAM boot entry found after registration", BootloaderID)
	}
	order, bootNext := bootOrderFromOutput(out)
	if len(order) == 0 {
		return fmt.Errorf("no UEFI BootOrder found after %s registration", BootloaderID)
	}
	first := order[0]
	inTrinity := map[string]struct{}{}
	for _, t := range trinity {
		inTrinity[t] = struct{}{}
	}
	if _, ok := inTrinity[first]; !ok {
		return fmt.Errorf(
			"first UEFI BootOrder entry is not %s: first=%s, expected a %s local boot entry first",
			BootloaderID, first, BootloaderID)
	}
	if bootNext != "" {
		return fmt.Errorf("UEFI BootNext is still set after %s registration", BootloaderID)
	}
	return nil
}
