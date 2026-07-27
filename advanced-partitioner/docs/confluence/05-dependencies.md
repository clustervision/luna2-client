---
title: Luna Installer — 05 Dependencies
confluence:
  id: '982286378'
  space: TRIX
  version: 2
  type: page
parent:
  id: '981499912'
---

# Luna Node Installer — Dependencies

*What this page is for: what the installer depends on, why, and when it is invoked.* There are three layers: the Go module's library dependencies, the **runtime toolset** the install initramfs must carry, and the per-distro package requirements.

## Dependency policy

The Go installer follows **standard-library-first**: a third-party dependency is added only when the stdlib is genuinely insufficient, and is justified in a decision record (ADR). The dependency surface is deliberately tiny; `govulncheck` runs in the gate (07) and must report zero known vulnerabilities on the shipped toolchain. Every binary is a **static, cgo-free** build, so there are no shared-library runtime dependencies on the node.

## Runtime toolset (carried in the install initramfs)

The `lpart-*` binaries are static, but they **shell out** to standard storage/boot tools through the `executor` chokepoint. `module-setup.sh` bakes these into the `95luna` initramfs via `dracut_install`. The load-bearing set:

| Group | Tools | Used by |
| --- | --- | --- |
| Partitioning | `sgdisk` / `gdisk`, `parted`, `partprobe`, `wipefs`, `blkid` | `lpart-storage-prepare` (`internal/partition`) |
| RAID | `mdadm` | `internal/raid` |
| LVM | `pvcreate`, `vgcreate`, `lvcreate`, `pvs`/`vgs`/`lvs`, `dmsetup` | `internal/lvm` |
| Filesystems | `mkfs.xfs`, `mkfs.ext4`, `mkfs.vfat`/`mkfs.fat`, `fatlabel` | `internal/fs` |
| Image | `tar`, `aria2c`, `curl`, `lbzip2` | `lpart-osimage-install` |
| Boot | `grub2`/`grub`, `efibootmgr`, the signed shim | `lpart-bootloader-finalise` (`internal/bootloader`) |
| Identity / debug | `tpm2-tools`, `dropbear`, `openssh`, `dmidecode`, `ipmitool` | `luna-installer.sh`, BMC setup |

The installer **probes for what a distro actually ships** (e.g. `mkfs.fat` vs `mkfs.vfat`) instead of assuming — which is how the same binary works across RHEL 8/9/10, Rocky, Alma, Debian and Ubuntu.

## Per-distro package requirements

Each packaging tree's spec/control declares the OS packages the client needs. Representative (the `redhat` spec and the `ubuntu` control):

| Distro | Declared (excerpt) |
| --- | --- |
| redhat | `dracut-config-generic`, `dracut-network`, `tpm2-tools`, `aria2`, `dropbear`, `mdadm`, `lvm2`, `gdisk`, `xfsprogs`, `dosfstools`, `grub2`, `lbzip2`, … |
| ubuntu | `dracut`, `dracut-network`, `mdadm`, `lvm2`, `gdisk`, `dosfstools`, `aria2`, `efibootmgr`, `dropbear-bin`, `tpm2-tools`, `lbzip2`, … |
| opensuse | `dracut`, `dracut-tools`, `mdadm`, `lvm2`, `gdisk`, `dropbear`, `tpm2.0-tools`, `aria2`, … |

These are the tools `dracut_install` then pulls into the initramfs; the package list and the `module-setup.sh` list are kept in step.

## Service dependencies

These are the controller-side services the install needs at runtime; their integration detail (protocols, auth, failure modes) is on **04 — Integrations**.

| Service | Role | What breaks without it |
| --- | --- | --- |
| Luna daemon (`:7050`) | the control plane | no token, no `install.sh` → no install |
| file server (`:7051`) | image transport | no OS image → install aborts |
| DHCP / PXE | get to the install kernel | node boots its disk instead |

## Build-time

The Go toolchain (cross-compiles `linux/amd64` + `linux/arm64`); the gate tools `golangci-lint` and `govulncheck` (07). No code generation, no cgo.

---
*Generated against `luna2-client@advanced-partitioner` on 10 Jun 2026. Next: 06 — Configuration Reference.*
