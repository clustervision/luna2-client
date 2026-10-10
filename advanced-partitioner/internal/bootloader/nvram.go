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

// trailingEntryRE matches entries that must never sit in front of our disk
// entry. Two different kinds, one consequence:
//
//   - The firmware's own UI -- shell, setup, boot menu, vendor diagnostics.
//     These do not fall through. An empty CD-ROM fails and firmware moves to the
//     next entry; a shell SUCCEEDS and the machine sits at a prompt. One ahead of
//     our entry strands the node rather than booting it.
//   - A foreign OS boot manager left behind by a previous install. It would boot
//     that OS instead of the one we just provisioned.
//
// The match is deliberately broad, because the two errors are not equal: a false
// positive only moves some entry behind ours, costing a boot position, while a
// false negative leaves a trapping or wrong-OS entry in front of the disk. So
// "Linux Boot Manager" (systemd-boot) and "Windows Boot Manager" are caught on
// purpose -- on a node TrinityX has just provisioned, ours should win.
var trailingEntryRE = regexp.MustCompile(
	`(?i)(shell|setup|boot\s*menu|device\s*manager|diagnostic|firmware|boot\s*manager)`)

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
// before re-creation and to find the entries to move to the end of
// BootOrder.
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

// bootLabelsFromOutput maps each boot-num in the listing to its label, so the
// reorder can classify entries. A boot-num present in BootOrder but absent from
// the listing (a stale reference) simply has no label and is treated as an
// ordinary device -- it keeps its place rather than being promoted or demoted on
// a guess.
func bootLabelsFromOutput(efibootmgrOutput string) map[string]string {
	labels := map[string]string{}
	for _, line := range strings.Split(efibootmgrOutput, "\n") {
		if m := bootEntryRE.FindStringSubmatch(line); m != nil {
			labels[m[1]] = m[2]
		}
	}
	return labels
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
	// disk (from storage-result esp_partition). 0 (a result that did not
	// record it, e.g. a mount-existing layout) means detect it per disk
	// (detectESPPartition), and only when that finds nothing assume 1.
	ESPPartition int
	// ESPDevice is the /boot/efi filesystem's device from the storage result
	// (a partition or its md mirror); it disambiguates the detection.
	ESPDevice   string
	SnapshotDir string // optional; if non-empty, efibootmgr -v captures are written here
	// BootOrder is "first" or "last" (empty = last), from the os set's
	// bootloader key.
	BootOrder string
}

// BootOrderNotHonouredMarker names the file RegisterUEFIEntries leaves in the
// snapshot (runtime) directory when a "first" request survived the efibootmgr
// write but not the read-back. The installer summary reads it to report what
// the firmware actually did; nothing else depends on it.
const BootOrderNotHonouredMarker = "boot-order-not-honoured.txt"

// bootOrderFirst is the one BootOrder value that changes what this file does;
// anything else (including the empty default) orders the disk last.
const bootOrderFirst = "first"

