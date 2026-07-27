# 0009 — Structural lpart steps in `install.sh`, freed pre/part/post columns, decoupled TUI

- Status: Proposed (2026-06-08)
- Deciders: Alex
- Supersedes parts of [0002](0002-flatten-installer-to-phase-only.md) (the welded `--phase` orchestrator + the `lpart-phase` column shim)

## Context
Today the lpart pipeline is injected through the node's **pre/part/post script columns**: each column
field is `exec /usr/bin/lpart-phase <phase>`, which runs `lpart-node-installer --phase <phase>` — a single
process that both **orchestrates** the lpart-* binaries and **renders** the Bubble Tea TUI (ADR 0002).

Three problems follow from that welding:

1. **The columns are not available to admins.** The `prescript`/`partscript`/`postscript` columns
   (`database_layout.py` TEXT columns → `LUNA_PRESCRIPT`/`PARTSCRIPT`/`POSTSCRIPT`) are classic Luna's
   per-stage admin hooks — the field where an operator tailors an install. On a model node the installer
   *occupies* that field, so the admin has nowhere to put their own part/post commands.
2. **The admin column can't run mid-pipeline.** The desired order runs the part column *between*
   partition-create and image-install (and the post column *between* bootloader-finalise and the legacy
   tail). One welded `--phase part` process can't yield the console to a bash column in the middle, so
   the interleaving is impossible and the single continuous TUI can't span the gap.
3. **Redundant plugin machinery.** The `scripts`/`customscript` list and the `boot/scripts` plugins
   (`diskfull`, `raid1`, `nodhcp`, `default`) inject partition logic via `## SCRIPT … CODE SEGMENT`.
   RAID/LVM/layout is now owned by the `disklayout` attribute + `lpart-storage-prepare`, so this path is
   dead weight.

Verified facts this builds on:
- `install.sh` is **rendered per-request** by the daemon from `templ_install_lpart.cfg` and re-downloaded
  every boot; `luna2-start.sh` (the committed 95luna ramdisk hook) fetches it (`curl …/boot/install/<node>`)
  and runs it (`/bin/sh /lunatmp/install.sh`), then retries/fatal-parks and finally **pivots** to `/sysroot`.
- Both templates already call `update_status "install.<stage>"` at every step, so stage boundaries
  (including the three columns) are **already demarcated** in `install.sh`.
- sshd and the network come up in `luna2-start.sh` **before** `install.sh`; the pivot happens in
  `luna2-start.sh` **after** it.
- Legacy runs `prescript` **before** `bmcsetup` (decision below keeps this).

## Decision
Invert the control split: the **template orchestrates** (a readable bash sequence), the **lpart-* binaries
do work**, and a **standalone renderer observes the whole run**. Concretely:

### 1. lpart steps become structural template steps
Rewrite `templ_install_lpart.cfg` so the rendered `install.sh` calls the lpart worker binaries directly at
fixed points, replacing the `download_image`/`unpack_imagefile` no-ops and the column-as-installer trick.

### 2. The pre/part/post columns return to being pure admin hooks
Drop the `exec /usr/bin/lpart-phase <phase>` shims from the group/node fields; the columns default empty
and run *whatever the admin puts there*, at the points marked **COLUMN** below. Keep legacy ordering:
**pre column runs before `bmcsetup`.**

> **Implementation status (client side, on `advanced-partitioner`):**
> - **slice 1** — `internal/eventbus` (NDJSON `WireEvent` bus) + `lpart-emit` producer.
> - **slice 2** — `lpart-tui` standalone renderer + shared `internal/installerui`; `lpart-node-installer`
>   refactored onto it (behaviour-preserving). `eventbus.OpenReader`/`Rehydrate`; `model.EnsureStage`.
> - **slice 3** — `lpart-emit --pipe` worker→bus bridge (workers stay pure; output teed to the log too).
> - **slice 5** — `luna2-start.sh` gains a **default-OFF** `lpart.hosttui` path that hosts one `lpart-tui`
>   around the whole `install.sh`; packaging (`module-setup.sh`, `create_rpm.sh`, `.spec` → 2.0-41) ships
>   the two new binaries. With the flag absent the install runs exactly as before.
> - **slice 4 / template** — the structural restructure of `templ_install_lpart.cfg` is the daemon MR; a
>   reviewable sketch is staged at `/root/luna2-v1.4-patches/legacy/templ_install_lpart.cfg.adr0009-sketch`.
>   The freed-column model is documented in `runtime/group/README-adr0009.md`.
>
> The end-to-end cutover (template + `lpart.hosttui` cmdline) is **not yet hardware-validated** — it needs
> the daemon MR to land and a node install. Slices 1–3 are unit-tested and the bus→render path is proven
> across processes on a real FIFO.

