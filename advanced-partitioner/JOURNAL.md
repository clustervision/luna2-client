# advanced-partitioner — JOURNAL (running log)

> Project log for the Go localboot installer, now a component of `clustervision/luna2-client`
> (`advanced-partitioner/`), rebased from `alex/installer-go` (see `decisions/0001`). Formal
> decisions live in `decisions/` (ADRs); cross-project conventions are in engineering-standards
> `gitlab-trinityx/STARTING-A-PROJECT.md`. (Repository facts in §A below predate the move — the
> live home is now luna2-client `advanced-partitioner/` on branch `advanced-partitioner`.)

> **Current design (2026-06-06):** the node storage and provisioning model was redesigned to v1.4.
> Start a new session from `docs/DESIGN-NOTES.md`; the full spec is `docs/storage-provisioning-model.md`
> and the build order is `docs/IMPLEMENTATION_PLAN.md`. It is designed but not yet implemented; the
> code below still uses the old `lpart-discover` and `intent` grammar that this model replaces.

Single source of truth for: long-running migration state, upstream patches we
rely on, deferred work, environment quirks, decisions we made and why, and
things future sessions need to know.

Keep this file flat and additive. New items go at the bottom of the relevant
section. **Do not remove entries; mark them resolved with a date.**

---

## Status (2026-06-03)
Go localboot installer, Phase 14 complete. **Relocated** from `alex/installer-go` into its
production home: a component of **`clustervision/luna2-client`** at `advanced-partitioner/`, on
branch **`advanced-partitioner`** (off `main` — this repo has no `development`). Clean restart, no
old git history carried (`decisions/0001`). `go build ./...` + `go vet` green from the new location.
**Build wiring DONE for redhat/x86** (2026-06-03, §G): `create_rpm.sh` cross-builds + stages the
`lpart-*` binaries into the RPM `%files`, `95luna/module-setup.sh` `dracut_install`s them into the
install initramfs, and the compute-group scripts run them from `/usr/bin` (no HTTP toolbox download).
ARM / openSUSE / Ubuntu trees deferred (untested targets). **Then refactored** (2026-06-03, §G):
`lpart-node-installer --phase pre|part|post` now owns all orchestration in-process (`internal/pipeline`
+ `internal/lpartinputs`), so the pre/part/post scripts are 5-line shims; `luna2-start.sh` got the
console printk-quiet + interruptible retry countdown. `make check` green; **RPM 2.0-23** built.
**Validated end-to-end on node001** (2026-06-04, §G) across an isolated reinstall loop up to
**luna2-client 2.0-27**: `install.success`, RAID1 OS on sda+sdb, `source=daemon`. Shipped the
countdown bugfix, dashboard accuracy (Luna panel, selected-disks, install method), kernel-message
capture, and real F1 Pause / F10 Reboot (dropped the no-op F5 Discovery). `make check` + `-race`
green. node001-level `--phase` shim overrides only; group + old toolbox files untouched for
node002/003. File-server disk hygiene fixed (superseded packs + aria2c handle release).
**Then (2026-06-04, §G), through luna2-client 2.0-33:** fixed a GRUB-rescue firmware-disk-boot bug
(osimage el9_8 BLS build-path; `normalizeBLSEntries`, **2.0-28**, validated on node001); reworked the
TUI per operator (Discovery panel, layout reorg, Use column dropped, F1 Pause removed, all
VGA-unrenderable Unicode → ASCII) + added a `lpart.notui` **plain mode** (**2.0-32**); and did a
behaviour-neutral **cleanup/flatten refactor** (**2.0-33**, `decisions/0002`): `lpart-node-installer` is
`--phase`-only, the dormant marker/child-process subsystem + `lpart-install-context` are gone, the hook
scripts share `/usr/bin/lpart-phase`, and the **module path is renamed** to
`gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner`. `make check` green; PXE
RAID1 install on 2.0-33 ✅. Then **2.0-34** (grub.cfg degraded-RAID fs-uuid robustness) and **2.0-35**
(Up/Dn scrolls the Run-output log; post-phase panels populated). **§3.8 RAID5 + RAID6 validated**
(mem OS + 4-disk data raid5/raid6). **One-file-per-host cluster-messages logging fixed on the
controller** (rsyslog hostname domain-strip; confirmed live). **§3.4b (degraded sdb-leg boot) PARKED**
— intermittent VMware-EFI boot-picker / `grub>`, not a deterministic GRUB bug (see §G). Latest:
**luna2-client 2.0-35** on node001 (mem OS + RAID6 data). Session report: `/tmp/regression-2026-06-04.md`.

## Next actions (priority order)
> **Next steps live HERE** — per engineering-standards `gitlab-trinityx/STARTING-A-PROJECT.md`, the
> JOURNAL's "Next actions" is the canonical place for what's next (formal ADRs go in `decisions/`).

> **⚠ MERGE GATE — cross-platform validation before the MR lands.** redhat/x86 is the only tested
> target. **openSUSE/SUSE** (`bootloader/suse.go` is a "not implemented" stub — needs a real
> bootloader adapter: `grub2-mkconfig`/shim paths, `update-initramfs` vs `dracut`) and
> **Ubuntu/Debian** (`bootloader/debian.go` exists but never exercised: `grub-install`/`update-grub`,
> BLS-less boot, `update-initramfs`, `mkfs.fat` vs `mkfs.vfat`) **must be built + installed end-to-end
> on a real node of each** (mem, single-disk, RAID1 at minimum) before merge. See §L for the
> per-module cross-platform checklist. Also fan the redhat/x86 packaging wiring (create_rpm /
> module-setup / %files / lpart-phase) out to redhat/arm + opensuse/{x86,arm} + ubuntu/{x86,arm}.

1. **Work on the partitioner** — the active task (the `advanced-partitioner` Go code).
2. **Wire the Go build into luna2-client packaging** — DONE for redhat/x86 (2026-06-03, §G).
   Remaining: on-node verify (rebuild an osimage with luna2-client-2.0-22, PXE-boot, confirm the
   lpart-* binaries are in the initramfs and the partscript runs them from `/usr/bin`); then fan the
   same wiring out to redhat/arm + opensuse/{x86,arm} + ubuntu/{x86,arm} once those targets are tested.
3. **Rename the `go.mod` module path** — DONE (2026-06-04, §G / `decisions/0002`): now
   `gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner`.
4. **§3.4b degraded-leg disk-boot (`grub>`)** — decide: log-as-known-issue vs implement the candidate
   fix (rewrite `/boot/grub2/grub.cfg` device resolution from `mduuid/…` to plain fs-uuid so a single
   metadata-1.0 member boots without md assembly). Needs live degraded re-test. See §G 2026-06-04.
5. **Finish the regression matrix** on 2.0-33: §3.8 RAID5/RAID6 (mem OS + data RAID on the 4 disks),
   §3.12/§3.13 persistent set, §3.6 OS-in-mem + persistent data, §4.1/§4.3/§4.5/§4.6/§4.7 osimage.
6. **★ FAST-TRACK — installer observability / one-file-per-host logging.** Brief:
   `advanced-partitioner/TASK-installer-observability.md`. Goal: install messages land in
   `/var/log/cluster-messages` as **ONE file per host** (today it's two: `<host>.cluster.messages` from
   the install env + `<host>.messages` from the booted OS, — **DONE on the live controller (2026-06-04): rsyslog domain-strip in both cluster-messages templates so <host>.cluster + <host> collapse to one <host>.messages; tested; upstream to the rsyslog ansible role**. The install env runs as
   `<host>.cluster`). Stop suppressing the kernel (drop `engine.QuietConsole()` default + the
   `luna2-start.sh` printk line), widen `PollKmsg` to all levels, and add `engine.ForwardSyslog()` to
   stream each content/stage/kernel event live to `loghost:514` stamped with the node FQDN — replacing
   the end-of-run batch `logger -n`. TUI frames must never enter the log/forward stream
   (`TeeToFile.shouldLog` already writes content-only — extend that selection to the forwarder). The
   netconsole 6666/`%fromhost%` apparatus stays only as a kernel/early-boot/panic safety net.
7. Push the one-line `lpart-phase` group shims to Luna (node001 still uses inline `--phase` node overrides).
7. (deferred) Phase 15 toolbox-in-ramdisk, Phase 16 deployment migration — see sections below.

---

## A. Repository facts

- Local clone: `/root/InstallerGo/`
- Origin: `https://gitlab.taurusgroup.one/alex/installer-go.git` (public)
- Go: 1.25 (Red Hat 1.25.9-1.el9_7), on the controller `controller1.cluster`
- Controller hostname: `controller1.cluster`
- Target nodes: all in Luna group `compute-group`.
  - `node001` — RAID1 disk install (4 approved destructive disks:
    `/dev/sda /dev/sdb /dev/sdc /dev/sdd`); driven by its node-level
    `tx_discover_config` override, `tx_install_mode=auto`.
  - `node002` / `node003` — tmpfs (memory-root) boot; `tx_install_mode=mem`,
    no per-node script or discover overrides.
- Provisioning scripts live at **group level** on `compute-group`
  (pre/part/postscript), node-agnostic via `${_HOSTNAME}`. No node has a
  per-node script override anymore — the only per-node config is the `tx_*`
  fields. Canonical copies versioned at `runtime/group/compute-group/`.
  See §G 2026-06-03. Extract/inspect live values with `luna group show
  compute-group -R` / `luna node show <node> -R` (`-f` for full scripts).
- Behavioural oracle: `/root/NEWTREE/` (Bash tree — do not modify during migration)
- Luna file server: `/trinity/local/luna/files/` (served as `http://10.141.255.254:7051/files/`)
- Companion repo: `https://gitlab.taurusgroup.one/alex/luna2-phase14-patches.git` (Phase 14e patch series for clustervision/luna2-daemon + luna2-cli).
- GitLab "Prime" instructions for this controller's GitLab usage live at
  `alex/gpt-project-prime` (project id 135). Fetch at session start with:
  ```bash
  curl -sk --header "PRIVATE-TOKEN: $GITLAB_TOKEN" \   # set $GITLAB_TOKEN in your shell; never commit the literal token
    "https://gitlab.taurusgroup.one/api/v4/projects/135/repository/files/PRIME.md/raw?ref=main"
  ```
  PRIME.md documents the `mila`/`gitlab` command vocabulary and the
  hard-rule constraints (no repo deletion; writes only inside the `alex/`
  namespace; reads + clones from any repo are fine).

---

## B. Upstream patches that must remain in place

These are patches outside this repo. If they get reverted, the localboot flow
breaks. Re-apply if the corresponding file ever gets refreshed by a Luna
upgrade.

### B.1 Luna prescript shebang rewrite

