package bootloader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
)

// /etc/default/grub is MERGED, never replaced. The image's file carries what
// the image builder decided the node needs -- console=ttyS0,115200 for SOL,
// net.ifnames=0 / biosdevname=0 for the NIC names its configs expect,
// crashkernel=, hugepages=, a serial GRUB_TERMINAL / GRUB_SERIAL_COMMAND,
// GRUB_ENABLE_BLSCFG -- and on el9 grub2-mkconfig copies GRUB_CMDLINE_LINUX
// into every BLS entry and /etc/kernel/cmdline, so writing a minimal file
// throws all of that away on the installed node.
//
// lpart owns exactly two things here:
//   - the kernel arguments that locate and assemble the root on THIS node's
//     storage (root=, rootfstype=, rd.auto, rd.md*, rd.lvm*, resume=): the
//     image's values name the build host's VG/array UUIDs and would make
//     dracut wait for devices that do not exist. They are removed and
//     lpart's KernelArgs are added.
//   - the handful of keys its boot flow depends on (grubDefaultKey with
//     force=true): GRUB_DEFAULT, because the default kernel is chosen via
//     saved_entry (RHEL) or entry 0 (Debian), and GRUB_DISABLE_OS_PROBER,
//     because probing the node's other disks is never wanted.
//
// Everything else the image sets is kept verbatim; lpart's old defaults are
// only added for keys the image does not set. Installer-only arguments (the
// root=luna netboot command line) are scrubbed from both GRUB_CMDLINE_LINUX
// and GRUB_CMDLINE_LINUX_DEFAULT so an image captured from a netbooted node
// cannot send the installed node back into the installer.

// grubDefaultKey is one key lpart writes into /etc/default/grub.
type grubDefaultKey struct {
	name  string
	value string // the literal right-hand side, quotes included
	// force replaces the image's value; otherwise the key is only added when
	// the image does not set it.
	force bool
	// unlessSet: when the image sets any of these keys, this one is not added
	// (GRUB_TERMINAL overrides GRUB_TERMINAL_OUTPUT, so a serial GRUB_TERMINAL
	// must not get a console-only GRUB_TERMINAL_OUTPUT beside it).
	unlessSet []string
}

const grubCmdlineKey = "GRUB_CMDLINE_LINUX"

// rhelGrubDefaultKeys / debianGrubDefaultKeys are lpart's keys, in the order
// the previous (overwriting) writer emitted them, so an image with no
// /etc/default/grub gets the same file as before.
var rhelGrubDefaultKeys = []grubDefaultKey{
	{name: "GRUB_TIMEOUT", value: "5"},
	{name: "GRUB_DEFAULT", value: "saved", force: true},
	{name: "GRUB_DISABLE_SUBMENU", value: "true"},
	{name: "GRUB_DISABLE_OS_PROBER", value: "true", force: true},
	{name: "GRUB_DISABLE_RECOVERY", value: "true"},
	{name: "GRUB_TERMINAL_OUTPUT", value: `"console"`, unlessSet: []string{"GRUB_TERMINAL"}},
}

var debianGrubDefaultKeys = []grubDefaultKey{
	{name: "GRUB_TIMEOUT", value: "5"},
	{name: "GRUB_DEFAULT", value: "0", force: true},
	{name: "GRUB_DISABLE_OS_PROBER", value: "true", force: true},
	{name: "GRUB_DISABLE_RECOVERY", value: "true"},
	{name: "GRUB_TERMINAL_OUTPUT", value: `"console"`, unlessSet: []string{"GRUB_TERMINAL"}},
}

// writeMergedGrubDefault reads <sysroot>/etc/default/grub (absent = empty),
// merges lpart's keys and kernel arguments into it, and writes it back through
// fs (dry-run aware).
func writeMergedGrubDefault(fs fsops.FS, sysroot string, keys []grubDefaultKey, kernelArgs string) error {
	path := filepath.Join(sysroot, "etc", "default", "grub")
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", path, err)
	}
	existing, err := os.ReadFile(path) //nolint:gosec // sysroot file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	merged := mergeGrubDefault(string(existing), keys, kernelArgs)
	return fs.WriteFile(path, []byte(merged), 0o644)
}

