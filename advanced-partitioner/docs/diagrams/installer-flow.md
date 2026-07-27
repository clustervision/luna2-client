# TrinityX installer flow — companion notes

Generated 2026-05-23. Source of truth: `docs/diagrams/installer-flow.mmd` in the repo.

The diagram in `installer-flow.mmd` describes the runtime behaviour of the
Go installer (`cmd/lpart-*`) when driven by Luna 2 + the partscript that
ships in `runtime/examples/`. Every claim below carries a `file:line`
citation so a reviewer can confirm the diagram matches the code without
re-running the inventory.

> **Finding up front:** the skill skeleton assumed three JSON files. The
> live flow has **seven**. Four come from Luna (inlined by `install.sh`),
> three are produced during the run and consumed downstream — and one of
> the Luna-supplied ones (`storage.json`) is *also* a possible installer
> output when `lpart-discover` runs. None of this can be collapsed without
> losing the routing logic.
>
> **Also retired since the previous regeneration (2026-05-23):** the
> prescript's `touch /lunatmp/skip_unpack` step. The Phase 14f patch to the
> lpart install template (`templ_install_lpart.cfg` — renamed 2026-06-07 from
> `templ_install.cfg`, which now holds the legacy/original install.sh)
> replaced `download_image` and `unpack_imagefile` with no-op stubs, so the
> marker is no longer read by anything in the lpart path. The diagram omits it.
> (The legacy `templ_install.cfg` restores those real bodies — see LEGACY.configs.)

---

## JSON inputs and outputs (7 files)

### Luna-supplied (written by `install.sh` template from first-class fields)

The Phase 14 template patch inlines five first-class Luna fields
(`disklayout`, `discover_config`, `osimage_filter`,
`install_mode`, `osimage_sync`) into four JSON files under
`/tmp/install_files/runtime/` *before* the partscript runs. See
`BOOKKEEPING.md` §C Phase 14e–f.

#### 1. `install-context.json`
- **Source:** Luna `install_mode` + `osimage_sync` enums, written by
  `install.sh`'s `write_lpart_inputs` (Phase 14f).
- **Schema:** `internal/installcontext/types.go:93-94` (file form),
  `internal/installcontext/types.go:106-107` (resolved form).
- **Direction:** read by every binary that takes `--install-context`;
  re-written by `lpart-install-context` after resolution
  (`cmd/lpart-install-context/main.go:65`).
- **Default path:** `/tmp/install_files/runtime/install-context.json`
  (`internal/installcontext/load.go:13`).
- **Allowed values:** `install_mode ∈ {auto, mem, force-init}`,
  `osimage_sync ∈ {Sync, Full, Local}`. Phase 14c renamed the install-mode
  enum (was `{mem, init, existing}`).
- **Resolution precedence:** explicit CLI flag > file > defaults (3-source;
  the env-var + `/proc/cmdline` carriers were removed in Phase 14c — see
  `internal/installcontext/load.go:42-78`).

#### 2. `storage.json`
- **Source:** Luna `disklayout` field (b64) when populated; otherwise
  empty (partscript triggers `lpart-discover` or defaults to memory-root).
- **Schema:** `internal/config/v2/types.go:25` (`Config`) +
  `internal/config/v2/types.go:50` (`Set`). The `safety.persistent`
  per-set flag is at `internal/config/v2/types.go:80`.
- **Direction:** read by `lpart-storage-prepare --config`
  (`cmd/lpart-storage-prepare/main.go:77-78`) and `lpart-storage-check
  --config` (`cmd/lpart-storage-check/main.go:103`). **Also written** by
  `lpart-discover --out` (`cmd/lpart-discover/main.go:72`).
- **Updated:** never by `lpart-storage-prepare`; written on Path A
  (discovery) by `lpart-discover`.

#### 3. `discover-config.json`
- **Source:** Luna `discover_config` field; **only consulted** when
  the partscript routes to `lpart-discover` (i.e. `install_mode=force-init`
  or `install_mode=auto` with empty `disklayout`).
- **Schema:** parsed by `intent.LoadDiscoverConfig` referenced at
  `cmd/lpart-discover/main.go:126`.
- **Direction:** read only.
- **Required:** for `install_mode=force-init` (partscript fails fast if
  empty — see historical `runtime/examples/partscript.sh` `git show
  fbdf6f3:runtime/examples/partscript.sh`).

#### 4. `osimage-filter.json`
- **Source:** Luna `osimage_filter`; consumed only when
  `osimage_sync=Sync`.
- **Direction:** read by `lpart-osimage-install --config`. Resolved into a
  tar `--exclude` list at the `filter_resolve` stage (the resolved list
  is logged via `logger.KV("tar_excludes", …)` per `BOOKKEEPING.md` §G
  2026-05-20 entry).
