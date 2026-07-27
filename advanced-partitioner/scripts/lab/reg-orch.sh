# reg-orch.sh -- campaign orchestration helpers, sourced AFTER nreg.sh:
#     CLUSTER=lpx N=<num> source nreg.sh; source reg-orch.sh
# Adds evidence capture, console-log polling, invariant-enforcing install/
# pull primitives, phase detection, controller-mutation revert traps, and
# result recording. Everything destructive routes through install_clean /
# degraded_pull -- never call raw virsh start for a reinstall.

# LAB_WORK = writable scratch/evidence dir (was hardcoded /root/lpart-uefi-work);
# LAB_LAYOUTS = where dl-*.json + empty-layout.json live (this script's dir).
LAB_WORK=${LAB_WORK:-$HOME/lpart-uefi-work}
LAB_LAYOUTS=${LAB_LAYOUTS:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}
EVROOT=${EVROOT:-$LAB_WORK/evidence/$(date +%Y-%m-%d)-campaign}
CONSLOG=/var/log/libvirt/console/$DOM.log
RESULTS_MD=${RESULTS_MD:-$EVROOT/RESULTS-campaign.md}
CLEAN_INSTALL_DONE=0

ev_init(){ # ev_init <testid>
    TESTID=$1
    EV="$EVROOT/$1-n$N"
    mkdir -p "$EV"
    [ -f "$RESULTS_MD" ] || { mkdir -p "$EVROOT"; echo "| test | node | verdict | fta | msg |" > "$RESULTS_MD"; echo "|---|---|---|---|---|" >> "$RESULTS_MD"; }
}

console_save(){ # console_save <tag> -- MUST run before any destroy/start
    [ -f "$CONSLOG" ] && cp -f "$CONSLOG" "$EV/console-${1:-save}-$(date +%H%M%S).log" 2>/dev/null
}

con_wait(){ # con_wait <regex> [timeout=600] [poll=5] -- grep the LIVE console log
    # (checks the follow-capture too, so text survives the auto-reboot truncation)
    local re=$1 t=${2:-600} p=${3:-5} end=$((SECONDS+${2:-600}))
    while [ $SECONDS -lt $end ]; do
        grep -qE "$re" "$CONSLOG" ${CONFOLLOW:+"$CONFOLLOW"} 2>/dev/null && return 0
        sleep "$p"
    done
    return 1
}

CTRLLOG=/var/log/cluster-messages/$NODE.messages
ctrl_log_wait(){ # ctrl_log_wait <regex> [timeout=600] [since-marker] -- the
    # lpart binaries log through rsyslog to the CONTROLLER per-node file;
    # the serial console only carries the wrapper's Luna2: lines (TUI eats
    # the rest). Use this for any lpart-internal text detection.
    local re=$1 t=${2:-600} end=$((SECONDS+${2:-600}))
    while [ $SECONDS -lt $end ]; do
        ctrl "grep -aqE '$re' $CTRLLOG" 2>/dev/null && return 0
        sleep 8
    done
    return 1
}
ctrl_log_grep(){ ctrl "grep -aE '$1' $CTRLLOG" 2>/dev/null; }
ctrl_log_mark(){ ctrl "wc -l < $CTRLLOG" 2>/dev/null; } # line count as a since-marker
ctrl_log_since(){ ctrl "tail -n +$(( ${1:-0} + 1 )) $CTRLLOG" 2>/dev/null; }

console_follow(){ # console_follow <tag> -- tail -F capture that SURVIVES the
    # append='off' truncation when the installer auto-reboots into the OS.
    CONFOLLOW="$EV/console-follow-${1:-cap}.log"
    ( exec tail -n +1 -F "$CONSLOG" >> "$CONFOLLOW" 2>/dev/null ) &
    CONFOLLOW_PID=$!
}
console_unfollow(){ [ -n "${CONFOLLOW_PID:-}" ] && kill "$CONFOLLOW_PID" 2>/dev/null; CONFOLLOW_PID=""; }