Bash glue files have their shebang rewritten to `#!/usr/bin/bash` by the
prescript (`runtime/nodes/node001-localboot/prescript.sh` line ~36). Go
binaries do not need this — they are statically linked. Once all bin/* files
in the toolbox are Go, the shebang loop can drop the bin/ pattern but must
still cover any remaining lib/*.sh helpers.

Status: still required for Bash glue, not for Go binaries.

---

## C. Migration status

All phases 1–14 complete (latest: Phase 14h, 2026-05-21). Phase 15
(toolbox-in-ramdisk) and Phase 16 (deployment migration) are deferred —
see §I.

For the chronological build history, run `git log --oneline`. For why a
specific choice was made, see §G.

<!-- archived phase log retained below for cross-reference --><details><summary>Phase log (archived 2026-05-23)</summary>

- [x] Phase 1 — Scaffold + Makefile + linter baseline + foundation packages
      (`internal/log`, `internal/executor`, `internal/cli`); 5 binaries
      compile statically for linux/amd64 + linux/arm64; `make check`
      green (fmt + vet + test + lint + govulncheck). Completed 2026-05-20.
- [x] Phase 2 — `internal/config/v2`: typed structs, Validate() with
      byte-for-byte error parity with `lib/json_v2.sh`, accessors
      (RAIDMode, VGName, StorageType, SelectorType, EffectiveDevices,
      ClearUEFINVRAM, ExecutionOrder, boot helpers, safety helpers),
      golden tests against the verbatim NEWTREE valid corpus + 34
      synthesised invalid fixtures covering every error path.
      Completed 2026-05-20.
- [x] Phase 3 — `internal/discover` (lsblk parsing tolerant of legacy
      string/scalar rota/size + by-path resolution with fallback warning)
      and `internal/intent` (SetSpec parser, size parser, FilterDevices,
      AssignDevices, CheckRAIDTolerance, AutoDetect rules, OSLayout
      scale-down ladder, BuildConfig assembling a v2.Config that passes
      Validate). `cmd/lpart-discover` now drives real flow with all bash CLI
      flags (--config / --set / --grub / --names / --raid-tolerance /
      --out) plus a hidden --efivars-dir test affordance. End-to-end
      tests use a fake lsblk shim on PATH to exercise mem/auto-detect/
      RAID5/--config/--out/--grub paths. Completed 2026-05-20.
- [x] Phase 4 — `internal/disk/selector` (Resolver with selector dispatch
      via v2.SelectorTypeOf, structured "device not found" candidate
      dump replicating _selector_device_not_found_error, full validation
      cascade: exists -> EvalSymlinks -> block device -> lsblk type
      "disk" -> findmnt not mounted -> RO=0 -> capture SIZE/MODEL/SERIAL)
      and `internal/disk/inventory` (Snapshot for before/after lsblk +
      findmnt sysroot, FinalState for blkid/pvs/vgs/lvs/mdadm). Both
      packages testable on any host via FakeRunner + t.TempDir; the
      remaining /dev-requiring branches land in Phase 7 integration.
      Completed 2026-05-20.
- [x] Phase 5 — `internal/cleanup`: full 5-step cascade ported from
      lib/cleanup.sh. Cleaner.CleanupSelectedSet orchestrates unmount
      under sysroot → VG teardown (with dmsetup probes) → same-disk
      VG removal with the md member-intersection fix → md array stop
      (named + /proc/mdstat scan) → ZFS labelclear → wipefs/sgdisk/
      partprobe with partition-wait loop. Helpers DiskBasename and
      PartitionOnSelectedDisk correctly handle NVMe (nvme0n1p1 →
      nvme0n1) where the Bash version was broken. Every cleanup
      command is best-effort: failures log via the supplied Logger
      but do not abort the cascade. 18 unit tests cover the
      parsers, force-unmount lazy fallback, VG teardown, the
      md-intersection bug-fix path explicitly, and an end-to-end
      cascade with FakeRunner. Completed 2026-05-20.
- [x] Phase 6 — destructive-create modules: `internal/devices`
      (PartitionPath, VGLVPath), `internal/partition` (sgdisk layout
      with NVMe-correct partition paths and FD00 type codes for
      mirrored boot/efi), `internal/raid` (mdadm storage array +
      mirrored efi/boot for raid1+UEFI, metadata 1.0 for the boot
      arrays, 1.2 for storage), `internal/lvm` (pvcreate→vgcreate→
      lvcreate with fixed-size LVs before 100%FREE), `internal/fs`
      (wipefs + mkfs dispatch for xfs/ext4/vfat/swap/tmpfs, plus
      MemoryFilesystems for the tmpfs path), `internal/mount`
      (sysroot tree mount shallow-first + unmount deepest-first, with
      "already mounted with other source" guards), `internal/safety`
      (block_device_size). All packages take an executor.Runner so
      tests drive the entire flow via FakeRunner — 40+ unit tests
      verify command sequences, RAID member counts, layout JSON
      shape, and refusal on conflicting mounts. Plan-then-apply is
      realised through the existing dry-run executor (no separate
      Step{} type yet). Completed 2026-05-20.
- [x] Phase 7 — `lpart-storage-prepare`: full per-set orchestrator wired
      through every Phase 1–6 module. End-to-end cmd-level tests with
      FakeRunner cover memory, single-disk, raid1+lvm, and multi-set
      paths. Code-complete; on-node validation against bats p01/p02/p07
      tracked under Phase 13.
- [x] Phase 8 — `lpart-storage-check`: full health-check binary (vg
      activate, blkid type match, fscheck), exit codes 0/1/2 matching
      the bash predecessor. Code-complete; on-node validation against
      bats s18–s27 tracked under Phase 13.
- [x] Phase 9 — `lpart-osimage-install`: filter parity (preserve /
      preserve_empty / no_new_files), FirstSymlinkResolver, BuildTarExcludes,
      aria2c-or-curl downloader, /sysroot bind + extract with --acls/--no-acls.
      Code-complete; on-node validation of the live download + tar
      extract tracked under Phase 13.
- [x] Phase 10 — `lpart-bootloader-finalise`: rhel + debian adapters, SUSE
      stub, dispatch by Family, fstab writer, UEFI NVRAM dance (delete
      stale → register per-disk → reorder BootOrder → validate first),
      BIOS path, ClearUEFINVRAM. Code-complete; on-node validation against
      bats p03/p04/p06 tracked under Phase 13.
- [x] Phase 11 — re-entrant. Added explicit second-pass tests at the
      module level (cleanup, mount, fstab, osimage), plus the
      documentation-only `safety.preserve_devices` field on v2 sets
      and `scripts/run-on-node.sh` for node001 staging. See D.8 / D.9.
      Completed 2026-05-20.
- [x] Phase 12 — install-mode + osimage-sync wiring. New
      `internal/installcontext` package defines the Mode (mem/init/
      existing) and Sync (Sync/Full/Local) enums, a `/proc/cmdline`
      parser, and a `Load` function with documented precedence
      (explicit > env > JSON file > cmdline > defaults). New
      `cmd/lpart-install-context` binary writes `runtime/install-context.json`
      from that resolution, replacing the prescript's inline heredoc.
      All five existing binaries gained a `--install-context` flag and
      log the resolved values; the binaries that are mode-specific
      enforce their constraints: lpart-discover refuses install_mode!=init,
      lpart-osimage-install refuses osimage_sync=Local, lpart-storage-check
      warns when osimage_sync!=Local. Each constraint has an
      `--ignore-install-context` operator escape. Partscript updated to
      call lpart-install-context once and route osimage_sync=Local to
      lpart-storage-check instead of lpart-osimage-install.
      Completed 2026-05-20.
- [x] Phase 13 — on-node validation. All four scenarios driven
      end-to-end on node001 (memory-root, single-disk-direct, raid1-os,
      data-raid5). data-raid5 passed twice consecutively (re-entrancy).
      Three latent bugs surfaced + fixed (PATH, stage-log accumulation,
      run-on-node IFS split). Multi-set sysroot-unmount divergence
      from bash captured + fixed via cleanup.Set.OwnsSysroot.
      9-combination install-context matrix validated via env-var
      overrides (no PXE reboot required for routing logic). All of
      the above is now codified as a Go integration test suite at
      tests/integration/ under build tag `node001` — see §H. Completed
      2026-05-20.
- [ ] Phase 14 — Luna-managed JSON + persistent flag. Eight batches per
      INSTALLER_PLAN.md §9. Status:
  - [x] 14a v2 schema + cleanup fast-path (2026-05-21):
        `safety.preserve_devices` removed, `safety.persistent` added,
        `allowedSafety` allowlist + invalid-fixture in place; cleanup
        cascade short-circuits when `Set.Persistent=true` and asserts
        zero commands via `TestCleanupSelectedSetPersistentSkipsEntirely`.
  - [x] 14b per-binary persistent honour (2026-05-21):
        lpart-storage-prepare persistent fast-path (no cleanup/create,
        mount-only via `fsmod.PredictFilesystemsForSet`);
        `StorageResult.Persistent` field; lpart-osimage-install auto-
        excludes persistent set mountpoints in every mode with defensive
        post-check; lpart-storage-check exits 2 with `safety_refusal=
        persistent` log marker on persistent-set health failure.
  - [x] 14c installcontext rework + exit codes (2026-05-21): cmdline
        + env carriers removed, enum renamed to `{auto, mem, force-init}`,
        new 3-source precedence (explicit > file > defaults),
        `cli.ExitSafetyRefusal` helper. lpart-discover refuses anything
        other than auto/force-init; lpart-osimage-install / lpart-storage-check
        constraints unchanged.
  - [x] 14d runtime/examples cleanup (2026-05-21): five per-scenario
        directories deleted, minimal `prescript.sh` (toolbox +
        skip_unpack only), `partscript.sh` now routes off Luna-populated
        JSON files via the auto/mem/force-init enum, `postscript.sh`
        POSTs `tx_storage_b64` back to Luna at end. README rewritten
        around `luna node change -qlpart-*` workflow.
        `runtime/node001-localboot-partscript.sh` and
        `runtime/nodes/node001-localboot/partscript.sh` also updated.
  - [x] 14e Luna patches (2026-05-21): eight patches committed to
        `alex/luna2-phase14-patches` (six for luna2-daemon, two for
        luna2-cli), one SQL migration, one apply script, applied-state
        copies of every touched file, and a README. All eight dry-run
        cleanly with `patch -p2` against the upstream HEADs
        (luna2-daemon `6e6a7de2`, luna2-cli `5e4ed24`). The patches
        also went in-place onto this controller (`/trinity/local/
        python/lib/python3.10/site-packages/daemon/` + `.../luna/`)
        plus the SQL migration on `/trinity/local/luna/daemon/common/
        luna.db`, with a snapshot at `/root/luna-phase14f-backup-
        20260521-171554/` for rollback. The 5 new fields show up in
        `luna node show node001` and the `-lpart-*` / `--lpart-*` CLI flags
        work. Daemon restart was needed; HA "overrule" had to be
        flipped via `GET /ha/overrule/_set` because controller2 is
        unreachable.
  - [x] 14f template patch (2026-05-21): bundled into 14e patch 06.
        `download_image` and `unpack_imagefile` bodies in
        `templ_install.cfg` replaced with no-op stubs; `write_tx_inputs`
        function added; called from main flow AFTER `prescript` (the
        first attempt called it before prescript, which then `rm -rf`'d
        `/tmp/install_files` and wiped the staged JSONs — caught by
        14g, fixed in commit `91a473d`). B.1 retired in §B.
  - [x] 14g on-node validation (2026-05-21): node001 booted clean with
        the full Phase 14 chain at 15:34:18 UTC. Log shows
        `install_mode_source=file` (write_tx_inputs landed
        install-context.json), `auto mode: tx_storage empty,
        tx_discover_config populated -- running lpart-discover` (auto
        routing decision worked), `download_image handled by
        lpart-osimage-install (no-op here)` and matching
        `unpack_imagefile handled by lpart-osimage-install (no-op here)`
        (legacy bash flow retired), exit status success. The earlier
        boot at 15:26 UTC was the pre-fix bug (ordering); see §G
        2026-05-21 entry "templ_install ordering".
  - [x] 14h BOOKKEEPING updates (2026-05-21): this entry + B.1 retired
        + §A PRIME.md curl recorded + §G decision-log entries for
        Phase 14e/f/g.

</details>

---

## D. Behavioural-parity contracts to preserve

Things future Go code must reproduce byte-for-byte until the corresponding
Bash code is retired. If a contract changes, document the new shape here.

### D.1 v2 storage JSON

Authority: `/root/NEWTREE/JSON_V2_SCHEMA.md`. Mirror at
`docs/storage-v2-schema.md` (TODO). Validation error messages must match the
pattern `config_validation: <path>: <reason>` so existing tests still pass.

### D.2 storage-result.json

Authority: `/root/NEWTREE/lib/output_json.sh::assemble_storage_result`.
Fields consumed downstream by `lpart-bootloader-finalise`: `mounted_target_root`,
`filesystems[]`, `boot_devices[]`, `boot_method`, `boot_firmware`,
`register_nvram`, `clear_uefi_nvram`, `firmware_mode`, `raid_mode`,
`storage_type`, `root_device`, `lvm`, `mdraid_device`, `efi_md_device`,
`boot_md_device`. Two output shapes:

- Single-set: object at root.
- Multi-set: `{version: 2, sets: [...]}`.

### D.3 boot-context.json

Authority: `/root/NEWTREE/lib/bootloader_common.sh::build_boot_context_json`.

### D.4 fstab field conventions

Reproduce the dump/pass logic in `bootloader_common.sh::write_fstab_from_result`:

- ext2/3/4: dump=1, pass=1 (root) or pass=2 (others)
- xfs/btrfs/swap/vfat: dump=0, pass=0
- /boot/efi: options `umask=0077,shortname=winnt`
- tmpfs: options from volume `.options` field
- Preserve any existing fstab entry already present at that mountpoint.

### D.5 GRUB kernel args

`bootloader_common.sh::bootloader_kernel_args`: `rd.auto`, `rd.md=1` if RAID,
`rd.lvm=1` if LVM, `rootfstype=<fs>` for xfs/ext4/ext3/btrfs.

### D.6 clear_uefi_nvram label allowlist

`bootloader_common.sh::clear_uefi_nvram_entries`: deletes UEFI entries whose
label does NOT contain `PXE`, `Network`, `Luna`, or `iPXE`. Do not narrow
this regex without testing — it is the safety net that keeps the network
boot entry alive.

### D.7 md cleanup member intersection

`cleanup.sh::_cleanup_lvm_on_selected_disks`: only md-backed VGs whose member
disks intersect the selected-disk set are removed. The bug previously
unconditionally removed any `md*` PV — see `/root/NEWTREE/NEXT_CHAT_HANDOFF_INSTALLER.md`
section "cleanup.sh: md-backed VG cleanup too aggressive".

### D.8 Re-entrant guarantees per module (Phase 11)

Every binary must be safe to re-run on a previously-prepared system. The
Go packages enforce this in the following places, each backed by a
dedicated unit test:

- **internal/cleanup**: probes for existing VGs / md arrays / partitions
  via lsblk + vgs/pvs/lvs + /proc/mdstat. Missing state is a no-op, not
  an error. Tests: `TestReentrantCleanupOnAlreadyCleanState`,
  `TestReentrantCleanupAfterPriorPreparePass`,
  `TestReentrantCleanupTwiceInARowSameInput`.
- **internal/partition / raid / lvm / fs**: cleanup runs first
  (orchestrated by lpart-storage-prepare), so these layers always see a
  wiped disk; their own idempotency is provided by the cleanup pass.
  `wipefs -af` before every `mkfs` also rescues a re-format from stale
  signatures.
- **internal/mount**: `mount_if_needed` no-ops when the target is
  already mounted with the same source; fails loudly when the target is
  mounted with a *different* source. Tests:
  `TestReentrantMountIsNoOpWhenAlreadyMounted`,
  `TestReentrantMountTwiceInARowDoesNotRemountChildren`,
  `TestReentrantUnmountTwiceIsSafe`.
- **internal/bootloader.WriteFstab**: preserves existing lines and
  appends only entries whose mountpoint is not already present (word-
  boundary match). Three consecutive calls produce exactly one entry
  per mount. Manual operator edits survive. Tests:
  `TestReentrantFstabSecondCallDoesNotDuplicate`,
  `TestReentrantFstabPreservesManualEdits`.
- **internal/bootloader.RegisterUEFIEntries**: deletes stale TrinityX
  NVRAM entries BEFORE creating new ones, so a second pass cannot
  accumulate duplicates. BootOrder is rewritten in full each pass.
- **internal/osimage.PrepareSysroot**: top-level wipe walks the live
  filesystem; if the prior pass already removed something, ReadDir just
  doesn't return it. Preserve-empty directories that already exist as
  empty are unchanged. Tests:
  `TestReentrantPrepareSysrootOnAlreadyCleanState`,
  `TestReentrantPrepareSysrootTwiceInARow`.

The Phase 5 md member-intersection regression test
`TestTeardownSameDiskVGsMDMemberIntersectionBugFix` continues to cover
the p11 safety property: md-backed VGs whose members do NOT intersect
the selected-disk set survive cleanup intact.

### D.12 `safety.persistent` (Phase 14a)

Per-set boolean flag (`safety.persistent: true|false`, default false).
Hard hands-off guarantee — no CLI flag overrides it. Honoured in three
binaries:

- **lpart-storage-prepare** (`cmd/lpart-storage-prepare/main.go`): persistent
  set skips cleanup, partition, raid, lvm, fs entirely. Only
  `fsmod.PredictFilesystemsForSet` + `mount.MountSysrootTree` run, so
  existing volumes are mounted against their predicted device paths.
  `StorageResult.FirstInstall` is forced false for persistent sets even
  when `--wipe` is in play (the layout was not rebuilt).
- **lpart-osimage-install** (`cmd/lpart-osimage-install/main.go`): every
  persistent set's mountpoints are added to the tar exclude list as
  `--exclude=./<mp>` + `--exclude=./<mp>/*` in every mode (Full, Sync,
  Local). Each gets a `persistent-set-mountpoint excluded from extract:
  <mp>` log line. A defensive post-check exits 2
  (`safety_refusal=persistent`) if a persistent mountpoint somehow
  failed to make it into the excludes.
- **lpart-storage-check** (`cmd/lpart-storage-check/main.go`): a persistent
  set's health failure exits 2 via `cli.ExitSafetyRefusal` with
  `safety_refusal=persistent` in the log line; non-persistent failures
  exit 2 without the marker. The future Go installer UI can pattern-
  match the marker to distinguish "things broke, see logs" from "do
  NOT continue without operator review."

`cleanup.Set.Persistent bool` (the cleanup cascade's input record) +
`cleanup.CleanupSelectedSet` short-circuit before any probe runs:
`TestCleanupSelectedSetPersistentSkipsEntirely` asserts zero commands
hit the persistent set's devices.

`v2.Set.Persistent()` accessor: `s.Safety != nil && s.Safety.Persistent`.
The flag is operator-set only — lpart-discover never emits it.

### D.11 First-install promotion (Sync -> Full)

`StorageResult.FirstInstall` (added 2026-05-21) is set by
lpart-storage-prepare when `--wipe` was applied and `--dry-run` was not.
lpart-osimage-install reads `--storage-result` (defaults to
`/tmp/install_files/runtime/storage-result.json`) and, when both
`FirstInstall=true` AND `install-context.osimage_sync=Sync`, auto-
promotes the run to Full semantics: `--config` is cleared,
`--preserve`/`--preserve-empty`/`--no-new-files` slices are zeroed,
and the user-visible log carries
`first_install detected from storage-result.json -- Sync filter
ignored, extracting as Full`.

A missing or unparseable storage-result file falls through silently
and honours the operator's explicit osimage_sync — Sync stays Sync.
The promotion is one-way (Sync -> Full only); Full and Local are
not affected by FirstInstall.

Multi-set storage-result files (`{"version":2,"sets":[...]}`) trigger
the promotion when ANY constituent set has `first_install=true`. This
matches the data-raid5 shape where the OS set is memory (always a
first install) and the data set may be persistent.

### D.10 Install-context resolution (Phase 12 / updated Phase 14c)

The Luna group/node fields `tx_install_mode` and `tx_osimage_sync`
are the single source of truth for "what kind of install is this?":

- `tx_install_mode={auto|mem|force-init}` (renamed Phase 14 from
  `{mem|init|existing}`)
  - **auto**: derive from JSON state. `tx_storage` populated → use it
    (was `existing`). `tx_storage` empty, `tx_discover_config`
    populated → run lpart-discover (was `init`). Both empty → memory-only.
  - **mem**: force memory-only regardless of any JSON content.
  - **force-init**: ignore any populated `tx_storage`, re-run
    lpart-discover from `tx_discover_config`, re-pin afterwards.
- `tx_osimage_sync={Sync|Full|Local}` (unchanged)
  - **Sync**: lpart-osimage-install extracts the image with filters.
  - **Full**: lpart-osimage-install extracts with no preserve list.
  - **Local**: skip the image; run lpart-storage-check instead.

Resolution lives in `internal/installcontext` with **three** sources and a
strict precedence (Phase 14 removed the env-var + cmdline carriers
since the Luna install.sh template writes the canonical JSON file from
the first-class fields directly):

  1. Explicit CLI flag on `lpart-install-context` (`--install-mode` /
     `--osimage-sync`) — operator-driven escape.
  2. The JSON file (default `/tmp/install_files/runtime/install-context.json`).
  3. Defaults: `install_mode=auto`, `osimage_sync=Sync`.

`lpart-install-context` round-trips the canonical JSON file. The partscript
calls it once before any other binary runs (with `--json-in` and
`--out` pointing at the same path). The case statements for which
downstream binary to invoke live in bash, keyed off `jq -r
'.install_mode'` and `jq -r '.osimage_sync'`. The "should this boot
run lpart-discover?" routing is a partscript-level decision based on
which of `tx_storage` / `tx_discover_config` is populated (see
runtime/examples/partscript.sh).

Every other Go binary reads the file via `--install-context`
(defaulted to the same path) and logs `install_mode` + `osimage_sync`
as KV pairs. The mode-aware binaries enforce their constraints:

- lpart-discover refuses unless `install_mode` is `auto` or `force-init`
  (mem mode means the partscript should never have called us).
- lpart-osimage-install refuses `osimage_sync = Local` (no image to
  extract — partscript routes Local to lpart-storage-check).
- lpart-storage-check warns when `osimage_sync != Local` (it's still
  callable by hand for diagnostics).
- lpart-storage-prepare and lpart-bootloader-finalise are mode-agnostic;
  they log the values for audit.

Each refusal is overridable with `--ignore-install-context` so an
operator running by hand outside of Luna can still drive any binary
into any state.

Exit code 2 (Phase 14c contract) means "safety refusal": a persistent
set's health failed (lpart-storage-check) or a persistent mountpoint
failed to make it into the tar excludes (lpart-osimage-install).
`cli.ExitSafetyRefusal` is the canonical helper that keeps the
stderr log line + exit code in lockstep.

---

## E. Linting baseline (`.golangci.yml`)

Enabled: govet, staticcheck, errcheck, ineffassign, unused, misspell, revive,
unconvert, unparam, nakedret, bodyclose, gosec.

Disabled with justification:
- `gosec G115` (integer overflow conversion): too noisy for size math; we
  audit manually where it matters (size_bytes → size_gib, partition counters).
- `revive unused-parameter`: noisy on interface implementations.

`//nolint` suppressions:
- Only allowed when (a) the reason is technically valid, (b) the reason is
  written next to the suppression, (c) the suppression is as narrow as
  possible.
- The executor package may need `//nolint:gosec` on `exec.CommandContext`
  calls (G204 — variable command), documented inline.

---

## F. Installer environment quirks (apply to Go code too)

From `/root/NEWTREE/README.md` + `regression.md`:

- PATH must include `/usr/sbin:/usr/bin:/sbin:/bin`.
- `root=luna` must appear in `/proc/cmdline` — install-only guard.
- `nohup`, `head`, `sha256sum`, `cmp`, `diff` missing in initramfs.
- `scp` only controller→node; no sftp-server on node.
- `/tmp` is RAM-backed; OS image (2.1 GB) is downloaded to `/tmp` then
  removed after extraction.
- `efibootmgr`, `dracut`, `grub2-install`, `grub2-mkconfig` exist only in
  `/sysroot/usr/sbin/`; call via `chroot /sysroot`.
- LUNA_TOKEN: read from `/lunatmp/install.sh` on the node — written by Luna
  at PXE boot.
- Image filename: extracted from `/lunatmp/install.sh` regex
  `(?<=/files/)[^"'\s]+\.tar\.bz2`.
- Luna HTTP base URL: `http://10.141.255.254:7051/files/`. Auth header:
  `x-access-tokens: ${LUNA_TOKEN}`.
- Test node may be parked in `sleep 10000` partscript — SSH in to test.
- **UEFI install on every GRUB family uses the distro's signed
  shim+grubx64.efi, NOT `grub2-install --target=x86_64-efi` (RHEL)
  or `grub-install --target=x86_64-efi` (Debian).** Those commands
  rebuild a custom EFI binary whose autodetected module set is too
  thin to boot (missing `normal`, `linux`, `initrd`, `configfile`,
  `search`, `blscfg`, `lvm`, `fat`, …) → GRUB rescue prompt every
  time. The canonical path is implemented in
  `internal/bootloader/uefi_signed.go::StageSignedShim` — find the
  signed `shimx64.efi` + `grubx64.efi` already unpacked under
  `<ESP>/EFI/<distro>/` by the OS-image tarball, write a stub
  `<distro>/grub.cfg` that hands off to `/boot/<grubDir>/grub.cfg`
  by `/boot` UUID, overwrite `<distro>/BOOTX64.CSV` with a
  TrinityX-labelled body so shim's fallback.efi registers the
  auto-created NVRAM entry as "TrinityX" rather than the distro
  name, and copy shim → `/EFI/BOOT/<fallbackLoader>` for the
  removable path. `GrubDirName` is `grub2` for RHEL family and
  `grub` for Debian; SUSE (when implemented) will use `grub2`.
- **Disk-boot vs PXE-boot sets a different hostname** → different
  rsyslog routing. PXE-installed nodes go through Luna's `set
  hostname` step (becomes `node001` in the booted OS, logs land in
  `<host>.messages`). A pure firmware-disk boot has no such hook,
  so the hostname stays `localhost` and the booted-OS logs never
  reach the controller's `node001.messages` channel until something
  else sets the hostname. When monitoring a disk-boot test, fall
  back to direct SSH (`hostname`, `uptime`, `efibootmgr`, `cat
  /proc/cmdline`) instead of waiting for cluster-messages.
- **shim 15.8 (RHEL/Rocky 9) ignores the BOOTX64.CSV "vendor" /
  label field** when it auto-creates a fallback NVRAM entry from
  `<ESP>/EFI/<distro>/BOOTX64.CSV` — the EFI_LOAD_OPTION Description
  is the shim binary's compiled-in vendor name, not what the CSV
  says. So writing `shimx64.efi,TrinityX,,...` into the CSV does
  NOT cause the auto-created entry to read "TrinityX"; it still
  reads "rocky" (or whatever the distro shim was built with).
  Verified by dumping `/sys/firmware/efi/efivars/Boot####-*` raw
  bytes — the Description field bytes literally spell out the
  distro name. The cosmetic mislabel is harmless: our explicit
  `Boot####* TrinityX <disk>` entries (written by
  `RegisterUEFIEntries`) are first in BootOrder and are what
  firmware actually selects on every boot; the auto-created entry
  only matters as a self-healing safety net if our own entries are
  ever cleared. `StageSignedShim` still writes the CSV with the
  TrinityX label so future shim versions that DO honour the field
  will work, but do not expect to see the relabel reflected on
  current systems.
- **mdraid metadata 1.0 survives wiping the first ~10 MB of the
  partition** because the superblock lives at the END of the device
  (so firmware can read the partition transparently as vfat/xfs).
  metadata 1.2 lives 4 KB from the start, so the same wipe destroys
  it. Pattern: 1.0 for `/boot/efi` + `/boot` (must be firmware-
  readable), 1.2 for LVM PVs. Use this fact when designing
  disk-failure tests: a `dd if=/dev/zero of=/dev/sd? bs=1M count=10`
  reliably wrecks the GPT + LVM PV but the os-boot/os-efi RAID legs
  on the surviving disk still assemble degraded.

---

## G. Decisions and rationale

| Date | Decision | Why |
|---|---|---|
| 2026-05-20 | Go module path = `gitlab.taurusgroup.one/alex/installer-go` | Matches public origin URL |
| 2026-05-20 | Group/node flag carrier = kernel cmdline (`lpart.install-mode=`, `lpart.osimage-sync=`) | Survives reboots; uses existing Luna mechanism (`luna group change -qk`); parsed by prescript |
| 2026-05-20 | Three partscript JSONs: `discover-config.json`, `storage.json`, `osimage-filter.json` | Matches user spec; one per Go binary |
| 2026-05-20 | bats suite is example only; Go gets a native test framework | Requested by user |
| 2026-05-20 | Bash tree at /root/NEWTREE remains the oracle | Avoids breaking tested production behaviour during migration |
| 2026-05-20 | Toolbox tar name: `installer-go-toolbox-amd64.tar.gz` | Different from `newtree-toolbox-node001.tar.gz` so the two paths can coexist on the file server |
| 2026-05-20 | Cross-platform support is a hard requirement: RHEL 8/9/10, Ubuntu, Debian, plus amd64 + arm64. Currently only RHEL 9 amd64 is tested. | User flag; see section L for the matrix and the per-module checklist. |
| 2026-05-20 | Executor allows `gosec` G204 (variable command) suppression with inline justification — the installer must launch sgdisk/mkfs/etc. by design, and `internal/executor` is the sanctioned chokepoint. | All other packages must run commands through Runner; G204 stays strict outside the executor. |
| 2026-05-20 | Log file permission lowered to `0o640`; log dir to `0o750` (was `0o644`/`0o755`). Permission `0o640` keeps operator-readable in the install env without tripping gosec G302. | Cosmetic; the installer runs as root in dracut, so any value works in practice. |
| 2026-05-20 | `--log-file` opens an operator-supplied path through `os.OpenFile`; gosec G304 suppressed with inline justification (binary already runs as root). | Operator chose the path. |
| 2026-05-20 | v2 unknown-key error chooses the lexically-smallest unknown key, not "first unsorted" as in the Bash validator. | Bash relied on jq's iteration order which is undefined; lexical sort gives reproducible test output. Operators rarely have multiple unknown keys at once so the user-visible deviation is minimal. |
| 2026-05-20 | The Bash "volumes[].raid is no longer supported" deprecation message is unreachable in practice — the per-volume unknown-key check fires first. Go port preserves the dead code path for parity but the observable error is "unknown field 'raid'". | Match observable behaviour; document the dead branch in validate.go and the test for future readers. |
| 2026-05-20 | v2.Parse is tolerant of `sets` being non-array: it leaves c.Sets empty and lets Validate emit "sets must be an array". | Bash jq is dynamic; Go typed decode is strict. Without this, the typed parse fails before Validate can produce the expected error string. |
| 2026-05-20 | lpart-discover error wording diverges from bash only on style (no trailing periods, lowercase first letter, %q quoting). bats does not assert on these strings. | staticcheck ST1005 hygiene; semantics preserved. |
| 2026-05-20 | discover.ListDevices accepts both lsblk JSON formats: modern (rota bool, size number) and legacy (rota "1"/"0", size "<digits>"). | Compatibility with RHEL 8 / older util-linux without forcing operators to upgrade lsblk. |
| 2026-05-20 | lpart-discover has a hidden --efivars-dir flag so tests can drive the firmware probe at a temp directory. | Cleaner than plumbing an interface through cli.Common just for one probe path; keeps the production probe logic real. |
| 2026-05-20 | byPathCache silently treats a missing /dev/disk/by-path directory as "no entries" and falls back to kernel paths everywhere. | VMs without udev populating by-path entries are normal; an error would force operators to use --names=kernel manually. |
| 2026-05-20 | selector.Resolver issues five separate `lsblk -dn -o <COL>` calls per device, matching the Bash structure 1:1. A single `lsblk -dn -b -P -o TYPE,RO,SIZE,MODEL,SERIAL` call would be more efficient but parsing key=value pairs would diverge from Bash style. Per-device latency is irrelevant at installer scale. | Keep parity simple; revisit if device counts ever go double digits. |
| 2026-05-20 | selector.Resolver takes an injectable IsBlockDevice predicate so tests can drive it against regular files in t.TempDir without root or /dev nodes. | Tests cannot create real block devices; the production default uses os.Stat + ModeDevice (and excludes ModeCharDevice). |
| 2026-05-20 | inventory.FinalState writes a "# <cmd> failed or unavailable" placeholder rather than erroring when a capture command (e.g. pvs without LVM installed) fails. | The Bash capture_cmd does the same — record what we have, never block the run on diagnostic capture. |
| 2026-05-20 | cleanup.DiskBasename and PartitionOnSelectedDisk correctly handle NVMe / MMC / md / loop partition names. The Bash equivalent (`${member%%[0-9]*}` then `${member_disk%p[0-9]*}` in `_cleanup_lvm_on_selected_disks`) is buggy for NVMe — for `/dev/nvme0n1p1` it produces `/dev/nvme` and so the md-member intersection check skips legitimate selected NVMe disks. The Go port fixes this with two regex patterns covering both SCSI/SATA (`sd[a-z]+\d+`) and NVMe/MMC/md/loop (`<name>(?:n\d+)?p\d+`). | Cross-platform support is a hard requirement; the Bash bug would corrupt cleanup on NVMe targets. The companion bash function `_cleanup_mdraid` already used the correct two-regex check, so this is purely the lvm-on-selected-disks path. |
| 2026-05-20 | cleanup.Cleaner.runBest returns nothing; failures are logged via the supplied Logger. Callers that need the success/failure outcome use runOK (returns CommandResult+ok). | Two-purpose helper would force every fire-and-forget caller to `_, _ =` discard; the split is cleaner and matches the bash _run_cleanup_cmd intent. |
| 2026-05-20 | cleanup.Options.MDAdmDetail is an injectable seam for "given an md device, return its member device paths". Defaults to running `mdadm --detail <md>` and parsing the data rows. | Lets the unit tests cover the md-intersection logic deterministically without a runtime mdadm output capture matrix. |
| 2026-05-20 | Phase 6 does NOT introduce a separate `plan.Step{ID,Check,Apply}` type — the executor's existing dry-run mode is the plan-then-apply mechanism. Each Build* function in partition/raid/lvm/fs/mount produces a typed result struct that downstream packages consume directly, and runs commands via the Runner; dry-run mode means the same code path logs without executing. | Avoids one more layer of abstraction for marginal benefit. If Phase 7+ needs a formal plan model (e.g. multi-step rollback), it can wrap these Build* calls without disturbing the modules. |
| 2026-05-20 | partition.Builder picks the sgdisk type code for the storage partition from the RAID mode: `8E00` (Linux LVM) for `none`, `FD00` (Linux RAID member) for every actual raid level. Mirrors `_partition_type_for_storage` in the Bash version. | Keeps the partition table self-describing for tools that introspect type codes after the fact (lvm-on-md cleanup, recovery tooling). |
| 2026-05-20 | raid.Builder uses metadata 1.2 for the storage array and 1.0 for the mirrored efi/boot arrays. The latter is required because GRUB / UEFI firmware reads the underlying disks directly and needs the md superblock at the END of the partition (metadata 1.0). | Long-standing mdadm/GRUB advice; preserved verbatim from the Bash tree. |
| 2026-05-20 | lvm.Builder sorts LVM volumes so fixed-size LVs are created before any 100%FREE LV. Stable sort preserves JSON order within each group. | The Bash `lvcreate -l 100%FREE` would otherwise consume all available extents before the fixed-size LVs are created, leaving no room for `-L <size>` to succeed. |
| 2026-05-20 | mount.Mounter.MountSysrootTree refuses to mount a target that is already mounted with a *different* source; same source is a no-op with a log line. | Matches lib/mounts.sh::mount_if_needed; this is a safety guard against the cross-set "two mounts at the same target" failure mode. |
| 2026-05-20 | executor.dryRunRunner forwards a name-keyed allowlist of observational commands (lsblk, findmnt, blkid, readlink, pvs, vgs, lvs, hostname, cat, stat, ls, true, false) plus `mdadm --detail/--examine/--scan` to the real exec; everything else is stubbed with a log line. | The bash predecessor invokes read-only probes via `$(...)` shell substitution, bypassing its run_cmd dry-run gate. A Go port that stubs probes would make dry-run unable to validate the selector path even on real disks. The allowlist is small and explicit; new probe commands need to be added deliberately. |
| 2026-05-20 | sysenv.RequireExpectedHost compares the SHORT hostname (first dot-separated component) so `node001.cluster` matches `node001`. Matches the `hostname -s` semantics in the bash require_expected_host helper. | Avoids surprising operators on systems where /etc/hosts sets FQDNs. |
| 2026-05-20 | Storage-result LVMInfo uses *string fields so a memory or pure-direct-partition set serialises `"lvm": {"vg_name": null, "pv_device": null}` (matching the bash jq emission) rather than empty strings. | Downstream lpart-bootloader-finalise distinguishes "no LVM at all" from "LVM with an empty name" via the null check. |
| 2026-05-20 | lpart-storage-prepare emits storage-result.json as a SINGLE object when there is one set, otherwise as the `{"version":2,"sets":[...]}` wrapper. Mirrors write_result_output. | Single-set is the production node001 shape; multi-set is used in tests and the future data-set scenarios. lpart-bootloader-finalise's extract_os_result_if_needed already handles both forms. |
| 2026-05-20 | cmd-level tests cover only the orchestrator wiring + memory-set path. The disk-touching p02/p07/etc. flows are validated by the bats-style on-node integration runs from /root/NEWTREE/regression.md — they need real /dev/sd* devices and are not in scope for unit tests on the controller. | Per-module tests already exercise every destructive code path with FakeRunner. Mocking the full orchestrator's command sequence inline would require 50+ FakeRunner matchers; the integration runs cover that for free. |
| 2026-05-20 | fscheck interprets exit codes per filesystem: ext (0/1 OK), xfs (0 OK, 4 → retry `-L`, else fail), vfat (0/1 OK), swap+tmpfs always OK. The exit code comes from CommandResult.ExitCode (FakeRunner) or exec.ExitError (realRunner), unified by an internal `exitCode(res, err)` helper. | Bash's `rc=$?` pattern would have been ugly in Go; this keeps the per-fs logic readable. |
| 2026-05-20 | The bash storage-check has a UUID-match branch keyed on `volumes[].uuid`, but the v2 schema has no such field — the branch is dead code in practice. The Go port skips it. | Avoids carrying dead schema territory; if a future v2 revision adds a UUID field, re-introduce alongside the schema change. |
| 2026-05-20 | lpart-storage-check `blkidScalar` swallows non-zero exits to `""` rather than returning an error: a device with no signature is a legitimate observable state ("missing filesystem" → mark unhealthy), not an exec failure. Mirrors bash's `|| true`. | Without this, every blkid on an unformatted device would bubble up as a script error (rc=1) instead of the expected unhealthy verdict. |
| 2026-05-20 | fs.PredictFilesystemsFromConfig uses `/dev/<vg>/<lv>` for LVM volumes (mirrors bash `/dev/${set_name}/${vol_name}`), not `/dev/mapper/<vg>-<lv>`. Both are LVM-managed symlinks pointing at the same dm device, so either works at mount time. | Matches bash output byte-for-byte; downstream readers see the same path. |
| 2026-05-20 | osimage.Pattern.Match treats every pattern as also matching its subtree (the bash `$pbase/*` case fires for any child). So `preserve=["etc/hosts"]` keeps `etc/hosts` AND `etc/hosts/*` if it existed. This is bash semantics; users wanting strict-file preservation get the same behaviour since files have no children. | Exactly mirrors path_matches_pattern in bin/lpart-osimage-install. |
| 2026-05-20 | osimage.globMatch follows bash-case semantics where `*` matches `/`. Go's filepath.Match does NOT (it's fnmatch-style), so this package ships its own matcher. Multiple consecutive `*`s collapse — `*`, `**`, `***` are equivalent in pattern context. | The osimage filter syntax uses `***` as the subtree marker; without a `*`-spans-`/` matcher the bash semantics could not be reproduced. |
| 2026-05-20 | osimage.DetectLunaToken reads LUNA_TOKEN directly from /lunatmp/install.sh (the file Luna writes during PXE boot). The bash predecessor relies on the parent shell exporting LUNA_TOKEN; Go binaries don't run inside that shell. | lpart-osimage-install must work whether or not the operator pre-sources install.sh. |
| 2026-05-20 | osimage.Download falls back from aria2c (torrent) to curl (HTTP) when aria2c is missing or the torrent attempt fails. The Aria2cAvailable seam in DownloadOptions defaults to "no" so the package's default behaviour is HTTP-only; the cmd binary plumbs an exec.LookPath probe to enable torrent when present. | Matches the bash `command -v aria2c` gate; production installers ship aria2c, but the controller may not. |
| 2026-05-20 | osimage.Extract creates preserve-empty directories at 0o755 (bash default) rather than the gosec-preferred 0o750. The `/home`, `/tmp`, `/data` directories must allow ordinary user/world traversal on the booted OS; tightening would break user logins. The gosec G301 finding is suppressed inline with a documented rationale. | Functional correctness wins over style. |
| 2026-05-20 | bootloader.RegisterUEFIEntries deletes stale TrinityX entries BEFORE creating new ones, then rewrites BootOrder with `Trinity..., residual...` so the local boot path wins on reboot. clear BootNext at start and end. Validate that TrinityX is first in BootOrder before returning. Mirrors bootloader_rhel_register_uefi_nvram_entries (same logic is duplicated in the debian helper in bash; we share one helper in Go). | One source of truth for the NVRAM dance reduces drift between families. |
| 2026-05-20 | bootloader.Adapter is the family-specific interface; cmd/lpart-bootloader-finalise dispatches by `BootContext.Family`. `FamilyNone` returns a noopAdapter so the cmd flow is uniform regardless of bootloader needs. Adding a new family (e.g. real SUSE support) is a new file + constructor entry in adapter.New. | Matches bootloader_dispatch in lib/plugin_registry.sh while staying type-safe. |
| 2026-05-20 | bootloader.WriteFstab preserves existing fstab lines and appends only entries whose mount point is not already present (whitespace-tokenised match). For tmpfs entries the operator-supplied options are honoured; the previously-buggy default of `umask=0077,shortname=winnt` on tmpfs is avoided. | Idempotency: re-running finalise mustn't duplicate or rewrite fstab content. |
| 2026-05-20 | Multi-set storage-result.json (`{"version":2,"sets":[...]}`) is unpacked in cmd/lpart-bootloader-finalise: the OS-role member becomes the working set, and the non-OS sets' filesystems[] are merged into the OS set so fstab covers /data /yourdata mounts. Mirrors extract_os_result_if_needed in bin/lpart-bootloader-finalise. | Without the merge, mkfs-ed data filesystems would never get fstab entries and would not auto-mount on boot. |
| 2026-05-20 | bootloader.New returns a stubbed `suseAdapter` that fails RegenerateInitramfs/Install/Validate with "not implemented". Prepare does enough probing to give the operator a clear "missing dracut/grub2-mkconfig" error before the fail. Bringing up real SUSE support is a future task. | Stays compatible with the bash predecessor's behaviour; the operator never sees a silent SUSE pass. |
| 2026-05-20 | install-context = one package (`internal/installcontext`), one binary (`lpart-install-context`). The empty placeholder dirs `internal/installmode/` + `internal/osimagesync/` were removed because the two values are always paired (read from the same JSON file, parsed from the same /proc/cmdline) and splitting them produced no cohesion benefit. | Avoid premature abstraction; the symmetric Mode + Sync types live next to each other and share the Load/Write helpers. |
| 2026-05-20 | install-context precedence is explicit > env > JSON file > cmdline > defaults. The JSON file outranks cmdline because by the time other binaries run, the partscript has already resolved + written the canonical file; cmdline parsing is only first-pass. Env vars outrank file so operators can do a single-binary override without rewriting the JSON. Explicit CLI flags outrank everything for tooling and tests. | Matches the user-stated "have binaries read it (or matching env vars)" while preserving cmdline as the production carrier. |
| 2026-05-20 | Each mode-aware binary refuses out-of-mode invocations rather than silently no-op'ing. lpart-discover would overwrite storage.json in install_mode=existing; lpart-osimage-install would extract on top of operator-preserved disks in osimage_sync=Local. Operator escape: `--ignore-install-context`. | Loud failure is safer than silent overwrite when the operator (or the partscript) gets the dispatch wrong. The escape hatch is named so its purpose is obvious in shell history. |
| 2026-05-20 | `osimage_sync=Local` routing handled in the partscript: when Local is selected the partscript runs lpart-storage-check (not lpart-osimage-install). lpart-storage-check serves as the "Local mode" binary; no special lpart-storage-prepare `--reuse` mode is needed. | Removes the Phase 8 open item ("lpart-storage-prepare should learn a --reuse mode"). The existing lpart-storage-check already verifies the preserved layout and mounts /sysroot. |
| 2026-05-20 | `internal/cli.EnsureInstallerPath()` called from every binary's `main()` prepends `/usr/sbin:/usr/bin:/sbin:/bin` to PATH if missing. Luna's initramfs launches install.sh with `PATH=/usr/bin:/bin` only, so the storage tooling (blkid, sgdisk, mdadm, lvm, mkfs.*) is otherwise unreachable. Mirrors `export PATH="..."` at the top of every bash binary under /root/NEWTREE/bin/. | Surfaced during Phase 13 on-node validation; the bash predecessor solved the same problem with the same prefix. |
| 2026-05-20 | `internal/log.Logger` keeps a stage-less `baseSlog` and rebuilds the active slog with `baseSlog.With(stage=...)` on each `SetStage`. Without this, consecutive `set_stage` calls stacked stage= attrs onto the prior logger and produced `stage=startup stage=installer_check stage=command_checks ...` log lines. | bash `set_stage` overwrites CURRENT_STAGE — single stage attribute is the contract. |
| 2026-05-20 | Multi-set scenarios (memory OS + storage data): the data set's per-set `_cleanup_unmount_all` used to unmount /sysroot itself, killing the OS-set tmpfs that had been placed there. Bash has the identical bug (lib/cleanup.sh:106 matches `$1 == sr` unconditionally), but bats only asserts on storage-result.json, never on live mount state. Fixed in Go via `cleanup.Set.OwnsSysroot`: the orchestrator passes `set.HasRootVolume()` (i.e. true iff this set has a volume with mountpoint `"/"`), and `findSysrootMounts` skips the literal `/sysroot` target when `!OwnsSysroot`. Targets strictly under `/sysroot/` are still returned regardless — they may be the current set's prior child mounts (re-entrancy). The orchestrator-level `unmount_sysroot` stage that runs once before any per-set processing handles stale state from prior full runs, so leaving `/sysroot` alone in the data set's per-set cleanup is safe. | Deliberate divergence from bash: tested twice consecutively on node001 against data-raid5 and the OS-set tmpfs at /sysroot survives both passes alongside the data-set xfs at /sysroot/data. |
| 2026-05-20 | `scripts/run-on-node.sh`: `IFS=' ' read -r -a _DISKS` overrides the script-global `IFS=$'\n\t'` so the space-separated `TX_REGRESSION_DISKS` actually splits across D1..D4. The original variant produced `D1="/dev/sda /dev/sdb /dev/sdc /dev/sdd"` — a single 4-device "configured path" — and the selector emitted device-not-found for the literal string. | Caught during Phase 13.2. Local fix is the smallest change; the rest of the script is fine. |
| 2026-05-20 | `scripts/run-on-node.sh` now inserts a `lpart-osimage-install` step between prepare and finalise when `boot_method != none` (or `clear_uefi_nvram = true`). grub2 and efibootmgr live only inside the extracted /sysroot, so finalise cannot run without an OS image first. Mirrors `_run_finalise` in /root/NEWTREE/tests_bash/bats/destructive/pipeline_helpers.bash. | Without this, single-disk-direct / raid1-os fail with "missing /sysroot/etc/os-release". |
| 2026-05-20 | `cmd/lpart-osimage-install`: filter resolution (FirstSymlinkResolver + ParsePatterns + BuildTarExcludes) happens at the `filter_resolve` stage, BEFORE the download. The resolved excludes are logged via `logger.KV("tar_excludes", ...)` so tests can assert on them even when the run halts early. Mirrors no bash predecessor decision — the bash version built tar excludes inline in the tar command line, with no logging. | Phase 13 integration tests needed visibility into the resolved excludes without running tar; this is the smallest refactor that gives them that. |
| 2026-05-20 | `cmd/lpart-osimage-install --skip-download` is a new test-only flag that bypasses the 60-90s torrent fetch. Implies `--skip-extract` (no tarball to expand). Sibling of the existing `--skip-extract` / `--skip-sysroot-check` test affordances. Production code paths never use it; the help text marks it `(test)`. | `tests/integration/osimage_filter_test.go` exercises 7 filter-mode sub-tests in ~7s total instead of ~7 min. |
| 2026-05-21 | First production Luna mem-install on node001 fully end-to-end: prescript downloads toolbox + writes discover-config (role=mem) + skip_unpack; partscript runs lpart-install-context -> lpart-discover -> lpart-storage-prepare -> lpart-osimage-install; postscript runs lpart-bootloader-finalise. install.sh pivoted to /sysroot and node booted Rocky 9.7 on tmpfs root. Two issues surfaced and fixed: (a) lpart-discover doesn't accept `--details` (the bash predecessor swallows it; ours exits 2) — flag dropped from the partscript invocation; (b) Luna's `node show` base64-decodes script bodies as strict ASCII, so non-ASCII characters in shell comments crash the CLI — runtime/nodes/node001-localboot/*.sh now ASCII-only. | Real on-node run beats every controller-side harness for catching wiring bugs. The ASCII gotcha is specific to Luna's `node show`; the scripts run fine if their bodies contain non-ASCII, but the show command for diagnostics doesn't. |
| 2026-05-21 | `result.StorageResult.FirstInstall` is set true when lpart-storage-prepare wiped + rebuilt the layout (i.e. `--wipe && !--dry-run`). lpart-osimage-install reads storage-result.json at `--storage-result <path>` (default `/tmp/install_files/runtime/storage-result.json`); when FirstInstall=true AND osimage_sync=Sync it auto-promotes to Full semantics (filter list zeroed, logged as `first_install detected from storage-result.json -- Sync filter ignored, extracting as Full`). A missing or unparseable storage-result file is silent and keeps Sync's filter — the operator's explicit choice wins when the signal is unavailable. | The Sync filter list is only meaningful when there is prior operator state on disk to preserve. On a `--wipe` first install or a memory-root tmpfs there is no such state, and `preserve` would actively block legitimate image content from extracting (e.g. var/spool/mail, var/log). Mem-install on node001 left /var/spool/mail absent because of this; the promotion fixes it without forcing operators to audit the filter list for first-install correctness. |
| 2026-05-21 | `CommandSpec.Quiet bool` demotes the "run_cmd failed" log entry from WARN to DEBUG when the command exits non-zero. Use for probes (e.g. `findmnt -rn -M <target>` "is this mount in use yet?") whose non-zero exit is the expected answer on the happy path. The error still propagates to the caller — only the log level shifts. Opted in: internal/bootloader.BindMountSysroot, internal/bootloader.CleanupBindMounts, internal/mount.Mounter.isMounted, internal/mount.Mounter.mountedSource, internal/cleanup.Cleaner.isMounted. Other findmnt calls whose stdout we parse for listings keep the WARN. | The cluster-messages log was emitting 8+ scary `level=WARN msg="run_cmd failed"` lines per install all on the happy path; the noise obscured real failures. |
| 2026-05-21 | `internal/log.compactHandler` produces operator-friendly `[stage] msg key=val` lines on stderr (no `time=` `level=` `msg=` prefixes; WARN/ERROR inline after the bracket). The synthetic "stage updated" Info record emitted by `Logger.SetStage` is suppressed on stderr — the next record's [stage] prefix already carries the same signal. The file sink keeps the full text/JSON for post-mortem grep; under `--json-log` stderr also goes JSON so programmatic consumers still get a structured stream. Fanout via `internal/log.fanoutHandler` so a single Logger feeds both sinks with different formats. | Tonight's reboot showed `time=2026-05-20T22:11:58.151Z level=INFO msg="stage updated" stage=write_fstab` consuming ~60 cols before any actual content; stage is kept (load-bearing for "where did it fail") but the wrapping prefixes are gone. |
| 2026-05-21 | `runtime/examples/` carries five scenario prescripts (memboot-only, memboot-uefi-clean, os-disk-sync, raid1-os-grub, memboot-plus-raid0-data) with a single shared partscript and postscript at the top of the tree. Only the prescript differs per scenario; it writes the right discover-config.json (or storage.json + install-context for non-init modes) under `/tmp/install_files/runtime/`. README walks the operator through `luna node change -qpre/-qpart/-qpost` and where the install logs land. | The "what changes between scenarios" question was confusing during the node001 setup — codifying the answer in code prevents the next operator from having to derive it. |
| 2026-05-21 | Phase 14a: `safety.persistent` is a load-bearing per-set flag (replaces doc-only `safety.preserve_devices`). Hands-off guarantee — no CLI flag overrides it. lpart-storage-prepare skips cleanup + create entirely (mounts existing volumes only); lpart-storage-check exits 2 with `safety_refusal=persistent` on health failure; lpart-osimage-install auto-excludes the set's mountpoints from the tar extract in every mode. `cleanup.Cleaner.CleanupSelectedSet` short-circuits before any probe runs — tests assert zero commands hit a persistent set's devices. | The doc-only `preserve_devices` field never produced behavioural guarantees, only operator intent recorded in comments. `persistent` ties the operator's "do not touch" intent to actual safety semantics across three binaries, with the existing cleanup md member-intersection check still protecting unrelated md-backed VGs from selected-set cleanup. |
| 2026-05-21 | Phase 14c: `install_mode` enum renamed `{mem|init|existing}` → `{auto|mem|force-init}`. `auto` derives behaviour from which JSON inputs are populated: `tx_storage` populated → use it (was `existing`); empty + `tx_discover_config` populated → run discover (was `init`); both empty → memory-only. `force-init` is the explicit "re-run discover regardless" override. `internal/installcontext` drops the env-var (`LPART_INSTALL_MODE`/`LPART_OSIMAGE_SYNC`) and `/proc/cmdline` carriers — Luna's install.sh template writes install-context.json directly from the first-class fields, so the file becomes the canonical input. Precedence is now 3-source (explicit > file > defaults) instead of 5-source. | Removes the "what overrode what" ambiguity that came from five precedence levels; the carriers we dropped were only ever a transport for the same information that lives in the Luna fields. The auto enum value lets a single config (`install_mode=auto`) cover every scenario (mem, init, existing) by routing off the JSON content the operator pinned. |
| 2026-05-21 | Phase 14c: `cli.ExitSafetyRefusal(stderr, tool, reason, fmt, args...)` is the canonical exit-code-2 helper. Prints `<tool>: safety_refusal=<reason> <msg>` to stderr and returns 2. Used by lpart-storage-check (persistent-set health failure) and lpart-osimage-install (defensive check that every persistent mountpoint reached the tar excludes). The future Go installer UI pattern-matches `safety_refusal=` to distinguish "things broke, see logs" from "do NOT continue without operator review." | Standardising on a single helper guarantees the log marker + exit code stay in lockstep; ad-hoc `fmt.Fprintf` + `return 2` had already led to one inconsistent marker placement during 14b. |
| 2026-05-21 | Phase 14d: per-scenario prescript directories (`01-memboot-only` ... `05-memboot-plus-raid0-data`) deleted. The three scripts now at the top of `runtime/examples/` are scenario-agnostic. Per-node JSON inputs (storage.json, discover-config.json, osimage-filter.json) come from Luna fields the install.sh template writes; the partscript decides whether to run lpart-discover based on whether `${RUNTIME}/storage.json` vs `${RUNTIME}/discover-config.json` is populated. Postscript POSTs the resolved storage layout back to `tx_storage_b64` via curl. | Operators now configure a node entirely through `luna node change <name> -qlpart-*` — no more "pick a scenario directory and copy its prescript." The three example scripts are universal; everything node-specific lives in Luna. |
| 2026-05-21 | Phase 14d: `runtime/node001-localboot-partscript.sh` and `runtime/nodes/node001-localboot/partscript.sh` both updated to the Phase 14 routing shape (auto/mem/force-init enum, `lpart-install-context --json-in/--out` round-trip, partscript-level "should discover run?" decision based on which JSON is populated). The node001-localboot prescript was NOT renamed: it still writes discover-config.json + osimage-filter.json inline because Luna isn't patched yet (Phase 14e/f outstanding); once the template patch lands, even those direct writes can be retired. | Keeps node001 testable on the current Luna controller while Phase 14e/f are in flight — the binaries are Phase-14-ready, the bash glue routes correctly, and the cutover to Luna-managed JSON is purely a Luna-side patch away. |
| 2026-05-21 | Phase 14b: `fsmod.PredictFilesystemsForSet(*v2.Set) Filesystems` is the per-set sibling of `PredictFilesystemsFromConfig`. The persistent fast-path in lpart-storage-prepare uses it to build a mount tree against an existing layout without re-running mkfs. The original whole-config predictor is now a thin wrapper. | lpart-storage-prepare's persistent branch needs the per-set predictor; refactoring the existing function avoided duplicating the (memory→tmpfs / partition→/dev/<disk>pN / lvm→/dev/<vg>/<lv>) mapping logic. |
| 2026-05-21 | Phase 14b: lpart-osimage-install reads storage-result.json once via `readStorageResultSummary` (returns FirstInstall bool + PersistentMountpoints []string). Single-set and multi-set shapes are both handled — for the single-set form the top-level fields are read via an embedded `setProbe` struct, for multi-set the `sets[]` array is iterated. Mountpoints from every persistent set are unioned into the tar excludes as `--exclude=./<mp>` + `--exclude=./<mp>/*`. | One read pass instead of two; the persistent-mountpoint logic ships at the same time + place as the FirstInstall promotion (and both gates depend on the same file), so a single helper kept the cmd flow linear. |
| 2026-05-21 | Phase 14e: Luna patches landed as a separate alex-namespace repo (`alex/luna2-phase14-patches`) rather than being pushed directly into `clustervision/luna2-daemon` / `luna2-cli`. Patches are unified diffs (`patch -p2`) + full applied-state copies + a SQL migration for an existing sqlite DB + an apply helper. Patch base: luna2-daemon `6e6a7de2`, luna2-cli `5e4ed24`. The same patches were applied in-place onto this controller's installed Luna (`/trinity/local/python/lib/python3.10/site-packages/daemon/` + `.../luna/`) plus the SQL migration on `/trinity/local/luna/daemon/common/luna.db`. Snapshot at `/root/luna-phase14f-backup-20260521-171554/`. | The "only write to alex repos" rule means clustervision/* is read-only for me. A separate alex-namespace patch repo is the cleanest way to land + share Luna-side changes for review; the user (or whoever owns clustervision) lands them when ready. The in-place apply on this controller exercises the changes against node001 without waiting for the upstream merge. |
| 2026-05-21 | Phase 14e wiring: the 5 Luna fields (`tx_storage`, `tx_discover_config`, `tx_osimage_filter`, `tx_install_mode`, `tx_osimage_sync`) reuse the existing `prescript`/`partscript`/`postscript` plumbing wherever possible: items[] dict for the 2 enum fields with `'auto'`/`'Sync'` defaults; b64items[] dict for the 3 b64 JSON fields with `''` default; group→node JOIN extended for the get_node detail path; `data.get(key, default)` in routes/boot.py so render still works if the DB columns somehow aren't present. CLI flags follow the `-pre`/`-qpre`/`-post`/`-qpost` pattern verbatim (`-lpart-storage`/`-qtx-storage` etc.); `--lpart-install-mode` / `--lpart-osimage-sync` use argparse `choices=[…]` because the enum is small + closed. | Minimum surface area: an upstream rebase of luna2-daemon won't touch most of the patched code paths because they extend lists / dicts rather than replacing functions. The one structural change (`write_tx_inputs` in templ_install.cfg) is well-isolated and easy to merge by hand. |
| 2026-05-21 | Phase 14f bug + fix (caught by Phase 14g): the first version of `write_tx_inputs` was called BEFORE `prescript` in the install.sh main flow. The prescript starts by running `rm -rf /tmp/install_files` to clear the toolbox download directory; that wiped the runtime/*.json files write_tx_inputs had just produced from the Luna fields. The partscript then saw an empty runtime/ and auto-routing fell through to the memory-root default. The 15:26 UTC boot logged `install_mode_source=default` (instead of `file`) and `auto mode: neither tx_storage nor tx_discover_config populated -- memory-root default`. Fix: move `write_tx_inputs` to run AFTER `prescript` (and before `bmcsetup`/`partscript`). The 15:34 UTC boot confirmed `install_mode_source=file` + correct routing. | Quintessential ordering bug that only surfaced on a real PXE boot — every per-package unit test was independently green; the install_files lifecycle ownership only becomes visible end-to-end. Phase 14g's value as a real-hardware checkpoint is exactly this kind of catch. |
| 2026-05-21 | Phase 14e operational gotcha: pushing config to Luna via `luna node change` returned "Currently not able to handle request as i am not in sync yet" because controller2 is unreachable (this controller is HA-paired but the peer is down). Workaround: `curl -sk -H "x-access-tokens: <token>" https://127.0.0.1:7050/ha/overrule/_set` (token obtained from `POST /token` with `{"username":"luna","password":"luna"}` from `/trinity/local/luna/cli/luna.ini`). The overrule flag stays set until the housekeeper resets it on insync. Documented in `daemon/routes/ha.py::ha_set_overrule`: "only really useful when all controllers are down and only one brought up. use with a lot of caution!" | Without overrule we can't push test configs to node001 at all on this single-controller setup, which would block every Phase 14g iteration. Noting it here so future operators know the exact API to flip and the caveat (don't leave it on for production). |
| 2026-05-21 | Phase 14h: PRIME.md curl is recorded in §A. The GitLab "Prime" file at `alex/gpt-project-prime` (project id 135) documents `mila`/`gitlab` command vocabulary plus hard rules: no repo deletion, writes only inside `alex/`, reads + clones from any repo are fine. Loaded once per chat session — its rules supersede ad-hoc memory entries when there's overlap. | Single source of truth for "how can I drive GitLab in this environment" lives in the GitLab project itself, not in this repo. The §A curl makes it discoverable for any future session starting from a cold read of BOOKKEEPING.md. |
| 2026-05-21 | Signed-shim UEFI install generalised across the GRUB families: extracted `internal/bootloader/uefi_signed.go::StageSignedShim` (used by both `rhel.go::installUEFI` and `debian.go::installUEFI`; `suse.go` carries a comment pointing future work at the same helper). The Debian/Ubuntu path had the identical `grub-install --target=x86_64-efi` module-stripping bug as RHEL — the new flow replaces all three per-distro `--target=x86_64-efi` invocations (`/dev/sda`, `/dev/sdb`, `--removable`) with one `StageSignedShim` call. The helper now also writes `<distro>/BOOTX64.CSV` as UCS-2 LE with `shimx64.efi,TrinityX,,...` so shim's `fbx64.efi` registers the auto-created NVRAM entry as "TrinityX" instead of "rocky" / "debian" / etc. — fixes the cosmetic leak the manual hand-fix surfaced. `Prepare` for both adapters no longer requires `grub2-install`/`grub-install` when `FirmwareMode=="uefi"` (still required for BIOS). Verified end-to-end on node001 via four scenarios: PXE-install + firmware-boot, dd-zero sda + sysrq → sdb leg boots, PXE-reinstall, dd-zero sdb + sysrq → sda leg boots — all four green with `BOOT_IMAGE=(hd0,gpt2)/vmlinuz...` cmdlines and Multi-User reached. | One helper means one place to fix the next family-wide bug. The CSV relabel costs about 20 lines of UTF-16 LE encoding plus a unit-test assertion and removes a steady source of "why does the boot menu say `rocky`?" confusion. |
| 2026-05-21 | GRUB rescue fix landed: `internal/bootloader/installUEFI` for the RHEL family no longer calls `grub2-install --target=x86_64-efi …`. New flow (in `internal/bootloader/uefi_signed.go`): locate `<ESP>/EFI/<distro>/` containing the signed `shimx64.efi` + `grubx64.efi` (from the `shim-x64` / `grub2-efi-x64` RPMs already inside the OS-image tarball), write a stub `<distro>/grub.cfg` that `search --fs-uuid`s to /boot and `configfile`s `/boot/grub2/grub.cfg`, copy the signed shim to `<ESP>/EFI/BOOT/BOOTX64.EFI` and grubx64.efi alongside it for the removable-media fallback path. `grub2-mkconfig`, dracut, fstab, and the efibootmgr registration are unchanged. `Prepare` no longer requires `grub2-install` when `FirmwareMode=="uefi"`. The signed grubx64.efi (2.5 MB, 105 modules — `normal`, `linux`, `initrd`, `configfile`, `search`*, `blscfg`, `lvm`, `fat`, `xfs`, `mdraid1x`, `diskfilter`, …) is what the distro builds + signs and what RHEL/Rocky expect at boot. Verified end-to-end on node001 (RAID1 sdb-only degraded leg from the 17:50 install): hand-applied the stub + shim copy, set `BootNext=0008`, reboot → firmware → `\EFI\BOOT\BOOTX64.EFI` (shim) → `\EFI\rocky\grubx64.efi` → `\EFI\rocky\grub.cfg` (stub) → `($dev)/grub2/grub.cfg` → kernel → `/dev/mapper/os-root`. Cosmetic side effect: shim's `fbx64.efi` re-creates a `Boot0000* rocky` NVRAM entry on first boot from `<distro>/BOOTX64.CSV`; we can override the CSV later if we want the label to read "TrinityX". | The bug-and-fix pair was diagnosed in the entry below: 5-module grub2-install output → no `normal.mod` → rescue. The fix sidesteps the rebuild entirely by re-using the distro's signed image, which is the canonical RHEL/Rocky UEFI flow (and also keeps secure-boot a valid option later). Tests in `rhel_test.go::TestRHELInstallUEFI` were updated to stage the signed binaries under a fake `<sysroot>/boot/efi/EFI/rocky/` and assert (a) the stub grub.cfg content, (b) BOOTX64.EFI = shim, (c) `grub2-install` is no longer invoked. |
| 2026-05-21 | GRUB rescue after RAID1+LVM install — diagnosis: `grub2-install --target=x86_64-efi --efi-directory=/boot/efi --bootloader-id=TrinityX --force --no-nvram <disk>` (the call our `internal/bootloader/rhel.go::installUEFI` makes) produces a 253,952-byte custom EFI binary whose `mods` section contains only 5 modules: `fshelp`, `xfs`, `part_gpt`, `diskfilter`, `mdraid1x` — the auto-detected minimum to reach `/boot`. Critically missing: `normal` (the script interpreter for grub.cfg), `linux`, `initrd`, `configfile`, `search`/`search_fs_uuid`, `blscfg`, `lvm`, `fat`, plus all display/builtin modules. Without `normal.mod` GRUB can't even parse grub.cfg → drops to rescue immediately. Confirmed by extracting each ELF from the `mods` section of `/EFI/TrinityX/grubx64.efi` on the surviving sdb mirror leg (script: `/tmp/inspect_mods.py` — splits on `\x7fELF` magic, runs `objcopy --dump-section=.modname`). For comparison the **signed** Rocky `/EFI/rocky/grubx64.efi` (from the `grub2-efi-x64` RPM, 2,566,472 bytes, shipped in the OS-image tarball) has 105 modules including all the runtime essentials. The "RHEL/Rocky way" of UEFI boot is: shim.efi (signed, in `/EFI/<distro>/`) chainloads the distro's pre-built `grubx64.efi` — distros don't expect anyone to rebuild via `grub2-install --target=x86_64-efi`. The single-disk install at 17:40 most likely also produced a 5-module binary; if it "booted" it was via firmware fallback to the `/EFI/rocky/shimx64.efi` entry (the Rocky NVRAM entry persists from prior installs on this disk), not via the TrinityX entry. | Fix options: (A) copy the signed `/EFI/<distro>/{shimx64.efi,grubx64.efi,mmx64.efi,BOOTX64.CSV}` into our `/EFI/TrinityX/` and `/EFI/BOOT/` paths and skip `grub2-install --target=x86_64-efi` entirely (canonical RHEL/Rocky path, retains secure-boot option). (B) keep the `grub2-install` flow but pass `--modules="normal linux initrd configfile search search_fs_uuid blscfg lvm fat xfs ext2 mdraid1x diskfilter part_gpt part_msdos all_video efi_gop efi_uga gfxterm font echo test true sleep halt reboot gzio xzio"` so the built binary is self-contained (~250KB → ~600KB). |
| 2026-05-21 | TUI wrapper v1 landed: `cmd/lpart-node-installer` (Bubble Tea + Bubbles + Lip Gloss) plus five new internal packages (`tui`, `engine`, `discovery`, `model`, `uiutil`). The wrapper is the parent process; existing installer (Bash or any of the six Go binaries) runs as `os/exec.Command` child. Architecture per `/root/InstallerGo/tui-design.plan`: TUI is render-only and never performs destructive work; the engine owns `Runner` (child orchestration), `RunCommand` (Go-driven commands with explicit `command.started` events), state files poller, and the typed `InstallerEvent` bus. The output classifier in `internal/uiutil/classify.go` realises the design's precedence ladder (error > destructive > warning > command > marker > success > stderr > normal) render-only — raw lines never mutated. Storage selection: tmpfs is conceptually always a candidate (seeded by `engine.NewState`), LPART_TARGET=<name> markers auto-add a row via `tui.ensureCandidate`, and state-file polls MERGE (empty `selected` from poll does not erase a marker-set selection — `update.go::EvStorageUpdated` handler preserves prior). Smoke-tested under `script(1)` PTY with `scripts/fake-installer.sh` in all three modes (tmpfs/disk/fail); exit code propagates correctly. `make check` green; `make build-linux` produces amd64 + arm64 binaries. | First Go-native UI layer; lays the groundwork for replacing Bash stages one-at-a-time with `engine.RunCommand` calls (each emits an explicit `command.started` event the TUI renders as a "$ argv..." banner) without ever touching the TUI package. Five internal packages stay small/focused: model (typed structs), engine (process orchestration + bus), discovery (TX_* markers + state files + candidate filter), uiutil (classifier + colour + truncate), tui (Bubble Tea model/update/view/styles/keys/shell-handoff). The seeded-tmpfs + ensureCandidate path means the storage panel is never empty even before discovery JSON lands. |
| 2026-05-21 | UEFI NVRAM registration bug fix: `internal/bootloader/bindmount.go` switched from `mount --bind` to `mount --rbind` + `mount --make-rslave` per dir, and `CleanupBindMounts` switched to `umount -R` (with `-l` as fallback). Plain `--bind` does NOT carry nested submounts; `/sys/firmware/efi/efivars` (its own efivarfs) was therefore invisible inside the chroot, so `efibootmgr --create` from `lpart-bootloader-finalise` silently wrote nowhere and the TrinityX entry never reached real NVRAM. Verified on node001 (VMware UEFI): with the fix, install registers `Boot0000* TrinityX sda` → `\EFI\BOOT\BOOTX64.EFI` to real efivars, and the next firmware reboot boots directly via Boot0000 → `BOOT_IMAGE=(hd0,gpt2)/vmlinuz` → /dev/mapper/os-root, no PXE round. `--make-rslave` is required so propagation is one-way (host → chroot only); without it, `umount -R /sysroot/sys` exits with `EBUSY 32` and the install env's own `/sys/firmware/efi/efivars` gets dragged down via shared propagation, hanging the node post-finalise. The bash predecessor at `/root/NEWTREE/lib/bootloader_common.sh::bind_mount_sysroot` has the same `mount --bind` bug — if anyone ever revives the bash flow they need the same patch. | Two diagnostic snapshots from before-the-fix were the smoking gun: the chroot's `efibootmgr` output (`efibootmgr-after-create.txt` and `-after-bootorder.txt`) showed `Boot0000* TrinityX sda` and `BootOrder: 0000,0002` — proving the chroot's view believed the entry existed — while the booted-OS `efivarfs` showed NO `Boot0000-*` file and only `Boot0002`. The chroot was writing to a sysfs-backed empty dir, not the host's efivarfs. Reproducer (any node, no installer needed): `mount --bind /sys /tmp/test/sys; ls /tmp/test/sys/firmware/efi/efivars/ \| wc -l` returns 0; `mount --rbind /sys /tmp/test/sys; ls ...` returns 34. |
| 2026-06-03 | compute-group node provisioning consolidated: node001's per-node installer-go pre/part/postscripts were promoted to **group-level** scripts on `compute-group` (node-agnostic) and node001's per-node overrides cleared, so all three compute nodes (node001/002/003) now share one set of scripts. Node identity is resolved at runtime from `${_HOSTNAME}` (install.sh exports `_HOSTNAME="{{ NODE_NAME }}"` at line 8, inherited by the partscript child) with a `$(hostname -s)` fallback — replaces the hardcoded `node001` in the NODE/HOSTNAME TUI markers and, critically, in `lpart-storage-prepare --expect-host`. The partscript's `mem`-mode branch now writes the tmpfs/memory-root `storage.json` (was a no-op that left storage.json absent → would have failed lpart-storage-prepare); `mem` now means "forced tmpfs regardless of any pinned tx_storage/tx_discover_config", matching §D.10. node002/003 set to `tx_install_mode=mem` → tmpfs; node001 stays `auto` and keeps its node-level `tx_discover_config` (RAID1), so it still lays down the disk layout. Per-node override is cleared by setting the script to empty: daemon `base/node.py` b64items merge rule is "node value empty AND group value present → inherit group" (`_<key>_source` flips node→group). Canonical scripts versioned at `runtime/group/compute-group/{prescript,partscript,postscript}.sh`. The group's previous naive tmpfs scripts (`mount -o mpol=interleave -t tmpfs tmpfs /sysroot` + an fstab echo) — which bypassed the installer-go pipeline entirely — are replaced. | One set of group scripts is the maintainable shape: the only per-node config left is the tx_* fields (RAID1 vs tmpfs), which is exactly the data that should differ. The `--expect-host ${_HOSTNAME}` keeps the wrong-node safety guard intact while being node-agnostic (PXE/dracut sets the kernel hostname to the node name before partscript runs — config_hostname in change_net runs *after* partscript, so the guard relies on the dracut-set hostname, which is why node001's hardcoded check worked). Fixing the mem branch removes a latent footgun for any future explicitly-mem node. |
| 2026-06-03 | lpart-* distribution = **luna2-client RPM, baked into the install initramfs** (not an HTTP toolbox download, not source-compiled at controller-install time). Decided after investigating the ecosystem: nothing in TrinityX compiles from source at install time — luna2-client is a hand-built RPM/deb pulled from `updates.clustervision.com/.../luna2/...` into the node osimage (`image_core_packages` in github-trinityX); daemon/cli/utils are pip wheels; the lone binary precedent is `libluna-fakeuname.so`, committed pre-built and listed in `%files` (no `%build`). engineering-standards `go/AGENTS.md` mandates static cross-compile (`CGO_ENABLED=0`, amd64+arm64), which `make build-linux` already does. **Implemented for redhat/x86 only** (per Alex 2026-06-03: focus redhat/intel; ARM/openSUSE/Ubuntu are wireable but untested, and the openSUSE/SUSE bootloader adapter is still a stub). Part A (packaging): `redhat/x86/create_rpm.sh` now runs `make -C ../../advanced-partitioner build-linux` and `install`s the seven `lpart-*-linux-amd64` binaries into `src/usr/bin/lpart-*` before `rpmbuild`; `luna2-client.spec` lists them in `%files` (Release 21→22 + changelog); `95luna/module-setup.sh` `dracut_install`s `/usr/bin/lpart-*` so dracut bakes them into the install initramfs at osimage pack time. Binaries are build artefacts — gitignored (`redhat|opensuse|ubuntu/*/src/usr/bin/lpart-*`), never committed (matches GIT-FLOW-005, unlike the committed `.so`). Verified: `luna2-client-2.0-22.x86_64.rpm` builds clean and `rpm -qlp` shows all seven statically-linked binaries. Part B (runtime): `runtime/group/compute-group/{prescript,partscript,postscript}.sh` rewired off the HTTP toolbox/wrapper download to a `LPART_BIN` seam (default `/usr/bin`); prescript now only re-execs under the TUI, ensures `runtime/`+`logs/` exist, and drops `skip_unpack` (no curl, LUNA_TOKEN no longer needed in prescript). The HTTP path (`scripts/upload_toolbox.sh`, `stage_to_node001.sh`) is retained as the fast dev/staging loop. | The RPM/initramfs carrier kills two footguns at once: the manual `upload_toolbox.sh` step + files-server drift, and the related §I `install_items`/`jq`-missing loop (tooling that should ship with the image was bolted on at runtime). Binaries become versioned with the osimage and flow through the exact channel luna2-client already uses, with zero new infra (no Go toolchain on controllers, no new repo). Staging into the buildroot at package time (rather than committing) keeps the repo artefact-free while the `.so` precedent shows the team accepts prebuilt binaries in this RPM. NOT YET on-node-verified: needs an osimage rebuilt with luna2-client-2.0-22 + a PXE boot to confirm dracut bakes the binaries in and the partscript runs them from `/usr/bin`. |
| 2026-06-03 | **In-process pipeline refactor + node001 isolated deploy (luna2-client-2.0-23).** The pre/part/post bash orchestration is gone: `lpart-node-installer` gained a `--phase pre\|part\|post` mode (`internal/pipeline`) that, in-process, gathers node+storage facts (native, replacing the lsblk/jq/dmidecode bash in `internal/pipeline/facts.go`), resolves the node's tx_* inputs (`internal/lpartinputs`: `curl GET <luna.url>/config/node/<luna.node>` with `/lunatmp/token.dat`, base64-decoding the b64items, falling back to the `write_tx_inputs` files), runs the install_mode routing (mem/auto/force-init, ported 1:1 from the partscript incl. the memory-root storage.json), and drives the lpart-* binaries via `engine.RunCommand` — all emitting the existing typed events the TUI already renders. The three compute-group scripts collapse to 5-line `exec lpart-node-installer --phase <p>` shims. `luna2-start.sh` (ships in the RPM) gained: `printk` lowered to console-quiet at start (kills the bmcsetup `modprobe ipmi_*` spam that scribbled over the TUI on every retry), a `dmesg` snapshot to `/tmp/luna_install.dmesg.last` on failure, and `retry_countdown 30` replacing the silent `sleep 10` between failed attempts (visible "INSTALLER FAILED -- retrying in 30s", any key -> shell). lpartinputs uses `curl` not net/http (keeps the Go HTTP stack — and its govulncheck findings — out of the binary, matching the osimage downloader). `make check` green incl. new routing/input unit tests; RPM bumped 22->23. **Deployed to node001 in isolation** (per Alex: test node001 only, not the shared group): RPM 2.0-23 installed into `/trinity/images/compute-image` (backup at `/root/node001-deploy-backup-20260603/`, enriched 922B `luna2.conf` preserved per §I), `luna osimage pack compute-image` repacked the initramfs (verified it contains the 7 lpart-*, jq, sgdisk, and the new luna2-start.sh hook), and the three `--phase` shims set as **node-level overrides on node001 only** (`luna node change node001 -qpre/-qpart/-qpost`) so compute-group + the old HTTP-toolbox files stay intact for node002/003. **Awaiting the node001 PXE boot to validate** (Alex drives the power-cycle). | The TUI already consumed `EvStage*`/`EvCommand*`/`EvNode/StorageUpdated`/`EvInstaller*`, so emitting those from a Go pipeline needed zero TUI changes — the bash TX_* marker round-trip and the lsblk/jq fact-gathering both deleted. The retry+IPMI pains turned out to live in `luna2-start.sh` (our RPM), fixable in bash with no Go. Node-level overrides keep the blast radius to node001 while the shared osimage carries the additive binaries + the universally-beneficial luna2-start.sh; promotion to the group + cleanup of the retired `installer-go-toolbox-amd64.tar.gz`/`lpart-node-installer-amd64`/`newtree-toolbox-*` files on the file server are deferred until node001 proves out. |
| 2026-06-04 | **node001 on-hardware validation + TUI polish (luna2-client 2.0-24 → 2.0-27).** The in-process `--phase` pipeline was validated end-to-end on node001 across an isolated reinstall loop (clear NVRAM `TrinityX`/`rocky` entries + `BootNext=EFI Network` before each reboot → PXE-reinstall; `install.success`, RAID1 OS on sda+sdb, `install context: source=daemon`). Fixes found + shipped: **2.0-24** — `luna2-start.sh` `retry_countdown` was unconditional at the loop bottom (where the old `sleep 10` was), so it printed a bogus "INSTALLER FAILED -- retrying in 30s" even on `install.sh exit status: success`; guarded it behind `[ "$RES" != "success" ]` (verified `INSTALLER FAILED` count = 0 on a successful install). **2.0-25** — dashboard accuracy: `pipeline.emitLuna` fills the Luna panel (controller/IP/hostname/image from `/proc/cmdline` + install-context); `pipeline.markLayout` parses `storage-result.json` (schema-agnostic `/dev/<disk>` regex + `raid_mode`) to flag every layout disk `InUse` (new `model.StorageCandidate.InUse` → Use column shows `sda☑ sdb☑`, not a single selection) and sets `model.Storage.Method` shown in the panel header; the cursor-row tmpfs detail is now context-aware (no "no partitioning" claim during a disk install); the storage panel is capped to its content so the bottom panels + footer fill the screen. **2.0-26** — `Method` firmware comes from the detected `/sys/firmware/efi` (UEFI/BIOS), not the config's `boot_firmware:"auto"`. **2.0-27** — kernel capture: new `engine/kmsg.go` tails `/dev/kmsg` (seek-to-end) and emits warn-and-above records as `EvKernel`, rendered into Run output as `[kernel] …` in warn/error colour so ipmi/driver errors are caught instead of scrolling the console (complements the `luna2-start.sh` printk-quiet); **`engine.FanIn` was broken for >1 aux channel** (only watched `aux[0]`, silently dropping the kmsg channel) — rewritten with `reflect.Select`. Footer honesty: F1 Pause now really pauses (a `pipeline.gate()` between stages, toggled via an injected `pauseToggle`), F10 Reboot really reboots (2-press confirm → injected `reboot` func → `syscall.Reboot`), the no-op F5 Discovery is removed, and a spacer line sits above the keybindings. New unit tests cover `parseKmsg`, the FanIn multi-aux merge, `parseLayout`, the pause/reboot handlers, and the kernel render. **File-server disk hygiene**: repeated `luna osimage pack` runs each leave a ~2.65 GB `compute-image-*.tar.bz2`; the disk hit 100% (a failed repack surfaced it). Freed it by deleting superseded packs and **restarting `aria2c.service`** to release deleted-but-open seeder handles (`lsof | grep deleted` was the tell) — a recurring gotcha worth remembering. node002/003 group scripts + their toolbox files left intact throughout. | These bugs only surface on real hardware or under the right event mix: the retry banner needs an actual `install.sh` success through `luna2-start.sh`; the FanIn drop needed a 2nd aux channel (kmsg) to exist; the disk-full needed several repacks. The pause/reboot funcs are injected (not `syscall` in the TUI) so the handlers stay unit-testable. The aria2c deleted-handle trap means "deleted a big file but `df` didn't move" → restart the seeder. |
| 2026-06-04 | **Regression scenario matrix driven on node001 via Luna config (the new `--phase` flow).** Six scenarios, each pushed via `luna node change -qlpart-discover-config/--lpart-install-mode/--lpart-osimage-sync`, EFI-cleared + PXE-rebooted, watched to a fresh `install.success`: **§3.1 mem-only** (auto+empty → tmpfs) ✅, **§3.2 single-disk** (`sets:[{os}]` → `/dev/mapper/os-root` LVM, no new md) ✅, **§3.3 RAID1** (`os raid1×2` → 3 raid1 arrays + os-root) ✅, **§3.9 force-init** (re-discovers → RAID1) ✅, **§3.10 mem-override** (`mem`+discover present → tmpfs) ✅. **§3.5 OS+data initially FAILED** — diagnosed via live SSH capture during the hang: `lpart-discover: invalid raid level "0" ... (valid: 1, 5, 6, 10)` → exit 1 → `luna2-start.sh` retry-loops forever on the bad config. Root cause: **RAID0 is rejected by design** (`intent/spec.go:78` allows only 1/5/6/10 — no redundancy); the **regression.md §3.5 example was itself wrong** (used `raid:0`). Re-ran as §3.7 (OS RAID1 + data RAID1) → ✅ `os-root` (sda+sdb) + `/dev/mapper/data-data` at `/data` (sdc+sdd, `md124`). Fixed the §3.5 runbook example to `raid:1` + a note. So **every distinct install path validated on the new pipeline**: memory-root, single-disk-LVM, RAID1-OS, multi-set OS+data, force-init + mem routing. Side findings: (a) the §3.9 install logged `INSTALLER FAILED`=1 — the retry countdown fired once and *recovered* from bad disk state left by the failed §3.5, a real-world positive test of the failure path; (b) cleanup probes `vgs`(exit 5)/`findmnt`(exit 1) on empty disks log at WARN (noisy but harmless — candidates for `CommandSpec.Quiet`); (c) stale md arrays auto-assemble (inactive/spare) on disks a given scenario doesn't wipe (e.g. single-disk leaves sdb/sdc/sdd arrays) — expected, since the installer only touches its target disks; a per-scenario harness should `mdadm --stop`+wipe all approved disks first. | Driving the matrix surfaced a real runbook bug (RAID0 example) and proved the retry-loop is *visible* now (countdown) rather than a silent hang. The hang "looked like" a multi-set/stale-array bug but the live capture (last part-log line = the lpart-discover validation error) cut straight to the config cause — capturing the failing process beats guessing. |
| 2026-06-04 | **GRUB-rescue on firmware disk-boot — osimage BLS build-path bug, fixed in finalise (2.0-28).** First true firmware disk-boot (not a forced PXE) dropped to GRUB rescue: the osimage's newest BLS entry (`…-5.14.0-687.12.1.el9_8.x86_64.conf`, the kernel GRUB defaults to) had `linux`/`initrd` pointing at the **build path** `/trinity/images/compute-image/boot/vmlinuz-…` + a build-time `root=/dev/mapper/vg_rhel-lv_root`, because the el9_8 kernel was added to the image (2026-05-31) while chrooted at its build path. The 611(el9_7) entry was correct → proved it. Fixed by generalising `internal/bootloader.normalizeBLSEntries` (was separate-`/boot`-only, clean-`/boot/`-prefix-only) to strip any build-path prefix (keep the segment from the last `/boot/`) and strip build-time `root=/rd.lvm.lv=/resume=` so the entry inherits grub2-mkconfig's probed kernelopts. +3 unit tests. **Validated on node001**: firmware boots Boot0008(TrinityX sdb) → GRUB → the el9_8 kernel from `(hd0,gpt2)/vmlinuz`, `root=os-root`. | The whole 2.0-2x "RAID1 ✅" history missed this because every regression cycle cleared NVRAM + forced PXE, so the GRUB→BLS disk-boot path was never exercised. `install.success` ≠ "the node boots from disk" — see [[regression-firmware-disk-boot-gap]] in auto-memory. |
| 2026-06-04 | **§3.4 RAID1 dd failover: sda-leg OK, sdb-leg degraded boot lands at `grub>` (open).** §3.4a (wipe sda → boot sdb leg) ✅ degraded `[2/1][_U]`. §3.4b (wipe sdb → boot sda leg) → GRUB command line. Diagnosed: **not** an asymmetric-ESP bug (sda1≡sdb1 mirror, both `grubx64.efi` md5-identical); the grub2-mkconfig `grub.cfg` resolves `/boot`+root by **`mduuid/…`**, needing GRUB to assemble the degraded md array, and that succeeded for the sdb survivor but not the sda survivor. Candidate fix (untried): post-process `/boot/grub2/grub.cfg` to resolve by plain fs-uuid (like the working `\EFI\rocky` stub) so a single metadata-1.0 member is found without array assembly — needs live degraded re-test. **PARKED** pending decision. | Real HA-robustness gap ("either leg boots alone"), but pinning the exact mechanism needs repeated strand→network-boot cycles. Not a quick deterministic fix like the BLS one. |
| 2026-06-04 | **TUI rework (2.0-29 → 2.0-32) + plain mode.** Per operator: Discovery panel beside the disk table (CPU model+topology, mem, GPU, NIC, disk-type counts from /proc+/sys, no lscpu/lspci dep); dropped the storage Use column (a non-empty Set means used) and the installer's memory-staging row (tmpfs shown only when OS targets RAM); disk **ID** = serial→WWN→shortened by-path→model. Layout reorg: Node panel folded into Luna; Discovery top-right; Progress+Storage middle; **Run output full-width bottom**; "Partition·Image·Boot" header line removed; disk-row selection cursor/highlight removed (still scrolls); **F1 Pause removed** (ambiguous boundary); F2 Shell clarified ("install continues in background") + redraw-on-return. **All VGA-unrenderable Unicode → ASCII** (☑☐✓✗○ … → — × → footer arrows all rendered as a solid ■ on the node's CP437 console). **Plain mode**: kernel cmdline `lpart.notui` (or `--no-tui`/`$LPART_NOTUI`) streams normal scrolling boot output instead of the TUI; exit code 0/1 same as TUI. | The ■ blocks + ambiguous Pause + dead space were only visible on the real VGA install console, not a dev terminal (which renders Unicode fine). Plain mode gives a TUI-free fallback for odd consoles/serial without changing the install logic. |
| 2026-06-04 | **Cleanup/flatten refactor (2.0-33), behaviour-neutral** — see `decisions/0002`. `lpart-node-installer` is `--phase`-only: deleted the dormant child-process "wrap an external installer" branch + the whole `TX_*` marker/state-file subsystem (`internal/discovery`, `engine.Runner`, the state poller). Retired **lpart-install-context** (7→6 lpart-* binaries; its install-context.json round-trip was redundant — `lpartinputs.Resolve` writes it, every tool reads+validates via `installcontext.LoadFile`). The 3 Luna hook scripts collapse to one-liners calling a shared `/usr/bin/lpart-phase` (shipped by 95luna). Removed the retired HTTP-toolbox scripts + Makefile `toolbox` target; quarantined the dev hot-swap to `scripts/dev/`. Cut 10 dead funcs. **Renamed the module path** off `alex/installer-go` → `clustervision/luna2-client/advanced-partitioner` (retires Next-action 3). `make check` green; PXE RAID1 install on 2.0-33 ✅. | The "old logic" was the viz-wrapping path: the TUI used to run the bash installer as a child and scrape `TX_*` markers; the in-process `--phase` pipeline emits typed events directly, so the marker/Runner/poller machinery was dead-on-live-path. One conceptual path now: Luna hook → lpart-phase → `--phase` pipeline → lpart-*. |
| 2026-06-04 | **§3.4b degraded-leg boot is intermittent (not a GRUB bug); grub.cfg fs-uuid robustness (2.0-34); §3.8 RAID5/6 ✅.** Re-ran §3.4b: the sda leg booted degraded twice, once landed at the UEFI **boot-picker** (operator correction — not `grub>`), and the first run was `grub>`. So it is **flaky VMware-EFI behaviour** after dd+sysrq+NVRAM-delete, not a deterministic GRUB failure. Kept a robustness fix anyway: `rhel.makeGrubCfgDegradedSafe` post-processes grub2-mkconfig output to drop `set root/boot='mduuid/…'` + `--hint='mduuid/…'` so /boot resolves by plain fs-uuid (a metadata-1.0 member is firmware-readable without md assembly) — unproven for §3.4b since that failure was pre-GRUB, but sound. **§3.8 RAID5 + RAID6 ✅** on the 4-disk node as mem OS (tmpfs) + data raid5/raid6 `[4/4][UUUU]` across sda-sdd, LVM xfs at /data. Config gotcha: a mem OS needs `grub:false` (`provider=memory requires method=none`; `grub:true` makes lpart-discover self-validation fail). | The boot-picker vs grub> distinction matters: chasing a grub.cfg fix for a pre-GRUB firmware-menu stall is wasted effort. Capture the actual stuck state before fixing. |
| 2026-06-04 | **TUI 2.0-35: Up/Dn scrolls the Run-output log; post-phase panels populated.** Up/Dn (and PgUp/PgDn) now drive an `OutputScroll` offset on the Run-output panel (the install log, which actually overflows — title shows `[scrolled +N - Dn to follow]`) instead of the disk table (which fits); removed the now-unused storage cursor/ScrollTop. `runPost` now calls emitNode/emitStorage/emitLuna + markLayout so the bootloader-finalise dashboard shows real facts instead of empty `-`/`(resolving…)`. | The disk list almost always fits; the log is the thing you want to scroll back through. The post phase is a separate process from part, so its panels start empty unless re-emitted. |
| 2026-06-04 | **One-file-per-host cluster-messages logging — FIXED on the live controller + validated.** Root cause: the install env logs syslog as `<host>.cluster` (Luna set-hostname) while the booted OS logs as `<host>`, and the controller rsyslog templates keyed the per-node filename on the **full** hostname → two files (`<host>.cluster.messages` + `<host>.messages`). Fix: domain-strip the hostname in BOTH templates — `/etc/rsyslog.d/template-rule-controllers.conf` (`%HOSTNAME%` on :514) and `/etc/rsyslog.d/udp-netconsole.conf` (`%fromhost%` on :6666) — via the rsyslog property regex `R,ERE,1,DFLT:([^.]+)` (chars up to the first dot). `rsyslogd -N1` clean; tested with a crafted `node001.cluster` packet → `node001.messages`; **confirmed live** on the §3.8 installs (29 install-env lines in `node001.messages`, 0 in `.cluster.messages`). Backups at `/root/{template-rule-controllers,udp-netconsole}.conf.bak-20260604`. **Upstream to the TrinityX rsyslog ansible role.** Addresses the "1 file per host" half of `TASK-installer-observability.md`; the live-forward + kernel-un-suppression half remains (Next-action 6). | A controller-side hostname normalisation is more robust than chasing the installer's hostname: one file per host regardless of how a sender names itself. |

---

## H. Test strategy

- **Go unit tests** under each `internal/<pkg>/` and `cmd/<binary>/`
  with table-driven cases and per-package golden fixtures under
  `internal/<pkg>/testdata/`. Run with `make test` (or `make check`
  for the full fmt+vet+test+lint+vuln sweep).
- **Fake executor** in `internal/executor` lets unit tests assert the
  full command sequence without running any subprocesses.
- **End-to-end regression on real hardware** is operator-driven via
  `regression.md` (follow that file to
  get the runbook). The automated controller-side integration harness
  at `tests/integration/` was removed 2026-05-23 in favour of this
  flow.
- **Luna token gotcha for any on-node run**: `/lunatmp/install.sh`'s
  LUNA_TOKEN is a JWT written at PXE boot with a fixed `exp` ~1 hour
  later. Scenarios that actually download the OS image start failing
  with HTTP 401 once the token expires; reboot the node to refresh.

---

## I. Open items / TODO

- [ ] **⚠ STRONG — DO NOT FORGET: ship the installer-go initramfs `install_items`
      from the luna2-client RPM (not a per-image manual patch).** The installer-go
      partscript needs `jq`, `wc`, and the storage tooling (sgdisk, partprobe,
      wipefs, blkid, the LVM suite, dmsetup, findmnt, mkfs.xfs/ext4, awk, mount,
      …) **present in the node's install initramfs**. Today those live ONLY in
      `compute-image`'s **hand-enriched** `/etc/dracut.conf.d/luna2.conf` (922 B,
      May 2026). The `luna2-client-2.0-19` RPM still ships the **stale 363 B
      `luna2.conf`** (Aug 2024) with none of them. So **any freshly-built osimage
      gets the stale conf** → its install initramfs lacks `jq` → the partscript
      dies at the unguarded `jq -r '.install_mode'` (~line 165) with
      `jq: command not found` → **exit 127 → install loop**, on a node that
      otherwise booted fine. Discovered 2026-06-03: a clean image
      (`controller-rocky-x86-lchroot`, built via the new lchroot) looped exactly
      this way; worked around by copying `compute-image`'s `luna2.conf` into the
      image and re-packing. **Proper fix: the enriched `install_items` must be
      shipped by the luna2-client RPM (or a TrinityX image-build drop-in) so every
      clean image has them — the manual per-image patch is a footgun.** Secondary:
      guard the partscript's `jq` read of install-context (fallback / clear error)
      so a missing `jq` fails loudly instead of a bare 127. NOTE: this fix is
      **deferred to the new-installer upstreaming** — the current lchroot test
      environment intentionally still assumes the old installer method; fix
      upstream once the new installer is ready (per Alex, 2026-06-03).
- [x] Phase 15 — toolbox-in-ramdisk (redhat/x86, 2026-06-03, §G). Realised
      cleaner than the original "bundle a tarball" framing: the `lpart-*` binaries
      ship in the luna2-client RPM `%files` and are pulled into the install
      initramfs by `95luna/module-setup.sh` `dracut_install` at osimage pack
      time; the prescript no longer downloads anything from the Luna files
      server. Still TODO: on-node verify + fan out to the other 5 distro/arch
      trees (untested targets, deferred). This also subsumes the §I
      `install_items` footgun above for the lpart-* tooling specifically — they
      are now declared by the RPM, not a per-image manual patch.
- [ ] Phase 16 — deployment migration. Replace the controller-side
      `scripts/upload_toolbox.sh` step with an Ansible role (or similar)
      driving install onto fresh controllers.
- [ ] Multi-arch toolbox at deploy time: `make toolbox TOOLBOX_ARCH=arm64`
      already builds the arm64 bundle, but the Luna prescript hard-codes
      `installer-go-toolbox-amd64.tar.gz`. When ARM nodes ship, route by
      `uname -m`.
- [ ] Retire the Bash oracle at `/root/NEWTREE/` once the Go binaries
      have driven enough regression cycles (per `regression.md`) without
      surfacing parity bugs.
- [ ] Luna template patch automation — wrap the Phase 14e/f patch
      series in an idempotent apply script so a Luna upgrade can be
      re-patched in one command (today: manual `patch -p2` per file
      from `alex/luna2-phase14-patches`).
- [ ] Group-wide config JSON via Luna HTTP — alternative carrier to
      the first-class Luna fields; deferred until the per-node fields
      prove limiting.
- [ ] **Pin-back: emit stable device paths (ADR-0007 rough edge).** The
      paste-ready `selection=manual` block from `cmd/lpart-storage-prepare/
      pinback.go` currently lists **kernel names** (`/dev/sda`), which are
      not stable across reboots/disk swaps. Carry `/dev/disk/by-path` (or
      by-id) on `selector.Resolved` (`discover` already has a by-path
      resolver cache + `ByPathMissing` — thread the actual link through)
      and emit that, so a pasted block is durable. Fine for regression now;
      harden before relying on a pasted layout in production.
- [ ] **Pin-back: optional real "observed/resolved layout" node field.**
      ADR-0007 chose copy/paste over a daemon round-trip to avoid Luna's
      node-shadows-group inheritance. A future, cleaner option is a
      read-only daemon field that records the resolved layout for
      visibility **without** participating in inheritance. Left open; only
      build if the copy/paste flow proves limiting.

---

## L. Cross-platform / architecture support matrix

The Bash predecessor at `/root/NEWTREE/` runs in production on **RHEL 9 /
amd64** only (node001 = controller1.cluster's compute). The migration
must hold up across the full target matrix; everything except RHEL 9 /
amd64 is currently **untested**:

| Platform | Status | Notes |
|---|---|---|
| RHEL 9 / amd64 | Tested (oracle) | The Bash tree is run here daily |
| RHEL 8 / amd64 | Untested | No hardware in this environment |
| RHEL 10 / amd64 | Untested | No hardware in this environment |
| Rocky / Alma (RHEL family) | Untested | Should track RHEL 9 |
| Ubuntu / amd64 | Untested | `bootloader_debian.sh` exists but never exercised here |
| Debian / amd64 | Untested | Tracks Ubuntu adapter |
| SUSE | Stub | `bootloader_suse.sh` is "not implemented" |
| Any / arm64 | Untested end-to-end | Code paths are arch-aware: `StageSignedShim`/`archShimAndGrub` write `BOOTAA64.EFI`+`grubaa64.efi`+`BOOTAA64.CSV`; `ArchToUEFITarget` returns `arm64-efi`. `TestStageSignedShimARM64` locks the file-naming behaviour. Still need a real ARM target to verify the signed shim binaries ship in the OS-image tarball at `/EFI/<distro>/shimaa64.efi`. |

### Cross-platform checklist for every module that touches the OS

When porting `lpart-bootloader-finalise` and any other distro-aware tool,
actively scrutinise the following:

- `dracut` vs `update-initramfs` (initramfs builder)
- `grub2-install` (RHEL) vs `grub-install` (Debian)
- `grub2-mkconfig` (RHEL) vs `update-grub` (Debian)
- `efibootmgr` location and behaviour inside `/sysroot`
- BLS entry layout: RHEL uses `/boot/loader/entries`; Debian historically does not
- UEFI fallback loader name per arch: `BOOTX64.EFI` (amd64) vs `BOOTAA64.EFI` (arm64); the override CSV name follows the same pattern (`BOOTX64.CSV` / `BOOTAA64.CSV`). Hardcoding `BOOTX64.CSV` was the only ARM-blocking bug found post-cutover — now derived from `fallbackLoaderBaseName(arch)`.
- `grub2-install --target=` value: `x86_64-efi` vs `arm64-efi` vs `i386-pc` for BIOS
- BIOS GRUB partition handling (EF02 partition, MBR vs GPT)
- `mkfs.fat` vs `mkfs.vfat` availability
- `--omit luna` flag is dracut-specific; Debian flow uses a different mechanism
- Package manager assumptions if/when we add one (`dnf`, `apt`, `zypper`)
- SELinux relabelling (RHEL only)
- Netplan vs sysconfig vs NetworkManager (already mostly Luna's problem,
  but we may need to honour it when laying out `/etc/`).

Rule of thumb: do not assume RHEL semantics. When unsure, file the
question in section J and move on; surface it again when the relevant
test bed exists.

---

## K. How to resume work in a fresh session

1. `cd /root/InstallerGo && git pull origin main`.
2. Read this file top to bottom. §A (live constants), §D (live
   behavioural contracts), §F (footguns) and §G (decision log) are
   the load-bearing context. §C is just a pointer to git log.
3. Read `README.md` for the directory layout, `go.md` for the
   engineering guide, and `regression.md` if you intend to drive
   end-to-end testing on real hardware.
4. The Bash oracle lives at `/root/NEWTREE/` — useful when porting a
   new helper or sanity-checking behaviour; treat it as
   reference-only, not as code that runs.
5. **All phases 1–14 are complete.** `make check` green; `make
   build-linux` produces amd64 + arm64 binaries; `make toolbox`
   builds the tarball Luna fetches at PXE time. Phase 14 Luna-side
   patches live in `alex/luna2-phase14-patches` (also applied
   in-place on this controller's `/trinity/local/python/...` tree).
6. To stage freshly built binaries onto a parked node without a
   PXE cycle: `bash scripts/stage_to_node001.sh node001`. To rebuild
   and publish the toolbox tarball: `bash scripts/upload_toolbox.sh`.
7. For end-to-end regression: load `regression.md` into a fresh
   session and follow its runbook (it expects an operator in the
   loop for power cycles + IPMI). No `make test-node` target any
   more.
8. When a real-disk run diverges from the bash oracle: file the
   divergence as a decision-log entry in §G, then fix or document.

---

## Status (2026-06-06) — storage & provisioning model redesigned to v1.4 (design only)

Reworked the node storage and provisioning model end to end as a written design, on branch
`advanced-partitioner`. Not implemented yet; the code still has the old `lpart-discover` and `intent`
grammar this replaces. Documents (in `docs/`):

- `DESIGN-NOTES.md` — start here. The settled vocabulary, the settled rules, and the open items.
- `storage-provisioning-model.md` (+ `.pdf`, `build-pdf.py`, `diagrams/`) — the spec, v1.4.
- `IMPLEMENTATION_PLAN.md` — the phased build order across luna2-client and luna2-daemon.

Settled this round, so do not reopen:
- One `install_mode` (Luna attribute): `auto` (default), `sync`, `full`, `local`, `memboot`,
  `sanitize`, `legacy`. It replaces the old `tx_install_mode` x `tx_osimage_sync` pair.
- `disklayout` is the whole storage config (the Luna attribute). A set's RAID topology is the
  `raid` field (`none|0|1|5|6|10`). Do not reuse `disklayout` for the per-set field.
- A set: `name`, `role` (os|data), `selection` (auto|manual), `raid`, `count`, `spares`, `match`
  (tags + min/max size + model), `devices`, flat `persistent`, `volumes`.
- Auto disk tags: `rotary`, `ssd` (any solid state, includes NVMe), `nvme` (subset of ssd).
- UEFI only (x86_64 and aarch64 via SBBR), no BIOS, no firmware field.
- Data fence: data sets wiped only by `full`; `sync` never wipes (halts on broken); `auto`
  repairs the OS set only; only `sanitize` erases a `persistent` set.
- Doc style: plain language, no em dashes. Two image placeholders (cover, installer screenshot)
  are left for the PDF.

Open items (roadmap, spec section 8): on-node reconcile/drift agent; declarative/GitOps API;
measured boot + attestation; operator-assigned custom disk tags.

Pushed to `alex/advanced-partitioner` (the build box SSH key authenticates as omar).

---

## Design decisions and discussion log (2026-06-06)

Rationale behind the v1.4 model. The spec (`docs/storage-provisioning-model.md`) is written
final-state only on purpose, so the reasoning and the alternatives we rejected live here.
Format: the decision, why, and what we turned down. Two deep research passes informed this
(HPC provisioners and MAAS storage; AI-factory and Kubernetes provisioning); the conclusions are
folded into the spec.

### Reframe: delete the discovery grammar
Decision: remove the `internal/intent` discovery grammar and the `lpart-discover` CLI; fold disk
discovery into one declarative config resolved on the node. Why: two input paths (a discover
config and a partitioned config) for one outcome was needless surface. Rejected: keeping a
separate discover step.

### The install_mode axis (the central decision)
- It started as a small set {reinstall, local, sync} and became one field with seven values:
  `auto`, `sync`, `full`, `local`, `memboot`, `sanitize`, `legacy`.
- `grub` vs `sync` are not two knobs. We first split into "full install no/yes/auto" plus
  "sync/local", then saw those overlap and collapse into one ordered enum. The nonsense
  combinations (local + wipe, memboot + wipe) do not exist as values.
- auto vs sync was the key argument. A correct `sync` must create the layout on a blank disk, but
  it must NOT wipe an existing-but-broken or renumbered disk, because someone who picks `sync` is
  asking to keep their data. So `sync` is fail-safe (creates on blank, halts on broken, never
  wipes) and `auto` is `sync` plus auto-repair (recreates the broken part). They differ only in
  the broken case. Rejected: a `sync` that self-heals by wiping, which is exactly Bright's footgun.
- `full` is not a separate content mode, it is `sync` with an empty preserve list. Bright
  equivalence: Bright's `rsync --delete` plus exclude list and our `tar` plus cruft-removal plus
  preserve list reach the same disk state; the preserve/exclude list is what protects data in
  both, and rsync's skip-identical is only a transfer optimisation. So "Full" did not earn a value.
- memboot fallback rule: `auto` falls back to `memboot` only when NO disk layout is declared. If a
  layout is declared but cannot be met (no disks, too few, no match), `auto` HALTS. A declared
  layout asserts the node is diskful, so missing disks are a fault to surface, not a reason to
  silently run from RAM.

### sanitize
Decision: `sanitize` securely erases all local disks for tenant turnover, then stops. It is the
only mode that erases even a `persistent` set. Why: multi-tenant AI clouds need a deliberate wipe
between tenants; the normal data fence protects against accidental wipes, so the deliberate one
needs its own explicit mode.

### legacy
Decision: `legacy` runs the original bash pre/part/post and a full image unpack with the preserve
filter omitted; the new model is bypassed. Why: migrate a cluster node by node; an un-migrated
node (old scripts, no storage config) is simply `legacy`.

### The data fence and the tmpfs + DATA hole
- Invariants: a data set is wiped only by explicit `full`; `sync` never wipes (halts on broken);
  `auto` repairs the OS set only, data sets halt; no mode wipes on a mismatch except the OS set
  under `auto` or `full` (closes the disk-renumber footgun); a `persistent` set is wiped by
  nothing except `sanitize`.
- Hole we found and fixed: stateless OS plus persistent local DATA. `memboot` had been
  over-constrained to "no storage config". Fixed so `memboot` may carry DATA sets (no OS set); a
  `persistent` DATA set is mounted, never touched, its mounts written to the RAM fstab from the
  set's volumes, and excluded from the image extract. This is the xCAT statelite pattern done
  declaratively.

### Naming (a lot of iteration; do not reopen)
- per-set RAID topology: profile, then layout, then disklayout, finally `raid`
  (`none,0,1,5,6,10`, none means a single disk). `disklayout` is reserved for the WHOLE storage
  config (the Luna attribute). The clash, disklayout meaning both a set property and the Luna
  label, was the bug that forced the split.
- "profile" rejected as too vague; "roles" rejected because `role` already means os/data and
  TrinityX Ansible `roles` is a separate layer; "layout" rejected as forgettable and it collides
  with the existing Go `partition.Layout`.
- disk selection: `select`/`given` became `selection` auto|manual. "given" was clumsy; auto vs
  manual needs no glossary.
- `safety.persistent` became a flat `persistent`; the size guard was dropped because `match`
  handles size for auto and manual selection is deliberate, so the `safety` wrapper held only one
  flag and went away.

### Disk selection by tags and size (MAAS-influenced)
- `selection: auto` picks disks with a `match` filter: tags (all listed must match), min and max
  size, model. Auto tags from the kernel: `rotary`, `ssd` (any solid state, includes NVMe),
  `nvme` (a subset of ssd, so an NVMe carries both). Grounded in MAAS (`storage=label:size(tags)`,
  a minimum size); we add a maximum.
- Custom operator tags are a roadmap item: the assign-and-deliver mechanism is not designed.

### Boot: UEFI only
- Decision: UEFI only on x86_64 and aarch64 (ARM is UEFI by SBBR, nothing special). No BIOS, no
  CSM, and therefore no firmware or boot field at all. The field went firmware, then boot, then
  removed. Why: we do not want legacy BIOS, and with no BIOS there is no UEFI/BIOS choice to make.
- Logical consequence caught during review: every disk-based OS set must have an EFI system
  partition (`/boot/efi`, vfat). The examples carry it.

### stateless/stateful and sync semantics
- stateless vs stateful is a derived label, not an input. The real property is persistence across
  reboots, which follows from the wipe and preserve policy, not the media. The middle ground (a
  disk OS refreshed while data persists) has no agreed name; xCAT coined statelite, Bright's
  installmode values blur it. We read the label off `install_mode` and never take it as input.
- `sync` means converge to the golden image, delete cruft, protect the preserve list. Transport
  agnostic (tar now, rsync-delta possible later); the definition is the disk end state.

### Group to node
Both inputs (`install_mode`, the `disklayout` config) live at the group and override per node,
reusing the daemon's existing Node, OSImage, Group, Cluster inheritance in `node.py`
(`items`/`b64items` plus the join aliases). install_mode is node-wins; the disklayout config is
replaced wholesale, never field-blended.

### AI factory and Kubernetes positioning, and the honest gap analysis
- Researched NVIDIA AI factory (DGX SuperPOD, Base Command Manager, Mission Control),
  Kubernetes/Metal3/Talos/Cluster API, MAAS and Warewulf. Finding: the model already matches the
  core cloud-native posture (declarative desired state, self-heal/reconcile, converge to a golden
  image, tag/label disk selection, group overrides, stateless plus persistent, cattle not pets).
  That is rare for HPC tooling and is the honest selling point.
- Honest gaps, on the roadmap, do not overclaim: (1) a continuous on-node reconcile and drift
  agent (today reconcile is boot-time only, like Warewulf wwclient or Talos); (2) a declarative
  API and GitOps surface (a custom resource reconciled by Argo or Flux); (3) measured boot and
  attestation on the existing TPM token; (4) operator-assigned custom disk tags.
- The Bright comparison drove much of this: Bright validates the auto idea (its installmode AUTO
  is the default) but has no genuinely safe refresh (AUTO and NOSYNC both wipe on a mismatch, and
  its manual says NOSYNC is not a way to protect data). Our `sync` is that missing safe refresh,
  and `persistent` is a guarantee rather than Bright's confirm-before-wipe prompt.

### Doc and process decisions
- The spec targets sales, engineers and devs: golden first pages that sell, the comparison moved
  up after the vocabulary, plain language, no em dashes, minimal generated-text tics. The
  reasoning and the journey were deliberately kept out of the spec and live here.
- Cover art and an installer screenshot are left as placeholders in the PDF for the operator to
  drop in.
- Repo discoverability: the AGENTS.md map and this JOURNAL both point at `docs/DESIGN-NOTES.md`
  as the starting point.

---

## Status (2026-06-06) — v1.4 client IMPLEMENTED (server patches staged)

The v1.4 model is now built on branch `advanced-partitioner` (was design-only earlier today).
ADRs `decisions/0003` (adopt v1.4 + schema break + retire the §D.1 NEWTREE config-grammar parity),
`0004` (single 7-value `install_mode`; `legacy` is daemon-only), `0005` (old→new mapping).

Client (Go), `make check` green throughout, landed in five commits:
- **Phase 0–2** — new zero-dep `internal/raidlevel` (RAID member-count rules) + `internal/diskresolve`
  (tag/size `match` + `Resolve`, salvaged tolerance/size parser); `discover.Device` gains `Tags`
  (rotary/ssd/nvme) + `Model`. **Breaking v2 schema**: `Config{version,sets}`, flat
  `Set{selection,raid,count,spares,match,persistent,devices,volumes}`; dropped the `install`/`provider`/
  `safety` blocks + the operator `provider=memory` path; new UEFI ESP rule. Accessors map
  `raid`→`raidN` and present UEFI-only boot constants so partition/raid/lvm/fs build untouched.
  Deleted `cmd/lpart-discover` + `internal/intent` + `internal/safety`.
- **Phase 3a/6** — collapse `install_mode`×`osimage_sync` onto the single `install_mode` enum across
  `installcontext`, `lpartinputs` (with the ADR-0005 `MapOldMode` shim), `pipeline` (thin mode-keyed
  sequencer), and the lpart-* binaries. install-context.json is now `{"install_mode"}`.
- **Phase 3b/4** — `lpart-storage-prepare` decision engine (`decide.go`): dispatch on mode
  (sanitize/memboot/local/full/sync/auto), `selection=auto` via `diskresolve` (pool consumed per set),
  and the **data fence** (persistent→mount-only; full→create; sync→create-on-blank/refresh-on-healthy/
  HALT-on-broken; auto→same but recreate a broken OS set only). New `internal/storagehealth`
  (tri-state healthy|blank|broken, shared with `lpart-storage-check`) and `internal/sanitize` (layered
  secure erase, dry-runnable). memboot synthesizes the tmpfs RAM root in-code.
- **Phase 5** — removed the lpart-discover TUI/stage remnants.
- **Phase 9** — redhat/x86 packaging (`create_rpm.sh` `LPART_BINS`, `luna2-client.spec` `%files`,
  `95luna/module-setup.sh` `dracut_install`) de-references the removed `lpart-discover`; the 5 lpart-*
  binaries cross-build clean.

Server (Python) — **patch series staged** at `/root/luna2-v1.4-patches` (8 patches, apply clean
against luna2-daemon `dc89e4cc` + luna2-cli `e331f45`): `install_mode`/`disklayout`/`osimage_filter`
on node+group, the cascade, boot render vars, `templ_install` `write_provisioning_inputs`, CLI flags,
SQL migration + apply script. Refactored from `alex/luna2-phase14-patches`; old `tx_*` columns kept
read-only for the client shim. NOT yet applied to a live daemon or merged.

### On-node validation (2026-06-06): memboot ✅
Migrated the **live controller** to v1.4 (in place, not via the clean-base patch series since the
installed daemon carried phase-14): removed the five `tx_*` fields from `database_layout.py` + the
`node.py`/`group.py` cascade + `boot.py` + `routes/boot.py` + `templ_install.cfg`
(`write_tx_inputs`→`write_provisioning_inputs`) + the CLI (`arguments.py`/`constant.py`), and
**rebuilt the `node`+`group` tables** in `luna.db` to physically drop the `tx_*` columns and add
`install_mode`/`disklayout`/`osimage_filter` (SQLite 3.34.1 has no DROP COLUMN; 3 nodes + 5 groups
preserved). Backups at `/root/luna-v1.4-daemon-backup-20260606/`. The compat shim was dropped from
the client too (ADR-0006). Built **luna2-client 2.0-36** (5 lpart-* binaries, no lpart-discover), installed
into `compute-image`, packed; verified the install initramfs carries the 5 v1.4 binaries + `lpart-phase`.
Set `node001.install_mode=memboot` (via the new `luna node change --install-mode` flag) and PXE-rebooted
(BootOrder was `EFI Network` only — no NVRAM clear needed). Result: discovered→partscript→**success in
124s**, node booted, **`findmnt /` = tmpfs** (RAM root) → the v1.4 decision engine's memboot path ran
end-to-end. Template markers `download_image/unpack_imagefile handled by lpart-osimage-install (no-op)`
present; `install.sh exit status: success`. The per-stage lpart-node-installer log isn't forwarded to
cluster-messages (install-env RAM /tmp not carried past pivot — the §6 observability live-forward,
still open).

### Observability (2026-06-06): live rsyslog install narrative ✅
The installer content now streams LIVE to the controller rsyslog (`engine.ForwardSyslog`,
RFC3164 UDP to `luna.loghost:514`, node-FQDN stamped, tagged `lpart-install[phase/stage]`, chained after
`TeeToFile`; TUI frames never forwarded). Kernel capture widened to info+ (read from `/dev/kmsg`, so
printk stays suppressed for a clean TUI yet kernel still forwards). The stream is now a self-describing
narrative: **pre** opens with a banner + `ReadKmsgBacklog` **dmesg snapshot (≤800 records: boot +
dracut + kernel cmdline)**; **part** emits a full banner (node, cpu/mem/gpu/nic, **disks seen** with
tags, controller, install_mode + a one-line plan) then live per-stage progress; **post** a banner.
Validated on node001 memboot: 875 forwarded lines (was 59), banner + 800-line dmesg + full
storage-prepare/osimage progress, success 174s. The working forward surfaced a **LUNA_TOKEN leak**
(curl `-H x-access-tokens: <JWT>` logged verbatim) → `executor.redactArgs` masks it (confirmed
`x-access-tokens: REDACTED`, 0 leaks). Shipped in luna2-client 2.0-39. Note: node001's VMware disks
report `rotary` (not ssd) — use `tags:["rotary"]` for auto-select disk tests here. Post-`install.success`
lines are the booted RAM OS's own journald, not a second installer dump.

### On-node validation (2026-06-06): disk RAID1 + deliberate-error/retry ✅
- **auto RAID1 OS on disk** (set `disklayout` = one `os` set, `selection=auto`, `raid:1`, `count:2`,
  `match.tags:["rotary"]`; `install_mode=auto`). Cleared the TrinityX disk-boot NVRAM entry over SSH
  (`efibootmgr -b <n> -B`) so the node PXE-cycled. Full create path ran: md125/126/127 RAID1 mirrors,
  `os` VG + `os-root` LV, fstab by-UUID, ESP on both legs, `efibootmgr` BootOrder `0001,0000,0002`.
  Bootloader-finalise chroot-ran dracut+grub2-mkconfig; **success in 187s**; node booted off the array.
  The whole stage narrative forwarded to cluster-messages, tokens REDACTED.
- **Deliberate-error + retry** (regression.md §3.18): set `match.tags:["nvme"]` on a box whose disks are
  all `rotary` → `lpart-storage-prepare: set "os": disk match unsatisfied: need 2 device(s) tags=nvme, but
  only 0 eligible ... Available pool: /dev/sda(25G,rotary) ...` → stage **FAILED**, `install.sh exit
  status: failure` — all logged to cluster-messages. The pre-existing Luna retry loop re-attempts after
  its delay; recovery is by fixing the layout (or memboot). Confirms: an install error is both
  **operator-visible in rsyslog** and **non-bricking** (retry, not a wedged node).
- **stderr forwarding** clarified (user asked): `engine.ForwardSyslog` forwards the installer's stderr
  too, not just stdout. `severityFor` maps stderr/`failed` records to syslog **err (3)**, kernel to its
  own level, everything else to **info (6)** — so a failure shows up as an `err`-severity line in
  cluster-messages, not buried in stdout. Verified in the deliberate-error run (the FAILED + match-
  unsatisfied lines arrived).

### Resolved-layout pin-back: decided NOT to POST; emit paste-ready manual instead — ADR-0007 ✅
Question raised: when `selection=auto` resolves a concrete layout (e.g. RAID1 over sda+sdb), should it be
POSTed back to the daemon (node? group?) for visibility + promotion to predefined-next-boot? **Decision
(ADR-0007): no daemon round-trip.** Luna's inheritance shadows group config the instant a field is set at
node level, so auto-writing `disklayout` per node would silently fork every node off its group template —
operator-hostile, and the antithesis of why `selection=auto` exists. Instead the client now **emits the
resolved layout as a complete, paste-ready `selection=manual` disklayout** into the part-phase log /
cluster-messages, fenced by greppable markers:
```
----8<---- BEGIN <host> disklayout (selection=manual, paste to pin) ----8<----
{ "version":2, "sets":[ { ... "selection":"manual", "devices":[...], "volumes":[...] } ] }
----8<---- END <host> disklayout ----8<----
```
Structural fields (role/raid/persistent/volumes) copied verbatim from the authored config; devices are
what the resolver selected; `selection`→`manual`; auto-only fields (count/spares/match) dropped; the
block validates standalone. To pin a node the operator copies it into the node's (or group's) disklayout
— discovered→predefined, no shadow surprise. Code: `cmd/lpart-storage-prepare/pinback.go`
(`buildPinnableConfig`+`emitPinnableLayout`), wired in `main` after the resolved/result JSON write, with
unit tests; `make check` green. Diskless modes (memboot/sanitize) emit nothing.
- **Known rough edge (deferred, see §I + ADR-0007):** devices are emitted as current **kernel names**
  (`/dev/sda`), which are not stable across reboots/disk swaps. Durable pinning wants
  `/dev/disk/by-path` or `by-id`; `selector.Resolved` doesn't carry that field yet. Good enough to
  unblock regression of the auto→manual promotion flow; harden before relying on a pasted block in prod.

---

## ▶ HANDOFF (2026-06-06 EOD) — for a higher model: refactor + validate against the design

**Where things stand.** v1.4 client is fully implemented and `make check`-green; server migrated in
place on the live controller; **memboot, auto-RAID1-disk, and deliberate-error/retry validated on
node001 with full rsyslog narrative**. Nothing has been pushed/merged (all local commits; the build box
SSH key authenticates as **omar**, not alex — verify remote + identity before any push, clustervision is
read-only). The pin-back design question is resolved (ADR-0007) and built.

**The ask for next session:** take a higher model, **re-read the whole thing against the design**
(`docs/DESIGN-NOTES.md`, `docs/storage-provisioning-model.md`, `docs/IMPLEMENTATION_PLAN.md`, ADRs
0003–0007), refactor where the implementation drifted, and finish regression. Concrete next steps in
priority order:

1. **Finish disk regression on node001** (decision engine + data fence). Remaining scenarios in
   `regression.md`: §3.6 sync-healthy preserve/delete (rsync-converge), §3.7 sync-broken **HALT** (must
   NOT wipe), §3.8 full wipe, §3.10/3.11 persistent data set survives auto/sync/full, and §3.13
   **sanitize LAST** (it secure-erases all local disks → bricks the node with no OS to pivot, and **IPMI
   power control is DEAD on node001**, exit -9, so there is no remote recovery — run it only when you're
   done with the node). For any sync/full/auto disk test the node is currently booted off the RAID1
   array, so **clear the TrinityX disk-boot NVRAM entry over SSH (`efibootmgr -b <n> -B`) before reboot**
   to force PXE. node001 approved-destructive disks: **sda sdb sdc sdd only**.
2. **Verify the auto→manual pin flow end-to-end**: after an auto RAID1 install, grep the BEGIN/END block
   out of cluster-messages, paste it into `node001.disklayout` with `install_mode` left at auto (or a
   pinned manual), reboot, confirm it reproduces the same array. This exercises ADR-0007.
3. **Harden the pin emit (ADR-0007 rough edge):** carry `/dev/disk/by-path` (or by-id) on
   `selector.Resolved` / `discover.Device` and emit that instead of kernel names, so a pasted block is
   reboot/disk-swap stable. (`discover` already has a by-path resolver cache + `ByPathMissing`; thread
   the actual link through.)
4. **Design-conformance refactor pass:** check the implementation against the spec for drift —
   especially (a) the data-fence matrix in `cmd/lpart-storage-prepare/decide.go::decideAction` vs spec §5,
   (b) tri-state health `internal/storagehealth` vs the healthy|blank|broken definitions, (c) the ESP /
   UEFI-only validation rules, (d) `internal/sanitize` erase layering. The decision matrix has
   table-driven unit tests (`decide_test.go`) — extend them if the spec says something the tests don't.
5. **Push/MR when the user asks:** 6 client commits on branch `advanced-partitioner` → reviewed MR off
   `main`. Daemon/CLI: the clean-base patch series at `/root/luna2-v1.4-patches` (8 patches) is for
   upstream MRs (luna2-daemon/luna2-cli are clustervision/read-only → alex-namespace patch series, same
   as the phase-14 precedent). The live controller was migrated **in place** (backups at
   `/root/luna-v1.4-daemon-backup-20260606/`), separate from the upstream series.
6. **Cross-platform gate (§L):** only redhat/x86 is on-node-tested; openSUSE/Ubuntu `create_rpm.sh`/
   `create_deb` + `.spec` still need a build check.

**Credential hygiene (still open):** the `alex/luna2-phase14-patches` clone has an **OAuth token embedded
in its git remote URL** — rotate it and switch the remote to SSH.

**Gotchas to carry forward:** node001's VMware disks report **`rotary`** (not ssd) — use
`tags:["rotary"]` for auto-select tests here. printk stays suppressed for a clean TUI but kernel still
forwards (read from `/dev/kmsg`, independent of console printk) — deliberate, don't "fix" it. The
LUNA_TOKEN-in-curl leak is masked by `executor.redactArgs`; keep new executor call-sites going through it.

---

## 2026-06-07 — ADR-0008: `save` flag pins a discovered layout (selection rename + write-back)

Design discussion with Alex turned the ADR-0007 "paste-ready block only" into an opt-in
write-back. Decisions captured in **decisions/0008-save-flag-pins-discovered-layout.md**:

- **Rename `selection: auto` → `selection: discover`** everywhere. Kills the overload with
  `install_mode: auto` (one word, two axes). `install_mode: auto` is unchanged. Mechanical sweep
  through `internal/config/v2` (types/validate/accessors), `cmd/lpart-storage-prepare/decide.go`
  (`resolveAuto`→`resolveDiscover`), comments, tests, and the two `os-auto-*` testdata fixtures
  (renamed `os-discover-*`). The SVG diagrams already used "discover" for the selection path and
  "auto" only for the mode, so no diagram change was needed.
- **New per-set `save` bool (discover only).** `save:true` → after resolving, POST the concrete
  layout back to the node-level `disklayout` as `selection: manual` so the next boot reproduces it.
  Validator rejects `save` on a manual set. Revert = clear the node `disklayout` (group `discover`
  resumes). New `origin` provenance object (`{from,at,by,host}`), machine-written, round-trips; it
  is stamped on discovered sets only.
- **No new daemon route.** Key finding from the live daemon: `common/validate_auth.py::token_required`
  accepts *any* JWT signed with the daemon SECRET_KEY, including the node install token; `disklayout`
  is a TEXT node column updatable via the existing `POST /config/node/<node>` body
  `{"config":{"node":{"<node>":{"disklayout":"<json-string>"}}}}` (the path `luna node change
  --disklayout` already uses). So the client write-back reuses it with the install token. ADR-0008
  task "daemon route" therefore collapsed to a verification.
- **Write-back is non-fatal + re-entrant.** Failure logs at err and continues (data is already on
  disk; the 0007 paste block remains the manual fallback). A pinned node is `manual`, and manual
  never writes back, so no loop. Group-level `save:true` self-pins every node in the group (noted
  in the ADR as a deliberate consequence of the 5.4 inheritance rule).

### Code (all on branch `advanced-partitioner`, not pushed)
- `internal/config/v2`: `Set.Save`, `Set.Origin`+`Origin` type, `allowedSet`/`allowedOrigin`,
  validate (`discover|manual`, save-only-on-discover, origin unknown-key check).
- `internal/lunaclient/` (new): the single HTTP-mutation chokepoint. `PinParams` (Endpoint/
  BodyBytes/Validate), `BuildRequest` (token in header only — never URL/body, so redaction is
  structural), `NewClient` (InsecureSkipVerify mirrors the node's curl `$INSECURE`, narrow gosec
  nolint), `PinDisklayout`.
- `internal/sysenv`: `KernelParam` (getargs-style /proc/cmdline reader) for luna.url/luna.node.
- `cmd/lpart-storage-prepare`: `pinback.go` builder now stamps origin + `anySetSaves`; new
  `writeback.go::maybeWriteBack` (gated, dry-run aware, non-fatal); wired in `main.go` with a new
  `--luna-install-script` flag (token source).
- Tests: lunaclient (httptest happy/401, body shape, token-not-in-URL), sysenv KernelParam,
  v2 save/origin validate + round-trip, writeback (happy/no-save-noop/manual-reentrant/dry-run/
  missing-cmdline). pinback test updated.

### Docs
- `docs/storage-provisioning-model.md`: auto→discover throughout; §3 vocab adds `save`/`origin`;
  new **§5.5 Pinning a discovered layout**; worked example 6 (discover→pin); worked examples
  renumbered to §5.6. No em dashes (style rule). **PDF regenerated** with build-pdf.py +
  WeasyPrint 69.0 under **`python3.11`** at `/usr/local` (the default `python3` is 3.10 without it;
  the daemon python must not be touched). Note: build-pdf.py only writes the HTML; the PDF is a
  separate `weasyprint <html> <pdf>` step.
- `docs/DESIGN-NOTES.md`, `docs/IMPLEMENTATION_PLAN.md`: vocabulary + 2026-06-07 update note.

### Gate
`make check` is **green under go1.25.11** (gofmt/vet/test/golangci-lint 0 issues/govulncheck 0
vulns). The box's pinned **go1.25.9 fails govulncheck only**, on 4 *stdlib* CVEs (net/http,
crypto/x509, net) newly published in 2026 and fixed in go1.25.10/.11 — one of them
(crypto/x509 via the untouched `log.go`) is independent of this change, so it is toolchain drift,
not new code. `GOTOOLCHAIN=go1.25.11` fetches cleanly. **Action for the build box: bump Go to
≥1.25.11** (or set GOTOOLCHAIN=auto + a `toolchain` directive — not added here because the box has
GOTOOLCHAIN=local, which would ignore it, and the bump is an infra decision).

### ▶ NEXT
- On-node validation of the save write-back on node001: a discover+save os install, confirm the
  node `disklayout` flips to manual+origin in luna.db, reboot, confirm it reproduces; then
  `--disklayout ''` and confirm it discovers fresh. Folds into the open disk-regression pass.
- Nothing pushed; build box SSH key authenticates as **omar** — verify identity + remote before
  any push, and clustervision stays human-initiated (push only to alex/*).

---

## 2026-06-07 (overnight, autonomous) — ADR-0008 test hardening + refactor; destructive E2E deferred

Ran standalone per Alex's instruction. Order followed: revise/expand tests → run
gates → deep refactor → re-run gates → on-node (safely).

### Tests (all axes, unit + regression)
- v2: save-on-discover ok / save-on-manual rejected / save default / origin
  round-trip / origin unknown-key (kept from impl session).
- pinback: **multi-set** test — a discover+save os set plus an operator manual
  data set both render `manual`, but `origin` is stamped **only** on the
  discovered set. `anySetSaves` unit (nil/discover±save/manual+save).
- writeback: happy / no-save-noop / manual-reentrant / dry-run-sends-nothing /
  missing-cmdline / **missing-token** / **daemon non-2xx surfaces (non-fatal)**.
- decide_test: selection literals + names auto→discover.
- regression.md: auto→discover sweep, fixture renames, resolveAuto→resolveDiscover,
  **new §3.19** "discover + save → node disklayout pinned, then revert" (cluster-
  messages expectations, re-entrancy, revert, non-fatal check, safety notes).

### Refactor (no redundant pieces)
- writeback.go: parse cmdline first; `origin.Host` now uses the authoritative
  `luna.node` (was the display host). Dropped the redundant `writeBackParams.host`
  field (struct + main.go + test).
- ADR-0008: added the breaking-rename **migration** consequence (stored
  `selection:"auto"` is rejected by the new client; migrate with the client
  deploy; no leniency alias by design).
- Confirmed ADR-0008 ↔ code ↔ spec §5.5 ↔ DESIGN-NOTES ↔ IMPLEMENTATION_PLAN all
  align. ADRs 0003/0007 and prior JOURNAL entries left intact (append-only history;
  they predate the rename, which 0008 documents). No stray `selection:auto` left in
  code/testdata. golangci-lint 0 issues (no dead code).

### Gates
- `make check` green under go1.25.11 twice (after tests, after refactor).
- `make build` + cross-compile of all 5 lpart-* binaries for linux/amd64 **and**
  linux/arm64: clean.

### On-node (deliberately non-destructive)
- node001 is **up and healthy**, undisturbed: `findmnt / = xfs on /dev/mapper/os-root`
  (its RAID1+LVM install). node001/002/003 all status "success".
- cluster-messages: `node001.cluster.messages` (the install channel) is **empty** —
  no pending or failed install. `node001.messages` is the running OS's journald.
- save write-back auth proven without touching the live daemon: `token_required`
  (validate_auth.py) does only `jwt.decode(SECRET_KEY)` with **no role/scope check**,
  so a node token is accepted on `POST /config/node/<node>`; the write path is the
  same one the working `luna node change --disklayout` uses; httptest units cover the
  client. No token minting / live POST done (production-touching, negligible marginal
  value over the code proof).

### ▶ DECISION: destructive PXE E2E (§3.19) deferred to a supervised run
node001 has **no recovery path if a reinstall bricks it** (IPMI dead, SSH-only, no
power cycle — a stuck GRUB is permanent until physical access). A full §3.19 run also
requires building client 2.0-40 and repacking the shared `compute-image`. Unrecoverable
downside + production image change + user asleep, against marginal validation value
(the install path is unchanged by this work; the write-back is additive and non-fatal,
and is already unit-proven). So I did **not** run it. §3.19 is written and ready to run
supervised (build 2.0-40, migrate node001/group disklayout auto→discover+save, clear
NVRAM, PXE, watch cluster-messages for `save: …pinned` + zero err lines, verify the
node-level pin, test re-entrancy + revert). sanitize (§3.13) remains do-not-run.

---

## 2026-06-07 (overnight, cont.) — lunaclient: net/http → curl (consistency + binary + gate)

Building the linux binary for the on-node test surfaced a regression my earlier
refactor missed: the net/http-based `lunaclient` had **doubled** `lpart-storage-prepare`
(3.1MB → 6.8MB) and re-introduced the Go HTTP/TLS stack into a shipped initramfs
binary — exactly what the project deliberately avoids (`internal/lpartinputs` talks to
the daemon via **curl**, with a comment saying so, to keep net/http and its CVEs out).

Reworked `internal/lunaclient` to POST via **curl through os/exec**, mirroring lpartinputs
in reverse (GET→POST): `--insecure` (controller self-signed, like luna2-start.sh's
`$INSECURE`), `-H x-access-tokens: <token>`, body on stdin (`--data-binary @-`),
`-o /dev/null -w %{http_code}` to check status. `curlArgs` is a pure, unit-tested
function; a guarded httptest+real-curl integration test covers the success/401 paths.
`writeback.go` now takes an injectable `pin func(...)` seam (tests record calls without
network/curl) instead of an `*http.Client`.

Results:
- `lpart-storage-prepare` back to **3.17MB**; `go list -deps` confirms **net/http no longer
  imported**.
- **`make check` is now green on the box's own go1.25.9** — removing net/http removed the
  reachability of the stdlib net/http/crypto/x509 CVEs, so the earlier "bump Go to 1.25.11"
  caveat is **moot**. (Still green under 1.25.11 too.)
- Honors the "one carrier for daemon calls" consistency + the central-exec spirit.

DESIGN-NOTES updated. Supersedes the prior entries' "single HTTP-mutation chokepoint
(net/http)" and "go1.25.9 fails govulncheck" notes.

---

## 2026-06-07 (overnight, cont.) — §3.19 save E2E validated on node001 (+ a real bug caught)

Ran the destructive save test on node001 (authorised; node002/003 as fallback, never
needed). Built+deployed client **2.0-40** (discover/save, curl write-back) into
`compute-image` and `luna osimage pack`ed; verified the install initramfs carried the
3.17MB curl `lpart-storage-prepare`. node001 set to `selection:discover raid:1
match.tags:[rotary] save:true`, install_mode auto; NVRAM disk entries cleared +
BootNext=network to force PXE.

**Cycle 1 — feature works, bug found.** cluster-messages showed the new code:
`set "os" selection=discover -> [/dev/sda /dev/sdb]`, data fence `verdict=broken ->
create`, RAID1 build, then `save: pinning resolved layout to node "node001" at
https://.../config/node/node001` and `save: node "node001" disklayout pinned`. Token
**REDACTED**. `install.sh exit status: success`; node booted off the new array. The
install token was accepted on `POST /config/node/<node>` exactly as ADR-0008 predicted
(no new daemon route). **Bug:** the daemon does NOT store a raw-JSON disklayout
faithfully — it came back **quote-stripped (invalid JSON)**, which would break the next
boot's re-read. Unit/httptest could not catch this (they never hit the real daemon's
storage). This is the payoff of an on-metal run.

