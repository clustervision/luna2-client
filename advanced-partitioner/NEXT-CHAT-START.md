# ▶ START HERE — next lpart chat (resume point, 2026-07-12)

Load this first. Full context: `advanced-partitioner/HANDOVER-2026-07-12-v3.md` +
`RESULTS-multidistro-2026-07-12.md` + `JOURNAL.md` (v3 entry).

## Plan for the next chat (user-directed)
1. **Start entirely from scratch**: build a fresh golden3 TrinityX controller VM + **4 UEFI trx
   compute VMs** with the right params (recipe below).
2. **Fix the OPEN POINTS FIRST**, before any further regression running.
3. Only after the open points are closed: run the wired-in destructive regression suite at scale
   per distro.
4. Consider the NEW FTA / regression test ideas below; add the good ones.

## Recreate the cluster from scratch (the recipe)
The installer already carries the tooling — one command stands it up:
```
bash /root/trinityx-lpart-install/kvm-lab/lab-up.sh <name>        # e.g. lpx2
#   -> democluster clone of trx16-demo-golden3 as <name>-ctrl + isolated <name>-prov net,
#      then 4 UEFI q35/OVMF nodes <name>-node001..004 (disks 3/3/4/5, MACs 52:54:00:de:00:01-04,
#      per-VM OVMF NVRAM + disk serials, ipxe-luna-uefi.iso). Nodes DEFINED (not started).
```
Then wire lpart + (optionally) build images + regression, all via the installer:
```
scp -r /root/trinityx-lpart-install /root/trinityx-lpart-install-artifacts root@<ctrl-ip>:/root/
# install.conf: GROUP=compute-group  NODES="node001..004"  IMAGES="compute-image ..."
#               UTILS_SRC=/root/trinityx-lpart-install-artifacts/luna2-utils
ssh root@<ctrl-ip> 'bash /root/trinityx-lpart-install/install.sh --offline /root/trinityx-lpart-install-artifacts'
# then start nodes: for n in 1..4: virsh start <name>-node00$n
```
Controller-side gotchas (all handled by the installer, noted for debugging): grow the ctrl disk
(`virsh blockresize <ctrl> vda 120G` + growpart/xfs_growfs) before building images; the ansible
venv MUST use `/trinity/local/python/bin/python3` (py3.10, not the el9 system py3.9); install
kernel needs `console=ttyS0,115200n8` in `luna osimage change -qo` or the installer is silent on
serial; lpart-internal text logs to `/var/log/cluster-messages/<node>.messages` on the controller.

## OPEN POINTS — fix these FIRST (priority order)

1. **Ubuntu: lpart never reaches the install initramfs.** luna packs ubuntu images with
   `mkinitramfs` (initramfs-tools; daemon `plugins/osimage/operations/image/ubuntu.py`), so the
   committed **dracut 95luna module is ignored** → `/usr/bin/lpart-phase: No such file` in
   prescript. FIX: write an **initramfs-tools hook** (`ubuntu/x86/src/usr/share/initramfs-tools/
   hooks/lpart` + a `local-premount`/`init-bottom` installer script) that `copy_exec`s the lpart
   binaries + lpart-phase + luna-installer.sh + the storage toolset (sgdisk/mdadm/lvm/mkfs.*/jq/
   efibootmgr...) and runs the installer — a port of the dracut `luna2-start.sh`/`luna-installer.sh`
   flow. Model on `ubuntu/arm/.../hooks/luna` + `scripts/local-premount/luna-start`. redhat/opensuse
   use dracut (`ramdisk_modules=['luna']`) and are fine. **This is the biggest item.**

2. **SUSE: `pvcreate /dev/vda3` exit 5** (lpart runs deep first: firmware gate + partition + mdadm
   OK). A SLE-15.5 variant of BE-A6c (lvm devices-file). Check whether `EnsureNoLVMAutoActivate`'s
   `use_devicesfile=0` lands on SUSE's lvm config path (`/etc/lvm/lvmlocal.conf` vs SLE default),
   and whether `/etc/lvm/devices/system.devices` is keyed differently on SLE. `internal/cleanup/
   lvm.go`. Also confirm SUSE images ship jq + gptfdisk (sgdisk) or add them to the image build
   package list (`vars/x64/image-opensuse.yaml`) — this session installed them ad-hoc via lchroot.

3. **Rocky 10: build it container-free.** The stock `redhat/base.yml` seeds the rootfs with the
   CONTROLLER's release RPM (el9), so `alternative_distribution=Rocky-10` gave el9. FIX (preferred,
   no podman): parameterize `base.yml` to install `rocky-release-10` + `releasever=10` against the
   el10 BaseOS + trinityx/luna2 el10 repos (all reachable). Add a `vars/rocky10` + adapt the
   overlay wrapper. (A container source is an alternative, not required.)

4. **Ubuntu image build — finish the EXTEND phase.** The lchroot-patched ansible builds a real
   noble base rootfs (979M, validated) but the in-image extend (kernel + luna2-client + trinity
   customization) failed at a vars-file-load task under the lchroot connection. Debug that task
   (`trinity/image-create` "Load a variable file based on the OS type"): likely a vars-lookup that
   the lchroot connection or the venv-ansible resolves differently. Once fixed, ubuntu24/ubuntu26/
   suse-latest builds complete.

5. **Parity/raid10 degraded boot (BE-B10, from v2).** raid5/6/raid10 degraded boot fails at
   `grub>`, non-deterministically, in the MAIN `/boot/grub2/grub.cfg` (the ESP stub is proven
   fine). Deeper GRUB work. Mitigation NB-10 (verify-time WARN, raid1-only) already shipped.

## NEW FTA / regression test ideas to add (for the next chat)
- **§4a-style pre-boot gate: "packed install initramfs contains the lpart toolset."** Per distro,
  after `luna osimage pack`, assert the packed initramfs has `lpart-phase` + `lpart-node-installer`
  + the storage tools (sgdisk/mdadm/lvm/jq/mkfs.*/efibootmgr). **This single gate would have caught
  BOTH the Ubuntu mkinitramfs-ignores-dracut bug AND the openSUSE missing-jq/sgdisk bug before any
  node boot.** High value; wire into module 60 verify + regression.md §4a.
- **BE-U1 regression**: a serial-only (0x0) console install must fall back to plain mode and
  complete (not hang on "terminal too small"). Unit-pinned; add a lab row.
- **Image-build sanity: os-release matches the target.** Assert the built image's
  `/etc/os-release VERSION_ID`/codename == the requested version (would have caught rocky10
  building el9). Add to build-image.sh + a build-verify test.
- **Per-distro min-bar matrix** (mem / single-disk / raid1) as first-class manifest rows with a
  `distro` column — run the same three against rocky/ubuntu/suse nodes.
- **SUSE lvm devices-file** targeted test (pvcreate on a serialed disk under SLE).
- **lchroot-build integration test**: assert the build ran through lchroot (connection plugin
  present in the ansible log / the patched call sites), not raw chroot.
- Fill the 10 manifest `TODO` rows (3.28/3.35/3.37/3.41/3.44/3.45/3.49/3.56/3.57/3.61) by adding
  driver stages.

## State at retirement
- Both repos committed, clean trees, NOT pushed (GitLab unreachable — pushing is a human step).
  luna2-client branch `advanced-partitioner-stable` HEAD ~ `60e5790`+; installer repo HEAD ~ `345e5b8`+.
- lpx cluster **TORN DOWN** this session (see teardown note in JOURNAL). trx + all customer demo
  clusters were never touched.
- `make check` GREEN.
