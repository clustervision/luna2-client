#!/bin/bash
# phase-marker-test.sh — regression test for the pre/part/post hook columns.
#
# Proves the node's pre/part/post *column scripts* actually execute, at the
# right point, by setting them on the controller to run the normal lpart phase
# AND drop a marker:
#   - pre  → echoes a marker line to the install log (sysroot not mounted yet)
#   - part → writes a marker into the INSTALL ROOT (/sysroot/root) after the OS
#            image is extracted, so it persists on the installed disk
#   - post → writes a marker into the install root after bootloader finalise
#
# After a disk install + boot, the part/post markers live at /root/ on the
# booted node (that was /sysroot/root during install) and the pre marker is in
# <node>.messages — three independent proofs the columns ran in order.
#
# The marker scripts run the real `lpart-phase <phase>` first and PRESERVE its
# exit code, so a genuine install failure still fails (the marker is best-effort
# and never masks it). This makes the test safe to leave configured.
#
# Usage:
#   phase-marker-test.sh set    <node>   # install the marker columns
#   phase-marker-test.sh verify <node>   # check the markers after install+boot
#   phase-marker-test.sh clear  <node>   # revert columns to the plain shims
#
# Then trigger an install (reboot the node) between `set` and `verify`.
set -u

TAG="LPART_PHASE_TEST"
LOGDIR="/var/log/cluster-messages"

emit_scripts() {
    local d="$1"
    cat > "$d/pre.sh" <<EOF
#!/bin/bash
/usr/bin/lpart-phase pre
rc=\$?
echo "$TAG: prescript executed on \$(hostname) at \$(date -u +%FT%TZ)"
exit \$rc
EOF
    cat > "$d/part.sh" <<EOF
#!/bin/bash
/usr/bin/lpart-phase part
rc=\$?
[ -d /sysroot/root ] && echo "$TAG partscript ran at \$(date -u +%FT%TZ)" > /sysroot/root/lpart-part-marker.txt
exit \$rc
EOF
    cat > "$d/post.sh" <<EOF
#!/bin/bash
/usr/bin/lpart-phase post
rc=\$?
[ -d /sysroot/root ] && echo "$TAG postscript ran at \$(date -u +%FT%TZ)" > /sysroot/root/lpart-post-marker.txt
exit \$rc
EOF
}

shim_scripts() {
    local d="$1" p
    for p in pre part post; do
        printf '#!/bin/bash\n# %s-group %sscript -- thin shim; all logic is in /usr/bin/lpart-phase.\nexec /usr/bin/lpart-phase %s\n' \
            "compute" "$p" "$p" > "$d/$p.sh"
    done
}

cmd="${1:-}"; node="${2:-}"
[ -n "$cmd" ] && [ -n "$node" ] || { grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

case "$cmd" in
  set)
    d=$(mktemp -d); emit_scripts "$d"
    luna node change "$node" -qpre "$d/pre.sh" -qpart "$d/part.sh" -qpost "$d/post.sh"
    echo "marker columns set on $node — now trigger an install (reboot it), then: $0 verify $node"
    rm -rf "$d"
    ;;
  clear)
    d=$(mktemp -d); shim_scripts "$d"
    luna node change "$node" -qpre "$d/pre.sh" -qpart "$d/part.sh" -qpost "$d/post.sh"
    echo "columns on $node reverted to the plain lpart-phase shims"
    rm -rf "$d"
    ;;
  verify)
    fail=0
    echo "=== pre  (install log) ==="
    if grep -h "$TAG: prescript executed" "$LOGDIR/$node.messages" 2>/dev/null | tail -1 | grep -q .; then
        grep -h "$TAG: prescript executed" "$LOGDIR/$node.messages" | tail -1; echo "  [OK] pre column ran"
    else echo "  [MISSING] no prescript marker in $LOGDIR/$node.messages"; fail=1; fi
    echo "=== part (install root /root/lpart-part-marker.txt) ==="
    if out=$(timeout 8 ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$node" 'cat /root/lpart-part-marker.txt 2>/dev/null') && [ -n "$out" ]; then
        echo "  $out"; echo "  [OK] part column ran (marker on installed disk)"
    else echo "  [MISSING] /root/lpart-part-marker.txt not on $node"; fail=1; fi
    echo "=== post (install root /root/lpart-post-marker.txt) ==="
    if out=$(timeout 8 ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$node" 'cat /root/lpart-post-marker.txt 2>/dev/null') && [ -n "$out" ]; then
        echo "  $out"; echo "  [OK] post column ran (marker on installed disk)"
    else echo "  [MISSING] /root/lpart-post-marker.txt not on $node"; fail=1; fi
    [ "$fail" = 0 ] && echo "=== ALL THREE COLUMNS VERIFIED ===" || echo "=== FAILED: some columns did not run ==="
    exit $fail
    ;;
  *) echo "unknown command: $cmd (use set|verify|clear)"; exit 2 ;;
esac
