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

# Don't use POSIX compliancy mode !
set +o posix

exec &> >(tee -a "/tmp/luna_install.log" "/dev/console")

echo "Welcome to Luna Installer"
. /lib/dracut-lib.sh

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
    local luna_bootproto
    local luna_mac
    echo "$(getargs luna.hostname=)" > /proc/sys/kernel/hostname
    echo "Luna2: Starting ssh"
    echo "sshd:x:74:74:Privilege-separated SSH:/var/empty/sshd:/sbin/nologin" >> /etc/passwd
    echo sshd:x:74: >> /etc/group
    mkdir -p /var/empty/sshd
    /usr/sbin/sshd > /dev/null 2>&1
    echo "Luna2: Start shell on tty2"
    luna_ctty2=/dev/tty2
    setsid -c /bin/sh -i -l 0<>$luna_ctty2 1<>$luna_ctty2 2<>$luna_ctty2 &
    udevadm settle
    # settle is not really reliable
    sleep 5
    echo "Luna2: Set-up network"
    luna_bootproto=$(getargs luna.bootproto=)
    if [ "x$luna_bootproto" = "xdhcp" ]; then
        echo "Luna2: Configuring dhcp for all interfaces"
        /usr/sbin/dhclient -lf /lunatmp/dhclient.leases
    else
        luna_ip=$(getargs luna.ip=)
        luna_mac=$(getargs luna.mac=)
        echo "Luna2: ${luna_ip} for interface with mac ${luna_mac} was specified"
        echo "Luna2: Trying to find interface for ${luna_mac}"
        luna_nic=$(find_nic ${luna_mac})
        if [ "x${luna_nic}" = "x" ]; then
            echo "Luna2: unable to find NIC for mac ${luna_mac}. Starting dhcp"
            /usr/sbin/dhclient -lf /lunatmp/dhclient.leases
        else
            echo "${luna_nic}" > /lunatmp/luna_nic
            export LUNA_BOOTIF="${luna_nic}"
            ip a add ${luna_ip} dev ${luna_nic}
            sleep 1
            ip l set dev ${luna_nic} up
        fi
    fi
}

function luna_finish () {
    # shutdown sshd
    /usr/bin/ps h --ppid `cat /var/run/sshd.pid` -o 'pid' | while read pid; do kill $pid; done
    kill `cat /var/run/sshd.pid`
    # shutdown dhclient
    /usr/sbin/dhclient -lf /lunatmp/dhclient.leases -x
    # bring interfaces down
    if [ -f /lunatmp/luna_nic ]; then
        luna_nic=$(cat /lunatmp/luna_nic)
        ip addr flush ${luna_nic}
        ip link set dev ${luna_nic} down
    else
        cat /lunatmp/dhclient.leases  | \
            sed -n '/interface /s/\W*interface "\(.*\)";/\1/p' | \
            while read iface; do
                ip addr flush $iface
                ip link set dev $iface down
            done
    fi
    mv /tmp/luna_install.log /sysroot/var/log/
    # kill shell on tty2
    ps h t tty2 o pid | while read pid; do kill -9 $pid; done

}

function _get_luna_ctty () {
    local luna_ctty
    luna_ctty=$(getargs luna.ctty=)
    # TODO [ "x${luna_ctty}" = "x" ] && luna_ctty=$(getargs console=)
    [ "x${luna_ctty}" = "x" ] && luna_ctty="/dev/tty1"
    echo -n $luna_ctty
}

function get_credentials {
    LUNA_INI='/trinity/local/luna/node/config/luna.ini'
    if [ -f $LUNA_INI ]; then
        while IFS='=' read -ra line; do
            if [ ! "$(echo $line | grep -E '^(#|;)')" ] && [ "$line" ]; then
                if [ "$(echo $line | grep '^\[')" ]; then
                    section=$(echo $line | grep -oE "[A-Z]+")
                else
                    key=$(echo ${line[0]})
                    value=$(echo ${line[1]})
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

function fetch_token {
    CREDENTIALS=$(get_credentials)
    USERNAME=$(echo $CREDENTIALS | awk -F':' '{ print $1 }')
    PASSWORD=$(echo $CREDENTIALS | awk -F':' '{ print $2 }')
    export TPM2TOOLS_TCTI="device:/dev/tpm0"
    ldconfig
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
    curl $INSECURE -X POST -H "Content-Type: application/json" -d "$json" -s "${luna_url}/tpm/${luna_node}" > /lunatmp/token.json
    valid=$(grep token /lunatmp/token.json)
    if [ "$valid" ]; then
        token=$(cat /lunatmp/token.json | awk '{ print $2 }' | grep -oE '"(\S+)"')
        token=$(echo "${token:1:-1}")
        echo $token > /lunatmp/token.dat
        echo $token
    else
        >&2 cat /lunatmp/token.json
        >&2 echo
        echo "None"
    fi
}

if [ "x$root" = "xluna" ]; then
    luna_init
    luna_start
    luna_ctty=$(_get_luna_ctty)
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
        while [ "x$RES" = "xfailure" ]; do
            echo "Luna2: Trying to get install script."
            while [ 0 ]; do
                token=$(fetch_token)
                curl $INSECURE -f -s -m 60 --connect-timeout 10 -H "x-access-tokens: $token" "${luna_url}/boot/install/${luna_node}" -o /lunatmp/install.sh -w "%{http_code}" > /lunatmp/http_code.out
                http_code=$(cat /lunatmp/http_code.out)
                if [ "$http_code" == "200" ]; then
                    break
                else
                    echo "Luna2: Could not get install script [$http_code]. Sleeping 10 sec."
                    sleep 10
                fi
            done
            /bin/sh /lunatmp/install.sh && RES="success"
            echo "Luna2: install.sh exit status: $RES"
            sleep 10
        done
    fi
    luna_finish
    echo 'Exit from Luna2 Installer'
fi
