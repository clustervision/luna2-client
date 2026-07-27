# Implementation Plan, Storage and Provisioning Model v1.4

Companion to [`storage-provisioning-model.md`](storage-provisioning-model.md). That document
is the spec; this is the build order. Phases are sequenced to keep `make check` green and to
allow incremental landing. Two repos are touched: luna2-client/advanced-partitioner (the node
side Go installer) and luna2-daemon (server fields and inheritance). luna2-utils is not touched.

Field names follow the spec: a set has `selection` (discover or manual), `raid`, `count`, `spares`,
`match`, `devices`, `save`, `persistent`, `origin`, `volumes`. The whole storage config is the Luna `disklayout`
attribute. The mode is the Luna `install_mode` attribute. Boot is UEFI only, so there is no
firmware or boot field.

Legend: file paths are relative to `advanced-partitioner/` unless noted.

> **STATUS (2026-06-06): IMPLEMENTED.** Phases 0–6 (Go client) are built and `make check`-green on
> branch `advanced-partitioner`; phases 7–8 (daemon + CLI) are staged as a patch series at
> `/root/luna2-v1.4-patches`. Some phase boundaries shifted during implementation (the schema break
> forced deleting lpart-discover/intent in phase 2; the install_mode rewire folded the compat shim into
> phase 3). See the JOURNAL "Status (2026-06-06) — v1.4 client IMPLEMENTED" entry for the as-built map
> and the remaining work (apply server patches, on-node regression, openSUSE/Ubuntu gate).

---

## Phase 0, baseline
- Confirm `make check` (gofmt, vet, test, golangci-lint, govulncheck) is green on
  `advanced-partitioner` before starting.
- Snapshot the `internal/intent` RAID tolerance helper to salvage in Phase 1.

## Phase 1, discovery tags and the disk resolver
Goal: a self contained, unit tested resolver, no wiring yet.
- `internal/discover/discover.go`: add `Tags []string` to `Device`, derived at list time from
  `lsblk` ROTA and TRAN. A rotational disk gets `rotary`. A non rotational disk gets `ssd`. An
  NVMe disk also gets `nvme` (so NVMe carries both `ssd` and `nvme`). Call `ListDevices` with no
  size fences.
- New package `internal/diskresolve` (kept distinct from the existing `partition.Layout`):
  - `Raid` type with values `none, 0, 1, 5, 6, 10`; `MinDisks()`, `MaxDisks()`, `mdadm` level.
  - `Match{ Tags []string; MinSize, MaxSize int64; Model string }` plus a `tags:size` parser.
  - `Resolve(pool []discover.Device, m Match, count, spares, tolerancePct)`: filter by tags
    (all listed must be present), by min and max size, and by model, then sort smallest first,
    take count plus spares, then run the salvaged RAID tolerance check.
- Tests: tag derivation, the ssd and nvme overlap, raid count rules, match filtering, tolerance,
  not enough disks.

## Phase 2, v2 schema and validation
- `internal/config/v2/types.go`:
  - `Config` is `{ version, sets[] }`. There is no install block (UEFI only).
  - `Set` gains `Selection` (auto or manual), `Raid`, `Count`, `Spares`, `Match`, and a flat
    `Persistent bool`. JSON keys `selection`, `raid`, `count`, `spares`, `match`, `persistent`.
    Keep `Devices` and `Volumes`. Remove the old `Safety` wrapper and the `provider=memory` path.
- `internal/config/v2/validate.go`:
  - `selection` is discover or manual.
  - `raid` is one of none, 0, 1, 5, 6, 10. Counts: 0 and 1 need 2, 10 needs 4, 5 needs 4 or 5,
    6 needs 5 or more, none means 1.
  - `selection=discover` needs a `raid` and may carry a `match`; `save` is valid only here.
    `selection=manual` needs `devices[]`. A discovered set can be written back as manual (ADR-0008).
  - `role` is os or data. Exactly one os set for the diskful modes. memboot may carry data sets
    only.
  - Drop all firmware, boot method, and tmpfs or memory rules.
- `internal/config/v2/accessors.go`: a set's RAID level comes from `Raid`. Add `Match`,
  `Selection`, `Persistent` accessors.
- testdata: valid per raid and selection and match. Invalid: bad selection, bad raid, count
  mismatch, auto without raid, manual without devices.

