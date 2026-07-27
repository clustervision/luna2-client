# 0013 — fstab: entries for installer-owned mounts are authoritative (replace, don't skip)

Date: 2026-07-10 · Status: accepted

## Context
`WriteFstab` appended only entries whose mountpoint was not already present, "preserving
existing lines". Against a golden image that ships its own fstab (any cloud image — reproduced
with a real Ubuntu 24.04 rootfs in Phase 9) this preserved the IMAGE's root entry
(`LABEL=cloudimg-rootfs /`), so the installed node's real root UUID was never written and the
node dropped to the emergency shell at boot (the ancestor FTA's TE-3 MCS-6, still live here).
TrinityX images ship an empty fstab, which is why the Rocky path never surfaced it.

## Decision
Replace-by-mountpoint: fstab lines for mounts THIS install created/reformatted are regenerated
authoritatively (their previous UUID/label is dangling by construction — mkfs minted a new
one), exactly like grub.cfg. Lines for mounts outside the layout — operator additions —
survive verbatim. Re-runs stay idempotent.

## Consequences
An operator's option-tuning on an installer-owned mount is rewritten to defaults on reinstall;
persistent-set volume options belong in the disklayout (`volumes[].options`), which is the
supported channel. Pinned by `TestWriteFstabReplacesStaleImageEntry` and the updated
re-entrancy test; verified against the real Ubuntu tree on node001.
