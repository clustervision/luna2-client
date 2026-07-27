#!/bin/bash
# drv-ctrl.sh <stage> -- P4 controller-adjacent tests on lpx-node001, after P2.
# Stages: s340 (parked-installer SSH incident loop) | s336 (retry config-lifetime)
#         s339 (kmsg flood) | s354 (8G streaming memboot)
#         s355 (mid-stream net cut) | s358 (download watchdog)
#         s360 (group->node cascade; CTRL-SERIAL) | s334 (efibootmgr-less clone; CTRL-SERIAL)
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
W=${LAB_LAYOUTS:-$HERE}
ALLD="vda vdb vdc"
NODE_IP=10.141.0.1   # node001 on lpx-prov (cluster net 10.141.0.0/16)

installer_ssh(){ # run a command inside the PARKED installer via the controller
    ctrl "ssh -o ConnectTimeout=6 -o StrictHostKeyChecking=no $NODE '$1'" 2>/dev/null
}

park_installer(){ # boot into a deterministic park (unsatisfiable layout)
    pushdl $W/os-discover-nvme.json
    setcfg "--install-mode full"
    console_save pre-park; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow park
    virsh start $DOM >/dev/null
    con_wait 'retry|attempt|park' 900 5
}

s340(){
    ev_init 3.40
    echo "== 3.40 SSH-into-parked-installer incident loop =="
    # fatal park: sanitize x persistent
    pushdl $W/dl-persist.json
    setcfg "--install-mode sanitize"
    console_save pre-340; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s340
    virsh start $DOM >/dev/null
    con_wait 'sanitize-persistent|safety_refusal|maintenance' 900 5 || { console_unfollow; result BLOCKED 3.40 "no fatal park observed"; return 1; }
    sleep 15
    local login prev dmesg
    login=$(installer_ssh 'echo SSH-OK')
    prev=$(installer_ssh 'ls /tmp/luna_install.log.prev 2>/dev/null | wc -l')
    dmesg=$(installer_ssh 'ls /tmp/luna_install.dmesg.last 2>/dev/null | wc -l')
    local refusal; refusal=$(installer_ssh 'grep -c "sanitize-persistent\|safety_refusal" /tmp/luna_install.log.prev 2>/dev/null')
    # exit must RE-OPEN the shell (no silent resume): probe by exiting and re-sshing
    installer_ssh 'exit' >/dev/null 2>&1
    sleep 10
    local again; again=$(installer_ssh 'echo STILL-PARKED')
    console_unfollow; console_save s340-done
    if [ "$login" = SSH-OK ] && [ "$prev" = 1 ] && [ "$again" = STILL-PARKED ]; then
        result PASS 3.40 "installer SSH up, incident files present (prev=$prev dmesg=$dmesg refusal=$refusal), park persists after exit"
    else
        result FINDING 3.40 "login=$login prev=$prev dmesg=$dmesg again=$again"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

s336(){
    ev_init 3.36
    echo "== 3.36 retry-loop config lifetime: storage.json byte-identical =="
    park_installer || { console_unfollow; result BLOCKED 3.36 "no park"; return 1; }
    console_unfollow
    sleep 20
    local h1 h2 h3
    h1=$(installer_ssh 'sha256sum /tmp/install_files/runtime/storage.json 2>/dev/null | cut -d" " -f1')
    sleep 75   # let one 30s-retry cycle (or two) pass
    h2=$(installer_ssh 'sha256sum /tmp/install_files/runtime/storage.json 2>/dev/null | cut -d" " -f1')
    sleep 75
    h3=$(installer_ssh 'sha256sum /tmp/install_files/runtime/storage.json 2>/dev/null | cut -d" " -f1')
    echo "hashes: $h1 / $h2 / $h3"
    if [ -n "$h1" ] && [ "$h1" = "$h2" ] && [ "$h2" = "$h3" ]; then
        # now push a good layout: the next attempt must pick up the NEW config
        pushdl $W/dl-single.json
        if wait_boot 1200; then
            result PASS 3.36 "storage.json immutable across retries ($h1); new config picked up -> install succeeded"
        else
            result FINDING 3.36 "hashes stable but new config did not lead to a boot"
        fi
    else
        result FINDING 3.36 "storage.json CHANGED across attempts: $h1 $h2 $h3 (BE-S2 copy-back)"
        pushdl $W/dl-single.json; wait_boot 1200 || true
    fi
}

s339(){
    ev_init 3.39
    echo "== 3.39 kmsg flood during install =="
    pushdl $W/dl-single.json
    setcfg "--install-mode full"
    console_save pre-339; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram; blank $ALLD
    console_follow s339
    virsh start $DOM >/dev/null
    # flood the kernel ring buffer as soon as the installer SSH is up
    local flooded=0 i
    for i in $(seq 1 60); do
        if installer_ssh 'echo up' | grep -q up; then
            installer_ssh 'for i in $(seq 1 20000); do echo "<4>lpartflood $i" > /dev/kmsg; done; echo FLOODED' | grep -q FLOODED && flooded=1
            break
        fi
        sleep 10
    done
    if wait_boot 1200; then
        console_unfollow
        [ "$flooded" = 1 ] && result PASS 3.39 "20k-line kmsg flood mid-install; install completed + booted" \
                           || result CHECK 3.39 "install booted but flood was never injected (SSH window missed)"
    else
        console_unfollow; console_save s339-fail
        result FINDING 3.39 "install did not complete under kmsg flood (BE-S1 class)"
        install_clean $W/dl-single.json auto $ALLD || true
    fi
}

s354(){
    ev_init 3.54
    echo "== 3.54 8G node memboots the big image via streaming =="
    # node RAM is already 8G (lab default) -- exactly the test condition
    pushdl $W/empty-layout.json
    setcfg "--install-mode memboot"
    console_save pre-354; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s354
    virsh start $DOM >/dev/null
    local streamed=0
    con_wait 'streaming osimage extract|streamed into /sysroot' 1200 5 && streamed=1
    local end=$((SECONDS+600)) up=0
    while [ $SECONDS -lt $end ]; do
        node 'findmnt -no FSTYPE /' 2>/dev/null | grep -qE 'tmpfs|rootfs' && { up=1; break; }
        sleep 20
    done
    console_unfollow; console_save s354-done
    local dfroot freemem
    dfroot=$(node 'df -h / | tail -1')
    freemem=$(node 'free -m | awk "/^Mem/{print \$7}"')
    if [ "$streamed" = 1 ] && [ "$up" = 1 ]; then
        result PASS 3.54 "8G node streamed the image to tmpfs root (df: $dfroot, avail mem ${freemem}M)"
    else
        result FINDING 3.54 "streamed=$streamed up=$up"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

s358(){
    ev_init 3.58
    echo "== 3.58 download watchdog: blocked 7051 aborts + retry recovers =="
    # scoped: only node001's IP is dropped, other nodes keep provisioning
    pushdl $W/empty-layout.json
    setcfg "--install-mode memboot"
    ctrl_mutate "iptables -I INPUT -s $NODE_IP -p tcp --dport 7051 -j DROP" \
                "iptables -D INPUT -s $NODE_IP -p tcp --dport 7051 -j DROP" 1200
    console_save pre-358; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s358
    virsh start $DOM >/dev/null
    local aborted=0
    con_wait 'timed out|timeout|curl.*(28|7)|failure|retry' 600 5 && aborted=1
    # unblock -> next attempt succeeds
    ctrl_revert_now
    local end=$((SECONDS+900)) up=0
    while [ $SECONDS -lt $end ]; do
        node 'findmnt -no FSTYPE /' 2>/dev/null | grep -qE 'tmpfs|rootfs' && { up=1; break; }
        sleep 20
    done
    console_unfollow; console_save s358-done
    if [ "$aborted" = 1 ] && [ "$up" = 1 ]; then
        result PASS 3.58 "download stall aborted within the watchdog window; retry after unblock reached tmpfs root"
    else
        result FINDING 3.58 "aborted=$aborted recovered=$up"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

s355(){
    ev_init 3.55
    echo "== 3.55 mid-stream network cut -> fallback recovers =="
    pushdl $W/empty-layout.json
    setcfg "--install-mode memboot"
    console_save pre-355; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s355
    virsh start $DOM >/dev/null
    con_wait 'streaming osimage extract' 1200 3 || { console_unfollow; result BLOCKED 3.55 "streaming never started"; return 1; }
    # cut the image port mid-stream; curl --speed-time will abort
    ctrl_mutate "iptables -I INPUT -s $NODE_IP -p tcp --dport 7051 -j DROP" \
                "iptables -D INPUT -s $NODE_IP -p tcp --dport 7051 -j DROP" 1200
    local failed=0 fellback=0
    con_wait 'extract.*fail|failure' 600 5 && failed=1
    ctrl_revert_now
    con_wait 'falling back|classic download|download.*extract' 600 5 && fellback=1
    local end=$((SECONDS+900)) up=0
    while [ $SECONDS -lt $end ]; do
        node 'findmnt -no FSTYPE /' 2>/dev/null | grep -qE 'tmpfs|rootfs' && { up=1; break; }
        sleep 20
    done
    console_unfollow; console_save s355-done
    if [ "$failed" = 1 ] && [ "$up" = 1 ]; then
        result PASS 3.55 "mid-stream cut aborted the pipe (fallback-log=$fellback); recovery reached tmpfs root"
    else
        result FINDING 3.55 "failed=$failed fallback=$fellback up=$up"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

s360(){ # CTRL-SERIAL: run with no other install in flight
    ev_init 3.60
    echo "== 3.60 group->node cascade for the v1.4 fields =="
    ctrl "luna group show compute-group -R 2>/dev/null || luna group show compute-group" > "$EV/group-before.txt"
    # (a) clear node-level layout; group-level (set by the installer) must serve
    setcfg "-qdisklayout ''" || setcfg "--disklayout ''" || true
    install_clean - full $ALLD || { result FINDING 3.60a "install with group-level layout failed"; return 1; }
    local r1; r1=$(node 'findmnt -no SOURCE /')
    # (b) node-level override wins
    pushdl $W/dl-352-multiset.json
    install_clean - full $ALLD || { result FINDING 3.60b "install with node override failed"; return 1; }
    local hasdata; hasdata=$(node 'findmnt -no SOURCE /data | wc -l')
    if [ "$r1" = /dev/mapper/os-root ] && [ "$hasdata" = 1 ]; then
        result PASS 3.60 "group layout served when node unset; node override (extra /data set) wins when set"
    else
        result FINDING 3.60 "group-install root=$r1 override-data=$hasdata"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

s334(){ # CTRL-SERIAL: osimage clone + pack are controller-global
    ev_init 3.34
    echo "== 3.34 efibootmgr-less image: multi-disk FAILS verify, single passes =="
    local CLONE=compute-noefibm
    if ! ctrl "luna osimage show $CLONE >/dev/null 2>&1"; then
        ctrl "luna osimage clone compute-image -nn $CLONE 2>&1 | tail -1" || { result BLOCKED 3.34 "osimage clone failed"; return 1; }
    fi
    ctrl_mutate "mv /trinity/images/$CLONE/usr/sbin/efibootmgr /trinity/images/$CLONE/usr/sbin/efibootmgr.gone 2>/dev/null; mv /trinity/images/$CLONE/usr/bin/efibootmgr /trinity/images/$CLONE/usr/bin/efibootmgr.gone 2>/dev/null; true" \
                "true" 3600
    ctrl "luna osimage pack $CLONE 2>&1 | tail -1"
    setcfg "--osimage $CLONE"
    # (a) RAID1 multi-disk -> verify must FAIL (park)
    pushdl $W/dl-raid1-vdbc.json
    setcfg "--install-mode full"
    console_save pre-334; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram; blank $ALLD
    console_follow s334a
    virsh start $DOM >/dev/null
    local failed=0
    con_wait 'per-leg NVRAM|requires.*efibootmgr|MISSING.*efibootmgr' 1200 5 && failed=1
    console_unfollow; console_save s334a-done
    # (b) single-disk -> tolerant skip, install passes
    pushdl $W/dl-single.json
    console_save pre-334b; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram; blank $ALLD
    console_follow s334b
    virsh start $DOM >/dev/null
    local single=0; wait_boot 1200 && single=1
    console_unfollow
    setcfg "--osimage compute-image"
    if [ "$failed" = 1 ] && [ "$single" = 1 ]; then
        result PASS 3.34 "efibootmgr-less: multi-disk verify FAILS loud, single-disk tolerant-skips + boots"
    else
        result FINDING 3.34 "multi-fail=$failed single-boot=$single"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

case "${1:?stage: s340 s336 s339 s354 s355 s358 s360 s334}" in
    s340) s340;; s336) s336;; s339) s339;; s354) s354;;
    s355) s355;; s358) s358;; s360) s360;; s334) s334;;
esac
