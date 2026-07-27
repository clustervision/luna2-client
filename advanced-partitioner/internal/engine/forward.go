package engine

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// ForwardSyslog streams installer content to a remote rsyslog target live, one
// RFC3164 UDP datagram per forwarded event, while passing every event through
// unchanged (so it chains after TeeToFile: events -> TeeToFile -> ForwardSyslog
// -> TUI). It is the live replacement for luna2-start.sh's end-of-run batch
// `logger -n` forward.
//
//   - addr is the controller target, e.g. "10.141.255.254:514" (luna.loghost).
//   - hostname is stamped as the syslog HOSTNAME field and MUST be the node
//     FQDN (luna.hostname/luna.node from the cmdline). os.Hostname() is
//     "localhost" in the initramfs, which is exactly the misattribution we are
//     fixing, so it is never used here.
//   - phase tags every line ("pre"/"part"/"post") so the controller sees which
//     phase + stage + the message: "lpart-install[part/storage-prepare]: <line>".
//
// Best-effort: a dial or send failure never blocks or fails the install (UDP is
// lossy by design and the local TeeToFile file remains the complete record).
// Frames are never forwarded — TUI box-drawing is not an event in the first
// place; shouldForward selects content + stage + kernel only.
func ForwardSyslog(ctx context.Context, addr, hostname, phase string, src <-chan InstallerEvent) (<-chan InstallerEvent, error) {
	if hostname == "" {
		hostname = "node"
	}
	// A dial failure is non-fatal: we still pass events through, just without
	// forwarding. The UDP "connection" is only a destination; no handshake.
	// net.DialUDP with a pre-resolved address (numeric port) is used instead
	// of net.Dial/DialTimeout deliberately: it avoids net.Dial/LookupPort,
	// and UDP needs no connect timeout.
	var conn net.Conn
	if ua := resolveUDPAddr(addr); ua != nil {
		if c, err := net.DialUDP("udp", nil, ua); err == nil {
			conn = c
		}
	}

	out := make(chan InstallerEvent, cap(src)+16)
	go func() {
		defer close(out)
		if conn != nil {
			defer func() { _ = conn.Close() }()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				if conn != nil && shouldForward(ev) {
					_, _ = conn.Write([]byte(formatRFC3164(ev, hostname, phase)))
				}
				out <- ev
			}
		}
	}()
	return out, nil
}

// resolveUDPAddr parses "ip:port" into a *net.UDPAddr using only pure parsing
// (SplitHostPort + ParseIP + Atoi) — deliberately NOT net.Dial / ResolveUDPAddr
// / LookupIP / LookupPort, which all sit on GO-2026-4971's call graph (a
// Windows-only NUL-byte panic, irrelevant to this Linux installer but flagged
// by govulncheck). luna.loghost is the controller's IP on the cluster network,
// so an IP literal is the expected form; a non-IP host yields nil and
// forwarding is skipped (best-effort). Returns nil on any failure.
func resolveUDPAddr(addr string) *net.UDPAddr {
	if addr == "" {
		return nil
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil // hostname loghost unsupported (avoids the flagged resolver path)
	}
	return &net.UDPAddr{IP: ip, Port: port}
}

// shouldForward selects what reaches the controller: the content set shouldLog
// already forwards to the local file, plus stage transitions and kernel lines.
// TUI frames are not events, so they can never be forwarded.
func shouldForward(ev InstallerEvent) bool {
	if shouldLog(ev) {
		return true
	}
	switch ev.Type {
	case EvStageStarted, EvStageDone, EvStageFailed, EvKernel:
		return true
	}
	return false
}

// formatRFC3164 builds one BSD-syslog datagram:
//
//	<PRI>Mmm dd hh:mm:ss HOSTNAME tag: msg
//
// facility is local0 (16); severity is derived from the event type/level.
func formatRFC3164(ev InstallerEvent, hostname, phase string) string {
	const facility = 16 // local0
	pri := facility*8 + severityFor(ev)
	ts := ev.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	msg := ev.Message
	switch ev.Type {
	case EvStageStarted:
		msg = "stage start: " + msg
	case EvStageDone:
		msg = "stage done: " + msg
	case EvStageFailed:
		msg = "stage FAILED: " + msg
	}
	return fmt.Sprintf("<%d>%s %s %s: %s",
		pri, ts.Format("Jan _2 15:04:05"), hostname, syslogTag(ev, phase), oneLine(msg))
}

// syslogTag is "lpart-install[<phase>/<stage>]" (or "lpart-install[<phase>]" when the
// event carries no stage), so the controller log shows the structure.
func syslogTag(ev InstallerEvent, phase string) string {
	scope := phase
	if ev.Type == EvKernel {
		scope = "kernel"
	} else if ev.Stage != "" {
		if phase != "" {
			scope = phase + "/" + ev.Stage
		} else {
			scope = ev.Stage
		}
	}
	if scope == "" {
		return "lpart-install"
	}
	return "lpart-install[" + scope + "]"
}

// severityFor maps an event to a syslog severity (0=emerg..7=debug).
func severityFor(ev InstallerEvent) int {
	switch ev.Type {
	case EvCommandFailed, EvInstallerFailed, EvStageFailed, EvCommandStderr, EvRawStderr:
		return 3 // err
	case EvKernel:
		if lvl, ok := ev.Data.(int); ok && lvl >= 0 && lvl <= 7 {
			return lvl
		}
		return 4 // warning
	default:
		return 6 // info
	}
}

// oneLine collapses newlines so a single event is a single datagram/line.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimRight(s, " ")
}
