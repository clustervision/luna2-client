Name:          luna2-client
Version:       %{?luna_version}%{!?luna_version:2.2}
Release:       %{?luna_release}%{!?luna_release:4}
Group:         Unspecified
Summary:       Kernel module for Luna2-client to deploy nodes

Packager:      ClusterVision

License:       GNU GPLv2 
URL:           https://github.com/clustervision/luna


Provides:      libluna-fakeuname.so()(64bit)  
Provides:      luna2-client = %{version}.%{release}
# Provides:      luna2-client(x86-64) = %{version}.%{release}
Requires:      tpm2-tools
Requires:      aria2
Requires:      curl  
Requires:      dracut-config-generic  
Requires:      dracut-network  
Requires:      e2fsprogs  
Requires:      xfsprogs  
Requires:      gawk  
Requires:      grub2  
Requires:      gzip  
Requires:      ipmitool  
Requires:      kernel  
Requires:      ((lbzip2) or (pbzip2))
Requires:      nss  
Requires:      nss-softokn  
Requires:      openssh  
Requires:      openssh-clients  
Requires:      openssh-server  
Requires:      dropbear
Requires:      parted  
Requires:      rootfiles  
Requires:      rsync  
Requires:      sed  
Requires:      tar  
Requires:      vim-minimal  
Requires:      wget
Requires:      coreutils
Requires:      util-linux
Requires:      bind-utils
Requires:      mdadm
Requires:      procps-ng
Requires:      pciutils
Requires:      dosfstools
Requires:      dmidecode
Requires:      util-linux
Requires:      device-mapper
Requires:      dmidecode
Requires:      dosfstools
Requires:      findutils
Requires:      gdisk
Requires:      jq
Requires:      lsscsi
Requires:      lvm2

%clean
echo "No need to remove the source"

%description
Dracut module for Luna deployment tool

%files
%dir %attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/bashrc"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/luna-parse-cmdline.sh"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/luna2-start.sh"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/luna-installer.sh"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/module-setup.sh"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/lpart-phase"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/profile"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/sshd_config"
%attr(0755, root, root) "/usr/lib64/libluna-fakeuname.so"
%attr(0755, root, root) "/etc/dracut.conf.d/luna2.conf"
%attr(0755, root, root) "/usr/sbin/dhclient-script-luna"
%attr(0755, root, root) "/usr/bin/lpart-storage-prepare"
%attr(0755, root, root) "/usr/bin/lpart-storage-check"
%attr(0755, root, root) "/usr/bin/lpart-osimage-install"
%attr(0755, root, root) "/usr/bin/lpart-bootloader-finalise"
%attr(0755, root, root) "/usr/bin/lpart-node-installer"
%attr(0755, root, root) "/usr/bin/lpart-emit"
%attr(0755, root, root) "/usr/bin/lpart-tui"

%changelog
* Tue Jun 09 2026 Alex Ninaber
- Package the shared installer body: add luna-installer.sh to %files. The
  95luna module-setup.sh installs it from $moddir into the initramfs at build
  time, so it must ship in the RPM; it was omitted when luna2-start.sh was
  split into a thin wrapper + the byte-identical shared body. Also carries the
  dropbear /etc/shells SSH-into-installer fix.
* Sun Jun 08 2026 Alex Ninaber
- ADR 0009 (decoupled install dashboard): add lpart-tui (standalone renderer
  that reads the /run/lpart/events bus) and lpart-emit (bash-facing producer +
  --pipe worker→bus bridge). luna2-start.sh gains a default-OFF lpart.hosttui
  path that hosts one lpart-tui around the whole install.sh; the deployed path
  is unchanged when the flag is absent. Additive; the structural-template that
  drives the bus is a separate (daemon) change.
* Sun Jun 07 2026 Alex Ninaber
- selection auto->discover rename; add the save flag (pin a discovered layout
  back to the node-level disklayout as manual+origin) and the lunaclient
  write-back. See advanced-partitioner ADR-0008.
* Sat Jun 06 2026 Alex Ninaber
- rsyslog install narrative: banner (node/hw facts/install_mode/plan) + kernel
  ring-buffer (dmesg) snapshot at install start, then live per-stage progress.
* Sat Jun 06 2026 Alex Ninaber
- Redact LUNA_TOKEN (x-access-tokens) from executor command logging so it no
  longer reaches cluster-messages via the live rsyslog forward.
* Sat Jun 06 2026 Alex Ninaber
- Live rsyslog forward of installer content (lpart-install[phase/stage] stream,
  node-FQDN stamped); kernel capture widened to info+. Data-fence decision
  extracted + unit-tested; regression.md rewritten for v1.4.
