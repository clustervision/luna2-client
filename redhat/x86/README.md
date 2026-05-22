
# Luna Installer

Luna installer to be included in the osimage

## Installation
 The files need to be placed in the osimage dracut directory
to be included in each build:
```usr/lib/dracut/modules.d/95luna```

The goal of the installer is to embed itself during the booting of the node
and retrieve the installation script from the controller node (see module-setup.sh)
The Luna installer will:
- Setup SSHD and include the authorized_keys of the controller to allow password-less access
- Setup basic networking
