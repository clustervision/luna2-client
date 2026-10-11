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
# extracted image, or disks the installer was still writing. luna_finish
# leaves /run/luna-install-finished as its last step. Without it the node
# reports why and reboots for a new attempt (the next boot starts the
# installer again; the controller log shows every such attempt).

if [ ! -e /run/luna-install-finished ]; then
    luna_guard_msg() {
        echo "Luna2: ============================================================"
        echo "Luna2: INSTALL INCOMPLETE -- the installer stopped before it finished"
        echo "Luna2: (its process was killed or its service stopped; the kernel"
        echo "Luna2: messages are forwarded to the loghost). /sysroot is NOT booted."
        echo "Luna2: Rebooting in 30s for a new attempt."
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
    sleep 30
    echo 1 > /proc/sys/kernel/sysrq 2>/dev/null
    reboot -f 2>/dev/null
    echo b > /proc/sysrq-trigger 2>/dev/null
    sleep 10
fi