- **Promoted to no-op** when `storage-result.json` reports
  `first_install=true` — see §D.11 in `BOOKKEEPING.md`.

### Installer-produced (consumed downstream)

#### 5. `storage-result.json`
- **Written by:** `lpart-storage-prepare`. Default path:
  `defaultRuntimeDir + "/storage-result.json"` =
  `/tmp/install_files/runtime/storage-result.json`
  (`cmd/lpart-storage-prepare/main.go:77`).
- **Schema:** `internal/result/result.go` (root) + per-set entry has
  `Persistent` (`internal/result/result.go:85`) and the
  `FirstInstall` flag set when `--wipe && !--dry-run`
  (added 2026-05-21; see BOOKKEEPING §G).
- **Read by:**
  - `lpart-osimage-install` at `cmd/lpart-osimage-install/main.go:94` (default
    `--storage-result` path), used both for FirstInstall promotion and
    to union persistent-set mountpoints into the tar excludes.
  - `lpart-bootloader-finalise` at `cmd/lpart-bootloader-finalise/main.go:71`
    (`--result`).
- **Shape:** single object for one set, `{"version":2,"sets":[…]}` for
  multi-set (see BOOKKEEPING §D.2 and the
  `write_result_output` decision).

#### 6. `storage-resolved.json`
- **Written by:** `lpart-storage-prepare` at
  `cmd/lpart-storage-prepare/main.go:78` (`--resolved-out`).
- **Direction:** consumed by the postscript and POSTed back to Luna as
  `disklayout_b64` so the resolved layout is pinned for subsequent
  reboots (`runtime/examples/postscript.sh` historical, accessible via
  `git show fbdf6f3:runtime/examples/postscript.sh`).

#### 7. `boot-context.json`
- **Written by:** `lpart-bootloader-finalise` at
  `cmd/lpart-bootloader-finalise/main.go:60` (default
  `--boot-context-out`).
- **Direction:** terminal output; not currently re-read by any binary
  (operator/diagnostic artifact).

---

## Decision points

### Install-mode routing (partscript-level)
- **Selector:** `install_mode` from `install-context.json` (resolved by
  `lpart-install-context`).
- **File:** historical `runtime/examples/partscript.sh` (deleted in
  commit `079610f`; latest version recoverable via `git show
  fbdf6f3:runtime/examples/partscript.sh`).
- **Branches:**
  - `mem` → skip `lpart-discover`; either use any existing `storage.json`
    or write a memory-root tmpfs default inline.
  - `force-init` → require `discover-config.json` populated; run
    `lpart-discover` regardless (re-pin).
  - `auto` → if `storage.json` populated, use as-is; elif
    `discover-config.json` populated, run `lpart-discover`; else memory-root
    default.
- **Refusal contract:** `lpart-discover` refuses `install_mode != auto &&
  install_mode != force-init` (`cmd/lpart-discover/main.go:115`). Override
  with `--ignore-install-context`.

### `osimage_sync` routing (partscript-level)
- **Selector:** `osimage_sync` from `install-context.json`.
- **Branches:**
  - `Sync` → `lpart-osimage-install --config osimage-filter.json` (filter
    list active, subject to FirstInstall promotion).
  - `Full` → `lpart-osimage-install` (no filter).
  - `Local` → `lpart-storage-check` (no extract — verifies existing layout).
- **Refusal contract:** `lpart-osimage-install` refuses
  `osimage_sync=Local` (BOOKKEEPING §D.10);
  `lpart-storage-check` warns when `osimage_sync != Local`.

### FirstInstall promotion (Sync → Full)
- **Condition:** `storage-result.json` reports
  `first_install=true` AND `install-context.osimage_sync=Sync`.
- **Effect:** `--config` cleared, `--preserve`/`--preserve-empty`/
  `--no-new-files` zeroed; logged as `first_install detected from
  storage-result.json -- Sync filter ignored, extracting as Full`.
- **Why:** the filter list is only meaningful when there is prior
  operator state to preserve. On a wiped first install (or memory-root)
  preserving `var/spool/mail` etc. blocks legitimate image content from
  extracting. Driven by node001 mem-install 2026-05-21 (BOOKKEEPING §G).
- **Multi-set:** ANY constituent set with `first_install=true` triggers
  the promotion (BOOKKEEPING §D.11).

### Persistent fast-path (per set)
- **Selector:** `set.safety.persistent: true` in `storage.json`.
- **Effect in `lpart-storage-prepare`:** skip cleanup + partition + raid +
  lvm + fs entirely; only `fsmod.PredictFilesystemsForSet` + mount run
  (`cmd/lpart-storage-prepare/main.go:368-385`). `FirstInstall` forced false
  for persistent sets even with `--wipe`.
