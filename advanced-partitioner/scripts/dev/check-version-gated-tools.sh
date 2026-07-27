#!/bin/bash
# check-version-gated-tools.sh -- static gate: no version-gated external-tool
# flags in production Go code.
#
# WHY (FTA-v2 BE-P1, CCF-9): the install runtime spans util-linux 2.32 (el8)
# through 2.39+ (el10/ubuntu26). `lsblk -o PATH` (column added in util-linux
# 2.33) was used in TWO independent sites; on el8 the call fails, the caller
# reads "nothing found", and the dependent barrier is SILENTLY off (the
# dormant protected-disk barrier + persistent-md assemble were both dead on
# rocky8, 2026-07-18). The lesson had been written in discover.go since v1
# and was reintroduced twice anyway -- so it is now a machine rule, per the
# engineering standard ("seen twice -> rule with an enforcement check").
#
# Contract: production code derives device paths from NAME/KNAME ("/dev/"+x),
# never the PATH column. Add new forbidden tokens here the day a new
# version-gated flag bites; each entry names its minimum version so the ban
# can be lifted when the fleet floor rises.
set -u
cd "$(dirname "$0")/../.."

fail=0
# lsblk PATH column: util-linux >= 2.33; fleet floor is el8's 2.32.
hits=$(grep -rnE '"PATH,|,PATH"|"PATH"' --include='*.go' cmd/ internal/ 2>/dev/null \
       | grep -v '_test.go' \
       | grep -viE 'os\.Getenv|Setenv|env|\$PATH' || true)
if [ -n "$hits" ]; then
    echo "FAIL: lsblk PATH column in production code (util-linux >= 2.33; el8 has 2.32):"
    echo "$hits" | sed 's/^/  /'
    fail=1
fi

[ "$fail" = 0 ] && echo "OK: no version-gated tool columns in production code"
exit $fail