**Fix (63347ee):** base64-encode the disklayout in the POST body, exactly as the luna
CLI's `-q` does; the node already base64-decodes (`lpartinputs.writeJSONField`). base64 has
no quotes for the daemon to strip, so it round-trips.

**Cycle 2 — fix confirmed.** Rebuilt+redeployed+repacked, reset node001 to
discover+save, forced PXE again. Result: `disklayout pinned`, and node001's stored
disklayout is now **valid base64 JSON**: `selection:manual, devices:[/dev/sda,/dev/sdb],
save dropped, origin:{from:discover, at:2026-06-07T00:17:40Z, by:lpart-storage-prepare dev,
host:node001}`. 2 success markers, **0 real failures**. node001 booted off the pinned
RAID1 (md125/126/127 active on sda+sdb, root LV in os VG). node001 left **correctly
pinned** (the happy end state).

**Validated:** discover resolution, data-fence create, the save write-back POST (token
auth + body shape + base64 round-trip), origin stamp, token redaction, install success,
disk boot. **Re-entrancy** not re-tested on metal (a 3rd reboot for marginal value):
node001's stored config is now `manual` with no `save`, so `anySetSaves` is false and a
re-PXE cannot re-pin — guaranteed by the stored config + `TestWriteBackManualIsReentrant`.
**Revert** not executed on node001 (clearing its disklayout would, with the empty group
layout, drop it to memboot); the mechanism is vanilla Luna inheritance (clear node attr
-> group), documented in spec §5.5.

