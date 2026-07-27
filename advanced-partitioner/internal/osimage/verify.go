package osimage

import (
	"os"
	"path/filepath"
	"strings"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/verify"
)

// VerifyExtract proves the OS image was actually extracted into the sysroot and
// shows the evidence: /etc/os-release present (the canonical "the image landed"
// marker), the top-level tree populated, and the always-present etc/ + usr/
// dirs. Fails loudly when the sysroot is empty or partial — the exact silent
// failure mode that bit Ubuntu (extract into the wrong dir left /root empty).
func VerifyExtract(logger *txlog.Logger, sysroot string) error {
	r := verify.New(logger, "osimage extract verification")

	// os-release is the canonical proof the image is in place.
	osr := filepath.Join(sysroot, "etc", "os-release")
	if data, err := os.ReadFile(osr); err == nil { //nolint:gosec // sysroot-derived path; runs as root
		r.Show("extracted OS (/etc/os-release)", verify.Lines(string(data)))
		r.OK("/etc/os-release present" + prettySuffix(string(data)))
	} else {
		r.Missing("/etc/os-release (image not extracted into " + sysroot + ")")
	}

	// Top-level tree populated, with the dirs every Linux rootfs always has.
	ents, _ := os.ReadDir(sysroot)
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	r.Show("sysroot "+sysroot, names)
	for _, d := range []string{"etc", "usr"} {
		_, err := os.Stat(filepath.Join(sysroot, d))
		r.Require(err == nil, d+"/ present", "sysroot dir "+d+"/")
	}

	return r.Done()
}

// prettySuffix returns " (PRETTY_NAME)" parsed from os-release, or "".
func prettySuffix(osRelease string) string {
	for _, ln := range strings.Split(osRelease, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(ln), "PRETTY_NAME="); ok {
			return " (" + strings.Trim(v, `"`) + ")"
		}
	}
	return ""
}