* Sat Jun 06 2026 Alex Ninaber
- v1.4 storage/provisioning model: single install_mode enum
  (auto|sync|full|local|memboot|sanitize|legacy), tag/size disk selection,
  the section-5 decision engine + data fence, secure-erase (sanitize), and the
  memboot RAM-root path. lpart-discover removed (5 lpart-* binaries now). The tx_*
  compat shim was dropped (ADR-0006).
* Thu Jun 04 2026 Alex Ninaber
- TUI: Up/Dn (and PgUp/PgDn) now scroll the Run-output panel (the install log,
  which actually overflows) instead of the disk table (which fits); the panel
  title shows "[scrolled +N - Dn to follow]" when paged back. Post phase now
  repopulates the Luna/Discovery/Storage panels (emitNode/emitStorage/emitLuna +
  markLayout) so the bootloader-finalise dashboard is no longer empty.
* Thu Jun 04 2026 Alex Ninaber
- lpart-bootloader-finalise: make degraded RAID1 boot deterministic. grub2-mkconfig
  locates /boot+root via the md-array UUID (mduuid/...), which GRUB resolves
  non-deterministically when a mirror leg is missing -> it intermittently drops
  to the grub> rescue prompt (seen on the RAID1 sdb-fail failover test). Post-
  process /boot/grub2/grub.cfg to strip the `set root/boot='mduuid/..'` lines and
  the `--hint='mduuid/..'` tokens, so the fs-uuid search resolves by scanning -
  a metadata-1.0 member is firmware-readable and found without md assembly. No-op
  on non-RAID layouts.
* Thu Jun 04 2026 Alex Ninaber
- Cleanup/refactor (behaviour-neutral): lpart-node-installer is now --phase-only;
  removed the dormant child-process "wrap an external installer" path and the
  whole TX_* marker / state-file subsystem (internal/discovery, engine Runner +
  state poller). Retired lpart-install-context (its install-context.json round-trip
  was redundant -- every lpart-* binary already reads+validates the file lpartinputs
  wrote). The three Luna hook scripts collapse to one-liners calling a shared
  /usr/bin/lpart-phase. Dropped the retired HTTP-toolbox scripts + Makefile target.
  Removed 10 dead funcs. Renamed the Go module path off alex/installer-go to
  clustervision/luna2-client/advanced-partitioner. make check green.
* Thu Jun 04 2026 Alex Ninaber
- TUI: remove F1 Pause (the "next stage" boundary was unclear amid activity);
  F2 Shell now prints "install continues in the background" and redraws on
  return. Replace all remaining Unicode that the VGA console renders as a solid
  block (em-dash placeholder, arrow, multiply, ellipsis, footer arrows) with
  ASCII -- empty values now show "-" instead of a block.
* Thu Jun 04 2026 Alex Ninaber
- lpart-node-installer: add a plain (non-TUI) mode that streams normal scrolling
  boot output — stage banners, "$ argv" lines, child stdout/stderr, kernel
  warnings. Toggled by kernel cmdline `lpart.notui` (set via a Luna group/node
  kernel option), the --no-tui flag, or $LPART_NOTUI. Exit-code contract matches
  the TUI (0 success / 1 failure).
- TUI layout reorg: drop the Node panel (Arch + Vendor/Model fold into Luna);
  Discovery moves to the top-right, Progress + Storage share the middle row,
  and Run output spans the full width at the bottom (the most space). Remove
  the "Partition / Image / Boot" header line. Drop the disk-row selection
  cursor/highlight (the list still scrolls; a non-empty Set means "used").
  Fix the "(resolving)" ellipsis + footer arrows (ASCII). Every panel clips
  to its box so long lines can't break the borders.
* Thu Jun 04 2026 Alex Ninaber
- TUI Node panel: drop the "Boot mode" row (UEFI is the supported path and
  already shows in the Method/Summary line); leaves 4 rows matching Luna.
* Thu Jun 04 2026 Alex Ninaber
- lpart-node-installer TUI: hardware "Discovery" panel beside the disk table
  (CPU model + topology vCPU/sockets/cores/threads, memory, GPU count/type,
  NICs, disk-type counts) gathered from /proc + /sys (no lscpu/lspci dep);
  Node panel slimmed to identity (it was clipping CPU/GPU off-screen). Drop
  the storage table's Use column -- a non-empty Set already means the disk is
  used. All status/check glyphs are ASCII now (the node's VGA console rendered
  the Unicode checkbox/check/ellipsis as a solid block). Disk ID column shows
  serial -> WWN -> shortened by-path (strip pci-0000:) -> model. Per-disk
  detail moved to a one-line "selected:" in Discovery; blank line under Method.
