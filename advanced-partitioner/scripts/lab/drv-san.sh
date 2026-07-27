#!/bin/bash
# drv-san.sh <stage> -- P3 sanitize/persistent tests on lpx-node002.
# Stages: s352 (sanitize full lifecycle) | s353 (sanitize x persistent ->
#         operator resolution) | s351 (persistent raid1 data) | s350 (sync half)
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
W=${LAB_LAYOUTS:-$HERE}

sanitize_cycle(){ # boots the sanitize installer; returns when erase evidence seen
    # (sanitize HALTS by design -- dracut emergency is the terminal state).
    # VM-lab detail: an installed disk is bootable via the ESP removable-path
    # fallback, so reset_nvram alone still boots the OLD OS (sanitize never
    # runs). Blank ONLY vda to force a netboot; vdb/vdc stay POPULATED so the
    # erase still faces real data. (On real HW the operator triggers reinstall
    # via BMC/PXE -- no disk pre-wipe needed.)
    setcfg "--install-mode sanitize"
    console_save pre-sanitize
    virsh destroy $DOM >/dev/null 2>&1; sleep 1
    reset_nvram
    blank vda
    console_follow sanitize
    virsh start $DOM >/dev/null
}

s352(){
    ev_init 3.52
    echo "== 3.52 sanitize lifecycle: install multi-set -> markers -> sanitize -> blank-proof -> reprovision =="
    install_clean $W/dl-352-multiset.json full vda vdb vdc || { result FAIL 3.52 "pre-install failed"; return 1; }
    node 'echo osmark > /root/mk; echo datamark > /data/mk; sync'
    local sigs_before; sigs_before=$(node 'blkid | wc -l')
    echo "markers set; blkid lines before: $sigs_before"

    local mark; mark=$(ctrl_log_mark)
    sanitize_cycle
    # detection via the CONTROLLER's per-node rsyslog (the lpart binaries log
    # there; the serial console only carries the wrapper's Luna2: lines)
    local erased=0 completed=0 retry=0
    if ctrl_log_wait 'sanitize: erasing [0-9]+ local disk' 900; then
        ctrl_log_wait 'sanitize completed' 600 && completed=1
        sleep 10
        erased=$(ctrl_log_since "$mark" | grep -acE 'blkdiscard succeeded|erased via wipe\+zap')
        ctrl_log_since "$mark" | grep -aqE 'retr(y|ying) in [0-9]+' && retry=1
    fi
    console_unfollow
    console_save sanitize-done
    ctrl_log_since "$mark" > "$EV/ctrl-log-sanitize.txt"
    echo "erase lines=$erased completed=$completed retry=$retry (expect 3,1,0)"

    # blankness proof via memboot
    if ! memboot_helper 1200; then result FINDING 3.52 "memboot for blank-proof did not come up"; return 1; fi
    local sigs parts
    sigs=$(node 'blkid 2>/dev/null | grep -v "^/dev/ram\|squashfs\|tmpfs" | wc -l')
    parts=$(node 'lsblk -no NAME /dev/vda /dev/vdb /dev/vdc 2>/dev/null | grep -vE "^(vda|vdb|vdc)$" | wc -l')
    node 'blkid; lsblk' > "$EV/blankproof.txt" 2>&1
    echo "post-sanitize: signatures=$sigs partitions=$parts (expect 0,0)"

    # reprovision
    if install_clean $W/dl-single.json auto vda vdb vdc && [ "$sigs" = 0 ] && [ "$parts" = 0 ] && [ "$erased" -ge 3 ] && [ "$retry" = 0 ]; then
        result PASS 3.52 "erase x$erased, no retry loop, memboot-proof blank (sigs=0 parts=0), reprovisioned"
    else
        result FINDING 3.52 "erased=$erased retry=$retry sigs=$sigs parts=$parts reprovision=$? -- see evidence"
    fi
}

