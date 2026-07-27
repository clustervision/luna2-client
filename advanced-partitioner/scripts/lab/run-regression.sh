#!/bin/bash
# run-regression.sh -- packaged lpart destructive/safe regression runner.
#
#   run-regression.sh <cluster> [--suite safe|destructive|all] [--tests 3.52,3.59]
#                     [--distro redhat|ubuntu|opensuse] [--nodes node001,node003]
#                     [--osimage <name>] [--dry-run]
#
# Consumes manifest.tsv (this dir). Schedules each selected test onto a node
# whose disk-shape satisfies it (node001=3,node002=3,node003=4,node004=5),
# runs nodes in PARALLEL and tests SERIALLY per node (a node's disk state is a
# shared resource), warms each node with a memboot first, invokes the mapped
# driver:stage (CLUSTER/N in env), captures the reg-orch result() verdict line,
# and rolls up run-report.{md,json,xml}. Exit 0 iff no FAIL (BLOCKED/FINDING/
# SKIP are surfaced but non-fatal); the §4a seam gate FAIL is fatal.
#
# --distro/--osimage re-points every target node's osimage first (guarded,
# reverted at the end) so the SAME tests run against ubuntu24/opensuse images.
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
MANIFEST="$HERE/manifest.tsv"

CLUSTER=${1:?usage: run-regression.sh <cluster> [opts]}; shift || true
SUITE=all; ONLY_TESTS=; DISTRO=; ONLY_NODES=; OSIMAGE=; DRYRUN=0
while [ $# -gt 0 ]; do case "$1" in
    --suite) SUITE=$2; shift 2;;
    --tests) ONLY_TESTS=$2; shift 2;;
    --distro) DISTRO=$2; shift 2;;
    --osimage) OSIMAGE=$2; shift 2;;
    --nodes) ONLY_NODES=$2; shift 2;;
    --dry-run) DRYRUN=1; shift;;
    *) echo "unknown flag: $1" >&2; exit 2;;
esac; done

export CLUSTER LAB_WORK=${LAB_WORK:-$HOME/lpart-uefi-work} LAB_LAYOUTS="$HERE"
CTRL=${CTRL:-$(awk -v h="${CLUSTER}.lab" '$2==h{print $1}' /etc/hosts | tail -1)}
: "${CTRL:?no CTRL and no ${CLUSTER}.lab in /etc/hosts}"
ctrl(){ timeout 60 ssh -o StrictHostKeyChecking=no root@$CTRL "$@"; }

RUNDIR="$LAB_WORK/regression-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$RUNDIR"
REPORT_MD="$RUNDIR/run-report.md"; REPORT_JSON="$RUNDIR/run-report.json"; REPORT_XML="$RUNDIR/run-report.xml"
: > "$RUNDIR/verdicts.tsv"

# node -> disk shape (matches kvm-lab/lab-up.sh default shapes 3,3,4,5)
declare -A NODE_SHAPE=( [1]=3 [2]=3 [3]=4 [4]=5 )
NODES_ALL="1 2 3 4"
[ -n "$ONLY_NODES" ] && NODES_ALL=$(echo "$ONLY_NODES" | tr ',' '\n' | sed 's/node00//' | tr '\n' ' ')

want_test(){ # want_test <safety> <id> <distros>
    local safety=$1 id=$2 distros=$3
    case "$SUITE" in safe) [ "$safety" = safe ] || return 1;; destructive) [ "$safety" = destructive ] || return 1;; all) ;; esac
    [ -z "$ONLY_TESTS" ] || echo ",$ONLY_TESTS," | grep -q ",$id," || return 1
    if [ -n "$DISTRO" ]; then
        case "$distros" in
            all) ;;
            !*) [ "$distros" = "!$DISTRO" ] && return 1;;
            *) echo ",$distros," | grep -q ",$DISTRO," || return 1;;
        esac
    fi
    return 0
}

