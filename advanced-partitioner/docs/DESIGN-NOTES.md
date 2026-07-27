# Design notes and starting point

Orientation for the next working session. Read these two first:

- `storage-provisioning-model.md`, the spec (v1.4). The PDF next to it is generated from it.
- `IMPLEMENTATION_PLAN.md`, the build order across luna2-client and luna2-daemon.

This file is the short version: the names and rules that are settled, and what is still open.
The point is to not re-open these in the next session.

## Settled vocabulary

- `install_mode`, a Luna node or group attribute, one of: `auto` (default), `sync`, `full`,
  `local`, `memboot`, `sanitize`, `legacy`. It is not a JSON key.
- `disklayout`, the Luna attribute that holds the whole storage config (the set of sets). It is
  the document, not a per set field. Do not reuse this word for anything inside a set.
- A set has: `name`, `role` (`os` or `data`), `selection` (`discover` or `manual`; the old
  name `auto` is retired, see ADR-0008), `raid` (`none`, `0`, `1`, `5`, `6`, `10`), `count`,
  `spares`, `match`, `devices`, `save`, `persistent`, `origin`, `volumes`.
- `save` (ADR-0008): a per set flag valid only on a `selection: discover` set. After resolving,
  the installer writes the concrete layout back to the node's `disklayout` as `selection: manual`
  (stamped with `origin`), pinning the node. Revert by clearing the node `disklayout`. Rejected on
  a manual set. `origin` = `{from, at, by, host}`, machine-written provenance that round-trips.
- `match` (used when `selection: discover`): `tags`, `min_size`, `max_size`, `model`. A disk must
  carry all listed tags and fall within the size range.
- disk tags, auto derived: `rotary` (spinning), `ssd` (any solid state), `nvme` (a subset of
  `ssd`, so an NVMe carries both). Operators may add custom tags later (open item).
- `persistent`, a flat per set flag. Hands off: never partitioned, formatted or written, only
  mounted. Survives `auto`, `sync`, `full`. Only `sanitize` erases it.

## Settled rules

- Two inputs decide a node: `install_mode` and the `disklayout` config. The config is read only
  when the mode touches disks.
- Data fence: a data set is wiped only by `full`; `sync` never wipes and halts on a broken or
  mismatched disk; `auto` repairs the os set only; no mode wipes on a mismatch except the os set
  under `auto` or `full`.
- `memboot` runs the OS from RAM and may still mount persistent data sets.
- `sanitize` securely erases all local disks for tenant turnover, then stops.
- UEFI only, on x86_64 and aarch64 (ARM is UEFI by SBBR). No BIOS, no firmware field.
- Stateless versus stateful is a derived label, never an input. It is read off `install_mode`.
- Config lives at the group and is overridable per node. `install_mode` is node wins; the
  `disklayout` config is replaced as a whole, not field blended.
- Writing style for the doc: plain language for sales, engineers and devs. No em dashes.

## Open items (roadmap, section 8 of the spec)

1. A small on node agent for continuous reconcile and drift detection (today reconcile is at
   boot only). Highest priority.
2. A declarative API and GitOps surface, ideally a custom resource.
3. Measured boot and attestation on top of the existing TPM token.
4. Operator assigned custom disk tags: where they are set in Luna and how they reach the node.

## Repo and build

- Branch `advanced-partitioner` on the alex fork. The build box SSH key authenticates as omar,
  so verify access before pushing.
- The PDF is built from the markdown with WeasyPrint, inlining `diagrams/*.svg`. The build
  script used this session is `build-pdf.py`.
- **Update 2026-06-06:** the client is now **implemented** on branch `advanced-partitioner` (the old
  `lpart-discover`/`intent` grammar is gone); the daemon/CLI patch series is staged at
  `/root/luna2-v1.4-patches`. See the JOURNAL "Status (2026-06-06) — v1.4 client IMPLEMENTED" entry.
- **Update 2026-06-07 (ADR-0008):** `selection: auto` renamed to `selection: discover` (kills the
  clash with `install_mode: auto`); added the `save` write-back flag and `origin` provenance.
  `save:true` on a discover set POSTs the resolved layout to the node-level `disklayout` (manual)
  via the existing `POST /config/node/<node>` route + the node install token (no new daemon route).
  The write-back transport is **curl via os/exec** (internal/lunaclient), mirroring internal/lpartinputs
  in reverse (GET→POST): curl is already in the install initramfs and this keeps the Go net/http
  stack out of the binary. Spec §5.5 added; PDF regenerated (build-pdf.py + WeasyPrint 69.0 under
  `python3.11` at `/usr/local`, not the default `python3` 3.10 nor the daemon python). `make check`
  is green on the box's own **go1.25.9** (the curl transport removed the net/http import, so the
  earlier stdlib net/http/x509 govulncheck findings are gone — no Go toolchain bump needed).
