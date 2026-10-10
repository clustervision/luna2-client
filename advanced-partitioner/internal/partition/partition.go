// Package partition is the Go port of /root/NEWTREE/lib/partitioning.sh.
// It owns the sgdisk-driven partition-table layout that lpart-storage-prepare
// runs once per set, before raid + lvm + fs.
//
// The package is intentionally narrow: it computes the desired partition
// layout from the typed v2.Set + resolved disks, issues the sgdisk
// commands via the executor.Runner, and returns a typed Layout struct
// describing where each volume landed on disk. Downstream packages
// (raid, lvm, fs) consume Layout directly — they never re-parse JSON.
//
// Cross-distribution notes:
//   - sgdisk ships with gdisk on RHEL/Fedora/Rocky/Alma and Debian/Ubuntu.
//     The same flags work identically on arm64 (sgdisk has been
//     architecture-neutral for as long as it has had a GPT mode).
//   - Partition type codes (EF00 for ESP, EF02 for the BIOS boot
//     partition, FD00 for Linux RAID member, 8E00 for Linux LVM,
//     8200 for Linux swap) are defined by the gdisk specification and
//     apply identically on every architecture.
//   - The "p<N>" partition-naming difference between SATA/SCSI and
//     NVMe/MMC/md/loop is handled by devices.PartitionPath, which is
//     used consistently throughout this package.
package partition

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/devices"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/diskresolve"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// PartitionVolume describes one direct-partition (non-LVM) volume after
// the partition table has been written. Mirrors the partition_volumes[]
// entries lib/partitioning.sh writes into its JSON output.
type PartitionVolume struct {
	Name   string `json:"name"`
	Mount  string `json:"mount"`
	FS     string `json:"fs"`
	Device string `json:"device"`
}

// Layout is the typed result of BuildLayout. Field names mirror the JSON
// keys the bash partitioning helper writes so downstream packages can be
// serialised identically when we generate storage-result.json.
type Layout struct {
	Role              string            `json:"role"`
	FirmwareMode      string            `json:"firmware_mode"`
	RAIDMode          string            `json:"raid_mode"`
	BootFS            string            `json:"boot_fs"`
	MirrorBoot        bool              `json:"mirror_boot"`
	EFIPartitions     []string          `json:"efi_partitions"`
	BootPartitions    []string          `json:"boot_partitions"`
	StoragePartitions []string          `json:"storage_partitions"`
	PartitionVolumes  []PartitionVolume `json:"partition_volumes"`
	// BIOSBootPartitions is the EF02 partition on each boot disk that holds
	// the BIOS GRUB core image. Empty unless the set asked for a bootloader
	// on non-uefi firmware; it carries no filesystem and no mountpoint, so it
	// is never one of the set's volumes.
	BIOSBootPartitions []string `json:"bios_boot_partitions,omitempty"`
}

// Options configures a Builder.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger
	// Sleep is invoked in the partition-wait poll; defaults to time.Sleep.
	Sleep func(time.Duration)
	// WaitAttempts caps the partition-wait loop. Defaults to 30 (matches
	// the Bash _wait_for_partitions cap).
	WaitAttempts int
	// DryRun skips the partition-table wait: sgdisk was stubbed, so no
	// partitions will ever appear — polling real lsblk would hang
	// WaitAttempts seconds per disk and then fail the advertised safe
	// preview (FTA D2).
	DryRun bool
	// CommandAvailable defaults to exec.LookPath; partprobe is probed
	// because some installer images omit it.
	CommandAvailable func(string) bool
}

// Builder is the public type. Construct via New.
type Builder struct {
	o Options
}

// New returns a Builder with sensible defaults applied.
func New(o Options) *Builder {
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.WaitAttempts <= 0 {
		o.WaitAttempts = 30
	}
	if o.CommandAvailable == nil {
		o.CommandAvailable = alwaysAvailable
	}
	return &Builder{o: o}
}

func alwaysAvailable(string) bool { return true }

