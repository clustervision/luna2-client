BuildArch:     %{_arch} 
Name:          luna2-client
Version:       2.0 
Release:       6.noarch
Group:         Unspecified
Summary:       Kernel module for Luna2-client to deploy nodes

Packager:      ClusterVision

License:       GNU GPLv2 
URL:           https://github.com/clustervision/luna


Provides:      libluna-fakeuname.so()(64bit)  
Provides:      luna2-client = %{version}.%{release}
# Provides:      luna2-client(x86-64) = %{version}.%{release}
Requires:      tpm2-tools
Requires:      aria2c
Requires:      curl  
Requires:      dracut-config-generic  
Requires:      dracut-network  
Requires:      e2fsprogs  
Requires:      gawk  
Requires:      grub2  
Requires:      gzip  
Requires:      ipmitool  
Requires:      kernel  
Requires:      lbzip2
Requires:      nss  
Requires:      nss-softokn  
Requires:      openssh  
Requires:      openssh-clients  
Requires:      openssh-server  
Requires:      parted  
Requires:      rb_libtorrent  
Requires:      rootfiles  
Requires:      rsync  
Requires:      sed  
Requires:      tar  
Requires:      vim-minimal  
Requires:      wget
Requires:      coreutils
Requires:      util-linux
Requires:      bind-utils
Requires:      mdadm
Requires:      procps-ng
Requires:      pcituils
Requires:      dosfstoolp

%clean
echo "No need to remove the source"

%description
Dracut module for Luna deployment tool

%files
%dir %attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/bashrc"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/luna-parse-cmdline.sh"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/luna-start.sh"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/module-setup.sh"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/profile"
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/sshd_config"
%attr(0755, root, root) "/usr/lib64/libluna-fakeuname.so"
%attr(0755, root, root) "/etc/dracut.conf.d/luna2.conf"

%changelog
* Wed Nov 23 2022 Matthew Steggink
- Rewrite of luna2 client

* Tue Mar 16 2021 Bart Lamboo
- Added lbzip2 dependency including dracut configuration extension

* Wed Feb 17 2021 Bart Lamboo
- Redirected both stdin / stderr to console and logfile

* Fri Aug 18 2023 Antoine Schonewille
- Adding real TPM support; using sha256 for now
- adding tpm2-tools / tpm2_pcrread
- improvements on install script fetch handling
