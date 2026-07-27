#!/bin/bash
# drv-grub-diag.sh <stage> -- P1 parity-RAID degraded-boot diagnosis (lpx-node003).
# Stages: g1 (instrumented healthy raid5), g2 (degraded-by-blank), g3 (grub-prompt
# forensics), g4 (clean reinstall + degraded-by-detach), r10 (raid10 clean confirm).
# Run stages one at a time; classify evidence between stages.
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
ev_init grub-diag
W=${LAB_LAYOUTS:-$HERE}

# run a multi-line script on the node (through the controller hop)
nodescript(){ # nodescript <local-script>
    ctrl "cat > /tmp/ns-$NODE.sh" < "$1"
    ctrl "scp -o StrictHostKeyChecking=no /tmp/ns-$NODE.sh $NODE:/tmp/ns.sh >/dev/null 2>&1 && ssh $NODE 'bash /tmp/ns.sh'"
}

g1(){
    echo "== G1: healthy raid5 install (instrumented) =="
    install_clean $W/dl-n3-raid5.json full vda vdb vdc vdd || { result FAIL 3.29-G1 "raid5 install did not boot"; return 1; }
    local md; md=$(node 'grep -c "\[4/4\]" /proc/mdstat')
    local nv; nv=$(node 'efibootmgr | grep -c TrinityX')
    local mdu; mdu=$(node 'grep -c mduuid /boot/grub2/grub.cfg')
    echo "healthy: [4/4]x$md nvram=$nv grub-mduuid=$mdu"
    node 'blkid -s UUID -o value $(findmnt -no SOURCE /boot)' > "$EV/boot-uuid.txt"
    node 'cat /proc/mdstat' > "$EV/mdstat-healthy.txt"
    node 'cp /boot/grub2/grub.cfg /tmp/ && ls -la /boot/efi/EFI' > /dev/null
    # enable GRUB serial output by editing grub.cfg DIRECTLY (grub2-mkconfig
    # would regenerate mduuid hints and undo lpart's degraded-safe rewrite)
    cat > /tmp/g1-serial.sh <<'EOS'
set -e
cp /boot/grub2/grub.cfg /boot/grub2/grub.cfg.prediag
grep -q 'terminal_output serial' /boot/grub2/grub.cfg && { echo already; exit 0; }
sed -i '1i serial --unit=0 --speed=115200\nterminal_input serial console\nterminal_output serial console' /boot/grub2/grub.cfg
# grubenv write probe (H4 evidence): does grub2-editenv work on the raid1 /boot?
grub2-editenv /boot/grub2/grubenv set lpartdiag=1 && echo "grubenv-write: OK" || echo "grubenv-write: FAILED rc=$?"
grep -c mduuid /boot/grub2/grub.cfg | xargs echo mduuid-count:
echo g1marker > /root/mk; sync
EOS
    nodescript /tmp/g1-serial.sh | tee "$EV/g1-serial-probe.txt"
    result PASS 3.29-G1 "healthy raid5 [4/4]x$md nvram=$nv mduuid=$mdu, serial-grub enabled"
}

g2(){
    echo "== G2: degraded by BLANKING vdb (vda ESP survives) =="
    degraded_pull vdb || return 1
    # watch: either the OS boots, or GRUB emits an error on serial, or silence
    console_follow g2-degraded
    local deadline=$((SECONDS+420)) outcome=silence
    while [ $SECONDS -lt $deadline ]; do
        if node 'findmnt -no SOURCE /' 2>/dev/null | grep -q os-root; then outcome=booted; break; fi
        if grep -qE 'error:|Entering rescue mode|grub rescue|GNU GRUB' "$CONSLOG" "$CONFOLLOW" 2>/dev/null; then outcome=grub-output; break; fi
        sleep 10
    done
    console_unfollow
    shot "$EV/g2-screen"; console_save g2
    echo "G2 outcome: $outcome"
    grep -E 'serial|error:|rescue|GRUB|md/|diskfilter|no such device' "$CONFOLLOW" 2>/dev/null | tail -25 | tee "$EV/g2-grub-lines.txt"
    case $outcome in
        booted) local mk; mk=$(node 'cat /root/mk'); result CHECK 3.29-G2 "degraded raid5 BOOTED (marker=$mk) -- earlier failures environmental?";;
        *) result FINDING 3.29-G2 "degraded raid5 outcome=$outcome -- see g2-grub-lines/screenshot";;
    esac
}

