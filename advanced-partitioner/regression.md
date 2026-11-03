# Regression — v1.4 installer end-to-end test driver

This file is the protocol for driving a real
end-to-end regression run of the TrinityX/Luna **v1.4** node installer
(`advanced-partitioner`) against a live cluster — *not* to write code. Read it
once, then execute the protocol in §1.

There is no test framework -- the operator is: propose tests, drive
Luna, watch the node boot, verify outcomes by SSH, and report. The operator is
in the loop for power cycles, IPMI, and any disk-affecting action not
pre-authorised.

> **What changed vs the old runbook (read this).** v1.4 replaced the
> `install_mode` × `osimage_sync` × `disklayout` × `discover_config`
> inputs and the `lpart-discover` binary with **two** Luna attributes:
> `install_mode` (one enum) and `disklayout` (the whole storage config). There
> is no discovery step — `selection: discover` lives inside the disklayout. The CLI
> verbs are now `luna node change <node> --install-mode <mode> -qdisklayout
> <file> [-qosimage-filter <file>]`. The data fence (what each mode may wipe) is
> the heart of the model; the catalog below is organised around it. The Go unit
> suite already pins the decision matrix (`decideAction`), the resolver, the
> tri-state classifier, sanitize, and the v2 schema — so the on-node tests here
> target what units can't: real disks, real boot, real preserve/delete, and
> real failover.

---

## 0. Required reading before you propose anything

1. **`advanced-partitioner/JOURNAL.md`** — repo state, decisions (§G), env
   quirks (§F: signed-shim UEFI, disk-boot vs PXE hostname/log routing, mdraid
   1.0-vs-1.2 wipe behaviour), the cross-platform matrix (§L), and the latest
   "Status" entry. The destructive-allowed disk list lives in §A.
2. **`docs/DESIGN-NOTES.md` + `docs/storage-provisioning-model.md`** — the v1.4
   vocabulary and the section-5 data fence. Do not re-derive the model.
3. **`decisions/0003`–`0006`** — the ADRs (schema break, single install_mode,
   legacy daemon-only, dropped compat shim).
4. **PRIME.md** (fetch per JOURNAL §A) — hard rules: never delete repos; writes
   only inside the `alex/` namespace.
5. **Your auto-memory** for prior-session operator feedback (e.g. clear the
   firmware NVRAM entry before a PXE re-cycle; the install-log channel).

Do not grep the code to derive a test — the catalog in §3 is the spec.

---

## 0a. Phase contract — WHEN pre / part / post run (and what each may touch)

The node's pre/part/post hook fields each `exec /usr/bin/lpart-phase <phase>` →
`lpart-node-installer --phase <phase>`. Each phase is a **separate process**, run by
the daemon install template in order. The contract (pinned by
`internal/pipeline/phases_test.go`):

| Phase | Stages (in order) | Touches |
|-------|-------------------|---------|
| **pre**  | `prepare-runtime` (mkdir runtime/log dirs) | **nothing destructive** — no disk, no daemon call. Just env prep + a kernel-log snapshot. (The legacy image-unpack bypass is the daemon template's own no-op `download_image`/`unpack_imagefile`, not a pre-phase step.) |
| **part** | `install-context` (`lpartinputs.Resolve`: GET node-detail → write `storage.json`/`osimage-filter.json`/`install-context.json`, incl. `provision_method`/`fallback`/`interface`), `storage-prepare` (partition/RAID/LVM/mkfs + mount the sysroot; dispatches on `install_mode`; **sanitize stops here**), `osimage` (`lpart-osimage-install` download+extract, or `lpart-storage-check` for `local`, or skipped for `sanitize`) | **the disk + the image.** Inputs are resolved *first*, before any mutation. |
| **post** | `bootloader` (`lpart-bootloader-finalise`: shim/grub + UEFI NVRAM; skipped for memboot/sanitize/local) | the ESP + NVRAM. Runs only after the OS image is in place. |

Invariants the tests assert: stage order never changes; a phase marks all *earlier*
phases' stages done; **pre owns no destructive stage**; the install-context (and thus
the provision fields the download honours) is resolved at the **start of part**, before
storage-prepare/osimage. The download method order comes from the node's
`provision_method`/`provision_fallback` (honoured, not hardcoded — see
`internal/osimage/download.go ProvisionOrder` + `download_test.go`), and the file-server
URL is derived from cmdline `luna.url` (not a baked-in IP — `FileBaseFromLunaURL`).

---

## 1. Protocol when this file is loaded

1. Read §0. Acknowledge: "Loaded JOURNAL, design notes, ADRs, PRIME, memory."
2. Run the questionnaire (§2). Wait for answers.
3. Echo a numbered plan (§3/§4 filtered by the answers) with per-test runtime.
4. Wait for approval/reorder. **Test #1 is always `memboot` (§3.1)** — no disks
   touched, catches a broken osimage/daemon early. Sync/full/persistent tests
   that share on-disk state are noted; run them in catalog order or recreate the
   marker each time.
5. Per approved test: prep → push Luna config → trigger PXE → monitor → verify
   by SSH → record → clean up. Workflow in §5.
6. Final report (§6 template).

If you'd do anything unauthorised (wipe a non-approved disk, push outside
`alex/`, restart a controller service), **stop and ask**.

---

## 2. Operator questionnaire

Ask as one block:

1. **Test-target hostname** (Luna node). Default `node001`.
2. **Approved destructive disks** — the exact `/dev/sd?` list you may wipe. The
   operator's answer is the only list. Default `sda sdb sdc sdd`.
3. **Disk tags** — are the disks SSD/NVMe or rotary as the kernel reports them
   (`lsblk -dno NAME,ROTA,TRAN`)? `selection: discover` matches on `rotary`/`ssd`/
   `nvme`; on VMware, virtual disks usually report non-rotational (`ssd`).
4. **Out-of-band access** (IPMI/redfish/vSphere or "operator power-cycles on
   request"). The dd+sysrq failover tests need a hard reset SSH can't guarantee.
5. **Time budget.** Full catalog ≈ 3–4 h. Short budget: §3.1 → §3.4 (core),
   §3.5–§3.8 (the data fence: sync/full), §3.10 (persistent). Skip the rest.

---

## 2a. Live log monitoring (use this, not end-of-run)

The installer now **streams content to rsyslog live** (one RFC3164 datagram per
stage/command/stdout/stderr/kernel event, tagged `lpart-install[phase/stage]`,
stamped with the node FQDN). On the controller:

```
tail -F /var/log/cluster-messages/<node>.messages | grep -E 'lpart-install|install\.'
```

You should see per-step lines appear **during** the install (stage starts,
`$ argv` command banners, child output, kernel warnings), attributed to
`<node>` — not just a batch at the end. If you see nothing tagged `lpart-install`,
the forward isn't reaching the controller (check `luna.loghost` on the node's
cmdline is the controller IP, and the install initramfs has the current
`lpart-node-installer`). The TUI on the node console shows the dashboard; the
rsyslog stream is the analysis channel.

> **HA overrule:** a solo controller may reject `luna node change` with "not in
> sync yet". Flip the overrule once per session (JOURNAL §G "Phase 14e
> operational gotcha"): `curl -sk -H "x-access-tokens: <tok>"
> https://127.0.0.1:7050/ha/overrule/_set`.

> **PXE re-cycle:** if the node has a firmware disk-boot entry, clear it before
> rebooting or it boots the installed disk instead of PXE (memory:
> feedback-force-pxe). A node whose `BootOrder` is `EFI Network` only PXEs on a
> plain reboot.

---

## 3. Test catalog

`<node>` = the approved target. Write each disklayout to `/tmp/<name>.json` and
push with `-qdisklayout`. Disk paths in `selection: manual` should be
`/dev/disk/by-path/...` (stable) — get them from `ls -l /dev/disk/by-path` on
the node.

### 3.1  memboot smoke — always test #1
- **Purpose.** No disks touched; proves the osimage/initramfs/daemon/pipeline
  and the new client are healthy before anything destructive.
- **Config.** `luna node change <node> --install-mode memboot` (no disklayout).
- **Expect.** discovered → partscript → success; node boots; **`findmnt -no
  FSTYPE /` = `tmpfs`** (RAM root). rsyslog shows `lpart-install[part/...]` stage
  lines and the `osimage` extract into RAM.

### 3.2  auto single-disk (no RAID, LVM root)
- **Purpose.** The simplest disk install: `auto` on a blank disk → create.
- **disklayout** (`os-discover-none.json`): one `os` set, `selection:discover`,
  `raid:"none"`, volumes uefi(vfat,600M,partition) + boot(xfs,1G,partition) +
  root(xfs,100%,lvm).
- **Config.** `--install-mode auto -qdisklayout os-discover-none.json`.
- **Expect.** disk wiped+created, `/dev/mapper/os-root` LVM, signed-shim UEFI,
  `install.success`; reboot (clear NVRAM) → firmware disk-boot reaches multi-user
  with `BOOT_IMAGE=(hd0,gpt2)/vmlinuz`, `root=os-root`.

### 3.3  auto RAID1 OS by tags
- **Purpose.** `diskresolve` tag selection + RAID1 create path + signed-shim on
  a RAID-backed ESP.
- **disklayout** (`os-discover-raid1.json`): `os` set, `selection:discover`, `raid:"1"`,
  `match:{tags:["ssd"]}`, same volumes as 3.2.
- **Expect.** two disks selected by the `ssd` tag, 3 md arrays (efi/boot meta
  1.0, storage meta 1.2), `os-root` on the md PV, `install.success`.

### 3.4  manual RAID1 OS (deterministic)
- **Purpose.** `selection:manual` with explicit by-path devices — no reliance on
  tag detection. Best first *disk* smoke.
- **disklayout** (`os-manual-raid1.json`): `os`, `selection:manual`, `raid:"1"`,
  `devices:[<by-path sda>,<by-path sdb>]`, same volumes.
- **Expect.** same as 3.3 but on the pinned disks.

### 3.5  sync on BLANK → creates (data fence)
- **Purpose.** `sync` must create a layout on a blank disk (fail-safe, not
  no-op). first_install promotes the image extract to full.
- **Setup.** Start from wiped disks (run sanitize §3.13 first, or §3.2 then
  `sgdisk --zap-all` the disk).
- **Config.** `--install-mode sync -qdisklayout os-manual-raid1.json`.
- **Expect.** rsyslog `verdict=blank -> create`; layout created; `install.success`.

