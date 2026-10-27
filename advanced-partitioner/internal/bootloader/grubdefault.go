package bootloader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
)

// /etc/default/grub is written from scratch, so the boot setup of the node
// does not depend on the host the image was built on. The image's file
// contributes an allow-list only: the console=ttyS* and crashkernel=
// arguments of its kernel command lines, its GRUB_TERMINAL /
// GRUB_SERIAL_COMMAND for a serial console, and GRUB_ENABLE_BLSCFG (how the
// distribution keeps its boot entries; without it a later kernel update is
// missing from the menu until grub2-mkconfig runs again). Luna's osimage kernel options
// travel on the installer's own command line and are passed on unchanged.

// writeGrubDefault writes <sysroot>/etc/default/grub through fs (dry-run
// aware). defaultEntry is GRUB_DEFAULT: "saved" where the default kernel is
// set afterwards in grubenv (saved_entry), "0" where the first entry is it.
func writeGrubDefault(fs fsops.FS, bc BootContext, defaultEntry string) error {
	path := filepath.Join(bc.Sysroot, "etc", "default", "grub")
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", path, err)
	}
	image, err := os.ReadFile(path) //nolint:gosec // sysroot file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	body := grubDefaultFile(string(image), bc.KernelArgs, bc.LunaKernelOptions, defaultEntry)
	return fs.WriteFile(path, []byte(body), 0o644)
}

// grubDefaultFile renders the file from lpart's keys, the allow-listed
// settings of the image's file and Luna's kernel options.
func grubDefaultFile(image, kernelArgs, lunaOptions, defaultEntry string) string {
	terminal, serial, bls, imageArgs := imageGrubSettings(image)
	lines := []string{
		"GRUB_TIMEOUT=5",
		"GRUB_DEFAULT=" + defaultEntry,
		"GRUB_DISABLE_SUBMENU=true",
		"GRUB_DISABLE_OS_PROBER=true",
		"GRUB_DISABLE_RECOVERY=true",
	}
	// GRUB_TERMINAL covers output too, so the image's replaces ours.
	if terminal == "" {
		terminal = `GRUB_TERMINAL_OUTPUT="console"`
	}
	lines = append(lines, terminal)
	for _, l := range []string{serial, bls} {
		if l != "" {
			lines = append(lines, l)
		}
	}
	args := strings.Fields(kernelArgs)
	args = append(args, imageArgs...)
	args = append(args, cmdlineFields(lunaOptions)...)
	lines = append(lines, GrubCmdlineValue(strings.Join(uniqueArgs(args), " ")))
	return strings.Join(lines, "\n") + "\n"
}

// imageGrubSettings is what an image's /etc/default/grub may contribute: its
// GRUB_TERMINAL, GRUB_SERIAL_COMMAND and GRUB_ENABLE_BLSCFG lines (the last
// assignment, as the shell reads them) and the serial console and crashkernel
// arguments of its GRUB_CMDLINE_LINUX(_DEFAULT).
func imageGrubSettings(image string) (terminal, serial, bls string, args []string) {
	for _, line := range strings.Split(image, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		key, rhs, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "GRUB_TERMINAL":
			terminal = line
		case "GRUB_SERIAL_COMMAND":
			serial = line
		case "GRUB_ENABLE_BLSCFG":
			bls = line
		case "GRUB_CMDLINE_LINUX", "GRUB_CMDLINE_LINUX_DEFAULT":
			for _, a := range strings.Fields(shellValue(rhs)) {
				if strings.HasPrefix(a, "console=ttyS") || strings.HasPrefix(a, "crashkernel=") {
					args = append(args, a)
				}
			}
		}
	}
	return terminal, serial, bls, args
}

// shellValue reads the right-hand side of a shell assignment: a quoted
// string up to its closing quote, else the first word.
func shellValue(rhs string) string {
	if rhs != "" && (rhs[0] == '"' || rhs[0] == '\'') {
		if end := strings.IndexByte(rhs[1:], rhs[0]); end >= 0 {
			return rhs[1 : 1+end]
		}
		return rhs[1:]
	}
	if i := strings.IndexAny(rhs, " \t#"); i >= 0 {
		return rhs[:i]
	}
	return rhs
}

// cmdlineFields splits a kernel command line as the kernel does: a double
// quoted value with blanks in it stays one argument.
func cmdlineFields(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case !quoted && (r == ' ' || r == '\t' || r == '\n'):
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// uniqueArgs keeps the first occurrence of every argument, but the last of a
// console=: the last one given is /dev/console.
func uniqueArgs(args []string) []string {
	last := make(map[string]int, len(args))
	for i, a := range args {
		last[a] = i
	}
	seen := make(map[string]bool, len(args))
	out := make([]string, 0, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "console=") && last[a] != i {
			continue
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// LunaKernelOptions reads the installer's kernel command line (cmdlinePath,
// "" for /proc/cmdline) and returns the osimage kernel options Luna put on
// it: everything that is neither the netboot installer's own argument nor
// lpart's, exactly as written -- an option with a leading underscore is a
// placeholder the operator enables by removing it.
func LunaKernelOptions(cmdlinePath string) (string, error) {
	if cmdlinePath == "" {
		cmdlinePath = "/proc/cmdline"
	}
	raw, err := os.ReadFile(cmdlinePath) //nolint:gosec // operator-supplied path; tests inject a file
	if err != nil {
		return "", fmt.Errorf("read kernel command line: %w", err)
	}
	var out []string
	for _, a := range strings.Fields(string(raw)) {
		if !installerOnlyArg(a) && !lpartOwnedArg(a) {
			out = append(out, a)
		}
	}
	return strings.Join(out, " "), nil
}

// argKey is the part of a kernel argument before "=".
func argKey(arg string) string {
	k, _, _ := strings.Cut(arg, "=")
	return k
}

// lpartOwnedArg reports whether an argument locates or assembles the root
// storage -- what KernelArgs computes for this node.
func lpartOwnedArg(arg string) bool {
	k := argKey(arg)
	switch k {
	case "root", "rootfstype", "resume", "rd.auto", "rd.md", "rd.lvm":
		return true
	}
	return strings.HasPrefix(k, "rd.md.") || strings.HasPrefix(k, "rd.lvm.")
}

// installerOnlyArg reports whether an argument belongs to the netboot
// installer's command line (root=luna is caught as root=) or is injected by a
// boot loader -- "kernel" is iPXE's image name -- and so must never reach the
// installed system's.
func installerOnlyArg(arg string) bool {
	k := argKey(arg)
	switch k {
	case "kernel", "luna", "boot", "initrd", "ip", "BOOTIF", "bootdev", "rd.neednet", "BOOT_IMAGE":
		return true
	}
	return strings.HasPrefix(k, "luna.") || strings.HasPrefix(k, "rd.luna") || strings.HasPrefix(k, "lpart.")
}
