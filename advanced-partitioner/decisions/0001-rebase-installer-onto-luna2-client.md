# 0001 — Rebase the Go localboot installer onto clustervision/luna2-client

- Status: Accepted (2026-06-03)
- Deciders: Alex

## Context
The Go localboot installer (disk discovery, partitioning/RAID/LVM, OS-image install, bootloader
finalise) was developed in the personal namespace as `alex/installer-go`, with its Bash behavioural
oracle in `/root/NEWTREE` (personal `trx-partitioning`). Nothing for it existed under
`clustervision/`. The **production** node-side installer — the `95luna` dracut module — lives in
**`clustervision/luna2-client`** (`…/src/usr/lib/dracut/modules.d/95luna/luna2-start.sh`, the
*network* installer). The localboot/disk-install pipeline is a **new capability** that belongs with
the node-side client, not in a personal repo.

## Decision
Fold the Go installer into **`clustervision/luna2-client`** as a single top-level component,
**`advanced-partitioner/`** (one cross-compiled Go module; not duplicated per distro/arch). Base the
work on the repo's integration branch **`main`** (this repo has no `development`), on the feature
branch **`advanced-partitioner`**. Adopt the engineering-standards project skeleton: repo-root
`AGENTS.md`, `advanced-partitioner/JOURNAL.md` (the running log, formerly `BOOKKEEPING.md`),
`decisions/` (this ADR). The personal `alex/installer-go` git history is **not** carried over — this
is a clean restart ("start over, move the code in").

## Consequences
- The installer now ships from its production home and lands via luna2-client's MR flow.
- Build/packaging into the per-distro/arch `create_rpm.sh`/`.spec` is **not yet wired** — that is the
  next task (cross-compile the Go binaries, install them via each target's spec into the node image).
- The Go module path is still `gitlab.taurusgroup.one/alex/installer-go`; rename to the
  luna2-client-owned path once the home is final (a follow-up; functional as-is since binaries build
  locally).
- The Bash oracle stays at `/root/NEWTREE` (personal) for now; revisit if it needs a clustervision home.

## Alternatives considered
- **New standalone `clustervision/` repo** — cleanest separation, but needs org access to create
  (push-to-create disabled; the `alex/`-only constraint blocked it) and splits the installer from the
  client it ships with.
- **Keep it in `alex/installer-go`** — no clustervision home; not shippable through the normal flow.
