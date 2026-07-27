# 0006 — Drop the lpart_* backward-compat shim

- Status: Accepted (2026-06-06)
- Deciders: Alex
- Supersedes: [[0005]]

## Context
ADR-0005 added a client-side shim (`lpartinputs.MapOldMode`) that translated the
pre-v1.4 `install_mode` × `osimage_sync` pair (and read `disklayout` as
the disklayout) so un-migrated nodes would keep working during a rollout.

On review, the `lpart_*` fields were **never a released model**: they were the
project's own in-flight phase-14 scaffolding, applied only in-place on the dev
controller. No real cluster carries `lpart_*`, so there is nothing to migrate
*from* — the shim is complexity guarding a case that cannot occur.

## Decision
Remove the shim entirely. `install_mode` (default `auto`) and `disklayout`
are the only inputs the client reads.

- Client: delete `MapOldMode` and the `Old*` `lpart_*` fields from
  `internal/lpartinputs`; `writeInputs` reads `install_mode`/`disklayout`/
  `osimage_filter` directly and defaults an absent mode to `auto`.
- Daemon (this controller, in place): drop the five phase-14 `lpart_*` columns
  from `database_layout.py` + the cascade + CLI, and **physically drop** them
  from `luna.db` via a node/group table rebuild (SQLite 3.34.1 has no
  `DROP COLUMN`); add `install_mode`/`disklayout`/`osimage_filter`.
- Upstream patch series (`/root/luna2-v1.4-patches`): targets clean upstream
  HEADs that never had `lpart_*`, so its patches only *add* the v1.4 fields —
  no shim retention needed there either. The README's "old lpart_* kept
  read-only" note is dropped.

## Consequences
- One provisioning path, less code, fewer states to test. The
  `decisions/0005` mapping table is historical only.
- Any stray node that somehow still has only `lpart_*` set resolves to
  `install_mode=auto` with no disklayout → memboot fallback, rather than being
  translated. Acceptable: no such node exists.