## Phase 3, install_mode and the decision engine
- `internal/installcontext/types.go`: `Mode` is one of auto, sync, full, local, memboot,
  sanitize, legacy. Remove the separate Sync enum. Mode comes from the `install_mode` attribute.
- `cmd/lpart-storage-prepare/main.go`: implement the section 5 trees:
  - dispatch: legacy short circuit (Phase 4); sanitize does a secure erase of all local disks
    then stops; memboot populates a RAM root and mounts any data sets; local verifies and mounts;
    full, sync and auto drive the disks.
  - auto: if no os layout is declared, memboot; otherwise resolve the os set with
    `internal/diskresolve`; if it cannot be met, halt.
  - per set apply: persistent is mount only; `lpart-storage-check` is the health probe; sync creates
    on blank and halts on broken; auto repairs the os set only; full wipes; the data fence holds.
- Reuse the existing `partition`, `raid`, `lvm`, `fs` builders below the decision layer.

## Phase 4, OS content and legacy
- `cmd/lpart-osimage-install/main.go`: sync converges and preserves using `osimage-filter.json` and
  deletes leftover files; full extracts in full with the filter ignored.
- legacy: `lpart-node-installer` sees `install_mode=legacy`, runs the original bash pre, part and
  post, and does a full unpack with the filter omitted. The storage config is not consulted.

## Phase 5, remove lpart-discover and rewire pipeline, TUI, inputs
- Delete `cmd/lpart-discover/` and `internal/intent/` (tolerance already salvaged).
- `internal/pipeline/pipeline.go`: drop the discover stage and the route branch; storage prepare
  always runs and reads `install_mode`.
- `internal/engine/stages.go`, `internal/tui/view.go`: remove the lpart-discover stage.
- `internal/lpartinputs/lpartinputs.go`: read one `install_mode` and the `disklayout` config; drop the
  old discover config and image sync fields.
- `Makefile`: remove `lpart-discover` from `CMDS`.

## Phase 6, backward compatibility shim
- Map old records, the old install mode crossed with the old image sync, onto the new
  `install_mode` per spec section 8. An un migrated node, old scripts and no storage config,
  becomes legacy. Implement in `lpartinputs` on the client and mirror it on the daemon.

## Phase 7, luna2-daemon (separate MR)
- DB: add `install_mode` and `disklayout` to both the group and node tables, plus the
  `osimage_filter`.
- `daemon/base/node.py::get_node()`:
  - join: add `group.install_mode AS group_install_mode` and the disklayout and filter aliases,
    near the existing `group.partscript` aliases.
  - cascade: add `install_mode` to the scalar `items` dict; add `disklayout` and `osimage_filter`
    to the base64 `b64items` dict, the same path the scripts already use.
- Retire the old image sync and discover config attributes (or keep them read only for the shim).
- Migration script and defaults: new objects get auto; un migrated nodes get legacy.

## Phase 8, CLI (luna2-cli)
- `--install-mode` with auto, sync, full, local, memboot, sanitize, legacy on node and group
  change.
- `disklayout` passthrough, a group default with a per node override.

## Phase 9, tests, docs, gates
- `go test ./...`; `make check` green.
- Update `README.md`; keep this plan and the spec in sync.
- Cross distro packaging build (redhat, opensuse, ubuntu) still succeeds.

---

## Verification
- Unit: `internal/diskresolve` (tags, match, tolerance) and `internal/config/v2` (valid and
  invalid corpus).
- Dry run per mode with a faked device pool: auto (mirror install, memboot fallback,
  halt on unmet), sync (halt on broken), full (wipe), local (verify), memboot (RAM), sanitize
  (secure erase).
- Data fence: a persistent set is never wiped by auto, sync or full; a data set halts under auto
  or sync when broken; only sanitize erases a persistent set.
- End to end on node001: a raid1 os by discovery and tags, a pinned persistent data array,
  memboot, a memboot node with a persistent data set, and a legacy node that behaves as before.

## Notes
- Daemon migration is the highest risk step, so land it behind the shim with un migrated nodes
  defaulting to legacy.
- The build box SSH key authenticates as omar, so verify access before pushing to the alex fork.
- Keep the resolver package name distinct from the existing `partition.Layout`.
- Tag derivation and the `lsblk` flags must hold on RHEL, openSUSE and Ubuntu.
- Operator assigned custom disk tags are out of scope here, tracked as a roadmap item in the spec.
