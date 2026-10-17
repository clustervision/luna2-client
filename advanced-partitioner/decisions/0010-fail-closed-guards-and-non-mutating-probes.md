# 0010 — Fail-closed guard inputs; health probes never destroy data

Date: 2026-07-10 · Status: accepted

## Context

A fault tree analysis of the installer (see `/root/lpart-review-out/FTA-lpart-current.md`;
summary in the repo review journal) found two recurring defect classes underneath most
data-loss cut sets:

1. **Guard-input tools were interpreted fail-OPEN.** When a probe that feeds a destructive
   decision failed — `findmnt` (mount guard before the sysroot wipe and in the disk selector),
   `blkid` (blank-disk classification), reading `storage-result.json` (the persistent-mountpoint
   list) — the error was swallowed and read as "no obstacle": no mounts, blank disk, no
   persistent sets. Every one of these authorised destruction on missing information.
2. **The health probes MUTATED the volumes they classified.** `e2fsck -y` answers yes to
   destructive repairs; `xfs_repair` auto-escalated to `xfs_repair -L`, which zeroes the
   journal and discards committed in-flight data — executed *inside* `storagehealth.Classify`,
   i.e. before the data fence ever ruled. A fence-protected data set on a power-lost node had
   its journal destroyed by the very check meant to protect it.

## Decision

**One policy for every guard input: uncertain ⇒ refuse the destructive step.**
- A probe error is never mapped to the permissive answer. `findmnt` failure aborts the sysroot
  wipe and refuses the disk; a `blkid` error classifies not-blank (only its genuine exit-2
  "no signature" answer means blank); an unreadable/unparseable `storage-result.json` is an
  exit-2 safety refusal when destructive work would follow.
- Refusals of this class use the exit-code-2 `safety_refusal` contract (`cli.ExitSafetyRefusal`)
  so the boot wrapper parks for an operator instead of retrying forever.

**Health probes are read-only, with one exception: the filesystem's own crash recovery.**
- ext: `e2fsck -f -p` (preen — journal replay and provably-safe fixes only; anything risky
  exits >1 = unhealthy). Never `-y`. A raid data set is assembled read-only, which a preen
  cannot open: it is checked with `e2fsck -f -n` (only rc 0 is healthy).
- xfs: `xfs_repair -n`. A dirty log is recovered by mount+umount (native journal replay, which
  preserves all committed data) and re-checked once. **`xfs_repair -L` is banned** on every
  probe path; a filesystem that journal replay cannot bring clean is reported unhealthy and the
  fence halts for an operator.
- vfat: `fsck.vfat -n` / `fsck.fat -n` (no journal; FAT auto-repair can truncate crosslinked
  files, so no auto-repair).

Rationale for allowing journal replay: it is exactly what the OS performs on the next boot of
that volume, it never discards committed transactions, and without it every ordinary power-loss
would park nodes carrying data sets — a fleet-scale operational cost with no safety gain.

## Consequences

- A merely-dirty filesystem after a crash still classifies healthy (via replay); a genuinely
  damaged one halts instead of being silently "repaired" into data loss.
- Barrier-pinning tests enforce the policy (`fscheck_test.go` asserts `-L`/`-y`/`-a` are never
  invoked by a probe; `storagehealth_test.go` pins the blkid fail-closed branch;
  `extract_barriers_test.go` pins the findmnt fail-closed behaviour). Weakening any of these
  tests is a design regression, not a test fix.
- Repair beyond crash recovery (if ever wanted) must be a separate, explicit operator-invoked
  mode — never a side effect of classification.