- **Effect in `lpart-osimage-install`:** every persistent set's mountpoints
  are added to the tar excludes as `--exclude=./<mp>` +
  `--exclude=./<mp>/*` in **every** mode (Full, Sync, Local). Defensive
  post-check exits 2 with `safety_refusal=persistent` if a mountpoint
  somehow missed (BOOKKEEPING §D.12).
- **Effect in `lpart-storage-check`:** health failure exits 2 via
  `cli.ExitSafetyRefusal` with `safety_refusal=persistent` marker.
- **Effect in `cleanup.Cleaner`:** short-circuits **before any probe
  runs** when `Set.Persistent=true`
  (`TestCleanupSelectedSetPersistentSkipsEntirely`).

### Bootloader firmware mode (`uefi` vs `bios`)
- **Selector:** `bctx.FirmwareMode` derived from `storage-result.json`
  (`firmware_mode` field).
- **RHEL switch:** `internal/bootloader/rhel.go:104-114`
  (`Install` → `installBIOS` vs `installUEFI`).
- **Debian switch:** `internal/bootloader/debian.go` (same shape).
- **BIOS path:** `grub2-install --no-floppy <disk>` per
  `bctx.BootDevices` in chroot (`internal/bootloader/rhel.go:133`).
- **UEFI path:** `StageSignedShim` copies the distro's signed
  `shimx64.efi` + `grubx64.efi` from `<ESP>/EFI/<distro>/` to
  `<ESP>/EFI/BOOT/`, writes the BOOTX64.CSV with a TrinityX label
  (cosmetic on shim 15.8 — BOOKKEEPING §F). The legacy
  `grub2-install --target=x86_64-efi` rebuild was retired in commit
  `079610f` because its auto-detected module set was too thin (5
  modules, missing `normal.mod` → rescue prompt) — see BOOKKEEPING §G
  2026-05-21 "GRUB rescue fix landed".
- **Family dispatch:** `bootloader.Adapter` interface; `New` picks
  rhel/debian/suse by detected `Family` in `bctx`. SUSE adapter is a
  stub that fails fast (BOOKKEEPING §G).

### `ClearUEFINVRAM` gate
- **Selector:** `bctx.ClearUEFINVRAM` from `storage-result.json`.
- **Effect:** deletes UEFI boot entries whose label does NOT contain
  `PXE` / `Network` / `Luna` / `iPXE` (BOOKKEEPING §D.6). Then registers
  fresh TrinityX entries per `BootDevice` and reorders `BootOrder` so
  TrinityX is first.
- **Bind-mount caveat:** efibootmgr runs in `chroot /sysroot`, so
  `/sys/firmware/efi/efivars` must be visible inside. Uses
  `mount --rbind` + `mount --make-rslave` per dir
  (`internal/bootloader/bindmount.go`); plain `mount --bind` does NOT
  carry the efivarfs submount and silently writes to a sysfs-backed
  empty dir (BOOKKEEPING §G 2026-05-21 entry). `--make-rslave` keeps
  propagation one-way so `umount -R` on cleanup doesn't drag the
  install env's own efivars down.

---

## External tools by stage

Every subprocess goes through `executor.Runner.Run(ctx,
executor.CommandSpec{...})`. The only sanctioned direct
`exec.CommandContext` site is `internal/executor/executor.go:186`; the
TUI shell handoff (`internal/tui/shell.go:17`) and the engine's child
orchestration (`internal/engine/command.go:63`,
`internal/engine/runner.go:61`) are also direct but live outside the
storage/boot flow.

