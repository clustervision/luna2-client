#!/bin/bash
# legacy-battery.sh — controller-side driver for the legacy pre/part/post test
# campaign (install_mode=legacy). Companion to reentrancy-battery.sh, but where
# that one drives lpart-storage-prepare directly on a memboot node, legacy mode is
# DAEMON-ONLY (the lpart-* tools never run) — so this driver goes through full PXE
# install cycles: set the node's legacy scripts, clear NVRAM, reboot to PXE,
# wait for the install, then verify and (for raid1) exercise dd-failover.
#
# Findings are appended to LEGACY.configs (the journal) by hand from the
# verify output; this script only runs the steps and prints structured results.
#
# SUBCOMMANDS
#   set-plugin  <node> <plugin>         luna node change --install-mode legacy --scripts <plugin>
#   set-tmpfs   <node> <part.rd> <post.rd>   diskless-by-hand (-qpart/-qpost)
#   pxe-reboot  <node>                  clear disk NVRAM + reboot -> PXE install
#   wait        <node> [secs]           poll until the node is back up post-install
#   verify      <node>                  findmnt / + lsblk + mdstat + efibootmgr -v
#   nvram       <node>                  efibootmgr -v (NVRAM analysis, diskfull §F8)
#   pull        <node> <disk>           DESTRUCTIVE: dd-wipe a leg + sysrq-reboot
#                                       (raid1 failover; <disk> must be approved)
#
# SAFETY
#   * Destructive ops (pull) require APPROVED_DISKS to list the disk AND an
#     interactive "yes". Default approved set: sda sdb sdc sdd (node001).
#   * node001 IPMI is DEAD: recovery from a bad boot is PXE-only. `pull` clears
#     NVRAM so the node PXEs back (reinstallable) rather than bricking.
#   * Never targets a non-approved disk; never runs sanitize.
#
# Usage examples (raid1 dd-failover, mirrors regression.md §3.14):
#   legacy-battery.sh set-plugin node001 raid1
#   legacy-battery.sh pxe-reboot node001 && legacy-battery.sh wait node001
#   legacy-battery.sh verify node001
#   legacy-battery.sh pull node001 sdb      # pull 2nd leg -> expect degraded boot
#   legacy-battery.sh wait node001 && legacy-battery.sh verify node001
#   # restore (reinstall), then:
#   legacy-battery.sh pull node001 sda      # pull 1st leg -> F1 predicts BRICK
set -u

APPROVED_DISKS="${APPROVED_DISKS:-sda sdb sdc sdd}"
LOGHOST_DIR="${LOGHOST_DIR:-/var/log/cluster-messages}"   # memory: <host>.cluster.messages
DAEMON="${DAEMON:-https://127.0.0.1:7050}"
SH(){ local n="$1"; shift; ssh -o BatchMode=yes -o ConnectTimeout=8 "$n" "$@"; }
die(){ echo "ABORT: $*" >&2; exit 2; }
approved(){ for d in $APPROVED_DISKS; do [ "$d" = "$1" ] && return 0; done; return 1; }

# HA overrule (a solo controller rejects `luna node change` with "not in sync").
ha_overrule(){
  local tok; tok=$(cat /trinity/local/luna/.token 2>/dev/null || echo "")
  [ -n "$tok" ] && curl -sk -H "x-access-tokens: $tok" "$DAEMON/ha/overrule/_set" >/dev/null 2>&1
}

cmd="${1:-}"; shift || true
case "$cmd" in

set-plugin)
  node="${1:?node}"; plugin="${2:?plugin (default|diskfull|raid1|nodhcp)}"
  ha_overrule
  echo "+ luna node change --install-mode legacy --scripts $plugin $node"
  luna node change --install-mode legacy --scripts "$plugin" "$node" \
    || die "luna node change failed (HA overrule? token?)"
  echo "set. confirm with: luna node show $node | grep -Ei 'install|script'"
  ;;

set-tmpfs)
  node="${1:?node}"; part="${2:?part.rd}"; post="${3:?post.rd}"
  ha_overrule
  echo "+ luna node change --install-mode legacy -qpart $part -qpost $post $node"
  luna node change --install-mode legacy -qpart "$part" -qpost "$post" "$node" \
    || die "luna node change failed"
  ;;

