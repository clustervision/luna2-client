#!/bin/bash
# drv-new.sh <stage> -- P8 new-FTA regression tests (post-redeploy, new binaries).
# Stages: t363 (md-name stability, n2) | t364 (disk-too-small refusal, n2)
#         t365 (NB-9 stale cross-array VG, n3)
# Also: r6retest (3.62 confirm the real BE-B1 stub fix boots degraded raid6, n4)
set -u
: "${N:?set N before sourcing}"
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
W=${LAB_LAYOUTS:-$HERE}
nodescript(){ ctrl "cat > /tmp/ns-$NODE.sh" < "$1"; ctrl "scp -o StrictHostKeyChecking=no /tmp/ns-$NODE.sh $NODE:/tmp/ns.sh >/dev/null 2>&1 && ssh $NODE 'bash /tmp/ns.sh'"; }

t363(){ # md-name stability across reboots (BE-N4 homehost)
    ev_init 3.63
    echo "== 3.63 md array names stable across reboots (homehost=any) =="
    install_clean $W/dl-raid1-vdbc.json full vda vdb vdc || { result FAIL 3.63 "raid1 install failed"; return 1; }
    local n1; n1=$(node 'ls /dev/md/ 2>/dev/null | tr "\n" " "'); local m127a; m127a=$(node 'ls /dev/md127* 2>/dev/null | wc -l')
    console_save pre-reboot1; virsh destroy $DOM >/dev/null 2>&1; sleep 1; virsh start $DOM >/dev/null; wait_boot 500 || { result FINDING 3.63 "reboot1 no boot"; return 1; }
    local n2; n2=$(node 'ls /dev/md/ 2>/dev/null | tr "\n" " "')
    console_save pre-reboot2; virsh destroy $DOM >/dev/null 2>&1; sleep 1; virsh start $DOM >/dev/null; wait_boot 500 || { result FINDING 3.63 "reboot2 no boot"; return 1; }
    local n3 m127c; n3=$(node 'ls /dev/md/ 2>/dev/null | tr "\n" " "'); m127c=$(node 'ls /dev/md127* 2>/dev/null | wc -l')
    echo "md names: '$n1' -> '$n2' -> '$n3'; md127 present: $m127a/$m127c"
    if [ -n "$n1" ] && [ "$n1" = "$n3" ] && [ "$m127c" = 0 ]; then
        result PASS 3.63 "md names stable across 2 reboots ('$n3'), no md127 fallback"
    else
        result FINDING 3.63 "names '$n1'->'$n3' md127=$m127c"
    fi
    install_clean $W/dl-single.json auto vda vdb vdc || true
}

