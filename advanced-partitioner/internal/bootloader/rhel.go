package bootloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// rhelAdapter ports lib/bootloader_rhel.sh. The family covers RHEL,
// Rocky, Alma, CentOS Stream, and Fedora — all of which ship dracut,
// grub2-mkconfig, and efibootmgr (used here), plus grub2-install
// (used only on BIOS — the UEFI path goes through StageSignedShim).
// Every external command runs through chroot /sysroot because the
// host installer environment does not have these binaries.
type rhelAdapter struct{ deps AdapterDeps }

func (a *rhelAdapter) Family() Family { return FamilyRHEL }

// Prepare validates the required tools and mount points are in place.
// Mirrors bootloader_rhel_prepare.
func (a *rhelAdapter) Prepare(_ context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	if !anyExist(sysroot, "usr/bin/dracut", "usr/sbin/dracut") {
		return fmt.Errorf("missing required command: dracut")
	}
	if !anyExist(sysroot, "usr/sbin/grub2-mkconfig", "usr/bin/grub2-mkconfig") {
		return fmt.Errorf("missing required command: grub2-mkconfig")
	}
	if len(a.deps.BootContext.StorageResult.BootDevices) == 0 {
		return fmt.Errorf("storage-result boot_devices must contain at least one disk")
	}
	if a.deps.BootContext.FirmwareMode == "uefi" {
		if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi")); err != nil {
			return fmt.Errorf("missing EFI mount directory: %s/boot/efi", sysroot)
		}
	}
	return nil
}

// RegenerateInitramfs finds the kernel version under /sysroot/lib/modules
// and runs `dracut -f --omit luna --kver <version>` via chroot.
// Mirrors bootloader_rhel_regenerate_initramfs.
func (a *rhelAdapter) RegenerateInitramfs(ctx context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	kver, err := detectKernelVersion(sysroot)
	if err != nil {
		return err
	}
	if _, err := a.deps.Runner.Run(ctx, executor.CommandSpec{
		Path: "/usr/bin/env", Chroot: sysroot,
		Args:          []string{"dracut", "-f", "--omit", "luna", "--kver", kver},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("dracut -f --omit luna --kver %s: %w", kver, err)
	}
	return nil
}

// detectKernelVersion walks /sysroot/lib/modules/* and returns the version
// directory to build the initramfs for.
//
// "last wins" is NOT safe on its own: el10 installs kernel-debug-core and
// kernel-debug-modules* alongside the real kernel, so the installed rootfs holds
// TWO trees --
//
//	6.12.0-211.34.1.el10_2.x86_64
//	6.12.0-211.34.1.el10_2.x86_64+debug
//
// os.ReadDir returns them sorted, so the debug tree came last and won; the install
// then died at postscript with `dracut -f --omit luna --kver <...+debug>: exit 1`.
// rocky8/rocky9 ship a single kernel, which is why this only surfaced on el10.
// Prefer a non-debug tree; fall back to a debug-only one rather than failing outright
// (an image that genuinely ships ONLY a debug kernel should still install).
func detectKernelVersion(sysroot string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(sysroot, "lib", "modules"))
	if err != nil {
		return "", fmt.Errorf("read %s/lib/modules: %w", sysroot, err)
	}
	var picked, debugOnly string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if isDebugKernelDir(e.Name()) {
			debugOnly = e.Name()
			continue
		}
		picked = e.Name() // last non-debug wins
	}
	if picked == "" {
		picked = debugOnly
	}
	if picked == "" {
		return "", fmt.Errorf("unable to determine target kernel version in %s/lib/modules", sysroot)
	}
	return picked, nil
}

// isDebugKernelDir reports whether a /lib/modules entry is a debug kernel variant.
// RedHat-family debug kernels carry a "+debug" suffix on the release
// (e.g. 6.12.0-211.34.1.el10_2.x86_64+debug).
func isDebugKernelDir(name string) bool {
	return strings.HasSuffix(name, "+debug")
}