// ValidateRAID is the Go equivalent of lib/partitioning.sh::validate_raid_mode.
// Called by lpart-storage-prepare during the resolve phase. Mirrors the per-mode
// disk-count rules. diskCount is the number of ACTIVE members: hot spares are
// not counted (a raid1 with one spare has 3 disks and 2 members).
func ValidateRAID(raidMode string, diskCount int) error {
	switch raidMode {
	case "none":
		if diskCount != 1 {
			return fmt.Errorf("raid mode none requires exactly 1 disk")
		}
	case "raid0":
		if diskCount < 2 {
			return fmt.Errorf("raid0 requires at least 2 disks")
		}
	case "raid1":
		if diskCount != 2 {
			return fmt.Errorf("raid1 requires exactly 2 disks")
		}
	case "raid5":
		if diskCount < 3 {
			return fmt.Errorf("raid5 requires at least 3 disks")
		}
	case "raid6":
		if diskCount < 4 {
			return fmt.Errorf("raid6 requires at least 4 disks")
		}
	case "raid10":
		if diskCount < 4 {
			return fmt.Errorf("raid10 requires at least 4 disks")
		}
		if diskCount%2 != 0 {
			return fmt.Errorf("raid10 requires an even number of disks")
		}
	default:
		return fmt.Errorf("unsupported RAID mode: %s", raidMode)
	}
	return nil
}

// biosBootPartitionSize is the BIOS boot partition's size. GRUB's core image
// fits several times over in one mebibyte, which is the conventional allowance.
const biosBootPartitionSize = "1M"

// raidIsRedundant reports whether the mode survives a member loss (and so
// warrants a mirrored boot chain).
func raidIsRedundant(raidMode string) bool {
	switch raidMode {
	case "raid1", "raid5", "raid6", "raid10":
		return true
	}
	return false
}