Errors check (both cycles): only benign kernel PCI-bridge noise (VMware) and expected
create-over-existing WARNs (old `os` VG broken -> create). No err-severity failures.

**Notes / minor follow-ups:**
- `origin.by` shows `lpart-storage-prepare dev` — `cli.Version()` is unstamped in
  `make build-linux`; add `-X .../cli.version=2.0-40` ldflags for a real version string.
- node001 `sdc/sdd` carry stale md metadata (`md124` inactive) from a prior layout;
  harmless, discover correctly used sda+sdb.
- Image/file-server hygiene: two `compute-image-*.tar.bz2` packs now in
  `/trinity/local/luna/files/` (disk at ~75%); prune superseded packs if it tightens.

---

## 2026-06-07 (overnight, cont.) — partitioner re-entrancy: battery + 6 fixes -> 14/14

Built a memboot re-entrancy battery (scripts/dev/reentrancy-battery.sh + 7 layout
fixtures: single-a, single-b, raid1, raid0, raid10, os(raid1)+data(raid0),
os(raid1)+2data) that drives lpart-storage-prepare across the matrix forward+reverse
(every transition both ways), each layout full(create) then auto(re-run), asserting
the on-disk fingerprint is unchanged on the auto re-run (idempotent: mount, no
reformat). Harness = node001 in **memboot** (tmpfs root, disks free, full toolset).
Each round it surfaced a real bug; fixed in order:

