#!/usr/bin/env bash
# Pinning test for the daemon-template dual pre/part/post-usage guard
# (FTA v2 MCS-13 / BE-S5, barrier NB-2) in templ_install_lpart.cfg.
#
# The guard's contract:
#   - lpart shim content            -> silent OK (rc 0)
#   - custom NON-disk content       -> WARN only (rc 0)
#   - DISK-MUTATING content         -> HARD BLOCK (exit 2 -> wrapper parks)
#   - 'lpart-allow-custom' marker   -> downgrades the block to the warning
#
# Usage: dual-usage-guard-test.sh [path-to-templ_install_lpart.cfg]
# Default template: the staged daemon-MR bundle. Skips (rc 0, SKIP notice)
# when the template is absent (e.g. a checkout without the daemon bundle).
set -u

TEMPLATE="${1:-/root/lpart-review-out/v14-daemon-reconciled/legacy/templ_install_lpart.cfg}"
if [ ! -r "$TEMPLATE" ]; then
    echo "SKIP: template not found: $TEMPLATE"
    exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Extract the guard function, neutralise Jinja, stub update_status.
{
    echo 'function update_status { echo "STATUS:$1"; }'
    sed -n '/^function lpart_dual_usage_guard {/,/^}/p' "$TEMPLATE" \
        | sed -e 's/{{[^}]*}}/lpart/g'
} > "$work/guard.sh"
grep -q 'lpart_dual_usage_guard' "$work/guard.sh" || {
    echo "FAIL: could not extract lpart_dual_usage_guard from $TEMPLATE"
    exit 1
}

fails=0
# run_case <name> <expected-rc> <expect-substr|-> <script-content...>
run_case() {
    local name="$1" want_rc="$2" want_sub="$3"; shift 3
    printf '%s\n' "$@" > "$work/field.sh"
    out="$(bash -c ". '$work/guard.sh'; lpart_dual_usage_guard part '$work/field.sh'" 2>&1)"
    rc=$?
    if [ "$rc" != "$want_rc" ]; then
        echo "FAIL: $name: rc=$rc want=$want_rc"; echo "$out" | sed 's/^/    /'; fails=$((fails+1)); return
    fi
    if [ "$want_sub" != "-" ] && ! grep -q "$want_sub" <<<"$out"; then
        echo "FAIL: $name: missing '$want_sub' in output"; echo "$out" | sed 's/^/    /'; fails=$((fails+1)); return
    fi
    echo "PASS: $name"
}

# 1. The lpart shim is silent-OK.
run_case "shim-ok" 0 "-" 'exec /usr/bin/lpart-phase part'
# 2. Custom non-disk content warns but proceeds.
run_case "custom-warns" 0 "WARNING" 'echo hello from a custom hook'
# 3. Disk-mutating legacy content is BLOCKED with exit 2 (safety refusal).
run_case "mke2fs-blocked" 2 "BLOCKED" 'parted /dev/sda mklabel gpt' 'mke2fs -t ext4 /dev/sda1'
run_case "dd-blocked" 2 "install.blocked.dual_part" 'dd if=/dev/zero of=/dev/sda bs=1M count=10'
run_case "mdadm-blocked" 2 "BLOCKED" 'mdadm --create /dev/md0 --level=1 /dev/sda /dev/sdb'
# 4. The escape marker downgrades the block to the warning.
run_case "escape-marker" 0 "WARNING" '# lpart-allow-custom: operator accepts the risk' 'mke2fs -t ext4 /dev/sda1'
# 5. Word-boundary sanity: names merely CONTAINING a command do not block.
run_case "no-false-positive" 0 "WARNING" 'echo running myparted-report.sh (no disk mutation)'
# 6. Empty field is silent-OK.
: > "$work/field.sh"
out="$(bash -c ". '$work/guard.sh'; lpart_dual_usage_guard part '$work/field.sh'" 2>&1)"; rc=$?
if [ "$rc" = "0" ] && [ -z "$out" ]; then echo "PASS: empty-silent"; else echo "FAIL: empty-silent rc=$rc out=$out"; fails=$((fails+1)); fi

if [ "$fails" -gt 0 ]; then echo "== $fails FAILURE(S) =="; exit 1; fi
echo "== ALL PASS =="
