// Package verify provides a small evidence Reporter so each installer step can
// END by PROVING what it did — showing the captured evidence and failing loudly
// on any missing artefact — with one consistent shape across storage-prepare,
// osimage-install, and bootloader-finalise. Runs verbose by design: a step on a
// new distro/arch should show exactly what landed, not a silent "ok".
package verify

import (
	"fmt"
	"strings"

	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Reporter accumulates pass/fail checks and the evidence behind them, then
// returns an aggregate error naming everything that was missing.
type Reporter struct {
	log     *txlog.Logger
	what    string
	missing []string
}

// New starts a verification report and logs its banner. A nil logger is
// tolerated (the Reporter still tracks missing items for the returned error).
func New(log *txlog.Logger, what string) *Reporter {
	if log != nil {
		log.UserInfo("==== %s: proving the result ====", what)
	}
	return &Reporter{log: log, what: what}
}

// Show logs a captured block of evidence (e.g. efibootmgr -v / lsblk output)
// under a header, one indented line each.
func (r *Reporter) Show(header string, lines []string) {
	if r.log == nil {
		return
	}
	r.log.UserInfo("%s:", header)
	for _, ln := range lines {
		r.log.UserInfo("    %s", ln)
	}
}

// OK records a satisfied check.
func (r *Reporter) OK(msg string) {
	if r.log != nil {
		r.log.UserInfo("  [OK] %s", msg)
	}
}

// Missing flags a missing artefact (logged + recorded for the final error).
func (r *Reporter) Missing(item string) {
	if r.log != nil {
		r.log.Warn("  [MISSING] " + item)
	}
	r.missing = append(r.missing, item)
}

// Require is the common shape: OK when present, Missing otherwise.
func (r *Reporter) Require(present bool, okMsg, missingItem string) {
	if present {
		r.OK(okMsg)
		return
	}
	r.Missing(missingItem)
}

// Failed reports whether any check is missing so far.
func (r *Reporter) Failed() bool { return len(r.missing) > 0 }

// Done returns an aggregate error when anything was missing, else logs VERIFIED.
func (r *Reporter) Done() error {
	if len(r.missing) > 0 {
		return fmt.Errorf("%s FAILED — missing: %s", r.what, strings.Join(r.missing, "; "))
	}
	if r.log != nil {
		r.log.UserInfo("==== %s VERIFIED ====", r.what)
	}
	return nil
}

// Lines splits captured command output into non-empty, CR-trimmed lines.
func Lines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, strings.TrimRight(ln, "\r"))
		}
	}
	return out
}