install_clean(){ # install_clean <layout|-> <mode> <disks-to-blank...>
    # THE destructive-reinstall primitive: config push, destroy, NVRAM reset,
    # blank, start, wait_boot. '-' keeps the current disklayout.
    local layout=$1 mode=$2; shift 2
    [ "$layout" != - ] && pushdl "$layout"
    setcfg "--install-mode $mode"
    console_save pre-install
    virsh destroy $DOM >/dev/null 2>&1; sleep 1
    reset_nvram
    blank "$@"
    console_follow "install-$mode"
    virsh start $DOM >/dev/null || { echo "install_clean: virsh start failed" >&2; return 2; }
    if wait_boot 1200; then CLEAN_INSTALL_DONE=1; console_unfollow; return 0; fi
    console_unfollow
    shot "$EV/installfail" 2>/dev/null || shot installfail
    console_save installfail
    return 1
}

degraded_pull(){ # degraded_pull <disk> [detach] -- invariant 2: no NVRAM reset,
    # only allowed right after a successful install_clean.
    local disk=$1 how=${2:-blank}
    [ "$CLEAN_INSTALL_DONE" = 1 ] || { echo "HARNESS-ERROR: degraded_pull without preceding install_clean" >&2; return 9; }
    CLEAN_INSTALL_DONE=0
    console_save pre-pull
    virsh destroy $DOM >/dev/null 2>&1; sleep 1
    if [ "$how" = detach ]; then
        virsh dumpxml $DOM > "$EV/domain-pre-detach.xml"
        virsh detach-disk $DOM "$disk" --config >/dev/null || { echo "detach-disk $disk failed" >&2; return 2; }
    else
        blank "$disk"
    fi
    virsh start $DOM >/dev/null
}

restore_disk(){ # restore_disk [detach] -- undo a detach (blank pulls need no undo)
    if [ "${1:-}" = detach ] && [ -f "$EV/domain-pre-detach.xml" ]; then
        virsh destroy $DOM >/dev/null 2>&1; sleep 1
        virsh define "$EV/domain-pre-detach.xml" >/dev/null
    fi
}

# Phase markers, calibrated 2026-07-12 from a real lpx install console:
# the lpart phases run under the TUI (no per-tool lines on serial); the
# WRAPPER's banner lines are the reliable serial markers. The whole storage
# pipeline (partition + extract) runs inside "Running partscript", so the
# extract phase is detected by write volume (domblkstat), not by text.
PHASE_RE_partition=${PHASE_RE_partition:-'Luna2: Running partscript'}
PHASE_RE_bootloader=${PHASE_RE_bootloader:-'Luna2: Running postscript'}
EXTRACT_WR_BYTES=${EXTRACT_WR_BYTES:-500000000}   # ~500MB into the target disk
EXTRACT_DISK=${EXTRACT_DISK:-vdb}

phase_wait(){ # phase_wait <partition|extract|bootloader> [timeout=900]
    local t=${2:-900}
    case "$1" in
        extract)
            local end=$((SECONDS+t)) wr
            # first wait for the part phase to begin at all
            con_wait "$PHASE_RE_partition" "$t" 3 || return 1
            while [ $SECONDS -lt $end ]; do
                wr=$(virsh domblkstat $DOM $EXTRACT_DISK 2>/dev/null | awk '/wr_bytes/{print $3}')
                [ "${wr:-0}" -ge "$EXTRACT_WR_BYTES" ] && return 0
                sleep 3
            done
            return 1 ;;
        partition)  con_wait "$PHASE_RE_partition" "$t" 3 ;;
        bootloader) con_wait "$PHASE_RE_bootloader" "$t" 3 ;;
        *) return 2 ;;
    esac
}

yank(){ console_save pre-yank; virsh destroy $DOM >/dev/null 2>&1; }