### 3.6  sync on HEALTHY → refresh + preserve + delete cruft (the rsync protocol)
- **Purpose.** THE sync test. `sync` converges to the golden image: it
  **preserves** the operator's preserve-list, **deletes** on-disk cruft NOT in
  the preserve list, and **does not wipe** the layout (no repartition).
- **Setup.** Install once (§3.2/§3.4, or a prior `full`). Boot the node, then on
  the booted OS create two files on the OS filesystem:
  - a **preserve** file: `echo keep > /etc/lpart-regression-keep.conf`
  - a **cruft** file (not in the image, not in preserve):
    `echo cruft > /etc/lpart-regression-cruft.conf` ; `sync`
- **osimage-filter** (`filter-keep.json`): `{"preserve":["etc/lpart-regression-keep.conf"]}`.
- **Config.** `--install-mode sync -qdisklayout <same> -qosimage-filter filter-keep.json`.
- **Expect after reinstall + boot:**
  - rsyslog `verdict=healthy -> mount (no wipe)`, `first_install=false` (NOT
    promoted to full), the layout is NOT repartitioned (fs UUIDs unchanged —
    capture `blkid` before/after).
  - `/etc/lpart-regression-keep.conf` **still present** (preserved).
  - `/etc/lpart-regression-cruft.conf` **GONE** (deleted: it isn't in the image and
    isn't preserved — the rsync-delete/converge behaviour).
  - image-managed files refreshed to the golden version.
- **This is the behaviour we changed** — verify all three: preserve kept, cruft
  deleted, no wipe.

### 3.7  sync on BROKEN → HALT (never wipe)
- **Purpose.** `sync` must refuse to wipe a broken/partial layout — it halts for
  operator review instead.
- **Setup.** Install (§3.4 RAID1), boot, then corrupt the layout WITHOUT fully
  blanking it: e.g. `wipefs -a /dev/sdb3` (break one RAID member / a signature)
  but leave the partition table so it reads partial, `sync`.
- **Config.** `--install-mode sync -qdisklayout os-manual-raid1.json`.
- **Expect.** `lpart-storage-prepare` classifies `verdict=broken` and **halts** with
  `data fence ... halting` (exit non-zero); `luna2-start.sh` shows the retry
  countdown; **no repartition / no wipe** occurred (the surviving data is
  intact). Operator decides; switching to `full` (§3.8) would then wipe.

### 3.8  full → WIPE (overlay, filter ignored)
- **Purpose.** `full` wipes and reinstalls from scratch, ignoring the preserve
  filter.
- **Setup.** Reuse the §3.6 node (with the keep + cruft files present).
- **Config.** `--install-mode full -qdisklayout <same> -qosimage-filter filter-keep.json`.
- **Expect.** rsyslog shows the layout recreated (fs UUIDs CHANGE); **both**
  `lpart-regression-keep.conf` and `lpart-regression-cruft.conf` are GONE (full
  overlay ignores preserve); `install.success`.

### 3.9  auto repairs a broken OS set only
- **Purpose.** `auto` = sync + repair: it recreates a *broken OS* set (no user
  data) but still HALTS on a broken *data* set.
- **Setup.** §3.12 multi-set (OS + data). Break the OS layout (wipefs the OS
  storage partition) leaving data intact; `--install-mode auto`.
- **Expect.** OS set `verdict=broken -> create` (recreated); data set, if also
  broken, HALTS. If only the OS is broken, data set stays `healthy -> mount`.

### 3.10  memboot + persistent local DATA
- **Purpose.** Stateless OS from RAM with persistent data that survives every
  normal mode (the xCAT statelite pattern, declarative).
- **disklayout** (`memboot-data.json`): one `data` set, `selection:manual`,
  `raid:"none"`, `persistent:true`, `devices:[<by-path sdc>]`, volumes
  data(xfs,100%,partition,mountpoint /data).
- **Config.** `--install-mode memboot -qdisklayout memboot-data.json`.
- **Setup.** First run creates the data set; boot, `echo keep > /data/marker; sync`.
- **Expect on re-PXE (same config):** `/` is tmpfs (RAM OS); `/data/marker`
  **survives** (persistent set `-> mount`, never partitioned/formatted); rsyslog
  shows the persistent set mounted, not created.

### 3.11  persistent set survives `full`
- **Purpose.** A `persistent` set is wiped by NOTHING except `sanitize` — not
  even `full`.
- **Setup.** §3.12-style OS + persistent data; write `/data/marker`.
- **Config.** `--install-mode full -qdisklayout <os+persistent-data>`.
- **Expect.** OS wiped+recreated; `/data/marker` **survives** (persistent
  excluded from wipe and from the tar extract).

### 3.12  multi-set: OS RAID1 (ssd) + scratch RAID0 (nvme)
- **Purpose.** Tag-based multi-set resolution + pool consumption (a disk picked
  for the OS set is not reused for scratch).
- **disklayout** (`os-raid1-scratch-raid0.json`): `os` auto raid1 match ssd +
  `scratch` (role data) auto raid0 match nvme, scratch volume xfs 100% lvm at
  /scratch.
- **Expect.** OS on the ssd pair, scratch on the nvme pair (disjoint), both
  created; `/scratch` mounted.

### 3.13  sanitize → secure-erase ALL local disks, then stop
- **Purpose.** Tenant turnover: `sanitize` erases every local disk (including
  `persistent` sets) and does NOT install.
- **Config.** `luna node change <node> --install-mode sanitize`.
- **Expect.** rsyslog shows `sanitize: erasing N local disk(s)` and per-disk
  `blkdiscard succeeded` (or `wipe+zap` fallback on rotary); afterwards every
  approved disk is blank (`lsblk` shows no partitions, `blkid` no signature);
  the run stops with no OS image, no bootloader. **Run this to reset state
  between destructive scenarios.**

### 3.14  RAID1 dd-failover (degraded boot)
- **Purpose.** HA: lose one mirror leg, the node still boots degraded.
- **Setup.** §3.4 RAID1 installed + firmware-booting the disk. Then
  `dd if=/dev/zero of=/dev/sda bs=1M count=10; sync; echo 1 >
  /proc/sys/kernel/sysrq; echo b > /proc/sysrq-trigger`. Clear NVRAM so it boots
  the surviving leg (sdb), not PXE.
- **Expect.** degraded boot from sdb (`[2/1] [_U]`), multi-user reached. Repeat
  wiping sdb instead (boot sda). See JOURNAL §G 2026-06-04 — the sdb-leg case has
  been flaky on VMware EFI (boot-picker vs grub>); capture the actual stuck
  state before concluding.

### 3.15  RAID5 / RAID6 data pool
- **Purpose.** Parity arrays beyond RAID1's 2 disks. Counts: raid5 needs ≥4,
  raid6 needs ≥5.
- **disklayout.** memboot OS (`--install-mode memboot`) + a `data` set
  `selection:discover raid:"5"` (or `"6"`) over the 4–5 data disks, xfs 100% lvm at
  /data. (mem OS keeps the disks free for the parity array.)
- **Expect.** `[N/N] [UUUU...]` array, `/data` xfs mounted.

### 3.16  firmware disk-boot + fstab + BLS
- **Purpose.** The thing `install.success` alone hides: does the node boot from
  DISK (not PXE) after install?
- **Setup.** After §3.2/§3.4, clear the TrinityX/distro NVRAM entries, set
  BootNext to the disk, reboot.
- **Expect.** GRUB → the newest BLS kernel from `(hd0,gpt2)/vmlinuz`,
  `root=os-root`, multi-user. Verify `/etc/fstab` has the expected entries
  (efi/boot/root with correct dump/pass), and the BLS entry was normalized (no
  build-path prefix; see JOURNAL §G 2026-06-04 BLS fix).

### 3.17  legacy (daemon-only) — sanity, low priority
- **Purpose.** `install_mode=legacy` means the node runs the OLD bash
  pre/part/post and the lpart-* binaries are NOT invoked. On a fully-migrated
  controller there are no old scripts, so this mainly confirms the client refuses
  legacy cleanly if reached. Document; skip unless an un-migrated node exists.

### 3.18  deliberate failure → error logged + retry countdown
- **Purpose.** Prove (a) an installer error reaches `cluster-messages` via the
  live forward, and (b) `luna2-start.sh` retries on failure after a countdown
  (`retry_countdown 30`, guarded by `RES != success`).
- **How to force a deterministic error.** Push a config the decision engine
  cannot satisfy: `install_mode=auto` with an OS set `selection:discover raid:"1"
  match:{tags:["nvme"]}` on a node with **no** nvme disks (node001's VMware
  disks are `rotary`). `resolveDiscover` halts with "disk match unsatisfied: need 2
  device(s) tags=nvme … only 0 eligible" → `lpart-storage-prepare` exits non-zero →
  `install.sh` fails.
- **Config.** write `os-discover-nvme.json` (os set, selection:discover, raid:"1",
  match tags ["nvme"], standard volumes); `--install-mode auto -qdisklayout
  os-discover-nvme.json`. PXE.
- **Expect in `<node>.messages`:**
  - the live `lpart-install[part/...]` error line (the resolveDiscover halt / the
    `set "os": ... data fence`/`disk match unsatisfied` message),
  - then `luna2-start.sh`'s failure handling: a dmesg snapshot + the retry
    **countdown** (giving ~30 s), then a fresh attempt — i.e. the node loops,
    it does NOT silently hang or proceed.
- **Expect on the console:** under the `INSTALLER FAILED` banner a
  `Luna2: Reason:` block — the stage, the reason and the failing step's stderr,
  read from `/tmp/install_files/logs/last-failure.txt` (written by the phase that
  failed, cleared at the start of every phase so a stale one is never shown),
  folded to 76 columns and capped at 15 lines. A park (exit 2 or 3) prints the
  same block plus a line saying what that exit code means.
- **Recover.** It will loop forever on a permanent error, so once you've
  captured the error + countdown, push a good config (`--install-mode memboot`);
  the next attempt re-reads the daemon and succeeds. (Confirms the loop also
  *recovers* when the config is fixed.)
- **No disks touched** (the failure is at resolve, before any wipe) — safe to
  run any time.

### 3.19  discover + save → node disklayout pinned, then revert (ADR-0008)
- **Purpose.** Prove the `save` write-back: a `selection:discover` set with
  `save:true` resolves, installs, and then writes the concrete layout back to the
  **node-level** `disklayout` as `selection:manual` (stamped with `origin`), so
  the next boot reproduces it; and that clearing the node `disklayout` reverts to
  group discovery.
- **Prereq (migration).** The client must be the discover-rename build (>=2.0-40).
  Any stored `disklayout` still using `selection:"auto"` must first be updated to
  `"discover"`, or the new client rejects it at config validation.
- **Config.** `os-discover-raid1.json` = one `os` set, `selection:discover`,
  `raid:"1"`, `match.tags:["rotary"]` (node001's VMware disks are `rotary`),
  `save:true`, standard volumes; `--install-mode auto -qdisklayout
  os-discover-raid1.json`. Clear the TrinityX disk-boot NVRAM entry
  (`efibootmgr -b <n> -B` over SSH) so the node PXE-cycles, then PXE.
- **Expect in `<node>.messages`:** the normal discover->RAID1 create path (md
  mirrors, `os` VG, ESP on both legs), then a `save: pinning resolved layout to
  node "node001" at <luna.url>/config/node/node001` line and `save: node
  "node001" disklayout pinned (1 set(s))`. **No `err`-severity lines.** `install.sh
  exit status: success`; node boots off the array.
- **Verify the pin on the controller.** `luna node show node001` (or the daemon
  API) — the node now carries a node-level `disklayout` with `selection:"manual"`,
  the two resolved devices, and an `origin` block (`from:"discover"`, `by:
  "lpart-storage-prepare ..."`, `host:"node001"`). It is a node-level override now, so
  it shadows the group layout (ADR-0007/0008, spec 5.4-5.5).
- **Re-entrancy.** A second PXE of the same node (still pointed at the pinned
  manual layout) must NOT re-write the disklayout (manual never saves) and must
  reproduce the identical array. No second `save: ...pinned` line.
- **Revert.** `luna node change node001 --disklayout ''` -> next boot falls back to
  the group's `discover` layout and chooses disks fresh again.
- **Non-fatal check (optional).** Point `luna.url` at an unreachable port for one
  run and confirm the install still **succeeds** with the layout on disk, a logged
  `save: ... write-back failed` line, and the paste-ready block still emitted (the
  pin is a convenience, never install-aborting).
- **Safety.** This is a normal `auto` RAID1 install (the 3.3 path, already
  validated to boot), plus the post-resolution write-back; the write-back touches
  no disks. Keep PXE in the BootOrder so a bad boot can be re-driven.

### 3.20  partitioner re-entrancy matrix (memboot + SSH battery)
- **Purpose.** Prove on real disks that the partitioner is re-entrant: safe to run
  any number of times in one boot, converging to the declared layout, and
  idempotent when the disk already matches (a re-run with `auto` is mount-only, no
  reformat). Exercised across a **layout matrix** and every transition between
  layouts.
- **Harness: memboot.** Boot the node into `memboot` (tmpfs RAM root) so it is a
  full OS (all tools: `lvs`, `mdadm`, `sgdisk`, `wipefs`, `mkfs.*`, `grub2-install`,
  `dracut`) with **all data disks free** (root is in RAM). Then SSH in and drive
  `lpart-storage-prepare` directly, any number of times, without ever touching the
  running root. (`memboot`'s cmdline carries `root=luna`, so the installer-env
  guard passes; the disks are not mounted.)
- **Set up.** `luna node change node001 --install-mode memboot` (its os-only
  disklayout is ignored by memboot, so no disk is touched at boot), force PXE
  (clear TrinityX disk NVRAM entries + `BootNext` = network), reboot. Confirm with
  `ssh node001 'findmnt -no FSTYPE /'` -> `tmpfs`.
- **Run the battery.** `scripts/dev/reentrancy-battery.sh node001
  scripts/dev/reentrancy-layouts` (from the controller; it SSHes in). The layout
  matrix (`scripts/dev/reentrancy-layouts/*.json`):
  single-disk A, single-disk B, RAID1, RAID0, RAID10, os(RAID1)+data(RAID0), and
  os(RAID1)+two data sets. For each layout it runs `full` (create) then `auto`
  (re-run) and asserts: every run exits 0, and the `auto` re-run leaves the on-disk
  fingerprint (block UUIDs + arrays + LVs) **unchanged** (idempotent: mount, no
  reformat). It runs the matrix **forward then reverse**, so every layout
  transition is exercised in both directions (e.g. RAID10 -> single-disk must tear
  down a 4-disk array and lay down one disk, and back). Prints
  `RE-ENTRANCY BATTERY: PASS/FAIL` plus an idempotent-vs-convergent-only tally.
- **Phases (pre/part/post).** The battery also runs `lpart-node-installer --phase pre`
  twice (idempotent). The `part` storage step is the matrix above. `part`'s osimage
  extract and `post` (bootloader) need the Luna token + `install.sh`, which memboot
  does not stage, so their re-entrancy is covered instead by **repeated full PXE
  installs** (§3.19 runs two full installs back-to-back; the second converges and
  boots) and the per-module `*/reentrant_test.go` unit suites (cleanup, mount,
  osimage, bootloader).
- **Restore.** `luna node change node001 --install-mode auto`, force PXE, reboot ->
  the node reinstalls to disk and boots normally.
- **Safety.** memboot is fully recoverable (the node never boots off a half-built
  disk; root is RAM). The battery is destructive to the data disks by design but
  never runs osimage/bootloader, so it cannot leave a bad GRUB. Approved disks only.
  (An alternative harness, parking the node in the installer via a hold prescript,
  is kept at `scripts/dev/prescript-hold.sh`; memboot is preferred for the richer
  toolset.)

### 3.21  legacy static lint (no hardware)
- **Purpose.** Lock in the catalogued defects of the legacy pre/part/post scripts
  (F1–F9 in `LEGACY.configs`) so they can't silently regress, and so an upstream
  fix flips a check green. `install_mode=legacy` is **daemon-only** — the lpart-*
  tools never run — so the test surface is the bash scripts themselves.
- **Run.** `scripts/dev/legacy-lint.sh` (override `LEGACY_PLUGINS` / `LEGACY_ROLES`
  / `LEGACY_DOCS` to point at the daemon plugins, the trinityx roles, and the
  trinityx-docs pages). It greps the **live** sources and prints `[BUG]`/`[FIXED]`
  per finding with file:line; exit status = number of BUG findings.
- **Expect (2026-06-07 baseline).** 12 BUG: F1 (raid1 mirrors only root, not
  ESP/boot — BLOCKER), F6 (raid1 no zero-superblock before create — BLOCKER, and
  the legacy initramfs has no `AUTO -all` guard), F3×3 (FAT16 ESP), F4×2 (parted
  `name 1"` typo), F5×2 (boot flag on p2), F2 (doc raid1 `/dev/${DISK}p1` bug),
  F7 (nodhcp device-name fstab), F9 (cloud device-name fstab). These are upstream
  (clustervision) scripts; fixes are tracked in `LEGACY.configs`, not applied here.

### 3.22  legacy diskfull install + NVRAM analysis
- **Purpose.** Run the shipped `--scripts diskfull` plugin end-to-end and compare
  its bootloader/NVRAM method to the new installer's `lpart-bootloader-finalise`.
- **Set up.** `scripts/dev/legacy-battery.sh set-plugin node001 diskfull`, then
  `pxe-reboot node001` and `wait node001`. Tail
  `/var/log/cluster-messages/node001.cluster.messages | grep -E 'DISKFULL|install\.'`.
- **Expect.** node boots from `/dev/sda4` (ext4 root), `/boot` ext4, `/boot/efi`
  vfat, swap on sda3; `findmnt / ` is the disk, not tmpfs. NVRAM has a `Shim1`
  entry. **Analysis (F8):** `legacy-battery.sh nvram node001` — legacy diskfull
  does **not** create the removable-media `EFI/BOOT/BOOTX64.EFI` fallback and does
  not set BootNext/Order or clear the firmware-recreated fbx64 entry; the new
  installer does. Note any FAT16 ESP (F3) and the `name 1"` parted error (F4).

### 3.23  legacy raid1 dd-failover (pull sdb, then sda)
- **Purpose.** HA of the shipped `--scripts raid1` plugin, and an **empirical test
  of F1** (the plugin mirrors only root, not ESP/boot).
- **Set up.** `legacy-battery.sh set-plugin node001 raid1`, `pxe-reboot`, `wait`,
  `verify` → expect `/dev/md0` ext4 root `[2/2] [UU]`, `/boot`+`/boot/efi` on
  **sda only**, swap on both legs.
- **Pull #1 (2nd leg).** `legacy-battery.sh pull node001 sdb` (dd-wipe + sysrq,
  per §3.14). ESP/boot survive on sda → **expect degraded boot** `[2/1] [_U]`,
  multi-user reached.
- **Restore**, then **Pull #2 (1st leg).** reinstall raid1, `pull node001 sda`.
  ESP/boot lived **only** on sda → **F1 predicts UNBOOTABLE** (root mirror is
  healthy on sdb but there is no ESP/boot anywhere) → node falls back to PXE
  (NVRAM is pre-cleared by `pull` so it is recoverable). Capture the actual stuck
  state. This asymmetry is the headline legacy defect; the new installer's
  `BootMirrorDevice` (mirrored os-efi/os-boot md at metadata 1.0) is the fix model.
- **Safety.** node001 IPMI is dead → recovery is PXE-only; `pull` clears disk
  NVRAM first so a failed degraded boot returns to PXE rather than bricking.
  Approved disks only (`sda sdb sdc sdd`); never `sanitize`.

### 3.24  pre/part/post column execution (marker proof)

Proves the node's **pre/part/post hook columns** actually run, in order, at the
right point — set them on the controller to run the normal `lpart-phase <phase>`
AND drop a marker, then check the markers after a disk install + boot. Driver:
`scripts/dev/phase-marker-test.sh {set|verify|clear} <node>`.

- **pre** echoes a `LPART_PHASE_TEST: prescript executed` line → lands in
  `<node>.messages` (sysroot not mounted yet).
- **part** writes `/sysroot/root/lpart-part-marker.txt` — persists on the
  installed disk as `/root/lpart-part-marker.txt` (proves part ran *after* the
  image was extracted into the install root).
- **post** writes `/sysroot/root/lpart-post-marker.txt` likewise (proves post ran
  after bootloader finalise).

The marker scripts preserve `lpart-phase`'s exit code, so a real install failure
still fails (markers never mask it). Workflow: `set <node>` → trigger install →
`verify <node>` (expects all three) → `clear <node>` to revert to the plain
shims. **HW-confirmed on node001 (2026-06-08).** Doubles as the re-entrancy proof
for the storage-cleanup fix (LOOSE-ENDS #15): a node that already has an `os`
VG/RAID reinstalls with no manual wipe.

### 3.25  SSH into the installer (must work DURING install)

The install initramfs runs an SSH daemon (redhat: dropbear `-p 22`; ubuntu:
`/usr/sbin/sshd`) so an operator can log into a node **while it installs** to
debug a stuck install. Driver: `scripts/dev/ssh-during-install.sh <node>` —
during an install it probes port 22 + a host-key-agnostic `ssh` login at each
non-final stage and PASSES only if SSH is reachable *during* the install (not
merely after the node boots its OS).

- **[2026-06-08 — FIXED] ubuntu: SSH was DEAD for the whole install.** Two
  causes, pinned via a `part`-stage sleep-hold + the SOL console:
  1. **(the blocker)** OpenSSH 9.8+ (24.04/26.04) split `sshd` into a listener +
     per-connection helpers; sshd exits with *"/usr/lib/openssh/sshd-session does
     not exist or is not executable"*. The initramfs hook copied `sshd` but not
     `sshd-session`/`sshd-auth`. Fix: `ubuntu/x86` hook `copy_exec`s them.
  2. `/run/sshd` (the modern privsep dir) was missing and the error was swallowed
     (`sshd > /dev/null 2>&1`). Fix: luna-start `mkdir -p /run/sshd` + chmod 0755
     + **surface** sshd's stderr so the console shows the real reason.
  redhat is unaffected: dropbear (primary; no helper split, no privsep dir) and
  rocky 9.7 is openssh 8.7 — but **confirm dropbear on a redhat node**.
- **Proper fix = converge ubuntu onto dropbear** (the installer's SSH should be
  the same tool both sides, not full OpenSSH on one). ubuntu luna2-client now
  `Depends: dropbear-bin`, the hook `copy_exec`s `/usr/sbin/dropbear`, and
  `luna-start` runs `dropbear -R -p 22` as primary (OpenSSH sshd kept as
  fallback). Mirrors redhat's `Requires: dropbear` + `dracut_install … dropbear`.
- **[2026-06-08 — the REAL blocker, found after dropbear converge] ubuntu: every
  key was denied even with dropbear listening.** Probing node002's installer
  showed dropbear up on :22, KEX OK, but `Permission denied` for a key that the
  *same* controller logs into redhat node003's installer dropbear with. Root
  cause: the install initramfs `/etc/passwd` had **no root entry** — the build
  hook copies a passwd with root, but the `dhcpcd` initramfs-tools hook overwrites
  `/etc/passwd` with only its own line. dropbear/sshd resolve the login user via
  `getpwnam()`; with no root entry it logs *"Login attempt for nonexistent user"*
  and denies all auth. Reproduced 1:1 against the extracted real initramfs + real
  dropbear binary + real `authorized_keys`: no-root → "nonexistent user"; add root
  → `Pubkey auth succeeded`. **Fix:** `luna_start()` guarantees `root` in
  `/etc/passwd`/`/etc/group` before SSH starts (mirrors how the redhat dracut side
  appends the sshd user). glibc's passwd db defaults to `files`, so no nsswitch
  entry is needed. redhat was unaffected — dracut's base initramfs already ships a
  root passwd entry.
- This is a tricky element: SSH host keys in the install initramfs differ from
  the installed OS, so a plain `ssh <node>` may also trip *"REMOTE HOST
  IDENTIFICATION HAS CHANGED"* — use `-o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null` (as the test does) to isolate "is sshd up?".

---

## 3B0. Step-0 hierarchy battery (added 2026-07-16) — §3.0

### 3.0  step-0 hierarchy battery: the no-reboot matrix on ONE boot  [DESTRUCTIVE, 6 disks]
- **Why (operator directive 2026-07-16).** Reboot-per-test costs ~12-15 min a row; the
  partitioner is re-entrant BY CONTRACT, so every behaviour that does not require a
  firmware boot or a pulled disk can be pinned back-to-back against a single held
  installer. The battery runs FIRST (after a tmpfs smoke and one 1-disk install) and
  GATES the parallel destructive tier: manifest section 0, serial, FAIL aborts the run.
- **Setup.** A 6-disk node (the driver self-provisions vdf on a 5-disk node). A
  node-level prescript override parks the wrapper in the installer (`touch
  /tmp/HIER_HOLD; wait`); every sub-test is driven over ssh. Two tiers: **direct**
  `lpart-storage-prepare` with a locally-pushed config (fast, no extract — the
  §3.20 reentrancy-battery pattern), and **phase** `lpart-node-installer --phase
  part/post` (the production path: daemon config fetch, osimage extract, bootloader +
  NVRAM) for three end-to-end configs.
- **Sub-tests (~26, S01-S30; each step = run → gates → next, NO reboot):** single-disk
  create / auto-idempotent / full-recreates (S01-03); real extract + bootloader on
  single (S04-05: NVRAM count, /sysroot/boot unmounted per a8826ad, grub.cfg fs-uuid
  mduuid=0 via norecovery readback); auto-preserves-marker (S06); sync-on-broken HALT +
  auto-repairs (S07-08); raid1 create shape + names + metadata + homehost (S09), raid1
  auto = ASSEMBLE-not-recreate (S10, the activateExisting pin), real raid1 end-to-end
  with per-leg NVRAM + BOTH-member norecovery grub readback (S11); raid5/6/10 + raid0
  multiset shapes (S12-15); persistent plain + persistent raid1 across full reinstalls
  (S16-17); discover+reservation (S18); by-id / by-path pins (S19-20); device-style +
  too-small refusals (S21-22); spanning-VG / spanning-md refusals + NB-9 stale-VG purge
  (S23-25); sanitize all-6-disks with read-back blank gates + sanitize×persistent
  exit-2 refusal (S26-27); hidden-blkid fail-closed (S28); exit-class rc pins (S29);
  release the hold → the wrapper completes a real install and the node boots (S30).
- **Expect.** Every gate green in one run (~30 min); the per-step tally is the verdict.
  What stays OUT (reboot-dependent, parallel tier): degraded boots/pulls (3.28-3.31,
  3.62), power yanks (3.32/3.59), NVRAM-loss fallback (3.27), boot order (3.61),
  memboot/streaming env (3.54-3.58), park-loop lifecycle through the wrapper (3.35-3.40).

## 3B. FTA-v2 regression block (added 2026-07-12) — §3.26–3.59

Derived from the FTA v2 event register (`docs/FTA-v2.md`: BE-S*/N*/W*/Z1/G5, MCS-13..18,
CCF-7/8), the NB-1..NB-8 barrier round, and the seven live-found 2026-07-11/12 bugs. Every
test names the FTA event it exercises so a failure maps straight back to the tree. Tags:
**[DESTRUCTIVE]** wipes disks / kills legs / yanks power — sacrificial node only;
**[SAFE]** no disk mutation (refusals, parks, observability). UEFI/OVMF node required
(q35 + `firmware='efi'`; conversion recipe in `/root/lpart-uefi-work/`), except 3.26 which
needs a legacy-BIOS node. 25 of 36 are destructive. Lab helpers: `/root/lpart-uefi-work/reg-helpers.sh` (`fresh_boot`, `blank`,
`setcfg`, `pushdl`, `shot`, `wait_write`).

### 3.26  BIOS node: installs, no bootloader, PXE-boots every time  [DESTRUCTIVE]
- **FTA.** Firmware gate (TE-C/TE-E guard): GRUB, the shim and the UEFI boot order are
  UEFI-only; everything else in the install is firmware-independent.
- **Setup.** A BIOS VM: redefine the node with `machine='pc-i440fx-*'`, no
  `<os firmware='efi'>` (keep the old XML backup), same MAC/disks. Any valid disklayout,
  `--install-mode auto`. PXE via the BIOS `ipxe-luna-direct.iso`.
- **Expect.** part phase logs `detected_firmware=bios`, `boot_plan_disk_os=true`,
  `boot_plan_installs_grub=false` and one warning naming GRUB/shim/boot order and
  PXE-every-boot. The install completes: `/boot/efi` is vfat and mounted, fstab carries
  `/` and `/boot/efi`, `storage-result.json` has `boot_method: none`, and finalise returns
  0 without running dracut, `grub2-install` or `efibootmgr`. The summary Boot line reads
  `Boot: none (BIOS firmware: …)`. The ramdisk pivots to `/sysroot` and the node reaches
  multi-user.
- **Second boot (`auto`).** Health verdict **healthy** → mount-only, no rebuild. The ESP is
  formatted on BIOS precisely so the health probe does not classify the set as broken and
  rebuild the OS on every boot.
- **Refusal leg (`[SAFE]`).** Same node, an osimage whose Luna ramdisk was packed without
  dracut (`mkinitramfs`): `safety_refusal=bootloader-none-needs-dracut-ramdisk` → exit 2 →
  node-installer 3 → **fatal park, NOT retrying**; `virsh domblkstat` wr_bytes on every
  disk = **0** (the preflight precedes EnsureNoAutoAssemble). Without it the node would
  reboot into a disk with nothing to boot and loop back into PXE for ever. If such a
  ramdisk cannot be produced in the lab, record that and rely on the unit test.
- **Recover.** Redefine with the OVMF XML → the *same* config installs with GRUB, the shim
  and the NVRAM entry, TrinityX last in the boot order.

### 3.27  NVRAM cleared → removable-fallback boot  [DESTRUCTIVE: NVRAM]
- **FTA.** BE-W3/BE-W4 mitigation: the ESP `EFI/BOOT/BOOT{X64,AA64}.EFI` + shim-CSV path
  must boot the node when firmware NVRAM is lost (firmware update, CMOS reset, BMC).
- **Setup.** After a §3.2 single-disk install, from the booted OS delete every TrinityX
  NVRAM entry (`efibootmgr -b XXXX -B` for each) and verify `efibootmgr` shows none. Reboot.
- **Expect.** OVMF finds no entry → falls back to the removable path → shim → grub → OS
  reaches multi-user. After boot, `bootloader-finalise` has NOT rerun (no PXE), so entries
  stay absent — assert the OS is up regardless. Then re-run one PXE install and assert the
  entries are re-created (register is idempotent, deletes stale + recreates).
- **Recover.** The reinstall already restored NVRAM.

### 3.28  RAID1 failover BOTH legs + rebuild + pull-the-other  [DESTRUCTIVE]
- **FTA.** C2 + BE-W* — "either leg boots alone" (v1 §3.4b parked this; UEFI unparks it),
  and the REBUILT leg must be genuinely bootable, not just present.
- **Setup.** §3.4 manual RAID1 on vda+vdb, booted from disk.
- **Steps + asserts.**
  1. Kill vda (blank the backing file, VM down). Boot → degraded from vdb
     (`Boot0007 TrinityX vdb`), all md `[2/1] [_U]`, multi-user.
  2. Recreate vda blank; from the degraded OS re-add: `sgdisk -R /dev/vda /dev/vdb;
     sgdisk -G /dev/vda; mdadm --add` each array; wait `[UU]`.
  3. Scrub: `echo check > /sys/block/mdX/md/sync_action`; `mismatch_cnt == 0` for all.
  4. **Now kill vdb** (the original survivor). Boot → degraded from the REBUILT vda
     (`Boot0004`), multi-user. This is the assertion 3.14 alone never made.
- **Recover.** Re-add vdb, resync, or full reinstall.

### 3.29  parity RAID for the OS set is REFUSED; raid5 DATA sets stay healthy  [DESTRUCTIVE]
- **POLICY (operator decision 2026-07-16, client 0c4bbd7).** The OS set uses exactly
  1, 2 or 4 drives with raid none/1/0/10; raid5/6 for role=os is refused at config
  validation (parity cannot boot degraded — NB-10). This test pins BOTH halves:
  (a) an OS-raid5 layout produces the named validation refusal, loudly;
  (b) os-on-vda + a DATA raid5 set over 4 disks installs healthy ([4/4], /data mounted).
- _(Superseded spec below — the pre-policy raid5-OS pull test — kept for history.)_

### 3.29-old  RAID5 OS single-disk-pull boot  [SUPERSEDED by the OS RAID policy]
- **FTA.** C2 for parity modes — MirrorBoot covers raid5 in code; never HW-run.
- **Setup.** Add a 4th disk to the node (virtio, boot order after the others). Manual
  raid5 OS set over 4 disks, standard volumes. Install, verify md125/126 (ESP,/boot) are
  **raid1 across all 4** (metadata 1.0) and root PV is raid5 `[4/4] [UUUU]`; NVRAM has an
  entry per leg.
- **Expect on pull.** Blank disk0 → boots degraded (`[3/4]`), root readable; write a file,
  re-add, resync, scrub 0. Repeat pulling a NON-boot-order-first disk (e.g. disk 2) —
  firmware should still pick a healthy leg without touching iPXE.
- **Recover.** Reinstall single-disk; detach the 4th disk when the block is done.

### 3.30  raid6 OS refused; raid6 DATA set healthy  [DESTRUCTIVE, full coverage needs 6 disks]
- **POLICY twin of 3.29** for raid6: (a) OS-raid6 layout → named validation refusal;
  (b) os-on-vda + DATA raid6 over 5 disks (vdb..vdf, a shape-6 node) → [5/5], /data
  mounted. On a 5-disk node the (b) half is skipped with an explicit note.
- _(Superseded spec below — the pre-policy raid6-OS double-pull test.)_

### 3.30-old  RAID6 OS DOUBLE-disk-pull boot  [SUPERSEDED by the OS RAID policy]
- **FTA.** raid6's whole point is surviving two losses — the boot chain must too.
- **Setup.** 5 disks, manual raid6 OS. Verify `[5/5]`, per-leg NVRAM entries (5).
- **Expect.** Blank disks 0 AND 1 → boots degraded `[3/5]` from a surviving leg; root
  mounts rw; re-add both, resync completes, scrub 0.
- **Recover.** As 3.29.

### 3.31  RAID10 OS create + pull  [DESTRUCTIVE, needs 4 disks]
- **FTA.** LOOSE-ENDS #10: "RAID10 never run" — the last redundant mode with zero coverage.
- **Setup.** 4 disks, manual `raid:"10"` OS set. Install; assert layout (md raid10 PV,
  raid1 ESP/boot across all members), `install.success`, disk-boot.
- **Expect.** Pull one disk → degraded boot; the far/near layout detail is captured in
  `mdadm --detail` evidence for the report.
- **Recover.** As 3.29.

### 3.32  resync-integrity under power loss  [DESTRUCTIVE]
- **FTA.** CCF-6 (post-condition verification) × BE re-entrancy: a resync interrupted by
  power loss must not produce a silently-inconsistent mirror.
- **Setup.** §3.28 step 2 — during the ACTIVE resync (`/proc/mdstat` shows recover),
  `virsh destroy` the node (power yank). Boot again.
- **Expect.** Arrays assemble, resync RESUMES (or restarts) to `[UU]`; scrub 0; both-leg
  boot still works (spot-check one leg). No fs corruption (`xfs_repair -n` clean).
- **Recover.** Complete the resync.

### 3.33  efivars absent during finalise → LOUD + verify verdict  [SAFE]
- **FTA.** BE-W3 / MCS-17, barrier NB-5 (nvram.go warn).
- **Setup.** SSH into the installer (§3.25, now working) during a single-disk install and
  `umount /sys/firmware/efi/efivars` in the initramfs before the post phase reaches
  finalise (park the part phase via a deliberate 3.35-style failure to buy time, or use a
  pre-field `sleep`-hold with `lpart-allow-custom` marker).
- **Expect.** RegisterUEFIEntries logs the NB-5 WARN ("no NVRAM boot entries will be
  created … removable fallback only") — NOT silent; then `efibootmgr -v` in verify errors
  → `[MISSING] efibootmgr -v (NVRAM unreadable)` → finalise FAILS (exit 1, retry). The
  failure is the correct fail-closed outcome; the WARN is the observability assertion.
- **Recover.** Remount efivars / reboot → next attempt succeeds.

### 3.34  efibootmgr-less image: multi-disk FAILS verify, single passes  [SAFE for node]
- **FTA.** BE-W3 / MCS-17 — the true "redundancy theatre" silent-pass, barrier NB-5.
- **Setup.** Clone the osimage (`luna osimage clone` or cp + register), remove
  `usr/sbin/efibootmgr` from the clone, pack. Point the node at the clone.
- **Expect.** (a) RAID1 install → bootloader verify **FAILS** with "multi-disk
  mirrored-boot install requires per-leg NVRAM entries; add efibootmgr…". (b) single-disk
  install → verify passes with the tolerant skip ("single boot disk; removable fallback
  suffices"). (c) same RAID1 + `register_nvram=no` → passes (deliberate opt-out).
- **Recover.** Point back at the real image; delete the clone.

### 3.35  exit-class park matrix: 1 retries, 2 parks, 3 parks  [SAFE]
- **FTA.** Cluster B / BE-S3+S4 — the full chain per class, through the RENDERED template.
- **Setup+expect.** Three runs on a blank node:
  1. **exit 1 (retryable):** nvme-tag discover layout (§3.18) → resolve halt → retry
     countdown → push a good config → next attempt SUCCEEDS (the loop recovers).
  2. **exit 2 (safety):** sanitize + persistent-set layout → `safety_refusal` →
     `INSTALL FAILED FATALLY (exit 2/3) -- NOT retrying`, shell offered.
  3. **exit 3 (fatal config):** install_mode that renders an unusable combination (e.g.
     sync with no disklayout → "requires a disklayout"; confirm which class it exits and
     document — if it is 1, note the retry is correct for a fixable config).
  Assert zero disk writes in all three (all halt pre-mutation).

### 3.36  retry loop config-lifetime: storage.json byte-identical  [SAFE]
- **FTA.** BE-S2 / MCS-15 (the copy-back pollution class), CCF-7.
- **Setup.** Force a permanent retryable failure (§3.18 nvme layout). SSH into the parked
  installer between countdowns; `sha256sum /tmp/install_files/runtime/storage.json` across
  ≥3 consecutive attempts (watch `retrying in`s pass).
- **Expect.** The hash NEVER changes (input config immutable across attempts);
  `storage-resolved.json`/`storage-result.json` may come and go (outputs). Then push a
  good layout → daemon re-fetch on the next attempt uses the NEW config and succeeds —
  proving the retry re-reads the daemon rather than trusting stale runtime files.

### 3.37  dual-usage guard live matrix (pre/part/post × 4 contents)  [DESTRUCTIVE attempt]
- **FTA.** BE-S5 / MCS-13, barrier NB-2 — beyond the single live case already run.
- **Setup.** For EACH phase field (pre, part, post) push in turn:
  (a) `exec /usr/bin/lpart-phase <phase>` — silent, install proceeds;
  (b) `echo custom-hook && touch /lunatmp/hook-ran` — WARN, install proceeds, marker file
      exists in the initramfs (custom hooks stay allowed);
  (c) `parted /dev/sda mklabel gpt` + `mke2fs …` — **BLOCKED**, exit 2, fatal park,
      `install.blocked.dual_<phase>` status on the controller, disk writes 0;
  (d) same as (c) plus a `# lpart-allow-custom` line — WARN only, script RUNS (verify the
      mklabel actually happened = the operator explicitly accepted it).
- **Expect.** 12 sub-results; (c) is the safety assertion, (d) proves the escape is real,
  (b) proves we did not break custom hooks. **Recover.** Reset fields to the shim, reinstall.

### 3.38  seam contract gate as a scheduled regression  [SAFE]
- **FTA.** CCF-7, barrier NB-3. Run `scripts/dev/dual-usage-guard-test.sh` (8 cases) +
  `scripts/dev/seam-contract-gate.sh <ctrl> <node>` (7 checks) and require ALL PASS.
  This is mandatory per release (§4a) — listing it here makes it part of every full
  regression sweep too, so template/daemon drift is caught even between releases.

### 3.39  kmsg flood during install  [SAFE]
- **FTA.** BE-S1 (the poller-park class) — prove the fix holds under pressure, not just
  idle. **Setup.** SSH into the installer during pre/part and flood the ring:
  `for i in $(seq 1 20000); do echo "<4>flood $i" > /dev/kmsg; done` while the phase runs.
- **Expect.** The phase completes normally; backlog capture stays capped (no OOM, no
  hang); the TUI/rsyslog stream shows kernel lines without stalling the install.

### 3.40  SSH-into-parked-installer incident loop  [SAFE]
- **FTA.** BE-S6 (NB-7) + the park design — the full operator incident path.
- **Setup.** Force a fatal park (sanitize×persistent). SSH into the parked installer.
- **Expect.** Login works (dropbear/sshd up); `/tmp/luna_install.log.prev` +
  `/tmp/luna_install.dmesg.last` exist and contain the refusal; fix the config from the
  controller, then from the installer shell `exit` → the wrapper re-opens the shell (no
  retry, as documented) — assert the node did NOT silently resume; power-cycle → clean
  install with the fixed config. Documents exactly what an on-call engineer can do.

### 3.41  by-id pinned install + serial-swap identity refusal  [DESTRUCTIVE]
- **FTA.** BE-A3/A4 / MCS-6, BR-9 — THE long-standing HW gap. The lab CAN do this: give
  each virtio disk a serial (`<serial>LPARTA001</serial>` etc. in the domain XML) →
  `/dev/disk/by-id/virtio-LPARTA001` appears in the initramfs.
- **Setup.** (a) Manual OS set pinned by `/dev/disk/by-id/virtio-…` → full install →
  boots (also re-proves the `-part<N>` fix on by-id). (b) Persistent data set pinned
  by-id on disk B, marker written. (c) **Swap the two `<serial>` values in the XML**
  (simulates a swapped backplane) and reinstall with install_mode=auto.
- **Expect.** (a/b) install clean. (c) the by-id names now point at PHYSICALLY different
  disks — but by-id follows the serial, so the persistent pin follows the data disk:
  assert the OS set installs on the disk that NOW carries the OS serial and the marker
  disk is untouched (this is by-id doing its job where by-path/kernel names would have
  destroyed the data). Additionally verify BR-9b serial re-verify logs.
- **Recover.** Restore serials, reinstall. **This closes the top BR-9 gap in-lab.**

### 3.42  by-path machine-topology drift fails closed  [DESTRUCTIVE setup]
- **FTA.** BE-N2 (found live: i440fx→q35). Reproduce deliberately: pin the OS set by the
  CURRENT by-path, then add a PCIe root-port/device to the domain so the disk moves to a
  different slot (or switch machine type), boot.
- **Expect.** `device not found: /dev/disk/by-path/…` **with the candidates list**;
  fail-closed halt; no other disk touched (wr_bytes 0 on all). Fix = re-pin
  (discover/by-id); assert the install then succeeds. The pair (halt, no-guess) is BR-1b's
  live contract.

### 3.43  udev-style single-disk mount matrix (by-path AND by-id)  [DESTRUCTIVE]
- **FTA.** BE-N1 / MCS-16 (the `-part<N>` bug) — live pin for BOTH udev styles, single
  disk, with a data partition: volumes /boot/efi + /boot + / (lvm) + a second manual
  by-style data set with a plain xfs partition at /data.
- **Expect.** All mounts land (`findmnt` shows `…-part1/2` device paths); reinstall
  (auto, healthy → mount) also works — the MOUNT-EXISTING path is where the bug lived.

### 3.44  unsupported device styles refused at validation  [SAFE]
- **FTA.** BE-N3 / CCF-8, barrier NB-4. Push disklayouts with (a) `/dev/mapper/mpatha`,
  (b) `/dev/dm-0`, (c) `sda` (no /dev/ — the closed single-device hole).
- **Expect.** Each fails config_validation with the named error ((a)/(b): "device-mapper/
  multipath … not supported"; (c): "must start with /dev/"); exit non-zero; **zero**
  probes/writes; the node retries (fixable config) and recovers when a good layout is
  pushed.

### 3.45  discover pressure: persistent smallest disk never drawn  [DESTRUCTIVE]
- **FTA.** MCS-1 (the keystone) under the nastiest ordering: persistent manual data set on
  the SMALLEST disk + `selection:discover count=2` OS raid1. Historic failure mode:
  smallest-first discover drew the persistent disk.
- **Setup.** Marker on the persistent (smallest) disk. install_mode=full (worst case:
  wipes whatever it selects).
- **Expect.** Reservation logs "disk X is reserved by set 'data' -- removed from the
  discover pool"; OS lands on the two larger disks; marker intact after boot; repeat with
  install_mode=auto and sync. Three passes, marker survives all.

### 3.46  spanning-VG destruction refused  [DESTRUCTIVE attempt]
- **FTA.** BE-A6 / MCS-7, BR-2 — unit-only until now.
- **Setup.** From a memboot session, hand-build the hazard: `pvcreate /dev/vdb1 /dev/vdc1;
  vgcreate span /dev/vdb1 /dev/vdc1; lvcreate -n keep -l50%FREE span`, write a marker on
  /dev/span/keep mounted from vdc's PV side. Declare vdb in a (non-persistent) OS set and
  vdc in a PERSISTENT data set; install_mode=full.
- **Expect.** Cleanup detects the VG spans selected+protected →
  `safety_refusal` halt (exit 2 → park); `vgs` still shows `span`; marker readable.
  The wrong outcome (v1's finding) was `vgremove -fy span` taking vdc's LV with it.
- **Recover.** Remove the span VG by hand, reinstall normally.

### 3.47  spanning md-array destruction refused  [DESTRUCTIVE attempt]
- **FTA.** BE-A7 / MCS-7 — the md sibling of 3.46. Build `mdadm --create /dev/md77
  --level=1 --raid-devices=2 /dev/vdb2 /dev/vdc2` with vdc protected; fs + marker on it.
  install_mode=full targeting vdb.
- **Expect.** Refusal (never `--stop` + `--zero-superblock` on vdc's member);
  `mdadm --examine /dev/vdc2` still shows the superblock; marker survives an assemble.
- **Recover.** `mdadm --stop md77; wipefs` both members by hand.

### 3.48  dirty-log data set: replayed, classified healthy, journal survives  [DESTRUCTIVE]
- **FTA.** BE-A10 / MCS-4, BR-7 — the probe-mutates class, live.
- **Setup.** OS + xfs data set installed; from the booted OS start a write loop onto
  /data, then `echo b > /proc/sysrq-trigger` (unclean power loss with a DIRTY xfs log).
  PXE with install_mode=auto (data set now needs classification).
- **Expect.** rsyslog shows `xfs_repair -n` and the replay mount+umount only (grep the runtime
  log for `-L` = **zero hits**); the log is replayed (mount+umount) and the set classifies
  healthy, the node boots with its data — a power cut must not park it (ADR 0010). HALT only
  when the replay itself fails; what is FORBIDDEN is repair-with-mutation (`-L`) during
  classify. The last-synced data is present.
- **Recover.** Normal reinstall.

### 3.49  guard-input sabotage: missing probe tool ⇒ fail closed  [DESTRUCTIVE attempt]
- **FTA.** CCF-1 / BE-A9/MCS-9 — live, now possible via installer SSH.
- **Setup.** Data-bearing set under auto. SSH into the installer during pre;
  `mv /usr/sbin/blkid /usr/sbin/blkid.gone` (and separately, a second run hiding
  `findmnt`). Let part run.
- **Expect.** Classification must fail CLOSED: verdict broken/halt or a hard preflight
  error — NEVER `blank → create` over the data. Assert the data partition's signature
  still present (`blkid` from the controller loop-mount or next boot). Restore the tool →
  retry succeeds.

### 3.50  persistent set survives auto AND sync (not just full)  [DESTRUCTIVE]
- **FTA.** P1 completeness — §3.11 proved `full`; auto and sync take different decision
  paths (verdict-driven). Marker on the persistent set; reinstall with auto (healthy OS →
  refresh) and again with sync.
- **Expect.** Both paths: persistent logged `mount only (never partitioned/formatted)`,
  marker intact, fs UUID unchanged across both reinstalls (capture blkid before/after).

### 3.51  persistent RAID1 DATA set across OS reinstalls  [DESTRUCTIVE]
- **FTA.** P1 × RAID: the persistent-mount path must ASSEMBLE (not create) an md array.
- **Setup.** Create a raid1 data set over vdb+vdc (non-persistent first install), marker;
  flip the set to `persistent:true`; then: full OS reinstall on vda.
- **Expect.** The data md is assembled read-write and mounted (activateExisting path),
  NEVER re-created (`mdadm --detail` Creation Time unchanged — capture before/after);
  marker survives; md events counter increases only by assembly.

### 3.52  sanitize full lifecycle: erase → verify blank → reprovision  [DESTRUCTIVE]
- **FTA.** MCS-18/BE-Z1+S7 (NB-1) + tenant-turnover completeness.
- **Setup.** Multi-set install with markers on OS and data. `--install-mode sanitize`
  (no persistent sets declared).
- **Expect.** (a) per-disk `blkdiscard succeeded` for ALL disks; (b) part exits 0, post
  logs `no bootloader finalise for install_mode=sanitize`, `install.sh … success (rc=0)`,
  **no retry countdown**; (c) node halts (nothing to boot — dracut emergency is the
  expected terminal state; document it); (d) prove blankness: memboot the node and
  `blkid` every disk = no signatures, `lsblk` no partitions; (e) switch to auto →
  clean install. The (b) assertions are the NB-1/S7 live pins.

### 3.53  sanitize×persistent refusal → operator resolution flow  [DESTRUCTIVE]
- **FTA.** BR-8 / MCS-8 + the documented operator path end-to-end.
- **Setup.** Persistent set with marker + sanitize → expect `safety_refusal=
  sanitize-persistent`, exit-3 park, marker intact. THEN (the operator decision) remove
  the persistent set from the disklayout, keep sanitize.
- **Expect.** Next attempt erases everything including the ex-persistent disk (that IS
  the semantics — sanitize after explicit removal); then reprovision. The pair proves the
  refusal is a decision point, not a dead end.

### 3.54  8 GB node membots the 4.4 GB image (streaming)  [SAFE, env change]
- **FTA.** BE-G5 (NB-6 + the 80% tmpfs). Set the node VM to 8192 MB (previously stalled
  /OOMed at this size), install_mode=memboot, provision_method http.
- **Expect.** rsyslog: `stream_extract=true` → `streaming osimage extract (curl | tar)` →
  `streamed into /sysroot successfully`; tmpfs root reached; `df /` shows the 80% size;
  free RAM after boot > 1 GB. Restore the RAM size afterwards.

### 3.55  mid-stream network cut → fallback path recovers  [DESTRUCTIVE-ish network]
- **FTA.** NB-6's fallback contract (stream failure must not strand the node).
- **Setup.** memboot; on the CONTROLLER, once rsyslog shows `streaming osimage extract`,
  block the node for 3+ minutes: `iptables -I INPUT -s <node-ip> -p tcp --dport 7051 -j
  DROP` (curl's stall watchdog: `--speed-time 120` aborts). Unblock after the abort.
- **Expect.** `streamed curl|tar extract: …` failure → `falling back to classic
  download+extract` → same attempt (or the next retry) completes; final tmpfs boot. No
  partial-extract debris visible after success (fresh RAM root).
- **Recover.** Delete the iptables rule (both on success and failure paths!).

### 3.56  stream-extract override matrix + torrent preference  [DESTRUCTIVE (1 install)]
- **FTA.** NB-6 policy edges. Three runs:
  (a) disk install (auto, single-disk) with the template/phase wired to pass
      `--stream-extract always` → rsyslog shows streaming onto the DISK sysroot; install
      boots (ACLs on: assert `--acls` in the pipe, not `--no-acls`).
  (b) memboot with `--stream-extract never` → classic download (tarball path logged),
      still boots.
  (c) set the group `provision_method torrent` (aria2c present in the initramfs) →
      memboot does NOT stream (log: "first usable provision method is torrent");
      torrent download used. Restore http afterwards.

### 3.57  streamed extract honours the preserve filter  [DESTRUCTIVE]
- **FTA.** NB-6 × BR-3/P3 interaction — the streamed tar must carry the same --exclude
  list as the classic path. Reuse the §3.6 keep/cruft node with `--stream-extract always`
  on an auto (healthy→refresh) reinstall with the preserve filter.
- **Expect.** Identical to §3.6: keep-file preserved, cruft deleted, no repartition —
  proving the filter excludes ride the pipe (`'--exclude' …` visible in the logged
  command).

### 3.58  download watchdog: server stall aborts, retry recovers  [SAFE]
- **FTA.** BE-G1/BR-12 live (flags exist; behaviour never timed on-node).
- **Setup.** Classic (non-streaming) memboot; block 7051 as in §3.55 BEFORE the download
  begins; time the abort.
- **Expect.** curl gives up within ~connect-timeout 30 (SYN dropped) or ≤ ~2 min stall
  window (established-then-stalled); attempt fails retryable → countdown; unblock →
  next attempt succeeds. Total wedge time bounded — no forever-hang (the ancestor CC-6
  fleet-wide freeze).

### 3.59  power-yank matrix: kill the node at 3 install stages  [DESTRUCTIVE ×3]
- **FTA.** LOOSE-ENDS #12 / §3.20's missing hardware half + CCF-6: re-entrancy across
  REAL reboots, at the three nastiest points.
- **Setup.** RAID1 full install; `virsh destroy` (power yank, no shutdown) at:
  (a) during partitioning/mkfs (watch rsyslog for sgdisk/mkfs lines);
  (b) mid-extract (wr_bytes climbing, ~50%);
  (c) during bootloader finalise (grub2-mkconfig/efibootmgr lines).
  After each yank: power on, let it re-PXE with the SAME config.
- **Expect.** Every re-run recovers to `install.success` + disk boot with NO manual
  cleanup: stale md/LVM/mapper state torn down (the c69b24b re-entrancy fixes), partial
  extracts overwritten, NVRAM deduplicated (no entry pile-up after (c) — count TrinityX
  entries == member count). Log any manual step needed as a FINDING.
- **Recover.** Final full reinstall.

### 3.60  group→node cascade for the v1.4 fields  [DESTRUCTIVE (installs)]
- **FTA.** Tier-4 #15 (never verified server-side): the client consumes the EFFECTIVE
  install_mode/disklayout/osimage_filter.
- **Setup.** (a) Clear the node-level fields; set install_mode+disklayout on the GROUP →
  node installs with the group layout (assert in the part banner: mode + resolved sets).
  (b) Add a node-level disklayout override (different volume label/size) → node uses the
  NODE version. (c) Clear both → auto + no layout → memboot fallback (assert
  "falling back to memboot (RAM root)").
- **Recover.** Restore the node's normal config.

### 3.61  boot-order contract: disk first, iPXE only when blank  [SAFE]
- **FTA.** The OVMF lab design (install loop protection): after a successful disk
  install, EVERY reboot must boot the DISK (never re-PXE = never reinstall); after
  blanking, the same node must fall through to the iPXE CD and reinstall unattended.
- **Expect.** (a) installed node: 3 consecutive reboots reach the OS with wr_bytes not
  reset (no reinstall); serial/VGA shows no iPXE banner. (b) blank vda → iPXE banner →
  reinstall → boots. This is the contract every other destructive test silently relies
  on — pin it explicitly once.

### 3.62  RETIRED (2026-07-16): parity-RAID OS degraded-boot characterisation
- The OS-set RAID policy (0c4bbd7) refuses parity for role=os at validation, so this
  characterisation has no legal object anymore; the manifest row is deleted. NB-10's
  documentation stands (raid1 is the only guaranteed degraded-bootable OS RAID; raid10
  degraded boot is measured by 3.31 and currently PASSES on el9). History below.

### 3.62-old  parity-RAID degraded boot: deterministic characterisation  [SUPERSEDED]
- **FTA.** The FTA-v2 open finding (raid5/6 degraded → `grub>`), codified so any
  bootloader change re-runs the exact evidence procedure. Barrier NB-10.
- **Procedure** (drv-grub-diag.sh): (G1) healthy raid5 install, verify `[n/n]` +
  per-leg NVRAM + `grep -c mduuid grub.cfg == 0`, enable GRUB serial output by
  EDITING grub.cfg (never `grub2-mkconfig` — it would regenerate the mduuid hints
  the degraded-safe rewrite strips); (G2) blank one NON-boot leg, no NVRAM reset,
  capture GRUB's serial error verbatim; (G3) at the prompt: `set`, `ls`,
  `insmod normal; normal`, `search --fs-uuid <boot-uuid>` (send-key + screenshots);
  (G4) repeat with the disk DETACHED instead of blanked (blank-in-slot vs absent).
- **Expect.** The captured error string matches the NB-10 documentation; only **raid1**
  degraded boot works. Any change in the failure signature is a FINDING against NB-10.
- **2026-07-12 result (lpx-node003/004).** Characterised to the current understanding:
  - **raid10 degraded ALSO fails** at `grub>` (previously only raid5/6 confirmed) — so the
    affected set is every non-raid1 degraded OS array. **raid1 is the only degraded-bootable
    OS RAID.** (Supersedes the earlier "use raid1/raid10" guidance.)
  - **The ESP stub is NOT the failure point.** Serial-instrumenting the stub proved it runs:
    `search --fs-uuid` finds `/boot` on a raid1 member and `configfile` is reached.
  - **The MAIN `/boot/grub2/grub.cfg` fails NON-DETERMINISTICALLY** under degraded parity/raid10:
    same config, different failure per run (main cfg ran-and-fell-through vs never-executed). The
    main cfg already uses `terminal_output console` (not gfxterm). Matches GRUB's known
    intermittent degraded-array resolution.
  - A candidate stub-`insmod` fix was implemented, HW-tested, and **reverted** (it does not fix
    the main-cfg failure). Real fix = deeper GRUB main-cfg degraded-path work; OPEN.

### 3.63  md array names stable across reboots (homehost)  [DESTRUCTIVE (1 install)]
- **FTA.** BE-N4: `mdadm --create` without `--homehost` stores the INSTALLER's
  hostname in the superblock; on the installed OS the array is "foreign" and udev may
  rename it md127+, breaking `/dev/md/<set>` consumers (activateExisting, fstab-by-name).
- **Setup.** RAID1 OS install (§3.28 layout); reboot twice.
- **Expect.** `ls /dev/md/` shows the deterministic set names (`<set>`, `<set>-efi`,
  `<set>-boot`) on every boot; `mdadm --detail` shows no `md127`-style fallback names;
  the client passes `--homehost=any` at create (unit-pinned).

### 3.64  disk smaller than layout: loud preflight refusal  [DESTRUCTIVE attempt]
- **FTA.** BE-G6: BuildLayout issued sgdisk creates without checking the disk can hold
  the fixed-size partitions — a too-small disk surfaced as a generic 30 s
  "timed out waiting for partitions", masking the real cause.
- **Setup.** Push a layout demanding more than a scratch disk holds (e.g. uefi 600M +
  boot 1G + a fixed 20G root PARTITION on an 8 G vdb); `install_mode=full`.
- **Expect.** Immediate, named error `disk <dev> too small for layout: need >= X, have Y`
  BEFORE any write (wr_bytes ~0 on all disks); no 30 s timeout; fix the layout → install
  proceeds.

### 3.65  stale same-name VG on a disassembled array (NB-9)  [DESTRUCTIVE]
- **FTA.** BE-A6b / NB-9: a VG named `os` living on an md array whose members are on
  UNSELECTED disks is invisible to `vgs` while the array is disassembled
  (EnsureNoAutoAssemble keeps it down); v1 `purgeVGsByName` missed it, and a later
  udev/assembly event could surface it mid-install → duplicate VG name → `lvcreate`
  exit 5.
- **Setup.** From memboot: build `md99` over vdb1+vdc1, `vgcreate os /dev/md99`,
  `lvcreate` an LV, stop the array. Then a single-disk OS install selecting ONLY vda.
  Then a second install selecting vda+vdb (vdb is a member of the hidden VG's array).
- **Expect.** Install 1 (vdb/vdc unselected): succeeds — the hidden VG is out of scope
  and left alone (protected-disk fence). Install 2 (vdb selected): NB-9 assembles/scans
  arrays on SELECTED disks and purges the stale `os` VG copy before create — no
  `lvcreate` exit 5, install boots. `vgs` afterwards shows exactly one `os` VG.

### 3.66  `bootloader: first` puts the disk ahead of the network  [DESTRUCTIVE]
- **FTA.** The opt-out from the boot-order policy of 3.61: a cluster whose nodes must
  come up without the controller asks for the disk first per OS set. Default unchanged.
- **Setup.** UEFI node, any valid OS layout with `"bootloader": "first"` on the os set,
  `--install-mode auto`.
- **Expect.** The install is otherwise identical to 3.2. `storage-result.json` has
  `boot_order: first`; the summary Boot line reads `+ NVRAM (disk first)`; from the
  booted OS, `efibootmgr` shows a `TrinityX <disk>` entry FIRST in BootOrder, ahead of
  every network entry. A plain power cycle (iPXE CD still attached) lands on the disk.
- **Firmware re-sort leg.** On firmware that re-applies its Setup priorities at POST the
  read-back finds the network entry first: one WARN naming the cause, the summary says
  `(disk first requested, firmware kept network first)`, and the install still exits 0.
  Record which firmware the lab reproduced it on, if any.
- **Recover.** Remove the key and reinstall → TrinityX last again (3.61 holds).

### 3.67  `bootloader: none` on UEFI: no loader, and the old one goes  [DESTRUCTIVE]
- **FTA.** A node switched from grub to none must not stay firmware-bootable from the
  stale install, or a PXE failure boots the old OS instead of the controller's choice.
- **Setup.** A UEFI node already installed by 3.2 (so the ESP and NVRAM carry a TrinityX
  boot path). Change the os set to `"bootloader": "none"`, `--install-mode auto`.
- **Expect.** The part phase warns that no bootloader is installed. No `dracut -f`, no
  `grub2-mkconfig`, no `efibootmgr --create`; `storage-result.json` has
  `boot_method: none` and the summary Boot line reads
  `Boot: none (bootloader: none; node PXE-boots every time)`. Finalise exits 0 and logs
  `existing bootloader removed (NVRAM entries: N, loaders: M)`:
  `efibootmgr` shows NO TrinityX entry (the network entries untouched), and the ESP has
  no `EFI/BOOT/BOOTX64.EFI` and no `EFI/<distro>/shim*.efi` / `grub*.efi` — while
  `grub.cfg`, the CSV and `grubenv` are still there. A second `auto` boot repeats the
  removal without error (the extract re-creates the directories).
- **Refusal leg (`[SAFE]`).** Same layout on an osimage whose Luna ramdisk was packed
  without dracut: `safety_refusal=bootloader-none-needs-dracut-ramdisk` → exit 2, and
  the message names the `bootloader` key, not the firmware.
- **Recover.** Remove the key and reinstall → GRUB, shim and the NVRAM entry are back.

### 3.68  `bootloader: first` on legacy BIOS: GRUB, and the controller is optional  [DESTRUCTIVE]
- **FTA.** `first` is the only value that asks for a bootloader on BIOS, and the property it
  buys is that the node comes up with the controller's boot service stopped. The install is
  refused outright if the layout carries no BIOS boot partition, so that leg matters too.
- **Setup.** A legacy BIOS/CSM node (OVMF off, CSM on), `dl-bootloader-first.json` on the os
  set, `--install-mode full`.
- **Expect.** The part phase warns that GRUB is installed but the shim and the UEFI boot order
  are skipped. `storage-result.json` has `boot_method: grub`, `boot_order: first` and
  `bios_boot_partition: 1`; `sgdisk -p` on every boot disk shows partition 1 as a 1MiB `EF02`
  with no filesystem, and the ESP and /boot shifted up by one. Finalise runs
  `grub2-install --target=i386-pc` (or `grub-install` on Debian/Ubuntu) once per boot disk and
  then `grub2-mkconfig` / `update-grub`; no `efibootmgr` call of any kind, and the ESP gains no
  `EFI/BOOT/BOOTX64.EFI`. The summary Boot line reads
  `Boot: BIOS/GRUB (BIOS GRUB on every boot disk; no shim and no boot order)`.
- **The property being bought.** Stop the controller's boot service (`systemctl stop luna2-daemon`
  / block the node's PXE), then power-cycle the node: it boots the installed OS off its own disk
  unattended. Start the service again afterwards.
- **Mirrored leg.** On a raid1 os set, pull either disk and power-cycle: the node still boots,
  because each disk carries its own BIOS boot partition and core image.
- **Refusal leg (`[SAFE]`).** Reinstall the same node with `--install-mode sync` against the
  existing layout but no BIOS boot partition on disk (a layout built before the key was set):
  `safety_refusal=missing-bios-boot-partition` → exit 2, before dracut, and the message names
  `install_mode full`.
- **Recover.** Remove the key and reinstall → no bootloader, the node PXE-boots again (3.67's
  BIOS equivalent).

---

## 4. osimage content edge cases

### 4.1  first-install promotion (sync → full on blank)
- **Purpose.** On a fresh/`--wipe` first install, a `sync` preserve list is
  meaningless and is ignored (extract as full) so image content (var/spool/mail,
  var/log) isn't blocked.
- **Verify.** §3.5 already exercises this — confirm rsyslog logs `first_install
  detected ... extracting as full` and the preserve filter is NOT applied on the
  blank install, but IS applied on the §3.6 healthy refresh.

### 4.2  osimage-filter shapes
- **Purpose.** Exercise `preserve` / `preserve_empty` / `no_new_files` in
  `osimage-filter.json` on a healthy `sync` (§3.6 covers `preserve`). Add a
  `preserve_empty` dir and a `no_new_files` path; verify each behaves per the
  filter semantics (kept-empty dir present but cleared; no-new-files path keeps
  existing content, adds nothing).

---

## 4a. Seam contract gate — MANDATORY before any release (FTA v2 CCF-7 / NB-3)

The 2026-07-11 real-PXE run proved seam contracts (exit-code propagation, config lifetime,
dual-usage) only fail in composition — unit tests cannot see them. Before any release, run:

    scripts/dev/seam-contract-gate.sh [controller] [node]     # default 192.168.123.217 node001

It fetches the **rendered** install script from the live daemon (not a template copy) and
verifies: `exit $LUNARET` in all 4 phase handlers (no `exit 1` collapse, BE-S4); the
dual-usage guard present + behaviourally blocking disk-mutating content with exit 2 (BE-S5,
executed against the rendered function); and the deployed wrapper's park codes
(LUNA_SAFETY_RC=2 / LUNA_FATAL_RC=3). The client-side seam is pinned by the Go suite
(`TestRunStepArgvPreservesExitCode`, `kmsg_backlog_test.go`,
`TestPostPhaseSkipsBootloaderForSanitize`) — `make check` is the other half of this gate.
Also run `scripts/dev/dual-usage-guard-test.sh` (8-case behaviour battery) when the template
itself changed.

## 4b. Packed-initramfs toolset gate — MANDATORY pre-boot / per-distro (OP#-catcher)

The 2026-07-12 multi-distro UEFI runs proved the seam/rootfs checks are NOT sufficient: a node
installs from the **packed install initramfs**, and the packer can silently drop the toolset even
though `/trinity/images/<img>/usr/bin` has every binary. Two real bugs, both invisible until the
node booted and died in the prescript:
- **Ubuntu**: luna packs with `mkinitramfs` (initramfs-tools), which **ignores the committed
  dracut 95luna module** → the initramfs has NONE of lpart-phase / luna-installer.sh / lpart-*
  (`/usr/bin/lpart-phase: No such file`). (OP#1)
- **openSUSE**: dracut runs, but the SLE image shipped without `jq`/`sgdisk`, so `dracut_install`
  silently omitted them → lpart dies at partition time. (OP#2)

Gate: `scripts/lab/gate-initramfs-toolset.sh [--images "<imgs>"] [--strict-efi] [--json OUT]`,
run **on the controller**. It resolves the ACTUAL artifact luna serves (`luna osimage show <img>`
→ `initrdfile` under `/trinity/local/luna/files`), lists it format-robustly (`lsinitrd` →
`lsinitramfs` → magic-detect manual; needs `zstd`/`lz4`/`xz`/`cpio` on the controller for
ubuntu/opensuse artifacts — installer module 00 ensures these), and asserts the contract the
`95luna/module-setup.sh` `dracut_install` list promises:
- **REQUIRED** (absence ⇒ FAIL, rc 1): the 6 lpart pipeline bins (`lpart-phase`,
  `lpart-node-installer`, `lpart-storage-{prepare,check}`, `lpart-osimage-install`,
  `lpart-bootloader-finalise`), `luna-installer.sh` + `luna2-start.sh`, and the storage toolset
  (`sgdisk mdadm pvcreate vgcreate lvcreate mkfs.{xfs,ext4,vfat} parted partprobe wipefs blkid jq
  lsblk dmsetup`).
- **RECOMMENDED** (WARN only): `efibootmgr` (NOT in the redhat `dracut_install` list — see §3.33/
  §3.34; promote to REQUIRED with `--strict-efi` on UEFI-only labs), `mkfs.fat`, `lpart-emit`,
  `lpart-tui`, `luna-parse-cmdline.sh`.
- An **unreadable** initramfs is rc 2 (BLOCKED, operator toolchain problem) — never a false pass.

Wired in two places: **installer module 60** runs it as a hard gate on every install (fails the
verify if any REQUIRED tool is missing from the packed artifact), and it is scheduled in the
regression suite as **§4a.2** (`orch:initramfs_gate`, manifest `4a.2`). Run it per distro after
any osimage (re)pack.

## 5. Per-test workflow

1. **Prep.** Ensure the node is in a known state (often: run §3.13 sanitize, or
   confirm the disks are blank). Write the disklayout/filter JSON to `/tmp`.
2. **Push.** `luna node change <node> --install-mode <m> -qdisklayout /tmp/x.json
   [-qosimage-filter /tmp/f.json]` (overrule first if solo). Confirm with
   `luna node show <node> | grep -E 'install_mode|disklayout'`.
3. **Trigger.** Clear the firmware NVRAM disk entry if present, then power-cycle
   (operator/IPMI) or `ssh <node> reboot` if it's currently up and PXE-ordered.
4. **Monitor.** `tail -F /var/log/cluster-messages/<node>.messages` (the live
   `lpart-install` stream) + `luna node show <node> | grep status`.
5. **Verify by SSH** once up: `findmnt`, `lsblk`, `blkid`, `cat /proc/cmdline`,
   `efibootmgr`, the marker/preserve/cruft files, `/etc/fstab`, `mdadm --detail`.
6. **Record** outcome (pass/fail + evidence). **Clean up** before the next test
   (sanitize or re-pin a known config).

---

## 6. Report template

```
# v1.4 regression run — <date> — node <name>
Controller: <host>  osimage: compute-image (<rpm version>)
install initramfs lpart-* verified: <yes/no>

| #    | scenario                  | mode     | result | evidence / notes |
|------|---------------------------|----------|--------|------------------|
| 3.1  | memboot smoke             | memboot  | PASS   | findmnt / = tmpfs |
| 3.6  | sync healthy preserve     | sync     | PASS   | keep kept, cruft deleted, UUIDs unchanged |
| 3.7  | sync broken halt          | sync     | PASS   | halted, no wipe |
| ...  |                           |          |        |                  |

Data-fence summary (the load-bearing guarantees):
- persistent never wiped by auto/sync/full: <PASS/FAIL>
- sync halts on broken (never wipes): <PASS/FAIL>
- sync healthy preserves + deletes cruft (rsync converge): <PASS/FAIL>
- auto recreates broken OS only (data halts): <PASS/FAIL>
- only sanitize erases a persistent set: <PASS/FAIL>

New bugs / divergences (file as JOURNAL §G entries):
- ...
Open / parked:
- ...
```

> When a real-disk run diverges from the spec, file it as a JOURNAL §G decision
> entry (date, what, why) and link it — don't re-investigate next time.
