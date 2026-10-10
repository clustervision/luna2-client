// Package raid is the Go port of /root/NEWTREE/lib/raid.sh — building
// mdadm RAID arrays on top of the partition layout that
// internal/partition produced. The single public entry point is
// Builder.Build, which assembles the storage array (always) and the
// mirrored EFI + /boot arrays (only when the OS set runs raid1 under
// UEFI, matching the lib/raid.sh logic).
//
// Cross-distribution / arch notes:
//   - mdadm semantics are kernel-level; the userspace tool ships
//     identically on RHEL/Debian/Ubuntu and on arm64.
//   - --metadata 1.2 is used for the storage array (matches what the
//     bash version emits). The mirrored efi/boot arrays use metadata
//     1.0 because GRUB/UEFI read the underlying disks directly and need
//     the superblock at the END of the partition. This is the
//     long-standing recommendation from the mdadm + GRUB maintainers.
package raid

import (
	"context"
	"fmt"
	"strings"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
)

// State is the typed equivalent of the JSON lib/raid.sh emits. Field
// names mirror the bash keys so we can serialise the same shape later.
type State struct {
	RAIDMode            string   `json:"raid_mode"`
	MDDevice            string   `json:"md_device"`
	MetadataVersion     string   `json:"metadata_version"`
	Members             []string `json:"members"`
	EFIMDDevice         string   `json:"efi_md_device,omitempty"`
	EFIMetadataVersion  string   `json:"efi_metadata_version,omitempty"`
	EFIMembers          []string `json:"efi_members,omitempty"`
	BootMDDevice        string   `json:"boot_md_device,omitempty"`
	BootMetadataVersion string   `json:"boot_metadata_version,omitempty"`
	BootMembers         []string `json:"boot_members,omitempty"`
}

// Options configures a Builder.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger
}

// Builder owns the construction sequence.
type Builder struct {
	o Options
}

// New returns a Builder.
func New(o Options) *Builder { return &Builder{o: o} }

// Build assembles the md arrays required by the partition layout. When
// the set has raid_mode=none, Build returns a zero-valued State and
// makes no command calls.
//
// Mirrors lib/raid.sh::create_storage_mdraid.
func (b *Builder) Build(ctx context.Context, set *v2.Set, layout partition.Layout) (State, error) {
	if b.o.Runner == nil {
		return State{}, fmt.Errorf("raid.Build: Runner is required")
	}

	raidMode := layout.RAIDMode
	if raidMode == "" || raidMode == "none" {
		return State{RAIDMode: "none"}, nil
	}

	level, err := levelFromMode(raidMode)
	if err != nil {
		return State{}, err
	}
	mdName := mdNameForSet(set.Name)
	mdDevice := "/dev/md/" + mdName
	metadata := "1.2"

	// Hot spares: the resolver hands the set count+spares disks and the
	// partition builder lays out every one of them, so the LAST <spares>
	// storage partitions are spares, not active members. Creating with
	// --raid-devices=len(members) made raid1/10 + spares fail and silently
	// grew raid0/5/6 by the spare count.
	spares := set.SpareCount()
	if spares < 0 {
		spares = 0
	}
	if active := len(layout.StoragePartitions) - spares; len(layout.StoragePartitions) > 0 && active < 1 {
		return State{}, fmt.Errorf("raid: set %q has %d storage partition(s) but asks for %d spare(s): no active member left",
			set.Name, len(layout.StoragePartitions), spares)
	}
	if err := b.create(ctx, mdDevice, metadata, level, layout.StoragePartitions, spares); err != nil {
		return State{}, err
	}

	state := State{
		RAIDMode:        raidMode,
		MDDevice:        mdDevice,
		MetadataVersion: metadata,
		Members:         append([]string(nil), layout.StoragePartitions...),
	}

	// The boot-chain mirrors exist only for the boot partitions the layout
	// actually has: an os set without a /boot volume (boot on the root LV)
	// used to fail "raid.create: no members" on the -boot array. They mirror
	// across EVERY disk of the set, spares included, so each disk the NVRAM
	// lists can boot.
	if layout.MirrorBoot && set.Role == "os" {
		if len(layout.EFIPartitions) > 0 {
			state.EFIMDDevice = "/dev/md/" + mdName + "-efi"
			state.EFIMetadataVersion = "1.0"
			state.EFIMembers = append([]string(nil), layout.EFIPartitions...)
			if err := b.create(ctx, state.EFIMDDevice, "1.0", "1", layout.EFIPartitions, 0); err != nil {
				return State{}, err
			}
		}
		if len(layout.BootPartitions) > 0 {
			state.BootMDDevice = "/dev/md/" + mdName + "-boot"
			state.BootMetadataVersion = "1.0"
			state.BootMembers = append([]string(nil), layout.BootPartitions...)
			if err := b.create(ctx, state.BootMDDevice, "1.0", "1", layout.BootPartitions, 0); err != nil {
				return State{}, err
			}
		}
	}

	return state, nil
}

