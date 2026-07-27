#!/bin/bash
# drv-misc4.sh <stage> -- P6 tests on lpx-node004 (5 disks).
# Stages: r6 (3.30 raid6 healthy + degraded single/double pull, instrumented)
#         s346 (spanning-VG refusal) | s347 (spanning-md refusal)
#         s348 (dirty-log read-only classify) | s349 (guard sabotage)
#         s356 (stream override matrix) | s357 (preserve filter over stream)
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
W=${LAB_LAYOUTS:-$HERE}
ALLD="vda vdb vdc vdd vde"

nodescript(){ ctrl "cat > /tmp/ns-$NODE.sh" < "$1"; ctrl "scp -o StrictHostKeyChecking=no /tmp/ns-$NODE.sh $NODE:/tmp/ns.sh >/dev/null 2>&1 && ssh $NODE 'bash /tmp/ns.sh'"; }

instrument_serial_grub(){ # same as grub-diag G1: serial GRUB w/o regenerating cfg
    cat > /tmp/n4-serial.sh <<'EOS'
cp /boot/grub2/grub.cfg /boot/grub2/grub.cfg.prediag
grep -q 'terminal_output serial' /boot/grub2/grub.cfg || \
  sed -i '1i serial --unit=0 --speed=115200\nterminal_input serial console\nterminal_output serial console' /boot/grub2/grub.cfg
echo n4marker > /root/mk; sync
EOS
    nodescript /tmp/n4-serial.sh >/dev/null
}

observe_boot(){ # observe_boot <tag> [secs=420] -> echoes booted|grub-output|silence
    console_follow "$1"
    local deadline=$((SECONDS+${2:-420})) outcome=silence
    while [ $SECONDS -lt $deadline ]; do
        if node 'findmnt -no SOURCE /' 2>/dev/null | grep -q os-root; then outcome=booted; break; fi
        if grep -qE 'error:|Entering rescue mode|grub rescue' "$CONSLOG" "$CONFOLLOW" 2>/dev/null; then outcome=grub-output; break; fi
        sleep 10
    done
    console_unfollow
    shot "$EV/$1-screen"; console_save "$1"
    echo "$outcome"
}

r6(){
    ev_init 3.30
    echo "== 3.30 raid6: healthy install + degraded single & double pull =="
    install_clean $W/dl-n4-raid6.json full $ALLD || { result FAIL 3.30 "raid6 install did not boot"; return 1; }
    local md; md=$(node 'grep -c "\[5/5\]" /proc/mdstat')
    local nv; nv=$(node 'efibootmgr | grep -c TrinityX')
    instrument_serial_grub
    result PASS 3.30-healthy "raid6 healthy [5/5]x$md nvram=$nv"
    # single pull (vdb; vda's ESP survives)
    degraded_pull vdb
    local o1; o1=$(observe_boot r6-single)
    if [ "$o1" = booted ]; then
        local mk; mk=$(node 'cat /root/mk')
        result CHECK 3.30-single "raid6 single-pull degraded BOOTED marker=$mk"
        # double pull on the live degraded system: pull vdc too
        console_save pre-double; virsh destroy $DOM >/dev/null 2>&1; sleep 1; blank vdc; virsh start $DOM >/dev/null
        local o2; o2=$(observe_boot r6-double)
        [ "$o2" = booted ] && result CHECK 3.30-double "raid6 DOUBLE-pull degraded booted (marker=$(node 'cat /root/mk'))" \
                          || result FINDING 3.30-double "raid6 double-pull outcome=$o2"
    else
        grep -aE 'error:|rescue|diskfilter|no such device|md/' "$EV"/console-follow-r6-single.log 2>/dev/null | tail -15 | tee "$EV/r6-grub-lines.txt"
        result FINDING 3.30-single "raid6 single-pull outcome=$o1 (parity degraded gap) -- see r6-grub-lines"
    fi
    install_clean $W/dl-single.json auto $ALLD || true   # restore
}

