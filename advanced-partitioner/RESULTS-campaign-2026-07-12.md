# lpart campaign 2026-07-12 (v2) — results on the lpx cluster

Run on the **lpx** cluster (golden3-clone controller `lpx-ctrl` 192.168.123.148 + 4 UEFI nodes,
disks 3/3/4/5), brought up and wired **entirely by the new `trinityx-lpart-install` installer**
(clean-install validation = WP-C, PASSED). Evidence bundles under
`evidence/2026-07-12-campaign/<testid>-nN/` (console copies, screenshots, node-state, ctrl-log
slices). Machine table: `evidence/2026-07-12-campaign/RESULTS-campaign.md`.

## Headline

- **Parity/raid10 degraded boot: deeply characterised (BE-B10); the ESP stub is ruled out; the
  main grub.cfg fails non-deterministically.** The v1 handover called raid5/6 degraded-`grub>` a
  GRUB limitation and expected raid10 to be safe. This session:
  - **raid10 degraded ALSO fails at `grub>`** — so **only raid1** is a degraded-bootable OS RAID
    (supersedes the "raid1/raid10" guidance).
  - Serial-instrumenting the ESP **stub** proved it runs fine degraded (`search` finds `/boot` on
    a raid1 member, `configfile` is reached) — the stub is NOT the failure point.
  - The **MAIN `/boot/grub2/grub.cfg` fails NON-DETERMINISTICALLY**: identical raid6-degraded
    config, different failure per run (diag3 main ran-and-fell-through; diag4 main never
    executed). The main cfg already uses `terminal_output console` (gfxterm ruled out). Matches
    GRUB's known intermittent degraded-array resolution.
  - A candidate stub-`insmod` fix (had named it BE-B1, renamed BE-B10) was implemented, HW-tested
    (regression 3.62 + diag3/diag4), and **REVERTED** — it targets the wrong location and does not
    fix the main-cfg failure. The single earlier "boot" (r6diag-d2) was the non-deterministic
    degraded assembly succeeding by chance.
  - **Mitigation (NB-10, the diagnose+mitigate deliverable):** verify-time WARN + docs — use
    `raid1` (not raid5/6/10) for a degraded-bootable OS set. Real fix = deeper GRUB main-cfg
    degraded-path work, tracked OPEN. Commit `43d00e1` reverted by `e8aa7af`.

- **The installer deliverable works end to end.** A stock golden3 controller + one
  `./install.sh --offline` → seam-contract gate green + a node PXE-installs the lpart pipeline.
  Bugs found and fixed during validation (daemon-ini discovery, lever patch paths, luna flag
  names, idempotent "Nothing is changed", the 95luna dracut module with the wrapper park codes,
  the lchroot `wheel` dep) are all committed in `/root/trinityx-lpart-install`.

- **The prior flaky results were harness, confirmed by fixing the harness.** All of 3.27/3.52/
  3.59/raid10 shared the OVMF-NVRAM-accumulation wedge + fixed-sleep timing + console
  truncation. With `install_clean` (reset_nvram + blank-all), `console_follow` (survives the
  append='off' truncation), the **controller rsyslog** channel for lpart-internal text, and the
  **serial console kernel option** (`console=ttyS0,115200n8`) the NB-1 sanitize contract reads
  cleanly (erase 3 disks, `sanitize completed`, **0** retries) and the yank recoveries pass.

## Verdicts

