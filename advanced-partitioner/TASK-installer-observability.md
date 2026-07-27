# TASK — lpart-installer observability: stop suppressing the kernel, stream install progress live

> **For:** an agent working on the `advanced-partitioner` lpart-installer (this repo/branch).
> Read `advanced-partitioner/AGENTS.md` + `JOURNAL.md` first for conventions and gates
> (`cd advanced-partitioner && make check` must stay green).
>
> **Why this exists:** a netconsole investigation (clustervision/luna2-client branch
> `trinityx-netconsole`) concluded that kernel netconsole only covers the *pre-installer* sliver
> (kernel init → dracut → the first lines of `luna2-start.sh`) and then goes silent — because
> `luna2-start.sh` and the TUI both **suppress kernel printk**, and the install log is **batch-
> forwarded to the controller only at the end of each attempt** (`cat /tmp/luna_install.log |
> logger -n $loghost`, on success in `luna_finish` / on failure mid-loop). So the controller sees
> *nothing live* during the actual install. The fix belongs in the installer, not in netconsole.

## Goal (the outcomes)

1. **Stop suppressing kernel messages.** The installer must not lower the kernel console loglevel.
   Kernel output stays at full verbosity and is *captured and surfaced*, not hidden.
2. **Push install logs to the controller LIVE, early, and per-step** — not as an end-of-run batch.
3. **Step-by-step progress is visible**: for every phase (pre/part/post) and every `lpart-*` app
   (`lpart-discover`, `lpart-storage-prepare`, `lpart-storage-check`, `lpart-osimage-install`,
   `lpart-bootloader-finalise`), the controller sees the stage transitions and exactly what each app
   prints — stdout, stderr, and kernel messages it triggers.
4. **Forward everything printed to the console EXCEPT the TUI window frames** — i.e. the log
   *content*, never the Bubble Tea box-drawing/redraw bytes.
5. Logs are **attributed to the node** (e.g. `node003.cluster`), not `localhost`.

## How it works today (grounded — read these before changing anything)

**Kernel suppression lives in TWO places — both must go:**
- `internal/engine/printk.go::QuietConsole()` — writes `1` to `/proc/sys/kernel/printk`
  (console_loglevel → near-silent), returns a restore func.
- `cmd/lpart-node-installer/main.go` — `--quiet-console` flag **defaults true** (~line 43) and calls
  `engine.QuietConsole()` (~line 72).
- `…/95luna/luna2-start.sh:31` — `echo 1 > /proc/sys/kernel/printk` (the bash side, before the TUI).

**The event bus already does most of the work:**
- `internal/engine/command.go::RunCommand` runs a command and emits
  `EvCommand{Started,Stdout,Stderr,Done,Failed}` — so each `lpart-*` app's output is already events.
- `internal/pipeline/pipeline.go::runStep` (~line 315) runs every `lpart-*` binary via `RunCommand`
  under a named stage → the per-app, per-stage stream already exists for all of pre/part/post.
- `internal/engine/tee.go::TeeToFile` + `shouldLog()` write **content-only** event `Message`s to a
  file (raw/command stdout+stderr, command + installer lifecycle) — TUI frames are NOT in it. This
  is the content/frame split you want; it just writes to a **local file**, not the controller.
- `internal/engine/kmsg.go::PollKmsg` tails `/dev/kmsg` but emits **only warn+ (level ≤ 4)** as
  `EvKernel`.
- `cmd/lpart-node-installer/main.go` wires it: `QuietConsole()` → `PollKmsg(ctx, kmsgEvents)` (~l.102)
  → `TeeToFile(ctx, *logFile, events)` (~l.109) → TUI consumes.
- `internal/log/log.go` is the structured slog logger (Text/JSON, compact stderr, file sink).

**The forward to the controller (to replace):** `luna2-start.sh` line 22
`exec &> >(tee -a /tmp/luna_install.log /dev/console)` captures the shell's stdout/stderr
(including the TUI frames!) into `/tmp/luna_install.log`, and `luna_finish` (~l.168) / the failure
path (~l.341) batch-send that whole file with `logger -d -n $luna_loghost` (UDP→514). Batch = "too
late", and it carries hostname `localhost` (→ the giant `localhost.messages`).

**Cmdline carries what we need:** `luna.node`, `luna.hostname`, `luna.loghost` (see
`templ_nodeboot.cfg`). `luna.loghost` is the controller's rsyslog target.

## The changes

### 1. Remove kernel suppression
- Flip `--quiet-console` default to **false** and stop calling `engine.QuietConsole()` in the
  normal path (keep the flag/function for an explicit operator opt-in, or delete — your call, but
  default must be "don't suppress"). Update the tests that assert the quiet behaviour.
- Remove the `echo 1 > /proc/sys/kernel/printk` line from `luna2-start.sh` (line 31). Leave a
  comment pointing here.
- **Acceptance:** during an install, `cat /proc/sys/kernel/printk` shows the kernel default (not 1);
  kernel messages appear in `dmesg` *and* are forwarded (task 3).