s346(){
    ev_init 3.46
    echo "== 3.46 spanning-VG destruction refused =="
    memboot_helper 1200 || { result BLOCKED 3.46 "memboot failed"; return 1; }
    cat > /tmp/span.sh <<'EOS'
set -e
sgdisk --zap-all /dev/vdb >/dev/null 2>&1; sgdisk --zap-all /dev/vdc >/dev/null 2>&1
sgdisk -n 1:0:0 /dev/vdb >/dev/null; sgdisk -n 1:0:0 /dev/vdc >/dev/null
sleep 2
pvcreate -ff -y /dev/vdb1 /dev/vdc1 >/dev/null
vgcreate span /dev/vdb1 /dev/vdc1 >/dev/null
lvcreate -y -n keep -l 50%FREE span >/dev/null
mkfs.xfs -f /dev/span/keep >/dev/null 2>&1
mkdir -p /m && mount /dev/span/keep /m && echo spanmark > /m/mk && umount /m
vgchange -an span >/dev/null
echo HAZARD-BUILT
EOS
    nodescript /tmp/span.sh | tee "$EV/hazard.txt" | grep -q HAZARD-BUILT || { result BLOCKED 3.46 "could not build spanning VG"; return 1; }
    # OS set takes vdb (non-persistent), vdc holds the protected side via a persistent data set
    cat > $W/dl-346.json <<'EOF'
{"version":2,"sets":[
 {"name":"os","role":"os","selection":"manual","raid":"none","devices":["/dev/vdb"],"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"boot","mountpoint":"/boot","fs":"xfs","provider":"partition","size":"1G"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]},
 {"name":"prot","role":"data","selection":"manual","raid":"none","persistent":true,"devices":["/dev/vdc"],"volumes":[{"name":"prot","mountpoint":"/prot","fs":"xfs","provider":"partition","size":"100%"}]}]}
EOF
    pushdl $W/dl-346.json
    setcfg "--install-mode full"
    console_save pre-346; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s346
    virsh start $DOM >/dev/null
    local refused=0
    con_wait 'safety_refusal|spans|protected' 900 5 && refused=1
    console_unfollow; console_save s346-done
    # verify the VG + marker survived
    memboot_helper 1200 || { result FINDING 3.46 "post-refusal memboot failed"; return 1; }
    local vg mk
    vg=$(node 'vgscan >/dev/null 2>&1; vgs --noheadings -o vg_name 2>/dev/null | tr -d " " | grep -c span')
    mk=$(node 'vgchange -ay span >/dev/null 2>&1; mkdir -p /m; mount /dev/span/keep /m 2>/dev/null && cat /m/mk; umount /m 2>/dev/null; vgchange -an span >/dev/null 2>&1')
    if [ "$refused" = 1 ] && [ "$vg" -ge 1 ] && [ "$mk" = spanmark ]; then
        result PASS 3.46 "spanning VG refused; VG + marker survived"
    else
        result FINDING 3.46 "refused=$refused vg=$vg mk=$mk"
    fi
    # cleanup hazard + restore
    cat > /tmp/span-clean.sh <<'EOS'
vgchange -an span >/dev/null 2>&1; vgremove -ff -y span >/dev/null 2>&1
pvremove -ff -y /dev/vdb1 /dev/vdc1 >/dev/null 2>&1
sgdisk --zap-all /dev/vdb >/dev/null 2>&1; sgdisk --zap-all /dev/vdc >/dev/null 2>&1
echo CLEANED
EOS
    nodescript /tmp/span-clean.sh >/dev/null 2>&1
    install_clean $W/dl-single.json auto $ALLD || true
}

s347(){
    ev_init 3.47
    echo "== 3.47 spanning-md destruction refused =="
    memboot_helper 1200 || { result BLOCKED 3.47 "memboot failed"; return 1; }
    cat > /tmp/spanmd.sh <<'EOS'
set -e
sgdisk --zap-all /dev/vdb >/dev/null 2>&1; sgdisk --zap-all /dev/vdc >/dev/null 2>&1
sgdisk -n 1:0:0 -t 1:fd00 /dev/vdb >/dev/null; sgdisk -n 1:0:0 -t 1:fd00 /dev/vdc >/dev/null
sleep 2
yes | mdadm --create /dev/md77 --level=1 --raid-devices=2 --metadata=1.2 /dev/vdb1 /dev/vdc1 >/dev/null 2>&1
mkfs.xfs -f /dev/md77 >/dev/null 2>&1
mkdir -p /m && mount /dev/md77 /m && echo mdmark > /m/mk && umount /m
mdadm --stop /dev/md77 >/dev/null 2>&1
echo HAZARD-BUILT
EOS
    nodescript /tmp/spanmd.sh | grep -q HAZARD-BUILT || { result BLOCKED 3.47 "could not build spanning md"; return 1; }
    pushdl $W/dl-346.json
    setcfg "--install-mode full"
    console_save pre-347; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s347
    virsh start $DOM >/dev/null
    local refused=0
    con_wait 'safety_refusal|spans|protected' 900 5 && refused=1
    console_unfollow; console_save s347-done
    memboot_helper 1200 || { result FINDING 3.47 "post-refusal memboot failed"; return 1; }
    local sb mk
    sb=$(node 'mdadm --examine /dev/vdc1 2>/dev/null | grep -c "Array UUID"')
    mk=$(node 'mdadm --assemble /dev/md77 /dev/vdb1 /dev/vdc1 >/dev/null 2>&1; mkdir -p /m; mount /dev/md77 /m 2>/dev/null && cat /m/mk; umount /m 2>/dev/null; mdadm --stop /dev/md77 >/dev/null 2>&1')
    if [ "$refused" = 1 ] && [ "$sb" -ge 1 ] && [ "$mk" = mdmark ]; then
        result PASS 3.47 "spanning md refused; superblock + marker survived"
    else
        result FINDING 3.47 "refused=$refused superblock=$sb mk=$mk"
    fi
    cat > /tmp/md-clean.sh <<'EOS'
mdadm --stop /dev/md77 >/dev/null 2>&1
mdadm --zero-superblock /dev/vdb1 /dev/vdc1 >/dev/null 2>&1
sgdisk --zap-all /dev/vdb >/dev/null 2>&1; sgdisk --zap-all /dev/vdc >/dev/null 2>&1
EOS
    nodescript /tmp/md-clean.sh >/dev/null 2>&1
    install_clean $W/dl-single.json auto $ALLD || true
}

s348(){
    ev_init 3.48
    echo "== 3.48 dirty-log data set: read-only classify =="
    # OS + data on vdb, then sysrq-b mid-write for a dirty xfs log
    cat > $W/dl-348.json <<'EOF'
{"version":2,"sets":[
 {"name":"os","role":"os","selection":"discover","raid":"none","count":1,"match":{"min_size":"10G"},"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"boot","mountpoint":"/boot","fs":"xfs","provider":"partition","size":"1G"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]},
 {"name":"data","role":"data","selection":"manual","raid":"none","devices":["/dev/vdb"],"volumes":[{"name":"data","mountpoint":"/data","fs":"xfs","provider":"partition","size":"100%"}]}]}
EOF
    install_clean $W/dl-348.json full $ALLD || { result FAIL 3.48 "pre-install failed"; return 1; }
    node 'echo d48 > /data/mk; sync'
    # dirty the log: write loop + immediate sysrq crash
    node 'nohup bash -c "for i in \$(seq 1 100000); do echo line\$i >> /data/loop.txt; done" >/dev/null 2>&1 & sleep 2; echo b > /proc/sysrq-trigger' || true
    sleep 5
    # reprovision with auto -- classify must be read-only (no xfs_repair -L)
    setcfg "--install-mode auto"
    console_save pre-348-auto; virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram
    console_follow s348-auto
    virsh start $DOM >/dev/null
    local booted=0; wait_boot 1200 && booted=1
    local repairL; repairL=$(grep -ac 'xfs_repair -L\|xfs_repair.*-L ' "$CONFOLLOW" 2>/dev/null)
    console_unfollow; console_save s348-done
    local mk=""; [ "$booted" = 1 ] && mk=$(node 'cat /data/mk 2>/dev/null')
    if [ "${repairL:-0}" = 0 ] && { [ "$mk" = d48 ] || [ "$booted" = 0 ]; }; then
        result PASS 3.48 "no mutating repair during classify (repair-L hits=0); booted=$booted marker=$mk"
    else
        result FINDING 3.48 "repairL=$repairL booted=$booted mk=$mk"
    fi
    [ "$booted" = 1 ] || install_clean $W/dl-single.json auto $ALLD || true
}

case "${1:?stage: r6 s346 s347 s348}" in
    r6) r6;; s346) s346;; s347) s347;; s348) s348;;
esac