// Install writes /etc/default/grub, then runs the UEFI install. BIOS/legacy
// boot is unsupported (see the firmware gate in lpart-storage-prepare):
// refuse any non-UEFI firmware rather than attempt a degraded BIOS install.
func (a *rhelAdapter) Install(ctx context.Context) error {
	if err := a.writeGrubDefault(); err != nil {
		return err
	}
	if a.deps.BootContext.FirmwareMode != "uefi" {
		return fmt.Errorf("unsupported firmware mode %q: lpart requires UEFI (BIOS/legacy boot is not supported)", a.deps.BootContext.FirmwareMode)
	}
	return a.installUEFI(ctx)
}

// writeGrubDefault writes /etc/default/grub. Mirrors
// bootloader_rhel_write_grub_default.
func (a *rhelAdapter) writeGrubDefault() error {
	path := filepath.Join(a.deps.BootContext.Sysroot, "etc", "default", "grub")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // sysroot defaults
		return fmt.Errorf("mkdir parent of %s: %w", path, err)
	}
	lines := []string{
		"GRUB_TIMEOUT=5",
		"GRUB_DEFAULT=saved",
		"GRUB_DISABLE_SUBMENU=true",
		"GRUB_DISABLE_OS_PROBER=true",
		"GRUB_DISABLE_RECOVERY=true",
		`GRUB_TERMINAL_OUTPUT="console"`,
		GrubCmdlineValue(a.deps.BootContext.KernelArgs),
	}
	body := strings.Join(lines, "\n") + "\n"
	//nolint:gosec // /etc/default/grub default perms
	return os.WriteFile(path, []byte(body), 0o644)
}

func (a *rhelAdapter) installUEFI(ctx context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	if err := StageSignedShim(StageSignedShimOpts{
		FS:            a.deps.FS,
		Sysroot:       sysroot,
		Arch:          a.deps.BootContext.Architecture,
		StorageResult: a.deps.BootContext.StorageResult,
		GrubDirName:   "grub2",
		// Family selects the signed-pair pre-stage source row: RHEL images
		// ship a populated EFI/<distro>/ (empty row); SUSE — which reuses
		// this whole flow via the embedded adapter — stages from
		// /usr/lib64/efi (FamilySUSE row).
		Family: a.deps.BootContext.Family,
		Logger: a.deps.Logger,
	}); err != nil {
		return err
	}

	if err := RegisterUEFIEntries(ctx, RegisterUEFIEntriesOptions{
		Runner:        a.deps.Runner,
		Logger:        a.deps.Logger,
		Sysroot:       sysroot,
		Arch:          a.deps.BootContext.Architecture,
		BootDevices:   a.deps.BootContext.StorageResult.BootDevices,
		RegisterNVRAM: a.deps.BootContext.RegisterNVRAM,
		ESPPartition:  a.deps.BootContext.StorageResult.ESPPartition,
		SnapshotDir:   a.deps.SnapshotDir,
		BootOrder:     a.deps.BootContext.BootOrder,
	}); err != nil {
		return err
	}

	a.normalizeBLSEntries()
	if _, err := a.deps.Runner.Run(ctx, executor.CommandSpec{
		Path: "/usr/bin/env", Chroot: sysroot,
		Args:          []string{"grub2-mkconfig", "-o", "/boot/grub2/grub.cfg"},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("grub2-mkconfig: %w", err)
	}
	a.makeGrubCfgDegradedSafe()
	return nil
}

// normalizeBLSEntries repairs the BLS entry files
// (/boot/loader/entries/*.conf) extracted from the OS image so the node
// can actually boot them. It fixes two corruptions, both produced when
// `kernel-install` ran while the image was chrooted at its build path
// (e.g. /trinity/images/compute-image) — typically for the newest
// kernel added by a post-build `dnf update`, which is also the kernel
// GRUB boots by default:
//
//   - `linux`/`initrd` carry the build-path prefix
//     (`/trinity/images/compute-image/boot/vmlinuz-*`) instead of a path
//     relative to the boot filesystem. GRUB then can't find the kernel
//     ("file not found" -> "you need to load the kernel first" -> rescue).
//     We keep only the segment from the last `/boot/` onward, re-prefixed
//     for the node's layout: "/" when /boot is its own filesystem (the
//     path is relative to that filesystem), "/boot/" otherwise. A path
//     with no `/boot/` segment is left untouched. Trailing tokens such as
//     `$tuned_initrd` are preserved.
//   - `options` carries a build-time `root=`/`rd.lvm.lv=`/`resume=`
//     pointing at the build host's volume group (e.g. vg_rhel). Left in
//     place these override the correct kernelopts that grub2-mkconfig
//     probes on the node (root=/dev/mapper/<node-vg>-root), so the node
//     boots looking for a VG that does not exist. We strip those tokens;
//     a clean entry (whose `options` is just `$tuned_params`) carries
//     none, so on it this is a no-op and the entry inherits the probed
//     kernelopts exactly as before.
//
// Generalises bootloader_rhel_normalize_bls_for_separate_boot, which only
// rewrote a clean `/boot/` prefix and only when /boot was separate.
func (a *rhelAdapter) normalizeBLSEntries() {
	sysroot := a.deps.BootContext.Sysroot
	dir := filepath.Join(sysroot, "boot", "loader", "entries")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := "/boot/"
	if a.hasBootMount() {
		prefix = "/"
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path) //nolint:gosec // sysroot file
		if err != nil {
			continue
		}
		var out []string
		for _, line := range strings.Split(string(body), "\n") {
			switch {
			case strings.HasPrefix(line, "linux "), strings.HasPrefix(line, "initrd "):
				out = append(out, rewriteBLSPathLine(line, prefix))
			case strings.HasPrefix(line, "options "):
				out = append(out, stripBuildKernelOpts(line))
			default:
				out = append(out, line)
			}
		}
		//nolint:gosec // sysroot file, default perms
		_ = os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
	}
}