### 3. Decouple the TUI (event-bus inversion)
- Split `lpart-node-installer`: the Bubble Tea renderer becomes a standalone **`lpart-tui`**, launched by
  `luna2-start.sh` *around* `install.sh`, owning the console for the whole run. The pipeline/sequencing
  logic in `internal/pipeline` is retired from the binary (sequencing now lives in the template).
- Add a local **event bus**: a FIFO at `/run/lpart/events`. `lpart-tui` tails it and renders one continuous
  dashboard spanning BMC, all three columns, every lpart step, and pivot.
- The lpart workers gain an **emit mode** (write their existing typed `InstallerEvent`s as JSON lines to the
  bus instead of rendering in-process). A tiny **`lpart-emit <stage> start|done`** lets the bash steps
  (bmcsetup, the columns, the legacy tail) report coarse progress to the same bus; the existing
  `update_status` markers are fanned to it too.
- `install.sh`'s raw stdout is routed into the dashboard's log pane (a redirect in `luna2-start.sh`), not
  straight to `/dev/console`.

### 4. Drop the plugin machinery from the lpart template only (keep it for legacy)
The `boot/scripts` plugins (`diskfull`/`raid1`/`nodhcp`/`default`) and the `scripts`/`customscript`
mechanism **stay** — the **legacy** template (`templ_install.cfg`) still uses them and must keep working.
In `templ_install_lpart.cfg` **only**, remove every *reference* to them: the `## SCRIPT … CODE SEGMENT`
injection markers, the `customscript` function and its per-stage calls, and the `node_scripts`/
`LUNA_SCRIPTS` usage. The lpart template then carries no plugin-injection path at all; RAID/LVM/layout
comes from `disklayout` + `lpart-storage-prepare`. Nothing is deleted from the daemon — only the lpart
template stops pointing at it.

## Execution order (rendered `install.sh`)
`[RAMDISK]` = `luna2-start.sh`, outside `install.sh`. **COLUMN** = admin hook. **‹lpart›** = structural call.

| Step | Runs | Owner |
|------|------|-------|
| network + **SSH** (sshd) + tty2; TPM token; fetch+launch `install.sh` | `[RAMDISK]` | `luna2-start.sh` |
| `lunainit`, `dynamic_ip_check` | framework | `install.sh` |
| **PRE COLUMN** (`prescript`) | **admin** | before BMC (legacy order) |
| `write_provisioning_inputs` | framework | writes `storage.json` / `install-context.json` |
| **`bmcsetup`** | framework | |
| **‹lpart-storage-check›** (partition health) | lpart | NEW structural |
| **‹lpart-storage-prepare›** (partition/RAID/LVM/mkfs + mount) | lpart | NEW structural |
| **PART COLUMN** (`partscript`) | **admin** | free hook, after create / before image |
| **‹lpart-osimage-install›** | lpart | replaces `download_image`/`unpack_imagefile` |
| `collect_mac_n_name_net`, `change_net`, `node_secrets` | framework | legacy net/secrets |
| **‹lpart-bootloader-finalise›** | lpart | NEW structural |
| **POST COLUMN** (`postscript`) | **admin** | free hook, after finalize |
| `node_roles`, `fix_capabilities`, `restore_selinux_context`, `update_system_info`, `cleanup`, `install.success` | framework | legacy tail |
| **pivot** → `switch_root /sysroot` | `[RAMDISK]` | `luna2-start.sh`, after success |

## Consequences
- **Admins regain the columns.** pre/part/post are real tailoring hooks again, at well-defined points
  around the structured lpart steps.
- **The TUI spans the whole install**, not just the part pipeline — strictly more coverage than today, and
  it survives the admin-column gaps. Cost: the `lpart-node-installer` split (renderer ↔ orchestration), the
  worker emit mode, `lpart-emit`, and a console-routing change in `luna2-start.sh`.