// create runs `mdadm --create <md> --run --metadata <m> --level <l> ...`
// then wipefs's the resulting md device so a subsequent pvcreate /
// mkfs does not refuse on a leftover signature. The last `spares` members
// become hot spares (--raid-devices counts the active ones only).
func (b *Builder) create(ctx context.Context, mdDevice, metadata, level string, members []string, spares int) error {
	if len(members) == 0 {
		return fmt.Errorf("raid.create: no members for %s", mdDevice)
	}
	active := len(members) - spares
	if spares < 0 || active < 1 {
		return fmt.Errorf("raid.create: %d member(s) with %d spare(s) for %s", len(members), spares, mdDevice)
	}

	// Defensively clear each member before creating. sgdisk re-creates the
	// partitions but does not wipe their contents, so a stale md superblock
	// from a prior array at the same offset can survive — and then `mdadm
	// --create` detects it ("appears to be part of an array") and, with no tty,
	// aborts on the y/n prompt instead of proceeding. Zeroing + wipefs the
	// members here makes the create deterministic regardless of prior state.
	for _, m := range members {
		_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{
			Path: "mdadm", Args: []string{"--zero-superblock", m},
			CaptureStdout: true, CaptureStderr: true,
		})
		_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{
			Path: "wipefs", Args: []string{"-a", m},
			CaptureStdout: true, CaptureStderr: true,
		})
	}

	args := []string{
		"--create", mdDevice,
		"--run",
		"--metadata", metadata,
		"--level", level,
		"--raid-devices", fmtInt(active),
	}
	if spares > 0 {
		args = append(args, "--spare-devices", fmtInt(spares))
	}
	args = append(args,
		"--force",
		// --homehost=any (BE-N4): metadata 1.x records the creating host's
		// name in the superblock. Without this the array carries the
		// installer initramfs hostname, so the installed OS considers it
		// "foreign" and may assemble it as /dev/md127 instead of its
		// deterministic name. "any" makes every host assemble it under the
		// name recorded in the superblock.
		"--homehost=any",
	)
	// --assume-clean is safe ONLY for non-parity levels: raid0 has no
	// redundancy to sync and raid1/raid10 copy written data at write time,
	// so unwritten-area mismatches are benign. For raid5/raid6 it marks the
	// array in-sync with UNINITIALISED PARITY — any later rebuild or
	// degraded read of a stripe the fs didn't fully rewrite reconstructs
	// garbage (silent data corruption, FTA C1). Parity levels therefore
	// take the initial background resync (the array is usable meanwhile).
	switch level {
	case "0", "1", "10":
		args = append(args, "--assume-clean")
	}
	args = append(args, members...)

	if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "mdadm", Args: args,
		// Auto-answer the "appears to be part of an array, continue?" prompt in
		// case a stale superblock survived the defensive clear above; without a
		// tty mdadm would otherwise read EOF and abort.
		Stdin:         strings.NewReader("yes\n"),
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("mdadm --create %s: %w", mdDevice, err)
	}

	// udevadm settle so /dev/md/<name> appears.
	_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{Path: "udevadm", Args: []string{"settle"}})

	// wipefs any stale signatures, then settle again.
	if _, err := b.o.Runner.Run(ctx, executor.CommandSpec{
		Path: "wipefs", Args: []string{"-af", mdDevice},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("wipefs -af %s: %w", mdDevice, err)
	}
	_, _ = b.o.Runner.Run(ctx, executor.CommandSpec{Path: "udevadm", Args: []string{"settle"}})

	return nil
}

// levelFromMode maps raid_modeN tokens to mdadm --level values.
// Mirrors lib/raid.sh::raid_level_from_mode.
func levelFromMode(raidMode string) (string, error) {
	switch raidMode {
	case "raid0":
		return "0", nil
	case "raid1":
		return "1", nil
	case "raid5":
		return "5", nil
	case "raid6":
		return "6", nil
	case "raid10":
		return "10", nil
	default:
		return "", fmt.Errorf("unsupported RAID mode for mdadm: %s", raidMode)
	}
}

// MDNameForSet is the exported form of mdNameForSet for callers that need
// to predict this set's md device names (e.g. the mount-existing
// activation in lpart-storage-prepare).
func MDNameForSet(setName string) string { return mdNameForSet(setName) }

// mdNameForSet mirrors lib/raid.sh::_md_device_name and
// lib/cleanup.sh::_set_md_name: strip non-alphanumeric to underscore,
// then trim trailing underscores.
func mdNameForSet(setName string) string {
	var b strings.Builder
	for _, r := range setName {
		switch {
		case 'a' <= r && r <= 'z',
			'A' <= r && r <= 'Z',
			'0' <= r && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// fmtInt avoids the strconv import for the single integer we render.
func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
