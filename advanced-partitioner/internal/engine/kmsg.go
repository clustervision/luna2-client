package engine

import (
	"bufio"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ReadKmsgBacklog reads the kernel ring buffer that already exists (boot +
// dracut + early-install messages) and returns it as EvKernel events — a
// one-shot "dmesg dump" for the start of the install, so the controller sees
// what the kernel/dracut did before lpart-installer took over. It reads
// records until the backlog is drained (EAGAIN), capped at limit records.
// Best-effort: returns nil if /dev/kmsg is unavailable.
//
// It uses RAW syscalls, NOT os.File.Read. Wrapping an O_NONBLOCK /dev/kmsg in
// an *os.File hands the fd to Go's netpoller; after the backlog is drained the
// poller PARKS the goroutine waiting for /dev/kmsg to become readable again —
// i.e. for the *next* kernel message — which on an idle install node may never
// come. That hung the whole `pre` phase in the real initramfs. syscall.Read on
// the raw fd returns EAGAIN when drained, so the loop terminates. (Verified on
// node001: the pre phase used to sit forever at "capturing boot/dracut kernel
// log"; this is what unblocked it.)
func ReadKmsgBacklog(limit int) []InstallerEvent {
	fd, err := syscall.Open("/dev/kmsg", syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = syscall.Close(fd) }()
	out := make([]InstallerEvent, 0, 256)
	buf := make([]byte, 1<<16)
	for len(out) < limit {
		n, rerr := syscall.Read(fd, buf)
		if rerr == syscall.EINTR {
			continue // interrupted, retry
		}
		if rerr == syscall.EPIPE {
			continue // a record was overwritten mid-drain; the fd advanced
		}
		if rerr != nil || n <= 0 {
			break // EAGAIN (caught up with the backlog) or EOF
		}
		if ev, ok := parseKmsg(string(buf[:n])); ok {
			out = append(out, ev)
		}
	}
	return out
}

// PollKmsg tails /dev/kmsg and emits emerg..info kernel records as EvKernel
// events, so kernel messages (e.g. the ipmi_* module errors that bmcsetup
// triggers, or disk/driver errors) are CAUGHT in the TUI and forwarded to the
// controller instead of scrolling the framebuffer console. Only debug(7) is
// dropped, so the forwarded kernel stream is complete; the renderer colours
// warn/err and shows info plain.
//
// Best-effort: if /dev/kmsg cannot be opened the function returns and no
// kernel events are produced. It seeks to the end first so only messages
// emitted after the installer starts are surfaced (not the whole boot log).
func PollKmsg(ctx context.Context, out chan<- InstallerEvent) {
	f, err := os.Open("/dev/kmsg")
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	// Only future records, not the entire ring buffer replayed at startup.
	_, _ = f.Seek(0, io.SeekEnd)
	// Closing the file on ctx cancel unblocks the (blocking) read below.
	go func() {
		<-ctx.Done()
		_ = f.Close()
	}()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 8192), 1<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		ev, ok := parseKmsg(sc.Text())
		if !ok {
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// parseKmsg parses one /dev/kmsg record ("prio,seq,timestamp,flags;message")
// and returns an EvKernel event for emerg..info records (syslog level <= 6).
// Continuation lines (key=value detail, which don't carry the numeric "prio,"
// header) and debug(7) records return false. Data carries the syslog level so
// the renderer and the forwarder can pick error vs warning vs info.
func parseKmsg(line string) (InstallerEvent, bool) {
	semi := strings.IndexByte(line, ';')
	if semi < 0 {
		return InstallerEvent{}, false
	}
	header, msg := line[:semi], line[semi+1:]
	comma := strings.IndexByte(header, ',')
	prioStr := header
	if comma >= 0 {
		prioStr = header[:comma]
	}
	prio, err := strconv.Atoi(strings.TrimSpace(prioStr))
	if err != nil {
		return InstallerEvent{}, false
	}
	level := prio & 7 // syslog severity: 0=emerg .. 7=debug
	if level > 6 {    // keep emerg..info; drop only debug(7) noise
		return InstallerEvent{}, false
	}
	if nl := strings.IndexByte(msg, '\n'); nl >= 0 {
		msg = msg[:nl]
	}
	return InstallerEvent{
		Time:    time.Now(),
		Type:    EvKernel,
		Stream:  StreamKernel,
		Message: strings.TrimSpace(msg),
		Data:    level,
	}, true
}
