# LOOSE-ENDS.md — open work for the new installer (lpart / advanced-partitioner)

> Catalogue of everything still open for the v1.4 Go installer, so the next session can pick
> up without re-deriving it. Compiled 2026-06-07 from a full sweep of the code, JOURNAL.md,
> decisions/ (ADRs), regression.md, LEGACY.configs, and the staged daemon patches at
> `/root/luna2-v1.4-patches`. Items tagged `[HW-confirmed]` were proven on node001 this session;
> the rest are inventory. Keep this file additive; tick items off as they land.

## Tier 1 — blocks shipping beyond redhat/x86 (merge gate)

1. **Open the daemon MR.** The entire lpart daemon integration is APPLIED LIVE on this
   controller but NOT upstream. `clustervision/luna2-daemon:development` (cfbf7c4) has zero lpart
   awareness (no `install_mode` lever, no `templ_install_lpart.cfg`, no `write_provisioning_inputs`,
   no `disklayout`/`osimage_filter` fields). Staged: `/root/luna2-v1.4-patches/patches/01..08` +
   `migration/001-add-v1.4-fields.sql` + `legacy/` (boot.py lever + the two templates). Human MR
   only (clustervision). See `/root/luna2-v1.4-patches/*/README.md` for the MR shape.
2. **openSUSE bootloader is a stub.** `internal/bootloader/suse.go` returns "not implemented" for
   Prepare/Install/RegenerateInitramfs/Validate. Either implement it or refactor onto the shared
   `StageSignedShim` the way debian.go did.
