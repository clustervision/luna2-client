# 0003 — Adopt the v1.4 storage & provisioning model (schema break)

- Status: Accepted (2026-06-06)
- Deciders: Alex

## Context
The node storage/provisioning model was redesigned to **v1.4** (`docs/storage-provisioning-model.md`,
`docs/DESIGN-NOTES.md`, `docs/IMPLEMENTATION_PLAN.md`). The shipped code predates it: storage intent
is expressed through two input paths (a discover config parsed by `internal/intent` + `lpart-discover`,
and a partitioned config validated by `internal/config/v2`), and the v2 grammar carries an `install`
boot block (method/firmware/register_nvram), a per-set `provider` block (type/raid/vg_name, including
`provider=memory` for tmpfs roots), and a `safety` wrapper. The v2 validator is matched **byte-for-byte**
to the Bash oracle `/root/NEWTREE/lib/json_v2.sh` (recorded as parity contract §D.1 in the JOURNAL).

v1.4 reframes this: one declarative `disklayout` config, resolved on the node, with no separate discover
step; boot is **UEFI only** (x86_64 + aarch64 by SBBR), so there is no firmware/BIOS choice; disk
selection moves to tags + size `match`; RAID topology becomes a flat per-set field.

## Decision
Replace the v2 config grammar wholesale to match the v1.4 spec:
- `Config` becomes `{ version, sets[] }`. **Delete the `install`/`boot` block** — UEFI is the only path.
- A `Set` gains `selection` (`auto|manual`), `raid` (`none|0|1|5|6|10`), `count`, `spares`,
  `match` (`tags`, `min_size`, `max_size`, `model`), and a flat `persistent`. **Remove the `safety`
  wrapper and the set-level `provider` block, including the `provider=memory` path** (memboot is now an
  `install_mode`, not a config provider — see [[0004]]). Volumes keep `provider: partition|lvm`.
- New rule: every disk-based OS set must carry a `/boot/efi` vfat partition volume (UEFI requires an ESP).
- **Retire the §D.1 byte-for-byte parity for the config grammar.** The NEWTREE Bash tree stays the
  behavioural oracle only for **storage mechanics** (partition/raid/lvm/fs command order, RAID metadata
  versions, the cleanup md-member-intersection rule, the signed-shim UEFI flow) — not for the config
  schema, which the oracle never modelled.
- Keep the downstream storage builders (`internal/partition`, `raid`, `lvm`, `fs`) untouched by having
  the v2 accessors absorb the change: `Set.RAIDMode()` maps the new flat `raid` to the `"raidN"` form the
  builders already consume; `VGName()` defaults to `Set.Name` (the old `provider.vg_name` is gone);
  `BootMethod()`/`BootFirmware()`/`RegisterNVRAM()` return the UEFI-only constants `"grub"`/`"uefi"`/`"auto"`.

## Consequences
- A clean cutover, not a versioned dual grammar: there is one schema, easier to reason about and test.
  The cost is that any pinned old-grammar `disklayout` JSON is invalid under the new validator — handled
  by the migration shim ([[0005]]) and the daemon defaulting un-migrated nodes to `legacy`.
- The `internal/intent` discovery grammar and `lpart-discover` are removed (the RAID-tolerance + size-parse
  helpers are salvaged into the new `internal/diskresolve`).
- Tests inherited from the Bash validator corpus are replaced by a v1.4 corpus (valid per
  raid/selection/match; invalid: bad selection, bad raid, count mismatch, auto-without-raid,
  manual-without-devices, OS-without-ESP). Validation error strings are no longer constrained to match
  Bash wording.
- The storage stack below the decision layer compiles and runs unchanged because the accessor layer
  presents the same shapes it always did.