pxe-reboot)
  node="${1:?node}"
  # Force exactly one PXE boot via BootNext -> the EFI Network entry. Robust against
  # disk-entry label drift: TrinityX labels its disk entries "TrinityX sda/sdb/..."
  # (not "rocky"/"redhat"), so deleting by distro keyword matched nothing and the node
  # local-booted instead of PXEing. BootNext is one-shot, so it self-clears after the
  # install rewrites NVRAM. (memory: feedback-force-pxe)
  net=$(SH "$node" 'efibootmgr | grep -iE "EFI Network|PXE|IPv4|IPv6|Network" | grep -oE "^Boot[0-9A-Fa-f]+" | grep -oE "[0-9A-Fa-f]+$" | head -1')
  if [ -n "${net:-}" ]; then
    echo "+ setting BootNext=$net (EFI Network) on $node for a one-shot PXE boot"
    SH "$node" "efibootmgr -n $net" 2>&1 | grep -i BootNext || true
  else
    echo "+ no EFI Network entry found; falling back to deleting TrinityX disk entries"
    SH "$node" 'for b in $(efibootmgr | grep -iE "TrinityX|Shim1|rocky|redhat|alma|centos" | grep -oE "^Boot[0-9A-F]+" | grep -oE "[0-9A-F]+$"); do efibootmgr -B -b $b; done' 2>&1 | tail -3 || true
  fi
  echo "+ rebooting $node into PXE install"
  SH "$node" 'reboot' || SH "$node" 'echo 1 > /proc/sys/kernel/sysrq; echo b > /proc/sysrq-trigger' || true
  echo "tail the install on the controller:"
  echo "  tail -F $LOGHOST_DIR/$node.cluster.messages | grep -E 'lpart-install|install\.|DISKFULL|RAID1'"
  ;;

wait)
  node="${1:?node}"; budget="${2:-1200}"; t=0
  echo "+ waiting up to ${budget}s for $node to come back up"
  while [ "$t" -lt "$budget" ]; do
    if SH "$node" true 2>/dev/null; then echo "  up after ${t}s"; exit 0; fi
    sleep 15; t=$((t+15)); echo -n "."
  done
  echo; die "$node did not return within ${budget}s"
  ;;

verify)
  node="${1:?node}"
  echo "===== verify $node ====="
  echo "-- root fs --";    SH "$node" 'findmnt -no SOURCE,FSTYPE /' 2>&1
  echo "-- block tree --"; SH "$node" 'lsblk -o NAME,SIZE,FSTYPE,MOUNTPOINT 2>/dev/null' 2>&1
  echo "-- mdstat --";     SH "$node" 'cat /proc/mdstat 2>/dev/null | grep -E "^md|blocks|_U|U_|UU" || echo "(no md)"' 2>&1
  echo "-- fstab --";      SH "$node" 'cat /etc/fstab 2>/dev/null | grep -vE "^\s*#|^\s*$"' 2>&1
  echo "-- NVRAM --";      SH "$node" 'efibootmgr -v 2>/dev/null | grep -iE "BootOrder|BootCurrent|Shim|rocky|redhat|EFI" | head' 2>&1
  ;;

nvram)
  node="${1:?node}"
  echo "===== NVRAM (efibootmgr -v) on $node — diskfull §F8 analysis ====="
  SH "$node" 'efibootmgr -v' 2>&1
  echo "-- removable-media fallback present? (new installer adds EFI/BOOT/BOOTX64.EFI) --"
  SH "$node" 'ls -l /boot/efi/EFI/BOOT/BOOTX64.EFI 2>/dev/null || echo "NO BOOTX64.EFI fallback (legacy diskfull does not create one)"' 2>&1
  ;;

pull)
  node="${1:?node}"; disk="${2:?disk e.g. sdb}"
  approved "$disk" || die "$disk is not in APPROVED_DISKS ($APPROVED_DISKS)"
  echo "!!! DESTRUCTIVE: wipe /dev/$disk header on $node, then sysrq-reboot."
  echo "!!! node001 IPMI is dead; recovery is PXE-only. NVRAM is cleared first so it PXEs back."
  read -r -p "type 'yes' to proceed: " ok; [ "$ok" = "yes" ] || die "declined"
  # Clear disk NVRAM so a failed degraded boot falls back to PXE (recoverable).
  # Match the real TrinityX disk-entry labels (NOT distro keywords — see pxe-reboot).
  SH "$node" 'for b in $(efibootmgr | grep -iE "TrinityX|Shim1|rocky|redhat|alma|centos" | grep -oE "^Boot[0-9A-F]+" | grep -oE "[0-9A-F]+$"); do efibootmgr -B -b $b; done' 2>&1 | tail -2 || true
  echo "+ dd-wiping /dev/$disk superblock + sysrq reboot"
  SH "$node" "dd if=/dev/zero of=/dev/$disk bs=1M count=10 2>&1; sync; echo 1 > /proc/sys/kernel/sysrq; echo b > /proc/sysrq-trigger" || true
  echo "node is rebooting. expected for raid1 root mirror: [2/1] [_U] degraded."
  echo "  pull of the 2nd leg (ESP/boot survivor present): should boot degraded."
  echo "  pull of the 1st leg (ESP/boot ONLY on it — F1): expected UNBOOTABLE -> PXE."
  ;;

*)
  grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//' | sed -n '1,60p'
  ;;
esac