3. **Only redhat/x86 is built + wired.** `redhat/arm`, `opensuse/{x86,arm}`, `ubuntu/{x86,arm}`:
   `create_rpm.sh`/`create_deb` do not run `make build-linux`, `95luna/module-setup.sh` has no
   `dracut_install lpart-*` / `inst_simple lpart-phase`, and `.spec`/DEB control do not list the
   binaries. The lpart binaries never reach those initramfs. Fan out only after each target is
   on-node tested.
   **[2026-06-07] Ubuntu/x86 proven wireable on node001:** lpart binaries + `lpart-phase` + a
   self-contained `initramfs-tools` `hooks/lpart` (full command set) baked into an Ubuntu 26.04
   image → the new installer ran end to end (`discovered → partscript → success`). **One blocker
   remains (see #3b).** Done only in the throwaway `ubuntu-2604-test` clone; fold into
   `ubuntu/x86/src/.../hooks/lpart` + DEB packaging once #3b is fixed.
3b. ~~**Pivot target is dracut-only — blocks Ubuntu/non-dracut boot.**~~ **RESOLVED 2026-06-08.**
   `mount.EffectiveSysroot()` now honours `$rootmnt` (else `/sysroot`), wired into `mount.New()` +
   `lpart-osimage-install --sysroot`. dracut behaviour unchanged; initramfs-tools follows `/root`.
   **Ubuntu 26.04 memboot GREEN on node001 + node002** (root=tmpfs, disk untouched). ubuntu/x86 repo
   wiring landed (hook + lpart-phase + create_deb stage + control Depends). See JOURNAL 2026-06-08.
   Remaining for full fan-out: ubuntu/arm + opensuse (still untested HW), and the deb actually built
   on an Ubuntu host (create_deb wiring added but built only by hand-injection so far).
4. **`luna2-start.sh` retry/fatal logic is redhat/x86 only.** `fatal_park` + `retry_countdown` +
   `LUNA_FATAL_RC=3` live only in `redhat/x86/.../95luna/luna2-start.sh`; the other three trees
   still have the old loop.
5. **Cross-platform on-node validation.** No ARM, openSUSE, or Ubuntu hardware run exists. Min bar
   per target before merge: mem, single-disk, RAID1.

## Tier 2 — daemon follow-ups + write-back

6. **`exit 1` -> `exit $LUNARET`** in the daemon templates' pre/part/post handlers — **APPLIED
   2026-07-11** to templ_install_lpart.cfg (in v14-daemon-reconciled bundle + deployed); validated
   end-to-end (sanitize+persistent -> rc 2->3 -> exit \$LUNARET -> fatal_park). Was NOT (specified in `/root/luna2-v1.4-patches/legacy/README.md`). Without it the client's
   fatal exit code 3 cannot reach `luna2-start.sh`, so `fatal_park` stays dormant and a fatal
   legacy error still loops.
7. **Save-flag write-back (ADR-0008) not wired.** `selection: discover` + `save:true` is designed
   and `internal/lunaclient` has a stub, but the actual `POST /config/node/<node>` pin-back is not
   called; resolved layouts are only emitted as paste-ready blocks to the log. Wire the POST +
   pipeline call + an end-to-end pin-and-retry test.
8. **ADR-0007 device paths are kernel names** (`/dev/sda`), not `by-id`/`by-path`; a pinned layout
   can drift across disk renames. `selector.Resolved` does not carry a stable ID field yet.

## Tier 3 — implemented but never hardware-proven

9. **Modes with no HW test:** `sync` on healthy (§3.6) and broken (§3.7), `memboot`+persistent
   (§3.10), persistent survives `full` (§3.11), **`sanitize` (§3.13)**, deliberate-failure/retry
   (§3.18), osimage filter shapes (§4.1/§4.2). Most are cheap + safe on a recoverable node.
   **[HW-confirmed] basic `memboot` proven on node001 (2026-06-07):** PXE → `lpart-phase memboot`
   → OS from RAM (`root=tmpfs`) in ~30 s, node healthy, prior RAID1/LVM left intact (no disk
   write). Only the `+persistent` variant (§3.10) remains for memboot. (See JOURNAL 2026-06-07.)
10. **RAID levels:** **RAID10 never run**; RAID5/6 only as data sets with a mem OS. RAID0 is
    rejected by design.
11. **§3.4b degraded RAID1 sda-leg boot — PARKED.** sdb-pull boots degraded; sda-pull lands at
    `grub>` (grub.cfg resolves by `mduuid`, needs array assembly). Candidate fix (post-process to
    plain fs-uuid) untried. The "either leg boots alone" HA property is unproven for lpart.
12. **§3.19 discover+save and §3.20 re-entrancy battery — DEFERRED** (need a recoverable node;
    memboot does not stage the token for the post-phase re-entrancy). cleanup/mount/fstab/
    bootloader/osimage re-entrancy is unit-tested only, not proven across real reboots.
13. **ARM end-to-end:** binaries cross-compile and shim naming is unit-locked (`BOOTAA64.EFI`),
    but no real ARM hardware has booted it.
14. **[2026-06-08 regression — FIXED] `bootloader_verify` ran `efibootmgr` on the host initramfs**,
    which has no `efibootmgr`, so a real UEFI disk install FAILED at post (verify → exit 1 → retry
    loop) even though the NVRAM write (chroot `/sysroot`) succeeded. Fixed: `verify.go` now reads
    NVRAM via `chroot /sysroot /usr/sbin/efibootmgr` (mirroring the write; efivars is bind-mounted
    in for the finalise), and skips the check (non-fatal) when the image has no `efibootmgr`. Found
    on node001 full RAID1; unit-tested. **Re-verify on HW.**
15. **[2026-06-08 regression — FIXED] storage cleanup re-entrancy (3 gaps).** Reinstalling a node
    with a prior LVM-on-RAID failed at `lvcreate` (exit 5). Root cause: `teardownVG` ran
    `dmsetup remove` *before* LVM deactivation, so an open `<vg>-<lv>` node survived and the next
    `lvcreate` collided with the orphaned mapper name. Fixed in `c69b24b`: (a) deactivate (vgchange
    -an) before dmsetup remove + `--retry`; (b) `purgeVGsByName` also removes duplicate-named VGs by
    UUID (stale "os" on non-layout disks); (c) `EnsureNoLVMAutoActivate` (lvmlocal.conf) stops udev
    pvscan re-activating a stale VG mid-cleanup. Unit-tested (order guard + dup purge + guard).
    **Re-verify on HW by reinstalling a node that already has an "os" VG (no manual wipe).**

## Tier 4 — open design questions (from the dev Q&A, 2026-06-07)

14. **Custom non-disk pre/post.** The node pre/part/post FIELD is now the lpart shim; custom
    finalize logic must move to a `--scripts` plugin (the template still runs `customscript`
    after the shim). bmcsetup is a template function and is unaffected. This migration contract
    is not yet documented/decided — decide and document it.
15. **group->node resolution of `disklayout`/`osimage_filter`.** The client consumes the
    *effective* value from the node-detail API; confirm the daemon resolves group->node for these
    fields server-side (node001 only ever used node-level overrides).
16. **Discovery surface.** Only part-phase discover+save exists; no standalone "scan the node"
    (network/disks) API. Decide whether one is wanted (3rd endpoint) or it stays embedded.
17. **Secure boot / iPXE shim menu.** localboot works via firmware BootOrder + iPXE `sanboot`
    (templ_boot_disk.cfg); secure boot through the chainloaded shim is not formally addressed.
    Daemon/iPXE-layer concern, not lpart.

## Tier 5 — legacy fixes still real upstream (re-baselined vs development cfbf7c4)

18. **F1 — legacy `raid1.py` is NOT disk-fault-tolerant.** [HW-confirmed] dev raid1 copies the ESP
    to both disks but `/boot` (kernel) is on sda2 only and the ESPs are unsynced -> pulling sda
    bricks to the EFI shell. Proper fix = RAID the whole boot chain (ESP md metadata 1.0 + /boot +
    root), the lpart model. File upstream.
19. **F3** nodhcp still FAT16; **F4** parted `name 1"` typo in diskfull+nodhcp; **F7/F9** device-name
    fstabs (nodhcp/cloud); **F2** doc RAID1 example bug. (F3/F4/F6 already fixed in development for
    diskfull/raid1.) Tracked by `scripts/dev/legacy-lint.sh`; see LEGACY.configs F11 re-baseline.
20. **`hostnamectl`-in-chroot** prints a benign systemd-bus error on every install (cosmetic;
    prefer writing `/etc/hostname` directly).

## Tier 6 — spec §9 roadmap (designed, not built)

21. Drift detection + Prometheus alerting (the separate `luna-drift` project).
22. Declarative API / GitOps custom resource.
23. Measured boot + attestation (TPM token exists for auth only).
24. Operator-assigned custom disk tags (auto `rotary`/`ssd`/`nvme` only today).
25. `sanitize` crypto-erase (`nvme format --ses` / `hdparm --security-erase`); today blkdiscard +
    wipe+zap.
26. Live kernel->loghost syslog forwarding (kmsg capture done; live `ForwardSyslog` to loghost:514
    + dropping the `QuietConsole` default is TODO — JOURNAL "Next actions 6").

## Suggested first five to act on
1. Commit + back up the working tree (this commit).
2. Open the daemon MR (patches 01-08 + legacy lever/regex + the `exit $LUNARET` follow-up).
3. Wire + on-node test ONE non-x86 target and de-stub SUSE.
4. Run the cheap, safe HW gaps (`sync` healthy/broken, sanitize, retry, filter shapes) on a
   recoverable node.
5. Wire the save-flag write-back (ADR-0008).

## 2026-07-10 — Stabilisation campaign (FTA barriers): status updates

A full fault-tree analysis + barrier implementation + node validation landed on the local
branch `advanced-partitioner-stable` (see JOURNAL 2026-07-10, ADRs 0010-0013, and the campaign
artifacts at `/root/lpart-review-out/`: `FTA-lpart-current.md`, `MASTER-PLAN.md`,
`HANDOFF-next-llm.md` — the last is the dev handover). Effects on this catalogue:

- **#2 (SUSE bootloader stub): CLOSED at code level.** Real adapter composing the RHEL flow +
  unit suite (`suse.go`, `suse_test.go`). On-SUSE-image run still pending (Tier 3 class).
- **#15 (cleanup re-entrancy): re-validated on node001** (repeated creates over existing
  os/data VGs, no lvcreate collisions) — plus a new protected-disk fence on every teardown path.
