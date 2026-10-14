# luna-installer.sh -- SHARED Luna install-initramfs logic.
#
# CANONICAL SHARED SCRIPT. This file is byte-identical in the redhat (dracut
# 95luna) and ubuntu (initramfs-tools) client trees, and is installed to
# /usr/share/luna/luna-installer.sh inside the install initramfs. The per-distro
# entrypoint sources it after setting up logging and the getargs/getargbool
# helpers:
#   - redhat/suse : 95luna/luna2-start.sh  (dracut; getargs from dracut-lib.sh)
#   - ubuntu      : scripts/local-premount/luna-start  (initramfs-tools; shims)
# Keep the two copies in sync -- edit one, copy to the other.
#
# Written by ClusterVision Development team. This file is part of Luna, a
# cluster provisioning tool, and is free software under the GNU GPL v3+
# (see <http://www.gnu.org/licenses/>); distributed WITHOUT ANY WARRANTY.

# Don't use POSIX compliancy mode !
set +o posix 2>/dev/null || true

# Keep kernel printk noise off the console (e.g. the ipmi_* module loads in
# bmcsetup, which re-run on every install retry) so it does not scribble over
# the installer UI. Messages stay in the kernel ring buffer (dmesg) and are
# snapshotted to /tmp/luna_install.dmesg.last on failure below.
echo 1 > /proc/sys/kernel/printk 2>/dev/null || true

function are_macs_equal () {
    awk -v mac=$1 -v reqmac=$2 'BEGIN{
        if (length(mac) == 17) {
            if (mac == reqmac) {
                print 1
            } else {
                print 0
            }
        } else {
            mac_len = split(mac, mac_array, ":")
            reqmac_len = split(reqmac, reqmac_array, ":")
            j=reqmac_len
            for (i=mac_len; i>0; i--) {
                if ((j>0) && (mac_array[i] == reqmac_array[j])) {
                    j--
                }
            }
            if (j==0) {
                print 1
            } else {
                print 0
            }
        }

    }'
}