# --- optional: re-point node osimages to a target image/distro (guarded) ---
declare -A ORIG_OSIMAGE=()
repoint_nodes(){
    [ -n "$OSIMAGE" ] || return 0
    local n node
    for n in $NODES_ALL; do node=node00$n
        ORIG_OSIMAGE[$n]=$(ctrl "luna node show $node 2>/dev/null | awk -F'|' '/ osimage /{gsub(/ /,\"\",\$3);print \$3}'")
        ctrl "luna node change $node --osimage $OSIMAGE" >/dev/null 2>&1 \
            && echo "  node$node -> osimage $OSIMAGE (was ${ORIG_OSIMAGE[$n]})"
    done
}
restore_osimage(){
    local n node; for n in "${!ORIG_OSIMAGE[@]}"; do node=node00$n
        [ -n "${ORIG_OSIMAGE[$n]}" ] && ctrl "luna node change $node --osimage ${ORIG_OSIMAGE[$n]}" >/dev/null 2>&1
    done
}
trap restore_osimage EXIT

# --- build per-node queues from the manifest ---
declare -A QUEUE=()
while IFS=$'\t' read -r id section safety shape layout mode stage distros timeout_s; do
    [ -z "${id:-}" ] && continue; case "$id" in \#*) continue;; esac
    want_test "$safety" "$id" "$distros" || continue
    # pick the least-loaded eligible node whose shape >= required
    local_best=; local_min=999
    for n in $NODES_ALL; do
        [ "${NODE_SHAPE[$n]}" -ge "$shape" ] || continue
        cnt=$(echo "${QUEUE[$n]:-}" | wc -w)
        if [ "$cnt" -lt "$local_min" ]; then local_min=$cnt; local_best=$n; fi
    done
    [ -n "$local_best" ] || { echo "SKIP $id: no node with shape>=$shape in [$NODES_ALL]"; continue; }
    QUEUE[$local_best]="${QUEUE[$local_best]:-} $id|$stage|$timeout_s"
done < "$MANIFEST"

# --- run one node's queue (serial), warm with memboot first ---
run_node_queue(){
    local n=$1 items=$2
    export N=$n
    source "$HERE/nreg.sh" 2>/dev/null
    source "$HERE/reg-orch.sh" 2>/dev/null
    local first=1 item id stage tmo
    for item in $items; do
        IFS='|' read -r id stage tmo <<<"$item"
        if [ "$first" = 1 ]; then
            ev_init "warmup"; memboot_helper 1200 >/dev/null 2>&1 && echo "[n$n] memboot warm-up ok" || echo "[n$n] memboot warm-up FAILED"; first=0
        fi
        if [ "$stage" = TODO ]; then
            echo "$id	n$n	SKIP	unautomated	catalogued, no driver stage yet" >> "$RUNDIR/verdicts.tsv"; continue
        fi
        local out; out=$(run_one "$id" "$stage" "$tmo" 2>&1)
        echo "$out" | tail -3
        # capture the reg-orch verdict line: [<id> n<N>] VERDICT (fta): msg
        local vl; vl=$(echo "$out" | grep -oE '\[[^]]*\] (PASS|FAIL|FINDING|BLOCKED|CHECK) \(' | tail -1)
        local verdict; verdict=$(echo "$vl" | grep -oE 'PASS|FAIL|FINDING|BLOCKED|CHECK' | tail -1)
        [ -n "$verdict" ] || verdict=NORESULT
        local msg; msg=$(echo "$out" | grep -oE '\] (PASS|FAIL|FINDING|BLOCKED|CHECK) \(.*' | tail -1 | sed 's/^] //')
        echo "$id	n$n	$verdict	-	${msg:-no result line captured}" >> "$RUNDIR/verdicts.tsv"
    done
}

