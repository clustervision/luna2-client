// Package lunaclient pins a node's resolved disklayout back to the luna-daemon
// (decisions/0008). It is the write counterpart of internal/lpartinputs, which
// GETs the node's inputs from the daemon.
//
// Transport is curl, not net/http, on purpose: curl already ships in the
// install initramfs and is the carrier every other Luna request uses
// (luna-installer.sh, lpartinputs), and avoiding net/http keeps the Go HTTP/TLS
// stack — and its size and CVE surface — out of the on-node binary. This
// mirrors internal/lpartinputs exactly, in the opposite direction (POST vs GET).
//
// The node already holds everything the call needs at install time: the
// controller URL and node name from the kernel cmdline (luna.url=, luna.node=)
// and the install token it used to GET /boot/install/<node>. token_required on
// the daemon accepts that token for POST /config/node/<node>, so no new route
// or credential is required.
package lunaclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// curl timeouts (seconds), matching internal/lpartinputs' GET.
const (
	maxTime     = "30"
	connTimeout = "10"
)

// PinParams is everything PinDisklayout needs. Disklayout is the complete
// selection=manual layout document (JSON bytes); it is sent as a string value
// inside the daemon's config payload because disklayout is stored as opaque
// TEXT (the same shape `luna node change --disklayout` uses).
type PinParams struct {
	BaseURL    string // luna.url, e.g. "https://controller:7050"
	Node       string // luna.node
	Token      string // install token, sent as the x-access-tokens header
	Disklayout []byte // the manual layout document to pin
	Insecure   bool   // skip TLS verification (controller self-signed cert)
}

// Endpoint is the node-config URL the pin is POSTed to.
func (p PinParams) Endpoint() string {
	return strings.TrimRight(p.BaseURL, "/") + "/config/node/" + p.Node
}

// BodyBytes renders the daemon update payload:
//
//	{"config":{"node":{"<node>":{"disklayout":"<base64-of-layout-json>"}}}}
//
// disklayout is **base64-encoded**, exactly as the luna CLI's `-q` (quick) flag
// sends script/JSON fields. This is required, not cosmetic: the daemon does not
// store a raw JSON value faithfully (it comes back with quotes stripped, i.e.
// invalid JSON), whereas base64 has no quotes to mangle and round-trips intact.
// The node-side reader (internal/lpartinputs.writeJSONField) base64-decodes it.
// Split out so tests can inspect the exact bytes that would be sent.
func (p PinParams) BodyBytes() ([]byte, error) {
	payload := map[string]any{
		"config": map[string]any{
			"node": map[string]any{
				p.Node: map[string]any{
					"disklayout": base64.StdEncoding.EncodeToString(p.Disklayout),
				},
			},
		},
	}
	return json.Marshal(payload)
}

// Validate checks the params carry enough to make the call.
func (p PinParams) Validate() error {
	switch {
	case p.BaseURL == "":
		return fmt.Errorf("lunaclient: missing controller URL (luna.url)")
	case p.Node == "":
		return fmt.Errorf("lunaclient: missing node name (luna.node)")
	case p.Token == "":
		return fmt.Errorf("lunaclient: missing install token")
	case len(p.Disklayout) == 0:
		return fmt.Errorf("lunaclient: empty disklayout")
	}
	return nil
}

// curlArgs builds the argv after "curl". The body is sent on stdin
// (--data-binary @-), so the token header is the only sensitive argument, and
// -o/dev/null -w prints just the HTTP status code on stdout for us to check.
func (p PinParams) curlArgs() []string {
	var args []string
	if p.Insecure {
		args = append(args, "--insecure")
	}
	args = append(args,
		"-sS", "-o", "/dev/null", "-w", "%{http_code}",
		"-X", "POST",
		"-H", "Content-Type: application/json",
		"-H", "x-access-tokens: "+p.Token,
		"--data-binary", "@-",
		"-m", maxTime, "--connect-timeout", connTimeout,
		p.Endpoint(),
	)
	return args
}

// PinDisklayout POSTs the resolved layout to the node-level disklayout via
// curl. A transport failure or a non-2xx HTTP status is returned as an error;
// the caller decides how loud to be (decisions/0008 makes the pin non-fatal).
func PinDisklayout(ctx context.Context, p PinParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	body, err := p.BodyBytes()
	if err != nil {
		return fmt.Errorf("lunaclient: encode body: %w", err)
	}
	//nolint:gosec // G204: fixed 'curl' command; args derive from the Luna cmdline + token, as in internal/lpartinputs.
	cmd := exec.CommandContext(ctx, "curl", p.curlArgs()...)
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("lunaclient: curl POST %s: %w", p.Endpoint(), err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("lunaclient: POST %s: unparseable HTTP status %q", p.Endpoint(), string(out))
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("lunaclient: POST %s: daemon returned HTTP %d", p.Endpoint(), code)
	}
	return nil
}
