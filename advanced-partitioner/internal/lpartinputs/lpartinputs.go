// Package lpartinputs resolves the node's Luna provisioning inputs (the
// install_mode enum, the provision_* download fields, and the disklayout +
// osimage-filter JSON) from the runtime files Luna's install template
// materialises before any lpart phase runs.
//
// Source: the runtime dir the install template writes
//
//	storage.json          <- disklayout       (LUNA_DISKLAYOUT_B64)
//	osimage-filter.json   <- osimage_filter    (LUNA_OSIMAGE_FILTER_B64)
//	install-context.json  <- install_mode + provision_method/fallback/interface
//
// The template renders these from the daemon's group->node *merged* values at
// /boot/install time, so the inherited result is already resolved here and
// there is no separate node-detail call from the installer. Keeping the node
// record off the install path is deliberate: the booting node's provision
// token is not accepted on GET /config/node/<node> (that stays admin-only), so
// the installer must not depend on reading it. The disklayout the node applies
// is available on its own, provision-scoped, at
// GET /config/node/<node>/disklayout for anything that wants it from the
// daemon; the install path here does not need it.
//
// install_mode is the single provisioning axis (decisions/0004); an absent
// value defaults to "auto".
package lpartinputs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Inputs is the resolved install mode plus which JSON inputs were populated.
type Inputs struct {
	InstallMode         string // auto|sync|full|local|memboot|sanitize|legacy
	DisklayoutPopulated bool
	FilterPopulated     bool
	Source              string // "file"
}

// Resolve reads runtime/{storage,osimage-filter,install-context}.json that the
// install template dropped in runtimeDir and normalises install-context.json
// (re-applying the mode default). storage.json and osimage-filter.json are
// left exactly as the template wrote them.
func Resolve(runtimeDir string) (Inputs, error) {
	if err := os.MkdirAll(runtimeDir, 0o750); err != nil {
		return Inputs{}, fmt.Errorf("create runtime dir: %w", err)
	}
	return resolveFromFiles(runtimeDir)
}

func resolveFromFiles(runtimeDir string) (Inputs, error) {
	in := Inputs{Source: "file"}
	in.DisklayoutPopulated = fileHasContent(filepath.Join(runtimeDir, "storage.json"))
	in.FilterPopulated = fileHasContent(filepath.Join(runtimeDir, "osimage-filter.json"))
	// Preserve whatever the install template wrote (mode + provision fields),
	// re-normalising the mode default.
	ic := readInstallContext(filepath.Join(runtimeDir, "install-context.json"))
	in.InstallMode = ic.InstallMode
	writeInstallContext(runtimeDir, ic)
	return in, nil
}

// --- install-context.json -------------------------------------------------

type installContext struct {
	InstallMode        string `json:"install_mode"`
	ProvisionMethod    string `json:"provision_method,omitempty"`
	ProvisionFallback  string `json:"provision_fallback,omitempty"`
	ProvisionInterface string `json:"provision_interface,omitempty"`
}

func readInstallContext(path string) installContext {
	ic := installContext{InstallMode: "auto"}
	data, err := os.ReadFile(path) //nolint:gosec // G304: runtimeDir-derived path; binary runs as root
	if err != nil {
		return ic
	}
	if json.Unmarshal(data, &ic) == nil {
		ic.InstallMode = orDefault(ic.InstallMode, "auto")
		return ic
	}
	return installContext{InstallMode: "auto"}
}

func writeInstallContext(runtimeDir string, ic installContext) {
	ic.InstallMode = orDefault(ic.InstallMode, "auto")
	if data, err := json.MarshalIndent(ic, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(runtimeDir, "install-context.json"), data, 0o600)
	}
}

// --- helpers --------------------------------------------------------------

func fileHasContent(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