memboot_helper(){ # memboot_helper [timeout=1200] [blank-disks...] -- empty layout + memboot
    # Like sanitize, an installed disk boots the OLD OS via the ESP removable
    # path even after reset_nvram, so memboot never runs. Blank vda (or the
    # given disks) to force a netboot; memboot is RAM-only so this is harmless.
    local t=${1:-1200}; shift || true
    local bd=("$@")
    if [ ${#bd[@]} -eq 0 ]; then
        # blank EVERY disk this node actually has, else a stale ESP on a
        # non-vda disk (from a prior multi-disk install) traps OVMF at grub>
        bd=()
        for d in vda vdb vdc vdd vde; do [ -f "$(diskfile $d)" ] && bd+=("$d"); done
        [ ${#bd[@]} -eq 0 ] && bd=(vda)
    fi
    pushdl "$LAB_LAYOUTS/empty-layout.json"
    setcfg "--install-mode memboot"
    console_save pre-memboot
    virsh destroy $DOM >/dev/null 2>&1; sleep 1
    reset_nvram
    blank "${bd[@]}"
    virsh start $DOM >/dev/null
    local end=$((SECONDS+t))
    while [ $SECONDS -lt $end ]; do
        local fs; fs=$(node 'findmnt -no FSTYPE /' 2>/dev/null)
        case "$fs" in tmpfs|rootfs) return 0;; esac
        sleep 20
    done
    return 1
}

# --- controller mutation with guaranteed revert -------------------------------
CTRL_REVERTS=()
_ctrl_revert_all(){ local i n=${#CTRL_REVERTS[@]}; for ((i=n-1;i>=0;i--)); do ctrl "${CTRL_REVERTS[$i]}" >/dev/null 2>&1; done; CTRL_REVERTS=(); }
trap _ctrl_revert_all EXIT
ctrl_mutate(){ # ctrl_mutate <apply-cmd> <revert-cmd> [self-revert-secs=900]
    local apply=$1 revert=$2 secs=${3:-900}
    echo "$revert" >> "$EV/ctrl-reverts.sh"
    CTRL_REVERTS+=("$revert")
    # belt: delayed self-revert on the controller survives a dead orchestrator
    ctrl "nohup bash -c 'sleep $secs; $revert' >/dev/null 2>&1 &"
    ctrl "$apply"
}
ctrl_revert_now(){ _ctrl_revert_all; }

result(){ # result <verdict PASS|FAIL|FINDING|BLOCKED|CHECK> <fta-tag> <msg...>
    local verdict=$1 fta=$2; shift 2
    echo "| $TESTID | n$N | $verdict | $fta | $* |" >> "$RESULTS_MD"
    echo "[$TESTID n$N] $verdict ($fta): $*"
    case "$verdict" in PASS) ;; *) shot "$EV/verdict" 2>/dev/null; console_save verdict;; esac
}

node_forensics(){ # node_forensics [tag] -- best-effort state dump via SSH
    { echo "== $(date) ${1:-} =="
      node 'cat /proc/mdstat; echo ---; vgs 2>/dev/null; lvs 2>/dev/null; echo ---; blkid; echo ---; lsblk -o NAME,SIZE,FSTYPE,MOUNTPOINT,SERIAL'
    } >> "$EV/node-state.txt" 2>&1
}

# --- GRUB prompt driving (virsh send-key) --------------------------------------
grub_type(){ # grub_type <string> -- types the string + ENTER at the console
    local s=$1 i c code
    for ((i=0;i<${#s};i++)); do
        c=${s:$i:1}
        case "$c" in
            ' ') code=KEY_SPACE;; '-') code=KEY_MINUS;; '.') code=KEY_DOT;;
            '/') code=KEY_SLASH;; '=') code=KEY_EQUAL;; ',') code=KEY_COMMA;;
            "'") code=KEY_APOSTROPHE;; '(') code="KEY_LEFTSHIFT KEY_9";; ')') code="KEY_LEFTSHIFT KEY_0";;
            '$') code="KEY_LEFTSHIFT KEY_4";; '_') code="KEY_LEFTSHIFT KEY_MINUS";;
            [A-Z]) code="KEY_LEFTSHIFT KEY_$c";;
            [a-z0-9]) code=KEY_$(echo "$c" | tr 'a-z' 'A-Z');;
            *) continue;;
        esac
        virsh send-key $DOM $code >/dev/null 2>&1
        sleep 0.08
    done
    virsh send-key $DOM KEY_ENTER >/dev/null 2>&1
}