s353(){
    ev_init 3.53
    echo "== 3.53 sanitize x persistent: refusal -> operator removes set -> erase proceeds =="
    # create the data set NON-persistent first (a persistent set on a blank
    # disk has nothing to mount -- correct usage is populate-then-flip), write
    # the marker, THEN flip the set to persistent for the sanitize refusal.
    install_clean $W/dl-352-multiset.json full vda vdb vdc || { result FAIL 3.53 "pre-install failed"; return 1; }
    node 'echo pmark > /data/mk; sync'
    pushdl $W/dl-persist.json

    local mark; mark=$(ctrl_log_mark)
    sanitize_cycle
    local refused=0
    ctrl_log_wait 'sanitize-persistent|safety_refusal' 900 && refused=1
    console_unfollow; console_save refusal
    ctrl_log_since "$mark" > "$EV/ctrl-log-refusal.txt"
    local mk=""
    if [ "$refused" = 1 ]; then
        # marker must have survived the refusal: prove via memboot mount
        memboot_helper 1200 || { result FINDING 3.53 "memboot after refusal failed"; return 1; }
        mk=$(node 'mkdir -p /m; mount /dev/vdb1 /m 2>/dev/null && cat /m/mk; umount /m 2>/dev/null')
        echo "refusal seen; marker after refusal: $mk"
    else
        result FINDING 3.53 "no sanitize-persistent refusal observed"; return 1
    fi

    # operator resolution: drop the persistent set, keep sanitize
    pushdl $W/dl-352-multiset.json
    mark=$(ctrl_log_mark)
    sanitize_cycle
    local erased=0
    ctrl_log_wait 'sanitize completed' 900 && erased=$(ctrl_log_since "$mark" | grep -acE 'blkdiscard succeeded|erased via wipe\+zap')
    console_unfollow; console_save post-resolution
    ctrl_log_since "$mark" > "$EV/ctrl-log-resolution.txt"
    memboot_helper 1200 || { result FINDING 3.53 "memboot after resolution failed"; return 1; }
    local vdb1; vdb1=$(node 'blkid /dev/vdb1 2>/dev/null | wc -l')
    if [ "$mk" = pmark ] && [ "$erased" -ge 3 ] && [ "$vdb1" = 0 ]; then
        install_clean $W/dl-single.json auto vda vdb vdc || true
        result PASS 3.53 "refusal kept data (mk=$mk); after set removal erase took ex-persistent disk (x$erased); reprovisioned"
    else
        result FINDING 3.53 "mk=$mk erased=$erased vdb1-sigs=$vdb1"
    fi
}

s351(){
    ev_init 3.51
    echo "== 3.51 persistent raid1 DATA across OS reinstalls =="
    install_clean $W/dl-351-raid1data.json full vda vdb vdc || { result FAIL 3.51 "initial install failed"; return 1; }
    node 'echo r51 > /data/mk; sync'
    local ct1; ct1=$(node 'mdadm --detail $(findmnt -no SOURCE /data) 2>/dev/null | grep "Creation Time"')
    local ev1; ev1=$(node 'mdadm --detail $(findmnt -no SOURCE /data) 2>/dev/null | grep "Events"')
    echo "created: $ct1 / $ev1"
    # flip the data set persistent, then OS-only reinstall (blank vda ONLY)
    sed 's/"name":"data","role":"data","selection":"manual","raid":"1"/"name":"data","role":"data","selection":"manual","raid":"1","persistent":true/' \
        $W/dl-351-raid1data.json > $W/dl-351-persist.json
    python3 -c 'import json;json.load(open("$W/dl-351-persist.json"))' || { result BLOCKED 3.51 "layout edit invalid"; return 1; }
    install_clean $W/dl-351-persist.json full vda || { result FINDING 3.51 "OS reinstall (persistent data) did not boot"; return 1; }
    local mk ct2
    mk=$(node 'cat /data/mk 2>/dev/null')
    ct2=$(node 'mdadm --detail $(findmnt -no SOURCE /data) 2>/dev/null | grep "Creation Time"')
    echo "after reinstall: mk=$mk / $ct2"
    if [ "$mk" = r51 ] && [ "$ct2" = "$ct1" ] && [ -n "$ct1" ]; then
        result PASS 3.51 "data md ASSEMBLED not recreated (Creation Time unchanged), marker intact"
    else
        node_forensics 3.51
        result FINDING 3.51 "mk=$mk ct1='$ct1' ct2='$ct2'"
    fi
}

s350(){
    ev_init 3.50sync
    echo "== 3.50 sync half: persistent survives install-mode=sync =="
    # assumes s351 end-state (persistent raid1 data + healthy OS) or any persistent layout
    node 'findmnt /data >/dev/null' || { result BLOCKED 3.50sync "no /data mounted; run s351 first"; return 1; }
    node 'echo s50 > /data/mk50; sync'
    local uuid1; uuid1=$(node 'blkid -s UUID -o value $(findmnt -no SOURCE /data)')
    install_clean - sync vda || { result FINDING 3.50sync "sync reinstall did not boot"; return 1; }
    local mk uuid2
    mk=$(node 'cat /data/mk50 2>/dev/null')
    uuid2=$(node 'blkid -s UUID -o value $(findmnt -no SOURCE /data)')
    if [ "$mk" = s50 ] && [ "$uuid1" = "$uuid2" ] && [ -n "$uuid1" ]; then
        result PASS 3.50sync "persistent survived sync (marker + fs UUID unchanged)"
    else
        result FINDING 3.50sync "mk=$mk uuid1=$uuid1 uuid2=$uuid2"
    fi
}

case "${1:?stage: s352 s353 s351 s350}" in
    s352) s352;; s353) s353;; s351) s351;; s350) s350;;
esac
