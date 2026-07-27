#!/bin/bash
# drv-r6-diag.sh <stage> -- focused raid6 degraded-boot diagnosis (lpx-node004).
# raid5 degraded BOOTS, raid6 degraded lands at `grub>` with no serial error:
# the shim's embedded/stub config fails before /boot's grub.cfg (with our
# serial directives) is ever read. Instrument the ESP STUB itself + drive the
# prompt. Stages: d1 (install + instrument stub) | d2 (pull + observe) |
# d3 (send-key forensics) | d4 (restore)
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
ev_init r6diag
W=${LAB_LAYOUTS:-$HERE}

nodescript(){ ctrl "cat > /tmp/ns-$NODE.sh" < "$1"; ctrl "scp -o StrictHostKeyChecking=no /tmp/ns-$NODE.sh $NODE:/tmp/ns.sh >/dev/null 2>&1 && ssh $NODE 'bash /tmp/ns.sh'"; }

d1(){
    echo "== d1: raid6 install + STUB instrumentation =="
    install_clean $W/dl-n4-raid6.json full vda vdb vdc vdd vde || { result FAIL r6diag-d1 "raid6 install failed"; return 1; }
    cat > /tmp/d1.sh <<'EOS'
set -e
echo "--- ESP stub(s):"
for f in /boot/efi/EFI/*/grub.cfg; do echo "== $f"; cat "$f"; done
echo "--- fs uuids:"
BOOTDEV=$(findmnt -no SOURCE /boot)
blkid -s UUID -o value $BOOTDEV | xargs echo BOOTUUID:
grep -c mduuid /boot/grub2/grub.cfg | xargs echo mduuid-in-bootcfg:
# instrument the STUB: serial output + step echoes (it runs BEFORE /boot cfg)
for f in /boot/efi/EFI/*/grub.cfg; do
  cp "$f" "$f.prediag"
  BU=$(blkid -s UUID -o value $BOOTDEV)
  cat > "$f" <<EOG
serial --unit=0 --speed=115200
terminal_output serial console
echo "STUB: start"
insmod part_gpt
insmod xfs
insmod diskfilter
insmod mdraid1x
echo "STUB: modules ok"
search --no-floppy --fs-uuid --set=dev $BU
echo "STUB: search done dev=(\$dev)"
set prefix=(\$dev)/grub2
export prefix
echo "STUB: prefix=\$prefix"
configfile (\$dev)/grub2/grub.cfg
echo "STUB: configfile RETURNED (config failed or missing)"
EOG
done
# serial directives in the real cfg too
grep -q 'terminal_output serial' /boot/grub2/grub.cfg || \
  sed -i '1i serial --unit=0 --speed=115200\nterminal_input serial console\nterminal_output serial console\necho "MAINCFG: loaded"' /boot/grub2/grub.cfg
echo d1marker > /root/mk
sync
echo INSTRUMENTED
EOS
    nodescript /tmp/d1.sh | tee "$EV/d1-instrument.txt" | tail -3
    grep -q INSTRUMENTED "$EV/d1-instrument.txt" || { result BLOCKED r6diag-d1 "instrumentation failed"; return 1; }
    result PASS r6diag-d1 "raid6 healthy + ESP stub instrumented (see d1-instrument.txt)"
}

d2(){
    echo "== d2: pull vdb, observe the instrumented stub =="
    CLEAN_INSTALL_DONE=1
    degraded_pull vdb || return 1
    console_follow d2
    local deadline=$((SECONDS+420)) outcome=silence
    while [ $SECONDS -lt $deadline ]; do
        if node 'findmnt -no SOURCE /' 2>/dev/null | grep -q os-root; then outcome=booted; break; fi
        if grep -qaE 'STUB: configfile RETURNED|grub>' "$CONFOLLOW" 2>/dev/null; then outcome=stub-fell-through; break; fi
        if grep -qaE 'MAINCFG: loaded' "$CONFOLLOW" 2>/dev/null; then outcome=maincfg-loaded; fi
        sleep 10
    done
    console_unfollow
    shot "$EV/d2-screen"; console_save d2
    echo "d2 outcome: $outcome"
    grep -aE 'STUB:|MAINCFG:|error|grub' "$CONFOLLOW" 2>/dev/null | sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' | tee "$EV/d2-stub-trace.txt"
    result FINDING r6diag-d2 "outcome=$outcome -- stub trace in d2-stub-trace.txt"
}

d3(){
    echo "== d3: send-key forensics at the prompt =="
    local bootuuid; bootuuid=$(grep -a 'BOOTUUID:' "$EV/d1-instrument.txt" | awk '{print $2}')
    shot "$EV/d3-0-initial"
    grub_type "set"; sleep 2; shot "$EV/d3-1-set"; console_save d3-set
    grub_type "ls"; sleep 3; shot "$EV/d3-2-ls"; console_save d3-ls
    grub_type "insmod raid6rec"; sleep 2
    grub_type "ls"; sleep 3; shot "$EV/d3-3-ls-after-raid6rec"; console_save d3-ls2
    [ -n "$bootuuid" ] && { grub_type "search --fs-uuid --set=x $bootuuid"; sleep 2; grub_type "echo found: \$x"; sleep 2; shot "$EV/d3-4-search"; console_save d3-search; }
    grub_type "normal"; sleep 5; shot "$EV/d3-5-normal"; console_save d3-normal
    echo "d3 evidence in $EV"
}

d4(){
    echo "== d4: restore node004 =="
    install_clean $W/dl-single.json auto vda vdb vdc vdd vde && result PASS r6diag-d4 "node004 restored" || result FINDING r6diag-d4 "restore failed"
}

case "${1:?stage: d1 d2 d3 d4}" in d1) d1;; d2) d2;; d3) d3;; d4) d4;; esac