- **§3.10 memboot+persistent, §3.6-ish refresh, sanitize refusal: node-validated** (Phase 8
  matrix V16/V13/V9a). `sanitize` now REFUSES when the disklayout declares a persistent set.
- **#8 (ADR-0007 kernel-name pinning): pinned layouts now emit /dev/disk/by-id (wwn preferred)**
  for discovered sets when the links exist (virtio lab has none — real-HW check pending), and
  storage-prepare re-verifies disk serials immediately before destructive work.
- **NEW safety invariants (do not weaken the pinning tests):** persistent mountpoints preserved
  by identity through the sysroot wipe; fail-closed guard inputs (findmnt/blkid/storage-result);
  cross-set device reservation + protected-disk cleanup fence; non-mutating health probes
  (xfs_repair -L banned); exit 2/3 park contract; dry-run touches nothing (fsops chokepoint);
  raid5/6 without --assume-clean; mirrored boot chain for all redundant OS modes; fstab
  replace-by-mountpoint (ancestor MCS-6, reproduced on real Ubuntu content and fixed).
- **⚠ 2026-07-11 — UEFI IS NOW REQUIRED; BIOS removed.** The installer refuses any non-UEFI node
  at a firmware gate in `lpart-storage-prepare` (exit-2 `safety_refusal=firmware-not-uefi`), with
  belts in `partition.BuildLayout` and the bootloader adapters; the BIOS execution paths
  (`installBIOS`, EF02/BIOSBOOT) are deleted. **Consequence: the demo lab (BIOS VMs) will now be
  refused** — flip the node VMs to **q35 + OVMF/edk2** before any further on-node lpart regression.
  See JOURNAL 2026-07-11. This makes item #18/F1 (legacy raid1 /boot brick) moot for lpart (still
  real for the legacy bash path).
