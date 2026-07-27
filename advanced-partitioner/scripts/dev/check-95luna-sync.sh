#!/bin/bash
# check-95luna-sync.sh -- gate: the per-family 95luna dracut module trees must
# stay BYTE-IDENTICAL. There is one 95luna module; redhat/opensuse/ubuntu each
# carry a copy only because the packaging trees are per-family. Any divergence
# is drift (the 2026-07 audit found ubuntu had silently dropped `efibootmgr`
# from an `inst_multiple -o` list -- harmless-looking, but the trees then rot
# apart). Distro differences belong in runtime capability checks or data
# tables, never in per-family copies of this module.
#
# Run from anywhere; exits non-zero (and prints the diff) on divergence.
# Wired into `make check` via the 95luna-sync target.
set -u
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)   # luna2-client repo root
REF="redhat/x86/src/usr/lib/dracut/modules.d/95luna"

rc=0
for other in opensuse ubuntu; do
    tree="${other}/x86/src/usr/lib/dracut/modules.d/95luna"
    if ! diff -ru "$ROOT/$REF" "$ROOT/$tree"; then
        echo "FAIL: $tree diverged from $REF (see diff above)" >&2
        rc=1
    fi
done
[ $rc -eq 0 ] && echo "OK: 95luna trees are byte-identical (redhat == opensuse == ubuntu)"
exit $rc
