#!/bin/bash

# Written by ClusterVision Development team
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

# dracut entrypoint for the install initramfs. It is a thin wrapper: set up
# console+log capture, pull in dracut's getargs/getargbool, and source the
# installer body (/usr/share/luna/luna-installer.sh), where all install logic
# lives. The ubuntu initramfs-tools hook has its own (luna-start).

# Don't use POSIX compliancy mode !
set +o posix

exec &> >(tee -a "/tmp/luna_install.log" "/dev/console")

echo "Welcome to Luna Installer"
. /lib/dracut-lib.sh

. /usr/share/luna/luna-installer.sh
