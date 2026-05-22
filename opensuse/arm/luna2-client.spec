BuildArch:     %{_arch} 
Name:          luna2-client
Version:       2.0 
Release:       20
Group:         Unspecified
Summary:       Kernel module for Luna2-client to deploy nodes for Suse/SLE based distributions

Packager:      ClusterVision

License:       GNU GPLv2 
URL:           https://github.com/clustervision/luna


Provides:      libluna-fakeuname.so()(64bit)  
Provides:      luna2-client = %{version}.%{release}
Requires:      dracut
Requires:      dracut-tools
Requires:      nss-systemd
Requires:      nss-myhostname
Requires:      nss_wrapper
Requires:      tpm-tools
Requires:      tpm2-0-tss
Requires:      tpm2.0-tools
Requires:      libtpms0
Requires:      aria2
Requires:      curl  
Requires:      e2fsprogs  
Requires:      gawk  
Requires:      grub2  
#Requires:      gzip  # provided through busybox-gzip
Requires:      ipmitool  
Requires:      kernel  
Requires:      lbzip2
Requires:      openssh  
Requires:      openssh-clients                                                                                                                                                               
Requires:      openssh-server                                                                                                                                                                
Requires:      dropbear                                                                                                                                                                      
Requires:      parted                                                                                                                                                                        
Requires:      rsync                                                                                                                                                                         
Requires:      sed                                                                                                                                                                           
Requires:      tar                                                                                                                                                                           
Requires:      vim-small                                                                                                                                                                     
Requires:      wget                                                                                                                                                                          
Requires:      coreutils                                                                                                                                                                     
Requires:      util-linux                                                                                                                                                                    
Requires:      bind-utils                                                                                                                                                                    
Requires:      mdadm                                                                                                                                                                         
Requires:      pciutils                                                                                                                                                                      
Requires:      dosfstools                                                                                                                                                                    
Requires:      dhcp-client                                                                                                                                                                   
Requires:      dmidecode                                                                                                                                                                     
Requires:      util-linux                                                                                                                                                                    
                                                                                                                                                                                             
%clean                                                                                                                                                                                       
echo "No need to remove the source"                                                                                                                                                          
                                                                                                                                                                                             
%description                                                                                                                                                                                 
Dracut module for Luna deployment tool                                                                                                                                                       
                                                                                                                                                                                             
%files                                                                                                                                                                                       
%dir %attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna"                                                                                                                              
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/bashrc"                                                                                                                            
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/luna-parse-cmdline.sh"                                                                                                             
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/luna2-start.sh"                                                                                                                    
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/module-setup.sh"                                                                                                                   
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/profile"                                                                                                                           
%attr(0755, root, root) "/usr/lib/dracut/modules.d/95luna/sshd_config"                                                                                                                       
%attr(0755, root, root) "/usr/lib64/libluna-fakeuname.so"                                                                                                                                    
%attr(0755, root, root) "/etc/dracut.conf.d/luna2.conf"                                                                                                                                      
%attr(0755, root, root) "/usr/sbin/dhclient-script-luna"

%changelog
* Thu Apr 17 2025 Antoine Schonewille
- allowing dhclient in non standard locations

* Mon Feb 10 2025 Antoine Schonewille
- added log.prev to allow for some install log rotation

* Tue Sep 17 2024 Antoine Schonewille
- added dropbear lightweight ssh server support

* Fri Aug 30 2024 Antoine Schonewille
- added logger and logging support

* Fri Jul 05 2024 Antoine Schonewille
- added dhclient-script-luna

* Fri Feb 23 2024 Antoine Schonewille
- added ipv6 compliancy

* Thu Feb 01 2024 Antoine Schonewille
- added aria2c and dmidecode into ramdisk

* Fri Aug 18 2023 Antoine Schonewille
- Adding real TPM support; using sha256 for now
- adding tpm2-tools / tpm2_pcrread
- improvements on install script fetch handling

* Wed Nov 23 2022 Matthew Steggink
- Rewrite of luna2 client

* Tue Mar 16 2021 Bart Lamboo
- Added lbzip2 dependency including dracut configuration extension

* Wed Feb 17 2021 Bart Lamboo
- Redirected both stdin / stderr to console and logfile
