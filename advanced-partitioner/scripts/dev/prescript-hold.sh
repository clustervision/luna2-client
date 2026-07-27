#!/bin/bash
# TEMPORARY re-entrancy-test prescript (node001 only). Runs the normal pre
# phase, then parks the node in the installer environment so an operator can
# SSH in and exercise lpart-storage-prepare re-entrancy by hand. Restore the
# original shim (saved as node001-prescript-orig.sh) when finished.
#
# Release: rm /tmp/REENTRANCY_HOLD on the node, or wait ~2h (self-release),
# after which pre returns and the install continues normally (part/post),
# leaving the node freshly installed and bootable.
LPART_BIN="${LPART_BIN:-/usr/bin}"
# stdin MUST be redirected away from this script. lpart-node-installer reads stdin
# (the TUI keyboard channel); without <>/dev/console it consumes the rest of
# this script (the hold loop below) as its stdin, so the hold never runs and the
# node installs straight through. This matches the production lpart-phase shim,
# which uses the same `<>/dev/console >&0 2>&1` redirection for all three phases.
"${LPART_BIN}/lpart-node-installer" --phase pre --bin-dir "${LPART_BIN}" \
    --log-file /tmp/install_files/logs/lpart-node-installer-pre.log <>/dev/console >&0 2>&1
echo "REENTRANCY HOLD: parked in installer. rm /tmp/REENTRANCY_HOLD to release." >/dev/console
touch /tmp/REENTRANCY_HOLD
i=0
while [ -f /tmp/REENTRANCY_HOLD ] && [ "$i" -lt 720 ]; do
    sleep 10
    i=$((i+1))
done
echo "REENTRANCY HOLD released; continuing to part/post." >/dev/console
exit 0
