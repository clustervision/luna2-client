# 0007 — No daemon POST-back; emit a paste-ready manual layout instead

- Status: Accepted (2026-06-06)
- Deciders: Alex

## Context
Under `selection=auto`, the node resolves a concrete disk layout at boot (e.g.
"RAID1 over /dev/sda + /dev/sdb"). The question: should the resolved layout be
sent back to the luna-daemon so it is visible per node and can be promoted to a
predefined (manual) layout on the next boot?

There is no push-it flag in the v1.4 grammar today, and the old `disklayout`
discovery round-trip was deliberately dropped (ADRs 0003/0006). The client only
ever *GETs* `/config/node/<node>`; nothing POSTs storage state back.

The blocker is Luna's inheritance model: **the moment a field is set at node
level it shadows the group-level value.** Auto-writing a resolved `disklayout`
onto each node would silently fork every node off its group template — so a
later edit to the group disklayout would no longer reach those nodes. That is
operator-hostile and the opposite of why `selection=auto` exists (one group
template, adaptively resolved per node).

## Decision
Do **not** POST the resolved layout back to the daemon. Instead, after an auto
resolution the client **emits** the concrete layout it chose as a complete,
ready-to-paste `selection=manual` disklayout document, written to the part-phase
log and forwarded to rsyslog (cluster-messages), fenced by greppable markers:

```
----8<---- BEGIN <host> disklayout (selection=manual, paste to pin) ----8<----
{ "version": 2, "sets": [ ... selection:"manual", devices:[...] ... ] }
----8<---- END <host> disklayout ----8<----
```

- Structural fields (role, raid, persistent, volumes) are copied verbatim from
  the authored config; the device list is what the resolver actually selected;
  `selection` flips to `manual` and the auto-only fields (count, spares, match)
  are dropped. The result validates as a standalone disklayout.
- Implemented in `cmd/lpart-storage-prepare/pinback.go`
  (`buildPinnableConfig` + `emitPinnableLayout`), called from `main` after the
  resolved/result JSON is written. Diskless resolutions (memboot/sanitize) emit
  nothing.

To pin a node, an operator copies the block from cluster-messages into the
node's (or the group's) `disklayout` — turning a discovered layout into a
predefined one — with no daemon round-trip and no inheritance surprise.

## Consequences
- No node-level shadowing happens behind the operator's back; pinning is an
  explicit, opt-in copy/paste action.
- Visibility + promotion are both served by the same artifact, but as a
  human-mediated step rather than an automatic write.
- **Known rough edge (deferred):** device paths are emitted as the resolver's
  current kernel names (`/dev/sda`); these are not stable across reboots/disk
  swaps. For durable pinning we should emit `/dev/disk/by-path` (or `by-id`)
  instead — `selector.Resolved` does not carry that yet. Tracked in JOURNAL
  "next steps". Acceptable for now: it unblocks regression testing of the
  auto→manual promotion flow.
- A fuller future option (a real read-only "resolved/observed layout" field on
  the node that never participates in inheritance) is left open; this ADR does
  not preclude it.
