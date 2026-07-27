// Package installcontext models the per-boot install context: the resolved
// install_mode, sourced from the canonical install-context.json file under
// /tmp/install_files/runtime/.
//
// Under the v1.4 model (decisions/0004) install_mode is the single axis that
// selects provisioning behaviour, replacing the old
// install_mode x osimage_sync pair. The seven values:
//
//   - "auto"     — install if blank, refresh if healthy, repair the OS set if
//     broken; falls back to memboot only when no disk layout is declared.
//   - "sync"     — refresh the existing layout, preserve everything, halt on a
//     broken or mismatched disk (never wipe).
//   - "full"     — wipe and reinstall from scratch (non-persistent sets).
//   - "local"    — boot the already-on-disk OS, fetch nothing; verify + mount.
//   - "memboot"  — run the OS from RAM; mount optional persistent DATA sets.
//   - "sanitize" — securely erase all local disks for tenant turnover, stop.
//   - "legacy"   — daemon-only; the client never sees this (the node runs its
//     old bash pre/part/post and does not invoke the lpart-* binaries).
//
// Sources, in precedence order (highest wins):
//
//  1. Explicit constructor arg (Load(opts))
//  2. The JSON file (default /tmp/install_files/runtime/install-context.json)
//  3. Default: install_mode="auto"
package installcontext

import "fmt"

// Mode is the resolved install-mode value.
type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeSync     Mode = "sync"
	ModeFull     Mode = "full"
	ModeLocal    Mode = "local"
	ModeMemboot  Mode = "memboot"
	ModeSanitize Mode = "sanitize"
	ModeLegacy   Mode = "legacy"
)

// AllModes enumerates the legal Mode values in source-of-truth order.
var AllModes = []Mode{ModeAuto, ModeSync, ModeFull, ModeLocal, ModeMemboot, ModeSanitize, ModeLegacy}

// Validate returns an error if m is not one of AllModes.
func (m Mode) Validate() error {
	for _, ok := range AllModes {
		if m == ok {
			return nil
		}
	}
	return fmt.Errorf("invalid install_mode %q (want one of: auto, sync, full, local, memboot, sanitize, legacy)", string(m))
}

// CreatesLayout reports whether the mode may create/refresh an on-disk
// layout via lpart-storage-prepare (auto, sync, full). local mounts existing
// storage; memboot uses a RAM root; sanitize erases; legacy is daemon-only.
func (m Mode) CreatesLayout() bool { return m == ModeAuto || m == ModeSync || m == ModeFull }

// ExtractsImage reports whether the OS image is extracted by
// lpart-osimage-install. The disk OS modes (auto, sync, full) and memboot (which
// extracts into the RAM root) all extract; local reuses on-disk content,
// sanitize/legacy install no OS.
func (m Mode) ExtractsImage() bool {
	return m == ModeAuto || m == ModeSync || m == ModeFull || m == ModeMemboot
}

// IsLocal reports the local mode (verify + mount existing, no fetch).
func (m Mode) IsLocal() bool { return m == ModeLocal }

// IsMemboot reports the memboot (RAM root) mode.
func (m Mode) IsMemboot() bool { return m == ModeMemboot }

// IsSanitize reports the sanitize (secure-erase) mode.
func (m Mode) IsSanitize() bool { return m == ModeSanitize }

// IsLegacy reports the legacy mode (daemon-only; should not reach the client).
func (m Mode) IsLegacy() bool { return m == ModeLegacy }

// InstallsBootloader reports whether a bootloader finalise step applies:
// every disk-based OS mode (auto, sync, full, local) but not memboot/
// sanitize/legacy.
func (m Mode) InstallsBootloader() bool {
	return m == ModeAuto || m == ModeSync || m == ModeFull || m == ModeLocal
}

// Source records where the value came from.
const (
	SourceFile     = "file"
	SourceExplicit = "explicit"
	SourceDefault  = "default"
)

// Context is the resolved + serialisable install context.
type Context struct {
	InstallMode Mode   `json:"install_mode"`
	Source      string `json:"source"`
	// Provision* are the node's resolved provisioning fields, carried so the
	// OS-image download honours them instead of hardcoding torrent->http.
	// They mirror the legacy install.sh's PROVISION_METHOD/PROVISION_FALLBACK.
	// Omitted (empty) when unknown -> the download falls back to its default.
	ProvisionMethod    string `json:"provision_method,omitempty"`
	ProvisionFallback  string `json:"provision_fallback,omitempty"`
	ProvisionInterface string `json:"provision_interface,omitempty"`
}

// Defaults returns the install context applied when no source supplies a value.
func Defaults() Context {
	return Context{InstallMode: ModeAuto, Source: SourceDefault}
}

// Validate runs Validate on the mode.
func (c Context) Validate() error { return c.InstallMode.Validate() }
