// Package sysenv holds the tiny preflight gates every destructive
// installer binary runs at startup: installer-environment detection,
// expected-hostname guard, required-command list.
//
// Ported from /root/NEWTREE/lib/common.sh::{require_installer_env,
// require_expected_host} and /root/NEWTREE/lib/commands.sh::{require_command,
// require_storage_prepare_commands, require_storage_check_commands,
// require_bootloader_common_commands}.
//
// Cross-distribution notes:
//   - /proc/cmdline is present on every Linux kernel we target. The
//     "root=luna" marker is set by the Luna PXE handoff; non-installer
//     contexts never see it. Both probes are filesystem reads so they
//     work identically on RHEL/Debian/Ubuntu/arm64.
//   - The command-preflight lookup uses exec.LookPath under the hood,
//     which honours $PATH. Installer images all set
//     PATH=/usr/sbin:/usr/bin:/sbin:/bin (see internal/cli) so the
//     same list of binaries is searched on every host.
package sysenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CmdlinePath is the default location probed by RequireInstallerEnv.
// Tests inject a temp file via Options.CmdlinePath.
const CmdlinePath = "/proc/cmdline"

// LunaMarker is the substring whose presence in /proc/cmdline signals
// "we are running inside the Luna installer initramfs".
const LunaMarker = "root=luna"

// Options carries the testable injection points for every preflight.
type Options struct {
	// CmdlinePath defaults to /proc/cmdline.
	CmdlinePath string
	// Hostname returns the local short hostname. Defaults to os.Hostname.
	Hostname func() (string, error)
	// AllowNonInstaller short-circuits RequireInstallerEnv. Defaults to
	// the NEWTREE_SKIP_INSTALLER_CHECK env-var behaviour; cli.Common
	// already exposes a flag for it.
	AllowNonInstaller bool
}

// RequireInstallerEnv refuses to continue unless /proc/cmdline contains
// "root=luna" or AllowNonInstaller is set. Mirrors require_installer_env.
func RequireInstallerEnv(opts Options) error {
	if opts.AllowNonInstaller {
		return nil
	}
	path := opts.CmdlinePath
	if path == "" {
		path = CmdlinePath
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path; tests inject t.TempDir
	if err != nil {
		// Treat a missing /proc/cmdline as "not in installer env" and
		// fail loud — the operator can pass --allow-non-installer if
		// that is wrong.
		return fmt.Errorf(
			"refusing to run outside the Luna installer environment: cannot read %s: %w",
			path, err)
	}
	if !strings.Contains(string(raw), LunaMarker) {
		return fmt.Errorf(
			"refusing to run outside the Luna installer environment: " +
				"/proc/cmdline does not contain 'root=luna' " +
				"(run only on a node booted via Luna PXE)")
	}
	return nil
}

// KernelParam returns the value of a `key=value` token on the kernel cmdline
// — the Go equivalent of luna2-start.sh's `getargs key=`. The path defaults to
// CmdlinePath; tests inject Options.CmdlinePath. ok is false when the file is
// unreadable or the key is absent. The first occurrence wins, matching kernel
// last-wins-at-parse but getargs first-match behaviour used by the bash client.
func KernelParam(opts Options, key string) (value string, ok bool) {
	path := opts.CmdlinePath
	if path == "" {
		path = CmdlinePath
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path; tests inject t.TempDir
	if err != nil {
		return "", false
	}
	prefix := key + "="
	for _, tok := range strings.Fields(string(raw)) {
		if strings.HasPrefix(tok, prefix) {
			return strings.TrimPrefix(tok, prefix), true
		}
	}
	return "", false
}

// ErrHostMismatch is matched (errors.Is) when the node is not the expected
// host: a deterministic refusal, unlike a failure to read the hostname.
var ErrHostMismatch = errors.New("host mismatch")

// shortHost is the `hostname -s` form: up to the first dot, lower-cased
// (host names are case-insensitive).
func shortHost(h string) string {
	h = strings.TrimSpace(h)
	if dot := strings.IndexByte(h, '.'); dot >= 0 {
		h = h[:dot]
	}
	return strings.ToLower(h)
}

// compareHosts returns the two names in the form they were compared in: in
// full when both carry a domain, as short names otherwise.
func compareHosts(current, expected string) (string, string, bool) {
	a := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(current)), ".")
	b := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(expected)), ".")
	if !strings.Contains(a, ".") || !strings.Contains(b, ".") {
		a, b = shortHost(a), shortHost(b)
	}
	return a, b, a == b
}