// rewriteBLSPathLine normalises the path token of a BLS `linux`/`initrd`
// line to the node's boot layout, preserving the directive and any
// trailing tokens (e.g. `$tuned_initrd`).
func rewriteBLSPathLine(line, prefix string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return line
	}
	out := fields[0] + " " + normalizeBootPath(fields[1], prefix)
	if len(fields) > 2 {
		out += " " + strings.Join(fields[2:], " ")
	}
	return out
}

// normalizeBootPath strips any prefix up to and including the last
// `/boot/` and re-prefixes the remainder. A path with no `/boot/`
// segment is returned unchanged.
func normalizeBootPath(p, prefix string) string {
	const marker = "/boot/"
	idx := strings.LastIndex(p, marker)
	if idx < 0 {
		return p
	}
	return prefix + p[idx+len(marker):]
}

// stripBuildKernelOpts removes device-specific tokens that a build-time
// kernel-install baked into a BLS `options` line (root=, rd.lvm.lv=,
// resume=). On a freshly installed node these always reference the build
// host's volume group and would override the kernelopts grub2-mkconfig
// probes on the node. Clean entries carry none of these tokens.
func stripBuildKernelOpts(line string) string {
	fields := strings.Fields(line) // fields[0] == "options"
	kept := fields[:1]
	for _, tok := range fields[1:] {
		if strings.HasPrefix(tok, "root=") ||
			strings.HasPrefix(tok, "rd.lvm.lv=") ||
			strings.HasPrefix(tok, "resume=") {
			continue
		}
		kept = append(kept, tok)
	}
	return strings.Join(kept, " ")
}

func (a *rhelAdapter) hasBootMount() bool {
	for _, fs := range a.deps.BootContext.StorageResult.Filesystems {
		if fs.Mount == "/boot" {
			return true
		}
	}
	return false
}

