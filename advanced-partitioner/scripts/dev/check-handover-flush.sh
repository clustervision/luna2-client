#!/bin/bash
# check-handover-flush.sh -- gate: every installer hand-over (each luna_finish, dracut
# and initramfs-tools alike) calls `lpart-phase finish` before it writes the finished
# marker. Without it the installed disk is handed over unflushed and a power cut in
# the next seconds loses the install's last writes.
# Run from anywhere; exits non-zero and names each copy that misses the call.
set -u
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)   # luna2-client repo root
rc=0; n=0
while IFS= read -r f; do
    n=$((n + 1))
    body=$(awk '/^ *(function )?luna_finish *\(\)/{p=1} p{print} p && /^}/{exit}' "$f")
    flush=$(printf '%s\n' "$body" | grep -n 'lpart-phase finish' | head -1 | cut -d: -f1)
    mark=$(printf '%s\n' "$body" | grep -n '/run/luna-install-finished' | grep -v '#' | head -1 | cut -d: -f1)
    if [ -z "$flush" ] || [ -z "$mark" ] || [ "$flush" -gt "$mark" ]; then
        echo "FAIL: ${f#$ROOT/}: luna_finish does not call 'lpart-phase finish' before the finished marker" >&2
        rc=1
    fi
done < <(grep -rlE '^ *(function )?luna_finish *\(\)' "$ROOT"/{redhat,opensuse,ubuntu}/*/src)
[ "$n" -gt 0 ] || { echo "FAIL: no luna_finish found under $ROOT" >&2; exit 1; }
[ $rc -eq 0 ] && echo "OK: all $n installer hand-overs flush before the finished marker"
exit $rc
