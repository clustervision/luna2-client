#!/usr/bin/env bash
# Seam contract gate (FTA v2 CCF-7, barrier NB-3).
#
# The 2026-07-11 real-PXE regression proved that client<->daemon<->wrapper
# contracts fail only in composition: exit-code propagation (BE-S3/S4),
# config lifetime across retries (BE-S2), the dual-usage guard (BE-S5).
# This gate verifies the daemon-side seam AS DEPLOYED -- against the
# RENDERED install script the daemon actually serves (not a template copy)
# -- plus the wrapper's park codes. Run it against the lab controller
# before any release; the client-side seam is pinned by the Go tests
# (result_test.go, kmsg_backlog_test.go, TestPostPhaseSkipsBootloaderForSanitize).
#
# Usage: seam-contract-gate.sh [controller-ssh-host] [node]
#   defaults: 192.168.123.217 node001
# Requires: ssh root access to the controller; the controller's
# /trinity/local/luna/utils/config/luna.ini [API] credentials.
set -u

CTRL="${1:-192.168.123.217}"
NODE="${2:-node001}"
SSH="ssh -o StrictHostKeyChecking=no root@$CTRL"
fails=0
say() { echo "$@"; }
ok()  { say "PASS: $1"; }
bad() { say "FAIL: $1"; fails=$((fails+1)); }

# --- 1. fetch the RENDERED install script from the live daemon -------------
rendered="$(mktemp)"; trap 'rm -f "$rendered"' EXIT
$SSH '
INI=/trinity/local/luna/utils/config/luna.ini
U=$(awk -F"= *" "/^USERNAME/{print \$2}" $INI | tr -d " \r" | head -1)
P=$(awk -F"= *" "/^PASSWORD/{print \$2}" $INI | tr -d " \r" | head -1)
tok=$(curl -sk -X POST -H "Content-Type: application/json" \
      -d "{\"username\":\"$U\",\"password\":\"$P\"}" https://127.0.0.1:7050/token \
      | sed -n "s/.*\"token\" *: *\"\([^\"]*\)\".*/\1/p")
[ -n "$tok" ] || { echo NO-TOKEN >&2; exit 3; }
curl -sf -H "x-access-tokens: $tok" "http://127.0.0.1:7051/boot/install/'"$NODE"'"
' > "$rendered" 2>/dev/null
if [ ! -s "$rendered" ]; then
    bad "could not fetch the rendered install script for $NODE from $CTRL"
    echo "== GATE FAILED =="; exit 1
fi
ok "fetched rendered install script ($(wc -c < "$rendered") bytes)"

# --- 2. exit-code propagation (BE-S4): exit \$LUNARET, never a collapse ----
n=$(grep -c 'exit \$LUNARET' "$rendered")
[ "$n" -ge 4 ] && ok "exit \$LUNARET present in all phase handlers ($n)" \
               || bad "exit \$LUNARET count $n < 4 -- the template collapses exit codes again (BE-S4)"
if grep -nE '^\s*exit 1\s*$' "$rendered" | grep -q .; then
    bad "bare 'exit 1' found in the rendered script -- check the phase handlers"
else
    ok "no bare 'exit 1' collapse in the rendered script"
fi

# --- 3. dual-usage guard (BE-S5): defined, called 3x, block path present ---
[ "$(grep -c 'lpart_dual_usage_guard' "$rendered")" -ge 4 ] \
    && ok "dual-usage guard defined + called for pre/part/post" \
    || bad "dual-usage guard missing from the rendered script (BE-S5 / NB-2)"
grep -q 'install.blocked.dual_' "$rendered" \
    && ok "guard hard-block path present (install.blocked.dual_*)" \
    || bad "guard block path missing -- warn-only regression (MCS-13)"

# --- 4. guard BEHAVIOUR against the rendered function (not a copy) ---------
guard_src="$(mktemp)"; trap 'rm -f "$rendered" "$guard_src"' EXIT
{
    echo 'function update_status { echo "STATUS:$1"; }'
    sed -n '/^function lpart_dual_usage_guard {/,/^}/p' "$rendered"
} > "$guard_src"
field="$(mktemp)"; echo 'mke2fs -t ext4 /dev/sda1' > "$field"
out=$(bash -c ". '$guard_src'; lpart_dual_usage_guard part '$field'" 2>&1); rc=$?
rm -f "$field"
[ "$rc" = "2" ] && grep -q BLOCKED <<<"$out" \
    && ok "rendered guard BLOCKS disk-mutating content with exit 2" \
    || bad "rendered guard did not block (rc=$rc) -- deployed template drifted"

# --- 5. wrapper park codes (BE-S3/S4 downstream): parks on rc 2 AND 3 ------
$SSH 'M=/trinity/images/compute-image/usr/lib/dracut/modules.d/95luna/luna-installer.sh
      grep -qE "LUNA_FATAL_RC=3" $M && grep -qE "LUNA_SAFETY_RC=2" $M' \
    && ok "wrapper park codes (LUNA_SAFETY_RC=2 / LUNA_FATAL_RC=3) present in the deployed 95luna" \
    || bad "wrapper park codes missing from the deployed 95luna wrapper"

echo
if [ "$fails" -gt 0 ]; then echo "== SEAM CONTRACT GATE FAILED ($fails) =="; exit 1; fi
echo "== SEAM CONTRACT GATE PASSED =="