1. **RAID1 health probe** (storagehealth): checked ESP/boot at the raw partition
   (/dev/sda1 = linux_raid_member) instead of the md device -> classified a fresh
   raid1 broken -> auto reformatted. Fix: resolve md device for boot-mirroring sets
   (Set.BootMirrorDevice/MDName/MirrorsBoot + volumeDevice).
2. **RAID1 mount path** (fs.predict): same raw-partition bug in the mount-existing
   path -> healthy verdict then mount /dev/sda2 (EBUSY/exit32). Fix: same resolver.
3. **Stale INACTIVE array** (cleanup): mdadm --detail can't enumerate inactive
   arrays, so a stale array held sdc busy -> partition timeout. Fix: parse members
   from the /proc/mdstat line (parseMDStatArrays) + whole-disk zero.
4. **udev re-assembly race, round 1** (cleanup): retry loop, drop mid-loop settle,
   wipefs members.
5. **mdadm --create on dirty members** (raid): defensive zero+wipefs members +
   "yes" stdin.
6. **ROOT FIX — udev auto-assembly** (cleanup.EnsureNoAutoAssemble): the race was
   irreducible with site fixes (symptom moved partprobe->create->member-busy every
   round) because udev's incremental assembler re-creates a stopped array from
   surviving superblocks faster than we can zero them. lpart-storage-prepare now writes
   "AUTO -all" to /etc/mdadm.conf + /etc/mdadm/mdadm.conf at startup, disabling udev
   auto-assembly for the install. Safe: only governs FUTURE incremental assembly, so
   arrays assembled at boot (healthy/persistent mount-existing) are untouched;
   mdadm --create assembles explicitly.