t364(){ # disk too small for layout -> loud refusal (BE-G6)
    ev_init 3.64
    echo "== 3.64 disk-too-small preflight refusal =="
    # OS set forced onto the 8G vdb with a fixed 20G root PARTITION -> impossible
    cat > $W/dl-364.json <<'EOF'
{"version":2,"sets":[{"name":"os","role":"os","selection":"manual","raid":"none","devices":["/dev/vdb"],"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"boot","mountpoint":"/boot","fs":"xfs","provider":"partition","size":"1G"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"partition","size":"20G"}]}]}
EOF
    local mark; mark=$(ctrl_log_mark)
    pushdl $W/dl-364.json
    setcfg "--install-mode full"
    console_save pre-364; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram; blank vda vdb vdc
    console_follow s364
    virsh start $DOM >/dev/null
    local refused=0 timeout30=0
    ctrl_log_wait 'too small for layout|need >=|insufficient' 900 && refused=1
    ctrl_log_since "$mark" | grep -qa 'timed out waiting for partitions' && timeout30=1
    console_unfollow; console_save s364-done
    ctrl_log_since "$mark" > "$EV/ctrl-log.txt"
    # no write to vda (the real OS disk stays blank)
    local wr; wr=$(virsh domblkstat $DOM vda 2>/dev/null | awk '/wr_bytes/{print $3}')
    if [ "$refused" = 1 ] && [ "$timeout30" = 0 ]; then
        result PASS 3.64 "loud too-small refusal (no 30s partition timeout); vda untouched wr=$wr"
    else
        result FINDING 3.64 "refused=$refused genericTimeout=$timeout30 wr=$wr"
    fi
    install_clean $W/dl-single.json auto vda vdb vdc || true
}

t365(){ # NB-9 stale cross-array VG (BE-A6b) -- needs >=3 disks
    ev_init 3.65
    echo "== 3.65 stale same-name VG on disassembled array (NB-9) =="
    # build a hidden 'os' VG on an md over vdb1+vdc1, then stop the array
    memboot_helper 1200 || { result BLOCKED 3.65 "memboot failed"; return 1; }
    cat > /tmp/hide.sh <<'EOS'
set -e
sgdisk --zap-all /dev/vdb >/dev/null 2>&1; sgdisk --zap-all /dev/vdc >/dev/null 2>&1
sgdisk -n 1:0:0 -t 1:fd00 /dev/vdb >/dev/null; sgdisk -n 1:0:0 -t 1:fd00 /dev/vdc >/dev/null
sleep 2
yes | mdadm --create /dev/md99 --level=1 --raid-devices=2 --metadata=1.2 /dev/vdb1 /dev/vdc1 >/dev/null 2>&1
pvcreate -ff -y /dev/md99 >/dev/null
vgcreate os /dev/md99 >/dev/null
lvcreate -y -n root -l 50%FREE os >/dev/null
vgchange -an os >/dev/null 2>&1
mdadm --stop /dev/md99 >/dev/null 2>&1
echo HIDDEN-VG-BUILT
EOS
    nodescript /tmp/hide.sh | grep -q HIDDEN-VG-BUILT || { result BLOCKED 3.65 "could not build hidden VG"; return 1; }
    # install 1: OS on vda ONLY (vdb/vdc hidden VG out of scope) -> must succeed + leave it alone
    install_clean $W/dl-single.json full vda || { result FINDING 3.65a "install selecting only vda failed (hidden VG interfered)"; return 1; }
    result PASS 3.65a "install (vda only) succeeded; hidden cross-disk VG left untouched (fence)"
    # install 2: select vda+vdb (vdb is a member of the hidden VG's array) -> NB-9 must purge
    local mark; mark=$(ctrl_log_mark)
    cat > $W/dl-365b.json <<'EOF'
{"version":2,"sets":[{"name":"os","role":"os","selection":"manual","raid":"none","devices":["/dev/vda"],"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"boot","mountpoint":"/boot","fs":"xfs","provider":"partition","size":"1G"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]},
 {"name":"scratch","role":"data","selection":"manual","raid":"none","devices":["/dev/vdb"],"volumes":[{"name":"s","mountpoint":"/scratch","fs":"xfs","provider":"partition","size":"100%"}]}]}
EOF
    install_clean $W/dl-365b.json full vda || { result FINDING 3.65b "install selecting vda+vdb FAILED -- NB-9 lvcreate exit5?"; ctrl_log_since "$mark" > "$EV/ctrl-log-365b.txt"; return 1; }
    local vgcount; vgcount=$(node 'vgs --noheadings -o vg_name 2>/dev/null | grep -c os')
    ctrl_log_since "$mark" > "$EV/ctrl-log-365b.txt"
    if [ "${vgcount:-0}" = 1 ]; then
        result PASS 3.65b "reinstall selecting a stale-array member: NB-9 purged the hidden 'os' VG, install booted, exactly 1 'os' VG"
    else
        result FINDING 3.65b "os VG count after reinstall = $vgcount (expect 1)"
    fi
    install_clean $W/dl-single.json auto vda vdb vdc || true
}

r6retest(){ # 3.62 confirm: real BE-B1 fix boots degraded raid6 (new binaries)
    ev_init 3.62
    echo "== 3.62 parity degraded boot with the REAL stub fix (new binaries) =="
    local ALLD="vda vdb vdc vdd vde"
    install_clean $W/dl-n4-raid6.json full $ALLD || { result FAIL 3.62 "raid6 install failed on new binaries"; return 1; }
    node 'echo r62 > /root/mk; sync'
    # verify the stub now carries the insmods
    node 'grep -c "insmod diskfilter" /boot/efi/EFI/*/grub.cfg' > "$EV/stub-has-insmod.txt"
    CLEAN_INSTALL_DONE=1
    degraded_pull vdb   # blank a non-boot leg
    if wait_boot 500; then
        local mk; mk=$(node 'cat /root/mk 2>/dev/null')
        [ "$mk" = r62 ] && result PASS 3.62 "raid6 DEGRADED boot now WORKS with the stub-insmod fix (marker=$mk)" \
                        || result FINDING 3.62 "degraded booted but marker=$mk"
    else
        shot "$EV/r62-fail"; console_save r62-fail
        result FINDING 3.62 "degraded raid6 still fails after the fix -- see evidence"
    fi
    install_clean $W/dl-single.json auto $ALLD || true
}

case "${1:?stage: t363 t364 t365 r6retest}" in
    t363) t363;; t364) t364;; t365) t365;; r6retest) r6retest;;
esac
