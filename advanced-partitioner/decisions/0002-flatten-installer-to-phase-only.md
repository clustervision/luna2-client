# 0002 — Flatten the installer to a `--phase`-only pipeline

- Status: Accepted (2026-06-04)
- Deciders: Alex

## Context
The installer carried two ways to drive the TUI. The **original** design ran an external
installer (bash) as a child process and scraped its `TX_*` stdout markers to populate the
dashboard (`internal/discovery` markers + state-file overlay, `engine.Runner`, `engine.PollStateDir`,
`lpart-node-installer --installer/--fake/--stages/--state-dir`). The Phase-14 **in-process** design
(`lpart-node-installer --phase pre|part|post` → `internal/pipeline` → lpart-* via `engine.RunCommand`,
emitting typed events) replaced it, but the old machinery was never removed. Nothing on the live
path used the child-process branch — every Luna hook shim execs `--phase`.

Two more redundancies had accumulated: `runPart` exec'd `lpart-install-context` to round-trip
`install-context.json` onto itself even though `lpartinputs.Resolve` already wrote it and every lpart-*
binary reads+validates it via `installcontext.LoadFile`; and the three Luna hook scripts were
near-identical 14-line copies.

## Decision
Make `lpart-node-installer` **`--phase`-only** and delete the dormant viz-wrapper:
- Remove the child-process branch + `internal/discovery` (markers + state files), `engine.Runner`,
  `engine.PollStateDir`, and the `--installer/--fake/--stages/--state-dir` flags. Typed events from
  the pipeline are the sole TUI data source. (No-node TUI dev render is covered by the snapshot tests.)
- **Retire `lpart-install-context`** (7→6 lpart-* binaries). `lpartinputs.Resolve` is the sole writer of
  `install-context.json`; downstream tools validate on read.
- Single-source the hook shims: ship `/usr/bin/lpart-phase` from the 95luna dracut module; each Luna
  prescript/partscript/postscript field is `exec /usr/bin/lpart-phase <phase>`.
- Remove the retired HTTP-toolbox path (`scripts/upload_toolbox.sh`, Makefile `toolbox` target);
  quarantine the dev hot-swap to `scripts/dev/`. Cut 10 unreachable funcs (kept the tested ones).
- Rename the Go module path `…/alex/installer-go` → `…/clustervision/luna2-client/advanced-partitioner`.

## Consequences
- The conceptual model is one path: Luna hook → `lpart-phase` → `lpart-node-installer --phase` → pipeline →
  lpart-*. The "external installer + marker scraper" indirection is gone.
- Behaviour-neutral: `make check` green; validated on node001 with a PXE RAID1 install on
  luna2-client **2.0-33** (`install.success`, 3× RAID1 `[2/2][UU]`, `os-root` xfs).
- `lpart-install-context` is gone; an operator who wants to override install-mode by hand edits the
  Luna `install_mode`/`osimage_sync` fields (or `install-context.json`) directly.
- Pre-existing node-level `--phase` shim overrides keep working unchanged. To use the new `lpart-phase`
  shape, push the one-line group shims (`runtime/group/compute-group/*.sh`) to Luna.