**Result: RE-ENTRANCY BATTERY PASS — 14/14 idempotent, 0 failures** (out7), every
layout + transition; phase-pre x2 idempotent. All fixes unit-tested; make check
green on go1.25.9. Commits 44ebf45..af36c82.

Also (per review) added the **installer-env guard** to lpart-osimage-install and
lpart-node-installer (root=luna unless --allow-non-installer), so every destructive
lpart-* refuses to run on a live system (lpart-storage-prepare/-check/-bootloader-finalise
already had it).

Fixed binaries deployed into compute-image + re-packed so the image carries them.

### Note on the battery's harshness vs production
28 destructive layout changes in ONE boot with no reboot is far more adversarial
than production (one install per PXE/memboot boot, and luna2-start.sh retries a
failed install). The deterministic re-entrancy bugs (1,2,3) were real and are fixed;
the udev race (4,5,6) only manifested under the rapid cycling, now removed at root.

---

### 2026-06-07 — Legacy install campaign + F10 fix + install-template rename

Investigated/validated the legacy pre/part/post configs (`install_mode=legacy`).
Full write-up + journal is in **`LEGACY.configs`** (the campaign's running log);
static guard `scripts/dev/legacy-lint.sh`; hardware driver
`scripts/dev/legacy-battery.sh`; runbook `regression.md §3.21-3.23`.

- **F10 (Class A, BLOCKER) — found + fixed.** `install_mode=legacy` was dead on
  the lpart client: the daemon served the lpart-pipeline install.sh for legacy too,
  whose partscript runs `lpart-node-installer --phase part`, which refuses legacy →
  the install aborted before any legacy `customscript` ran. HW-confirmed on
  node001 (diskfull-part.sh staged but never executed).
- **Fix = install-template rename (daemon).** The ORIGINAL name
  **`templ_install.cfg`** is now reserved for the **legacy/original** install.sh
  (restored real bash `unpack_imagefile`+`download_image`; pre/part/post run
  `customscript`, skipping the node FIELD when it is the lpart thin shim via
  `grep -q lpart-node-installer`; no lpart pipeline, no TUI). The v1.4 lpart pipeline
  template is **renamed to `templ_install_lpart.cfg`** (unchanged content). `boot.py`
  `install()` routes the default (no-method) path by `install_mode`: legacy →
  `templ_install.cfg`, else → `templ_install_lpart.cfg` (mirrors the kickstart
  per-method mechanism). Staged at **`/root/luna2-v1.4-patches/legacy/`** (both
  templates + `boot.py.lever.patch` + backups + MR README), **deployed on this
  controller**, NOT pushed (clustervision repo → human MR).
- **HW-validated:** GET /boot/install/node001 renders `templ_install_lpart.cfg` for
  auto and `templ_install.cfg` (legacy) for legacy; legacy diskfull installs
  end-to-end + boots (sda4 ext4 root). Empirically confirmed the diskfull plugin
  bugs F3 (FAT16 ESP) and **F5** (`set 2 boot on` puts the EF00/ESP flag on the
  wrong partition → firmware can't find the real ESP).
- **Deferred:** node001 hung at firmware after the diskfull cycle (the F5/NVRAM
  mess); IPMI dead + no VMware/govc control here → **operator power-cycles** it
  (pre-set to `auto`+netboot to self-recover). Then re-run raid1 dd-failover
  (confirm F1) + the tmpfs path; land F1-F9 + the F10 MR upstream. See
  [[legacy-install-campaign]] in auto-memory.

---

### 2026-06-07 — GLOBAL RENAME: tx → lpart ("luna partitioner")

Wholesale rename of the installer's `tx` naming to **`lpart`** (luna partitioner),
per operator request. `make check` GREEN; build produces the renamed binaries.

- **Binaries:** `cmd/tx-*` → `cmd/lpart-*` (`lpart-storage-prepare`,
  `lpart-storage-check`, `lpart-osimage-install`, `lpart-bootloader-finalise`,
  `lpart-node-installer`). Makefile `CMDS` updated.
- **Internals:** `internal/txinputs` → `internal/lpartinputs` (package + files);
  env `TX_BIN`→`LPART_BIN`, `TX_NOTUI`→`LPART_NOTUI`; kernel cmdline
  `tx.notui`→`lpart.notui`; the TUI marker family `TX_*`→`LPART_*` (classifier +
  tests; the bash installer emits none, so no cross-side break).
- **Scripts:** the shared hook shim `95luna/tx-phase` → `lpart-phase` (+ its
  `module-setup.sh` dracut_install lines, `create_rpm.sh`, `.spec`); the
  group-shim pre/part/post-scripts in `runtime/` now `exec /usr/bin/lpart-phase`;
  dev scripts.
- **Daemon template:** `templ_install_tx.cfg` → `templ_install_lpart.cfg` (repo
  staging + deployed + `boot.py` lever). The legacy `templ_install.cfg` guard
  greps **both** `tx-node-installer|lpart-node-installer` for migration safety.
- **Docs:** all `.md`/`.svg`/`.html` + `LEGACY.configs` + AGENTS.md + auto-memory.
- **Cleaned the dead `tx_*` Luna field refs** in current/user-facing docs
  (`tx_storage`, `tx_install_mode`, `tx_osimage_*`, `tx_discover_config`) and the
  matching `-qtx-*` CLI flags — they describe the Phase-14e design that v1.4
  already superseded with `install_mode`/`disklayout`; they were never `lpart_*`.
- **Runtime migration — DONE (2026-06-07, this controller).** Built the 5
  `lpart-*` amd64+arm64 binaries (`make build-linux`; packaging confirmed).
  Deployed into `/trinity/images/compute-image`: `lpart-*` into `/usr/bin`, the
  95luna `module-setup.sh` (now `dracut_install`s `lpart-*`) + the `lpart-phase`
  shim, removed the old `tx-*` + `tx-phase` (backup
  `/root/lpart-rename-deploy-backup-20260607`). `luna osimage pack compute-image`
  → new initramfs `compute-image-1780837439-…` (verified: ships `lpart-*` +
  `lpart-phase`, no `tx-*`; `luna osimage show` serves it) + repacked tarball.
  Migrated the **compute-group** AND **node001 node-level** pre/part/post FIELDS to
  `exec /usr/bin/lpart-phase` (node002/003 inherit the group). Render verified
  end-to-end: `/boot/install/node00{1,2}` → 3 `lpart-phase` fields, 0 `tx-` refs;
  legacy render still correct (dual guard matches `lpart-node-installer`).
  Full chain is now lpart: daemon templates → node/group field shims → initramfs
  binaries. (The `.pdf` doc still needs a `build-pdf.py` regen — cosmetic.)

---

### 2026-06-07 — TUI polish + storage-provisioning-model.md finalisation

**TUI (internal/pipeline + internal/tui), all `make check` green, deployed.**
- **Storage Method line** now built from the declared layout (config), shown from
  resolve onward instead of `(resolving...)`, and reduced per operator to just
  `manual|discover|mixed · next boot: local|PXE` (memboot → `diskless (RAM) ·
  next boot: PXE`). The per-set raid/disk detail stays in the disk table only.
  `configLayout()` in `facts.go`; `method_test.go`.
- **Boot dmesg backlog → rsyslog only.** New `engine.InstallerEvent.SyslogOnly`;
  `dumpKernel` flags the backlog; TUI `applyEvent` + `runPlain` skip it; the
  rsyslog forward + log file still get it (console already printed it).
- **Progress shows the WHOLE pipeline** (pre+part+post = 6 stages incl.
  `Bootloader & GRUB finalise`) in every phase, earlier phases pre-marked done
  (`stagesFor` rewrite). Removed the duplicate-note glitches ("Prepare storage
  Prepare storage", "Bootloader & GRUB finalise Bootloader") by only showing a
  note on finished stages. Removed the config line from Progress and the
  `OS -> …` summary from Storage (they were redundant 3×).
- **Validated on hardware:** node001 PXE auto-install on the new TUI reached
  `install.success`, manual RAID1 OS, `md125/126/127 [UU]`. Screenshot-confirmed
  the lean look. **One cosmetic left in SOURCE but NOT yet in the deployed
  initramfs:** the running-note suppression landed after the last repack, so the
  serving initramfs (`compute-image-1780841885…`) still shows the minor running
  note. Needs one more `make build-linux` + copy into compute-image + `luna
  osimage pack compute-image` to go live. (Deploy procedure: see the
  2026-06-07 rename entry; backup at /root/lpart-rename-deploy-backup-20260607.)
- Gotcha learned: `luna osimage pack` sets `packing_bootpause`; a node PXE-ing
  mid-pack shows `cleartoboot: Booting has been temporarily paused due to
  constraints` and auto-proceeds when the pack's queue task finishes.

**docs/storage-provisioning-model.md (the design/sales spec) — heavily polished.**
- Cover = `diagrams/alice.png`; Figure 5 = `diagrams/tui.jpg` (replaced with
  engineering-standards `tui2.jpg`). `build-pdf.py` now wraps raster images in a
  capped `<figure>`. Regen: `python3.11 build-pdf.py` then
  `weasyprint storage-provisioning-model.html storage-provisioning-model.pdf`.
- Added the §2 capstone "**Discover, pin, or manual: the essence of the disk
  layout**" (recipe vs fixed-plan + the one-way `save` migration).
- §5.6 worked examples reordered (single-disk, manual-RAID1, persistent-DATA);
  example 3 extended to a hardened-RHEL separate-mount LVM layout (swap, /var,
  /var/log, /var/log/audit, /tmp, / 100%) — validated against `v2.Validate()`.
- **Reorg:** §2 now sits right after §1; the Cool-facts + pluggable + built +
  testing block moved to a new **§8 Cool facts** (Roadmap → §9). Contents +
  `section 8`→`9` cross-refs updated.
- **Roadmap drift rewritten to the real philosophy:** NOT continuous auto
  reconcile (dangerous mid-job in HPC/AI) — instead drift *detection* via
  Prometheus metrics + Alertmanager, operator decides when to act. Carried into
  the Overview paragraph + the expectations table row.

**State at wrap-up:** `make check` green; node001 healthy (auto, manual RAID1,
up); daemon on lpart templates + legacy `templ_install.cfg` (dual guard);
nothing committed (106 files dirty on branch `advanced-partitioner`); daemon
patches staged at /root/luna2-v1.4-patches/legacy (human MR, clustervision).
**▶ NEXT: the legacy hardware campaign** — `scripts/dev/legacy-battery.sh` on
node001: (a) diskfull (re-confirm F10 end-to-end), (b) raid1 install + the
destructive dd-failover (pull sdb → degraded boot; pull sda → F1 predicts
brick), (c) tmpfs-by-hand. Plus optionally repack to deploy the TUI running-note
fix. See [[legacy-install-campaign]].

---

### 2026-06-07 (PM) — legacy campaign attempt: F11 (stale shim regex) + display/UX fixes

Started the legacy hardware campaign on node001 via `scripts/dev/legacy-battery.sh`.
It surfaced bugs before any disk test completed; pivoted to fixing them.

**legacy-battery.sh fixes (dev tooling).**
- `SH()` helper was defined as `SSH()` while every call site used `SH` — with
  `set -u` not catching undefined *commands*, every node op silently no-op'd
  (pxe-reboot didn't reboot, the dd-wipe would not have run). Renamed helper ->
  `SH`, dropped the unused `ssh_n`.
- `pxe-reboot`/`pull` NVRAM clear matched distro labels (`rocky|redhat|…`) but
  node001's disk entries are labelled `TrinityX sda/sdb/TrinityX`, so nothing was
  cleared and the node LOCAL-BOOTED instead of PXE. Rewrote `pxe-reboot` to force a
  one-shot PXE via `efibootmgr -n <EFI Network>` (BootNext); added `TrinityX` to the
  `pull` clear regex.

**F11 — the real legacy blocker (supersedes F10's root cause).** With PXE forced,
the diskfull install ran but aborted: the TUI appeared and `lpart-storage-prepare`
refused (`install_mode=legacy is daemon-only and must not reach the client`), then
`luna2-start.sh` looped the un-retryable error. The F10 legacy lever (boot.py +
legacy `templ_install.cfg`) IS live and DOES serve the legacy template — but that
template's shim-skip guard greps the PRE-RENAME marker
(`tx-node-installer|lpart-node-installer`) while the renamed shim is
`exec /usr/bin/lpart-phase <stage>`. No match -> shim runs -> lpart under legacy.
Verified by the absence of "legacy mode -- skipping" in the retry cycles and a
direct grep test. See LEGACY.configs F11.

**Fixes — client side (this repo, branch advanced-partitioner, `make check` GREEN):**
- `cmd/lpart-node-installer/main.go`: read install-context.json at startup; if
  `install_mode=legacy`, refuse up-front with a plain console FATAL, **no TUI, no
  pipeline**, exit `exitFatalConfig` (3). Implements the operator requirement
  "legacy means legacy: no TUI" as a client backstop. Test: `TestLegacyRefusal`.
- `redhat/x86/.../95luna/luna2-start.sh`: capture install.sh's real exit code; on
  the fatal sentinel (3) `fatal_park` (maintenance shell) instead of the infinite
  `retry_countdown`. (ARM/openSUSE trees lack the modern wrapper — out of scope.)

**Fixes — daemon side (clustervision; staged at /root/luna2-v1.4-patches/legacy,
NOT pushed):**
- `templ_install.cfg`: add `|lpart-phase` to the 3 shim-skip greps — the campaign
  unblocker AND true "no TUI on legacy". APPLIED in the staged copy.
- both templates: `exit 1` -> `exit $LUNARET` in pre/part/post handlers so the
  fatal code reaches luna2-start.sh. Specified in the staged README for the human
  MR (not auto-applied).

**State at wrap-up:** node001 restored to its healthy `auto`/`default` baseline
(operator is driving its boot). Client changes uncommitted on branch
`advanced-partitioner`. **▶ NEXT: deploy daemon fix (A) (`|lpart-phase`) to the live
controller daemon (human decision — clustervision), then resume the battery
(diskfull -> raid1 -> pulls -> tmpfs).** Until then the campaign stays blocked.

---

### 2026-06-07 (PM2) — dev-sync, F1 confirmed, osimage config doc, LOOSE-ENDS catalogue

**Source-of-truth correction (see [[feedback-daemon-source-of-truth]]).** Earlier legacy
findings were derived against the live runtime daemon + the alex fork (stale). Re-baselined
against `clustervision/luna2-daemon:development` @ cfbf7c4 (cloned at
`/root/git/clustervision-luna2-daemon`). development is the CLASSIC daemon: no lpart, no
install_mode lever, no `templ_install_lpart.cfg` — the whole lpart daemon integration is
local-only. GIT-FLOW-006 (patch tracked source, not runtime) is the rule I had missed.

**Deployed development's legacy path + validated on node001:**
- Deployed development `diskfull.py`/`raid1.py`/`nodhcp.py` + development `templ_install.cfg`
  AS the legacy template, re-applying ONLY the lpart-coexistence shim-skip (now matches
  `tx-node-installer|lpart-node-installer|lpart-phase`). lpart path (lever +
  `templ_install_lpart.cfg`) untouched. Backups: `/root/luna2-v1.4-patches/legacy/backup-20260607-devsync`.
- diskfull (development): installs end-to-end and boots, sda4 ext4, Shim1/Boot0000 NVRAM. GREEN.
- raid1 (development): [UU] mirror + dual ESP (`/boot/efi2`); BOTH sda1+sdb1 carry shim+grub+BOOTX64.
- **F1 [HW-CONFIRMED BRICK]:** dev raid1 is NOT disk-fault-tolerant — `/boot` (kernel) on sda2
  only, sdb2 empty, ESPs unsynced. Operator pulled sda + rebooted -> EFI shell. The dual ESP is
  necessary but not sufficient; proper fix = RAID the whole boot chain (the lpart model). The
  earlier "F1 fixed" claim was WRONG (the non-destructive ESP check missed the kernel/boot stage);
  LEGACY.configs F1 + `legacy-lint` F1 corrected. **Lesson re-learned: validate a real cold
  reboot into the disk, never trust "node is up" via the PXE->Luna chainload — see
  [[regression-firmware-disk-boot-gap]].** node001 recovered to healthy lpart auto; its NORMAL
  (non-install) reboot DOES boot local from disk (BootCurrent=TrinityX disk entry, BOOT_IMAGE set,
  kernel 687) — the model is: install boot = PXE/Luna (one cycle), subsequent boots = local disk.

**Client + dev-tooling fixes (uncommitted before this commit; `make check` GREEN):**
- `cmd/lpart-node-installer`: refuses install_mode=legacy at startup -> plain console FATAL, NO
  TUI, NO pipeline, exit `exitFatalConfig` (3). Backstop for "legacy means legacy: no TUI". Test
  `TestLegacyRefusal`.
- `redhat/x86/.../luna2-start.sh`: capture install.sh exit code; on the fatal sentinel (3)
  `fatal_park` (maintenance shell) instead of the infinite `retry_countdown`. Needs the daemon
  `exit $LUNARET` follow-up to actually receive the 3.
- `scripts/dev/legacy-battery.sh`: fixed the dead `SH`/`SSH` helper (every node op silently
  no-op'd) and made `pxe-reboot` force PXE via `efibootmgr -n <EFI Network>` (node disk entries
  are labelled `TrinityX*`, not `rocky`). `legacy-lint.sh` F1 check corrected to the real gap.

**Docs:** added storage-provisioning-model.md **§5.7 Configuring the OS image** (osimage,
osimage_filter preserve model, how install_mode gates the filter) + **§6.1 Getting the image
onto the node** (torrent->http download, ACLs, first-install promotion, persistent excludes).
HTML+PDF regenerated.

**▶ NEXT — see `LOOSE-ENDS.md` (new, canonical).** Top five: (1) this commit + alex backup;
(2) open the daemon MR (patches 01-08 + legacy lever/regex + `exit $LUNARET`); (3) wire +
on-node test one non-x86 target, de-stub SUSE; (4) run the cheap safe HW gaps (sync
healthy/broken, sanitize, retry, filter shapes) on a recoverable node; (5) wire save-flag
write-back (ADR-0008).

---

## memboot HW-confirmed on node001 (2026-06-07)
**`install_mode=memboot` proven end-to-end on node001** — closes the basic-path half of
LOOSE-ENDS Tier-3 #9 (memboot was implemented but never hardware-proven). Surfaced while testing
an Ubuntu-26.04 image upgrade (a cloned `ubuntu-image` dist-upgraded 22.04→24.04→26.04 in an
lchroot sandbox, packed fine); provisioning that clone exposed the lpart-binaries-only-in-
redhat/x86 gap (Tier-1 #3 — `download_image handled by lpart-osimage-install (no-op here)` because
the Ubuntu initramfs carries no `lpart-*`), so the memboot test was run on the lpart-wired
`compute-image` instead.
- **Setup:** node001 → compute-group / compute-image (redhat/x86, lpart in initramfs),
  `install_mode=memboot` (node override). Forced PXE by operator power-cycle (IPMI dead + govc
  unconfigured here, as the JOURNAL/LEGACY notes record).
- **Result:** PXE → compute-image dracut initramfs → `lpart-phase` saw memboot → OS came up
  **from RAM (`root=tmpfs`)** in ~30 s; luna node `status = success`; node healthy
  (`systemd is-system-running = running`, sshd + munge active), ssh-reachable; 8.4/31 Gi RAM used.
- **Disk fence held:** sda/sdb prior RAID1 (md126 ESP / md127 xfs / md125 LVM `os-root`) left
  **fully intact** — memboot partitioned/wrote nothing; no on-disk filesystem mounted (pure RAM boot).
- **Still open for memboot:** the `+ persistent data set` variant (§3.10) — node001's layout had
  no persistent set, so that path stays inventory. Tier-3 #9 ticked for the **basic** path only.
- node001 restored to its campaign config afterward (compute-group, `install_mode=auto`, the lpart
  `disklayout`) from the saved backup.

## new installer wired into the Ubuntu image + pivot-target bug found (2026-06-07)
Pushed the memboot test onto the **Ubuntu 26.04** clone to exercise lpart on a non-redhat target
(LOOSE-ENDS Tier-1 #3). **The wiring works; one cross-distro bug blocks the boot.**
- **Wired lpart into the Ubuntu (`initramfs-tools`) initramfs** — the gap was only the binaries:
  copied the 5 static `lpart-*` amd64 binaries + the `lpart-phase` shim into the image `/usr/bin`,
  `apt install`ed the tools the image lacked (`mdadm lvm2 gdisk jq fdisk lsscsi`), and added a
  self-contained `initramfs-tools` hook `hooks/lpart` that `copy_exec`s the full installer command
  set (parity with redhat 95luna `dracut_install`) + the binaries. `update-initramfs` + repack;
  verified every command + binary present in the served initrd. (The Ubuntu `hooks/luna` already
  carried most disk tools but the *deployed* image hook was an older/shorter copy.)
- **Result on node001 (PXE, install_mode=memboot, ubuntu-2604-test):** the new installer **ran
  end to end** — luna status `discovered → partscript → success`; `lpart-osimage-install` did
  torrent→HTTP fallback download + `tar -I lbzip2 … -C /sysroot` extract, "part phase complete".
- **BUG — pivot target is dracut-only.** `lpart-osimage-install` extracts into **`/sysroot`**
  (DefaultSysroot, the dracut convention / bash `LPART_TARGET_SYSROOT`), but Ubuntu `initramfs-tools`
  pivots into **`$rootmnt` = `/root`** via `run-init`. So `/root` was empty → "No init found" →
  busybox `(initramfs)` shell. Confirmed on the console: `[extract] extracting osimage into /sysroot`,
  then `ls /root` empty.
- **Fix options (boot-test pending — node001 must be power-cycled to re-PXE, no remote power here):**
  (a) make lpart's memboot target the init-system pivot root — honour `$rootmnt` env when set
  (initramfs-tools), else `/sysroot` (dracut); needs both `lpart-storage-prepare` (memboot tmpfs
  mount) and `lpart-osimage-install --sysroot` to agree (the flag already exists). OR (b) bridge in
  the Ubuntu initramfs: after luna-start, `mount --move /sysroot "$rootmnt"` before `run-init`.
  (a) is the proper cross-distro fix; (b) is a per-distro glue script. Decide + boot-test next session.
- **node001 left configured for this test** (ubuntu-group / ubuntu-2604-test / memboot / lpart shim
  scripts); currently parked at the initramfs shell. Power-cycle returns it to its **intact disk
  Rocky** install (BootNext consumed). Full restore-to-campaign config saved at
  `/root/ubuntu2604-test-restore/node001-full.json`. The lpart binaries/hook live only in the
  throwaway `ubuntu-2604-test` clone, not the repo — fold into `ubuntu/x86/src/.../hooks/lpart` +
  packaging once the pivot fix lands (the real Tier-1 #3 deliverable).

## RESOLVED (2026-06-08) — pivot fix: lpart honours $rootmnt; Ubuntu 26.04 memboot GREEN on HW
**The pivot bug (JOURNAL 2026-06-07 / LOOSE-ENDS #3b) is fixed and on-node proven.**
- **Root cause:** `lpart-osimage-install` extracted into `/sysroot` (DefaultSysroot, dracut's pivot
  root) while Ubuntu `initramfs-tools` `run-init`s into `$rootmnt` (`/root`, exported by `/init`
  line 74). The legacy bash template was already `${rootmnt:-/sysroot}`-aware; lpart was not.
- **Fix (code):** new `mount.EffectiveSysroot()` returns `$rootmnt` when set, else `/sysroot`
  (mirrors the template). Wired into `mount.New()` (storage-prepare's memboot tmpfs mount) and the
  `lpart-osimage-install --sysroot` default. So on dracut (rootmnt unset) behaviour is unchanged
  (`/sysroot`); on initramfs-tools it follows `/root`. `internal/mount/mount.go` +
  `cmd/lpart-osimage-install/main.go` + `effective_sysroot_test.go` (3 cases). `make check` GREEN
  (lint 0, govulncheck clean); `make build-linux` rebuilt the binaries.
- **Repo wiring (Tier-1 #3 for ubuntu/x86, now on-node tested):** added
  `ubuntu/x86/src/usr/share/initramfs-tools/hooks/lpart` (bakes the 5 lpart binaries + full command
  set) + committed `ubuntu/x86/src/usr/bin/lpart-phase`; `create_deb.sh` now `make build-linux`s +
  stages the binaries (gitignored, like create_rpm.sh); `DEBIAN/control` gained `Depends:` (mdadm,
  lvm2, gdisk, fdisk, lsscsi, jq, parted, dosfstools, lbzip2, aria2, efibootmgr, grepcidr, curl,
  openssh-server, tpm2-tools) — these were missing from the deployed image. Fixed `.gitignore`
  (stale `tx-*` → `lpart-*`, keep `lpart-phase`).
- **Proof:** node001 — `discovered → prescript → partscript → success`, then **Ubuntu 26.04 LTS,
  kernel 7.0.0-22-generic, `root=tmpfs`** (~30s); disk RAID1/LVM untouched, pure RAM boot. Only
  `openipmi`/`sssd-*` units degraded (no IPMI HW / no LDAP in standalone — environmental, like the
  redhat memboot's munge). Reproduced on **node002** (same image, no repack). Both driven entirely
  over ssh (no operator power-cycle needed — disk Rocky / RAM Rocky are ssh-reachable; arm BootNext
  PXE + reboot).
- **Still open:** ubuntu/arm + opensuse lpart wiring (untested HW); fold the daemon templates' MR;
  the `+persistent` memboot variant (§3.10). node001/node002 left on the ubuntu test image — restore
  to campaign config from `/root/ubuntu2604-test-restore/node00{1,2}-full.json` when done.

## de-hardcoded the OS-image download; phase contract pinned (2026-06-08)
A code review (full audit, see commit) found `lpart-osimage-install` had **frozen the legacy
install.sh download contract**: it hardcoded the file-server IP (`10.141.255.254:7051`), always
tried torrent-first, ignored `luna.verifycert`, and read the NIC from env — while every *other*
Luna caller in the tree reads `luna.url`/`luna.verifycert` from `/proc/cmdline`. The legacy bash
template honours `download_{{PROVISION_METHOD}}`/`{{PROVISION_FALLBACK}}`; the Go port did not.
- **Fix (lpart only — no daemon change):** the node-detail API already returns
  `provision_method`/`provision_fallback`/`provision_interface`, so `lpartinputs` (part-phase
  `install-context`) now reads them and writes them into `install-context.json`;
  `installcontext.Context` carries them; `osimage.Download` honours the order via `ProvisionOrder`
  (empty ⇒ torrent→http default); and `lpart-osimage-install` derives the file base URL from cmdline
  `luna.url` (`FileBaseFromLunaURL` → `http://<host>:7051/files`), honours `luna.verifycert`
  (`--insecure`), and logs `file_base_url`/`provision_method`. `--luna-url` default dropped (was the
  hardcoded IP).
- **Proven locally** (synthetic cmdline + `provision_method=http` ctx): URL derived as
  `http://10.141.255.252:7051/files` (cmdline `.252`, NOT the hardcoded `.254`), HTTP tried first,
  torrent as fallback, `--insecure` applied. `make check` green. New tests: `download_test.go`
  (`ProvisionOrder`, `FileBaseFromLunaURL`), lpartinputs provision round-trip.
- **Phase contract pinned** (new `internal/pipeline/phases_test.go` + `regression.md §0a`):
  pre = env prep (no disk/daemon); part = resolve install-context (incl. provision fields) →
  storage-prepare → osimage; post = bootloader. Inputs resolved at the *start* of part, before any
  mutation; pre owns no destructive stage.
- **Still open:** the audit's B1 (decompressor/`.tar.bz2` frozen — ARM/format), A6 (split token
  paths), and threading the download tuning (bt-stop-timeout etc.) from context; the on-node test on
  node002 is pending a reachable node (both cycling now). Full audit in the session; binaries rebuilt.

## 2026-07-09 — Stabilisation campaign start (local, pre-GitLab)

Campaign plan: `/root/lpart-review-out/MASTER-PLAN.md`; analysis: `/root/lpart-review-out/FTA-lpart-current.md`
(qualitative FTA of this tree) + `REVIEW-advanced-partitioner.md`. Local git initialised at the repo
root (branch `advanced-partitioner-stable`), baseline commit = the reviewed tip `5279b98`.

**Baseline `make check` (controller, Go 1.25.9) — RED as shipped:**
- `cmd/lpart-osimage-install`: `TestPersistentMountpointsAddedToExcludes` + `TestPersistentSingleSetShape`
  FAIL on any host without a populated `/sysroot` (non-hermetic; open the real path).
- gosec: G703 `internal/pipeline/pipeline.go:190`, G602 `internal/partition/partition_test.go:313` (3 issues).

**Node lab:** node001-004 (10.141.0.1-4) UP, luna status "success". Scratch disks already attached:
n1/n2 vdb+vdc (8G), n3 vdb-vdd, n4 vdb-vde. RAID matrix runs on n3/n4.

## 2026-07-10 — Stabilisation campaign: Phases 1-8 (barriers + on-node validation)

All FTA barriers implemented (commits `osimage: file-level...` through `fsops/selector...`,
tag `phases-1-7-done`), `make check` fully green. Full plan/evidence:
`/root/lpart-review-out/MASTER-PLAN.md`, FTA: `/root/lpart-review-out/FTA-lpart-current.md`.

### Phase 8 — destructive validation on node001-004 (trx cluster, BIOS VMs, scratch disks vdb..vde)
Method: binaries built on the controller, pushed to nodes, driven over SSH (this cluster's
luna daemon has no lpart wiring — full PXE-driven lpart installs deliberately out of scope to
avoid rewiring the demo cluster). Real image extracts served via a temporary http.server on
the controller. Node OS disks (vda) never named in configs; the new selector subtree guard
additionally refuses them (mounted).

| Case | Result | Evidence |
|---|---|---|
| V1 reservation (MCS-1 shape: discover OS + persistent smallest disk) | **PASS** | `disk /dev/vdb is reserved by set "data"`; OS created on vdc; sha256 of persistent files intact |
| V2 persistent survives install_mode=full recreate | **PASS** | checksums intact |
| V4 REAL 4.4GB Rocky 9.8 extract over mounted persistent volume | **PASS** | extract VERIFIED in 120s; `/data` excluded; checksums intact post-extract |
| V5 corrupt storage-result.json | **PASS** | osimage-install rc=2 `safety_refusal=storage-result` |
| V6 dry-run | **PASS** | rc0 in 20s (no 30s/disk hang), disks byte-identical (GPT+first-4MiB fingerprint) |
| V7a data fence halt (sync + broken data set) | **PASS** | rc=2 `safety_refusal=data-fence` |
| V9a sanitize×persistent refusal | **PASS** | rc=2 `safety_refusal=sanitize-persistent`, disk untouched |
| V11 RAID5 parity (C1) | **PASS** | create WITHOUT --assume-clean (resync observed); scrub mismatch_cnt=0; degraded read of real data OK; rebuild OK; final scrub 0 |
| V13 persistent cold refresh | **PASS** | after node reboot (which REPROVISIONED vda via the legacy flow) + vgchange -an: auto run activated + mounted, checksums intact |
| V14 reinstall over prior LVM (re-entrancy) | **PASS** | repeated create runs over existing os/data VGs, no lvcreate collisions |
| V15 pinnable paste block | **PASS** | BEGIN/END block with both sets emitted (kernel paths on virtio — no by-id links exist; by-id upgrade is unit-pinned, needs real HW) |
| V16 memboot + persistent (§3.10) | **PASS** | tmpfs /sysroot + persistent mounted, checksums intact, declared os set ignored |
| V3 findmnt-absent refusal | unit-pinned | extract_barriers_test.go (fail-closed + sysroot-presence sanity); on-node repeat adds nothing |
| V7b/V8 park-on-fatal + SIGINT via full node-installer | unit-pinned | result_test.go + strict_test.go + e2e exit-2 tests; full wrapper park needs an lpart-wired PXE controller |
| V9b real sanitize erase | **deliberately skipped** | sanitize erases EVERY disk incl. the driving OS disk by design — needs a ramdisk/PXE context; EraseAll is unit-tested |
| V10/V12 boot-after-disk-pull (C2) | **HW GAP (top item)** | trx nodes are BIOS VMs; the mirrored boot chain is UEFI-only. Unit-pinned (N-member md efi/boot arrays); MUST be proven on UEFI HW before ship |

Incidental validations: D1 (activation of deactivated VGs before mount-existing), D2 (dry-run
wait skip), identity re-check runs on every create (virtio serials empty → vacuous, as designed).
Node lab left with the persistent `data` VG on node001:vdb (checksummed fixture), r5data on
node004:vdb-vde; node OS disks untouched by lpart (only the demo cluster's own reprovision flow).

### Phase 9 — SUSE de-stub, Ubuntu-content validation, packaging
- **SUSE adapter implemented** (LOOSE-ENDS #2 / gaps G2 closed at code level): composes the
  RHEL flow — the families share the exact toolchain this installer drives (dracut,
  grub2-mkconfig at /boot/grub2/grub.cfg, /etc/default/grub, signed shim under EFI/opensuse|sles
  via the generic findSignedShimDir scan, grub2-install for BIOS). update-bootloader/
  perl-bootloader deliberately not used. Unit suite pins dispatch, preflight messages, the
  chrooted command set (grub2-install/mkconfig/dracut with the SUSE kver), and Validate paths.
  On-SUSE-image validation remains for the HW lab.
- **Real-Ubuntu validation of the debian family** (Ubuntu 24.04 server cloudimg rootfs staged
  onto a fresh lpart layout on node001): family=debian detected from os-release; missing-tool
  preflight refuses cleanly naming update-grub (the rootfs-only tarball ships no grub/kernel —
  full debian-flow runs need a complete image on the PXE lab).
- **FOUND + FIXED by that test: the ancestor's MCS-6 fstab bug was live here.** WriteFstab's
  skip-if-mount-present semantics preserved the golden image's own root entry
  (`LABEL=cloudimg-rootfs /`), so the installed node's REAL root UUID was never written → boot
  to emergency shell on any image that ships a populated fstab (TrinityX images ship an empty
  one, which is why Rocky never hit it). Fix: replace-by-mountpoint — entries for mounts this
  install created are authoritative (like grub.cfg); non-owned operator lines survive verbatim.
  Verified against the real Ubuntu tree (stale line replaced by UUID=<node-root>). Pinned by
  TestWriteFstabReplacesStaleImageEntry + updated reentrancy test.
- **Packaging gate:** redhat/x86 `create_rpm.sh` builds clean on the controller
  (luna2-client-2.0-42.x86_64.rpm) with the new binaries + the modified 95luna wrapper.
- Remaining for the distro min-bar (memboot/single-disk/raid1 BOOTS per target): needs the
  lpart-wired PXE controller + UEFI nodes; ubuntu/x86 repo wiring itself landed pre-campaign
  (LOOSE-ENDS 3b).

### Phase 11 — TrinityX compute images built via the TrinityX ansible path + full-flow validation
Answering the "which distros, which versions, with slurm, via the TrinityX path" question head-on.
Images built ON the controller with the stock TrinityX site playbooks (`/root/trinityX/site`,
`workload_manager: [slurm]` in group_vars):

| Image | Playbook | Version | Slurm | lpart full flow (storage-prepare → REAL extract → bootloader-finalise) |
|---|---|---|---|---|
| compute-image (pre-existing) | compute-redhat.yml | Rocky 9.8 | yes | **PASS** (Phase 8 V4 + whole matrix) |
| ubuntu-image | compute-ubuntu.yml | **Ubuntu 22.04 LTS** | **yes (slurmd+munge baked)** | **PASS on node003**: full debian adapter — BIOS grub-install, update-grub → /boot/grub/grub.cfg, update-initramfs → initrd rebuilt, UUID fstab, verify green (rc0) |
| opensuse-image | compute-opensuse.yml | **SLE 15-SP5** | no — `trinity/slurm` role is COMMENTED OUT upstream for suse (no SLE slurm packages in the TrinityX repo) | **PASS on node003**: first live run of the new suse adapter — family=suse, dracut initrd, grub2-mkconfig → /boot/grub2/grub.cfg, UUID fstab, verify green (rc0) |
| Ubuntu 24.04 | compute-ubuntu.yml `-e alternative_distribution=Ubuntu-24` | 24.04 base created, **setup FAILS** | — | **TrinityX-16 platform limit, not lpart**: controller's pinned ansible-core 2.14 cannot manage Python 3.12 targets (`HTTPSConnection.__init__() got an unexpected keyword argument 'cert_file'` at the luna2-client.deb download). 22.04 is the supported ceiling on this release; fixing = ansible-core >= 2.16 on the controller (operator/TrinityX decision). |
| Debian proper | — | — | — | No TrinityX compute-debian playbook exists; the pre-existing `debian-image` luna entry points at a kernel-only bootstrap tree (no grub/initramfs-tools — adapter refuses at preflight by design). Debian family is represented by Ubuntu. |

Notes: image transport for the tests = temporary `systemd-run` http.server on the controller
(unit `lpart-http8899`, stopped after) — no luna config touched; images/tarballs registered by
the playbooks' own luna/osimage-pack role. **Controller disk is now at 91% (3.7G free)** — the
new images+tarballs account for ~5G; the four older `compute-image-*.tar.bz2` build generations
(~7.8G) predate this campaign and are the operator's to prune.

### Phase 11 addendum — Ubuntu 24.04 UNBLOCKED (venv ansible-core 2.16)
The operator (correctly) recalled Ubuntu 24/26 working before; the earlier "blocked" finding was
too broad. Precise statement: TrinityX-16's image-setup play cannot manage a Python>=3.12 chroot
under the controller's PINNED ansible-core 2.14 (`cert_file` kwarg removed in py3.12). With a
throwaway venv (`/root/ansible216-venv`, python3.12 + ansible-core 2.16.19, system config +
collections via ANSIBLE_COLLECTIONS_PATH) the STOCK playbook builds 24.04 cleanly:

    /root/ansible216-venv/bin/ansible-playbook compute-ubuntu.yml \
      -e image_name=ubuntu24-image -e alternative_distribution=Ubuntu-24 \
      -e alternative_image_source=base

Result: **ubuntu24-image = Ubuntu 24.04 LTS, slurmd+munge baked, packed + luna-registered**
(ubuntu24-image-1783721853.tar.bz2), failed=0. Full lpart flow on node003 (vdd): storage-prepare
→ real extract → complete debian adapter (grub.cfg generated, initrd.img-6.8.0-31 rebuilt, UUID
fstab) → verify green, all rc0. The earlier hand-built Ubuntu 26.04 installer runs (LOOSE-ENDS
3b, 2026-06-08) are consistent with this: the lpart side was never the problem.
Housekeeping: pruned the three stale compute-image tarball generations (had to kill the luna
aria2c seeder holding the deleted files open — it respawns via aria2c-daemon.sh); controller at
85% with all four images present (rocky/ubuntu22/ubuntu24/suse). Recommendation for TrinityX
upstream: ansible-core >= 2.16 (or the venv recipe) for any py3.12+ target image.

### Phase 12 — UEFI boot-side gaps CLOSED on a real secure-boot VM (host OVMF)
Built a UEFI+SecureBoot VM on the host (OVMF, q35) with two blank virtio disks carrying
SERIALS (LPARTVDB001/LPARTVDC002 → real /dev/disk/by-id/virtio-* links), as the installer env.
Ran the full lpart flow onto a **RAID1 OS across both disks**, real Rocky 9.8 compute image.

- **G7 / C2 (mirrored boot chain) — PROVEN.** storage-prepare built ESP *and* /boot as md
  raid1 (metadata 1.0) across BOTH members — md126=ESP(vdb1+vdc1), md125=/boot(vdb2+vdc2),
  md127=LVM root(vdb3+vdc3) — not disk-0-only. bootloader-finalise (UEFI path) ran
  StageSignedShim + chroot efibootmgr + grub2-mkconfig, rc0.
  - Boot #1 (both disks): booted to `Rocky Linux 9.8 … localhost login:` — GRUB→kernel→systemd
    off the mirrored ESP. Screenshot uefi-bothdisks.png.
  - Boot #2 (**disk 0 / vdb physically detached**): booted DEGRADED off vdc alone to full
    multi-user — NetworkManager, sshd, SSSD, Prometheus+slurm exporters, /trinity/{home,shared}
    mounted, DHCP lease acquired. Screenshot uefi-degraded2.png. This is the exact case the
    pre-fix code bricked (ESP/boot on disk 0 only → firmware/GRUB dead-end).
- **BR-9 identity — PROVEN live.** The OS set was pinned by `/dev/disk/by-id/virtio-LPARTVDB001`
  (not kernel names); the pre-destruction serial re-verify ran against REAL serials and passed
  (no false refusal), exercising the by-id + TOCTOU-serial path that was vacuous on the virtio
  nodes.
- Env notes (installer-golden gaps, not lpart): the rocky golden lacked lbzip2 (EPEL) + lvm2 +
  efibootmgr-on-host; the real Luna initramfs bundles these. `--allow-non-installer` used since
  the VM isn't PXE-booted with root=luna.

Still needs the daemon+initramfs PXE path (see Phase-12 daemon note): wrapper fatal_park in a
real PXE boot, and the Ubuntu initramfs-tools hook leg. The exit-code chain feeding park is
unit-pinned + node-proven (storage-prepare exit 2 on V7a/V9a); node-installer 2→3 mapping and
the 95luna park-on-2-or-3 are code+review complete.

### Phase 12 daemon note — the supplied patches are the SUPERSEDED generation
`luna2-phase14-patches.tgz` = the Phase-14e **tx_*** field model (`tx_storage`,
`tx_install_mode`, `tx_osimage_sync`, …) with a template that file-drops them into
/tmp/install_files/runtime. But ADR-0006 RETIRED that shim: the shipped client
(`internal/lpartinputs`) reads `install_mode` / `disklayout` / `osimage_filter` DIRECTLY from
the daemon node-detail API and does NOT read tx_*. Applying these to the live trx-ctrl daemon
would add columns the current client never queries (its daemon path would return defaults and
never fall through to the tx_* file-drop). The CORRECT daemon series per ADR-0006 is
`/root/luna2-v1.4-patches` (adds install_mode/disklayout/osimage_filter) — NOT present on this
host or controller. Did NOT apply anything to the live daemon (backed up daemon code+DB to
/root/luna-daemon-pre-phase12.tgz + /root/luna.db.pre-phase12.bak regardless). Needs an operator
decision: locate the v1.4 series (GitLab / backup) or authorise re-deriving it.

### Phase 12 continued — v1.4 daemon series verified (correct, partial-apply), wrapper park isolation-proven
Operator supplied the RIGHT series (`luna2-v1.4-patches.tgz`): install_mode/disklayout/
osimage_filter — matches the shipped client (ADR-0006), not the superseded tx_*. Verified it
against a COPY of the live daemon (nothing applied live): patches 01/04/05 clean; **02/03
cascade hunks FAIL** — this controller's daemon is a different base than the series' `dc89e4cc`,
so landing needs manual reconciliation of the node/group JOIN-alias+b64 blocks. Full analysis:
`/root/lpart-review-out/DAEMON-LANDING-v1.4.md`.

**HAZARD found + avoided:** the routing lever (`boot.py.lever`) sends every default/non-legacy
node (install_mode cascades to 'auto') to templ_install_lpart.cfg, whose partscript needs
lpart-node-installer — absent from the compute-image initramfs (verified). On the SHARED trx
controller that would break reprovisioning for node001-004 + every demo cluster. Did not apply;
it belongs on the GitLab MR or a dedicated throwaway controller.

**Wrapper park — isolation-proven:** drove the exact patched 95luna decision for every class →
rc0 success, rc1 retry(30s), rc2 park, rc3 park. Combined with the node-proven exit chain
(V7a/V9a exit 2) + unit pins, the park path is validated bar a live node sitting in the shell.

### Phase 12 — v1.4 daemon/CLI series RECONCILED to this controller's base (ready-to-commit)
Reconciled the two (well, three) hunks that failed against this daemon's drifted base and
packaged the full landable series at `/root/lpart-review-out/v14-daemon-reconciled/` (APPLY.md
+ patches/ + migration/ + legacy/). Verified end-to-end: the complete series applies clean on
fresh daemon+cli copies and every patched .py re-parses under the daemon's Python 3.10.
- `02-03-node-group-cascade.reconciled.patch` (new) replaces upstream 02+03 — install_mode
  default on the get_node CASCADE dict (not the node-list dict), + disklayout/osimage_filter
  aliases & b64items, re-anchored past the live ipxe_kernel/unmanaged_bmc_users members.
- `08-cli-constant.reconciled.patch` (new) replaces upstream 08 — EDITOR_KEYS/overrides/sortby/
  divider re-anchored past the live tpm_*/cloud/ipxe_kernel members.
- 01/04/05/06/07 apply clean (line offsets only).
Nothing applied to the live daemon; the routing-lever HAZARD (reroutes all nodes to the lpart
template) is documented in APPLY.md — land on the MR / a dedicated controller only. All
controller scratch dirs removed.

## 2026-07-11 — REAL full PXE regression through patched Luna (honest, end-to-end)

After being (rightly) called out that the V1-V16 matrix drove the binaries directly and skipped
the Luna pipeline, wired the actual control plane and ran regression.md through real PXE:
- Applied the v1.4-reconciled daemon+CLI series + migration + templ_install_lpart.cfg + routing
  lever to the live trx-ctrl daemon (node001-004 = the disposable lab this controller serves);
  built luna2-client-2.0-42 (lpart + park fix), installed into compute-image, repacked → the
  PXE initramfs now carries the lpart pipeline. install_mode set per node/group via
  `luna node/group change --install-mode` (legacy → templ_install.cfg, else → the lpart template).

### Real bugs the real pipeline surfaced (none visible to direct-binary tests)
1. **kmsg poller-park hang (FIXED, engine/kmsg.go).** The `pre` phase hung forever at
   "capturing boot/dracut kernel log". ReadKmsgBacklog wrapped an O_NONBLOCK /dev/kmsg in an
   *os.File → Go's netpoller parked after draining, waiting for the next kernel message that never
   comes. Fixed with raw syscall.Read. Only `--phase pre` through the full flow hit it; the direct
   tests only ran `--phase part`. Pinning tests assert it returns within a timeout.
2. **retry re-entrancy / config pollution (FIXED, pipeline.go).** runPart copied the RESULT-shaped
   storage-resolved.json over the INPUT storage.json; the install tmpfs survives luna-installer.sh's
   30s retry loop, so the next attempt re-fed it as --config → "config_validation: unknown field
   firmware_mode". Removed the copy-back; storage.json stays the input config across retries.
3. **dual pre/part/post-usage (WARNING ADDED, templ_install_lpart.cfg).** The compute-group's
   pre/part/post FIELDS still held the LEGACY mke2fs disk script; the lpart template ran them
   (dual usage) and the install failed. Added lpart_dual_usage_warn: warns + sets
   install.warn.dual_<phase> when a field is non-lpart content while an lpart install_mode is set.
   VERIFIED firing on the console. (Answers the operator's exact ask about switching legacy↔new.)
   The fix for a node is to set its fields to `exec /usr/bin/lpart-phase <phase>` (the shim).
Env findings (not client bugs): the installer-initramfs sshd fails to start (deprecated
`UsePrivilegeSeparation` in 95luna/sshd_config) → breaks SSH-into-installer (regression 3.25);
memboot of the 4.4GB image needs RAM > tarball(2.2)+extracted(4.4) since both sit in tmpfs — 8GB
stalls, 16GB completes. (Streaming curl|tar would halve peak RAM — future enhancement.)

### 3.1 memboot smoke — PASS (real PXE, node001, 16GB)
daemon served templ_install_lpart.cfg (lever), source=daemon input resolution, pre→part→post via
lpart-node-installer, storage-prepare tmpfs + osimage download(watchdog curl)+extract into RAM,
status → **success**. Node booted: `findmnt -no FSTYPE /` = **tmpfs**, SSH-reachable, running from
RAM. First genuinely end-to-end pipeline pass. Screenshots n1-*.png in the session scratchpad.
Remaining regression.md cases (3.2 single-disk, 3.4 raid1, 3.5/3.7 sync fence, 3.18 park) to run.

### park-on-fatal — PASS (real PXE) after fixing TWO more real bugs
sanitize+persistent through the real pipeline: storage-prepare exit 2 (safety_refusal) was
RETRY-LOOPING instead of parking. Two seam bugs my unit tests missed:
4. **runStepArgv discarded the exit code (pipeline.go).** RunCommand returns (2, exitError) for a
   non-zero child; runStepArgv returned the raw err BEFORE building the stepError, so the code was
   lost -> classified retryable -> node-installer exited 1 not 3. Fixed: build stepError from the
   code first. Pinning test TestRunStepArgvPreservesExitCode drives real /bin/sh children.
5. **template collapsed the code (templ_install_lpart.cfg, = LOOSE-ENDS #6).** pre/part/post did
   `exit 1` on any non-zero child -> even a correct exit 3 became install.sh exit 1. Fixed:
   `exit $LUNARET`. This was a known-but-unapplied LOOSE-END; now applied + validated.
After both: storage-prepare 2 -> node-installer 3 -> exit $LUNARET 3 -> install.sh 3 ->
luna-installer.sh fatal_park: "INSTALL FAILED FATALLY (exit 3) -- NOT retrying" + maintenance
shell (no retry cycle). Screenshot n1-park2.png. The FTA Cluster B park fix now works end-to-end.

### Real PXE regression tally (this session)
PASS through the actual patched Luna pipeline: 3.1 memboot (tmpfs root), 3.2 auto single-disk
(boots from LVM disk root), park-on-fatal (sanitize+persistent -> fatal_park). FIVE real bugs the
real pipeline found that direct-binary + unit tests all missed: kmsg poller-park hang, retry
re-entrancy config pollution, runStepArgv code-loss, template exit-code collapse, + the dual-usage
warning gap. Env findings: installer-initramfs sshd broken (deprecated UsePrivilegeSeparation),
memboot RAM sizing. The direct-drive V-matrix validated the client logic; only the real pipeline
exposed the integration seams — the callout was correct.

### More real-PXE cases (node001, 32GB)
- **3.4 manual RAID1 OS — PASS.** auto + manual raid1 on vda+vdb (by-path): create → success →
  boots from disk, root = xfs /dev/mapper/os-root on md127 (raid1 vda3+vdb3). BIOS mode.
- **3.14 disk-pull (BIOS raid1) — FINDING (real gap).** Detached vda (disk0), booted: the node
  could NOT localboot from vdb and fell back to PXE-reinstall — because on BIOS the mirrored-boot
  chain is NOT built (C2 MirrorBoot is UEFI-only; proven working on the UEFI VM). /boot lives on
  vda2 only, so losing disk0 loses /boot. Same class as the legacy F1 brick. **Recommended fix:**
  extend the md-mirrored /boot chain (metadata 1.0) to BIOS raid1/5/6/10 OS sets too (grub2-install
  already runs on every member; /boot just needs to be an md mirror so a surviving leg is readable).
  UEFI is already covered. NOT a regression from this campaign — a pre-existing BIOS-path gap the
  real disk-pull surfaced.
- **Reservation fail-closed — confirmed live.** The post-pull reinstall's storage-prepare errored
  cleanly "device not found: /dev/disk/by-path/pci-0000:00:05.0" with a candidate list (didn't
  guess another disk) — BR-1b working end-to-end.

---

## 2026-07-11 — BIOS support removed; UEFI is now REQUIRED (firmware gate)

**Decision (operator):** kill BIOS/legacy-CSM support altogether and have the installer *check*
for firmware. Rationale: BIOS has no NVRAM boot-entry management (efibootmgr is UEFI-only) and
cannot provide a disk-fault-tolerant mirrored boot chain, so a BIOS install is silently degraded
(this is the same class as the F1/C2 "raid1 /boot single-disk brick" gap). Every current server
target boots UEFI — x86 UEFI; **NVIDIA Grace/GH200/GB200 and other Arm SystemReady SR machines**
(Arm's server spec *mandates* UEFI+ACPI); BlueField DPUs. A BIOS result means legacy/CSM is
enabled in firmware. Rather than half-support BIOS, refuse it.

**Change (client, branch advanced-partitioner-stable):**
- **Primary gate (operator-facing):** `lpart-storage-prepare` now refuses a non-UEFI node right
  after `detectFirmware()`, *before any destructive action*, via the exit-2 `safety_refusal`
  contract (`safety_refusal=firmware-not-uefi`) so the wrapper parks instead of retrying a
  permanent condition. `detectFirmware` is now a package var (test seam).
- **Belts (fail-loud invariants):** `partition.BuildLayout` refuses non-uefi (no BIOS-GRUB layout
  is ever produced); the three bootloader adapters (`rhel`/`debian`/`suse`) `Install()` refuse
  non-uefi instead of dispatching to a BIOS path.
- **Dead BIOS code deleted:** `installBIOS` (rhel + debian), the EF02/BIOSBOOT branch in
  `partition.go`, and the BIOS `grub2-install`/`grub-install` presence checks in the three
  adapters' `Prepare`. The v2 config layer was already UEFI-only (`BootFirmware()=="uefi"`,
  validator requires a `/boot/efi` vfat ESP), so this only removed the runtime BIOS branches.
- **Tests:** the 4 BIOS behaviour tests became refusal tests
  (`TestRHELInstallRefusesBIOS`, `TestDebianInstallRefusesBIOS`, `TestSUSEInstallRefusesBIOS`,
  `TestBuildLayoutRefusesBIOS`) + new `TestRefusesBIOSFirmware` (gate → exit 2). `runWithEnv`
  pins firmware to uefi so the suite is host-independent.

**make check (go1.25.11 on the host — toolchain installed to `/usr/local/go`, none was present):**
build ✓ · gofmt ✓ · vet ✓ · **golangci-lint `./...` 0 issues** ✓ · **govulncheck 0** ✓ · tests:
every test passes **except** the pre-existing host artifact `TestBuildPinnableConfig_DiscoverBecomesManual`
(proven to also fail on baseline HEAD — it resolves this bare-metal box's real `/dev/disk/by-id`
`wwn-*` symlinks; on a VM/CI `/dev/sda` stays `/dev/sda` and it passes). Not a regression.

**⚠ OPERATIONAL CONSEQUENCE — the demo lab is BIOS.** The trx/demo nodes are BIOS VMs, so lpart
will now **refuse every current lab node** at the gate. To resume real-PXE lpart regression the
node VMs must be switched to **UEFI firmware (q35 + OVMF/edk2)**. Until then the on-node regression
set (memboot/single-disk/raid1/park) cannot run — the binaries are unit-green but the lab firmware
no longer matches what the installer accepts. Flip the lab to OVMF (vmclone/democluster boot
config), or provision a UEFI node, before the next HW pass.

## 2026-07-11 (cont.) — demo lab converted to UEFI; full PXE→lpart→UEFI-boot proven

Because lpart now requires UEFI (above), the BIOS demo nodes had to move to UEFI. Proven
end-to-end on **trx-node001** (the disposable lpart target under controller **trx-ctrl**, OS
hostname `trx16`, reachable `ssh root@192.168.123.217`). The protected `trx` *cluster* semantics
are about fleet golden-migration, NOT node reinstalls — node00X are the lpart install targets.

**UEFI iPXE artifact** (mirror of the BIOS `ipxe-luna-direct.iso`):
- Built `bin-x86_64-efi/ipxe.efi` from `/root/ipxe-build/ipxe` with the SAME embedded
  `direct.ipxe` (`make bin-x86_64-efi/ipxe.efi EMBED=/root/ipxe-build/direct.ipxe`).
- Packaged UEFI-bootable via iPXE's own helper: `util/genfsimg -o
  /var/lib/libvirt/images/ipxe-luna-uefi.iso bin-x86_64-efi/ipxe.efi` (EFI El-Torito; places
  ipxe.efi as EFI/BOOT/BOOTX64.EFI). Direct-boot bypasses Luna's iPXE menu loop, same as BIOS.

**Node VM conversion (i440fx BIOS → q35 UEFI)** — recipe in `/root/lpart-uefi-work/`:
- New domain XML: `machine='pc-q35-rhel8.6.0'`, `<os firmware='efi'>` + `secure-boot=no`
  (libvirt auto-selects OVMF `OVMF_CODE.cc.fd` + copies a per-VM NVRAM varstore), same MAC/disks.
- CD-ROM swapped to `ipxe-luna-uefi.iso` on a **sata** bus; **disk boot order 1, iPXE-CD order 2**
  (empty disk → falls through to iPXE install; after install → boots the UEFI disk).
- Strip all i440fx `<address>`/controllers so libvirt regenerates the q35 PCIe topology.
- Backup of the original BIOS XML: `/root/lpart-uefi-work/backups/trx-node001.bios.xml`.

**GOTCHA — by-path pinning is machine-type-fragile.** node001's pinned disklayout had
`selection:manual devices:["/dev/disk/by-path/pci-0000:00:05.0"]` (an i440fx flat-PCI path).
q35 moves virtio disks behind PCIe root-ports (`pci-0000:03/04/05:00.0`), so that by-path
vanished → `lpart-storage-prepare` **correctly fail-closed** ("device not found … selector=
physical_paths") instead of guessing another disk (BR-1b working live). Fix: re-pin as
**`selection:discover`** with `match.min_size:10G` (uniquely selects the 12.6G OS disk over the
8G data disks) — topology-independent. Applied on the controller:
`luna node change node001 --install-mode full -qdisklayout <file>`.

**Result (verified from inside node001):** `/sys/firmware/efi` present (UEFI); efibootmgr shows
`Boot0004* TrinityX vda` **first** in BootOrder (lpart's UEFI NVRAM registration works on OVMF —
the UEFI-only capability); layout `vda1 600M vfat /boot/efi · vda2 1G xfs /boot · vda3 11G LVM
os-root xfs /`; node boots Rocky 9.8 from the UEFI disk. ~5.4 GB written, single pass, no reinstall
loop.

**Caveat:** this ran the controller's EXISTING (pre-BIOS-removal) lpart binaries — valid because
my edits leave the UEFI path unchanged (they only ADD a BIOS refusal). Deploying the new
UEFI-gate binaries into `compute-image` + repack, then re-running, is the next step before the
full regression resumes on UEFI. node002-004 convert with the same recipe.

## 2026-07-11 (cont.) — NEW UEFI-gate binaries deployed + verified on UEFI

Built the BIOS-removed binaries on the host (go1.25.11, `make linux-amd64-*`, CGO-off static),
scp'd to trx-ctrl, replaced `/trinity/images/compute-image/usr/bin/lpart-*` (old set backed up to
`/root/lpart-bin-backup-20260711/` on the controller), and `luna osimage pack compute-image`
(new initramfs `...1783786377...`). Confirmed the new gate string
(`firmware-not-uefi` / "will not partition a BIOS/legacy-CSM node") is in the packed
`lpart-storage-prepare`.

Forced a clean reinstall on node001 (zeroed vda's first 100MB so OVMF falls through to the iPXE
CD → netboot). Result: netbooted the NEW initramfs, **no wrong `safety_refusal` on the UEFI node**
(the gate correctly stayed silent), full UEFI install (~5.4 GB), reboot, boots Rocky 9.8 from disk.
Inside-verify: `/sys/firmware/efi` present; `Boot0004* TrinityX vda`; ESP+/boot+LVM layout intact.
=> My BIOS-removal change installs correctly on UEFI end-to-end; only new behaviour is the BIOS
refusal (unit-tested; can't exercise on a UEFI node).

**Lab status:** node001 is UEFI (q35+OVMF) and installs via the new binaries. node002-004 are still
BIOS and are part of the *live* trx demo cluster — converting/reinstalling them disrupts the demo,
so that needs the operator's OK (not a throwaway). The full regression catalogue can now run on
node001 under UEFI.

## 2026-07-11 (cont.) — UEFI regression on node001 + PartitionPath by-path bug fixed

Ran the core UEFI regression catalogue on node001 (new BIOS-removed binaries). Results tracker:
`/root/lpart-uefi-work/RESULTS.md`. PASS: 3.1 memboot (tmpfs root), 3.2 auto single-disk (LVM),
3.4 manual RAID1 OS (md ESP+/boot super 1.0, PV super 1.2, **NVRAM entry for BOTH legs**),
**3.14 RAID1 dd-failover** (killed the vda leg → booted degraded from vdb via Boot0007 + the
metadata-1.0 mirrored ESP, md `[2/1][_U]`, multi-user — the disk-fault-tolerant UEFI boot BIOS
cannot do), 3.18 deliberate-fail→retry-countdown (resolve halt, no wipe).

**BUG FOUND + FIXED — `devices.PartitionPath` mishandled udev symlink names.** Case 3.11 (single-
disk **manual by-path** OS set) failed at the `/boot` mount: `mount /dev/disk/by-path/
pci-0000:03:00.02 … Can't lookup blockdev`. Root cause: `PartitionPath` appended a bare partition
number for every non-nvme/md disk, but udev persistent names (`/dev/disk/by-path|by-id|…`) suffix
partitions with `-part<N>`, so it produced `…0.0` + `2` = `…0.02` (nonexistent). Only single-disk
by-path hit it: RAID sets mount the **md** device and discover sets resolve to **kernel** names, so
3.2/3.4 passed. Fix: `PartitionPath` now returns `<disk>-part<N>` when the path contains `/disk/by-`
(pinning tests added; `make check` green). Rebuilt + redeployed into `compute-image` + repacked.
This is also why the earlier node001 layout (pinned by-path) was doubly fragile — q35 moved the
paths AND single-disk by-path mounts were broken; `selection:discover` sidesteps both.

**UEFI regression tally (node001, new binaries): 8 PASS.** 3.1 memboot, 3.2 single-disk, 3.4
RAID1 (mirrored ESP+/boot super 1.0, NVRAM entry for BOTH legs), **3.14 dd-failover** (killed vda
leg → degraded boot from vdb, multi-user — the UEFI-only disk-fault-tolerant boot), 3.11
persistent-survives-full (OS UUID changed, /data marker survived), 3.18 resolve-halt→retry
(exit 1), sanitize-vs-persistent refusal → **exit-3 fatal-park (NOT retrying)**, 3.13 sanitize
(all 3 disks blkdiscard'd). Full tracker: `/root/lpart-uefi-work/RESULTS.md`.

**Minor finding (NOT yet fixed):** `install_mode=sanitize` erases correctly but then
`lpart-storage-prepare` exits **1** on `result.WriteAuto: no results` → Luna retry-loops instead
of a clean stop/exit 0. Fix: the sanitize path (or WriteAuto) should treat "no results" as success
for sanitize. Harmless (re-erasing blank disks) but should be tidied. Skipped-this-pass (not
UEFI-specific): 3.6 sync-healthy rsync, 3.15 raid5/6 (need ≥4-5 disks), 3.20 re-entrancy, 3.25
ssh-during-install.

## 2026-07-11 (cont.) — Enhanced Fault Tree Analysis v2 produced (docs/FTA-v2.md)

Re-did the full FTA with the campaign's new knowledge, per the NASA *Fault Tree Handbook with
Aerospace Applications* v1.1 (2002, the NUREG-0492 revision) 8-step process, IEC 61025 framing,
and Leveson-style SFTA. Analysis only (operator decision) — no code changes.

Key deltas vs v1 (`/root/lpart-review-out/FTA-lpart-current.md`, now marked superseded):
- **Boundary expanded**: the client↔daemon↔wrapper seam (template exit propagation, wrapper
  park/retry, runtime-file lifetime across retries, initramfs env) is now IN scope — all five
  real-PXE bugs lived there, outside v1's boundary.
- **New event classes**: BE-S1..S6 (seam), BE-N1..N3 (device naming), BE-W1..W4 (firmware/UEFI),
  BE-Z1 (sanitize exit), BE-G5 (memboot RAM); MCS-13..18; CCF-7 (contracts only exercised
  end-to-end) + CCF-8 (distributed device-name conventions).
- **Fresh deductive findings**: BE-W3 — `RegisterUEFIEntries` returns SILENTLY (no log/flag)
  when efivars is absent (nvram.go:178) → NVRAM entries never registered → the 3.14 failover
  property can be absent while the install reports success ("redundancy theatre", MCS-17);
  the mapper/multipath naming hole (BE-N3); the sanitize WriteAuto exit-1 path (BE-Z1).
- **Top residual risk re-ranked**: no longer a code defect but the dual pre/part/post-usage
  migration state (MCS-13, warning-only) — recommended barrier NB-2 (hard block + escape).
- **Closed v1's top HW gap**: C2 mirrored boot chain proven on UEFI (RESULTS 3.4/3.14).
- Recommendations NB-1..NB-8 mapped to cut sets, with order: NB-2 (dual-usage block), NB-5
  (efivars loud + verify fail), NB-1 (sanitize exit 0), NB-3 (seam contract gate), rest.

## 2026-07-12 — FTA v2 barriers NB-1/NB-2/NB-5 implemented + live-validated; BE-S7 found+fixed

Implemented the three recommended barriers from docs/FTA-v2.md (operator: "go 4 it"), each with
a pinning test and live validation on node001. All make-check gates green (gofmt/vet/lint 0/
tests, excl. the known host artifact).

- **NB-2 (rank-1, MCS-13) — dual-usage HARD BLOCK.** `templ_install_lpart.cfg`:
  `lpart_dual_usage_warn` -> `lpart_dual_usage_guard`. Disk-mutating pre/part/post FIELD content
  (mkfs/sgdisk/parted/mdadm/wipefs/lv*/vg*/pv*/blkdiscard/dd-of=/dev) under an lpart install_mode
  now BLOCKS with exit 2 (safety refusal -> fatal park); custom NON-disk content still warns;
  literal `lpart-allow-custom` marker = explicit operator escape. Deployed to the controller
  daemon (site-packages template; backup `.pre-nb2.bak`) + staged bundle updated. Pinned:
  `scripts/dev/dual-usage-guard-test.sh` (8 cases). **LIVE:** legacy parted+mke2fs part field ->
  `BLOCKED` -> rc 2 -> `INSTALL FAILED FATALLY -- NOT retrying`, vda writes = 0.
- **NB-5 (MCS-17) — efivars-absent is loud + verified.** `nvram.go`: the efivars-skip (was a
  SILENT return nil) now logs a loud WARN. `verify.go`: a MULTI-disk mirrored-boot install whose
  image lacks efibootmgr now FAILS verification (per-leg NVRAM entries are the verified failover
  property); single-disk keeps the tolerant skip; register_nvram=no is the opt-out. Pinned:
  `TestVerifyRedundantBootRequiresNVRAMTooling`.
- **NB-1 (MCS-18) — sanitize success contract.** `lpart-storage-prepare`: an empty result set is
  SUCCESS for install_mode=sanitize (exit 0); all other modes keep the fail-closed WriteAuto
  error. Pinned: `TestSanitizeWithNoResultsExitsZero`.
- **BE-S7 — NEW seam bug found live by NB-1 (CCF-7 strikes a THIRD time).** The moment sanitize's
  part phase exited 0, the POST phase failed: `runPost` required storage-result.json BEFORE
  loading the install mode, so sanitize (no results by design) died at the precheck -> retry
  loop. Fixed (`internal/pipeline/pipeline.go`: mode resolved first; non-bootloader modes skip
  the finalise cleanly); pinned `TestPostPhaseSkipsBootloaderForSanitize`. **LIVE end-to-end
  sanitize now:** erase x3 -> part rc 0 -> post `no bootloader finalise for install_mode=sanitize`
  -> `install.sh exit status: success (rc=0)` -> node stops (dracut emergency shell; nothing to
  boot after an erase -- the intended 3.13 "then stop" state). Regression 3.13 minor finding
  RESOLVED.
- Deploys: binaries into compute-image + 2 repacks; template into the daemon. FTA-v2 matrix +
  MCS-13/17/18 statuses updated to CLOSED with the live evidence; LOOSE-ENDS sanitize item
  marked fixed. node001 restored to a healthy auto install afterwards.

Remaining recommendations (unimplemented, tracked in FTA-v2 §8.2): NB-3 (seam contract as a
release gate), NB-4 (device-name chokepoint audit), NB-6 (streaming extract), NB-7 (95luna
sshd_config -- the exact errors are visible in the nb2block screenshot), NB-8 (v1 leftovers
BE-F7/G3/G4).

## 2026-07-12 (cont.) — remaining FTA v2 recommendations implemented (NB-3/4/6/7/8)

All remaining barriers from docs/FTA-v2.md §8.2, each pinned; make-check gates green throughout.

- **NB-8 (v1 leftovers).** Re-verification CORRECTED two stale statuses: BE-G3 (sub-tool
  timeout) was ALREADY closed (`Options.StepTimeout` + context.WithTimeout, pipeline.go:414);
  BE-G4's options-drop was already fixed (mountIfNeeded honours per-fs options). Newly closed:
  **BE-F7** — `lpart-storage-check` now REFUSES `--dry-run` outright (its probes are already
  read-only; stubbing them fabricates verdicts; pinned `TestDryRunRefused`); **BE-G4/G5
  residual** — the synthesized memboot tmpfs is now `size=80%` (50% default stalled an 8GB
  node on a 4.4GB image).
- **NB-4 (CCF-8 chokepoint).** Audit: no ad-hoc partition-path construction exists outside
  `internal/devices` (it IS the chokepoint). The v2 validator now refuses `/dev/mapper/*` and
  `/dev/dm-*` set devices fail-closed (dm partition naming has no reliable convention) and
  style-classifies SINGLE-device sets (previously skipped even the /dev/ check — latent hole
  closed). Pinned `TestDeviceMapperPathsRejected`.
- **NB-3 (CCF-7 gate).** `scripts/dev/seam-contract-gate.sh`: fetches the RENDERED install
  script from the live daemon (token via the controller's luna.ini; https 7050 /token ->
  http 7051 /boot/install/<node>), verifies `exit $LUNARET` x4 / no `exit 1` collapse /
  dual-usage guard present AND behaviourally blocking (executes the rendered function) /
  wrapper park codes in the deployed 95luna. Documented as MANDATORY per release in
  regression.md §4a. First live run: 7/7 PASS.
- **NB-6 (BE-G5 streaming extract).** `osimage.StreamHTTPExtract`: single `sh -c "curl | tar"`
  under pipefail — no tarball on disk (the initramfs /tmp is tmpfs, so the classic path's
  tarball costs RAM on EVERY install; memboot peak was tarball+tree). curl `--retry` is
  deliberately omitted inside the pipe (mid-stream retry corrupts the tar stream); watchdog
  connect/stall flags kept. `--stream-extract auto|always|never` on lpart-osimage-install:
  auto streams ONLY tmpfs sysroots (memboot) — disk installs keep the payload-in-hand-before-
  wipe ordering — and any stream failure falls back to classic download+extract in the same
  run. Pinned `internal/osimage/stream_test.go` (pipe shape, no-retry, stdin-tar, os-release
  proof, quoting). **LIVE on node001:** `stream_extract=true` -> `streaming osimage extract
  (curl | tar)` -> `osimage streamed into /sysroot successfully` in 75s (4.4GB), tmpfs root
  boots. RAM peak halved.
- **NB-7 (BE-S6 installer sshd).** THREE stacked faults fixed in `95luna` (all 5 distro trees +
  the deployed image module): (1) deprecated `UsePrivilegeSeparation sandbox` removed from
  sshd_config; (2) `inst_dir /usr/share/empty.sshd` (el9 compiled-in privsep dir, sshd refuses
  to start without it); (3) `inst_multiple -o /usr/libexec/openssh/sshd-session` (OpenSSH >=9.8
  split; found live after the first two were fixed — each fix peeled the next error).

## 2026-07-12 (cont.) — regression catalogue expanded: §3B (3.26–3.61), 36 new tests

Reviewed the whole 2026-07-11/12 campaign (FTA v2 event register, the NB-1..8 barrier round,
the 7 live-found bugs, the UEFI/OVMF mechanics, the remaining gap register) and added a new
regression block to regression.md: **36 deep on-node tests, 24 destructive**, each traced to
its FTA event (BE-*/MCS-*/CCF-*) so a failure maps straight back to the tree. Highlights:
- **Redundant-boot matrix**: both-legs failover + rebuilt-leg-boots (3.28), raid5 pull (3.29),
  raid6 DOUBLE pull (3.30), raid10 first-ever run (3.31), resync under power loss (3.32),
  NVRAM-cleared removable-fallback boot (3.27).
- **BR-9 gap closable in-lab (3.41)**: QEMU `<serial>` gives virtio disks real
  /dev/disk/by-id names → by-id pinned installs + a serial-swap test proving identity
  follows the data. The long-standing "needs real HW" item no longer does.
- **Seam class live**: exit-class park matrix (3.35), retry config-lifetime byte-identical
  via installer-SSH (3.36), dual-usage 12-way matrix incl. the escape marker (3.37), seam
  gate as scheduled regression (3.38), kmsg flood (3.39), parked-installer incident loop (3.40).
- **Data-fence deep**: discover-pressure on a persistent smallest disk (3.45), spanning
  VG/md refusals live (3.46/3.47 — unit-only until now), dirty-log read-only classify with
  sysrq-b (3.48), guard-tool sabotage via installer-SSH → fail-closed (3.49), persistent
  across auto+sync (3.50) and as a RAID1 data set (3.51), sanitize lifecycle + blank-proof +
  reprovision (3.52) and the refusal→operator-resolution flow (3.53).
- **Streaming/availability**: 8GB-node memboot (3.54), mid-stream network cut → fallback
  (3.55), override matrix + torrent preference (3.56), streamed preserve-filter (3.57),
  watchdog stall timing (3.58).
- **Re-entrancy under power**: 3-stage power-yank matrix (3.59). Plus group→node v1.4 field
  cascade (3.60) and the boot-order contract pin (3.61).

## 2026-07-12 (cont.) — §3B regression execution (first pass): 6 tests, 3 findings

Ran a distinct-class subset of the new §3B catalogue live on node001 (UEFI q35/OVMF). Results
tracker: /root/lpart-uefi-work/RESULTS.md.

PASS (5, one per class):
- **3.61 boot-order contract** — 3 reboots same os-root UUID (disk boot, no reinstall); blank vda
  → iPXE fallthrough → reinstall new UUID. Both directions.
- **3.38 seam gates** — dual-usage 8/8 + seam-contract-gate 7/7 vs the live daemon.
- **3.44 unsupported device styles** — /dev/mapper, /dev/dm-, no-/dev-prefix all refused at
  config_validation, exit 1 retry, wr_bytes=0 (NB-4 live).
- **3.35 exit-class matrix** — exit-1 nvme resolve-halt (wr=0) → push good layout → loop
  re-fetched + installed WITHOUT reboot (BE-S2/CCF-7 config re-read). exit-2→3 park via NB-1 work.
- **3.28 RAID1 both-legs failover + rebuild + pull-other** — the crown jewel (v1 §3.4b was
  PARKED): install→kill vda→boot vdb→rebuild vda→scrub mismatch=0→kill vdb→boot REBUILT vda.

by-id premise PROVEN: QEMU <serial> gives virtio disks /dev/disk/by-id/virtio-LPARTx-part{1,2,3}
links → the "BR-9 needs real hardware" excuse is dead; by-id resolution + PartitionPath -part<N>
re-proven live (installer Target=LPARTA001→vda, partitioned).

FINDINGS (3):
1. **Rebuilt-RAID-leg NVRAM staleness** — after a leg rebuild with `sgdisk -G` (new ESP
   PARTUUID), the leg's named `TrinityX <disk>` NVRAM entry goes stale; the node boots via the
   removable fallback (BE-W3/W4 mitigation working) but `efibootmgr` shows the entry unrefreshed.
   Operational note for RAID-rebuild runbooks; not a bug.
2. **BE-A6b re-entrancy gap** (added to FTA-v2) — a stale same-name `os` VG on a NON-layout disk's
   md array (from a prior different-topology install) collides with the new lvcreate → exit 5.
   purgeVGsByName removes duplicates BY UUID but only ones `vgs` can see; an md array kept
   disassembled by EnsureNoAutoAssemble hides its VG until a later assembly surfaces it. Narrow
   but silently fails the install. Recommended barrier NB-9 (analysis-only, in FTA-v2 §6.3a).
3. **lvcreate exit 5 on the 5-disk+serial VM topology** — REPRODUCIBLE even with all disks blank;
   the IDENTICAL binaries/config install cleanly on 3 disks (3.2/3.28/3.61), so it is a topology-
   triggered LVM/env interaction (likely duplicate-PV/device-scan with the extra serialized
   disks), NOT lpart code. Needs a non-retry-looping installer to root-cause. Tracked for
   follow-up; blocks the 4/5-disk tests (3.29/3.30/3.31 raid5/6/10, 3.41 by-id) until resolved.

Lab restored: node001 back on the 3-disk topology, healthy auto/discover install. Remaining ~30
§3B tests are scripted + documented in regression.md, ready to run (the 4/5-disk ones gated on
finding #3). Protected trx cluster untouched throughout.

## 2026-07-12 (cont.) — "all of it": root-caused #3 → BE-A6c real-HW ship-blocker FIXED; NB-9 done

Delivered all three follow-ups.

(a) **ROOT-CAUSED the lvcreate exit-5 finding → BE-A6c, a real-hardware SHIP-BLOCKER.** On a
memboot node I reproduced the exact install LVM sequence: `pvcreate /dev/vda3` on a disk with a
serial warns `idname LPARTA001 already used for /dev/vda`; `vgcreate os` "succeeds"; `lvcreate`
→ `Cannot use /dev/vda3: device is not in devices file` → rc 5. Cause: the el9+ **LVM devices
file** keys devices by `IDTYPE=sys_serial`; a PARTITION inherits its disk's serial → idname
collision → the VG-on-partition is unusable. **The virtio lab masked it for weeks** (default
virtio disks have no serial); it hits 100% of real HPC hardware (all disks have serial/WWN).
FIX: `EnsureNoLVMAutoActivate` now also writes `devices { use_devicesfile = 0 }` to the
installer lvmlocal.conf (correct for a controlled wipe-all installer). Verified on-node (rc 0)
and end-to-end: the by-id serialed install now writes 5.4GB + boots. Pinned by the extended
`TestEnsureNoLVMAutoActivate`. Added to FTA-v2 §6.3b. **Highest-severity find of the campaign.**

(c) **NB-9 (BE-A6b) implemented.** `lvm.Build` now purges any stale same-name VG by UUID
immediately before vgcreate (a duplicate `os` from a prior different-topology install would make
lvcreate ambiguous → exit 5). Leaves unrelated VGs untouched; runs after pvcreate on the fresh PV.
Pinned `TestBuildPurgesStaleSameNameVG`. FTA-v2 §6.3a updated to CLOSED.

(b) **§3B execution** — 6 tests: 3.61/3.38/3.44/3.35/3.28 PASS + 3.41 PASS-after-fix. The 4/5-disk
RAID tests (3.29/3.30/3.31) are now UNBLOCKED by the BE-A6c fix (node has 5 serialed disks).

make check green throughout (gofmt/vet/lint 0/tests, excl the host-only pinback artifact).
Deployed: storage-prepare (BE-A6c + NB-9) into the compute-image, repacked. node001 healthy on a
UEFI by-id serialed install. Protected trx cluster untouched.

## 2026-07-12 (cont.) — §3B parallelized across node001-004; campaign close-out

Converted node002/003/004 to UEFI (q35+OVMF+serials, 3/4/5 disks) and ran the remaining §3B
tests in PARALLEL across all four nodes (should have done this from the start).

Results:
- **3.37 dual-usage guard — LIVE PASS on real legacy nodes.** node002/003/004 still carried the
  demo cluster's legacy mke2fs/parted partscripts; NB-2 BLOCKED all three (exit 2 → fatal park)
  until the lpart shim was set. Unplanned real-world validation of the hard block.
- **3.50 persistent survives auto — PASS** (marker intact after auto reinstall, node002).
- **3.45 discover-pressure — PASS** (persistent disk avoided by discover; marker survived full
  reinstall, node001).
- **RAID degraded boot (definitive):** raid1 boots degraded (3.28, clean). Parity raid5/6 FAIL at
  `grub>` even when the boot disk survives (definitive clean check) — a real GRUB-degraded-parity
  limitation (grub.cfg already fs-uuid-based, so not the raid1 rewrite). raid10 (mirror) expected
  to work; clean confirm is a follow-up. Guidance: raid1/raid10 for boot-fault-tolerant OS sets.
  The parallel driver's "degraded booted" reads were UNRELIABLE (no NVRAM reset before the pull →
  booted stale installs), corrected in RESULTS.
- **Harness lessons (all fixed):** reg-helpers blank() only recreated vda/vdb/vdc (vdd/vde kept
  leftover ESPs → OVMF booted them → grub>); OVMF NVRAM VARS accumulates stale TrinityX entries
  across reinstalls (trap boot before iPXE) — reset the VARS per destructive reinstall.
- **Open follow-ups (harness-noisy, not product-confirmed):** 3.52 sanitize re-test (erase proven
  in 3.13); 3.59 clean power-yank ×3 recovery FAILED (whether 3 back-to-back yanks leave
  unrecoverable md/LVM state or it's harness needs a focused single-yank re-test); raid10 clean
  degraded confirm; raid5/6 degraded-boot FIX (deeper GRUB work). All tracked in LOOSE-ENDS.

All four nodes restored to healthy single-disk UEFI installs. Full §3B tracker + per-node logs in
/root/lpart-uefi-work/ (RESULTS.md, RESULTS-n{1..4}.txt).

## ════ SESSION HANDOVER — 2026-07-12 (resume point) ════

**▶ Full handover: `/root/lpart-uefi-work/HANDOVER-2026-07-12.md`** (lab state, creds, harness,
what's open). This entry is the pointer + the one-screen summary.

**DONE this campaign (3 local commits on `advanced-partitioner-stable`, NOT pushed):**
- `d29ea3a` remove BIOS / enforce UEFI at a firmware gate.
- `74323e4` FTA-v2 fixes+barriers incl. **BE-A6c** (el9 LVM devices-file broke partition-LVM on
  ALL real serialed disks → lvcreate exit 5 → every disk install failed; masked by the virtio lab;
  fixed via `use_devicesfile=0`). Also NB-1/4/5/6/8/9, PartitionPath by-path fix, seam fixes.
- `0557106` FTA-v2.md, seam-contract gates, 95luna sshd fix (NB-7), +36-test regression §3B.
- make check GREEN (excl. the host-only `TestBuildPinnableConfig_DiscoverBecomesManual` artifact).

**LAB now:** node001-004 are UEFI (q35/OVMF/serials, 3/3/4/5 disks), all restored healthy.
Controller `ssh root@192.168.123.217` (luna/test2026). compute-image + guarded template deployed.
go1.25.11 at /usr/local/go. Harness at /root/lpart-uefi-work (nreg.sh — parameterized; two harness
bugs fixed: blank-all-disks + reset-NVRAM-per-reinstall). **Parallelize across all 4 nodes.**

**OPEN (also in LOOSE-ENDS):**
1. Parity RAID (raid5/6) degraded boot → `grub>` (CONFIRMED real GRUB limit; raid1 works). Fix =
   deeper grub work. raid10 clean degraded-confirm pending (expected OK, mirror-based).
2. 3.59 power-yank ×3 clean recovery failed once (focused re-test; not a permanent brick).
3. 3.52 sanitize re-test (erase proven in 3.13); remaining §3B tests (~20) not yet run.
4. Daemon MR not upstream (staged /root/luna2-v1.4-patches + v14-daemon-reconciled).
5. Push commits + open MR when GitLab wired.

---

## SESSION 2026-07-12 (v2) — installer deliverable + lpx campaign + parity-boot characterisation (BE-B10)

Ran on a NEW cluster **lpx** (golden3-clone ctrl 192.168.123.148 + 4 UEFI nodes 3/3/4/5 disks),
stood up and wired **entirely by the new one-command installer** `/root/trinityx-lpart-install`
(the "gitlab download + run" self-contained deliverable). Full results:
`/root/lpart-uefi-work/RESULTS-campaign-2026-07-12.md`.

### Headline: parity/raid10 degraded boot (event BE-B10) — CHARACTERISED; v1 assessment stands; stub ruled out
The 2026-07-12(v1) handover called raid5/6 degraded-`grub>` a "real GRUB degraded-parity limit".
This session tested that hypothesis to destruction:
- **NEW: raid10 degraded ALSO fails at `grub>`** (not just raid5/6) — so the affected set is
  every non-raid1 degraded OS array. raid1 degraded boots fine.
- **The ESP stub is NOT the failure point.** Instrumenting the stub (serial + per-line echoes)
  proved it runs cleanly under degraded parity: `search --fs-uuid` finds `/boot` on a raid1
  member and `configfile` is reached (diag3: "search dev=(hd0,gpt2)" → "configfile returned").
- **The MAIN `/boot/grub2/grub.cfg` is where degraded boot fails, NON-DETERMINISTICALLY.**
  Same raid6-degraded config, different failure each run: diag3 the main cfg ran-and-fell-through;
  diag4 the main cfg never executed (its prepended echo never fired). The main cfg already uses
  `terminal_output console` (not gfxterm — that hypothesis ruled out). This matches the v1
  observation that GRUB's degraded-array resolution is intermittent.
- **A candidate stub-insmod fix was implemented, HW-tested, and REVERTED** (e8aa7af
  reverts 43d00e1): regression 3.62 on real binaries + diag3/diag4 showed the stub insmods do
  not fix it (the stub was never the failure). The one d2 "boot" was the non-deterministic
  degraded assembly succeeding by chance, not the insmods.
**Mitigation (NB-10, the diagnose-and-mitigate deliverable):** verify-time WARN + docs —
`raid1` (NOT raid5/6/10) for a degraded-bootable OS set; parity/striped OS arrays boot only
while healthy. The real fix is deeper GRUB debugging of the main-cfg degraded path, tracked open.

### Also landed (branch advanced-partitioner-stable, LOCAL — not pushed)
- ff417d6 five FTA-v2 follow-ons: mdadm `--homehost=any` (BE-N4), disk-fit preflight (BE-G6),
  duplicate/empty-serial WARN (BE-N5), Debian degraded-safe grub.cfg (BE-W5), NVRAM
  stale-entry label-variant pins (BE-W6).
- NB-9 (BE-A6b) stale cross-array VG purge: confirmed already implemented (74323e4) + pinned;
  FTA-v2 reclassified CLOSED; lab test 3.65.
- 9d02b10 fixed the host-only red test (byIDDir seam) → `make check` GREEN on bare metal.
- 015e1df FTA-v2 new events + regression 3.62-3.65.
- 9a55504 cluster-parameterized harness (nreg CLUSTER=<name> + reg-orch.sh).

### Harness lessons (all prior flaky §3B results were harness, now fixed)
- **Install kernel needs `console=ttyS0,115200n8`** (`luna osimage change -qo`) or NO installer
  output reaches serial — the old trx "3.52 blkdiscard=0" inconclusive was this.
- **lpart-internal text logs to the CONTROLLER's `/var/log/cluster-messages/<node>.messages`,
  NOT the serial console** (the TUI eats stdout). Use that channel for erase/refusal detection.
- `console_follow` (tail -F) survives the `append='off'` truncation at the installer's
  auto-reboot; `install_clean` enforces reset_nvram+blank-all; `degraded_pull` refuses without a
  preceding clean install (kills the stale-NVRAM false-read that produced v1's unreliable reads).
- Sanitize/reinstall in the VM lab must **pre-blank vda to force netboot** — an installed disk
  boots via the ESP removable-path even after reset_nvram, so the sanitize installer never runs
  otherwise. (On real HW the operator triggers reinstall via BMC/PXE.)

### Installer bugs found + fixed during the clean-install validation (WP-C)
daemon-ini discovery on stock layout; lever patch regenerated -p1; luna flag names
(-qpre/-qpart/-qpost); "Nothing is changed" = idempotent success; deploy the whole 95luna
dracut module (carries the LUNA_SAFETY_RC/FATAL_RC park codes the seam gate checks) not just
binaries; module 50 self-heals the missing `wheel` package for the lchroot build.

### Verdicts (see RESULTS-campaign-2026-07-12.md for the full table)
PASS: baseline×4; raid5/raid6 healthy; 3.59 single power-yank ×3 (partition/extract/bootloader)
all recovered; 3.52 sanitize NB-1 contract (erase 3, completed, 0 retries). Diagnosis: stub proven NOT the failure point (diag3/diag4); parity/raid10
degraded boot fails non-deterministically in the MAIN grub.cfg (v1 assessment stands, extended
to raid10). The stub-insmod fix was tested + reverted. Post-redeploy confirmations + remaining §3B: see the results file + machine
table `evidence/2026-07-12-campaign/RESULTS-campaign.md`.

---

## SESSION 2026-07-12 (v3) — multi-distro (lchroot builds) + wired regression + cross-distro UEFI findings

Full report: `advanced-partitioner/RESULTS-multidistro-2026-07-12.md`. Delivered the
lchroot-based multi-distro image-build tooling + a packaged/wired-in destructive regression
suite, and made the FIRST attempt to run lpart on Ubuntu/SUSE under UEFI (all prior distro passes
were BIOS). Honest, valuable outcome: a chain of real findings, each advancing the install.

- **lchroot ansible integration (the "based on lbwrap" ask) — WORKS + validated.** A real Ansible
  connection plugin runs the in-image phase inside `lchroot --path <img>` (bwrap); the build roles'
  raw `chroot {{image_path}}` calls + dynamic_hosts connection were patched to lchroot (backed up).
- **Per-family lpart wiring (installer module 10/30) — all 3 families carry lpart** (ubuntu got
  dracut via lchroot). **Regression suite (module 70) packaged + validated** (manifest + shape-
  scheduled runner + roll-up md/json/junit, seam gate through it).
- **Go: BE-U1** auto-plain-mode fallback when the terminal is 0x0/unsized (serial installs) — +test,
  make check GREEN. create_deb now stages all 7 bins; ubuntu/opensuse luna-installer.sh synced to
  redhat's LUNA_SAFETY_RC park half.
- **Cross-distro UEFI findings (all real, mostly OPEN):**
  - **Ubuntu**: lpart NOT in the install initramfs — luna packs ubuntu with `mkinitramfs`
    (initramfs-tools), so the committed **dracut 95luna module is ignored**. Fix = an
    initramfs-tools hook (substantial). This means Ubuntu lpart install never worked end-to-end
    under the current luna despite the wired dracut tree.
  - **openSUSE (SLE 15 SP5)**: lpart now runs DEEP (firmware gate + partition + mdadm), fixed two
    gates along the way (TUI-on-0x0 → BE-U1 + notui; missing jq/sgdisk → installed via lchroot),
    now stops at `pvcreate /dev/vda3 exit 5` — a SUSE variant of BE-A6c (lvm devices-file).
  - **Rocky 10**: `alternative_distribution=Rocky-10` bootstraps the controller's el9 (dnf-
    installroot uses the controller release); a cross-major el10 image needs a container source —
    **no podman/docker on this host** → BLOCKED (el10 repos themselves are reachable).
  - **ubuntu build**: `alternative_distribution=Ubuntu-24` wrongly triggered osgrab; corrected to
    `ubuntu_distribution_release=noble` (plain debootstrap).

### 2026-07-12 (v3) retirement — lpx torn down; NEXT-CHAT-START.md is the resume point
lpx cluster (ctrl + 4 UEFI nodes + lpx-prov net + overlays/scratch/NVRAM/console logs + /etc/hosts
lpx.lab) fully removed; trx + all 6 customer demo clusters untouched (36 non-lpx domains still
running). All work committed to both repos (advanced-partitioner-stable + the trinityx-lpart-install
repo); NOT pushed (GitLab unreachable). Next chat rebuilds a fresh golden3 ctrl + 4 UEFI trx nodes
via `trinityx-lpart-install/kvm-lab/lab-up.sh` and fixes the OPEN POINTS (Ubuntu initramfs-tools
hook; SUSE pvcreate exit-5; Rocky10 container-free el10 build; ubuntu extend-phase) BEFORE further
regression. Full plan + new test ideas: `/root/lpart-uefi-work/NEXT-CHAT-START.md`. Rocky10 note
CORRECTED: podman is NOT required — `dnf --installroot` builds el10 natively from an el9 host
(parameterize base.yml release-RPM + releasever); container source is only the stock alternative.