| # | test | node | verdict | evidence / note |
|---|------|------|---------|-----------------|
| baseline | single-disk auto | n1-4 | **PASS** | all 4 boot os-root, nvram=1 |
| 3.29 G1 | raid5 healthy + serial-grub instr. | n3 | **PASS** | [4/4]×3, per-leg nvram=4, mduuid=0 |
| 3.29 G2 | raid5 degraded (blank leg) | n3 | **PASS**(reclassified) | booted once serial-grub in place; the un-instrumented stub is the failure — see BE-B1 |
| 3.29 G4 | raid5 degraded (detach leg) | n3 | finding→BE-B1 | same stub-autoload root cause (H2 eliminated) |
| 3.30 | raid6 healthy | n4 | **PASS** | [5/5]×3, nvram=5 |
| 3.30 single | raid6 degraded (un-instrumented stub) | n4 | finding→BE-B1 | grub> (the bug the stub fix closes) |
| r6diag d1/d2 | raid6 degraded, **instrumented stub** | n4 | **PASS (root cause)** | booted; search→prefix→MAINCFG traced — proves BE-B1 |
| 3.62 | raid6 degraded, **real stub fix** | n4 | _(r6retest, post-redeploy)_ | |
| 3.31 | raid10 degraded (old binaries) | n3 | finding→BE-B1 | **ALSO failed at grub>** — strengthens BE-B1: the bug is ANY non-raid1 degraded array (raid5/6/10) whose members `search` scans while diskfilter autoloads; raid1 alone is trivially readable degraded. Re-test on new binaries. |
| 3.59 partition | single power-yank + recovery | n1 | **PASS** | recovered os-root, md [2/2]×3 |
| 3.59 extract | single power-yank + recovery | n1 | **PASS** | recovered os-root, md [2/2]×3 |
| 3.59 bootloader | single power-yank + recovery | n1 | **PASS** | recovered os-root, md [2/2]×3 |
| 3.59 triple | 3 back-to-back yanks | n1 | **PASS** | recovered os-root, md [2/2]×3 — **resolves open item #2** (was harness NVRAM wedge, not a product re-entrancy bug) |
| 3.52 | sanitize lifecycle | n2 | **PASS (NB-1 contract)** | ctrl-log: erase 3 disks + `sanitize completed` + **0** retries; harness driver fix (pre-blank vda to force netboot) applied |
| 3.64 | disk-too-small refusal (BE-G6) | n1 | **PASS** | loud "too small for layout" refusal, no 30s partition timeout, vda untouched (wr=0) — **new binaries** |
| 3.62 | raid6 degraded, stub fix | n4 | **FINDING → fix reverted** | stub insmods didn't help; diag3/diag4 proved the failure is the main grub.cfg (non-deterministic), not the stub → BE-B10 / NB-10 |
| 3.63 | md-name stability (homehost) | n2 | finding (harness) | reboot2 no boot — warm-reboot NVRAM-ordering flake, not homehost; the `--homehost=any` fix is unit-pinned (`TestBuildCreateSetsHomehostAny`). Needs a cleaner re-test. |
| 3.65 | NB-9 stale cross-array VG | n3 | BLOCKED (harness) | memboot tmpfs lacks the full mdadm/lvm toolset to build the hazard; NB-9 itself is unit-pinned (`TestBuildPurgesStaleSameNameVG`) | |
| 3.53 | sanitize×persistent → resolution | n2 | _(driver corrected)_ | first attempt hit BE-P1 (persistent-on-blank); driver now populate-then-flip |
| 3.40 | parked-installer SSH loop | n1 | BLOCKED (BE-P1) | dl-persist on blank disk parks on a mount error, not the sanitize-persistent refusal; needs populate-then-flip setup |

_(remaining §3B — 3.36/3.39/3.54/3.55/3.58/3.60/3.34, 3.27/3.33/3.42/3.43, 3.46/3.47/3.48/3.49/
3.56/3.57 — deferred to a follow-up run; drivers written (`drv-ctrl/misc3/misc4.sh`), each
maps to already-shipped behaviour. Live status: the machine table.)_

## Code + docs landed (branch `advanced-partitioner-stable`, local)

- `43d00e1` BE-B1 parity degraded-boot stub fix (+test)
- `015e1df` FTA-v2 events BE-N4/N5/G6/W5/W6/W7 + NB-9/11 + regression 3.62-3.65
- `9d02b10` test seam fix (byIDDir) — `make check` now green on bare metal
- `ff417d6` mdadm homehost, disk-fit preflight, serial-collision warn, debian degraded-safe, NVRAM pins
- `9a55504` lab harness (cluster-param nreg + reg-orch + layouts)
- installer repo `/root/trinityx-lpart-install` (own history) — the self-contained deliverable

## Additional finding (BE-P1, minor UX)
A **persistent** data set declared on a **blank** disk fails the FIRST install:
`lpart-storage-prepare` honours "persistent = mount-only, never format", so on a disk with no
partition table it tries `mount /dev/vdb1` → `Can't lookup blockdev` → part exits 1 → install
fails into the retry-park (observed on lpx-node002). Correct usage is non-persistent-first then
flip to persistent (what test 3.51 does), so this is narrow — but a persistent set with no
existing filesystem could fail-closed with a NAMED diagnosis ("persistent set <name> has no
existing filesystem on <dev>; first provisioning must create it") instead of a raw mount error.
Candidate barrier; priority low. Test 3.53 driver corrected to populate-then-flip.
