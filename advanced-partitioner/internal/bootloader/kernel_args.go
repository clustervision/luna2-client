package bootloader

import (
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// KernelArgs returns lpart's own part of GRUB_CMDLINE_LINUX (the image's and
// the Luna options follow it), as bootloader_kernel_args in the bash predecessor
// assembles it:
//
//	always:                rd.auto
//	if raid_mode != none:  rd.md=1
//	if storage_type=lvm:   rd.lvm=1
//	if root fs is one of:
//	    xfs, ext4, ext3, btrfs → rootfstype=<fs>
//
// Mirrors lib/bootloader_common.sh::bootloader_kernel_args.
func KernelArgs(sr result.StorageResult) string {
	args := []string{"rd.auto"}
	if sr.RAIDMode != "" && sr.RAIDMode != "none" {
		args = append(args, "rd.md=1")
	}
	if sr.StorageType == "lvm" {
		args = append(args, "rd.lvm=1")
	}
	rootFS := ""
	for _, fs := range sr.Filesystems {
		if fs.Mount == "/" {
			rootFS = fs.FS
			break
		}
	}
	switch rootFS {
	case "xfs", "ext4", "ext3", "btrfs":
		args = append(args, "rootfstype="+rootFS)
	}
	return strings.Join(args, " ")
}

// GrubCmdlineValue formats the GRUB_CMDLINE_LINUX= line that goes into
// /etc/default/grub. The file is sourced by sh, so a quoted Luna option must
// not end the value early.
func GrubCmdlineValue(kernelArgs string) string {
	return `GRUB_CMDLINE_LINUX="` + grubQuoteEscaper.Replace(kernelArgs) + `"`
}

// grubQuoteEscaper escapes what stays special inside sh double quotes.
var grubQuoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
