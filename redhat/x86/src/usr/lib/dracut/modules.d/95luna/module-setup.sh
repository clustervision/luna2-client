#!/bin/bash

check() {
    return 0
}

depends() {
    echo network
    echo nss-softokn
    return 0
}

install() {
    dracut_install ssh sshd scp tar wget curl awk sed gzip basename dd partx \
                   parted mkfs.ext2 mkfs.ext3 mkfs.ext4 mkfs.xfs ipmitool ldconfig \
                   blkdiscard fstrim nslookup dig base64 tpm2_pcrread chroot dhclient \
                   dhclient-script-luna dmidecode aria2c ping logger dropbear \
                   wc mkswap swapon swaplabel mkfs.vfat mkfs.fat mkdosfs dosfslabel \
                   fatlabel mdadm top fdisk lspci lsblk lsscsi tee lbzip2 \
                   sgdisk partprobe wipefs blkid pvcreate vgcreate lvcreate \
                   lvchange vgremove pvremove lvremove vgchange pvs vgs lvs \
                   dmsetup jq findmnt dirname cut readlink sort xargs mount umount \
                   sleep udevadm env mktemp date hostname
    inst_libdir_file libnssdbm3.so libnsspem.so libsoftokn3.chk \
                     libsoftokn3.so libsqlite3.so libtss2-tcti-device.so.0

    inst "$moddir/sshd_config" "/etc/ssh/sshd_config"
    inst "$moddir/bashrc" "/root/.bashrc"
    inst "$moddir/profile" "/root/.profile"
    inst_simple /etc/ssh/ssh_host_rsa_key
    inst_simple /etc/ssh/ssh_host_ecdsa_key
    inst_simple /etc/ssh/ssh_host_ed25519_key
    inst_simple /etc/pki/tls/certs/ca-bundle.crt
    mkdir -m 0700 -p "$initdir/root/.ssh"
    inst_simple /root/.ssh/authorized_keys
    inst_simple /trinity/local/luna/node/config/luna.ini
    inst_simple /etc/resolv.conf

    mkdir -m 0755 -p "$initdir/luna"

    inst_hook cmdline 99 "$moddir/luna-parse-cmdline.sh"
    inst_hook initqueue/finished 99 "$moddir/luna2-start.sh"

}
