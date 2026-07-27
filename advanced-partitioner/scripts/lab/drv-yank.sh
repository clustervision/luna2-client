#!/bin/bash
# drv-yank.sh <stage> -- P2 power-yank matrix on lpx-node001 (3.59 + 3.32).
# Stages: y-partition | y-extract | y-bootloader  (single yank + recovery each)
#         y-triple    (the original 3-back-to-back matrix; run only if singles pass)
#         y-resync    (3.32: yank during raid1 resync)
# Layout: raid1 OS on vdb+vdc (dl-raid1-vdbc.json) -- vda stays out of the set.
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
W=${LAB_LAYOUTS:-$HERE}

start_install(){ # fresh full-install boot WITHOUT waiting for completion
    pushdl $W/dl-raid1-vdbc.json
    setcfg "--install-mode full"
    console_save pre-install
    virsh destroy $DOM >/dev/null 2>&1; sleep 1
    reset_nvram
    blank vda vdb vdc
    console_follow yank-install
    virsh start $DOM >/dev/null
}

verify_recovered(){ # after unattended recovery boot: full health check
    local tag=$1
    if ! wait_boot 1200; then
        shot "$EV/$tag-recovfail"; console_save "$tag-recovfail"
        node_forensics "$tag-failed"
        result FINDING "3.59-$tag" "recovery did NOT reach booted OS -- forensics captured"
        return 1
    fi
    local md nv root
    md=$(node 'grep -o "\[2/2\]" /proc/mdstat | wc -l')
    nv=$(node 'efibootmgr | grep -c TrinityX')
    root=$(node 'findmnt -no SOURCE /')
    if [ "$md" -ge 3 ] && [ "$nv" -ge 1 ]; then
        result PASS "3.59-$tag" "unattended recovery: $root, md [2/2]x$md, nvram=$nv"
    else
        node_forensics "$tag-degraded"
        result FINDING "3.59-$tag" "recovered but md=[2/2]x$md nvram=$nv (expect x3, >=1)"
    fi
}

single_yank(){ # single_yank <phase>
    local phase=$1
    ev_init "yank-$phase"
    echo "== single yank during $phase =="
    start_install
    if ! phase_wait "$phase" 900; then
        console_unfollow
        result BLOCKED "3.59-$phase" "phase marker never seen (regex ${phase}) -- calibrate PHASE_RE"
        return 1
    fi
    case "$phase" in partition) sleep 10;; *) sleep 2;; esac   # land mid-phase
    yank
    console_unfollow
    echo "yanked during $phase; unattended re-PXE (same config, no cleanup)"
    virsh start $DOM >/dev/null
    verify_recovered "$phase"
}

y_triple(){
    ev_init yank-triple
    echo "== 3 back-to-back yanks (original 3.59 matrix) =="
    start_install
    phase_wait partition 900 && { sleep 2; yank; }
    console_unfollow
    for phase in extract bootloader; do
        console_follow "triple-$phase"
        virsh start $DOM >/dev/null
        phase_wait "$phase" 900 || true
        sleep 2; yank
        console_unfollow
    done
    virsh start $DOM >/dev/null
    verify_recovered triple
}

y_resync(){ # 3.32: power loss during ACTIVE raid1 resync
    ev_init yank-resync
    echo "== 3.32: yank during raid1 resync =="
    install_clean $W/dl-raid1-vdbc.json full vda vdb vdc || { result FAIL 3.32 "raid1 install failed"; return 1; }
    node 'echo r32marker > /root/mk; sync'
    # force a resync: fail+remove+re-add one leg of the root array
    cat > /tmp/resync.sh <<'EOS'
set -e
md=$(awk '/^md/{print $1; exit}' /proc/mdstat)
leg=$(mdadm --detail /dev/$md | awk '/\/dev\/vd/{print $NF}' | tail -1)
mdadm /dev/$md --fail $leg --remove $leg
mdadm --zero-superblock $leg 2>/dev/null || true
mdadm /dev/$md --add $leg
sleep 3
grep -A2 "^$md" /proc/mdstat
EOS
    ctrl "cat > /tmp/ns-node001.sh" < /tmp/resync.sh
    ctrl "scp -o StrictHostKeyChecking=no /tmp/ns-node001.sh node001:/tmp/ns.sh >/dev/null 2>&1 && ssh node001 'bash /tmp/ns.sh'" | tee "$EV/resync-trigger.txt"
    if ! node 'grep -q recovery /proc/mdstat && echo RESYNCING'; then
        result BLOCKED 3.32 "could not observe active resync"
        return 1
    fi
    yank
    virsh start $DOM >/dev/null
    if ! wait_boot 900; then
        shot "$EV/resync-bootfail"; console_save resync-bootfail
        result FINDING 3.32 "no boot after yank mid-resync"
        return 1
    fi
    sleep 60   # let resync resume/continue
    local st mk
    st=$(node 'grep -E "md|recovery|\[2/2\]|\[U|_" /proc/mdstat | tr "\n" " "')
    mk=$(node 'cat /root/mk')
    node 'for i in $(seq 1 30); do grep -q "\[2/2\]" /proc/mdstat && break; sleep 20; done; grep -c "\[2/2\]" /proc/mdstat' > /tmp/md232 || true
    local final; final=$(cat /tmp/md232 2>/dev/null)
    if [ "$mk" = r32marker ] && [ "${final:-0}" -ge 3 ]; then
        result PASS 3.32 "resync survived power loss: resumed to [2/2]x$final, marker intact"
    else
        node_forensics resync
        result FINDING 3.32 "post-yank state: marker=$mk mdfinal=$final ($st)"
    fi
}

case "${1:?stage: y-partition y-extract y-bootloader y-triple y-resync}" in
    y-partition) single_yank partition;;
    y-extract)   single_yank extract;;
    y-bootloader) single_yank bootloader;;
    y-triple)    y_triple;;
    y-resync)    y_resync;;
esac