// shellAssignRE matches `KEY=value` / `export KEY=value` at the start of a
// line (comments and anything else are kept as they are).
var shellAssignRE = regexp.MustCompile(`^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// mergeGrubDefault is the pure merge; see the file comment for the rules.
func mergeGrubDefault(existing string, keys []grubDefaultKey, kernelArgs string) string {
	var lines []string
	if existing != "" {
		lines = strings.Split(strings.TrimRight(existing, "\n"), "\n")
	}
	present := map[string]bool{}
	lastCmdline := -1
	for _, ln := range lines {
		if m := shellAssignRE.FindStringSubmatch(ln); m != nil {
			present[m[2]] = true
		}
	}
	forced := map[string]string{}
	for _, k := range keys {
		if k.force {
			forced[k.name] = k.value
		}
	}

	for i, ln := range lines {
		m := shellAssignRE.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		prefix, name, rhs := m[1], m[2], m[3]
		switch {
		case forced[name] != "":
			lines[i] = prefix + name + "=" + forced[name]
		case name == grubCmdlineKey || name == "GRUB_CMDLINE_LINUX_DEFAULT":
			lines[i] = prefix + name + "=" + scrubCmdlineValue(rhs)
			if name == grubCmdlineKey {
				lastCmdline = i
			}
		}
	}

	// lpart's own arguments go on the last GRUB_CMDLINE_LINUX assignment:
	// the shell's last assignment wins, and a `"$GRUB_CMDLINE_LINUX ..."`
	// line carries the earlier ones along.
	if lastCmdline >= 0 {
		m := shellAssignRE.FindStringSubmatch(lines[lastCmdline])
		lines[lastCmdline] = m[1] + m[2] + "=" + appendCmdlineArgs(m[3], kernelArgs)
	}

	for _, k := range keys {
		if present[k.name] {
			continue
		}
		skip := false
		for _, other := range k.unlessSet {
			if present[other] {
				skip = true
			}
		}
		if !skip {
			lines = append(lines, k.name+"="+k.value)
		}
	}
	if lastCmdline < 0 {
		lines = append(lines, GrubCmdlineValue(kernelArgs))
	}
	return strings.Join(lines, "\n") + "\n"
}

// parseShellValue splits the right-hand side of a shell assignment into its
// quote character (0 for an unquoted word), the inner text, and whatever
// follows the value (a trailing comment). ok is false for anything this
// simple reader does not understand (an unterminated quote).
func parseShellValue(rhs string) (quote byte, inner, trailing string, ok bool) {
	if rhs == "" {
		return 0, "", "", true
	}
	switch rhs[0] {
	case '"':
		for i := 1; i < len(rhs); i++ {
			if rhs[i] == '\\' {
				i++
				continue
			}
			if rhs[i] == '"' {
				return '"', rhs[1:i], rhs[i+1:], true
			}
		}
		return 0, "", "", false
	case '\'':
		if j := strings.IndexByte(rhs[1:], '\''); j >= 0 {
			return '\'', rhs[1 : 1+j], rhs[2+j:], true
		}
		return 0, "", "", false
	default:
		if j := strings.IndexAny(rhs, " \t"); j >= 0 {
			return 0, rhs[:j], rhs[j:], true
		}
		return 0, rhs, "", true
	}
}

// formatShellValue re-quotes inner with the original quote character, double
// quotes for a value that was a bare word.
func formatShellValue(quote byte, inner, trailing string) string {
	if quote == 0 {
		quote = '"'
	}
	return string(quote) + inner + string(quote) + trailing
}

// scrubCmdlineValue drops lpart-owned and installer-only arguments from a
// GRUB_CMDLINE_LINUX(_DEFAULT) right-hand side, keeping every other argument
// in its original order and the original quoting.
func scrubCmdlineValue(rhs string) string {
	quote, inner, trailing, ok := parseShellValue(rhs)
	if !ok {
		// Unparsable: lpart cannot tell which arguments are its own, so it
		// replaces the value rather than risk leaving a build-host root=.
		return `""`
	}
	return formatShellValue(quote, strings.Join(keepImageArgs(strings.Fields(inner)), " "), trailing)
}

// appendCmdlineArgs adds lpart's arguments to an already scrubbed value.
func appendCmdlineArgs(rhs, kernelArgs string) string {
	quote, inner, trailing, ok := parseShellValue(rhs)
	if !ok {
		return GrubCmdlineValue(kernelArgs)[len(grubCmdlineKey)+1:]
	}
	args := strings.Fields(inner)
	args = append(args, strings.Fields(kernelArgs)...)
	return formatShellValue(quote, strings.Join(args, " "), trailing)
}

// keepImageArgs returns the arguments that are neither lpart's nor the
// installer's.
func keepImageArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if lpartOwnedArg(a) || installerOnlyArg(a) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// argKey is the part of a kernel argument before "=".
func argKey(arg string) string {
	k, _, _ := strings.Cut(arg, "=")
	return k
}

// lpartOwnedArg reports whether an argument locates or assembles the root
// storage -- what KernelArgs computes for this node, and what an image's
// /etc/default/grub has for its build host.
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
// boot loader, and so must never reach the installed system's.
func installerOnlyArg(arg string) bool {
	k := argKey(arg)
	switch k {
	case "luna", "rd.neednet", "ip", "BOOTIF", "bootdev", "initrd", "BOOT_IMAGE":
		return true
	}
	return strings.HasPrefix(k, "luna.") || strings.HasPrefix(k, "rd.luna")
}
