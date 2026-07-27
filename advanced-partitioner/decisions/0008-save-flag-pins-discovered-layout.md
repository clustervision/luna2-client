# 0008 — `save` flag pins a discovered layout to the node (write-back)

- Status: Accepted (2026-06-07)
- Deciders: Alex
- Supersedes: parts of [0007](0007-no-daemon-pinback-emit-paste-manual.md) (see "Relationship to 0007")

## Context
Under the v1.4 model a set is resolved one of two ways: the operator lists the
disks explicitly, or the installer chooses them by a match filter at boot. ADR
0007 named the second mode `selection=auto` and decided **not** to write the
resolved layout back to the daemon — it only *emits* a paste-ready
`selection=manual` block to the part log, because auto-writing the node-level
`disklayout` would silently fork every node off its group template (Luna
inheritance shadows the group the instant a node-level field is set).

0007 left two doors open: "a real push-it flag" and "a read-only resolved
layout field." Operator experience since then asked for the first: after an
`auto` install produces, say, RAID1 over sda+sdb, the operator wants that
pinned so the **next** boot reproduces it deterministically — without hand-
copying a log block, and with a trivial way back to discovery. Two further
problems with 0007's vocabulary surfaced:

1. **`auto` is overloaded.** It is both an `install_mode` value ("repair the OS
   set only") and a `selection` value ("resolver picks the disks"). One word,
   two unrelated axes — a steady source of confusion in the spec and the code.
2. **The pin must survive a reimage.** Storing the pin on the node's own disk
   fails the moment `full`/`sanitize` wipes that disk — exactly when you most
   want the layout to be reproduced. Whatever holds the pin has to live where a
   wipe cannot reach it.

A node-local file was considered and rejected for problem 2 (see Considered
Alternatives). The pin must live in controller state.

## Decision

### 1. Rename `selection: auto` → `selection: discover`
The disk-choosing selection mode is now spelled **`discover`**. `install_mode:
auto` is unchanged. This removes the overload: `selection` is `discover|manual`,
`install_mode` is its own axis. The term `auto` no longer appears on the
selection axis anywhere (grammar, validator, accessors, decision engine, spec).

### 2. New per-set boolean `save` (discover sets only)
`save: true` on a `discover` set means: after the installer resolves and applies
the concrete layout, **write that layout back to the node-level `disklayout`**
so the node is pinned for the next boot. Default `false`/absent. `save` is only
valid on a `discover` set; the validator rejects it on a `manual` set (a manual
layout is already the pinned form — re-saving it is a no-op by construction).

### 3. Write-back target and shape
- The resolved layout is rebuilt as a complete `selection=manual` document
  (same builder as 0007's paste block: structural fields copied verbatim,
  devices set to what the resolver chose, `selection`→`manual`, the
  discover-only fields `save`/`count`/`spares`/`match` dropped), plus an
  `origin` provenance object (see 4).
- It is written to **node-level** `disklayout` via the **existing** daemon route
  `POST /config/node/<node>`, body
  `{"config":{"node":{"<node>":{"disklayout":"<json-string>"}}}}`, authenticated
  with the node's install token (`x-access-tokens`).
- **No new daemon route, no schema change.** `token_required` accepts any JWT
  signed with the daemon `SECRET_KEY`, including the node install token already
  used to GET `/boot/install/<node>`; `disklayout` is already a TEXT node column
  updatable through this route (the same path `luna node change --disklayout`
  uses). Verified against the live daemon (common/validate_auth.py,
  common/database_layout.py).

### 4. `origin` provenance stamp
The written-back set carries an `origin` object so an operator can tell a
machine-captured pin from a hand-authored layout:
`{"from":"discover","at":"<RFC3339>","by":"lpart-storage-prepare <ver>","host":"<node>"}`.
`origin` is a known, optional field everywhere in the grammar — it round-trips
when the pinned layout is read back on the next boot, and is never required of
operator input. The timestamp is stamped by the client at write-back time.

### 5. Behaviour, failure, and re-entrancy
- **Gated:** write-back happens only for sets that were `discover` + `save:true`
  and resolved to concrete disks. `manual` sets, memboot, and sanitize never
  write back.
- **Dry-run:** logs the exact POST it would make (URL, node, body) and sends
  nothing.
- **Non-fatal:** a write-back failure (network, auth, daemon error) is logged
  loudly at `err` severity but does **not** fail the install — the data is
  already correctly on disk, and 0007's paste-ready block is still emitted to
  the part log as the manual fallback. Pinning is a convenience, not a
  precondition for a working node.
- **Re-entrant by construction:** the write-back flips the node's set to
  `manual`, and `manual` sets never write back — so a pinned node cannot
  re-trigger a write on subsequent boots. No loop, no churn.

### 6. Revert
To return a node to group-driven discovery, clear the node-level attribute:
`luna node change <node> --disklayout ''`. Luna inheritance then falls back to
the group's `discover` layout. Deleting the pin is the whole revert.

## Relationship to 0007
0007 stands as the `save:false` path: a `discover` set with no `save` still only
emits the paste-ready block and never touches the daemon — its inheritance-fork
argument is exactly why write-back is **opt-in**, not automatic. 0008 supersedes
only 0007's absolute "do not POST the resolved layout back": with `save:true` the
operator has explicitly asked for the node to be forked off its group template,
which is the entire point of pinning. The paste block remains as the fallback
and the visibility artifact.

## Consequences
- **`save:true` at group level self-pins every node.** Because the flag can sit
  on a group's `discover` set, every node in that group writes its own node-
  level `disklayout` on first install. That is deliberate (opt-in) and is often
  what you want for heterogeneous disks — but it is the 0007 "node forks off the
  group" effect happening on purpose, at scale. Operators choosing group-level
  `save:true` are choosing per-node overrides for the whole group.
- The pin survives every reimage mode because it is controller state, not on the
  wiped disk.
- Devices in the written-back layout inherit 0007's open rough edge: they are
  still emitted as the resolver's current paths. Hardening to stable
  `/dev/disk/by-path` (or by-id) selectors is tracked separately and applies to
  both the paste block and the write-back.
- `save` is a third disk-affecting axis alongside `install_mode` and
  `selection`; the spec §5 must teach it next to the data fence so the
  interaction (write-back never wipes; it only records) is clear.
- **The rename is a breaking change for stored layouts.** A `disklayout` already
  persisted with `selection: "auto"` is rejected by the new client at config
  validation (`selection unsupported: 'auto' (allowed: discover, manual)`). There
  is deliberately no `auto`→`discover` leniency alias (it would re-introduce the
  overload this ADR removes); the validation error is self-documenting instead.
  Migration is therefore an explicit step taken **together with deploying the new
  client**: update every group/node `disklayout` that uses `selection: "auto"` to
  `"discover"`. On the current single-controller dev setup this is just node001
  and its group; at fleet scale it is a one-time sweep of the `disklayout`
  attributes. Do not change the stored value ahead of the client deploy, or the
  still-deployed older client (which only knows `auto`) will reject `discover`.

## Considered alternatives
- **Node-local override file** (write the JSON to a surviving partition; presence
  = pin, delete = revert). Rejected: it fights the stateless model — the pin
  lives on the disk being managed, so `full`/`sanitize` erase it precisely when
  the layout should be reproduced; the initramfs would have to read it off a
  disk before partitioning; and "delete to revert" needs a bootable node, so a
  bad pin that breaks boot is unrecoverable.
- **New node-scoped daemon route** (e.g. `POST /boot/disklayout/<node>`).
  Rejected as unnecessary: `token_required` already accepts the node install
  token on the existing `/config/node/<node>` route, which already updates the
  `disklayout` column. A new route would add surface for no gain.
- **Write back at end-of-install instead of at storage-prepare.** Deferred: the
  resolved layout is known at storage-prepare and that is where the paste block
  is already emitted, so co-locating the write-back is simplest. A failed image
  step after a successful pin just means the next retry reproduces the same
  (now `manual`) layout — acceptable. Moving the write-back to a post-install
  success hook is left open if "pin only on full success" is ever required.