- **`lpart-phase` is deleted** and `internal/pipeline` sequencing leaves the binary; the conceptual path
  from 0002 (`hook → lpart-phase → --phase → pipeline`) is replaced by (`template sequences workers →
  event bus → lpart-tui renders`).
- **Boundary:** the template (`templ_install_lpart.cfg`) is **daemon-side = clustervision → human MR only**;
  it lands as a staged patch under `/root/luna2-v1.4-patches`, not pushed. The client-side work (the
  `lpart-tui` split, worker emit mode, `lpart-emit`, freed group shims, dereferencing the plugin
  machinery *in the lpart template*, `luna2-start.sh` redirect) lands on the alex-owned
  `advanced-partitioner` branch. The `boot/scripts` plugins themselves are untouched (legacy keeps them).
- **Migration:** a node keeps installing through the unchanged `luna2-start.sh` fetch/run/retry/pivot loop;
  only the rendered `install.sh` and the (now-empty) column defaults change.

## Open items (resolve during implementation)
- Event-bus protocol: JSON line schema for worker events vs the coarse `lpart-emit` start/done; how
  `lpart-tui` maps them to the stage list and log pane. **Implemented (slice 1):** `internal/eventbus`
  defines the NDJSON `WireEvent` (a 1:1 projection of `engine.InstallerEvent`); `lpart-emit` is the
  bash-facing producer (`start|done|fail|log <stage> [msg]`, best-effort — exit 0 with no reader).
  **Requirement this pins for `lpart-tui`:** every bash step calls `lpart-emit` as a *separate*
  open→write→close process, so the reader MUST hold the FIFO open `O_RDWR` (or keep a sentinel write fd)
  — otherwise the first emitter's close delivers EOF and the dashboard stops after one event. Verified on
  a real FIFO: with the reader holding it `O_RDWR`, three sequential emitter processes all land.
- Failure semantics: a non-zero column or lpart step still aborts `install.sh` and triggers the ramdisk
  retry; confirm `lpart-tui` renders the failure before the console is reclaimed.
- Whether `lpart-storage-check` stays a separate structural step for every mode or only for `auto`/`sync`
  (it is the whole job for `local`).

---

# Document text for the PDF major revision

> This section is the **source text** for the next major revision of
> `docs/storage-provisioning-model.md`. The current Chapter 6 ("Installer workflow") is too shallow and,
> as this design lands, will be wrong. Replace it with the chapter below. **Reporting rule: describe only
> the final (target) state** — no migration history, no "this used to be…", no legacy-vs-new asides except
> the one clearly-marked subsection that exists *because* legacy still ships. The chapter is written so a
> reader who has never seen the system can follow a node from power-on to first boot without guessing.

## Chapter — How a node boots and installs, end to end

A node has no operating system of its own. Every install is driven from the **controller** (the Luna
daemon): the node network-boots a small **install ramdisk**, the ramdisk asks the controller for a
**per-node install script**, runs it, and on success **pivots** into the freshly installed system. This
chapter follows that path in order and names every actor, script, and step, so it is unambiguous *who*
runs *what*, and *when*.

### 1. The actors and the artifacts

| Actor / artifact | What it is |
|------------------|-----------|
| **Controller (Luna daemon)** | The server. REST API (`:7050`) + file server (`:7051`). Holds every node/group's config, renders the install script per request, serves boot artifacts, and records install status. |
| **`luna2-start.sh`** | The node-side **install ramdisk controller** — the `95luna` dracut module hook, baked into the install initramfs. Owns the console, the network, sshd, the fetch/run/retry loop, and the final pivot. It is *committed code*, identical on every install. |
| **`install.sh`** | The **install sequence** — a single bash script the daemon **renders per request** from a template and the node **re-downloads every boot**. It runs the ordered steps (below). It exists only in the ramdisk's RAM; editing the template changes the steps with no node rebuild. |
| **`lpart-*` binaries** | The Go **workers** that do the real install work (storage, image, bootloader). Baked into the install initramfs. Each ends by *proving and showing* what it did. |
| **`lpart-tui`** | The **renderer** — a standalone dashboard process `luna2-start.sh` launches around `install.sh`; it observes the whole run and draws one live screen. |
| **osimage** | The packed OS image the node installs, served by the controller's file server. |

### 2. The boot chain — power-on to the install ramdisk