// Validate checks the bootloader install left the right files behind.
// Mirrors bootloader_rhel_validate.
func (a *rhelAdapter) Validate(_ context.Context) error {
	sysroot := a.deps.BootContext.Sysroot
	grubCfg := filepath.Join(sysroot, "boot", "grub2", "grub.cfg")
	if _, err := os.Stat(grubCfg); err != nil {
		return fmt.Errorf("missing boot file: %s", grubCfg)
	}
	fstab := filepath.Join(sysroot, "etc", "fstab")
	info, err := os.Stat(fstab)
	if err != nil {
		return fmt.Errorf("missing or empty %s: %w", fstab, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("missing or empty %s", fstab)
	}
	if a.deps.BootContext.FirmwareMode == "uefi" {
		efiDir := filepath.Join(sysroot, "boot", "efi", "EFI")
		if _, err := os.Stat(efiDir); err != nil {
			return fmt.Errorf("missing EFI directory under %s", efiDir)
		}
		trinity := filepath.Join(efiDir, BootloaderID)
		fallback := filepath.Join(efiDir, "BOOT")
		if _, err := os.Stat(trinity); err != nil {
			if _, err2 := os.Stat(fallback); err2 != nil {
				return fmt.Errorf("missing %s or removable EFI boot directory under %s",
					BootloaderID, efiDir)
			}
		}
	}
	return nil
}

// anyExist returns true if any of the given relative paths exists
// under sysroot.
func anyExist(sysroot string, rel ...string) bool {
	for _, r := range rel {
		if _, err := os.Stat(filepath.Join(sysroot, r)); err == nil {
			return true
		}
	}
	return false
}

// makeGrubCfgDegradedSafe rewrites the grub2-mkconfig output so the boot
// filesystem and root are located by plain filesystem UUID instead of by md
// array UUID (mduuid/…). On a metadata-1.0 RAID (the firmware-readable
// /boot/efi + /boot mirror), each member partition is directly readable as
// its filesystem, so a single surviving leg is found by a fs-uuid scan
// without GRUB having to assemble the (now degraded) array. GRUB's degraded
// md assembly is non-deterministic — it intermittently fails to resolve the
// mduuid and drops to the `grub>` rescue prompt — so a node that lost one
// mirror leg sometimes would not boot. Dropping the mduuid hints + the
// `set root/boot='mduuid/…'` lines forces the deterministic fs-uuid path.
//
// No-op on non-RAID layouts (grub2-mkconfig emits no mduuid references) and
// best-effort: a missing/unreadable grub.cfg just leaves it untouched.
func (a *rhelAdapter) makeGrubCfgDegradedSafe() {
	makeGrubCfgDegradedSafeAt(
		filepath.Join(a.deps.BootContext.Sysroot, "boot", "grub2", "grub.cfg"))
}

// makeGrubCfgDegradedSafeAt applies rewriteMduuidToFSUUID to every grub.cfg
// path that exists. Shared by the RHEL/SUSE and Debian adapters (BE-W5:
// Debian used to keep the mduuid-based grub.cfg — the non-deterministic
// degraded assembly — that the RHEL fix above removed). Best-effort: a
// missing/unreadable path is skipped, an unchanged body is not rewritten.
func makeGrubCfgDegradedSafeAt(paths ...string) {
	for _, path := range paths {
		body, err := os.ReadFile(path) //nolint:gosec // sysroot file
		if err != nil {
			continue
		}
		out := rewriteMduuidToFSUUID(string(body))
		if out == string(body) {
			continue
		}
		//nolint:gosec // grub.cfg default perms
		_ = os.WriteFile(path, []byte(out), 0o644)
	}
}

var (
	// `--hint='mduuid/…'` / `--hint-efi='mduuid/…'` etc. on a search line.
	mduuidHintRe = regexp.MustCompile(`\s*--hint(?:-[a-z]+)?='mduuid/[0-9a-fA-F]+'`)
	// a standalone `set root='mduuid/…'` / `set boot='mduuid/…'` line.
	setMduuidRe = regexp.MustCompile(`(?m)^[ \t]*set (?:root|boot)='mduuid/[0-9a-fA-F]+'[ \t]*\n`)
)

// rewriteMduuidToFSUUID strips the md-array hints/assignments from a grub.cfg
// so its `search --fs-uuid` lines resolve by scanning (which finds a raw
// metadata-1.0 member) rather than via the array device.
func rewriteMduuidToFSUUID(cfg string) string {
	cfg = setMduuidRe.ReplaceAllString(cfg, "")
	cfg = mduuidHintRe.ReplaceAllString(cfg, "")
	return cfg
}