// RegisterUEFIEntries deletes any stale TrinityX entries, creates one
// per boot disk pointing at the distro's signed shim (falling back to the
// architecture-specific removable loader only when no shim is on the ESP),
// then re-orders BootOrder to put the TrinityX entries LAST
// and clears BootNext. Mirrors bootloader_rhel_register_uefi_nvram_entries
// and its Debian sibling; the only family-specific bit was the
// snapshot file name, which is parameterised via SnapshotDir.
//
// The order is network, then other boot devices, then ours, then the firmware's
// own shell/menu entries. Not first: the node must keep asking the controller
// what to do.
// Luna already decides locally-vs-network per node (the netboot flag, and
// the iPXE chain that boots the local disk when that is what is wanted), and
// a disk entry ahead of the network entry takes that decision away from the
// controller and hands it to firmware -- the node then provisions once and
// never returns, so a changed install_mode or a re-provision needs someone at
// the BMC. Ordering the disk after the network entry keeps the redundancy
// this function exists for (one entry per boot disk, so a dead mirror leg
// still boots) while leaving the controller in charge. It also matches the
// shim ordering the boot-script path has always used, which appends its
// entry last.
//
// An operator who wants the opposite asks for it per set with
// `bootloader: first`, which puts the TrinityX group at the head instead. That
// is for a cluster whose nodes must come up without the controller; it hands
// the boot decision to firmware, so it is opt-in and never the default. Some
// firmware re-applies its own Setup boot priorities at POST and ignores the
// request; the read-back says so rather than claiming success.
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
			if opts.BootOrder == bootOrderFirst {
				opts.Logger.UserInfo("bootloader=first is ignored with register_nvram=no: there is no UEFI boot order to arrange")
			}
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
	// Register the distro's signed shim directly where we can find it, and only
	// fall back to the removable path when we cannot.
	//
	// The removable path IS shim's fallback.efi. Booting it makes fallback.efi
	// read <ESP>/EFI/<distro>/BOOT<arch>.CSV, create a SECOND TrinityX entry
	// pointing at the real shim, and put that entry FIRST -- which is its job,
	// it exists to rebuild lost NVRAM. Observed on a node: one local boot turned
	// a single entry into two and moved the new one to the head of BootOrder.
	// So registering the removable path costs us both properties this function
	// is trying to hold: exactly one entry per disk, and the disk ordered behind
	// the network. Pointing at the shim means fallback.efi never runs on a normal
	// boot, so neither happens.
	//
	// It is also the entry Secure Boot wants: the shim is the signed binary, and
	// the removable path is not guaranteed to be in db.
	//
	// StageSignedShim runs before this (see rhel.go), so the shim is already on
	// the ESP by the time we look. The CSV is deliberately left in place: it is
	// the recovery path for a machine whose NVRAM has been cleared, and there
	// re-promoting itself is the correct behaviour.
	loader := fallback
	if shimLoader, ok := signedShimLoaderPath(opts.Sysroot, opts.Arch); ok {
		loader = shimLoader
	} else if opts.Logger != nil {
		opts.Logger.Warn("no signed shim found on the ESP; registering the removable " +
			"loader " + fallback + " instead -- shim's fallback will add its own boot entry " +
			"ahead of this one on the first local boot")
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
	// an assumption of the current layout builder rather than a fact), or is
	// read from the disk when the result did not record it.
	for _, disk := range opts.BootDevices {
		espPart := opts.ESPPartition
		if espPart <= 0 {
			espPart = detectESPPartition(ctx, opts.Runner, disk, opts.ESPDevice)
			switch {
			case espPart > 0 && opts.Logger != nil:
				opts.Logger.Info(fmt.Sprintf("ESP partition number not in the storage result; detected partition %d on %s", espPart, disk))
			case espPart <= 0:
				espPart = 1
				if opts.Logger != nil {
					opts.Logger.Warn("ESP partition number neither in the storage result nor detectable on " + disk +
						" (no EF00 partition, none mounted at /boot/efi); assuming partition 1")
				}
			}
		}
		label := fmt.Sprintf("%s %s", BootloaderID, filepath.Base(disk))
		if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
			Path: eb, Chroot: opts.Sysroot,
			Args: []string{
				"--create", "--disk", disk, "--part", fmt.Sprintf("%d", espPart),
				"--label", label, "--loader", loader,
			},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			return fmt.Errorf("efibootmgr --create %s: %w", disk, err)
		}
	}

	// 3. Clear BootNext (matches the bash flow).
	clearBootNext(ctx, opts.Runner, opts.Sysroot)
	maybeSnapshot(ctx, opts.Runner, opts.Sysroot, opts.SnapshotDir, "efibootmgr-after-create.txt")

	// 4. Reorder BootOrder so the new TrinityX entries come last.
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
	// Three classes, each keeping its existing relative order:
	//   network  -- must stay ahead of us; it is how the controller keeps control
	//   device   -- CD-ROM, other disks: legitimate to prefer, and they fall
	//               through harmlessly when empty
	//   trailing -- shells, menus, foreign OS managers: must stay behind us
	// With no trailing entries the list simply ends with ours, which is the
	// intended fallback rather than a special case.
	labels := bootLabelsFromOutput(current)
	var network, device, trailing []string
	for _, n := range order {
		if _, ok := inTrinity[n]; ok {
			continue
		}
		switch {
		case pxeAllowRE.MatchString(labels[n]):
			network = append(network, n)
		case trailingEntryRE.MatchString(labels[n]):
			trailing = append(trailing, n)
		default:
			device = append(device, n)
		}
	}
	var newOrder []string
	if opts.BootOrder == bootOrderFirst {
		newOrder = append(newOrder, trinity...)
		newOrder = append(newOrder, network...)
		newOrder = append(newOrder, device...)
	} else {
		newOrder = append(newOrder, network...)
		newOrder = append(newOrder, device...)
		newOrder = append(newOrder, trinity...)
	}
	newOrder = append(newOrder, trailing...)
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

	// 5. A disk-first request is a request: report what the firmware kept.
	if opts.BootOrder == bootOrderFirst {
		reportDiskFirstReadBack(ctx, opts)
	}

	// 6. Validate that TrinityX is registered and reachable from BootOrder.
	return validateTrinityRegistered(ctx, opts.Runner, opts.Sysroot)
}