run_one(){ # run_one <id> <driver:stage> <timeout>
    local id=$1 stage=$2 tmo=$3
    case "$stage" in
        orch:memboot)   ev_init "$id"; memboot_helper "$tmo" && result PASS 3.26 "memboot reached tmpfs root" || result FINDING 3.26 "memboot did not reach tmpfs";;
        orch:seam_gate) if bash "${SEAM_GATE:-$HERE/../dev/seam-contract-gate.sh}" "$CTRL" node001 >/dev/null 2>&1; then echo "[4a.1 n$N] PASS (4a): seam gate green"; else echo "[4a.1 n$N] FAIL (4a): seam gate failed"; fi;;
        orch:initramfs_gate)
            # PRE-BOOT: prove the packed initramfs the node will fetch carries
            # the lpart toolset (catches ubuntu-mkinitramfs / opensuse-missing-jq
            # before any install). Runs the vendored gate ON the controller.
            if scp -q -o StrictHostKeyChecking=no "$HERE/gate-initramfs-toolset.sh" root@"$CTRL":/tmp/gate-ird.sh 2>/dev/null \
               && ssh -o BatchMode=yes -o StrictHostKeyChecking=no root@"$CTRL" \
                     "bash /tmp/gate-ird.sh ${OSIMAGE:+--images '$OSIMAGE'} --quiet" >/dev/null 2>&1; then
                echo "[4a.2 n$N] PASS (4a): packed-initramfs toolset gate green"
            else
                local grc=$?
                if [ "$grc" = 2 ]; then echo "[4a.2 n$N] BLOCKED (4a): controller could not read initramfs (decompressor missing)"
                else echo "[4a.2 n$N] FAIL (4a): packed-initramfs missing lpart toolset (nodes will not install)"; fi
            fi;;
        drv-*:*)
            local drv=${stage%%:*} sub=${stage#*:}
            timeout "$tmo" env CLUSTER="$CLUSTER" N="$N" LAB_WORK="$LAB_WORK" LAB_LAYOUTS="$HERE" bash "$HERE/$drv.sh" "$sub";;
        *) echo "[$id n$N] BLOCKED (runner): unknown stage '$stage'";;
    esac
}

echo "== run-regression: cluster=$CLUSTER suite=$SUITE distro=${DISTRO:-current} osimage=${OSIMAGE:-current} =="
for n in $NODES_ALL; do echo "  node00$n queue: ${QUEUE[$n]:-(empty)}"; done
[ "$DRYRUN" = 1 ] && { echo "(dry-run: not executing)"; exit 0; }

repoint_nodes

# launch node queues in parallel
pids=()
for n in $NODES_ALL; do
    [ -n "${QUEUE[$n]:-}" ] || continue
    ( run_node_queue "$n" "${QUEUE[$n]}" ) &
    pids+=($!)
done
for p in "${pids[@]}"; do wait "$p"; done

# --- roll-up ---
total=$(wc -l < "$RUNDIR/verdicts.tsv")
fails=$(awk -F'\t' '$3=="FAIL"{c++}END{print c+0}' "$RUNDIR/verdicts.tsv")
{
  echo "# lpart regression run -- $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "cluster=$CLUSTER suite=$SUITE distro=${DISTRO:-current} osimage=${OSIMAGE:-current}"; echo
  echo "| id | node | verdict | detail |"; echo "|---|---|---|---|"
  awk -F'\t' '{printf "| %s | %s | %s | %s |\n",$1,$2,$3,$5}' "$RUNDIR/verdicts.tsv" | sort
  echo; echo "**totals**: $total tests; FAIL=$fails; $(awk -F'\t' '{c[$3]++}END{for(k in c)printf "%s=%d ",k,c[k]}' "$RUNDIR/verdicts.tsv")"
} > "$REPORT_MD"
# JUnit
{
  echo "<?xml version=\"1.0\"?>"
  echo "<testsuite name=\"lpart-regression\" tests=\"$total\" failures=\"$fails\">"
  awk -F'\t' '{
    printf "  <testcase classname=\"%s\" name=\"%s\">", $2, $1
    if($3=="FAIL") printf "<failure message=\"%s\"/>", $5
    else if($3!="PASS") printf "<skipped message=\"%s: %s\"/>", $3, $5
    print "</testcase>"
  }' "$RUNDIR/verdicts.tsv"
  echo "</testsuite>"
} > "$REPORT_XML"
# JSON
awk -F'\t' 'BEGIN{print "["}{printf "%s{\"id\":\"%s\",\"node\":\"%s\",\"verdict\":\"%s\",\"detail\":\"%s\"}", (NR>1?",":""),$1,$2,$3,$5}END{print "]"}' "$RUNDIR/verdicts.tsv" > "$REPORT_JSON"

echo "== report: $REPORT_MD  ($total tests, FAIL=$fails) =="
cat "$REPORT_MD" | tail -n +1 | sed -n '/totals/p'
[ "$fails" = 0 ] || exit 1
exit 0
