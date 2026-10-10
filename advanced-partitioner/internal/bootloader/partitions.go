package bootloader

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// Partition type identifiers as lsblk PARTTYPE / blkid PART_ENTRY_TYPE print
// them (lowercase GUID for GPT, 0x.. for an MBR label).
const (
	gptTypeESP      = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" // EF00
	gptTypeBIOSBoot = "21686148-6449-6e6f-744e-656564454649" // EF02
	mbrTypeESP      = "0xef"
)

// blockDev is one line of `lsblk -nPp -o NAME,TYPE,PARTTYPE,PKNAME,MOUNTPOINT`.
type blockDev struct {
	Name, Type, PartType, PKName, MountPoint string
}

var lsblkPairRE = regexp.MustCompile(`([A-Z:_-]+)="((?:[^"\\]|\\.)*)"`)

// listBlockDevs lists disk and everything below it (partitions, and the md /
// LVM devices built on them). lsblk is read-only, so it also runs in dry-run.
// util-linux 2.32 (el8) has every column used here.
func listBlockDevs(ctx context.Context, runner executor.Runner, disk string) ([]blockDev, error) {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path:          "lsblk",
		Args:          []string{"-nPp", "-o", "NAME,TYPE,PARTTYPE,PKNAME,MOUNTPOINT", disk},
		CaptureStdout: true, CaptureStderr: true, Quiet: true,
	})
	if err != nil {
		return nil, fmt.Errorf("lsblk %s: %w", disk, err)
	}
	var out []blockDev
	for _, ln := range strings.Split(string(res.Stdout), "\n") {
		kv := map[string]string{}
		for _, m := range lsblkPairRE.FindAllStringSubmatch(ln, -1) {
			kv[m[1]] = unescapeLsblk(m[2])
		}
		if kv["NAME"] == "" {
			continue
		}
		out = append(out, blockDev{
			Name: kv["NAME"], Type: kv["TYPE"], PartType: strings.ToLower(kv["PARTTYPE"]),
			PKName: kv["PKNAME"], MountPoint: kv["MOUNTPOINT"],
		})
	}
	// Without a udev database lsblk can leave PARTTYPE empty; probe those
	// partitions directly.
	for i := range out {
		if out[i].Type == "part" && out[i].PartType == "" {
			if r, err := runner.Run(ctx, executor.CommandSpec{
				Path:          "blkid",
				Args:          []string{"-p", "-s", "PART_ENTRY_TYPE", "-o", "value", out[i].Name},
				CaptureStdout: true, CaptureStderr: true, Quiet: true,
			}); err == nil {
				out[i].PartType = strings.ToLower(strings.TrimSpace(string(r.Stdout)))
			}
		}
	}
	return out, nil
}

// unescapeLsblk undoes lsblk's \xNN escaping in -P output.
func unescapeLsblk(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// partNumber is the partition number at the end of a partition device name
// (/dev/sda1 -> 1, /dev/nvme0n1p3 -> 3), or 0.
func partNumber(name string) int {
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	n, err := strconv.Atoi(name[i:])
	if err != nil {
		return 0
	}
	return n
}

// biosBootPartitions returns the BIOS boot (EF02) partitions on disk.
func biosBootPartitions(ctx context.Context, runner executor.Runner, disk string) ([]blockDev, error) {
	devs, err := listBlockDevs(ctx, runner, disk)
	if err != nil {
		return nil, err
	}
	var out []blockDev
	for _, d := range devs {
		if d.Type == "part" && d.PartType == gptTypeBIOSBoot {
			out = append(out, d)
		}
	}
	return out, nil
}

// HasBIOSBootPartition reports whether disk carries a BIOS boot (EF02)
// partition, for a storage result that recorded none (bios_boot_partition 0,
// e.g. a mount-existing layout).
func HasBIOSBootPartition(ctx context.Context, runner executor.Runner, disk string) bool {
	parts, err := biosBootPartitions(ctx, runner, disk)
	return err == nil && len(parts) > 0
}

// espDeviceOf returns the device of the /boot/efi filesystem in the storage
// result ("" when there is none): a partition, or the md mirror of them.
func espDeviceOf(sr result.StorageResult) string {
	for _, fs := range sr.Filesystems {
		if fs.Mount == "/boot/efi" {
			return fs.Device
		}
	}
	return ""
}

// detectESPPartition finds the ESP's partition number on disk, for a storage
// result that recorded none (esp_partition 0). In order: the one partition of
// GPT type EF00 (or MBR 0xef); among several, or when the type is not set, the
// partition that is -- or carries the md mirror that is -- the /boot/efi
// device or is mounted at .../boot/efi. 0 when nothing qualifies.
func detectESPPartition(ctx context.Context, runner executor.Runner, disk, espDevice string) int {
	devs, err := listBlockDevs(ctx, runner, disk)
	if err != nil {
		return 0
	}
	isESPMount := func(d blockDev) bool {
		return strings.HasSuffix(d.MountPoint, "/boot/efi") || (espDevice != "" && sameDevice(d.Name, espDevice))
	}
	holdsESP := func(p blockDev) bool {
		if isESPMount(p) {
			return true
		}
		for _, c := range devs {
			if c.PKName == p.Name && isESPMount(c) {
				return true
			}
		}
		return false
	}
	var byType, byMount []blockDev
	for _, d := range devs {
		if d.Type != "part" {
			continue
		}
		if d.PartType == gptTypeESP || d.PartType == mbrTypeESP {
			byType = append(byType, d)
		}
		if holdsESP(d) {
			byMount = append(byMount, d)
		}
	}
	switch {
	case len(byType) == 1:
		return partNumber(byType[0].Name)
	case len(byType) > 1:
		for _, d := range byType {
			if holdsESP(d) {
				return partNumber(d.Name)
			}
		}
		return partNumber(byType[0].Name)
	case len(byMount) > 0:
		return partNumber(byMount[0].Name)
	}
	return 0
}

// sameDevice compares two device paths after resolving symlinks
// (/dev/md/esp -> /dev/md127).
func sameDevice(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
