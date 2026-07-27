// Package lpartinputs resolves the node's Luna provisioning inputs (the
// install_mode enum and the disklayout + osimage-filter JSON) and
// materialises them as files in the runtime dir for the lpart-* binaries to
// consume.
//
// Primary source: the Luna daemon node-detail API
//
//	GET <luna.url>/config/node/<luna.node>   (header x-access-tokens: <token>)
//
// where luna.url/luna.node come from /proc/cmdline and the token from
// /lunatmp/token.dat. The JSON fields are stored base64-encoded and decoded
// before writing.
//
// install_mode is the single provisioning axis (decisions/0004); an absent
// value defaults to "auto". The earlier tx_* migration shim was dropped
// (decisions/0006 supersedes 0005): tx_* was only ever in-flight scaffolding,
// never released, so there is nothing to migrate from.
//
// Fallback: whatever Luna's install template already wrote into the runtime
// dir before the partscript ran.
package lpartinputs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Inputs is the resolved install mode plus which JSON inputs were populated.
type Inputs struct {
	InstallMode         string // auto|sync|full|local|memboot|sanitize|legacy
	DisklayoutPopulated bool
	FilterPopulated     bool
	Source              string // "daemon" or "file"
}

const (
	tokenFile   = "/lunatmp/token.dat" //nolint:gosec // G101: path to the token file, not a credential
	cmdlineFile = "/proc/cmdline"
)

// Resolve obtains the inputs and writes runtime/{storage,osimage-filter,
// install-context}.json. It tries the Luna daemon first and falls back to the
// files the install template already dropped in runtimeDir.
func Resolve(runtimeDir string) (Inputs, error) {
	if err := os.MkdirAll(runtimeDir, 0o750); err != nil {
		return Inputs{}, fmt.Errorf("create runtime dir: %w", err)
	}
	if in, err := resolveFromDaemon(runtimeDir); err == nil {
		return in, nil
	}
	return resolveFromFiles(runtimeDir)
}

// --- daemon source --------------------------------------------------------

type nodeFields struct {
	InstallMode        string `json:"install_mode"`
	Disklayout         string `json:"disklayout"`
	Filter             string `json:"osimage_filter"`
	ProvisionMethod    string `json:"provision_method"`
	ProvisionFallback  string `json:"provision_fallback"`
	ProvisionInterface string `json:"provision_interface"`
}

func resolveFromDaemon(runtimeDir string) (Inputs, error) {
	cl := parseCmdline()
	lurl, lnode := cl["luna.url"], cl["luna.node"]
	if lurl == "" || lnode == "" {
		return Inputs{}, fmt.Errorf("luna.url/luna.node not on cmdline")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return Inputs{}, fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || token == "None" {
		return Inputs{}, fmt.Errorf("no usable token")
	}

	// curl rather than net/http: curl is already in the install initramfs and
	// is the carrier every other Luna request uses.
	url := strings.TrimRight(lurl, "/") + "/config/node/" + lnode
	args := []string{"-s", "-f", "-m", "20", "--connect-timeout", "10",
		"-H", "x-access-tokens: " + token, url}
	switch strings.ToLower(cl["luna.verifycert"]) {
	case "yes", "true":
		// verify the certificate
	default:
		args = append([]string{"--insecure"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	//nolint:gosec // G204: fixed 'curl' command; args derive from the Luna cmdline + token file
	body, err := exec.CommandContext(ctx, "curl", args...).Output()
	if err != nil {
		return Inputs{}, fmt.Errorf("curl node detail: %w", err)
	}
	f, err := extractNodeFields(body, lnode)
	if err != nil {
		return Inputs{}, err
	}
	in := writeInputs(runtimeDir, f)
	in.Source = "daemon"
	return in, nil
}

func extractNodeFields(body []byte, node string) (nodeFields, error) {
	var root struct {
		Config struct {
			Node map[string]json.RawMessage `json:"node"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return nodeFields{}, err
	}
	raw, ok := root.Config.Node[node]
	if !ok {
		for _, v := range root.Config.Node { // single-node responses may key differently
			raw, ok = v, true
			break
		}
	}
	if !ok {
		return nodeFields{}, fmt.Errorf("node %s absent from response", node)
	}
	var f nodeFields
	if err := json.Unmarshal(raw, &f); err != nil {
		return nodeFields{}, err
	}
	return f, nil
}

func writeInputs(runtimeDir string, f nodeFields) Inputs {
	in := Inputs{InstallMode: orDefault(f.InstallMode, "auto")}
	in.DisklayoutPopulated = writeJSONField(filepath.Join(runtimeDir, "storage.json"), f.Disklayout)
	in.FilterPopulated = writeJSONField(filepath.Join(runtimeDir, "osimage-filter.json"), f.Filter)
	writeInstallContext(runtimeDir, installContext{
		InstallMode:        in.InstallMode,
		ProvisionMethod:    strings.TrimSpace(f.ProvisionMethod),
		ProvisionFallback:  strings.TrimSpace(f.ProvisionFallback),
		ProvisionInterface: strings.TrimSpace(f.ProvisionInterface),
	})
	return in
}

// writeJSONField writes a base64-or-plain JSON value to path. Returns true if
// a non-empty JSON document was written.
func writeJSONField(path, val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return false
	}
	if dec, err := base64.StdEncoding.DecodeString(val); err == nil && looksJSON(dec) {
		return os.WriteFile(path, dec, 0o600) == nil
	}
	if looksJSON([]byte(val)) {
		return os.WriteFile(path, []byte(val), 0o600) == nil
	}
	return false
}

// --- file fallback --------------------------------------------------------

func resolveFromFiles(runtimeDir string) (Inputs, error) {
	in := Inputs{Source: "file"}
	in.DisklayoutPopulated = fileHasContent(filepath.Join(runtimeDir, "storage.json"))
	in.FilterPopulated = fileHasContent(filepath.Join(runtimeDir, "osimage-filter.json"))
	// Preserve whatever the install template already wrote (mode + provision
	// fields), re-normalising the mode default.
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

func parseCmdline() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(cmdlineFile)
	if err != nil {
		return out
	}
	for _, tok := range strings.Fields(string(data)) {
		if eq := strings.IndexByte(tok, '='); eq > 0 {
			out[tok[:eq]] = tok[eq+1:]
		}
	}
	return out
}

func looksJSON(b []byte) bool {
	t := strings.TrimSpace(string(b))
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

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
