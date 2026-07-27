# Fault Tree Analysis v2 — luna2-client `advanced-partitioner` (lpart)

**Supersedes** `/root/lpart-review-out/FTA-lpart-current.md` (v1, 2026-07-10). v1 remains valid
as the historical record of the barrier campaign; THIS document is the current tree of record.

**Analysed tree:** branch `advanced-partitioner-stable`, commit `49868d8` **plus the uncommitted
2026-07-11 working-tree changes** (BIOS removal / UEFI gate, `devices.PartitionPath` udev fix,
their pinning tests). Every basic event and barrier claim below was re-verified against this
exact tree (file:line / test name), not inherited from v1.

**Why a v2:** the 2026-07-11 **real Luna PXE regression** and **UEFI campaign** falsified two of
v1's working assumptions. (1) v1's §1 boundary held the client↔daemon↔wrapper seam *outside*
the analysis — yet **all five** bugs the real pipeline surfaced lived exactly there. (2) v1's
appendix marked Cluster B (BR-10, exit-code integrity) closed — yet two seam faults
(`runStepArgv` code loss, template `exit 1` collapse) silently defeated it end-to-end. Both
outcomes were *predicted* by v1's own CCF-4 ("barriers recur without seam-level pins"). v2
re-baselines the boundary, adds the new failure classes, and re-derives the cut sets.

---

## Step 0 — Methodology and sources

Qualitative Fault Tree Analysis following the **NASA Fault Tree Handbook with Aerospace
Applications v1.1 (2002)** — the modern revision of **NUREG-0492** (Fault Tree Handbook, USNRC
1981) — structured on its canonical 8-step process, with **IEC 61025** as the cross-industry
standard frame and **Leveson-style Software FTA** (NASA SWE Handbook topic 8.07) for the
software adaptation. Steps: (1) objective → (2) top events → (3) scope → (4) resolution →
(5) ground rules → (6) construction → (7) evaluation (minimal cut sets + importance; CCF pass)
→ (8) interpretation/presentation.

No failure-rate data exists for this system, so evaluation is **qualitative**: importance is
*structural* (cut-set order + how ordinary the enabling conditions are), not probabilistic.
This matches the handbook's guidance that qualitative MCS analysis is the primary product when
credible basic-event probabilities are unavailable.

Sources: NASA FT Handbook v1.1 (2002); NUREG-0492 (1981); IEC 61025 (FTA, EN 61025);
NASA SWE-8.07 Software Fault Tree Analysis (after Leveson et al.).

## Step 1 — Objective

Protect operator data first, then deliver correct, bootable, honestly-reported installs:

1. **Never destroy data the operator marked protected** (classes P1–P5 below) — priority #1.
2. Install on the intended disks of the intended node, and only there.
3. Leave the node bootable (including after a mirror-leg loss on redundant layouts).
4. Report the true outcome to the retry/park wrapper (a false report converts every other
   fault into repeated destructive exposure).
5. Complete on supported configurations (availability).

The analysis drives: the next barrier round (recommendations, §Step 8), the regression scope
(which cases must run on hardware), and the dev-handover risk register.

## Step 2 — Top events

Carried from v1, re-baselined for the **UEFI-only** design (2026-07-11: BIOS support was
removed; `lpart-storage-prepare` refuses non-UEFI firmware with `safety_refusal=firmware-not-uefi`):

- **TE-A — PROTECTED DATA DESTROYED OR CORRUPTED** (P1–P4). *Priority #1.*
  - TE-A1 block-level, TE-A2 file-level (structure unchanged from v1).
- **TE-B** — unintended *unprotected* target destroyed (wrong disk / wrong node).
- **TE-C** — node unbootable or trapped in an install loop. **New UEFI branches:** boot
  redundancy silently absent (NVRAM entries never registered) = "redundancy theatre"; firmware
  gate wrongly passing a non-UEFI node (would recreate the removed BIOS brick class).
- **TE-D** — false outcome report (success-on-failure, wrong exit class, preview-that-mutates).
  Still the #2 practical priority: TE-D faults multiply every TE-A cut set via the 30 s retry
  loop — and 2026-07-11 proved this coupling **live** (two closed-on-paper exit faults made a
  safety refusal retry-loop instead of park).
- **TE-E** — install cannot complete on a supported config (availability). **New UEFI branch:**
  firmware gate refusing legitimately (BIOS/CSM lab or cloud Gen1 nodes) is *by design* but is
  an availability event operators must recognise (the refusal names the fix).

Firmware faults are modelled as **branches under TE-C and TE-E**, not a new top event: their
consequence is always "unbootable/degraded boot" or "cannot install", never a new consequence
class.

## Step 3 — Scope / analysis boundary  ★ the big change vs v1

**Mission:** on a PXE-booted node inside the Luna installer initramfs, running as root with
`--wipe` authorised, partition/RAID/LVM/format the intended disks, extract the OS image into
`/sysroot`, finalise the UEFI bootloader — never destroying protected data — and report the
true outcome to the retry/park wrapper.

**Inside the boundary (v2):**
- the Go module (all `lpart-*` binaries + `internal/`);
- **the client↔daemon↔wrapper integration seam** *(new in v2)*:
  - the daemon-rendered install template `templ_install_lpart.cfg` — its pre/part/post exit
    propagation (`exit $LUNARET`) and its interaction with node pre/part/post FIELD contents
    (dual-usage);
  - `95luna/luna2-start.sh` / `luna-installer.sh` retry/park behaviour (`LUNA_FATAL_RC=3`,
    `LUNA_SAFETY_RC=2`, `retry_countdown`);
  - the **lifetime of runtime files across the retry loop** — the install tmpfs survives
    between attempts, so anything a failed attempt wrote (`storage.json`, result files, state)
    is *input* to the next attempt;
  - the initramfs execution environment the binaries actually meet: `/dev/kmsg` semantics,
    udev device-name conventions, `q35`/machine-type device topology, OVMF/efivars presence.

  *Rationale:* all five 2026-07-11 real-PXE bugs (kmsg poller-park, config copy-back
  pollution, `runStepArgv` exit-code loss, template `exit 1` collapse, dual-usage) lived in
  this seam, which v1 had excluded. Excluding it made v1's Cluster-B "closed" claim
  unfalsifiable by unit tests alone.

**Outside the boundary (interface/enabling events, not decomposed):** luna2-daemon internals
(mode/layout *delivery* is an interface event), DHCP/hostname assignment, physical disk
hardware faults, BMC/firmware NVRAM manipulation between boots, the operator authoring the
disklayout.