// RequireExpectedHost ensures the local hostname matches expected when
// non-empty. Mirrors require_expected_host. When both names carry a domain
// they are compared in full (node001.a is not node001.b); otherwise as short
// names: the pipeline passes $_HOSTNAME, which Luna may set to the FQDN
// ("node001.cluster"), and stripping the domain only from the local name
// refused every such node as a "mismatch".
func RequireExpectedHost(expected string, opts Options) error {
	if expected == "" {
		return nil
	}
	get := opts.Hostname
	if get == nil {
		get = os.Hostname
	}
	current, err := get()
	if err != nil {
		return fmt.Errorf("hostname: %w", err)
	}
	if a, b, same := compareHosts(current, expected); !same {
		return fmt.Errorf(
			"%w: refusing destructive execution on host %s; expected %s",
			ErrHostMismatch, a, b)
	}
	return nil
}

// RequireCommands returns a non-nil error listing every command name in
// `names` that exec.LookPath cannot find. Mirrors require_command for
// every list in lib/commands.sh. Pass the result of one of the canned
// lists below.
func RequireCommands(names ...string) error {
	var missing []string
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required commands: %s", strings.Join(missing, ", "))
	}
	return nil
}

// RequireCommandsWithFallback is the OR variant for command pairs where
// the bash helper accepts either name (e.g. mkfs.fat | mkfs.vfat,
// fsck.vfat | fsck.fat). The function returns the name it picked, or
// an error listing the alternatives when none exist.
func RequireCommandsWithFallback(primary string, alternatives ...string) (string, error) {
	if _, err := exec.LookPath(primary); err == nil {
		return primary, nil
	}
	for _, alt := range alternatives {
		if _, err := exec.LookPath(alt); err == nil {
			return alt, nil
		}
	}
	return "", fmt.Errorf("missing required command: %s (or %s)",
		primary, strings.Join(alternatives, ", "))
}

// StoragePrepareCommands lists the binaries lib/commands.sh::
// require_storage_prepare_commands probes. mkfs.fat / mkfs.vfat is a
// fallback pair, handled separately by the caller.
var StoragePrepareCommands = []string{
	"jq", // historical: every bash module shells out to jq; the Go port
	// no longer needs it, but the bash partscripts still do, so keep
	// the preflight covering it.
	"lsblk",
	"findmnt",
	"readlink",
	"blkid",
	"udevadm",
	"wipefs",
	"sgdisk",
	"mdadm",
	"vgchange",
	"lvremove",
	"vgremove",
	"pvremove",
	"pvcreate",
	"vgcreate",
	"lvcreate",
	"pvs",
	"vgs",
	"lvs",
	"mount",
	"umount",
	"mkfs.xfs",
	"mkfs.ext4",
	"mkswap",
	// The health probe's filesystem checkers (internal/fscheck). sync and
	// auto decide mount-vs-recreate on their verdict, so a ramdisk without
	// them must fail closed here, before anything happens -- not have every
	// volume probed as "broken" and the os set recreated. fsck.vfat /
	// fsck.fat is a fallback pair, handled separately.
	"e2fsck",
	"xfs_repair",
}

// StorageCheckCommands lists the binaries lib/commands.sh::
// require_storage_check_commands probes.
var StorageCheckCommands = []string{
	"jq",
	"blkid",
	"lsblk",
	"findmnt",
	"readlink",
	"e2fsck",
	"xfs_repair",
	"swaplabel",
	"vgchange",
	"mount",
	"umount",
}

// BootloaderCommonCommands lists what require_bootloader_common_commands
// probes.
var BootloaderCommonCommands = []string{
	"jq",
	"chroot",
	"blkid",
	"findmnt",
	"mount",
	"umount",
	"readlink",
}

// StoragePrepareTools are the fallback-pair names the storage-prepare
// preflight picked.
type StoragePrepareTools struct {
	// MkfsFat is mkfs.fat or mkfs.vfat (fs.Builder.Options.MkfsFatName).
	MkfsFat string
	// FsckVfat is fsck.vfat or fsck.fat (fscheck.Options.FsckVfatCmd).
	FsckVfat string
}

// RequireStoragePrepareCommands runs the preflight for lpart-storage-prepare
// and returns the chosen FAT tools. The bash helper is non-trivial because
// it accepts mkfs.fat OR mkfs.vfat (and the health probe fsck.vfat OR
// fsck.fat, like lpart-storage-check) — we return the chosen names so the
// caller plumbs them into the fs builder and the fs checker.
func RequireStoragePrepareCommands() (StoragePrepareTools, error) {
	if err := RequireCommands(StoragePrepareCommands...); err != nil {
		return StoragePrepareTools{}, err
	}
	mkfs, err := RequireCommandsWithFallback("mkfs.fat", "mkfs.vfat")
	if err != nil {
		return StoragePrepareTools{}, err
	}
	fsck, err := RequireCommandsWithFallback("fsck.vfat", "fsck.fat")
	if err != nil {
		return StoragePrepareTools{}, err
	}
	return StoragePrepareTools{MkfsFat: mkfs, FsckVfat: fsck}, nil
}
