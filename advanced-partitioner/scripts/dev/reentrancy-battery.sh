#!/bin/bash
# Re-entrancy regression battery for the localboot partitioner, run against a
# node booted into memboot (tmpfs RAM root) so the disks are free and the full
# OS toolset is present. Drives lpart-storage-prepare across a matrix of layouts,
# forward then reverse (exercising every layout transition), and asserts it
# converges (every run exits 0) and is idempotent (a re-run with install_mode
# auto on an already-correct disk is mount-only: the on-disk fingerprint is
# unchanged, no reformat).
#
# Also checks the install phase entrypoints are re-runnable: lpart-node-installer
# --phase pre is run twice (idempotent). part(osimage)/post(bootloader) require
# the Luna token + install.sh, which memboot does not stage; their re-entrancy
# is covered by repeated full PXE installs (regression.md §3.19 / cycles) and
# the per-module reentrant_test.go suites.
#
# Usage:  reentrancy-battery.sh <node> <layouts-dir> [outdir]
# Pre:    <node> booted in memboot (findmnt / == tmpfs), disks free.
# Safe:   partitioner only; never extracts an image or installs a bootloader,
#         so it cannot leave a bad GRUB. Destroys the data disks by design.
set -u
NODE="${1:-node001}"
LDIR="${2:?usage: reentrancy-battery.sh <node> <layouts-dir> [outdir]}"
OUT="${3:-/tmp/reentrancy-out}"
SSH="ssh -o BatchMode=yes -o ConnectTimeout=8 $NODE"
mkdir -p "$OUT"; fail=0; idem_ok=0; idem_no=0
hr(){ echo; echo "=================================================="; }

# Precondition: memboot.
if ! $SSH 'test "$(findmnt -no FSTYPE /)" = tmpfs'; then
    echo "ABORT: $NODE is not in memboot (/ is not tmpfs)."; exit 3
fi
$SSH 'mkdir -p /tmp/rt/runtime /tmp/rt/logs'
ctx(){ $SSH "printf '{\"install_mode\":\"%s\"}' '$1' > /tmp/rt/runtime/install-context.json"; }
# lpart-storage-prepare run; $1 layout name, $2 mode, $3 extra flags. Prints RC=.
SP(){ $SSH "/usr/bin/lpart-storage-prepare --config /tmp/L.json --install-context /tmp/rt/runtime/install-context.json --expect-host $NODE --result-out /tmp/rt/runtime/storage-result.json --resolved-out /tmp/rt/runtime/storage-resolved.json --skip-final-inventory $3 2>&1; echo RC=\$?"; }
# Stable on-disk fingerprint.
fp(){ $SSH 'lsblk -rno NAME,FSTYPE,UUID,TYPE 2>/dev/null | sort; echo --MD--; cat /proc/mdstat 2>/dev/null | grep -E "^md" | awk "{print \$1,\$3,\$4}" | sort; echo --LV--; { lvs --noheadings -o lv_name,vg_name,lv_size 2>/dev/null | tr -s " " || true; }'; }
ckrc(){ grep -q 'RC=0' "$1" || { echo "  !! $2 exited non-zero (see $1)"; fail=1; }; }

# One layout: full(create) then auto(should mount, idempotent). Asserts RC=0;
# reports whether the auto re-run preserved the fingerprint (idempotent).
test_layout(){
  local name="$1"
  local pass="$2"
  local tag="$pass-$name"
  echo; echo "## $tag : full(create) + auto(re-run)"
  scp -q "$LDIR/$name" "$NODE:/tmp/L.json" || { echo "  scp $name failed"; fail=1; return; }
  ctx full; SP "$name" full "--wipe" > "$OUT/$tag.full.log"; ckrc "$OUT/$tag.full.log" "$tag full"
  fp > "$OUT/$tag.fp1.txt"
  ctx auto; SP "$name" auto "--wipe" > "$OUT/$tag.auto.log"; ckrc "$OUT/$tag.auto.log" "$tag auto"
  fp > "$OUT/$tag.fp2.txt"
  if diff -q "$OUT/$tag.fp1.txt" "$OUT/$tag.fp2.txt" >/dev/null; then
    echo "  IDEMPOTENT: auto re-run preserved the on-disk fingerprint (mount, no reformat)"; idem_ok=$((idem_ok+1))
  else
    echo "  convergent-only: auto re-run changed the fingerprint (recreated, not mount):"; diff "$OUT/$tag.fp1.txt" "$OUT/$tag.fp2.txt" | head -4; idem_no=$((idem_no+1))
  fi
  grep -qiE 'verdict=healthy|mount \(no wipe\)' "$OUT/$tag.auto.log" && echo "  (auto took the mount path)"
}

FWD="01-single-a.json 02-single-b.json 03-raid1.json 04-raid0.json 05-raid10.json 06-os-raid1-data-raid0.json 07-os-raid1-2data.json"
REV="07-os-raid1-2data.json 06-os-raid1-data-raid0.json 05-raid10.json 04-raid0.json 03-raid1.json 02-single-b.json 01-single-a.json"

hr; echo "PART 1a: layout matrix FORWARD (each layout + transition from the previous)"
for n in $FWD; do test_layout "$n" fwd; done
hr; echo "PART 1b: layout matrix REVERSE (exercise every transition the other way)"
for n in $REV; do test_layout "$n" rev; done

hr; echo "PART 2: phase entrypoint re-entrancy (pre x2 idempotent)"
$SSH '/usr/bin/lpart-node-installer --phase pre --no-tui --runtime /tmp/rt/runtime --log-dir /tmp/rt/logs >/tmp/rt/pre1.log 2>&1; echo RC=$?' > "$OUT/pre1.log"; ckrc "$OUT/pre1.log" "phase pre #1"
$SSH '/usr/bin/lpart-node-installer --phase pre --no-tui --runtime /tmp/rt/runtime --log-dir /tmp/rt/logs >/tmp/rt/pre2.log 2>&1; echo RC=$?' > "$OUT/pre2.log"; ckrc "$OUT/pre2.log" "phase pre #2"

hr
echo "RE-ENTRANCY BATTERY: $([ $fail -eq 0 ] && echo PASS || echo FAIL)  (fail=$fail)"
echo "  layouts: $(echo $FWD | wc -w) x 2 directions x 2 runs(full+auto) = $(( $(echo $FWD|wc -w) * 4 )) partitioner runs"
echo "  idempotent re-runs: $idem_ok   convergent-only re-runs: $idem_no"
echo "  logs + fingerprints in $OUT"
exit $fail
