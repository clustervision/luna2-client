#!/bin/bash

# This file is part of Luna, cluster provisioning tool

# Luna is free software: you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.

# Luna is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU General Public License for more details.

# You should have received a copy of the GNU General Public License
# along with Luna.  If not, see <http://www.gnu.org/licenses/>.

# dracut pre-pivot hook: switch root into /sysroot only when the installer
# finished. The installer runs in the initqueue (luna2-start.sh); when it stops
# early -- killed (out of memory), or its service stopped by systemd -- dracut
# carries on and would boot whatever /sysroot holds at that moment: a half
# extracted image, or disks the installer was still writing. The installer
# leaves /run/luna-install-started when an install begins (root=luna) and
# luna_finish /run/luna-install-finished as its last step. Started without
# finished: the node parks (it would stop the same way on the next attempt)
# and reports install.fatal; this hook never returns then, so dracut never
# pivots. Any other boot -- an installed system whose own initramfs carries
# this module, e.g. after a kernel update that ran dracut without --omit luna --
# is left alone.

if [ -e /run/luna-install-started ] && [ ! -e /run/luna-install-finished ]; then
    luna_guard_msg() {
        echo "Luna2: ============================================================"
        echo "Luna2: INSTALL INCOMPLETE -- the installer stopped before it finished"
        echo "Luna2: (its process was killed, e.g. out of memory, or its service was"
        echo "Luna2: stopped; the kernel messages are forwarded to the loghost)."
        echo "Luna2: /sysroot is NOT booted and the node is parked: it would stop the"
        echo "Luna2: same way again. Fix the cause in Luna (more memory, a disk layout"
        echo "Luna2: instead of memboot), then reboot the node."
        echo "Luna2: 'poweroff' stops the node; exiting the shell re-opens it (no retry)."
        echo "Luna2: ============================================================"
    }
    luna_guard_msg > /dev/console 2>&1
    luna_guard_loghost=$(getargs luna.loghost=)
    if [ -n "$luna_guard_loghost" ]; then
        {
            tail -n 300 /tmp/luna_install.log 2>/dev/null
            dmesg 2>/dev/null | tail -n 60
            luna_guard_msg
        } | logger -d -n "$luna_guard_loghost" -P 514 2>/dev/null
    fi
    # Best effort: report the park with the token the installer fetched for this
    # boot, through a copy of the installer's luna_curl (the hook must not depend
    # on the installer that just died). No NIC binding: luna_nic stays empty.
    function luna_guard_curl () {
        local -a _opts=()
        [ -n "$INSECURE" ] && _opts+=("$INSECURE")
        [ -n "$luna_nic" ] && _opts+=(--interface "$luna_nic")
        curl "${_opts[@]}" "$@"
    }
    INSECURE=
    case "$(getargs luna.verifycert= | tr 'A-Z' 'a-z')" in no|false) INSECURE='--insecure' ;; esac
    luna_guard_url=$(getargs luna.url=)
    luna_guard_node=$(getargs luna.node=)
    luna_guard_token=$(cat /lunatmp/token.dat 2>/dev/null)
    if [ -n "$luna_guard_url" ] && [ -n "$luna_guard_node" ] && [ -n "$luna_guard_token" ]; then
        luna_guard_curl -s -m 10 -X POST -H "x-access-tokens: $luna_guard_token" -H "Content-Type: application/json" \
            -d '{"monitor": {"status": {"'"$luna_guard_node"'": {"state": "install.fatal"}}}}' \
            "${luna_guard_url}/monitor/node/${luna_guard_node}" > /dev/null 2>&1
    fi
    luna_guard_ctty=$(getargs luna.ctty=)
    [ -n "$luna_guard_ctty" ] || luna_guard_ctty=/dev/console
    while true; do
        if { exec 9<>"$luna_guard_ctty"; } 2>/dev/null; then
            exec 9<&-
            setsid -c /bin/sh -i -l 0<>"$luna_guard_ctty" 1<>"$luna_guard_ctty" 2<>"$luna_guard_ctty"
        else
            sleep 3600
        fi
    done
fi