function find_nic () {
    REQMAC=$1
    for NIC in /sys/class/net/*; do
        IFNAME=$(basename ${NIC})
        if [ "x${IFNAME}" = "xlo" ]; then
            continue
        fi
        MAC=$(cat ${NIC}/address)
        if [ "$(are_macs_equal $MAC $REQMAC)" -eq 1 ]; then
            echo ${IFNAME}
            break
        fi
    done
}

function luna_init () {
    if [ ! -d /lunatmp ]; then
        mkdir /lunatmp
    fi
}

function luna_start () {
    local luna_ip
    local luna_gw
    local luna_bootproto
    local luna_mac
    echo "$(getargs luna.hostname=)" > /proc/sys/kernel/hostname

    # The SSH daemon resolves the login user via getpwnam(); it rejects every
    # key with "nonexistent user" if root is missing from /etc/passwd. dracut's
    # base initramfs ships a root entry, but the initramfs-tools dhcpcd hook can
    # overwrite /etc/passwd with only its own line, so guarantee root exists
    # before SSH starts. glibc's passwd database defaults to "files", so no
    # nsswitch entry is needed; the single missing piece is the root line.
    grep -q '^root:' /etc/passwd 2>/dev/null || echo 'root:x:0:0:root:/:/bin/sh' >> /etc/passwd
    grep -q '^root:' /etc/group  2>/dev/null || echo 'root:x:0:' >> /etc/group

    # dropbear resolves the login user with getpwnam(), which on glibc dlopens
    # libnss_files.so.2 from the multiarch dir -- only found via ld.so.cache.
    # Refresh the cache so the lookup succeeds (else every key is rejected with
    # "nonexistent user"). Harmless where the cache is already complete.
    command -v ldconfig >/dev/null 2>&1 && ldconfig 2>/dev/null

    # dropbear validates the login user's shell via getusershell(): with no
    # /etc/shells, glibc returns the default {/bin/sh,/bin/csh}, so dracut's root
    # shell (/bin/bash) is rejected as "invalid shell" and every key is denied.
    # List the shells we actually use so the installer SSH login works.
    [ -s /etc/shells ] || printf '/bin/sh\n/bin/bash\n/usr/bin/sh\n/usr/bin/bash\n' > /etc/shells

    # SSH into the installer: prefer dropbear -- one self-contained binary, no
    # privsep dir, no helper-process fan-out. Full OpenSSH sshd is the fallback
    # only if dropbear is somehow absent (it drags in /run/sshd + the 9.8+
    # sshd-session/sshd-auth split, which silently broke SSH into the installer).
    local dropbear_binary
    if [ -x /usr/sbin/dropbear ]; then
        dropbear_binary=/usr/sbin/dropbear
    elif [ -x /usr/local/sbin/dropbear ]; then
        dropbear_binary=/usr/local/sbin/dropbear
    elif [ -x /usr/bin/dropbear ]; then
        dropbear_binary=/usr/bin/dropbear
    fi
    if [ "$dropbear_binary" ]; then
        echo "Luna2: Starting dropbear"
        mkdir -p /etc/dropbear/
        $dropbear_binary -R -p 22 || echo "Luna2: WARN dropbear failed to start"
    else
        if [ -e /usr/sbin/sshd ]; then
            echo "Luna2: Starting ssh (dropbear absent)"
            echo "sshd:x:74:74:Privilege-separated SSH:/var/empty/sshd:/sbin/nologin" >> /etc/passwd
            echo "sshd:x:74:" >> /etc/group
            # Privilege-separation dir: /run/sshd (modern RH/Debian),
            # /var/empty/sshd (older RH), /var/lib/empty (SUSE builds with
            # --with-privsep-path=/var/lib/empty -- "Missing privilege
            # separation directory" killed the installer sshd on Leap 15.6).
            # Empty root-owned dirs are harmless on every family: make all.
            mkdir -p /var/empty/sshd /run/sshd /var/lib/empty
            chown root:root /run/sshd 2>/dev/null
            chmod 0755 /run/sshd
            sshd_err=$(/usr/sbin/sshd 2>&1)
            if [ -n "$sshd_err" ]; then
                echo "Luna2: WARN sshd failed to start: ${sshd_err}"
            fi
        fi
    fi
    echo "Luna2: Start shell on tty2"
    luna_ctty2=/dev/tty2
    setsid -c /bin/sh -i -l 0<>$luna_ctty2 1<>$luna_ctty2 2<>$luna_ctty2 &
    # Periodic progress forwarder (diagnostic): push the tail of the running
    # install log to the loghost every 20s, so an install that HANGS (never
    # reaches luna_finish/retry to flush its log) is still diagnosable on the
    # controller -- the last snapshot before the hang shows where it stopped.
    ( _plh=$(getargs luna.loghost=)
      [ -n "$_plh" ] && while sleep 20; do
          readable_log < /tmp/luna_install.log 2>/dev/null | tail -n 120 2>/dev/null \
            | logger -d -n "$_plh" -P 514 -t luna-progress 2>/dev/null
      done ) &
    # Bounded + instrumented settle: on ubuntu26/resolute an unbounded settle
    # here hung 30-52 MINUTES intermittently (initqueue spinner forever, no
    # network, the install never started -- 2026-07-17), far beyond settle's
    # documented 120s default timeout. Bound it explicitly and print markers
    # so any future hang names its exact line on the console.
    echo "Luna2: pre-settle"
    udevadm settle --timeout=30
    echo "Luna2: post-settle rc=$?"
    # settle is not really reliable
    sleep 5
    echo "Luna2: Set-up network"
    # dracut ships a custom dhclient-script (dhclient-script-luna); the
    # initramfs-tools image does not, so only pass -sf when it is present.
    local dhclient_sf=""
    [ -x /usr/sbin/dhclient-script-luna ] && dhclient_sf="-sf /usr/sbin/dhclient-script-luna"
    luna_bootproto=$(getargs luna.bootproto=)
    luna_ip=$(getargs luna.ip=)
    luna_gw=$(getargs luna.gw=)
    luna_mac=$(getargs luna.mac=)
    echo "Luna2: Trying to find interface for ${luna_mac}"
    luna_nic=$(find_nic ${luna_mac})
    if [ "x$luna_bootproto" = "xdhcp" ]; then
        export LUNA_BOOTPROTO="${luna_bootproto}"
        if [ -x /usr/sbin/dhclient ]; then
            if [ "x${luna_nic}" = "x" ]; then
                echo "Luna2: unable to find NIC for mac ${luna_mac}. Starting dhcp for all interfaces"
                /usr/sbin/dhclient -lf /lunatmp/dhclient.leases $dhclient_sf
            else
                echo "${luna_nic}" > /lunatmp/luna_nic
                export LUNA_BOOTIF="${luna_nic}"
                echo "Luna2: Starting dhcp for ${luna_nic}"
                /usr/sbin/dhclient -lf /lunatmp/dhclient.leases ${luna_nic} $dhclient_sf
                if [ "$luna_gw" ]; then
                    sleep 1
                    echo "Luna2: setting default gateway ${luna_gw}"
                    ip route add default via ${luna_gw} dev ${luna_nic}
                fi
            fi
        else
            echo "Luna2: I need to start dhcp, but dhclient was not found"
        fi
    else
        echo "Luna2: ${luna_ip} for interface with mac ${luna_mac} was specified"
        if [ "x${luna_nic}" = "x" ]; then
            echo "Luna2: unable to find NIC for mac ${luna_mac}. Starting dhcp"
            if [ -x /usr/sbin/dhclient ]; then
                /usr/sbin/dhclient -lf /lunatmp/dhclient.leases $dhclient_sf
            else
                echo "Luna2: I need to start dhcp, but dhclient was not found"
            fi
        else
            echo "${luna_nic}" > /lunatmp/luna_nic
            export LUNA_BOOTIF="${luna_nic}"
            ip a add ${luna_ip} dev ${luna_nic}
            sleep 1
            ip l set dev ${luna_nic} up
            if [ "$luna_gw" ]; then
                sleep 1
                echo "Luna2: setting default gateway ${luna_gw}"
                ip route add default via ${luna_gw} dev ${luna_nic}
            fi
        fi
    fi
}

# The install log is a pty typescript, so it carries the lpart TUI: one
# alternate-screen session per phase, whose content the lpart tools already
# forward themselves. Cut those sessions, the control sequences and the blank
# padding, so the loghost gets the install narrative and not the rendering.
function readable_log () {
    awk 'BEGIN { skip = 0 }
    {
        line = $0; out = ""
        while (1) {
            if (!skip) {
                i = index(line, "\033[?1049h")
                if (i == 0) { out = out line; break }
                out = out substr(line, 1, i - 1); line = substr(line, i + 8); skip = 1
            } else {
                i = index(line, "\033[?1049l")
                if (i == 0) break
                line = substr(line, i + 8); skip = 0
            }
        }
        if (out != "") print out
    }' \
    | sed -e 's/\x1b\[[0-9:;<=>?]*[a-zA-Z]//g' -e 's/\x1b\][^\x07\x1b]*\x07//g' \
          -e 's/\x1b\][^\x07\x1b]*\x1b\\//g' -e 's/\x1b.//g' -e 's/\r//g' \
    | grep -v '^[[:space:]]*$'
}

function luna_finish () {
    local luna_loghost=$(getargs luna.loghost=)
    if [ "$luna_loghost" ]; then
        readable_log < /tmp/luna_install.log | logger -d -n $luna_loghost -P 514
    else
        echo "Luna2: loghost not defined, cannot forward logs."
    fi
    # shutdown sshd
    sshdpids=$(ps ax|grep sshd|grep -v grep|awk '{ print $1 }')
    if [ "$sshdpids" ]; then
        kill $sshdpids
    fi
    dbpids=$(ps ax|grep dropbear|grep -v grep|awk '{ print $1 }')
    if [ "$dbpids" ]; then
        kill $dbpids
    fi
    # shutdown dhclient
    /usr/sbin/dhclient -lf /lunatmp/dhclient.leases -x
    # bring interfaces down
    if [ -f /lunatmp/luna_nic ]; then
        luna_nic=$(cat /lunatmp/luna_nic)
        ip addr flush ${luna_nic}
        ip link set dev ${luna_nic} down
    else
        if [ -f /lunatmp/dhclient.leases ]; then
            cat /lunatmp/dhclient.leases  | \
                sed -n '/interface /s/\W*interface "\(.*\)";/\1/p' | \
                while read iface; do
                    ip addr flush $iface
                    ip link set dev $iface down
                done
        fi
    fi
    mv /tmp/luna_install.log /sysroot/var/log/
    # The lpart steps write their own per-tool logs, and /tmp is the initramfs --
    # it does not survive the pivot, so without this the detailed record of the
    # install is discarded exactly when someone would want to read it. These are
    # also the clean copy: luna_install.log is a pty typescript and carries every
    # TUI redraw, so it is complete but verbose.
    if [ -d /tmp/install_files/logs ]; then
        mkdir -p /sysroot/var/log/luna
        cp -a /tmp/install_files/logs/. /sysroot/var/log/luna/ 2>/dev/null \
            || echo "Luna2: could not preserve the lpart logs"
    fi
    # On a pty the install's children see a terminal and emit control sequences
    # (aria2c and curl draw progress meters, the lpart steps draw a TUI). Strip
    # them so the saved log stays readable; a failure here must not cost us the
    # log, hence the guard.
    sed -i 's/\x1b\[[0-9:;<=>?]*[a-zA-Z]//g; s/\r$//' /sysroot/var/log/luna_install.log 2>/dev/null \
        || echo "Luna2: could not strip terminal escapes from luna_install.log"
    # kill shell on tty2
    ps h t tty2 o pid | while read pid; do kill -9 $pid; done

    # The pre-pivot guard (luna-pivot-guard.sh) boots /sysroot only when this
    # exists: the installer reached its end, the root is complete.
    : > /run/luna-install-finished
}

# The console the kernel uses last: the last console= on the command line is
# /dev/console, where lpart-phase renders the installer. Interactive recovery
# (retry countdown, park shell) must sit on the same device: on a
# serial-console node tty1 is a VT nobody sees. console=tty0 ("the foreground
# VT") maps to tty1; options after the comma (ttyS0,115200n8) are dropped.
function _last_kernel_console () {
    local _o _c="" _args=()
    { read -r -a _args < /proc/cmdline; } 2>/dev/null
    for _o in "${_args[@]}"; do
        case "$_o" in
            console=*) _c="${_o#console=}"; _c="${_c%%,*}" ;;
        esac
    done
    [ -n "$_c" ] || return 0
    [ "$_c" = "tty0" ] && _c=tty1
    _c="/dev/${_c#/dev/}"
    [ -c "$_c" ] && echo -n "$_c"
    return 0
}

function _get_luna_ctty () {
    local luna_ctty
    luna_ctty=$(getargs luna.ctty=)
    [ -n "${luna_ctty}" ] || luna_ctty=$(_last_kernel_console)
    [ -n "${luna_ctty}" ] || luna_ctty="/dev/tty1"
    echo -n "$luna_ctty"
}

# luna_trim prints $1 without leading/trailing whitespace. Unlike $(echo $x)
# it does not glob-expand: a password holding '*' stays the password.
function luna_trim () {
    local v="$1"
    v="${v#"${v%%[![:space:]]*}"}"
    v="${v%"${v##*[![:space:]]}"}"
    printf '%s' "$v"
}

function get_credentials {
    LUNA_INI='/trinity/local/luna/node/config/luna.ini'
    if [ -f "$LUNA_INI" ]; then
        while IFS='=' read -ra line; do
            if [ ! "$(luna_trim "$line" | grep -E '^(#|;)')" ] && [ "$line" ]; then
                if [ "$(luna_trim "$line" | grep '^\[')" ]; then
                    section=$(luna_trim "$line" | grep -oE "[A-Z]+")
                else
                    key=$(luna_trim "${line[0]}")
                    value=$(luna_trim "${line[1]}")
                    declare -x "${section}_${key}"="$value"
                fi
            fi
        done < "$LUNA_INI"
    else
        >&2 echo "No config file: $LUNA_INI"
        echo ":"
    fi
    echo "${API_USERNAME}:${API_PASSWORD}"
}

# curl's --interface takes the next word as its argument. With an empty
# $luna_nic (no NIC matched luna.mac; dhcp then ran on every interface) the
# unquoted expansion vanished, --interface swallowed the next option and the
# fetch failed for ever ("Could not get install script"). Bind to the NIC
# only when it is known.
function luna_curl () {
    local -a _opts=()
    [ -n "$INSECURE" ] && _opts+=("$INSECURE")
    [ -n "$luna_nic" ] && _opts+=(--interface "$luna_nic")
    curl "${_opts[@]}" "$@"
}

function fetch_token {
    CREDENTIALS=$(get_credentials)
    USERNAME=$(echo "$CREDENTIALS" | awk -F':' '{ print $1 }')
    PASSWORD=$(echo "$CREDENTIALS" | awk -F':' '{ print $2 }')
    export TPM2TOOLS_TCTI="device:/dev/tpm0"
    command -v ldconfig >/dev/null 2>&1 && ldconfig
    TPM=$(tpm2_pcrread sha256:0 2> /dev/null | grep '0x' | awk -F' : ' '{ print $2 }')
#    if [ "$TPM" == "" ]; then
#        TPM=$(ip a| grep -oE '[0-9a-z]+:[0-9a-z]+:[0-9a-z]+:[0-9a-z]+:[0-9a-z]+:[0-9a-z]+'|grep -v '00:00:00'|tr '\n' ':')
#    fi
    json='{"tpm_sha256":"'$TPM'","username":"'$USERNAME'","password":"'$PASSWORD'"}'
    luna_url=$(getargs luna.url=)
    luna_node=$(getargs luna.node=)
    luna_verifycert=$(getargs luna.verifycert= | tr 'A-Z' 'a-z')
    if [ "$luna_verifycert" == "no" ] || [ "$luna_verifycert" == "false" ]; then
        INSECURE='--insecure'
    fi
    luna_mac=$(getargs luna.mac=)
    luna_nic=$(find_nic "${luna_mac}")
    luna_curl -X POST -H "Content-Type: application/json" -d "$json" -s "${luna_url}/tpm/${luna_node}" > /lunatmp/token.json
    valid=$(grep token /lunatmp/token.json)
    if [ "$valid" ]; then
        token=$(cat /lunatmp/token.json | awk '{ print $2 }' | grep -oE '"(\S+)"')
        token=$(echo "${token:1:-1}")
        echo "$token" > /lunatmp/token.dat
        echo "$token"
    else
        >&2 cat /lunatmp/token.json
        >&2 echo
        echo "None"
    fi
}

# The install script reported where it stopped with a token issued when it
# started, which a long erase outlives: report that state again with a fresh one.
function luna_post_state () {
    local _token
    _token=$(fetch_token 2>/dev/null)
    [ -n "$_token" ] && [ "$_token" != "None" ] || return 0
    luna_curl -s -m 10 -X POST -H "x-access-tokens: $_token" -H "Content-Type: application/json" \
        -d '{"monitor": {"status": {"'"$luna_node"'": {"state": "'"$1"'"}}}}' \
        "${luna_url}/monitor/node/${luna_node}" > /dev/null 2>&1
}

# The install pipeline writes the stage, the reason and the failing step's
# stderr here. Printing it on the console is the only chance an operator gets:
# the node reboots into the next attempt before anyone can open a log.
LUNA_FAILURE_FILE=/tmp/install_files/logs/last-failure.txt
function print_failure_reason () {
    [ -s "$LUNA_FAILURE_FILE" ] || return 0
    echo "Luna2: Reason:"
    awk -v pfx="Luna2:   " -v w=67 -v cap=15 '
        {
            line = $0
            do {
                if (n >= cap) { cut = 1; exit }
                if (length(line) <= w) { print pfx line; n++; break }
                # Break at the last space in the row, so a word is not split
                # across two lines; a row holding one long token is cut.
                brk = 0
                for (i = w; i > 1; i--) {
                    if (substr(line, i, 1) == " ") { brk = i; break }
                }
                if (brk > 1 && substr(line, 1, brk - 1) ~ /[^ ]/) {
                    print pfx substr(line, 1, brk - 1)
                    line = substr(line, brk + 1)
                } else {
                    print pfx substr(line, 1, w)
                    line = substr(line, w + 1)
                }
                n++
            } while (length(line) > 0)
        }
        END { if (cut) print pfx "... (see log)" }' "$LUNA_FAILURE_FILE"
}

# Visible, interruptible pause between failed install attempts. Replaces a
# silent `sleep 10` so the operator can see that the installer FAILED (rather
# than the screen just churning through another bmcsetup/ipmi reload) and can
# drop to a shell to investigate. Any key -> shell; exit the shell to resume
# the retry; `poweroff` to stop. Falls back to a plain sleep if the console is
# not readable so it never busy-spins.
function retry_countdown () {
    local secs="${1:-30}"
    local rc="${2:-1}"
    local ctty="${luna_ctty:-/dev/tty1}"
    echo ""
    echo "Luna2: ============================================================"
    echo "Luna2: INSTALLER FAILED -- retrying in ${secs}s."
    print_failure_reason
    echo "Luna2: Exit ${rc}: a retryable failure; the reason is above. Retrying in ${secs}s."
    echo "Luna2: Press any key to drop to a shell (exit to resume, poweroff to stop)."
    echo "Luna2: Logs: /tmp/luna_install.log.prev  Kernel: /tmp/luna_install.dmesg.last"
    echo "Luna2: ============================================================"
    if ! { exec 9<>"$ctty"; } 2>/dev/null; then
        sleep "$secs"
        return 0
    fi
    local i key _quiet=0 _n=0
    # Input is read from the recovery console only (fd 9), and the first second
    # is ignored: what is already buffered there (keys pressed during the
    # install, serial line noise) and what arrives in that second is dropped,
    # so a stray byte does not take the node into a shell instead of a retry.
    printf 'Luna2: retrying in %2ds (press any key for a shell)\r' "$secs" >"$ctty"
    while (( _quiet < 10 && _n < 4096 )); do
        if read -t 0.1 -n 1 -s key <&9; then
            (( _n++ ))
        elif (( $? > 128 )); then
            (( _quiet++ ))            # 0.1 s without input
        else
            sleep 1; break            # EOF/error: no input to ignore
        fi
    done
    for (( i=secs-1; i>0; i-- )); do
        printf 'Luna2: retrying in %2ds (press any key for a shell)\r' "$i" >"$ctty"
        if read -t 1 -n 1 -s key <&9; then
            exec 9<&-
            echo "" >"$ctty"
            echo "Luna2: Dropping to shell. 'exit' resumes the retry; 'poweroff' stops." >"$ctty"
            setsid -c /bin/sh -i -l 0<>"$ctty" 1<>"$ctty" 2<>"$ctty"
            return 0
        elif (( $? <= 128 )); then
            sleep 1                   # EOF/error, not a timeout: keep the pause
        fi
    done
    exec 9<&-
    echo "" >"$ctty"
    return 0
}

# Non-retryable failure. Some install failures are deterministic: they will fail
# identically on every attempt, so the retry_countdown loop is pure noise (e.g.
# install_mode=legacy reaching the lpart client -- a daemon routing
# misconfiguration -- which lpart-node-installer reports with exit code
# $LUNA_FATAL_RC). For those, do NOT retry and do NOT fall through to luna_finish
# (there is no installed OS to boot): show why, then park the operator in a
# maintenance shell. 'poweroff' stops the node; exiting the shell re-opens it.
# install_mode=sanitize finished (marker from lpart-node-installer): report and
# park the node. Nothing is left to boot, and returning to the retry loop
# would only start another erase. The marker holds what lpart-storage-prepare
# reported (the disks it erased, or that there was nothing to erase); print
# that, never a fixed claim.
LUNA_SANITIZE_MARKER=/tmp/install_files/runtime/sanitize.done
function luna_sanitize_stop () {
    local rc="${1:-0}" _line
    local ctty="${luna_ctty:-/dev/tty1}"
    echo "Luna2: ============================================================"
    echo "Luna2: SANITIZE FINISHED -- lpart-storage-prepare reported:"
    if [ -s "$LUNA_SANITIZE_MARKER" ]; then
        while IFS= read -r _line || [ -n "$_line" ]; do
            echo "Luna2:   ${_line}"
        done < "$LUNA_SANITIZE_MARKER"
    else
        echo "Luna2:   (no report was written -- check the install log)"
    fi
    echo "Luna2: install_mode=sanitize stops the node here (install script exit"
    echo "Luna2: status ${rc}): set another install_mode, then reboot the node."
    echo "Luna2: 'poweroff' stops the node; exiting the shell re-opens it (no retry)."
    echo "Luna2: ============================================================"
    if [ "$luna_loghost" ]; then
        readable_log < /tmp/luna_install.log | logger -d -n $luna_loghost -P 514
    fi
    luna_post_state install.sanitized
    sync
    while true; do
        if { exec 9<>"$ctty"; } 2>/dev/null; then
            exec 9<&-
            setsid -c /bin/sh -i -l 0<>"$ctty" 1<>"$ctty" 2<>"$ctty"
        else
            sleep 3600
        fi
    done
}

LUNA_FATAL_RC=3   # keep in sync with lpart-node-installer exitFatalConfig
# Exit 2 is the lpart tools' safety_refusal contract (data fence, persistent-
# set protection): also deterministic, also parks. lpart-node-installer exits
# 2 for a safety refusal and 3 for a non-retryable configuration error.
LUNA_SAFETY_RC=2  # keep in sync with internal/cli ExitSafetyRefusal
# The state the install template reported for lpart's park exit (lpart_phase).
function lpart_park_state () {
    if [ "$1" = "$LUNA_SAFETY_RC" ]; then
        [ -f /tmp/install_files/runtime/degraded.done ] && echo install.degraded || echo install.refused
    else
        echo install.fatal
    fi
}

function fatal_park () {
    local rc="${1:-?}" kind
    local ctty="${luna_ctty:-/dev/tty1}"
    echo ""
    echo "Luna2: ============================================================"
    echo "Luna2: INSTALL FAILED (exit ${rc}) -- NOT retrying."
    # Say only what is known. install.sh's exit 2/3 can come from lpart (it then
    # wrote this attempt's reason: the file is cleared before every attempt) or
    # from anything else the install script runs, e.g. an operator script.
    if [ -s "$LUNA_FAILURE_FILE" ]; then
        print_failure_reason
        case "$rc" in
            "$LUNA_SAFETY_RC") kind="a safety refusal" ;;
            *) kind="a non-retryable configuration error" ;;
        esac
        echo "Luna2: lpart reported the reason above: ${kind}, which would recur on"
        echo "Luna2: every attempt."
    else
        echo "Luna2: The install script exited ${rc} (operator script or install step);"
        echo "Luna2: see the log."
    fi
    echo "Luna2: Exit 2 and 3 are non-retryable, so the node is parked instead of"
    echo "Luna2: reinstalling in a loop."
    echo "Luna2: Logs: /tmp/luna_install.log.prev  Kernel: /tmp/luna_install.dmesg.last"
    echo "Luna2: 'poweroff' stops the node; exiting the shell re-opens it (no retry)."
    echo "Luna2: ============================================================"
    if [ -s "$LUNA_FAILURE_FILE" ]; then
        luna_post_state "$(lpart_park_state "$rc")"
    fi
    while true; do
        if { exec 9<>"$ctty"; } 2>/dev/null; then
            exec 9<&-
            setsid -c /bin/sh -i -l 0<>"$ctty" 1<>"$ctty" 2<>"$ctty"
        else
            sleep 3600
        fi
    done
}

# --- ADR 0009: host the decoupled install dashboard --------------------------
# When the kernel cmdline carries `lpart.hosttui` (set together with the
# structural-template daemon patch), the entrypoint runs ONE lpart-tui renderer
# around the whole install.sh, fed by the /run/lpart/events bus that the
# structural lpart-* steps and the bash steps (via lpart-emit) write to. The
# renderer owns the real console; install.sh's raw output goes to the log only
# (the bus carries what the dashboard shows). Default OFF: with the flag absent
# the install runs exactly as before (the in-field lpart-phase shims render
# their own per-phase TUI), so the deployed path is unchanged. Guarded by
# `command -v lpart-tui`, so it is a no-op on an image without the renderer.
LPART_BUS="/run/lpart/events"
LPART_TUI_PID=""

function start_lpart_tui () {
    command -v lpart-tui >/dev/null 2>&1 || return 0
    mkdir -p "$(dirname "$LPART_BUS")" 2>/dev/null
    [ -p "$LPART_BUS" ] || mkfifo "$LPART_BUS" 2>/dev/null || return 0
    # The renderer takes the real console directly (bypassing the log tee), the
    # same way the lpart-phase shim hands the console to lpart-node-installer.
    setsid lpart-tui --bus "$LPART_BUS" --loghost "$luna_loghost" --node-name "$luna_node" \
        <>/dev/console 1>&0 2>&0 &
    LPART_TUI_PID=$!
}

function stop_lpart_tui () {
    [ -n "$LPART_TUI_PID" ] || return 0
    sleep 1                              # let the renderer drain the last events
    kill -TERM "$LPART_TUI_PID" 2>/dev/null
    wait "$LPART_TUI_PID" 2>/dev/null
    LPART_TUI_PID=""
}

# run_install_sh runs the fetched script, hosting the dashboard when requested.
# In hosted mode install.sh's output goes to the log file only (the renderer
# owns the console); otherwise it inherits the console tee as before.
#
# install.sh is rendered from a bash template (it uses `function name {}` and
# other bashisms), so it MUST run under bash. On redhat /bin/sh is bash, but on
# ubuntu/debian /bin/sh is dash -- which fails the template with
# "function: not found" / "syntax error" -- so invoke bash explicitly.
function run_install_sh () {
    if getargbool 0 lpart.hosttui; then
        start_lpart_tui
        bash /lunatmp/install.sh >> /tmp/luna_install.log 2>&1
        local rc=$?
        stop_lpart_tui
        return $rc
    fi
    # install.sh otherwise inherits luna2-start's tee, which is a PIPE: the lpart
    # steps cannot read a window size off it, so they fall back to plain output and
    # no TUI is ever drawn. A pty is both a terminal and capturable, so run under
    # script(1) where it exists. -a because the tee has already written to this log
    # and script truncates without it; -e so the child's exit code still decides the
    # install; -f so a node that dies mid-install still has its log on disk.
    # Binding the console is what gives the pty a real size -- without it script
    # inherits the tee pipe and the TUI stays plain, which looks like this change
    # not working.
    local ctty="${luna_ctty:-/dev/console}"
    if command -v script > /dev/null 2>&1 && { exec 9<>"$ctty"; } 2>/dev/null; then
        exec 9>&-
        script -q -a -e -f -c "bash /lunatmp/install.sh" /tmp/luna_install.log \
               <>"$ctty" 1>&0 2>&0
        return $?
    fi
    bash /lunatmp/install.sh
}

# --- main --------------------------------------------------------------------
# dracut exports the resolved root device as $root (lowercase); initramfs-tools
# exports it as $ROOT. Normalise so the same guard works under both frameworks.
root="${root:-$ROOT}"
if [ "x$root" = "xluna" ]; then
    # An install starts in this boot: from now on the pre-pivot guard boots
    # /sysroot only after luna_finish (luna-pivot-guard.sh).
    : > /run/luna-install-started
    luna_init
    luna_start
    luna_ctty=$(_get_luna_ctty)
    luna_loghost=$(getargs luna.loghost=)
    luna_url=$(getargs luna.url=)
    luna_node=$(getargs luna.node=)
    luna_service=$(getargs luna.service=)
    luna_verifycert=$(getargs luna.verifycert= | tr 'A-Z' 'a-z')
    if [ "$luna_verifycert" == "no" ] || [ "$luna_verifycert" == "false" ]; then
        INSECURE='--insecure'
    fi
    if [ "x$luna_service" = "x1" ]; then
        echo "Luna2: Entering Service mode."
        setsid -c /bin/sh -i -l 0<>$luna_ctty 1<>$luna_ctty 2<>$luna_ctty
    else
        RES="failure"
        luna_mac=$(getargs luna.mac=)
        luna_nic=$(find_nic "${luna_mac}")
        while [ "x$RES" = "xfailure" ]; do
            echo "Luna2: Trying to get install script."
            while [ 0 ]; do
                token=$(fetch_token)
                luna_curl -f -s -m 60 --connect-timeout 10 -H "x-access-tokens: $token" "${luna_url}/boot/install/${luna_node}" -o /lunatmp/install.sh -w "%{http_code}" > /lunatmp/http_code.out
                http_code=$(cat /lunatmp/http_code.out)
                if [ "$http_code" == "200" ]; then
                    break
                else
                    echo "Luna2: Could not get install script [$http_code]. Sleeping 10 sec."
                    sleep 10
                fi
            done
            # Per-attempt state. /tmp/install_files survives the retries, and
            # the lpart phases clear their own files only once they run: a
            # failure in prescript or before the first lpart phase showed the
            # PREVIOUS attempt's lpart reason as this attempt's.
            rm -f "$LUNA_FAILURE_FILE" "$LUNA_SANITIZE_MARKER"
            run_install_sh
            rc=$?
            # sanitize = "erase, then stop". lpart-node-installer writes the
            # marker after a verified erase; stop here whatever the rest of
            # install.sh did (its OS steps cannot succeed without an OS, and a
            # retry would erase the disks again every minute).
            if [ -f "$LUNA_SANITIZE_MARKER" ]; then
                luna_sanitize_stop "$rc"   # never returns
            fi
            [ "$rc" = "0" ] && RES="success"
            echo "Luna2: install.sh exit status: $RES (rc=$rc)"
            if [ "$RES" != "success" ]; then
                # printk is quiet on the console; snapshot the kernel ring
                # buffer so ipmi/driver errors survive for the shell + loghost.
                dmesg > /tmp/luna_install.dmesg.last 2>/dev/null || true
            fi
            if [ "$RES" != "success" ]; then
                # keep the failed attempt's log locally (the banners point the
                # operator to it), with or without a loghost
                if [ "$luna_loghost" ]; then
                    readable_log < /tmp/luna_install.log | logger -d -n "$luna_loghost" -P 514
                fi
                cat /tmp/luna_install.log > /tmp/luna_install.log.prev
                if [ ! -f /tmp/luna_install.log.first ]; then
                    cat /tmp/luna_install.log > /tmp/luna_install.log.first
                fi
                echo -n '' > /tmp/luna_install.log
            fi
            # Only pause+countdown on a failed attempt; on success the loop
            # exits immediately (no spurious "INSTALLER FAILED" banner). A fatal
            # (non-retryable) exit code parks instead of looping the countdown.
            if [ "$RES" != "success" ]; then
                if [ "$rc" = "$LUNA_FATAL_RC" ] || [ "$rc" = "$LUNA_SAFETY_RC" ]; then
                    fatal_park "$rc"   # never returns
                fi
                retry_countdown 30 "$rc"
            fi
        done
    fi
    luna_finish
    echo 'Exit from Luna2 Installer'

    # Post-install boot of the freshly installed disk.
    # dracut (redhat/suse) pivots cleanly to /sysroot once this hook returns, so
    # there is nothing more to do. initramfs-tools (ubuntu/debian) cannot: with
    # root=luna there is no device for mountroot to mount, so it would drop to an
    # emergency shell (and luna_finish has already taken the network down, so the
    # node is then unreachable). For that case force a reboot instead -- the
    # firmware then boots the installed disk via the UEFI entry that
    # lpart-bootloader-finalise registered. Detected by the absence of dracut.
    # Only reboot when a real OS was actually installed to a DISK
    # (auto/sync/full/local). sanitize wipes every disk -- there is nothing to
    # boot -- and memboot runs the OS from a RAM/tmpfs root, so for those modes a
    # reboot would just loop (sanitize) or throw away the RAM root (memboot).
    # Detect a real install by an os-release on a /dev-backed sysroot mount.
    _luna_disk_os=""
    for _sr in /sysroot /root; do
        if [ -e "$_sr/etc/os-release" ]; then
            case "$(awk -v m="$_sr" '$2==m{print $1; exit}' /proc/mounts 2>/dev/null)" in
                /dev/*) _luna_disk_os="$_sr"; break;;
            esac
        fi
    done
    if [ ! -e /lib/dracut-lib.sh ] && [ ! -e /usr/lib/dracut/dracut-lib.sh ] && [ -n "$_luna_disk_os" ]; then
        echo "Luna2: install complete -- unmounting + rebooting into the installed disk"
        # Cleanly tear down the sysroot tree first. The ESP is vfat (NO journal),
        # so the GRUB/shim files just written to it MUST be flushed by an unmount
        # before the reboot or the disk can come up non-bootable. Unmount deepest
        # first (/root/boot/efi, /root/boot, /root), then the rest.
        sync
        for _mp in /root/boot/efi /root/boot /root /sysroot; do
            umount -R "$_mp" 2>/dev/null
        done
        # anything still mounted under the sysroot, lazy-detach so it flushes
        for _mp in $(awk '$2 ~ /^\/(root|sysroot)/ {print $2}' /proc/mounts 2>/dev/null | sort -r); do
            umount -l "$_mp" 2>/dev/null
        done
        sync
        sleep 1
        echo 1 > /proc/sys/kernel/sysrq 2>/dev/null
        echo b > /proc/sysrq-trigger 2>/dev/null
        reboot -f 2>/dev/null
        sleep 5; echo o > /proc/sysrq-trigger 2>/dev/null   # power-off fallback
    fi
fi
