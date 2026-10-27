#!/bin/sh
# initramfs-tools builds its boot-script ORDER files with `sh -n` (dash on
# Debian/Ubuntu) and silently drops every script that fails the check: a
# bashism in scripts/local-premount/luna-start leaves the installer out of the
# initramfs and the node boots to "No init found". Run this under dash.
# Usage: check-initramfs-tools-syntax.sh [repo-root]
set -u
root=${1:-$(cd "$(dirname "$0")/../../.." && pwd)}
sh_bin=$(command -v dash || true)
if [ -z "$sh_bin" ]; then
    echo "check-initramfs-tools-syntax: dash not found; run inside a Debian/Ubuntu container" >&2
    exit 2
fi
rc=0
for f in $(find "$root/ubuntu" -path '*/initramfs-tools/*' -type f \( -path '*/scripts/*' -o -path '*/hooks/*' \) ! -path '*/luna_files/*'); do
    if ! "$sh_bin" -n "$f"; then
        echo "FAIL: $f does not parse with dash -n (initramfs-tools would leave it out)" >&2
        rc=1
    fi
done
[ "$rc" = 0 ] && echo "OK: every ubuntu initramfs-tools script parses with dash -n"
exit $rc