// BuildLayout is the public entrypoint. It mirrors create_partition_layout
// in lib/partitioning.sh:
//
//  1. For each selected disk:
//     a. sgdisk --zap-all      (clear existing GPT)
//     b. sgdisk -o              (new empty GPT)
//     c. If role=os:
//     - EFI System Partition (EF00), if any
//     partition-provider volume targets /boot/efi or the OS has
//     a separate /boot partition. The type code becomes FD00
//     when we mirror the boot partitions across a 2-disk raid1.
//     - /boot partition (8300 or FD00 mirrored), if requested.
//     d. For each direct-partition volume in JSON order: sgdisk -n
//     with the volume's size + a type derived from fs.
//     e. If any LVM volume exists or this is a data set without direct
//     partitions: a storage partition (8E00 for none, FD00 for
//     RAID) consumes the rest of the disk.
//  2. Wait for the partition table to settle (lsblk poll, partprobe).
//
// firmwareMode decides one partition and nothing else: a BIOS node whose os
// set asks for a bootloader leads with a BIOS boot partition, because GRUB for
// BIOS has nowhere else to put its core image. Every other layout is the same
// on UEFI and BIOS -- GPT with an ESP (EF00, or FD00 when mirrored) -- so a
// node switched to UEFI later needs no repartition.
//
// easy side-by-side diffing.
//
//nolint:gocyclo // mirrors the flat structure of the Bash function for
func (b *Builder) BuildLayout(ctx context.Context, set *v2.Set, resolved []selector.Resolved, firmwareMode string) (Layout, error) {
	if b.o.Runner == nil {
		return Layout{}, fmt.Errorf("partition.BuildLayout: Runner is required")
	}
	if set == nil {
		return Layout{}, fmt.Errorf("partition.BuildLayout: set is nil")
	}
	if len(resolved) == 0 {
		return Layout{}, fmt.Errorf("partition.BuildLayout: no resolved disks")
	}
	layout := Layout{
		Role:         set.Role,
		FirmwareMode: firmwareMode,
		RAIDMode:     set.RAIDMode(),
		BootFS:       set.BootFS(),
	}
	// Mirror the boot chain (ESP + /boot as N-way raid1 md arrays,
	// metadata 1.0) for EVERY redundant OS RAID mode, not just raid1: a
	// raid5/6/10 root survives a disk loss, and the machine must still
	// BOOT after that loss — otherwise the array's redundancy is theatre
	// (FTA C2: /boot+ESP used to exist only on disk 0). raid0 is excluded:
	// it has no redundancy for the boot chain to match.
	if set.MirrorsBoot() {
		layout.MirrorBoot = true
	}
	// GRUB for BIOS refuses to install without a BIOS boot partition for its
	// core image. Only "first" asks for a bootloader on BIOS: "last" is a UEFI
	// boot-order idea, and on UEFI GRUB is loaded from the ESP instead.
	needBIOSBoot := set.Role == "os" && firmwareMode != "uefi" &&
		set.Bootloader == v2.BootloaderFirst

	// Classify volumes once.
	var (
		hasPartitionEFI  bool
		hasPartitionBoot bool
		lvmCount         int
		genericPartCount int
	)
	for _, v := range set.Volumes {
		switch v.Provider {
		case "lvm":
			lvmCount++
		case "partition":
			switch v.Mountpoint {
			case "/boot/efi":
				hasPartitionEFI = true
			case "/boot":
				hasPartitionBoot = true
			default:
				genericPartCount++
			}
		}
	}

	// Disk-fit preflight (BE-G6): refuse a disk that cannot hold the
	// fixed-size partitions before any sgdisk runs. Without this a
	// too-small disk surfaces only as the generic 30s "timed out waiting
	// for partitions" after the table has already been zapped.
	need := requiredDiskBytes(set, needBIOSBoot, hasPartitionEFI, hasPartitionBoot,
		lvmCount > 0 || (set.Role == "data" && genericPartCount == 0))
	for _, d := range resolved {
		// SizeBytes == 0 means discovery didn't capture a size (test
		// fixtures, exotic paths); skip rather than fail on unknown.
		if d.SizeBytes > 0 && d.SizeBytes < need {
			return Layout{}, fmt.Errorf("disk %s too small for layout: need >= %d bytes, have %d bytes",
				d.ResolvedPath, need, d.SizeBytes)
		}
	}

	for _, d := range resolved {
		disk := d.ResolvedPath
		b.runBest(ctx, "sgdisk", "--zap-all", disk)
		b.runBest(ctx, "sgdisk", "-o", disk)

		partIndex := 1

		if set.Role == "os" {
			// First on the disk: EF02, no filesystem. It is never an md
			// member -- GRUB writes a raw core image to each disk's own copy,
			// so a mirrored set boots from either member.
			if needBIOSBoot {
				b.sgdiskNewSized(ctx, disk, partIndex, biosBootPartitionSize, "EF02", "BIOSBOOT")
				layout.BIOSBootPartitions = append(layout.BIOSBootPartitions,
					devices.PartitionPath(disk, partIndex))
				partIndex++
			}
			if hasPartitionEFI {
				efiSize := set.BootEFISize()
				efiType := "EF00"
				if layout.MirrorBoot {
					efiType = "FD00"
				}
				b.sgdiskNewSized(ctx, disk, partIndex, efiSize, efiType, "EFI")
				layout.EFIPartitions = append(layout.EFIPartitions, devices.PartitionPath(disk, partIndex))
				partIndex++
			}
			if hasPartitionBoot {
				bootSize := set.BootSize()
				bootType := "8300"
				if layout.MirrorBoot {
					bootType = "FD00"
				}
				b.sgdiskNewSized(ctx, disk, partIndex, bootSize, bootType, "BOOT")
				layout.BootPartitions = append(layout.BootPartitions, devices.PartitionPath(disk, partIndex))
				partIndex++
			}
		}

		// Direct-partition volumes (non-boot, non-efi) in JSON order.
		for _, v := range set.Volumes {
			if v.Provider != "partition" {
				continue
			}
			if v.Mountpoint == "/boot" || v.Mountpoint == "/boot/efi" {
				continue
			}
			typ := "8300"
			if v.FS == "swap" {
				typ = "8200"
			}
			label := truncatePartLabel(v.Name)
			if v.Size == "100%" {
				b.sgdiskNewFull(ctx, disk, partIndex, typ, label)
			} else {
				b.sgdiskNewSized(ctx, disk, partIndex, v.Size, typ, label)
			}
			device := devices.PartitionPath(disk, partIndex)
			layout.PartitionVolumes = append(layout.PartitionVolumes, PartitionVolume{
				Name:   v.Name,
				Mount:  v.Mountpoint,
				FS:     v.FS,
				Device: device,
			})
			partIndex++
		}

		// Storage partition for LVM (or a data-without-partitions set).
		if lvmCount > 0 || (set.Role == "data" && genericPartCount == 0) {
			typ := storageTypeFor(layout.RAIDMode)
			b.sgdiskNewFull(ctx, disk, partIndex, typ, "STORAGE")
			layout.StoragePartitions = append(layout.StoragePartitions, devices.PartitionPath(disk, partIndex))
			partIndex++
		}

		expected := partIndex - 1
		if err := b.waitForPartitions(ctx, disk, expected); err != nil {
			return Layout{}, err
		}
	}

	if b.o.CommandAvailable("partprobe") {
		for _, d := range resolved {
			b.runBest(ctx, "partprobe", d.ResolvedPath)
		}
	}
	b.runBest(ctx, "udevadm", "settle")

	return layout, nil
}