```
firmware ─DHCP─► iPXE ─► controller's netboot decision (discover_mac)
                                  │
                                  ▼
        kernel + install initramfs, with a cmdline (root=luna, luna.url=…, luna.node=…)
                                  │
                                  ▼
                       the ramdisk's init starts luna2-start.sh
```

The firmware PXE-boots, iPXE chains to the controller, and the controller decides (by MAC) to hand the
node its **install** kernel + initramfs with a kernel cmdline carrying the controller URL, node name, log
host and verify-cert flag. Nothing node-specific to the *install* has happened yet — that is the next
stage.

### 3. The install ramdisk — `luna2-start.sh`

This is the node-side controller. **Before** it fetches anything it makes the node reachable and
inspectable:

- brings up the install NIC and the network;
- starts **sshd** and a **tty2** login (so an operator can watch or intervene);
- fetches a **TPM token** (measured boot, PCR-0) by POSTing to `…/tpm/<node>` → `/lunatmp/token.json`,
  used to authenticate the script fetch.

Then it runs a **fetch → run → check** loop:

```
loop:
  curl -H "x-access-tokens: <token>"  <controller>/boot/install/<node>  -o /lunatmp/install.sh
  /bin/sh /lunatmp/install.sh                       # ← runs the whole install sequence
  case $? in
     0)  break               # success → fall through to pivot
     3)  fatal-park          # non-retryable (e.g. a daemon routing mistake); show why, hold the operator
     *)  sleep; continue     # transient → retry the whole script
  esac
pivot:  switch_root /sysroot # boot the installed OS
```

Two boundaries to hold onto: **SSH and the network come up here, before `install.sh`**, and **the pivot
happens here, after `install.sh` succeeds**. Everything between is `install.sh`.

### 4. What the controller does at install time

When the node requests `/boot/install/<node>`, the daemon:

1. **Picks a template by `install_mode`.** `legacy` → the original bash installer template; any other mode
   → the **lpart** template. Same packed image and same ramdisk either way — only the rendered `install.sh`
   differs. Moving a node between the two is a single field change; nothing is rebuilt.
2. **Renders the script**, substituting the node's configuration into the template: controller URL/port,
   token, OS image name, **provision method/fallback**, per-interface network, BMC config, the node's
   **`install_mode` / `disklayout` / `osimage_filter`**, and the node's **three script columns**
   (`prescript`, `partscript`, `postscript`).
3. **Serves** the rendered script to the node.
4. **Records progress.** Each step in `install.sh` calls `update_status "install.<stage>"`, which POSTs to
   `…/monitor/node/<node>`; those are the *"Luna installer: <stage>"* lines the controller shows.

The script is generated fresh for every boot, so the template is the single source of the install
sequence.

### 5. `install.sh` — the install sequence

A single bash script, run top to bottom, **no reboot inside it**: it ends by reporting success and control
returns to `luna2-start.sh` for the pivot. Three kinds of step appear in it:

- **framework steps** — the unchanging plumbing (make dirs, report DHCP IP, configure BMC, set hostname
  and network into the new OS, install secrets/roles, relabel, report success);
- **structural lpart steps** — the install proper, each a direct call to an `lpart-*` worker;
- **admin columns** — the node's `prescript`/`partscript`/`postscript`, run at fixed points so an operator
  can tailor the install.

The order, with the plug points marked (**COLUMN** = admin hook, **‹lpart›** = worker call):

| Step | What runs | Kind |
|------|-----------|------|
| `lunainit` | create runtime + sysroot dirs | framework |
| `dynamic_ip_check` | if DHCP, report the leased IP | framework |
| **`prescript`** | the node's **pre** column | **COLUMN** |
| `write_provisioning_inputs` | write `storage.json` / `install-context.json` / `osimage-filter.json` | framework |
| `bmcsetup` | configure the BMC (IPMI user + network) | framework |
| **‹lpart-storage-check›** | probe an existing layout's health (reuse vs recreate) | lpart |
| **‹lpart-storage-prepare›** | discover disks, partition / RAID / LVM / mkfs, mount the sysroot | lpart |
| **`partscript`** | the node's **part** column | **COLUMN** |
| **‹lpart-osimage-install›** | download + extract the OS image into the sysroot, then verify | lpart |
| `collect_mac_n_name_net`, `change_net`, `node_secrets` | hostname + netplan/ifcfg + secrets into the new OS | framework |
| **‹lpart-bootloader-finalise›** | fstab, signed shim + GRUB into the ESP, UEFI NVRAM entry, then verify | lpart |
| **`postscript`** | the node's **post** column | **COLUMN** |
| `node_roles`, `fix_capabilities`, `restore_selinux_context`, `update_system_info`, `cleanup` | roles + setcap + SELinux relabel + asset tag + scratch cleanup | framework |
| `update_status install.success` | report final state | framework |

