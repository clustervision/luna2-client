# scripts/dev — developer-only helpers

These are **not** part of the install path. The live install ships the `lpart-*`
binaries inside the luna2-client RPM / install initramfs (95luna
`module-setup.sh`); nothing fetches a toolbox tarball or stages binaries at
boot.

- `stage_to_node001.sh [node]` — hot-swap freshly built `lpart-*` binaries onto a
  parked node's `/tmp/install_files/bin` without a PXE cycle. Drive the node
  with `LPART_BIN=/tmp/install_files/bin` so the shim picks them up.

- `reentrancy-battery.sh <node> <layouts-dir>` — re-entrancy matrix for the new
  partitioner on a memboot node (regression.md §3.20).

- `legacy-lint.sh` — static regression guard for the **legacy** pre/part/post
  scripts (daemon plugins + roles + mkdocs). Encodes the F1–F9 defects from
  `../../LEGACY.configs` as greps over the live sources; exit = #BUG findings
  (regression.md §3.21). Point it at other checkouts with `LEGACY_PLUGINS` /
  `LEGACY_ROLES` / `LEGACY_DOCS`.

- `legacy-battery.sh <subcommand> <node> …` — controller-side driver for the
  legacy install campaign (regression.md §3.22/§3.23): `set-plugin`, `pxe-reboot`,
  `wait`, `verify`, `nvram`, and the DESTRUCTIVE `pull <disk>` (dd-failover).
  `install_mode=legacy` is daemon-only, so it drives full PXE cycles, not lpart-*.

(The retired `upload_toolbox.sh` + Makefile `toolbox` target were removed when
the HTTP-toolbox fetch was replaced by RPM/initramfs delivery.)
