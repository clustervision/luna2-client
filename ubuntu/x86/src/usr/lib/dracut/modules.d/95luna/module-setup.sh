#!/bin/bash

check() {
    return 0
}

depends() {
    # lvm + mdraid + dm: lpart's storage stack NEEDS their tools, kernel
    # modules and -- critically -- their udev RULES in the install initramfs.
    # Distro dracut defaults are not a contract: ubuntu26/resolute packed
    # ZERO dm/lvm udev rules (no 95-dm-notify.rules -> every udev-synced LVM
    # command waited forever on its cookie) and zero raid personalities
    # (2026-07-17). Declaring the modules is a no-op where they are already
    # included (el9, noble, suse).
    echo network lvm mdraid dm
    # nss-softokn only exists in the redhat/suse dracut; skip where absent (ubuntu)
    { [ -d "$dracutbasedir/modules.d/05nss-softokn" ] || [ -d /usr/lib/dracut/modules.d/05nss-softokn ]; } && echo nss-softokn
    return 0
}

installkernel() {
    # GUARANTEE the md raid personalities lpart creates with (raid none/1/0/10
    # for OS sets, raid5/6 for data sets). Distro dracut defaults are NOT a
    # contract: ubuntu26/resolute's dracut packed ZERO raid .ko modules, so
    # every mdadm --create died with 'RUN_ARRAY failed: Invalid argument'
    # (raid personality unloadable) while ubuntu24 happened to include them
    # (2026-07-17). instmods skips modules a kernel does not ship; hostonly=''
    # forces inclusion regardless of the pack's hostonly mode. One list for
    # every distro.
    hostonly='' instmods raid0 raid1 raid10 raid456
}

install() {
    # Tools present on every target distro.
    dracut_install ssh sshd scp tar curl awk sed gzip basename dd partx \
                   parted mkfs.ext4 mkfs.xfs ldconfig \
                   blkdiscard fstrim base64 tpm2_pcrread chroot \
                   dmidecode aria2c ping logger dropbear \
                   wc mkswap swapon swaplabel mkfs.vfat mkfs.fat dosfslabel \
                   fatlabel mdadm fdisk lsblk tee lbzip2 \
                   sgdisk partprobe wipefs blkid pvcreate vgcreate lvcreate \
                   lvchange vgremove pvremove lvremove vgchange pvs vgs lvs \
                   dmsetup jq findmnt dirname cut readlink sort xargs mount umount \
                   sleep udevadm env mktemp date hostname \
                   mv cp ln

    # Optional / distro-specific tools -- install only if present (-o), so the
    # same module builds on redhat (dhclient/ipmitool/dig) and ubuntu (no dhclient).
    inst_multiple -o dhclient dhclient-script-luna ipmitool nslookup dig wget \
                     mkfs.ext2 mkfs.ext3 mkdosfs top lspci lsscsi efibootmgr

    # TPM TCTI + sqlite are needed for the token; the NSS/softokn set is a
    # redhat NSS-curl concern -- pull whatever the platform actually ships.
    inst_libdir_file -o libtss2-tcti-device.so.0 libsqlite3.so \
                        libnssdbm3.so libnsspem.so libsoftokn3.chk libsoftokn3.so

    inst "$moddir/sshd_config" "/etc/ssh/sshd_config"
    # sshd privilege-separation dir (compiled-in default on el9; sshd refuses to start without it)
    inst_dir /usr/share/empty.sshd
    # OpenSSH >=9.8 splits per-connection handling into sshd-session; sshd
    # refuses to start without it. Optional (-o): older images lack it. Cover
    # every family's install dir with one candidate list, no distro branch:
    # el9 /usr/libexec/openssh, debian /usr/lib/openssh (ubuntu26's WARN
    # "sshd-session does not exist" -- the installer had NO ssh, 2026-07-17),
    # suse /usr/lib/ssh.
    # (openssh >=10 adds a THIRD helper, sshd-auth -- ubuntu26 needs it too)
    inst_multiple -o /usr/libexec/openssh/sshd-session /usr/libexec/openssh/sshd-auth /usr/libexec/openssh/sftp-server \
                     /usr/lib/openssh/sshd-session /usr/lib/openssh/sshd-auth /usr/lib/openssh/sftp-server \
                     /usr/lib/ssh/sshd-session /usr/lib/ssh/sshd-auth /usr/lib/ssh/sftp-server
    inst "$moddir/bashrc" "/root/.bashrc"
    inst "$moddir/profile" "/root/.profile"
    inst_simple /etc/ssh/ssh_host_rsa_key
    inst_simple /etc/ssh/ssh_host_ecdsa_key
    inst_simple /etc/ssh/ssh_host_ed25519_key

    # CA bundle: redhat path vs debian/ubuntu path -- ship whichever exists.
    for _ca in /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/certs/ca-certificates.crt; do
        [ -e "$_ca" ] && inst_simple "$_ca" && break
    done

    mkdir -m 0700 -p "$initdir/root/.ssh"
    inst_simple /root/.ssh/authorized_keys
    inst_simple /trinity/local/luna/node/config/luna.ini
    inst_simple /etc/resolv.conf

    mkdir -m 0755 -p "$initdir/luna"

    # The luna installer logic (luna2-start.sh + luna-installer.sh) is bash, not
    # POSIX sh. On redhat dracut'''s /bin/sh already IS bash; on ubuntu it is dash,
    # which rejects bashisms (set -o posix, arrays, ${x:1:-1}). Force the initramfs
    # shell to bash so the identical scripts run on both distros.
    dracut_install bash
    ln -sf bash "$initdir/usr/bin/sh"
    [ -L "$initdir/bin" ] || ln -sf /usr/bin/bash "$initdir/bin/sh"

    # advanced-partitioner localboot installer (Go lpart-* binaries).
    dracut_install /usr/bin/lpart-storage-prepare /usr/bin/lpart-storage-check \
                   /usr/bin/lpart-osimage-install /usr/bin/lpart-bootloader-finalise \
                   /usr/bin/lpart-node-installer
    # lpart-emit / lpart-tui only feed the default-off lpart.hosttui dashboard.
    inst_multiple -o /usr/bin/lpart-emit /usr/bin/lpart-tui

    # Shared pre/part/post hook shim: exec /usr/bin/lpart-phase pre|part|post.
    inst_simple "$moddir/lpart-phase" /usr/bin/lpart-phase

    # Shared installer body, sourced by luna2-start.sh (byte-identical across distros).
    inst_simple "$moddir/luna-installer.sh" /usr/share/luna/luna-installer.sh

    inst_hook cmdline 99 "$moddir/luna-parse-cmdline.sh"
    inst_hook initqueue/finished 99 "$moddir/luna2-start.sh"
}
