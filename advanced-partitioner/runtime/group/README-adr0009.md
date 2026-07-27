# Group pre/part/post columns — pre-0009 vs ADR 0009

The `compute-group/{prescript,partscript,postscript}.sh` files here are the
**pre-0009** model: each is a one-line shim, `exec /usr/bin/lpart-phase <phase>`,
so the lpart installer rides in through the node/group script *column* and
renders its own per-phase TUI.

Under **[ADR 0009](../../decisions/0009-structural-lpart-steps-and-decoupled-tui.md)**
this changes:

- The lpart workers (`lpart-storage-check`/`-prepare`/`-osimage-install`/
  `-bootloader-finalise`) become **structural steps in the rendered `install.sh`**
  (the daemon template), bracketed by `lpart-emit` and bridged with
  `lpart-emit --pipe`. See the template sketch staged for the daemon MR.
- The **pre/part/post columns return to being pure admin hooks** — empty by
  default; an operator puts their own commands there to tailor an install
  (pre runs before BMC; part after storage-create, before image; post after the
  bootloader). They are no longer the installer's entry point.
- One **`lpart-tui`** renderer, hosted by `luna2-start.sh` when the nodeboot
  cmdline carries `lpart.hosttui`, draws a single dashboard for the whole
  install by reading the `/run/lpart/events` bus that every step emits to.

So in the 0009 model these shim files are not used: the group's part/post
columns hold the operator's own scripts (or nothing), and the structural lpart
invocation lives in the template. The shims are kept here until the daemon
template cutover lands, so the pre-0009 path keeps working in the meantime.

Example of a freed admin column (what an operator might put in `partscript`
*after* the cutover — runs between storage-create and image-install):

```bash
#!/bin/bash
# compute-group partscript — admin hook (ADR 0009 model).
# Disks exist and are mounted at the sysroot; the OS image is NOT yet extracted.
echo "tagging freshly-built array for the asset DB"
# ... operator commands ...
```
