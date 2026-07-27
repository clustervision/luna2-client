# 0004 — `install_mode` as a single seven-value enum; `legacy` is daemon-only

- Status: Accepted (2026-06-06)
- Deciders: Alex

## Context
Provisioning behaviour was selected by two orthogonal inputs: `install_mode` (`auto|mem|force-init`)
and `osimage_sync` (`Sync|Full|Local`), resolved in `internal/installcontext` (a `Mode` enum and a
separate `Sync` enum) and carried as two Luna fields. The pair produced combinations that never made
sense (e.g. `mem` + `Local`, or `force-init` + `Local`) and split a single "what kind of install is
this?" decision across two axes the operator had to keep consistent.

The v1.4 spec (§2, §5) collapses this to **one** ordered attribute, `install_mode`, with seven values:
`auto`, `sync`, `full`, `local`, `memboot`, `sanitize`, `legacy`. `auto` is `sync` plus auto-repair of
the OS set; `full` is `sync` with an empty preserve list; `memboot` runs the OS from RAM; `local` boots
the on-disk OS and fetches nothing; `sanitize` securely erases all local disks for tenant turnover;
`legacy` runs the original bash pre/part/post unchanged for un-migrated nodes.

## Decision
- `internal/installcontext` exposes a single `Mode` enum over the seven values; **the `Sync` enum and
  its plumbing are removed**. `Mode` comes from the Luna `install_mode` attribute; the default is `auto`.
- `legacy` is a **daemon-only concept**. A legacy node keeps its OLD Luna pre/part/post scripts and
  **never invokes `lpart-node-installer`**. The Go client carries no `legacy` execution path — it does not
  shell out to a bash installer. This preserves [[0002]]'s single in-process pipeline (the deleted
  child-process / marker-scraping path is not re-introduced).
- Memboot's RAM-root population moves into the `lpart-storage-prepare` decision engine (mode-driven), using
  the existing `fs.MemoryFilesystems`/tmpfs mechanics, since `provider=memory` left the grammar ([[0003]]).
- Mode-aware refusals are restated against the new enum: `lpart-osimage-install` only extracts under
  `sync`/`full`; `local` routes to `lpart-storage-check`; `memboot`/`sanitize` install no bootloader.

## Consequences
- One field instead of two; the nonsense combinations are unrepresentable.
- The client never needs to understand or run bash: if a node is `legacy`, the daemon simply serves the
  old scripts and the lpart-* binaries are not in that node's flow at all. ADR-0002's cleanliness holds.
- Old two-field records are translated to the new enum by the migration shim ([[0005]]).
- `lpart-storage-check` grows a tri-state verdict (`healthy|blank|broken`) because the new per-mode decision
  tree distinguishes "create on blank" from "halt on broken" (the old binary only signalled pass/fail).
