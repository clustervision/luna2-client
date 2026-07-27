# 0005 — Backward-compat mapping from the old two-field model to `install_mode`

- Status: Accepted (2026-06-06)
- Deciders: Alex

## Context
v1.4 replaces the old `install_mode` (`auto|mem|force-init`) × `osimage_sync` (`Sync|Full|Local`)
pair with a single `install_mode` enum ([[0004]]). A cluster migrates node by node, so for a window
both shapes exist: some nodes/groups still carry the old fields, some carry the new `install_mode` +
`disklayout`, and some carry neither. The spec (§7–8) states the *intent* (un-migrated nodes behave as
`legacy`; migration is gradual) but gives no explicit value-by-value translation table. We need one so
the shim is deterministic.

## Decision
Translate old records to the new `install_mode` with this table, applied in `internal/lpartinputs` on the
client and mirrored in the daemon:

| old `install_mode` | old `osimage_sync` | new `install_mode` |
|---|---|---|
| `mem` | any | `memboot` |
| `auto` | `Sync` | `auto` |
| `auto` | `Full` | `full` |
| `auto` | `Local` | `local` |
| `force-init` | `Sync` | `auto` |
| `force-init` | `Full` | `full` |
| `force-init` | `Local` | `local` |
| (no old fields and no new fields present) | — | `legacy` |

Rules:
- `mem` forces a RAM root regardless of the sync axis, so it maps to `memboot` for any `osimage_sync`.
- `force-init` collapses onto the same row as `auto`: v1.4 has no stored-vs-discovered duality
  (`disklayout` is declarative and re-resolved every boot), so "re-run discovery regardless" is just the
  normal `auto` behaviour. The sync axis still selects `auto`/`full`/`local`.
- If a record carries the **new** `install_mode` already, it wins and the table is not consulted.
- A record with neither the old nor the new fields resolves to `legacy` (un-migrated; old scripts run).
- `sanitize` has no old equivalent (it is a new capability) and is never produced by the shim.

## Consequences
- The shim is total: every legacy record resolves to exactly one new mode, and absence resolves to
  `legacy`, so no node is left in an undefined state during migration.
- The old `lpart_*` attributes are kept **read-only** during the migration window (the daemon retains the
  columns for the shim to read); they are retired in a later cleanup once all nodes carry `install_mode`.
- The live controller already has the old `lpart_*` columns applied in-place (un-merged). The daemon patch
  series targets clean upstream HEADs; reconciling the live DB is a separate one-off migration note, not
  part of this mapping.
- One-way only: the shim never writes old fields from new ones.
