# 0012 — RAID: parity arrays take the initial resync; every redundant OS set mirrors its boot chain

Date: 2026-07-10 · Status: accepted

## Context
`mdadm --create --assume-clean` was unconditional. For raid5/6 that marks the array in-sync
with uninitialised parity: any later rebuild or degraded read of a stripe the filesystem did
not fully rewrite reconstructs garbage — silent corruption of real data, verified in the FTA
(C1). Separately, only raid1 OS sets mirrored `/boot`+ESP; a raid5/6/10 root survived a disk-0
loss but the machine could no longer boot (C2).

## Decision
- `--assume-clean` only for non-parity levels (raid0/1/10 — no parity to reconstruct from;
  mirrors copy written data at write time). raid5/6 take the initial background resync; the
  array is usable meanwhile. (A future `--write-zeroes` fast path may replace the resync when
  the fleet's mdadm supports it.)
- `MirrorBoot` applies to every redundant OS RAID mode (raid1/5/6/10): ESP and `/boot` are
  N-way raid1 md arrays (metadata 1.0, firmware-readable members) across ALL set members.
  raid0 stays unmirrored — no redundancy for the boot chain to match.

## Consequences
Node-validated (Phase 8 V11): creation shows a real resync, post-resync scrub `mismatch_cnt=0`,
degraded read of real data correct, rebuild clean. The *boot* half (firmware fallback from a
surviving ESP member after disk-0 loss) is unit-pinned but **needs UEFI hardware validation**
— the lab's nodes are BIOS VMs; this is the top remaining HW item.
