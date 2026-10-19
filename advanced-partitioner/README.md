# installer-go

Go port of the TrinityX/Luna localboot installer. Replaces the Bash tree under
`/root/NEWTREE/{bin,lib}` with a set of statically-linked Go binaries shipped
as a Luna toolbox tarball. Per-node inputs (storage, discover, image filter,
install mode, sync mode) are pinned directly in Luna as first-class fields;
the prescript/partscript/postscript bodies are kept in Luna too.

The Bash tree at `/root/NEWTREE` is the **behavioural oracle** during the
migration. Every Go module must reproduce the behaviour and JSON shapes of
its Bash counterpart byte-for-byte until the corresponding Bash code is
retired (see `BOOKKEEPING.md` for retirement criteria).

## Binaries

| Binary | Purpose | Bash equivalent |
|---|---|---|
| `lpart-discover` | Block-device discovery; emits v2 storage JSON | `/root/NEWTREE/bin/lpart-discover` |
| `lpart-storage-prepare` | Partition, RAID, LVM, mkfs, mount `/sysroot`; emits `storage-result.json` | `/root/NEWTREE/bin/lpart-storage-prepare` |
| `lpart-storage-check` | Verify live storage against intent; exit 0/2/1 | `/root/NEWTREE/bin/lpart-storage-check` |
| `lpart-osimage-install` | Download + extract Luna OS image into `/sysroot` with preserve/exclude filters | `/root/NEWTREE/bin/lpart-osimage-install` |
| `lpart-bootloader-finalise` | fstab, initramfs, GRUB, NVRAM | `/root/NEWTREE/bin/lpart-bootloader-finalise` |

## Build

```sh
make build-host                # controller-side build of all binaries
make build-linux               # cross-compiled static linux/amd64 + linux/arm64
make toolbox                   # toolbox tarball for the Luna prescript
make check                     # fmt + vet + test + lint + govulncheck
```

The toolbox tarball produced by `make toolbox` is uploaded to
`/trinity/local/luna/files/installer-go-toolbox-amd64.tar.gz` and fetched
by the prescript via Luna HTTP.

**For a release you do not run any of this by hand.** The pipeline runs
`make build-linux` and stages the resulting binaries into each package tree
before packing, for every family and architecture in one go — see the README at
the root of this repository. The targets above remain the way to build and test
locally; they are no longer how a shipped package gets its binaries.

## Layout

```
cmd/                  one main.go per binary, no business logic
internal/
  cli/                shared flag parsing, --help rendering, PATH guard
  config/v2/          v2 storage JSON: structs, Validate(), accessors
  log/                slog setup (compact stderr + text/JSON file sink)
  executor/           THE ONLY package that may import os/exec
  safety/             host guard, size guards
  sysenv/             require_installer_env, hostname checks
  distro/             rhel | debian | suse adapters; family detection
  discover/           lsblk parsing, by-path resolution
  intent/             set-spec parser, OS layout ladder, RAID tolerance
  disk/{selector,inventory}/
  partition/ raid/ lvm/ fs/ fscheck/ mount/ cleanup/
  devices/            disk + partition + LVM device-path helpers
  plan/               Step + dry-run renderer
  result/             storage-result.json + boot-context.json schemas
  osimage/            delete-before-extract, ACL detect, torrent+http fetch
  luna/               LUNA_TOKEN read, image-filename detection
  bootloader/         fstab, bind mounts, NVRAM cleanup, BLS normalisation
  installcontext/     install_mode + osimage_sync resolution
  state/              durable on-disk state for re-entrant runs (later)
  testutil/           golden fixture loader
docs/                 schemas, design notes
scripts/              upload_toolbox.sh, run-on-node.sh, stage_to_node001.sh
testdata/             captured lsblk/blkid/pvs/mdstat etc. from real nodes
tests/integration/    on-node integration suite (build tag node001)
```

## Per-node inputs (pinned in Luna)

The v1.4 model exposes every per-node input as first-class Luna fields.
Operators edit them with `luna node change <name> -*`:

| Field            | Holds                                                  | Default |
|---               |---                                                     |---      |
| `install_mode`   | one enum (see below)                                   | `auto`  |
| `disklayout`     | the whole storage config (sets / selection / raid / volumes), b64 JSON; the postscript POSTs the resolved layout back | empty |
| `osimage_filter` | osimage-filter.json (preserve list)                    | empty   |

| `install_mode` | Behaviour                                                          |
|---             |---                                                                |
| `auto`         | `disklayout` populated → resolve + create/repair/mount per the data fence; empty → memory-root (tmpfs). |
| `sync`         | refresh the OS image onto the existing layout, preserving data (blank → create). |
| `full`         | wipe + recreate the layout, full image extract.                    |
| `local`        | verify the existing layout in place (`lpart-storage-check`), no extract. |
| `memboot`      | memory-root tmpfs; disks untouched.                                |
| `sanitize`     | securely erase all local disks, then stop.                         |
| `legacy`       | daemon-only: run the old bash prescript/partscript/postscript (see `LEGACY.configs`); the `lpart-*` tools are not invoked. |

There is **no separate discovery step** — `selection: discover` lives inside the
`disklayout`. Set the fields with the same `-flag` / `-qflag` pattern as `prescript`:

```sh
luna node change node001 --install-mode auto              # auto|sync|full|local|memboot|sanitize|legacy
luna node change node001 -qdisklayout      /path/to/disklayout.json
luna node change node001 -qosimage-filter  /path/to/osimage-filter.json
```

For the default (non-legacy) path the Luna `install.sh` template
(`templ_install_lpart.cfg`) inlines these into
`/tmp/install_files/runtime/*.json` before the partscript runs, then the
`lpart-phase` shim drives `lpart-node-installer --phase pre|part|post`;
`luna_finish` (dracut's `luna-installer.sh` and initramfs-tools' `luna-start`) runs
`lpart-phase finish` last, which flushes the installed system to disk before the
hand-over to it.

## Status

Phases 1–14 complete (2026-05-21). Remaining open work in `BOOKKEEPING.md`
§I — most notably Phase 15 (toolbox-in-ramdisk) and Phase 16 (deployment
migration).