| Stage | Tool | Call site |
| --- | --- | --- |
| Toolbox fetch (prescript) | `curl` + `tar` | historical `runtime/examples/prescript.sh` |
| Block-device discovery | `lsblk` | `internal/discover/discover.go` (called by `cmd/lpart-discover/main.go:162`) |
| Cleanup — VG teardown | `vgs`, `lvs`, `pvs`, `vgremove`, `lvremove`, `pvremove` | `internal/cleanup/lvm.go:18,68,155,161,178,199` |
| Cleanup — MD teardown | `mdadm --stop`, `mdadm --zero-superblock` | `internal/cleanup/mdraid.go:25,51,62,75,78,141` |
| Cleanup — partition wipe | `wipefs -af`, `sgdisk --zap-all`, `partprobe` | `internal/cleanup/disks.go:45,50,51` |
| Cleanup — mount probe | `findmnt`, `umount` | `internal/cleanup/cleanup.go:154,167` + `internal/mount/` |
| Partition | `sgdisk` + `partprobe` | `internal/partition/partition.go:206,207,295,306` |
| RAID assembly | `mdadm --create` (metadata 1.0 for `/boot`+`/boot/efi`; 1.2 for storage) | `internal/raid/raid.go:131,142` |
| LVM | `pvcreate`, `vgcreate`, `lvcreate` | `internal/lvm/lvm.go:96,102,147` |
| Filesystems | `wipefs`, `mkfs.xfs`, `mkfs.ext4`, `mkfs.fat`/`mkfs.vfat`, `mkswap` | `internal/fs/fs.go:184,195,197,201` + fat fallback `internal/fs/fs.go:74-79` |
| Mount tree | `mount`, `umount`, `findmnt` | `internal/mount/` (via `mount.Mounter.MountSysrootTree`) |
| OS image fetch | `aria2c` (torrent) → `curl` (HTTP fallback) | `internal/osimage/download.go:100,108,138`; probe at `cmd/lpart-osimage-install/main.go:285` (`exec.LookPath("aria2c")`) |
| OS image extract | `tar -x` with computed `--exclude` list | `internal/osimage/extract.go:265` |
| Storage health (Local mode) | `blkid`, `fsck.xfs` / `fsck.ext4` (exit-code per filesystem) | `cmd/lpart-storage-check/main.go:395` + per-fs branches |
| Initramfs rebuild | `dracut -f --omit luna --kver <ver>` in chroot | `internal/bootloader/rhel.go:62` |
| BIOS bootloader | `grub2-install --no-floppy <disk>` (RHEL) / `grub-install --no-floppy <disk>` (Debian) | `internal/bootloader/rhel.go:133`, `internal/bootloader/debian.go:114` |
| UEFI bootloader | `StageSignedShim` (file copy + CSV write; no subprocess) | `internal/bootloader/uefi_signed.go` |
| GRUB config | `grub2-mkconfig -o /boot/grub2/grub.cfg` | `internal/bootloader/rhel.go:142,176` |
| Bind mounts for chroot | `mount --rbind <src> <dst>` + `mount --make-rslave <dst>` | `internal/bootloader/bindmount.go` (per BOOKKEEPING §G 2026-05-21) |
| UEFI NVRAM | `efibootmgr` (via `chroot /sysroot/usr/sbin/efibootmgr`) | `internal/bootloader/nvram.go:46,162-163` |
| POST resolved layout to Luna | `curl -X POST -H x-access-tokens` | historical `runtime/examples/postscript.sh` |

`executor.dryRunRunner` allowlists a small set of read-only probes
(`lsblk`, `findmnt`, `blkid`, `readlink`, `pvs`, `vgs`, `lvs`,
`hostname`, `cat`, `stat`, `ls`, `true`, `false`, plus
`mdadm --detail/--examine/--scan`) — see BOOKKEEPING §G 2026-05-20
"executor.dryRunRunner forwards a name-keyed allowlist". Everything
else is stubbed with a log line in dry-run.

---

## Things the skeleton's `<json-file-1/2/3>` model got wrong

Worth knowing for the next regeneration:

1. **There are 7 JSON files, not 3.** Four Luna-supplied
   (`install-context.json`, `storage.json`, `discover-config.json`,
   `osimage-filter.json`) + three installer-produced
   (`storage-result.json`, `storage-resolved.json`,
   `boot-context.json`).
2. **`storage.json` is dual-direction.** Read by `lpart-storage-prepare` /
   `lpart-storage-check`; written by `lpart-discover` (Path A in the
   skeleton's framing). The same filename appears on both sides of the
   discovery decision.
3. **The "discovery vs predefined" framing is incomplete.** Actual
   routing is 3-way (`mem` / `auto` / `force-init`) with `auto` itself
   sub-routing on which Luna JSON field is populated. A 2-way path
   decision in the skeleton would hide the memory-root default branch
   that lit up on node001's first PXE boot 2026-05-21.
4. **The trigger is PXE, not iPXE/kexec.** Luna ships a PXE-bootable
   initramfs that includes `install.sh`. Field on the diagram says
   "PXE".
5. **The toolbox is a separate artifact.** `installer-go-toolbox-amd64.tar.gz`
   from the Luna files server is fetched by the prescript before any
   `lpart-*` binary can run. Phase 15 will bundle it into the OS image
   ramdisk (BOOKKEEPING §I).

---

## Open questions / TODO(verify)

- **None at present.** Every branch in the diagram cites a code line.
  Run `git log -p -- internal/bootloader/uefi_signed.go` after any
  bootloader refactor; the signed-shim flow is the part most likely to
  evolve (e.g. when ARM64 signed shim shapes are validated end-to-end —
  currently `TestStageSignedShimARM64` is unit-test only per BOOKKEEPING
  §L).
- The historical partscript/prescript/postscript at
  `git show fbdf6f3:runtime/examples/*.sh` is the authoritative
  orchestration view; the live tree has no `runtime/` dir (deleted in
  commit `079610f`). When Phase 15 reintroduces these as in-image
  artifacts, refresh this companion accordingly.