// requiredDiskBytes computes the minimum per-disk size for the set's
// layout (BE-G6): the sum of every fixed-size partition BuildLayout will
// create, plus 4MiB alignment slack per partition and 1MiB for the GPT
// itself. Rest-of-disk partitions (100% volumes, the STORAGE partition)
// contribute only their slack. A size string ParseSize cannot read
// contributes nothing — sgdisk rejects it later with its own error.
func requiredDiskBytes(set *v2.Set, hasBIOSBoot, hasPartitionEFI, hasPartitionBoot, hasStorage bool) int64 {
	const (
		gptOverhead    = 1 << 20 // primary + backup GPT headers/tables
		alignmentSlack = 4 << 20 // per-partition 1MiB alignment + sgdisk rounding
	)
	var need int64
	parts := 0
	add := func(size string) {
		parts++
		if size == "100%" {
			return
		}
		if v, err := diskresolve.ParseSize(size); err == nil {
			need += v
		}
	}
	if set.Role == "os" {
		if hasBIOSBoot {
			add(biosBootPartitionSize)
		}
		if hasPartitionEFI {
			add(set.BootEFISize())
		}
		if hasPartitionBoot {
			add(set.BootSize())
		}
	}
	for _, v := range set.Volumes {
		if v.Provider != "partition" ||
			v.Mountpoint == "/boot" || v.Mountpoint == "/boot/efi" {
			continue
		}
		add(v.Size)
	}
	if hasStorage {
		parts++
	}
	return need + int64(parts)*alignmentSlack + gptOverhead
}

// sgdiskNewSized issues `sgdisk -n N:0:+SIZE -t N:TYPE -c N:LABEL <disk>`.
func (b *Builder) sgdiskNewSized(ctx context.Context, disk string, n int, size, typeCode, label string) {
	nstr := strconv.Itoa(n)
	b.runBest(ctx, "sgdisk",
		"-n", nstr+":0:+"+size,
		"-t", nstr+":"+typeCode,
		"-c", nstr+":"+label,
		disk,
	)
}

// sgdiskNewFull issues `sgdisk -n N:0:0 -t N:TYPE -c N:LABEL <disk>`.
func (b *Builder) sgdiskNewFull(ctx context.Context, disk string, n int, typeCode, label string) {
	nstr := strconv.Itoa(n)
	b.runBest(ctx, "sgdisk",
		"-n", nstr+":0:0",
		"-t", nstr+":"+typeCode,
		"-c", nstr+":"+label,
		disk,
	)
}

// truncatePartLabel keeps the GPT label below sgdisk's 36-character cap
// and replaces any non-alphanumeric character with '_'. Mirrors the
// `tr -c '[:alnum:]' '_'` step in lib/partitioning.sh.
func truncatePartLabel(name string) string {
	const maxLen = 36
	buf := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case 'a' <= r && r <= 'z',
			'A' <= r && r <= 'Z',
			'0' <= r && r <= '9':
			buf = append(buf, byte(r))
		default:
			buf = append(buf, '_')
		}
	}
	if len(buf) > maxLen {
		buf = buf[:maxLen]
	}
	return string(buf)
}

// storageTypeFor returns the sgdisk type code for the set storage
// partition. Mirrors _partition_type_for_storage.
func storageTypeFor(raidMode string) string {
	if raidMode == "none" {
		return "8E00"
	}
	return "FD00"
}

// runBest mirrors cleanup.Cleaner.runBest: log on failure, never abort.
// In the partition path failures here are still fatal at the cascade
// level because subsequent steps will trip on missing partitions, but
// the immediate behaviour of "log and continue" matches the Bash
// run_cmd-on-error pattern (the per-cmd failure logs, the wait loop
// catches a partition table that didn't settle).
func (b *Builder) runBest(ctx context.Context, path string, args ...string) {
	if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: path, Args: args, CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		if b.o.Logger != nil {
			b.o.Logger.Warn("partition cmd failed",
				"path", path, "args", args, "err", err.Error())
		}
	}
}

// waitForPartitions polls lsblk for partition count, sleeping 1s
// between attempts. Mirrors lib/partitioning.sh::_wait_for_partitions.
// Under dry-run the wait is skipped entirely (the create was stubbed).
func (b *Builder) waitForPartitions(ctx context.Context, disk string, expected int) error {
	if b.o.DryRun {
		if b.o.Logger != nil {
			b.o.Logger.Info("dry-run: skipping partition-table wait",
				"disk", disk, "expected_partitions", expected)
		}
		return nil
	}
	for i := 0; i < b.o.WaitAttempts; i++ {
		count := b.partitionCount(ctx, disk)
		if count >= expected {
			return nil
		}
		b.o.Sleep(time.Second)
	}
	return fmt.Errorf("timed out waiting for partitions on %s", disk)
}

func (b *Builder) partitionCount(ctx context.Context, disk string) int {
	res, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "lsblk",
		Args:          []string{"-ln", "-o", "TYPE", disk},
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if strings.TrimSpace(line) == "part" {
			count++
		}
	}
	return count
}
