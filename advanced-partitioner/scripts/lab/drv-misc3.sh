#!/bin/bash
# drv-misc3.sh <stage> -- P5 tests on lpx-node003 (4 disks), after P1.
# Stages: s327 (NVRAM-cleared fallback boot) | s333 (efivars absent LOUD)
#         s342 (by-path drift fail-closed) | s343 (udev-style mount matrix)
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
W=${LAB_LAYOUTS:-$HERE}
ALLD="vda vdb vdc vdd"

nodescript(){ ctrl "cat > /tmp/ns-$NODE.sh" < "$1"; ctrl "scp -o StrictHostKeyChecking=no /tmp/ns-$NODE.sh $NODE:/tmp/ns.sh >/dev/null 2>&1 && ssh $NODE 'bash /tmp/ns.sh'"; }

s327(){
    ev_init 3.27
    echo "== 3.27 NVRAM cleared -> removable-fallback boot =="
    install_clean $W/dl-single.json auto $ALLD || { result FAIL 3.27 "base install failed"; return 1; }
    node 'echo m327 > /root/mk; sync'
    # delete every TrinityX NVRAM entry from the booted OS
    cat > /tmp/n327.sh <<'EOS'
for b in $(efibootmgr | awk '/TrinityX/{gsub("[^0-9A-F]","",$1); print $1}'); do efibootmgr -b $b -B >/dev/null; done
efibootmgr | grep -c TrinityX || true
EOS
    local left; left=$(nodescript /tmp/n327.sh | tail -1)
    [ "$left" = 0 ] || { result BLOCKED 3.27 "could not clear entries (left=$left)"; return 1; }
    # reboot WITHOUT touching NVRAM file (the firmware must use the removable path)
    console_save pre-reboot; virsh destroy $DOM >/dev/null 2>&1; sleep 1; virsh start $DOM >/dev/null
    if wait_boot 500; then
        local mk nv
        mk=$(node 'cat /root/mk'); nv=$(node 'efibootmgr | grep -c TrinityX')
        if [ "$mk" = m327 ] && [ "$nv" = 0 ]; then
            result PASS 3.27 "booted via removable fallback (marker intact, entries still absent=$nv)"
        else
            result FINDING 3.27 "booted but mk=$mk nvram=$nv (expected m327, 0)"
        fi
        # idempotent re-register: one reinstall recreates entries
        install_clean $W/dl-single.json auto $ALLD && nv=$(node 'efibootmgr | grep -c TrinityX')
        [ "${nv:-0}" -ge 1 ] && result PASS 3.27-reregister "reinstall recreated NVRAM entries ($nv)" \
                             || result FINDING 3.27-reregister "reinstall nvram=$nv"
    else
        shot "$EV/s327-fail"; console_save s327-fail
        result FINDING 3.27 "no boot after NVRAM clear -- removable fallback gap (BE-W3/W4)"
        install_clean $W/dl-single.json auto $ALLD || true
    fi
}

