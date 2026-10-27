package bootloader

// dracutStorageModules are the dracut modules the installed system's initramfs
// must carry for an lpart-created root to assemble at boot: the same list 95luna
// declares for the install initramfs, since lpart lays the root down on LVM, md
// or both.
const dracutStorageModules = "lvm mdraid dm"

// dracutArgs is the one dracut invocation that rebuilds the installed system's
// initramfs, on every family.
//
//   - --omit luna: 95luna belongs to the netboot install initramfs; left in, a
//     disk boot re-enters the installer.
//   - --add: declare the storage modules instead of leaving them to dracut's
//     host detection, which inside the install chroot can drop them; a
//     force-added module's check() runs with hostonly unset.
//   - --no-hostonly / --no-hostonly-cmdline: a generic initramfs, not one
//     specialised to the host state mid-install; nodes are re-imaged and
//     re-shaped on demand.
func dracutArgs(kver string) []string {
	return []string{
		"dracut", "-f",
		"--omit", "luna",
		"--add", dracutStorageModules,
		"--no-hostonly", "--no-hostonly-cmdline",
		"--kver", kver,
	}
}
