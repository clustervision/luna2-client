# luna2-client

luna2-client is the node side of Luna, the provisioning and cluster management system of
TrinityX. It is installed into every OS image and becomes part of the image's initramfs.
When a node boots over the network, this code runs before the operating system: it
brings up the network, authenticates the node to the Luna daemon, fetches the node's
install script, prepares the disks, installs the OS image and hands over to the installed
system.

## Features

- Network installer for Red Hat Enterprise Linux and compatibles, openSUSE and Ubuntu, on
  x86_64 and aarch64: a dracut module (`95luna`) and an initramfs-tools hook.
- Network set-up by DHCP or a static address, on the interface that matches the node's MAC
  address.
- Node authentication with the daemon, optionally bound to the node's TPM.
- Install script fetched per node from the daemon, so all install logic stays under the
  daemon's control.
- OS image download over HTTP or BitTorrent.
- `lpart`, the advanced partitioner: partitioning, software RAID, LVM, file systems,
  verification of an existing layout, OS image extraction with preserve and exclude
  filters, and boot loader set-up.
- Install modes for full reinstall, image refresh with data preserved, in-place
  verification, diskless (memory) boot and secure erasure of local disks.
- A live install dashboard on the console.
- SSH access into a running install and a shell on the second console, for diagnosis.
- Automatic retry on failure, with a countdown that can be interrupted for a shell;
  failures that would recur on every attempt stop and wait for the operator instead.
- Service mode: boot a node into a shell without installing anything.
- Install log forwarded to a remote syslog host.

## How a node installs

1. The node network-boots the kernel and initramfs of its OS image. The Luna daemon puts
   the install parameters on the kernel command line (`root=luna` and the `luna.*`
   arguments below).
2. The installer starts SSH (dropbear, or OpenSSH when dropbear is absent) and a shell on
   tty2, then configures the network interface that has the node's MAC address.
3. It requests a token from the daemon (`/tpm/<node>`), using the credentials from
   `luna.ini` and, when present, the TPM's SHA-256 PCR 0 value.
4. It fetches the node's install script (`/boot/install/<node>`) and runs it. The script
   runs the pre, part and post phases, which call the `lpart` tools to prepare the disks,
   install the OS image and make it bootable. In the `legacy` install mode the script runs
   the group's or node's own pre, part and post scripts instead.
5. On failure it retries after 30 seconds. On a failure that would recur on every attempt,
   it stops and waits for the operator.
6. On success it forwards the install log, stops SSH, releases the network and continues
   booting the installed system.

### Kernel command line

| Argument | Purpose |
|---|---|
| `luna.url` | Daemon API address |
| `luna.node` | Node name |
| `luna.hostname` | Host name to set during the install |
| `luna.mac` | MAC address of the provisioning interface |
| `luna.bootproto` | `dhcp` or static |
| `luna.ip`, `luna.gw` | Static address and gateway |
| `luna.verifycert` | `no` or `false` skips certificate verification |
| `luna.ctty` | Console for the retry prompt and the shells |
| `luna.loghost` | Syslog host that receives the install log |
| `luna.service` | `1` boots into service mode |
| `lpart.hosttui`, `lpart.notui` | Install dashboard on the console, or plain output |

## lpart tools

| Tool | Purpose |
|---|---|
| `lpart-node-installer` | Runs one install phase (pre, part or post) and drives the other tools |
| `lpart-storage-prepare` | Partitions, RAID, LVM, file systems; mounts the target |
| `lpart-storage-check` | Verifies existing storage against the disk layout |
| `lpart-osimage-install` | Downloads the OS image and extracts it into the target |
| `lpart-bootloader-finalise` | fstab, initramfs, GRUB and EFI boot entries |
| `lpart-tui` | Install dashboard |
| `lpart-emit` | Progress events from the shell steps of the install script to the dashboard |

The disk layout, OS image filter and install mode are set per group or node in Luna. The
install modes are described in [advanced-partitioner/README.md](advanced-partitioner/README.md).

## Configuration

The installer reads the API account from `/trinity/local/luna/node/config/luna.ini`. The file
is copied from the OS image into the initramfs when the initramfs is built, which happens
when the image is packed.

**On a TrinityX cluster, the TrinityX installer writes a fresh `luna.ini` into the OS images
and onto the nodes on every run and replaces whatever the file contained.** Change settings
through the TrinityX configuration, not in the file, and pack the image again for the change
to reach the initramfs. luna2-client does not depend on TrinityX: with any Luna daemon,
`luna.ini` is maintained by hand in the image.

---

The Luna node install module: the initramfs code a node boots into, the dracut
`95luna` module and the initramfs-tools hook, plus the `lpart` advanced
partitioner under `advanced-partitioner/`.

## Layout

One committed file tree per distribution family and architecture. `src/` in each
is the buildroot the package is made from.

```
redhat/{x86,arm}/      luna2-client.spec + src/
opensuse/{x86,arm}/    luna2-client.spec + src/
ubuntu/{x86,arm}/      src/DEBIAN/control + src/
advanced-partitioner/  the lpart Go source
VERSION                the only place the version is stated
```

## Packages are built by the pipeline, not by hand

`.gitlab-ci.yml` loads the shared pipeline from
`ClusterVision/gitlab-cicd-pipelines`. One run compiles the `lpart` binaries for
every architecture, **stages them into each package tree**, and produces all six
packages — two rpm families and Ubuntu, x86_64 and aarch64 — on a single x86
runner. Nothing is cross-built on native hardware: the Go side cross-compiles and
the packagers only pack files.

Publishing is decided by the branch. A branch that is not a publishing branch is
still built, so its packages are available as pipeline artifacts.

**The `create_rpm.sh` / `create_deb.sh` scripts remain for local builds** and are
the quickest way to test a packaging change. Be aware that only the two x86 rpm
scripts compile anything; the other four assume the binaries are already staged.

### Version

`VERSION` at the root holds `<version>-<release>`, e.g. `2.2-0`, and is the only
place it is stated. The specs read it through the `luna_version` / `luna_release`
macros and keep the same value as their fallback, so a local `rpmbuild` produces
the same package the pipeline does. Editing that file is how the version moves.

Six trees each carrying their own version is how they drift: before this, the
release numbers had reached 42, 21, 21 and 20 for the same package.

### The lpart binaries in `*/src/usr/bin/`

They are committed **for now**, and they should not be. They exist in git because
four of the six build scripts never compiled anything and could not otherwise
produce a package at all. The pipeline stages them itself, so once a published
release has been confirmed working they come out and the `.gitignore` gains them.

`libluna-fakeuname.so` is a different case and stays: it is a prebuilt artefact
that is committed deliberately and listed in `%files`.

## Notes for anyone touching the packaging

- **Directory modes are not stored by git.** Only file modes are, so directory
  permissions come from whoever's umask did the checkout, and both packagers copy
  them into the package. `dpkg-deb` refuses a control directory of `0777`;
  `rpmbuild` accepts it silently and ships a world-writable `/usr/bin`. The
  pipeline pins them to `0755` before packing. A local build inherits your umask.
- **`BuildArch` is deliberately absent from the specs.** Pinning it to `%{_arch}`
  makes an aarch64 package impossible to build on an x86 machine — `rpmbuild`
  fails with *"No compatible architectures found for build"*. Without it,
  `--target` decides, and a local build still resolves to the host architecture.
- **`%clean` deliberately does nothing.** `rpmbuild` removes `--buildroot` after a
  successful build, and these buildroots are committed trees. Leave it alone.
