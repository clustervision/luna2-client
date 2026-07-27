package osimage

import (
	"fmt"
	"os"
	"regexp"
)

// LunaInstallScript is the file Luna writes during PXE boot that
// carries the LUNA_TOKEN and the image URL. lpart-osimage-install pulls
// both pieces of information from it; the operator usually overrides
// the path only in tests.
const LunaInstallScript = "/lunatmp/install.sh"

// imageFileRE pulls the OS image tarball name out of Luna's install.sh.
// The bash equivalent is `grep -m1 -oP '(?<=/files/)[^"'"'"'\s]+\.tar\.bz2'`.
//
// We accept any non-{quote, whitespace} run that ends with .tar.bz2 and
// comes after a "/files/" prefix.
var imageFileRE = regexp.MustCompile(`/files/([^"'\s]+\.tar\.bz2)`)

// tokenRE pulls LUNA_TOKEN="<value>" out of install.sh. The bash side
// reads LUNA_TOKEN from its environment; the wrapper script
// (runtime/node001-localboot/prescript.sh) sets it. For Go we want a
// single function that does the lookup in either source.
var tokenRE = regexp.MustCompile(`LUNA_TOKEN="([^"]+)"`)

// DetectImageFilename returns the OS-image filename (e.g.
// "compute-image-1779269535.tar.bz2") in priority order:
//
//  1. opts.Override (typically from --osimage-file or OSIMAGE_FILE env)
//  2. The first /files/<name>.tar.bz2 hit in opts.LunaInstallScript
//
// Returns an error when neither source yields a name.
//
// Mirrors detect_osimage_file in /root/NEWTREE/bin/lpart-osimage-install.
func DetectImageFilename(opts DetectOptions) (string, error) {
	if opts.Override != "" {
		return opts.Override, nil
	}
	path := opts.LunaInstallScript
	if path == "" {
		path = LunaInstallScript
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied path; tests inject
	if err != nil {
		return "", fmt.Errorf(
			"cannot detect osimage filename: set OSIMAGE_FILE or ensure %s is present: %w",
			path, err)
	}
	m := imageFileRE.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf(
			"cannot detect osimage filename: %s does not contain a /files/<name>.tar.bz2 reference",
			path)
	}
	return string(m[1]), nil
}

// DetectLunaToken returns the LUNA_TOKEN value in priority order:
//
//  1. opts.Override (operator passed via env / flag)
//  2. The LUNA_TOKEN="<v>" assignment in opts.LunaInstallScript
//
// The Bash predecessor relies on the parent shell exporting LUNA_TOKEN;
// the Go binary doesn't run inside that shell so it reads the source
// file directly.
func DetectLunaToken(opts DetectOptions) (string, error) {
	if opts.Override != "" {
		return opts.Override, nil
	}
	path := opts.LunaInstallScript
	if path == "" {
		path = LunaInstallScript
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied path; tests inject
	if err != nil {
		return "", fmt.Errorf("cannot read LUNA_TOKEN source %s: %w", path, err)
	}
	m := tokenRE.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("%s does not contain a LUNA_TOKEN assignment", path)
	}
	return string(m[1]), nil
}

// DetectOptions configures DetectImageFilename / DetectLunaToken.
type DetectOptions struct {
	// Override short-circuits the file probe. Set this from
	// OSIMAGE_FILE / LUNA_TOKEN env vars or operator flags.
	Override string
	// LunaInstallScript is the path Luna writes during PXE boot.
	// Defaults to LunaInstallScript when empty.
	LunaInstallScript string
}
