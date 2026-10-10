package bootloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// Every installed kernel gets its initramfs rebuilt, not just the one the
// installer picked. GRUB_DEFAULT=saved follows the image's saved_entry, and
// any entry can be chosen at the menu, so every bootable kernel's initramfs
// has to be built on THIS node: one left from the image host is hostonly (no
// driver for this node's md/LVM root) or still carries the luna module (back
// into the installer). The default entry is then set explicitly
// (setDefaultBootEntry) instead of being left to the image.

// bootableKernels returns every kernel release that has a module tree under
// /lib/modules AND a /boot/vmlinuz-<release>, newest first. A module
// directory alone is a leftover of an erased kernel package (see
// detectKernelVersion) and is not rebuilt.
func bootableKernels(sysroot string) []string {
	entries, err := os.ReadDir(filepath.Join(sysroot, "lib", "modules"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if fileExists(filepath.Join(sysroot, "boot", "vmlinuz-"+e.Name())) {
			out = append(out, e.Name())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return compareKernelVersions(out[i], out[j]) > 0 })
	return out
}

// defaultKernel picks the kernel the default boot entry starts. With
// preferRunning (RHEL/SUSE, where lpart sets saved_entry) that is the kernel
// the installer itself booted when it is one of the bootable kernels: Luna
// builds the node's boot kernel from the osimage, so this is the osimage's
// kernel -- the newest installed one unless the osimage pins an older
// kernelversion, which is a supported choice that must not be overridden.
// Otherwise (and on Debian, whose GRUB_DEFAULT=0 is the newest entry) it is
// the newest non-debug kernel, else the newest debug one.
func defaultKernel(kernels []string, preferRunning bool) string {
	if preferRunning {
		if k := runningKernelVersion(); k != "" {
			for _, c := range kernels {
				if c == k {
					return k
				}
			}
		}
	}
	if k := newestKernel(kernels, false); k != "" {
		return k
	}
	return newestKernel(kernels, true)
}

// dracutAllKernels runs `dracut -f --omit luna --kver <v>` for every bootable
// kernel, the default kernel first, and returns the default. The luna module is
// for the netboot install initramfs only, never the installed system's.
//
// A failure on the default kernel fails the step (the node would not boot); a
// failure on another kernel is a loud warning, because that kernel is only
// reached by picking it at the menu and failing the install for it would
// retry for ever on a broken leftover.
//
// When no kernel has both halves the old single-kernel choice
// (detectKernelVersion) is used, so this never refuses an image that
// installed before.
func dracutAllKernels(ctx context.Context, deps AdapterDeps, preferRunning bool) (string, error) {
	sysroot := deps.BootContext.Sysroot
	kernels := bootableKernels(sysroot)
	if len(kernels) == 0 {
		k, err := detectKernelVersion(sysroot)
		if err != nil {
			return "", imageErrorf("image-no-kernel", "%v", err)
		}
		kernels = []string{k}
	}
	def := defaultKernel(kernels, preferRunning)
	order := append([]string{def}, without(kernels, def)...)
	var failed []string
	for _, kver := range order {
		if _, err := deps.Runner.Run(ctx, executor.CommandSpec{
			Path: "/usr/bin/env", Chroot: sysroot,
			Args:          []string{"dracut", "-f", "--omit", "luna", "--kver", kver},
			CaptureStdout: true, CaptureStderr: true,
		}); err != nil {
			if kver == def {
				return "", fmt.Errorf("dracut -f --omit luna --kver %s: %w", kver, err)
			}
			failed = append(failed, kver)
			if deps.Logger != nil {
				deps.Logger.Warn(fmt.Sprintf(
					"dracut failed for the non-default kernel %s (%v): its initramfs is still the image's "+
						"and may not boot this node or may start the installer -- do not pick it at the boot menu",
					kver, err))
			}
		}
	}
	if deps.Logger != nil {
		deps.Logger.UserInfo("initramfs rebuilt for %d of %d kernel(s); default kernel %s",
			len(order)-len(failed), len(order), def)
		if msg := kernelNotNewestWarning(sysroot, def); msg != "" {
			deps.Logger.Warn(msg)
		}
	}
	return def, nil
}

func without(list []string, drop string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

// setDefaultBootEntry makes kver the saved default entry (GRUB_DEFAULT=saved,
// written by writeGrubDefault) with `grub2-editenv - set saved_entry=<id>`. The
// id is the BLS entry file name (el8+/fedora) or, for a classic grub.cfg, the
// menuentry id grub2-mkconfig gives the kernel. Without either there is no id
// to name and GRUB keeps its first entry, which grub2-mkconfig sorts newest
// first.
func setDefaultBootEntry(ctx context.Context, deps AdapterDeps, kver string) error {
	if kver == "" {
		return nil
	}
	sysroot := deps.BootContext.Sysroot
	id := blsEntryIDFor(sysroot, kver)
	if id == "" {
		id = grubCfgEntryIDFor(filepath.Join(sysroot, "boot", "grub2", "grub.cfg"), kver)
	}
	if id == "" {
		if deps.Logger != nil {
			deps.Logger.Info("no boot entry id found for kernel " + kver + "; the default stays GRUB's first entry")
		}
		return nil
	}
	if !anyExist(sysroot, "usr/bin/grub2-editenv", "usr/sbin/grub2-editenv") {
		if deps.Logger != nil {
			deps.Logger.Warn("the image has no grub2-editenv: saved_entry not set, the default boot entry is the image's")
		}
		return nil
	}
	if _, err := deps.Runner.Run(ctx, executor.CommandSpec{
		Path: "/usr/bin/env", Chroot: sysroot,
		Args:          []string{"grub2-editenv", "-", "set", "saved_entry=" + id},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("grub2-editenv set saved_entry=%s: %w", id, err)
	}
	if deps.Logger != nil {
		deps.Logger.UserInfo("default boot entry: %s (kernel %s)", id, kver)
	}
	return nil
}

// blsEntryIDFor returns the id (file name without .conf) of the BLS entry
// whose linux line boots vmlinuz-<kver>, or "".
func blsEntryIDFor(sysroot, kver string) string {
	dir := filepath.Join(sysroot, "boot", "loader", "entries")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // sysroot file
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(body), "\n") {
			f := strings.Fields(ln)
			if len(f) >= 2 && f[0] == "linux" && filepath.Base(f[1]) == "vmlinuz-"+kver {
				return strings.TrimSuffix(e.Name(), ".conf")
			}
		}
	}
	return ""
}

// grubCfgEntryIDFor returns the menuentry id grub2-mkconfig's 10_linux gives
// kver ('gnulinux-<kver>-advanced-<root uuid>'), or "".
func grubCfgEntryIDFor(grubCfg, kver string) string {
	body, err := os.ReadFile(grubCfg) //nolint:gosec // sysroot file
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`'(gnulinux-` + regexp.QuoteMeta(kver) + `-advanced-[^']*)'`)
	if m := re.FindStringSubmatch(string(body)); m != nil {
		return m[1]
	}
	return ""
}

// lvmDevicesFile is LVM's devices file (el9+: use_devicesfile=1 by default).
const lvmDevicesFile = "etc/lvm/devices/system.devices"

// removeImageLVMDevicesFile deletes the image's LVM devices file before any
// initramfs is built. The file lists the device IDs of the machine the image
// was made on; dracut copies it into the initramfs, and LVM then ignores
// every PV not in it -- this node's root PV included -- so the node stops at
// "Volume group not found". Without the file LVM uses every device, which is
// what a freshly provisioned node needs; `vgimportdevices -a` recreates it for
// this node's PVs if a site wants the filtering back.
func removeImageLVMDevicesFile(deps AdapterDeps) error {
	p := filepath.Join(deps.BootContext.Sysroot, lvmDevicesFile)
	if _, err := os.Lstat(p); err != nil {
		return nil
	}
	if err := deps.FS.Remove(p); err != nil {
		return fmt.Errorf("remove the image's LVM devices file %s: %w", p, err)
	}
	if deps.Logger != nil {
		deps.Logger.UserInfo("removed the image's LVM devices file /%s (it names the image host's disks and would hide "+
			"this node's PVs from LVM in the initramfs); LVM now scans all devices", lvmDevicesFile)
	}
	return nil
}