### 2. Widen kernel capture
- `PollKmsg`/`parseKmsg`: capture **all** levels (or down to a configurable threshold, default
  info/notice — not just warn+), so the kernel stream the controller sees is complete. Render level
  faithfully in the TUI's kernel pane (warn/err coloured; info plain).
- **Acceptance:** an `<6>`-level kmsg line shows up in the forwarded stream and the TUI.

### 3. Add a LIVE syslog forwarder (the core new work)
- Add `internal/engine/forward.go::ForwardSyslog(ctx, addr, hostname, src) (<-chan InstallerEvent, error)`
  — a consumer shaped like `TeeToFile`: for each event that `shouldForward(ev)` accepts, send one
  **RFC3164 UDP syslog** datagram to `addr` (`<loghost>:514`) **live**, and pass the event through
  (so it chains: `events → TeeToFile → ForwardSyslog → TUI`).
  - Write a tiny RFC3164 writer (`<PRI>Mmm dd hh:mm:ss HOSTNAME tag: msg`). **Set HOSTNAME
    explicitly to the node FQDN** (`luna.hostname`/`luna.node` from cmdline) — do NOT use
    `os.Hostname()` (that's `localhost` in the initramfs). `log/syslog` can't set the hostname, so
    a ~30-line custom writer is correct here.
  - Tag each line with phase + stage + command so the controller sees structure, e.g.
    `lpart-install[part/lpart-storage-prepare]: <line>`.
  - `shouldForward` = `shouldLog`’s content set **plus** `EvStage*` and `EvKernel` (stages and
    kernel, yes; TUI frames, never — they aren't events anyway).
  - Best-effort: a send error must never block or fail the install (drop + count, like the cleanup
    cascade). UDP is lossy by design — fine for a live console; the local file (TeeToFile) remains
    the complete record.
- Wire it in `cmd/lpart-node-installer/main.go` after `TeeToFile`, reading `luna.loghost` +
  `luna.hostname` (add a `--loghost`/`--node-name` flag with cmdline fallback for testability).
- **Acceptance:** while an install runs on a node, `tail -f /var/log/cluster-messages/<node>.cluster.messages`
  on the controller shows lines appearing **in real time**, attributed to `<node>.cluster`,
  tagged by phase/stage. (Lands via the EXISTING 514 + `%HOSTNAME%.messages` rule — no netconsole,
  no new rsyslog config needed.)

### 4. Retire the batch forward (and stop teeing frames into the log)
- In `luna2-start.sh`: drop the end-of-run / on-failure `cat /tmp/luna_install.log | logger -n …`
  (now superseded by the live stream). Keep `/tmp/luna_install.log` as the local record, but make
  sure the **TUI frames don't pollute it**: lpart-node-installer renders the TUI to the controlling
  tty (`/dev/console`) while its log/event content goes to the file + forwarder. Verify Bubble Tea
  output is targeted at the tty and is NOT what `exec &>(tee …)` captures into the forwarded log.
- **Acceptance:** `/tmp/luna_install.log` and the controller file contain clean text (no ANSI box
  characters); the dashboard still renders locally.

## Constraints & gotchas
- **Feedback loop:** `PollKmsg` *reads* `/dev/kmsg`. Do NOT route installer output back through
  `/dev/kmsg` (the old `start_kmsg_mirror` idea) — forward via syslog instead. Otherwise the
  installer reads its own writes.
- **TUI flicker:** with printk un-suppressed, kernel lines may briefly scribble the framebuffer;
  Bubble Tea's full-frame redraw overwrites them. If flicker is unacceptable, the *correct* fix is
  to put the TUI on its own VT and direct the kernel console elsewhere — NOT to re-suppress printk.
  Note whatever you choose in the JOURNAL.
- **Attribution:** the forward must stamp the node FQDN as the syslog HOSTNAME, or it lands in
  `localhost.messages` again.
- **Gates:** `make check` green (gofmt/vet/test/lint/govulncheck); new code gets unit tests
  (RFC3164 framing, hostname stamping, `shouldForward` selection, best-effort drop-on-error).
- Library packages must not `os.Exit`; only `main` decides exit codes (see the Go kit).

## Out of scope
- Kernel netconsole (separate, de-scoped — it only covered the pre-installer window).
- Post-`pivot_root` logging (the installed OS owns that).
- Any new controller-side rsyslog config — reuse the existing 514 + `%HOSTNAME%.messages` path.

## Test plan
1. `make check` green; new unit tests pass.
2. Stage into a packable osimage (e.g. compute-image: apply the delta to its `95luna`, `luna
   osimage pack`), PXE a test node, and on the controller `tail -f
   /var/log/cluster-messages/<node>.cluster.messages` — confirm: live per-step lines during the
   install (not just at the end), correct node attribution, kernel messages present, no ANSI noise.
3. Confirm `cat /proc/sys/kernel/printk` on the node is the default (suppression gone).
