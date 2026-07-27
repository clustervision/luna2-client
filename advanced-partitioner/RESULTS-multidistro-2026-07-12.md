# Multi-distro + lchroot-build + wired-regression campaign — 2026-07-12 (v3)

Extends the installer to build Rocky 10 / Ubuntu 24.04 / Ubuntu 26.04 / SUSE-latest images via
the **TrinityX ansible routed through lchroot-bwrap**, wires lpart into all distro families, and
packages a **wired-in destructive regression suite**. Run on the lpx cluster (ctrl 192.168.123.148).

## Delivered + validated

- **lchroot ansible integration (the "based on lbwrap" requirement) — WORKS.**
  - `ansible-overlay/connection_plugins/lchroot.py` — a real Ansible **connection plugin** that
    runs the in-image customization phase inside `lchroot --path <image>` (bwrap sandbox).
    **Validated end-to-end**: `ansible-playbook` ran `command`/`raw`/`copy` tasks inside
    compute-image via lchroot (whoami=root, os-release read, 0 failures).
  - `ansible-overlay/apply-lchroot-patch.sh` — rewrote the roles' raw `chroot {{ image_path }}`
    shell tasks to `lchroot --path {{ image_path }} --force --` and flipped `dynamic_hosts`
    `ansible_connection` chroot→lchroot. **Applied + verified** on `/root/trinityX/site`
    (backed up + reversible).
- **Per-family lpart wiring (module 10/30) — DONE.** All three existing images
  (compute-image/rocky9, ubuntu24-image, opensuse-image) carry the full 7 lpart binaries +
  lpart-phase + the family-matched 95luna module; ubuntu got dracut installed via lchroot; each
  packed. Seam gate green.
- **Wired-in regression suite (module 70) — DONE + validated.** manifest.tsv (30 automated + 10
  catalogued-TODO) + run-regression.sh (shape-scheduled, parallel-across-nodes, roll-up
  md/json/junit) + module 70 (allowlist-guarded). Validated end-to-end (seam gate via the runner,
  roll-up produced, exit 0).
- **build tooling — DONE.** module 25 + build-image.sh + probes (SUSE 15.6→15.5, Ubuntu-26
  `resolute`, Rocky-10 el10 + container) + wrapper playbooks + ansible-core 2.17 venv (on the
  TrinityX py3.10).
- **Go fixes:** BE-U1 auto-plain-mode on an unusable/0x0 terminal (+test); create_deb stages all 7
  binaries; ubuntu/opensuse `luna-installer.sh` synced to redhat's `LUNA_SAFETY_RC=2` park half.

## Cross-distro UEFI validation — a chain of REAL findings (honest)

First-ever attempt to run lpart on Ubuntu/SUSE under **UEFI** (all prior distro passes were BIOS,
pre-2026-07-12). Each finding was diagnosed and, where cheap, fixed — advancing the install further:

- **rocky9 (compute-image): PASS** — baseline lpart UEFI install, reference.
- **openSUSE (SLE 15 SP5): lpart now runs DEEP into the install** (furthest ever under UEFI):
  1. TUI rendered "terminal too small (0x0)" on the serial console → part exit 1.
     **FIXED**: BE-U1 auto-plain-mode fallback (Go) + `lpart.notui` kerneloption.
  2. `lpart-storage-prepare: missing required commands: jq, sgdisk` — the SLE image lacked them,
     so the dracut module couldn't bake them in. **FIXED**: installed jq + gptfdisk into the image
     **via lchroot (zypper)**, repacked.
  3. Now: firmware gate passed, vda partitioned (vda1/2/3), mdadm zero-superblock + wipefs done,
     then **`pvcreate -ff -y /dev/vda3` exit 5** — a SUSE-15.5 variant of the BE-A6c LVM
     devices-file issue (the `use_devicesfile=0` installer-lvmlocal fix may not take on SLE's LVM
     config path). **OPEN** — needs a SUSE-specific lvm check. Genuine progress, not a pass.
