#!/bin/bash
# legacy-lint.sh — static regression guard for the legacy pre/part/post configs.
#
# Encodes the F1..F9 defects catalogued in LEGACY.configs as machine-checkable
# greps over the *actual* sources (luna2-daemon boot/scripts plugins, the
# trinityx roles .rd files, and the trinityx-docs mkdocs pages). Each check
# prints [BUG-PRESENT] or [FIXED] with a file:line, so this doubles as:
#   (a) the evidence behind the static-analysis section of the journal, and
#   (b) a regression guard that flips green once an upstream fix lands.
#
# Sources are clustervision repos (NOT modified here); override their locations
# with the env vars below. Exit status = number of BUG-PRESENT findings (0=all
# fixed), so CI/`make`-style callers can gate on it.
#
# Usage:  scripts/dev/legacy-lint.sh
# Env:    LEGACY_PLUGINS  dir with diskfull.py raid1.py nodhcp.py default.py
#         LEGACY_ROLES    trinityx site/roles dir
#         LEGACY_DOCS     trinityx-docs .../docs dir
set -u

# --- locate sources (first existing wins) ----------------------------------
pick(){ for d in "$@"; do [ -e "$d" ] && { echo "$d"; return; }; done; echo "$1"; }
PLUGINS="${LEGACY_PLUGINS:-$(pick \
  /trinity/local/python/lib/python*/site-packages/daemon/plugins/boot/scripts \
  /root/git/github-luna2-daemon/daemon/plugins/boot/scripts \
  /root/luna-v1.4-daemon-backup-*/daemon/plugins/boot/scripts)}"
ROLES="${LEGACY_ROLES:-$(pick /root/git/trinityx-combined/site/roles \
  /root/git/github-trinityX/site/roles)}"
DOCS="${LEGACY_DOCS:-$(pick /root/git/gitlab-trinityx-docs/trinityX/docs)}"

DF="$PLUGINS/diskfull.py"; R1="$PLUGINS/raid1.py"; ND="$PLUGINS/nodhcp.py"
HOWTO="$DOCS/howtos/disk-full-node.md"
echo "plugins: $PLUGINS"; echo "roles:   $ROLES"; echo "docs:    $DOCS"; echo

bugs=0
# present <F-id> <severity> <desc> <file> <regex>  — BUG-PRESENT if regex matches
present(){ local id="$1" sev="$2" desc="$3" f="$4" re="$5"
  if [ ! -f "$f" ]; then printf "  [SKIP ] %-3s %s (no %s)\n" "$id" "$desc" "$(basename "$f")"; return; fi
  local hit; hit=$(grep -nE "$re" "$f" | head -1)
  if [ -n "$hit" ]; then printf "  [BUG  ] %-3s (%s) %s\n           %s:%s\n" "$id" "$sev" "$desc" "$(basename "$f")" "$hit"; bugs=$((bugs+1))
  else printf "  [FIXED] %-3s %s\n" "$id" "$desc"; fi; }
# absent <F-id> <severity> <desc> <file> <regex> — BUG-PRESENT if regex is MISSING
absent(){ local id="$1" sev="$2" desc="$3" f="$4" re="$5"
  if [ ! -f "$f" ]; then printf "  [SKIP ] %-3s %s (no %s)\n" "$id" "$desc" "$(basename "$f")"; return; fi
  if grep -qE "$re" "$f"; then printf "  [FIXED] %-3s %s\n" "$id" "$desc"
  else printf "  [BUG  ] %-3s (%s) %s  (expected /%s/ in %s)\n" "$id" "$sev" "$desc" "$re" "$(basename "$f")"; bugs=$((bugs+1)); fi; }

echo "== raid1 boot mirroring & re-entrancy =="
# F1: raid1 must make the FULL boot chain disk-fault-tolerant -- mirror the ESP +
# /boot, not just root. The robust way is to RAID them (lpart: md on the ESP with
# metadata 1.0 so firmware still reads it as FAT, + md on /boot), so both legs
# stay in sync and either disk boots. development only RAIDs root (p4) and COPIES
# the ESP to /boot/efi2 while leaving /boot (kernel/initramfs) on sda2 alone (sdb2
# empty) -> NOT disk-fault-tolerant. HW-confirmed brick on sda loss (2026-06-07):
# the dual ESP is necessary but not sufficient. Flags BUG until the boot chain is
# RAIDed (the 'set 2 raid on' marker the proper fix would carry).
absent F1 BLOCKER "raid1 RAIDs the boot chain ESP/boot, not just root" "$R1" "set 2 raid on"
# F6: raid1 must zero stale md superblocks before create.
absent F6 BLOCKER "raid1 zeroes superblocks before mdadm --create"       "$R1" "zero-superblock"

echo; echo "== ESP filesystem / parted hygiene (all disk plugins) =="
present F3 robust  "FAT16 ESP (mkfs.fat -F 16) in diskfull"  "$DF" "mkfs\.fat +-F +16"
present F3 robust  "FAT16 ESP (mkfs.fat -F 16) in raid1"     "$R1" "mkfs\.fat +-F +16"
present F3 robust  "FAT16 ESP (mkfs.fat -F 16) in nodhcp"    "$ND" "mkfs\.fat +-F +16"
present F4 cosmet  "parted name typo (name 1\") in diskfull" "$DF" "name 1\""
present F4 cosmet  "parted name typo (name 1\") in nodhcp"   "$ND" "name 1\""
present F5 cosmet  "boot flag on p2 (set 2 boot on) diskfull" "$DF" "set 2 boot on"
present F5 cosmet  "boot flag on p2 (set 2 boot on) nodhcp"   "$ND" "set 2 boot on"

echo; echo "== doc example correctness =="
# F2: the howto's commented raid1 inspiration uses /dev/${DISK..}p1 (double /dev).
present F2 wontrun "howto raid1 example device-path bug (/dev/\${DISK)" "$HOWTO" "/dev/\\\$\{DISK"

echo; echo "== informational (design limits, expected red) =="
# F7: nodhcp hardcodes a device-name fstab (/dev/sda4 / ext4) instead of UUIDs.
#     (The current nodhcp DOES install grub/shim — that earlier concern is moot.)
present F7 robust  "nodhcp fstab hardcodes /dev/sdaN (not UUID)" "$ND" "/dev/sda4[[:space:]]+/[[:space:]]+ext4"
# F9: cloud post.rd fstabs root by device name, not UUID.
present F9 robust  "cloud fstab by device name (not UUID)"  "$ROLES/cloud/luna/files/post.rd" "\\\$\{DISK\}.* / +ext4"

echo
echo "=================================================="
echo "BUG-PRESENT findings: $bugs"
echo "(non-zero is expected until the upstream legacy scripts are fixed;"
echo " see LEGACY.configs 'TEST JOURNAL' for the fix plan.)"
exit "$bugs"