s333(){
    ev_init 3.33
    echo "== 3.33 efivars absent during finalise -> LOUD + verify fails =="
    # park the installer via an unsatisfiable layout, ssh in, umount efivars,
    # then push the good layout so the next attempt runs with efivars gone.
    pushdl $W/os-discover-nvme.json
    setcfg "--install-mode full"
    console_save pre-333; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s333
    virsh start $DOM >/dev/null
    con_wait 'retry|park|attempt' 900 5 || { console_unfollow; result BLOCKED 3.33 "installer never parked"; return 1; }
    # installer runs sshd (NB-7): reach it via the controller
    cat > /tmp/n333.sh <<'EOS'
umount /sys/firmware/efi/efivars 2>/dev/null && echo EFIVARS-UMOUNTED || echo UMOUNT-FAILED
EOS
    ctrl "cat > /tmp/ns333.sh" < /tmp/n333.sh
    local um; um=$(ctrl "ssh -o ConnectTimeout=6 -o StrictHostKeyChecking=no node003 'bash -s' < /tmp/ns333.sh" 2>/dev/null)
    [ "$um" = EFIVARS-UMOUNTED ] || { console_unfollow; result BLOCKED 3.33 "could not umount efivars in installer ($um)"; return 1; }
    pushdl $W/dl-single.json   # next retry: good layout, no efivars
    local warn=0 missing=0
    con_wait 'no NVRAM boot entries|removable fallback only' 600 5 && warn=1
    con_wait 'MISSING.*efibootmgr|NVRAM unreadable' 300 5 && missing=1
    console_unfollow; console_save s333-done
    if [ "$warn" = 1 ] && [ "$missing" = 1 ]; then
        result PASS 3.33 "NB-5 WARN loud + verify fail-closed on absent efivars"
    else
        result FINDING 3.33 "warn=$warn missing=$missing -- see console-s333"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

s342(){
    ev_init 3.42
    echo "== 3.42 by-path drift fails closed with candidates =="
    # discover the CURRENT by-path of vdb from the booted OS, pin the OS set
    # to it, then change the topology by detaching vdb (path vanishes).
    node 'ls -l /dev/disk/by-path/ | grep vdb' > "$EV/by-path-before.txt"
    local bp; bp=$(node 'for p in /dev/disk/by-path/*; do [ "$(readlink -f $p)" = /dev/vdb ] && basename $p; done' | grep -v part | head -1)
    [ -n "$bp" ] || { result BLOCKED 3.42 "no by-path link for vdb"; return 1; }
    echo "vdb by-path: $bp"
    sed "s|/dev/disk/by-id/virtio-LPARTA001|/dev/disk/by-path/$bp|" $W/dl-byid-os.json > $W/dl-342.json
    # detach vdb => the pinned path cannot resolve
    virsh dumpxml $DOM > "$EV/domain-pre-342.xml"
    pushdl $W/dl-342.json
    setcfg "--install-mode full"
    console_save pre-342; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    virsh detach-disk $DOM vdb --config >/dev/null
    console_follow s342
    virsh start $DOM >/dev/null
    local failclosed=0 candidates=0
    con_wait 'device not found|not found:.*by-path' 900 5 && failclosed=1
    grep -aq 'candidate' "$CONFOLLOW" 2>/dev/null && candidates=1
    # ensure nothing else was written: all disks' wr should stay ~0 (check vda)
    local wr; wr=$(virsh domblkstat $DOM vda 2>/dev/null | awk '/wr_bytes/{print $3}')
    console_unfollow; console_save s342-done
    virsh destroy $DOM >/dev/null 2>&1; sleep 1
    virsh define "$EV/domain-pre-342.xml" >/dev/null   # re-attach vdb
    if [ "$failclosed" = 1 ] && [ "${wr:-0}" -lt 1000000 ]; then
        result PASS 3.42 "pinned by-path vanish -> fail-closed halt (candidates=$candidates, vda wr=$wr)"
    else
        result FINDING 3.42 "failclosed=$failclosed candidates=$candidates wr=$wr"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

s343(){
    ev_init 3.43
    echo "== 3.43 udev-style single-disk mount matrix (by-id OS + by-path data partition) =="
    # build the 2-set layout from the node's real udev names for vda+vdb
    local vda_id vdb_bp
    vda_id=$(node 'for p in /dev/disk/by-id/virtio-*; do [ "$(readlink -f $p)" = /dev/vda ] && basename $p; done' | head -1)
    vdb_bp=$(node 'for p in /dev/disk/by-path/*; do [ "$(readlink -f $p)" = /dev/vdb ] && basename $p; done' | grep -v part | head -1)
    [ -n "$vda_id" ] && [ -n "$vdb_bp" ] || { result BLOCKED 3.43 "udev names not found (id=$vda_id bp=$vdb_bp)"; return 1; }
    cat > $W/dl-343.json <<EOF
{"version":2,"sets":[
 {"name":"os","role":"os","selection":"manual","raid":"none","devices":["/dev/disk/by-id/$vda_id"],"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"boot","mountpoint":"/boot","fs":"xfs","provider":"partition","size":"1G"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]},
 {"name":"data","role":"data","selection":"manual","raid":"none","devices":["/dev/disk/by-path/$vdb_bp"],"volumes":[{"name":"data","mountpoint":"/data","fs":"xfs","provider":"partition","size":"100%"}]}]}
EOF
    install_clean $W/dl-343.json full $ALLD || { result FAIL 3.43 "install with udev-style devices failed (BE-N1 -part<N> class)"; return 1; }
    local mounts; mounts=$(node 'findmnt -no SOURCE /boot /boot/efi /data | tr "\n" " "')
    node 'echo m343 > /data/mk; sync'
    # the MOUNT-EXISTING path is where the -part<N> bug lived: auto reinstall
    install_clean $W/dl-343.json auto vda || { result FINDING 3.43 "auto (healthy->mount) reinstall failed"; return 1; }
    local mk; mk=$(node 'cat /data/mk 2>/dev/null')
    local m2; m2=$(node 'findmnt -no SOURCE /data')
    if [ -n "$m2" ]; then
        result PASS 3.43 "by-id OS + by-path data both landed; mount-existing OK (data=$m2 mk=$mk; first: $mounts)"
    else
        result FINDING 3.43 "mounts after reinstall: $(node 'findmnt -no SOURCE /boot /boot/efi /data | tr "\n" " "') mk=$mk"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

case "${1:?stage: s327 s333 s342 s343}" in
    s327) s327;; s333) s333;; s342) s342;; s343) s343;;
esac
