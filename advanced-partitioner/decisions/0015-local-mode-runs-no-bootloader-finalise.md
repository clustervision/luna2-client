# 0015 — install_mode local runs no bootloader finalise

Date: 2026-10-28 · Status: accepted

## Context
`local` is "boot the already-on-disk OS, fetch nothing; verify + mount" ([[0004]]), yet the post
phase ran `lpart-bootloader-finalise` for it: fstab rewritten, the initramfs rebuilt, GRUB
regenerated and the NVRAM entry re-registered on a system the operator asked us to leave alone.
A `local` node netboots Luna's kernel and pivots into the disk root, so it never exercises its
own boot chain; damage done there stays hidden until the node first boots from disk. That is
how a wrong-kernel initramfs rebuild went unnoticed for days on a production gateway.

## Decision
The post phase skips the finalise for `local`, as it does for memboot and sanitize.
`InstallsBootloader()` keeps meaning "a disk-based OS mode" and keeps `local`:
`lpart-storage-prepare` plans the boot from it, and the boot safety refusals there must still
apply to `local`.

## Consequences
- A `local` boot leaves fstab, GRUB, the initramfs and NVRAM exactly as they were. Rewriting fstab
  has nothing to correct either: `local` never formats, so the UUIDs already match, and the
  rewrite would reset operator options on installer-owned mounts ([[0013]]).
- `local` no longer repairs a boot chain as a side effect, and switching an OS set to
  `bootloader: none` removes the old bootloader only on the next `auto`, `sync` or `full`
  install. A boot chain that needs repair wants `auto`.
- A node that never had a bootloader does not get one from `local`; the first install is
  `auto` or `full`.
- Pinned by `TestPostPhaseSkipsBootloaderForLocal`.