// reportDiskFirstReadBack re-reads BootOrder after the reorder and, when a
// TrinityX entry is not at the head, warns and leaves the marker the installer
// summary reads. Not an error: the node still boots, and the firmware is
// entitled to its own priorities -- the install must not be failed for it.
// A failed read is left to validateTrinityRegistered, which reports it next.
func reportDiskFirstReadBack(ctx context.Context, opts RegisterUEFIEntriesOptions) {
	out, err := runEFIBootmgr(ctx, opts.Runner, opts.Sysroot)
	if err != nil {
		return
	}
	order, _ := bootOrderFromOutput(out)
	if len(order) == 0 {
		return
	}
	head := bootLabelsFromOutput(out)[order[0]]
	if strings.Contains(head, BootloaderID) {
		return
	}
	if opts.Logger != nil {
		opts.Logger.Warn(fmt.Sprintf(
			"bootloader=first was requested but the firmware did not keep it: BootOrder now starts with %q -- "+
				"some AMI and Supermicro firmware re-applies its Setup boot priorities at POST; "+
				"put the disk first in the node's BIOS boot menu to make it stick", head))
	}
	if opts.SnapshotDir == "" {
		return
	}
	if err := os.MkdirAll(opts.SnapshotDir, 0o750); err != nil {
		return
	}
	//nolint:gosec // diagnostic marker in operator-controlled dir
	_ = os.WriteFile(filepath.Join(opts.SnapshotDir, BootOrderNotHonouredMarker),
		[]byte(strings.Join(order, ",")+"\n"), 0o640)
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

// validateTrinityRegistered returns an error when registration did not leave a
// usable TrinityX entry: none created, none reachable from BootOrder, or a
// BootNext still pinned over it.
//
// It deliberately does NOT assert the entry is first. The disk entry is ordered
// last so the controller keeps deciding how the node boots (see
// RegisterUEFIEntries), so "first" would now fail on every correct install. What
// still has to hold is that the entry exists and firmware can reach it -- an
// entry absent from BootOrder is invisible no matter where the disk sits.
func validateTrinityRegistered(ctx context.Context, runner executor.Runner, sysroot string) error {
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
	inOrder := map[string]struct{}{}
	for _, n := range order {
		inOrder[n] = struct{}{}
	}
	reachable := false
	for _, t := range trinity {
		if _, ok := inOrder[t]; ok {
			reachable = true
			break
		}
	}
	if !reachable {
		return fmt.Errorf(
			"no %s UEFI boot entry appears in BootOrder (%s): firmware cannot reach the local boot path",
			BootloaderID, strings.Join(order, ","))
	}
	if bootNext != "" {
		return fmt.Errorf("UEFI BootNext is still set after %s registration", BootloaderID)
	}
	return nil
}

// signedShimLoaderPath returns the EFI-style path of the distro's signed shim on
// the ESP (e.g. `\EFI\rocky\shimx64.efi`), and whether one was found. Reuses the
// same discovery StageSignedShim uses, so registration and staging cannot
// disagree about where the shim is.
func signedShimLoaderPath(sysroot, arch string) (string, bool) {
	shimName, _, err := archShimAndGrub(arch)
	if err != nil {
		return "", false
	}
	sel, err := findSignedShim(sysroot, arch)
	if err != nil {
		return "", false
	}
	return `\EFI\` + filepath.Base(sel.dir) + `\` + shimName, true
}