g3(){
    echo "== G3: grub-prompt forensics (send-key) =="
    local bootuuid; bootuuid=$(cat "$EV/boot-uuid.txt" 2>/dev/null)
    shot "$EV/g3-0-initial"
    grub_type "set"; sleep 2; shot "$EV/g3-1-set"; console_save g3-set
    grub_type "ls";  sleep 3; shot "$EV/g3-2-ls";  console_save g3-ls
    grub_type "insmod normal"; sleep 2; shot "$EV/g3-3-insmod"
    grub_type "normal"; sleep 5; shot "$EV/g3-4-normal"; console_save g3-normal
    [ -n "$bootuuid" ] && { grub_type "search --fs-uuid --set=x $bootuuid"; sleep 2; grub_type "echo found:\$x"; sleep 2; shot "$EV/g3-5-search"; }
    console_save g3-final
    echo "G3 evidence captured to $EV (classify g3-*.png + console-g3-*)"
}

g4(){
    echo "== G4: clean reinstall then degraded by DETACH =="
    install_clean $W/dl-n3-raid5.json full vda vdb vdc vdd || { result FAIL 3.29-G4 "reinstall did not boot"; return 1; }
    nodescript /tmp/g1-serial.sh >/dev/null 2>&1   # re-instrument serial grub
    degraded_pull vdb detach || return 1
    console_follow g4-detach
    local deadline=$((SECONDS+420)) outcome=silence
    while [ $SECONDS -lt $deadline ]; do
        if node 'findmnt -no SOURCE /' 2>/dev/null | grep -q os-root; then outcome=booted; break; fi
        if grep -qE 'error:|Entering rescue mode|grub rescue' "$CONSLOG" "$CONFOLLOW" 2>/dev/null; then outcome=grub-output; break; fi
        sleep 10
    done
    console_unfollow
    shot "$EV/g4-screen"; console_save g4
    echo "G4 outcome: $outcome"
    grep -E 'error:|rescue|GRUB|md/|diskfilter|no such device' "$CONFOLLOW" 2>/dev/null | tail -20 | tee "$EV/g4-grub-lines.txt"
    case $outcome in
        booted) result FINDING 3.29-G4 "detach BOOTS where blank failed => H2 (blank-disk-in-slot chokes GRUB)";;
        *) result FINDING 3.29-G4 "detach also fails (outcome=$outcome) => H2 eliminated, H1/H4 per G2/G3 evidence";;
    esac
    restore_disk detach
}

r10(){
    echo "== 3.31: raid10 clean degraded confirm =="
    install_clean $W/dl-n3-raid10.json full vda vdb vdc vdd || { result FAIL 3.31 "raid10 install did not boot"; return 1; }
    node 'echo r10marker > /root/mk; sync'
    local md; md=$(node 'grep -c "\[4/4\]" /proc/mdstat')
    degraded_pull vdb
    if wait_boot 500; then
        local mk dmd; mk=$(node 'cat /root/mk'); dmd=$(node "grep -o '\[4/4\]\|\[[0-9]/4\]' /proc/mdstat | sort | uniq -c | tr '\n' ' '")
        if [ "$mk" = r10marker ]; then result PASS 3.31 "raid10 degraded boot OK, marker intact, md: $dmd (healthy was x$md)";
        else result FAIL 3.31 "raid10 degraded booted but marker '$mk' wrong (stale install?)"; fi
    else
        shot "$EV/r10-degfail"; console_save r10-fail
        result FINDING 3.31 "raid10 degraded boot FAILED -- mirror-based, unexpected; see evidence"
    fi
}

case "${1:?stage: g1 g2 g3 g4 r10}" in
    g1) g1;; g2) CLEAN_INSTALL_DONE=1 g2;; g3) g3;; g4) g4;; r10) r10;;
esac