**Mission-phase conditions treated TRUE** (unchanged): root, installer env, `--wipe`. They are
credited as barriers only against out-of-mission invocation.

## Step 4 — Resolution

Decompose to the function/guard level where a **barrier or pinning test can attach** — the same
resolution as v1 (e.g. "findmnt error is interpreted as no-mounts in `extract.go`" rather than
"mount detection fails"). Seam events resolve to the specific contract clause that broke (e.g.
"the template maps any non-zero child to `exit 1`", not "the wrapper misbehaves").

## Step 5 — Ground rules (explicit in v2; v1 applied them implicitly)

1. **Immediate-cause stepping** — each gate lists only the *immediate* necessary and
   sufficient causes; no skipping levels (NUREG-0492 "think small").
2. **No-miracles rule** — no credit for an unmodelled recovery (e.g. "the operator would
   notice"); recovery counts only where a coded barrier exists.
3. **Complete-the-gate rule** — finish each gate's inputs before descending.
4. **State-of-system vs state-of-component** — events are phrased as system states
   ("protected disk becomes a member of a destructive selection"), decomposed into component
   faults only at the leaves.
5. **Mission conditions TRUE** — root / installer env / `--wipe` are not barriers in-mission.
6. **Qualitative only** — no invented probabilities; ENV/IF annotations mark cut sets whose
   enabling condition is an *ordinary* operational state (they behave one order lower).
7. **Fault vs failure** — code that behaves as written but violates the safety intent (e.g.
   fail-open guard) is a *fault* of the design and modelled the same as a coding failure.

## Step 6 — Fault trees

### 6.1 Status re-baseline of v1 events (verified against the current tip)

Every v1 basic event was re-checked. Summary (full closure evidence in the Step-8 matrix):

- **CLOSED with pin** (barrier installed + removing-it-fails test + validation): BE-A1, A2,
  A5, A6, A7, A8, A9, A10; BE-B1–B9; BE-C1/C2; BE-E1 (C1), BE-E2 (C2 — **now HW-proven on
  UEFI**, RESULTS 3.4 + 3.14), BE-E3, BE-E4; BE-F1–F6; BE-G1, G2.
- **CLOSED at code level, HW-partial:** BE-A3/A4 (BR-9 by-id pinning + serial re-verify —
  virtio lab exposes no by-id/serial, so the HW leg is unproven).
- **Still OPEN:** BE-A11 (health-blind discover pick — accepted design, mitigated by BR-1/BR-2
  second barriers).
- **CORRECTED on re-verification (v2 had inherited stale v1 statuses):** BE-G3 was ALREADY
  CLOSED — `Options.StepTimeout` bounds every sub-tool via `context.WithTimeout`
  (`pipeline.go:414`, default 2h, kill message names the timeout); BE-G4's options-drop was
  ALREADY FIXED (`mount.go mountIfNeeded` honours per-fs options, comment cites D6).
- **CLOSED 2026-07-12 (NB-8):** BE-F7 — storage-check now REFUSES `--dry-run` outright
  (stubbing read-only probes would fabricate verdicts; pinned `TestDryRunRefused`);
  BE-G4/G5 residual — the synthesized memboot tmpfs is now `size=80%` (was the 50% default
  that stalled an 8GB node on a 4.4GB image; `decide.go membootSet`).
- **PRUNED (code deleted 2026-07-11):** every BIOS-only branch — the EF02/BIOS-GRUB partition
  path, `installBIOS` (rhel/debian), the BIOS `grub2-install` prepare checks, and the v1
  appendix item "C2 is UEFI-only" (BIOS raid1 `/boot`-on-disk-0 brick, F1): the non-UEFI
  world is now refused at the gate, so those events are out of the reachable state space.

### 6.2 New basic events — integration-seam class (BE-S*)

All found by the first-ever end-to-end run through the real Luna control plane (JOURNAL
2026-07-11). The seam is now in-boundary; these are its event register.

```
G-S  the client↔wrapper contract fails although each side passes its own tests
└─ OR
   ├─ BE-S1  /dev/kmsg backlog read parks the pre phase forever            [CLOSED]
   │         (O_NONBLOCK fd wrapped in *os.File → Go netpoller waits for
   │         the NEXT kernel message on an idle node)
   ├─ BE-S2  a failed attempt's OUTPUT becomes the next attempt's INPUT    [CLOSED]
   │         (storage-resolved.json copied over storage.json; tmpfs
   │         survives the 30 s retry → config_validation fails forever)
   ├─ BE-S3  the child's numeric exit class is lost inside the            [CLOSED]
   │         orchestrator (RunCommand returns (code, exitErr); runStepArgv
   │         returned the raw err first → safety exit 2 classified
   │         retryable → park never fires)
   ├─ BE-S4  the daemon template flattens every non-zero child to         [CLOSED]
   │         `exit 1` → wrapper cannot distinguish fatal (3) from
   │         retryable (1) → fatal_park dormant  (= LOOSE-ENDS #6)
   ├─ BE-S5  node pre/part/post FIELDS still hold the LEGACY bash disk    [MITIGATED·warn]
   │         script while install_mode selects lpart → both run (dual
   │         usage); legacy script mutates disks OUTSIDE every lpart
   │         barrier   ⚠ residual: warning only, not a hard block
   ├─ BE-S6  installer-initramfs sshd fails to start (deprecated          [OPEN·env]
   │         UsePrivilegeSeparation in 95luna/sshd_config) → no SSH into
   │         a wedged installer → observability loss during incidents
   └─ BE-S7  the post phase required storage-result.json BEFORE reading   [CLOSED]
             the install mode → sanitize (no results by design) failed
             the finalise precheck → retry loop  (found live 2026-07-12,
             the moment NB-1 let a sanitize run reach the post phase —
             the CCF-7 class caught a THIRD time by end-to-end execution)
```

**Evidence:** S1 `internal/engine/kmsg.go:29-53` (raw `syscall.Read`, EAGAIN-terminated; the
fix) — pinned `internal/engine/kmsg_backlog_test.go` (`TestReadKmsgBacklogDoesNotHang`).
S2 `internal/pipeline/pipeline.go:282-290` (copy-back removed; comment records the fault).
S3 `internal/pipeline/pipeline.go` `runStepArgv` (code-first `stepError`) — pinned
`internal/pipeline/result_test.go:104` (`TestRunStepArgvPreservesExitCode`, drives real
children). S4 `templ_install_lpart.cfg` pre/part/post now `exit $LUNARET` (deployed on the
controller + staged in `/root/lpart-review-out/v14-daemon-reconciled/legacy/`); validated
end-to-end 2026-07-11 (rc 2 → node-installer 3 → `exit $LUNARET` → fatal_park, no retry).
S5 `lpart_dual_usage_warn` in the same template (sets `install.warn.dual_<phase>`).
S6 `95luna/sshd_config` (packaging, outside the Go module — regression case 3.25).
S7 `internal/pipeline/pipeline.go` `runPost` (mode now resolved before the storage-result
precheck; sanitize/non-bootloader modes skip cleanly) — pinned
`TestPostPhaseSkipsBootloaderForSanitize` (`internal/pipeline/result_test.go`), found +
fixed + live-validated on node001 2026-07-12.

### 6.3 New basic events — device-naming class (BE-N*)

```
G-N  a device path names the wrong (or no) block device
└─ OR
   ├─ BE-N1  PartitionPath appends a bare number to a udev symlink name   [CLOSED]
   │         (…/by-path/pci-0000:03:00.0 + 2 → "…0.02", nonexistent) →
   │         single-disk manual-by-path /boot & /data mounts fail; RAID
   │         (mounts md) and discover (kernel names) masked it
   ├─ BE-N2  a pinned by-path device vanishes when the machine/firmware   [MITIGATED]
   │         topology changes (i440fx→q35 moved virtio behind PCIe root
   │         ports) → reservation fail-closed halts the install (BR-1b
   │         behaved correctly — availability, not data loss)
   └─ BE-N3  /dev/mapper|dm-* (multipath SAN) partition naming is not     [OPEN·low]
             covered by PartitionPath (mpatha1 vs mpathap1 vs -part1
             conventions vary by tooling); multipath disks never tested
```

### 6.3a New basic event — re-entrancy across TOPOLOGY change (BE-A6b)

```
BE-A6b  a stale same-name VG ("os") on a DIFFERENT disk's md array,        [CLOSED 2026-07-12 via NB-9]
        left by a prior install of a different topology, collides with
        the new layout's lvcreate -> exit 5 -> install fails, in an
        md-assembly-timing-dependent way. Cleanup's purgeVGsByName
        removes duplicate-name VGs BY UUID, but only ones `vgs` can see;
        an md array kept DISassembled by EnsureNoAutoAssemble hides its
        VG from the scan (so it is neither purged nor conflicting) until
        a later udev/assembly event surfaces it mid-install.
        CLOSURE (NB-9, landed 74323e4 as lvm.Builder.purgeStaleVGsByName,
        pinned by TestBuildPurgesStaleSameNameVG): a second purge-by-UUID
        runs IMMEDIATELY before vgcreate, so any same-name VG visible at
        create time is removed at the last possible moment; a VG that is
        still hidden at that point cannot surface later because
        EnsureNoAutoAssemble (AUTO -all) holds for the installer's whole
        lifetime, and stale members on SELECTED disks were already
        superblock-zeroed by cleanup. Members on UNselected disks stay
        untouched (protected-disk fence) — by design. Lab proof: §3.65.
```

### 6.3b New basic event — el9 LVM devices-file breaks partition LVM on serialed disks (BE-A6c) ★ real-HW ship-blocker

```
BE-A6c  el9+ LVM enables the devices file (/etc/lvm/devices/system.devices)  [CLOSED]
        which keys devices by IDTYPE=sys_serial IDNAME=<serial>. A PARTITION
        inherits its whole disk's serial, so `pvcreate /dev/sdX3` registers
        an idname already owned by /dev/sdX; the VG created on the partition
        is then "not in devices file" and `lvcreate` exits 5 -> EVERY disk
        install on real hardware (all HPC disks have a serial/WWN) fails.
```

**Evidence:** root-caused 2026-07-12 on a memboot node whose virtio disks were given real
`<serial>` values (for the by-id test). The exact install sequence reproduced it:
`pvcreate /dev/vda3` -> `WARNING: idname LPARTA001 which is already used for /dev/vda`;
`vgcreate os` "succeeds"; `lvcreate` -> `Volume group "os" not found` / `Cannot use /dev/vda3:
device is not in devices file` -> rc 5. The lab MASKED it for weeks because default virtio
disks have no serial (3-disk installs passed); the 5-disk serialed topology exposed the bug that
hits ALL real hardware. **Barrier (implemented + verified same day):** `EnsureNoLVMAutoActivate`
now also writes `devices { use_devicesfile = 0 }` to the installer's lvmlocal.conf, so every
lpart LVM command ignores the devices file (correct for a controlled wipe-all installer).
Verified on-node: the identical serialed-disk sequence then succeeds rc 0. Pinned by the extended
`TestEnsureNoLVMAutoActivate`. This is the highest-severity find of the whole regression campaign
— a silent install failure on 100% of real hardware, invisible to the virtio lab.

**Evidence:** found live 2026-07-12 running §3.41 on a node whose vdb/vdc still carried the
`os` VG from a prior §3.28 raid1 install (only vda was wiped): `lvcreate -y -W y -n root -l
100%FREE os: exit status 5` after `vgcreate os /dev/vda3` (a SECOND `os` VG). Distinct from the
c69b24b same-disk re-entrancy fix (that fix handles duplicates `vgs` can see on the selected
disk). **Barrier (NB-9, IMPLEMENTED 74323e4):** `lvm.Builder.purgeStaleVGsByName` purges every
same-name VG by UUID immediately before `vgcreate` (pinned by `TestBuildPurgesStaleSameNameVG`);
combined with cleanup's superblock-zero of every SELECTED disk partition and `AUTO -all` holding
for the installer's lifetime, a hidden VG can no longer surface mid-install. Members on
unselected disks are left alone (protected-disk fence). Originally reachable only when a node is
reinstalled with a DIFFERENT disk topology without wiping the old disks first — narrow, but it
silently failed the install (TE-E). Lab proof: §3.65.


**Evidence:** N1 `internal/devices/devices.go:29-46` (`-part<N>` branch for `/disk/by-`) —
pinned `internal/devices/devices_test.go` (`TestPartitionPath` by-path/by-id rows); found live
(regression case 3.11, JOURNAL 2026-07-11), fix live-validated on node001. N2 JOURNAL
2026-07-11 (node001 q35 conversion; `device not found: /dev/disk/by-path/pci-0000:00:05.0`
with candidate list — BR-1b fail-closed observed live); operational guidance: prefer
`selection:discover` + match filter, by-id (WWN/serial) over by-path. N3 `needsPPrefix`
covers only nvme/mmcblk/loop/md; no `/dev/mapper` or `/dev/dm-` case; the validator's allowed
styles (`validate.go:453`) don't include mapper paths either, so today this is reachable only
via kernel-name-style mapper paths — kept OPEN·low for the SAN/multipath future.

### 6.4 New basic events — firmware class (BE-W*)  ★ new subsystem 2026-07-11

Context: UEFI is now **required**. Gate: `cmd/lpart-storage-prepare/main.go:214`
(`safety_refusal=firmware-not-uefi`, exit 2 → park), placed **before the first side-effect**
(EnsureNoAutoAssemble's mdadm.conf write is after it). Belts: `partition.BuildLayout` refuses
non-uefi (`internal/partition/partition.go:185-191`); all three bootloader adapters' `Install()`
refuse non-uefi (rhel/debian/suse). Redundant-boot design: mirrored ESP+/boot (md metadata 1.0)
+ one NVRAM entry per mirror leg — HW-proven by regression 3.4/3.14 (degraded boot from the
surviving leg after a leg kill).

```
G-W  firmware handling produces a degraded or refused install
└─ OR
   ├─ BE-W1  gate false-negative: detectFirmware reports "uefi" on a      [OPEN·exotic]
   │         node that cannot complete the UEFI chain (kernel exposes
   │         /sys/firmware/efi but efivars/runtime services broken)
   ├─ BE-W2  gate refusal on genuinely-BIOS nodes (CSM labs, cloud        [BY-DESIGN]
   │         Gen1/legacy images) → install parked  (availability event;
   │         message names the fix: enable UEFI, reprovision)
   ├─ BE-W3  efivars absent under the sysroot → RegisterUEFIEntries       [OPEN·mitigated]
   │         returns nil SILENTLY (no log, no result flag) → NO NVRAM
   │         entries → the "boots from either leg" property (3.14)
   │         silently does not exist  → redundancy theatre on that node
   └─ BE-W4  NVRAM rewritten after install (firmware update, BMC,        [INTERFACE]
             other OS) → boot order no longer ours   (out of boundary;
             mitigated by the removable-path fallback below)
```

**Evidence:** W1 `detectFirmware` = `os.Stat("/sys/firmware/efi")` only
(`cmd/lpart-storage-prepare/main.go:614-621`); no efivars-writability probe at the gate.
W2 the gate itself + pins `TestRefusesBIOSFirmware` (`cmd/lpart-storage-prepare/main_test.go`),
`TestBuildLayoutRefusesBIOS`, `TestRHELInstallRefusesBIOS`, `TestDebianInstallRefusesBIOS`,
`TestSUSEInstallRefusesBIOS`. W3 `internal/bootloader/nvram.go:178-180` — the efivars stat
failure branch is the ONLY early-return in that ladder with **no log line** (RegisterNVRAM=no
and no-efibootmgr both log). Mitigation for W3/W4: `StageSignedShim` always populates the
removable path `EFI/BOOT/BOOT{X64,AA64}.EFI` + fallback CSV on **every** ESP mirror leg
(`internal/bootloader/uefi_signed.go`), so firmware's removable-media fallback still boots —
but boot-order preference and the *verified* dual-entry failover are gone, and
`bootloader_verify` skips the NVRAM check when efibootmgr is missing (`verify.go:43`), so W3
can pass verification silently.

### 6.5 New basic event — sanitize outcome (BE-Z1)

```
BE-Z1  sanitize succeeds (all disks blkdiscard'd) but exits 1            [OPEN·minor]
       (result.WriteAuto errors on the empty result set) → wrapper
       treats it as retryable failure → node re-erases blank disks
       every 30 s forever   (TE-D false report + TE-E availability)
```

**Evidence:** `internal/result/result.go:242-244` (`return fmt.Errorf("result.WriteAuto: no
results")`) reached from `cmd/lpart-storage-prepare/main.go:287` after `runSanitize` returns
`nil, nil, nil`; observed live (regression 3.13, RESULTS.md; console: `blkdiscard succeeded`
×3 then `exited 1` → retry countdown). Harmless to data (re-erasing blank disks) but an
honest-reporting fault: the operator sees FAILED for a successful erase.

### 6.5a Follow-on basic events — 2026-07-12 campaign v2 (BE-N4/N5, BE-G6, BE-W5/W6, BE-W7)

```
G-N/G/W  (extensions of the naming, availability and firmware classes)
├─ BE-N4  mdadm --create recorded the INSTALLER initramfs hostname as     [CLOSED]
│         homehost (metadata 1.x) -> the installed OS treats the array
│         as foreign and may assemble it as /dev/md127 instead of
│         /dev/md/<set> -> activateExisting / fstab-by-name consumers
│         break on SOME boots (udev-timing dependent).
│         Fix: --homehost=any on every create (raid.go); pinned by
│         TestBuildCreateSetsHomehostAny. Lab: §3.63.
├─ BE-N5  duplicate or EMPTY disk serials make verifyDiskIdentity pass    [MITIGATED·warn]
│         vacuously -> wrong-disk selection undetectable on cloned-
│         serial/cheap hardware. Now WARNs (serialCollisionWarnings,
│         main.go) — warn-only: refusing would break virtio labs where
│         serials are absent by default.
├─ BE-G6  a disk smaller than the layout's fixed-size partitions          [CLOSED]
│         surfaced as a generic 30s "timed out waiting for partitions"
│         (sgdisk had failed silently) -> misdiagnosis, retry loops.
│         Fix: BuildLayout preflights summed sizes vs the resolved disk
│         size and refuses loud BEFORE any write; pinned by
│         TestBuildLayoutRefusesTooSmallDisk. Lab: §3.64.
├─ BE-W5  makeGrubCfgDegradedSafe (mduuid strip) ran on the RHEL path     [CLOSED]
│         only -> Debian RAID boot kept mduuid-based grub.cfg (the
│         non-deterministic degraded assembly the fix removes). Now
│         invoked on the Debian install path too (SUSE already
│         delegates to the RHEL flow — pinned). Unit-only (no Debian lab).
├─ BE-W6  stale TrinityX NVRAM entries with duplicated or suffix-variant  [PINNED]
│         labels could in principle survive re-registration.
│         Behavior audit: the contains-match already deletes all of
│         them; pinned by TestRegisterUEFIEntriesDeletesAllStaleTrinityX.
└─ BE-W7  SecureBoot enforced with unenrolled MOK: detectFirmware only    [OPEN·doc-only]
          checks /sys/firmware/efi; StageSignedShim ships the signed
          shim, but a node enforcing SecureBoot against keys that do not
          cover the distro shim fails to boot with no lpart-side
          diagnosis. No lab coverage (OVMF here runs secure-boot off).
          Documented; a preflight probe of SecureBootEnable via efivars
          is the candidate barrier if this ever bites in the field.
```

### 6.6 New availability event — memboot RAM sizing (BE-G5)

```
BE-G5  memboot peak RAM = image tarball + extracted tree, both in        [OPEN·env]
       tmpfs → node with RAM < ~2× image size stalls/OOMs mid-extract
```

**Evidence:** JOURNAL 2026-07-11 (4.4 GB image: 8 GB node stalled, 16 GB completed; fix was
operational — RAM bump). Design note: a streaming `curl | tar` extract would halve peak RAM.

### 6.7 New basic events — 2026-07-18 six-distro sweep (BE-P1, BE-W8/W9, BE-G8)

The cross-distro widening (rocky 8/9/10, ubuntu 24/26, opensuse — all six now FAIL=0 on one
binary set) surfaced a new PORTABILITY class plus two firmware-chain events. All four are
CLOSED with pins; they are recorded because their enabling conditions are ordinary (any new
distro or tool version can re-create them) and because two of them were *self-masking* —
the system appeared healthy while the protection/boot was silently absent.

```
G-P  (portability class — external-tool surface varies per distro userland)
├─ BE-P1  a tool invoked with a version-gated flag/column (lsblk -o PATH,  [CLOSED]
│         util-linux >= 2.33; el8 ships 2.32) fails -> the CALLER treats
│         "command failed" as "nothing found" -> the dependent barrier is
│         SILENTLY OFF. Realized TWICE in one day, in two independent
│         copies of the same idiom (a CCF of the duplicated-code kind —
│         a shared helper would have failed once, been fixed once):
│           (a) cleanup.partitionsOf -> dormant protected-disk barrier
│               examined zero partitions -> spanning-md destruction
│               UNREFUSED on el8 (battery S24 + §3.47 refused=0);
│           (b) storage-prepare partitionsOn -> activateExisting saw zero
│               partitions -> persistent md NEVER assembled across an OS
│               reinstall on el8 (§3.51, retry-loop).
│         Fix: KNAME + /dev/ prepend (byte-identical where PATH worked),
│         e02d966 + e7e22b2; pinned by the KNAME test fakes and the NEW
│         static gate scripts/dev/check-version-gated-tools.sh (make
│         check) — the seen-twice mistake now has a machine rule.
│         discover.go had documented this exact pitfall since v1: the
│         lesson EXISTED and was reintroduced twice. Sweep rule: grep the
│         SYMPTOM ("unknown column") across the tree, not the fixed site.

G-W  (firmware/boot-chain class, extending 6.4)
├─ BE-W8  the family shim chains a loader NAME the stager did not         [CLOSED]
│         provide (SUSE shim -> grub.efi; stager staged grubx64.efi
│         only) -> BdsDxe "failed to start" -> firmware falls through to
│         PXE -> SILENT RE-PROVISION. Worst-shape outcome: the reinstall
│         MASKS itself as a healthy boot (§3.61's install counter was the
│         only honest witness: 40->43->44 per "reboot"). Fix: stage the
│         signed grub under BOTH names in EFI/<distro>/ and EFI/BOOT/
│         (40050e9); pinned in the arm64 staging test; §3.61 counter-
│         constant is the runtime gate.
├─ BE-W9  the signed grub's $prefix SEMANTIC differs per family: RH/     [CLOSED]
│         Debian bake an absolute /EFI/<distro>; SUSE derives it from
│         the LOAD DIRECTORY (cmdpath patch) -> booted via EFI/BOOT it
│         reads EFI/BOOT/grub.cfg, which did not exist -> bare grub>.
│         Verified at the live prompt (set -> prefix='(hd2,gpt1)/EFI/
│         BOOT'; configfile /EFI/sle/grub.cfg booted the degraded
│         survivor). Fix: write the stub grub.cfg beside the fallback
│         loader too (53d7770); inert for baked-prefix grubs; pinned.

G-G  (availability/diagnosability, extending 6.6)
├─ BE-G8  the installer's debug-ssh env is unreachable although the       [CLOSED]
│         installer is healthy: (a) the image's dropbear predates
│         rsa-sha2 (2019.78) and modern clients refuse SHA-1 -> every
│         probe dies at auth; (b) SUSE sshd wants privsep dir
│         /var/lib/empty which the 95luna fallback did not create.
│         Consequence: false-BLOCKED verdicts and lost diagnosability
│         exactly when a node needs remote autopsy. Fix: 95luna creates
│         all three privsep dirs (05968e1); the harness client enables
│         legacy signatures per-process (OPENSSL_ENABLE_SHA1_SIGNATURES);
│         §3.40 pins reachability per distro. Residual: dropbear-2019
│         requires the legacy client opts forever on el8 — recorded, not
│         product-fixable (image package set).
```

**CCF-9 (new):** BE-P1's two sites are one root cause expressed through duplicated code —
DRY violations are CCF enablers: every copy of an idiom is an independent opportunity for
the same latent fault, and fixing one copy leaves the others armed. The static gate (not the
fix) is what closes the class.

**Self-masking note (BE-W8/W9):** a boot-chain failure that lands in PXE re-provision looks
like SUCCESS to every "is the node up" check — only install-count monotony (§3.61) or a
persistent marker distinguishes a boot from a reinstall. Any future boot-chain change MUST be
validated against the §3.61 counter, not against "the node came up".

## Step 7 — Evaluation

### 7.1 Minimal cut sets v2 (TE-A + the new classes)

v1's MCS-1..12 are **retained with status**; MCS-13..18 are new. Order counts
failures/abnormal conditions beyond an ordinary config; ENV = ordinary operational state
collapses the order by one; IF = interface (out-of-boundary) event.

| # | Cut set | Order | Status / scenario |
|---|---|---|---|
| MCS-1 | {BE-A1} | 1 | **CLOSED** (BR-1a/1b/BR-2) — validated node001 V1. |
| MCS-2 | {BE-B1 ∧ BE-B2·ENV} | 1–2 | **CLOSED** (BR-3) — node001 V4. |
| MCS-3 | {BE-B5·ENV} | 1 | **CLOSED** (fail-closed summary, exit 2) — node001 V5. |
| MCS-4 | {BE-A10·ENV} | 1 | **CLOSED** (BR-7 read-only probes; `-L` banned). |
| MCS-5 | {BE-B7} | 1 | **CLOSED** (BR-5 dry-run firewall) — node001 V6. |
| MCS-6 | {BE-A3 ∧ enum-drift·ENV} | 1–2 | **CLOSED code / HW-partial** (BR-9; virtio has no by-id). |
| MCS-7 | {BE-A6} or {BE-A7} | 1 | **CLOSED** (BR-2 spanning refusal) — unit only. |
| MCS-8 | {BE-A5 ∧ wrong-mode·IF} | 2 | **CLOSED** (BR-8) — re-proven on UEFI 2026-07-11 incl. the exit-3 park. |
| MCS-9 | {BE-A9·ENV} | 1–2 | **CLOSED** (fail-closed diskBlank). |
| MCS-10 | {BE-B1 ∧ BE-B3 ∧ overlap} | 2 | **CLOSED** (guards in all 3 steps) — node001 V4. |
| MCS-11 | {BE-B9 ∧ symlink·ENV} | 2 | **CLOSED** (BR-6 resolveWithinSysroot). |
| MCS-12 | {BE-A2 ∧ operator-dup} | 2 | **CLOSED** (BR-1a/1b). |
| **MCS-13** | {BE-S5 ∧ legacy-fields·ENV} | **1–2** | **CLOSED (NB-2, 2026-07-12)** — template guard hard-blocks disk-mutating field content under lpart mode (exit 2 → park; `lpart-allow-custom` = explicit escape; custom non-disk hooks still warn-only). LIVE on node001: legacy parted+mke2fs part field → BLOCKED, 0 bytes written, fatal park. |
| **MCS-14** | {BE-S3} or {BE-S4} | 1 | **CLOSED** — exit-class loss (either seam side) re-opened the retry-multiplier on every safety halt. Both pinned + end-to-end validated 2026-07-11. Kept as a row because it *recurred after v1 closed Cluster B*: the pin now covers the seam, not just the unit. |
| **MCS-15** | {BE-S2} | 1 | **CLOSED** — retry-loop config pollution: every failed part-phase became permanently unrecoverable (TE-E) and masked the real error (TE-D). |
| **MCS-16** | {BE-N1 ∧ by-path-manual·ENV} | 1–2 | **CLOSED** — single-disk manual udev-path layout unmountable (TE-E; also TE-C when it survives to boot). |
| **MCS-17** | {BE-W3·ENV} | 1–2 | **CLOSED (NB-5, 2026-07-12)** — efivars-skip now logs a loud WARN; verification FAILS a multi-disk mirrored-boot install whose image lacks efibootmgr (single-disk tolerant; register_nvram=no = opt-out). Residual: the warn is log-only (no result flag) — acceptable, verification is the enforcement point. |
| **MCS-18** | {BE-Z1 (+BE-S7)} | 1 | **CLOSED (NB-1 + S7 fix, 2026-07-12)** — empty results = success for sanitize (exit 0) AND the post phase resolves the mode before requiring storage-result.json (BE-S7, found live the moment NB-1 unblocked it). LIVE on node001: erase ×3 → part rc 0 → post "no bootloader finalise for install_mode=sanitize" → install.sh success rc=0, node stops (nothing to boot). |

*Events with no cut-set row* — BE-S1 (closed; pure TE-E hang), BE-S6, BE-G5 (availability/
observability), BE-N3, BE-W1 (latent/exotic, no ordinary enabling condition today), BE-W2
(by-design refusal), BE-W4 (interface event) — follow v1's convention that TE-E/interface
events are tracked in the trees and the gap register, not the TE-A cut-set table.

**Cross-tree coupling (unchanged, now proven live):** any exit-class fault (TE-D) converts
every halted attempt into an infinite 30 s retry, multiplying exposure to all TE-A cut sets.
2026-07-11 demonstrated both directions: the broken chain retry-looped a safety refusal
(BE-S3/S4 before fix), and the repaired chain parked it (sanitize-persistent → rc 2 → 3 →
fatal_park, no retry).

### 7.2 Common-cause failure analysis (CCF-1..6 carried; CCF-7/8 new)

| CCF | Single underlying cause | Events it spawns | Status |
|---|---|---|---|
| CCF-1 | No fail-closed policy for guard-input tools | BE-B2, C1, A9, B5 | **CLOSED** (ADR 0010, one policy + pins) |
| CCF-2 | Native Go fs mutations bypass the executor chokepoint | BE-B7, B8, B9 | **CLOSED** (`internal/fsops` chokepoint) |
| CCF-3 | Device identity = kernel-name string | BE-A3, A4 | **CLOSED code / HW-partial** (BR-9) |
| CCF-4 | No barrier-regression protection in the port process | BE-B7, B9, G1 | **CLOSED as process** (BR-11 pinning suites) — and *vindicated*: it predicted the Cluster-B recurrence that became BE-S3/S4 |
| CCF-5 | Best-effort / silent-continue on safety-relevant paths | BE-F5, B4, B5 | largely CLOSED; **BE-W3 is a fresh instance** (silent efivars skip) — the class is not extinct |
| CCF-6 | No post-condition verification after irreversible steps | BE-A4 residual | partial (bootloader verify exists; W3 shows a verify hole) |
| **CCF-7** | **Contracts that only fail end-to-end are only tested in units.** The exit-code contract (S3/S4), the config-lifetime contract (S2), and the kmsg environment (S1) were each fine in isolation and broken in composition; v1's boundary excluded the seam, so no test could catch them. | BE-S1, S2, S3, S4, **S7** (+ historic F-cluster recurrence) | root cause CLOSED for the known instances (seam pins + the real-PXE regression as a required gate) — and STILL PRODUCING: BE-S7 surfaced 2026-07-12 the moment NB-1 unblocked the sanitize post phase; **process residual:** any NEW seam contract starts unpinned — see NB-3 |
| **CCF-8** | **Device-name convention assumptions are distributed, not centralised.** Partition naming (bare-N vs pN vs -partN), path styles (kernel/by-path/by-id/by-uuid), and topology stability (by-path vs machine type) are each assumed at multiple sites; PartitionPath was one instance, the validator's style list another, mapper/multipath a third. | BE-N1 (closed), N2 (mitigated), N3 (open) | partially closed; **NB-4** proposes the chokepoint audit |

### 7.3 Structural-importance ranking v2 (data-preservation first)

| Rank | Event / class | Why |
|---|---|---|
| 1 | **BE-S5 / MCS-13** (dual usage) | The only remaining order-1-with-ordinary-ENV path to TE-A: a legacy script that mutates disks with **zero** lpart barriers between it and the data. Warning-only today. |
| 2 | **BE-W3 / MCS-17** (silent NVRAM skip) | Defeats the purpose of the mirrored boot chain exactly when it matters, invisibly, and verification skips it. Mitigated by the removable fallback but the *redundancy claim* becomes false. |
| 3 | **BE-F7, BE-G3** (v1 leftovers) | Unverified false-HEALTHY dry-run probe; no sub-tool timeout — both feed TE-D/TE-E. |
| 4 | **BE-Z1 / MCS-18** (sanitize exit) | Order-1 honest-reporting fault; trivial fix; erodes operator trust in FAILED. |
| 5 | **BE-N3, by-uuid style residuals** (CCF-8) | Latent until SAN/multipath or exotic layouts arrive; cheap to fence now. |
| 6 | **BE-S6** (installer sshd) | Pure observability, but it is the incident-response path for everything above. |
| 7 | **BE-G5** (memboot RAM) | Availability only; operationally understood (RAM ≥ ~2× image). |

## Step 8 — Interpretation and presentation

### 8.1 Barrier status matrix v2 (delta over v1's appendix)

v1 appendix rows all remain closed with their pins; verified rows below are **new or changed**.

| Cut set / event | Barrier | Pinned by | Validated |
|---|---|---|---|
| BE-S1 kmsg park | raw-syscall backlog read (EAGAIN-terminated) | `TestReadKmsgBacklogDoesNotHang`, `TestReadKmsgBacklogRespectsLimit` (`internal/engine/kmsg_backlog_test.go`) | node001 real PXE (pre phase completes) |
| BE-S2 config pollution | copy-back removed; input config is never overwritten | `internal/pipeline/pipeline.go:282-290` (comment pins intent) | node001 real PXE retry survives |
| BE-S3 exit-code loss | code-first `stepError` in `runStepArgv` | `TestRunStepArgvPreservesExitCode` (`internal/pipeline/result_test.go:104`, real children) | end-to-end park 2026-07-11 |
| BE-S4 template collapse | `exit $LUNARET` in all 4 phase handlers | deployed template + staged bundle (`v14-daemon-reconciled/legacy/`) | end-to-end: rc 2→3→fatal_park, no retry |
| BE-S5 dual usage | `lpart_dual_usage_warn` (warning + `install.warn.dual_<phase>`) | template (deployed) | observed live | 
| BE-N1 udev -part<N> | PartitionPath `/disk/by-` branch | `TestPartitionPath` by-path/by-id rows (`internal/devices/devices_test.go`) | live re-run of case 3.11 on node001 |
| BE-N2 by-path drift | BR-1b reservation fail-closed (existing) + operational guidance (discover/by-id) | `reserve_test.go` | **observed live fail-closed** (q35 conversion) |
| Firmware gate (TE-C guard) | refuse non-UEFI before first side-effect + 4 belts | `TestRefusesBIOSFirmware`; `TestBuildLayoutRefusesBIOS`; `TestRHELInstallRefusesBIOS` / `TestDebianInstallRefusesBIOS` / `TestSUSEInstallRefusesBIOS` | gate silent on UEFI node001 (2 installs) |
| C2 mirrored boot chain | (v1 barrier) MirrorBoot all redundant modes + per-leg NVRAM | `TestMirrorBootForAllRedundantModes` (`internal/partition/partition_test.go:323`) | **UEFI HW-PROVEN: RESULTS 3.4 (both legs in NVRAM) + 3.14 (degraded boot from surviving leg)** — closes v1's "UEFI HW GAP (top item)" |
| BR-8 sanitize×persistent | (v1 barrier) | `TestSanitizeRefusesWithPersistentSet` (`reserve_test.go:148`) | **re-proven on UEFI incl. full exit-3 park** |
| BE-W3 NVRAM silent skip | **NB-5 (2026-07-12):** efivars-skip logs a loud WARN (`nvram.go`); verify FAILS a multi-disk mirrored-boot install whose image lacks efibootmgr (register_nvram=no = opt-out; single-disk keeps the tolerant skip) | `TestVerifyRedundantBootRequiresNVRAMTooling` (`internal/bootloader/verify_test.go`) | unit (live case needs an efibootmgr-less image) |
| BE-S5/MCS-13 dual usage | **NB-2 (2026-07-12):** template guard hard-blocks disk-mutating pre/part/post FIELD content under lpart mode (exit 2 → park); custom non-disk content still warns; `lpart-allow-custom` marker = explicit operator escape | `scripts/dev/dual-usage-guard-test.sh` (8 cases) | **LIVE on node001:** legacy parted+mke2fs part field → `BLOCKED` → rc 2 → `INSTALL FAILED FATALLY -- NOT retrying`; vda writes = 0 |
| BE-Z1 sanitize exit | **NB-1 (2026-07-12):** empty result set is SUCCESS for sanitize (exit 0); other modes stay fail-closed | `TestSanitizeWithNoResultsExitsZero` (`cmd/lpart-storage-prepare/main_test.go`) | **LIVE on node001:** erase ×3 → success rc=0, no retry loop |
| BE-S7 post-phase sanitize seam | mode resolved BEFORE the storage-result precheck; non-bootloader modes skip the finalise cleanly | `TestPostPhaseSkipsBootloaderForSanitize` (`internal/pipeline/result_test.go`) | **LIVE on node001** (same run: post skipped, install.sh rc=0) |

### 8.2 Recommended new barriers (analysis only — NOT implemented in this pass)

| ID | Barrier | Closes | Effort |
|---|---|---|---|
| **NB-1** ✅ done 2026-07-12 | Sanitize success contract: after a completed erase, write an explicit sanitize result (or treat empty results as success for `mode=sanitize`) and **exit 0** so the wrapper stops. | MCS-18 / BE-Z1 | trivial |
| **NB-2** ✅ done 2026-07-12 | **Dual-usage hard block**: when install_mode selects lpart and a pre/part/post FIELD contains non-`lpart-phase` content that matches known disk-mutating patterns, refuse (safety_refusal) instead of warn — with a `--allow-custom-scripts` style escape for legitimate custom hooks. | MCS-13 / BE-S5 (rank-1) | small (template + daemon-side lint) |
| **NB-3** ✅ done 2026-07-12 | **Seam contract gate**: `scripts/dev/seam-contract-gate.sh` fetches the RENDERED install script from the live daemon and verifies `exit $LUNARET` ×4 (no `exit 1` collapse), the dual-usage guard present + behaviourally blocking (executed against the rendered function), and the deployed wrapper park codes. Mandatory per release (regression.md §4a); client-side seam = the Go pins + make check. First run: 7/7 PASS against the live daemon. | future BE-S* class | — |
| **NB-4** ✅ done 2026-07-12 | **Device-name chokepoint** (CCF-8): audit found NO ad-hoc partition construction outside `internal/devices` (it already is the chokepoint); the v2 validator now REFUSES `/dev/mapper/*` and `/dev/dm-*` set devices fail-closed (dm partition naming has no reliable convention) and style-classifies SINGLE-device sets (previously skipped entirely). Pinned `TestDeviceMapperPathsRejected`. | BE-N3 + single-device style hole | — |
| **NB-5** ✅ done 2026-07-12 | **efivars-absent is loud**: log at warn level, set a result flag (`nvram_registered=false`), and make `bootloader-finalise` verification FAIL (or at minimum report degraded) when the mode expects NVRAM and efivars was absent — the removable fallback keeps the node bootable, but the report must say the failover entry set is missing. | MCS-17 / BE-W3 | small |
| **NB-6** ✅ done 2026-07-12 | Streaming extract: `osimage.StreamHTTPExtract` pipes curl\|tar under pipefail (no tarball on disk; no curl --retry inside the pipe — mid-stream retry corrupts tar); `--stream-extract auto` streams tmpfs sysroots (memboot) only, falling back to the classic payload-in-hand path on any failure. Pinned `stream_test.go` (4 tests). | BE-G5 | — |
| **NB-7** ✅ done 2026-07-12 | `UsePrivilegeSeparation` dropped from `95luna/sshd_config` (all 5 distro trees + deployed image) and `inst_dir /usr/share/empty.sshd` added to module-setup (the el9 privsep dir sshd refuses to start without). | BE-S6 | — |
| **NB-8** ✅ done 2026-07-12 | v1 leftovers: BE-G3 found already closed (StepTimeout, pipeline.go:414); BE-F7 closed (`--dry-run` refused on storage-check, `TestDryRunRefused`); BE-G4 closed (options honoured + memboot tmpfs `size=80%`). | BE-F7/G3/G4 | — |
| **NB-9** ✅ done 2026-07-12 | **Stale cross-array VG purge**: `purgeStaleVGsByName` (by UUID, immediately before `vgcreate`) + cleanup superblock-zero on selected disks + lifetime `AUTO -all` jointly close the hidden-VG window; fence-respecting (unselected disks untouched). Pinned `TestBuildPurgesStaleSameNameVG`; lab §3.65. | BE-A6b | — |
| **NB-10** ⚠ mitigation only 2026-07-12 | **Parity/raid10 degraded boot (BE-B10)**: verify-time WARN + docs — only **raid1** is a degraded-bootable OS RAID; raid5/6/**raid10** OS arrays boot only while healthy (all confirmed failing degraded on lpx). Diagnosis: the ESP stub is fine (search finds /boot); the MAIN grub.cfg fails NON-DETERMINISTICALLY under a degraded parity/striped array. A stub-`insmod` fix candidate was HW-tested + reverted (wrong location). Real fix = deeper GRUB main-cfg work — OPEN. Regression 3.62. | BE-B10 (TE-C) | large/open |
| **NB-11** ✅ done 2026-07-12 | Follow-on batch: `--homehost=any` on mdadm create (BE-N4, §3.63); disk-fit preflight in BuildLayout (BE-G6, §3.64); duplicate/empty-serial WARN (BE-N5); Debian degraded-safe grub.cfg parity (BE-W5); stale-NVRAM label-variant pins (BE-W6). | BE-N4/N5, BE-G6, BE-W5/W6 | — |

Recommended order: **NB-2 first** (only remaining ordinary-ENV order-1 TE-A path), then NB-5
(redundancy honesty), NB-1 (trivial), NB-3 (process), the rest opportunistically.

### 8.3 Gap register (unproven, not un-designed)

- **HW-unproven:** BR-9 by-id/serial identity on real hardware (virtio exposes neither);
  raid5/6/10 disk-pull boot matrix (needs ≥4–5 disks); BR-2 spanning-VG refusal on real
  spanning state; re-entrancy across real reboots (§3.19/3.20); distro min-bar boots
  (Ubuntu/Debian/SUSE memboot/single-disk/raid1 on UEFI); ARM end-to-end (BOOTAA64 path is
  unit-locked only).
- **Lab state:** node001 is UEFI (q35/OVMF); node002-004 are still BIOS **and part of the live
  demo cluster** — converting them needs operator scheduling. Until then, multi-node UEFI
  scenarios can't run.
- **Upstream:** the daemon MR (`/root/luna2-v1.4-patches` + reconciled bundle) is not merged;
  the whole seam contract currently exists only on the lab controller.
- **Residual OPEN events (after ALL NB barriers landed 2026-07-12):** BE-W1 (exotic) and
  BE-A11 (accepted design) only. CLOSED: BE-S5 (NB-2), BE-W3 (NB-5), BE-Z1+BE-S7 (NB-1),
  BE-F7/G3/G4 (NB-8), BE-N3 (NB-4 fail-closed refusal), **BE-G5** (NB-6 streaming — LIVE:
  4.4GB image streamed curl|tar into the RAM root in 75s, no tarball ever on disk — plus the
  80% tmpfs sizing), **BE-S6** (NB-7 — three stacked sshd faults fixed; SSH INTO the running
  installer demonstrated live on node001, regression 3.25 capability restored).

### 8.4 What v2 added beyond v1

1. The **boundary correction** — the client↔daemon↔wrapper seam is in-scope, with its own
   event register (BE-S*) and CCF (CCF-7). This is the structural lesson of 2026-07-11:
   every real-pipeline bug lived in territory the unit-tested tree could not see.
2. The **firmware class** (BE-W*) for the new UEFI-only design, including the one genuinely
   new open hazard it created: the **silent** efivars skip (BE-W3) that can quietly void the
   redundancy property the design exists to provide.
3. The **device-naming CCF** (CCF-8) generalising the PartitionPath bug beyond its instance.
4. **Closure of v1's top HW gap** — C2's mirrored boot chain is now proven on UEFI hardware
   (both-legs NVRAM + degraded boot from the surviving leg).
5. Re-ranking: the top residual risk is no longer a code defect but a **migration state**
   (dual usage, MCS-13) — the barrier that closes it is a policy decision (hard block vs
   warn), which is why it is a recommendation, not a patch.