**Ordering note (deliberate):** the **pre** column runs **before** `bmcsetup` — preserving classic Luna
behaviour, where a pre script must not assume the BMC is configured yet. The **part** column runs *after*
the disks exist but *before* the image lands; the **post** column runs *after* the system is bootable but
*before* the legacy tail. So an operator can act at exactly the three moments that matter: before the
install, between storage and image, and after the system is finished.

### 6. The admin columns, precisely

`prescript`, `partscript`, `postscript` are **TEXT fields on the node and group** — the box an operator
types commands into to tailor an install. They inherit group→node like any other attribute. At each plug
point the daemon writes the field's contents to `/lunatmp/<stage>script.sh` and the sequence runs it with
`bash`; a non-zero exit aborts the install and the ramdisk retries.

These columns are the **only** customization path in the lpart install. (Luna also has a separate `scripts`
attribute — a list of named boot-script plugins such as `diskfull`/`raid1` — but that mechanism belongs to
the **legacy** installer and the lpart template does not use it; RAID/LVM/layout is described declaratively
by the `disklayout` attribute and realized by `lpart-storage-prepare`.)

### 7. The `lpart-*` workers

| Binary | Stage | What it does |
|--------|-------|--------------|
| `lpart-storage-check` | storage | Probe an existing layout: healthy OS set → reuse-and-mount; broken → recreate. The whole job for `local` (verify only). |
| `lpart-storage-prepare` | storage | Discover disks (by tag/size), resolve the declared layout, then partition / build RAID / create LVM / mkfs and mount the tree at the sysroot — dispatching on `install_mode` and enforcing the data fence. `sanitize` securely erases and stops here; `memboot` makes a tmpfs root. Ends by showing `blkid`/`pvs`/`vgs`/`lvs`/`mdadm` evidence. |
| `lpart-osimage-install` | image | Download + extract the OS image into the sysroot — honouring `provision_method`/fallback, the `osimage_filter`, the first-install→full promotion, and the persistent-set excludes — then verify the extract (`/etc/os-release` present). |
| `lpart-bootloader-finalise` | bootloader | Write `fstab`, install the signed shim + GRUB into the ESP, register the UEFI NVRAM entry — then verify by re-reading `efibootmgr`, the ESP loaders and `grub.cfg`, and showing that evidence. Skipped for `memboot` (RAM root, no bootloader). |

Each worker is independently runnable and dry-runnable, does one thing, and ends by **proving and showing**
its result — so a run on a new distro/arch shows exactly what landed instead of a silent "ok".

### 8. The live view — `lpart-tui` and the event bus

The dashboard is a **separate process from the work**, so it can span the whole install — including the
admin-column gaps — without being tied to any one step. `luna2-start.sh` launches `lpart-tui`, which owns
the console and tails a local **event bus** (`/run/lpart/events`). Every step reports to that bus: the
`lpart-*` workers emit structured events, and the bash steps (BMC, the three columns, the legacy tail) emit
coarse start/done markers. The raw output of `install.sh` flows into the dashboard's log pane. The result
is one continuous screen showing the Luna context, the storage decision, the image install, the bootloader
finalise, and the operator's own column scripts as they run.

### 9. The handoff

When `install.sh` reports `install.success`, control returns to `luna2-start.sh`, which moves the install
log into the new system and **pivots** (`switch_root /sysroot`) — the node's first boot of its freshly
installed OS. From here the node is a running machine, not an installer.

---

### Diagram to (re)draw for the chapter

One figure only (per "no two pages of pictures"): a single vertical swimlane with three columns —
**controller**, **`luna2-start.sh` (ramdisk)**, **`install.sh` (sequence)** — showing the handoffs
(render → fetch → run → status POSTs → pivot) and, in the `install.sh` lane, the step list from §5 with
the three COLUMN plug points and the four ‹lpart› steps highlighted. Keep the existing TUI screenshot as
the chapter's only raster image.