* Thu Jun 04 2026 Alex Ninaber
- lpart-bootloader-finalise: sanitise BLS entries (/boot/loader/entries) on the
  node after extract. A kernel installed into the osimage while chrooted at
  its build path (e.g. the el9_8 kernel added 2026-05-31) leaves the newest
  BLS entry pointing linux/initrd at /trinity/images/compute-image/boot/... and
  carrying a build-time root=/dev/mapper/vg_rhel-lv_root. GRUB defaults to the
  newest kernel -> "vmlinuz not found" -> rescue on firmware disk-boot. The
  finaliser now strips the build-path prefix (keeping the path relative to the
  boot fs) and the build-time root=/rd.lvm.lv=/resume= tokens, so the entry
  inherits the node's grub2-mkconfig-probed kernelopts. Generalises the old
  separate-/boot-only normaliser; covers single-disk and RAID, UEFI and BIOS.
- lpart-node-installer TUI storage panel: only show real install targets. Drop
  the installer's own memory-staging row (no more "Memory-backed install /
  /mnt/osimg" / Backing / Mount path / Limit); tmpfs is listed only when the
  OS actually targets RAM. New columns: Set (OS/DATA/DATA1..) and RAID level
  per disk, a disk ID (serial) column, and a one-line layout Summary
  (OS -> sda,sdb (RAID1) ... Boot: UEFI/GRUB + NVRAM) under the table.
* Thu Jun 04 2026 Alex Ninaber
- capture kernel messages (/dev/kmsg, warn+) into the TUI Run output instead
  of letting them scroll the console; fix engine FanIn to merge >1 aux channel
- TUI footer: F1 Pause and F10 Reboot now actually work (real engine pause /
  confirmed reboot); drop the non-functional F5 Discovery; add a spacer line
  above the keybindings

* Thu Jun 04 2026 Alex Ninaber
- TUI install method shows the detected firmware (UEFI/BIOS) instead of the
  config's boot_firmware "auto" -> e.g. "RAID1 . UEFI"

* Thu Jun 04 2026 Alex Ninaber
- lpart-node-installer TUI: populate the Luna panel (controller/IP/hostname/
  image) and show the resolved install method (e.g. "RAID1 . UEFI") + every
  disk the layout writes to (Use column); cap the storage panel to its
  content so the bottom panels + footer fill the screen

* Wed Jun 03 2026 Alex Ninaber
- luna2-start.sh: only run the retry countdown on a FAILED attempt; a
  successful install no longer prints a spurious "INSTALLER FAILED" banner

* Wed Jun 03 2026 Alex Ninaber
- lpart-node-installer gains an in-process --phase pre/part/post pipeline so the
  Luna pre/part/post scripts collapse to a one-line exec; tx_* inputs resolved
  from the Luna daemon (curl) with a write_tx_inputs file fallback
- luna2-start.sh: quiet kernel printk on the install console, snapshot dmesg
  on failure, and replace the silent retry sleep with an interruptible
  "INSTALLER FAILED -- retrying in 30s" countdown (any key -> shell)

* Wed Jun 03 2026 Alex Ninaber
- bundle the advanced-partitioner localboot installer (lpart-* Go binaries)
  into the 95luna initramfs; built by create_rpm.sh, shipped in %files

* Tue May 26 2026 Antoine Schonewille
- added device-mapper, dmidecode, dosfstools, findutils,
  gdisk, jq, lsscsi, lvm2

* Fri Nov 28 2025 Antoine Schonewille
- added pbzip2 and removed dhcp-client as a require

* Mon Feb 10 2025 Antoine Schonewille
- added a log.prev to add some rotation to install logs

* Tue Sep 17 2024 Antoine Schonewille
- added dropbear lightweight ssh server support

* Fri Aug 30 2024 Antoine Schonewille
- added logger and logging support

* Fri Jul 05 2024 Antoine Schonewille
- added dhclient-script-luna

* Fri Feb 23 2024 Antoine Schonewille
- added ipv6 compliancy

* Thu Feb 01 2024 Antoine Schonewille
- added aria2c and dmidecode into ramdisk

* Fri Aug 18 2023 Antoine Schonewille
- Adding real TPM support; using sha256 for now
- adding tpm2-tools / tpm2_pcrread
- improvements on install script fetch handling

* Wed Nov 23 2022 Matthew Steggink
- Rewrite of luna2 client

* Tue Mar 16 2021 Bart Lamboo
- Added lbzip2 dependency including dracut configuration extension

* Wed Feb 17 2021 Bart Lamboo
- Redirected both stdin / stderr to console and logfile