- **Top remaining HW items (unchanged in kind, sharpened in priority):**
  1. UEFI boot-chain validation: boot after disk-0 pull for raid1/5/6/10 (**lab must move to UEFI/OVMF first — see the BIOS-removed note above**);
  2. full wrapper park behaviour (rc 2/3 → fatal_park) on an lpart-wired PXE controller;
  3. distro min-bar boots (Ubuntu/Debian/SUSE memboot / single-disk / raid1);
  4. by-id pinning on hardware with real by-id links.

- ~~[2026-07-11] sanitize exits 1 (retry-loop) after a clean erase~~ **FIXED 2026-07-12 (FTA v2 NB-1)**: empty results = success for sanitize, exit 0; pinned by TestSanitizeWithNoResultsExitsZero; was: — `install_mode=sanitize` blkdiscards all disks then `lpart-storage-prepare` exits 1 on `result.WriteAuto: no results`, so luna2-start.sh retry-loops instead of stopping. Fix: sanitize path should exit 0 (no-results is success for sanitize). Found in the UEFI regression on node001.

- **[2026-07-12] CONFIRMED (definitive): parity RAID (raid5/6) degraded boot fails at `grub>`.**
  Clean single-install check (raid5 incl. vda; pull a NON-boot leg so vda's ESP SURVIVES): still
  `grub>` (OVMF loads `TrinityX vda` → GRUB → rescue prompt, never reaching the kernel). Healthy
  raid5/6 install + boot works. RAID1 degraded works (3.28). grub.cfg is ALREADY fs-uuid-based
  (grep mduuid = 0), so `makeGrubCfgDegradedSafe` has nothing to fix — the parity failure has a
  DIFFERENT cause (GRUB cannot resolve the boot chain on a degraded parity array). Earlier
  "degraded booted" reads from the parallel driver were UNRELIABLE (the driver did not reset OVMF
  NVRAM before the pull, so it booted a stale leftover install — "marker lost"). => lpart's C2
  mirrored-boot fault-tolerance is REAL for raid1 (proven); raid5/6 are NOT boot-fault-tolerant
  despite the mirrored ESP. raid10 (mirror-based) is EXPECTED to boot degraded like raid1 but the
  clean confirm is a follow-up. Guidance until fixed: use raid1/raid10 for boot-fault-tolerant OS
  sets. Fix is non-trivial (deeper GRUB degraded-parity investigation). Reopens+extends v1 §3.4b.
  [confirmed 2026-07-12]
  on the 5-disk UEFI node. HEALTHY raid5 boots fine ([4/4], per-leg NVRAM), but after pulling one
  member GRUB loads from the mirrored ESP then drops to the `grub>` rescue prompt; raid6/raid10
  behave the same. RAID1 degraded boot WORKS (3.28) because its root is also raid1 (any leg is a
  complete copy). `makeGrubCfgDegradedSafe` (rhel.go) already strips mduuid hints for the raid1
  /boot, so the gap is deeper — GRUB cannot reach the kernel on a degraded parity/striped array.
  => lpart's C2 mirrored-boot fault-tolerance is currently REAL only for raid1(/raid10?); raid5/6
  nodes are NOT boot-fault-tolerant despite the mirrored ESP. Reopens/extends the v1 §3.4b parked
  item. Needs dedicated bootloader work (grub degraded-assembly, or forcing kernel+initramfs load
  purely from the raid1 /boot without any array reference). Tracked as FTA-v2 finding.