- **Ubuntu (24.04): lpart is NOT in the install initramfs — real architectural gap.**
  `/usr/bin/lpart-phase: No such file or directory` in prescript. Root cause: **luna packs Ubuntu
  images with `mkinitramfs` (initramfs-tools), NOT dracut** (daemon `.../operations/image/
  ubuntu.py`), so the committed **ubuntu/x86 dracut 95luna module is ignored** and lpart never
  reaches the Ubuntu install initramfs. redhat/opensuse pack with dracut (`ramdisk_modules=
  ['luna']`) so they get it. **OPEN, fix identified**: ship an **initramfs-tools hook**
  (`hooks/lpart` + a local-premount installer) that copy_execs the lpart tooling into the
  mkinitramfs-built initramfs — a substantial port of the ~500-line dracut luna2-start/
  luna-installer flow (the BIOS-era Phase 11 "Ubuntu proof" used a hand-built, never-committed
  hook). Documented in LOOSE-ENDS.

## Image builds

- **lchroot-patched ansible build validated: it produced a real Ubuntu 24.04 noble base
  rootfs (979M)** — controller-side debootstrap + the lchroot in-image connection both ran. The
  in-image **extend** (kernel + luna2-client) then hit a residual vars-file-load failure → the
  image is a partial base, not a complete compute image. This proves the lchroot build MECHANISM
  works end-to-end for the base; completing the extend is the next iteration.
- **rocky10: needs a base.yml adaptation (container-free)** — `alternative_distribution=Rocky-10`
  produced an el9 rootfs because the stock `redhat/base.yml` installs the CONTROLLER's release RPM.
  A container source is one route (absent here) but NOT required: `dnf --installroot` builds el10
  natively from an el9 host with `rocky-release-10` + `releasever=10` against the reachable el10
  repos. Parameterize the release-RPM + releasever in base.yml — the container-free fix.
- **ubuntu24 (debootstrap): build fix + real base rootfs produced** — `alternative_distribution=
  Ubuntu-24` wrongly triggered the osgrab path (`ubuntu_distribution_source undefined`); corrected
  to `-e ubuntu_distribution_release=noble -e ubuntu_distribution_source=<mirror>`. The final run
  **built a genuine Ubuntu 24.04 noble base rootfs (979M)** via the lchroot-patched ansible; the
  in-image EXTEND phase (kernel + luna2-client + trinity customization) then hit a residual
  vars-file-load failure, so the image had no kernel/luna2-client yet (partial base). OPEN: finish
  the ubuntu extend phase under the lchroot connection.
- **suse-latest / ubuntu26**: probes pass (15.6 reachable; `resolute`=26.04 confirmed live), full
  builds deferred behind the higher-signal findings above.

## Commits (local, both repos; not pushed — GitLab unreachable)

luna2-client `advanced-partitioner-stable`: regression suite (manifest/runner/de-hardcoded
drivers), park-code sync, create_deb 7 bins, BE-U1 auto-notui (+test). `make check` GREEN.
Installer repo: per-family module 10/30, module 25 (lchroot builds), module 70 (regression),
ansible-overlay (connection plugin + patch + probes + wrappers), README/conf.

## What's OPEN (honest next steps)
1. **Ubuntu**: initramfs-tools hook so lpart reaches the mkinitramfs initramfs (substantial).
2. **SUSE**: pvcreate exit-5 on SLE 15.5 (BE-A6c lvm-devices variant) — a SUSE-specific check.
3. **Rocky 10**: parameterize `redhat/base.yml` (target `rocky-release-10` + `releasever=10`
   against the el10 repos) so `dnf --installroot` builds el10 natively from the el9 controller —
   container-free (no podman). A container source is an alternative, not a requirement.
4. **Full image builds** (ubuntu24 debootstrap end-to-end, suse-latest, ubuntu26) once (1)-(3) land.
5. Run the packaged destructive suite at scale per distro once each distro installs.
