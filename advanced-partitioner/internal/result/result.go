// Package result owns the storage-result.json schema that
// lpart-storage-prepare emits and lpart-bootloader-finalise + lpart-storage-check
// consume. It is the Go port of /root/NEWTREE/lib/output_json.sh::{
// assemble_storage_result, record_block_uuids}.
//
// The struct field tags reproduce the JSON keys the Bash version writes
// byte-for-byte so downstream tools continue to work without changes.
//
// Cross-platform notes: blkid behaves identically across the supported
// distros and architectures; the UUID format it prints is the
// filesystem's UUID for ext4/xfs/vfat and the LABEL/UUID stamped by
// mkswap for swap.
package result

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	fsmod "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raid"
)

// Filesystem is one filesystem entry in StorageResult.Filesystems. The
// schema mirrors the bash JSON. UUID is populated by RecordBlockUUIDs.
type Filesystem struct {
	Mount          string `json:"mount"`
	Device         string `json:"device"`
	FS             string `json:"fs"`
	UUID           string `json:"uuid,omitempty"`
	Options        string `json:"options,omitempty"`
	ClearUEFINVRAM bool   `json:"clear_uefi_nvram,omitempty"`
}

// LVMInfo holds the small VG/PV pair the bootloader uses.
type LVMInfo struct {
	VGName   *string `json:"vg_name"`
	PVDevice *string `json:"pv_device"`
}

// StorageResult is one set's portion of storage-result.json. The
// multi-set variant wraps these in {"version":2,"sets":[...]} — see
// WriteAuto for the dispatch rule.
type StorageResult struct {
	Version        int                 `json:"version"`
	Name           string              `json:"name"`
	Role           string              `json:"role"`
	FirmwareMode   string              `json:"firmware_mode"`
	SelectedDisks  []selector.Resolved `json:"selected_disks"`
	StorageType    string              `json:"storage_type"`
	BootMethod     string              `json:"boot_method"`
	BootFirmware   string              `json:"boot_firmware"`
	RegisterNVRAM  string              `json:"register_nvram"`
	ClearUEFINVRAM bool                `json:"clear_uefi_nvram"`
	RAIDMode       string              `json:"raid_mode"`
	BootDevices    []string            `json:"boot_devices"`
	// ESPPartition is the GPT partition NUMBER of the EFI System Partition
	// on each boot disk (the layout builder creates it at the same index on
	// every member). The bootloader's efibootmgr --create uses this instead
	// of a hardcoded 1 (FTA D11). 0 means "unknown" (older results) and
	// callers default to 1.
	ESPPartition      int          `json:"esp_partition,omitempty"`
	RootDevice        string       `json:"root_device"`
	Filesystems       []Filesystem `json:"filesystems"`
	LVM               LVMInfo      `json:"lvm"`
	MDRaidDevice      *string      `json:"mdraid_device"`
	EFIMDDevice       *string      `json:"efi_md_device"`
	BootMDDevice      *string      `json:"boot_md_device"`
	MountedTargetRoot string       `json:"mounted_target_root"`
	// FirstInstall is true when lpart-storage-prepare just wiped + re-built
	// the layout (i.e. ran with --wipe, which is the default path).
	// Downstream tools use this to short-circuit re-install heuristics:
	// lpart-osimage-install treats osimage_sync=Sync as Full when this is
	// true, because the preserve list is only meaningful when there is
	// prior content on disk to preserve.
	FirstInstall bool `json:"first_install,omitempty"`
	// Persistent mirrors v2.Set.Safety.Persistent for the set. When true,
	// lpart-storage-prepare skipped cleanup + create and only mounted
	// existing volumes; lpart-osimage-install reads this to auto-exclude the
	// set's mountpoints from the tar extract in every mode; the future
	// installer UI uses it to mark the set as operator-pinned.
	Persistent bool `json:"persistent,omitempty"`
}

// partitionNumberOf extracts the trailing partition number from a
// partition device path (/dev/sda1 -> 1, /dev/nvme0n1p3 -> 3). Returns 0
// when the path carries no trailing digits.
func partitionNumberOf(partPath string) int {
	i := len(partPath)
	for i > 0 && partPath[i-1] >= '0' && partPath[i-1] <= '9' {
		i--
	}
	if i == len(partPath) {
		return 0
	}
	n := 0
	for _, c := range partPath[i:] {
		n = n*10 + int(c-'0')
	}
	return n
}

// MultiSet wraps several StorageResults when more than one set is
// active. Mirrors {"version":2,"sets":[...]}.
type MultiSet struct {
	Version int             `json:"version"`
	Sets    []StorageResult `json:"sets"`
}

// AssembleInput bundles every typed result the modules produced for one
// set. The fields are populated by the orchestrator in lpart-storage-prepare;
// memory sets pass Layout/RAID/LVM as zero values.
type AssembleInput struct {
	Set          *v2.Set
	FirmwareMode string
	Selected     []selector.Resolved
	Layout       partition.Layout
	RAID         raid.State
	LVM          lvm.State
	Filesystems  fsmod.Filesystems
	Mount        mount.Result
	// FirstInstall propagates to StorageResult.FirstInstall. Set to true
	// when lpart-storage-prepare wiped + re-built (i.e. --wipe was on).
	FirstInstall bool
	// Persistent propagates to StorageResult.Persistent. Set when the set
	// has safety.persistent=true; lpart-storage-prepare also flips
	// FirstInstall off for persistent sets even when --wipe is in play
	// (their layout was not just rebuilt).
	Persistent bool
}

// Assemble builds a StorageResult from one AssembleInput. UUIDs are
// left blank; callers run RecordBlockUUIDs to populate them once the
// filesystems exist on disk.
//
// Mirrors lib/output_json.sh::assemble_storage_result.
func Assemble(in AssembleInput) StorageResult {
	res := StorageResult{
		Version:           v2.SchemaVersion,
		Name:              in.Set.Name,
		Role:              in.Set.Role,
		FirmwareMode:      in.FirmwareMode,
		SelectedDisks:     in.Selected,
		StorageType:       string(in.Set.StorageType()),
		BootMethod:        in.Set.BootMethodOrNone(),
		BootFirmware:      "uefi",
		RegisterNVRAM:     "auto",
		ClearUEFINVRAM:    in.Set.ClearUEFINVRAM(),
		RAIDMode:          in.Set.RAIDMode(),
		MountedTargetRoot: nonEmpty(in.Mount.MountedTargetRoot, mount.DefaultSysroot),
		FirstInstall:      in.FirstInstall,
		Persistent:        in.Persistent,
	}
	for _, d := range in.Selected {
		res.BootDevices = append(res.BootDevices, d.ResolvedPath)
	}
	if len(in.Layout.EFIPartitions) > 0 {
		res.ESPPartition = partitionNumberOf(in.Layout.EFIPartitions[0])
	}
	for _, f := range in.Filesystems.Filesystems {
		if f.Mount == "/" {
			res.RootDevice = f.Device
		}
		res.Filesystems = append(res.Filesystems, Filesystem{
			Mount:          f.Mount,
			Device:         f.Device,
			FS:             f.FS,
			Options:        f.Options,
			ClearUEFINVRAM: f.ClearUEFINVRAM,
		})
	}
	// LVM info is null-valued when this set has no LVM (memory or pure
	// direct-partition).
	if in.LVM.VGName != "" {
		v := in.LVM.VGName
		p := in.LVM.PVDevice
		res.LVM = LVMInfo{VGName: &v, PVDevice: &p}
	}
	if in.RAID.MDDevice != "" {
		s := in.RAID.MDDevice
		res.MDRaidDevice = &s
	}
	if in.RAID.EFIMDDevice != "" {
		s := in.RAID.EFIMDDevice
		res.EFIMDDevice = &s
	}
	if in.RAID.BootMDDevice != "" {
		s := in.RAID.BootMDDevice
		res.BootMDDevice = &s
	}
	return res
}

// RecordBlockUUIDs runs `blkid -s UUID -o value <device>` for every
// filesystem in the result and patches the UUID field in place. tmpfs
// entries are skipped (no blkid signature). Errors from a single blkid
// invocation are ignored — the field is just left blank, matching the
// Bash behaviour of `|| true`.
//
// Mirrors lib/output_json.sh::record_block_uuids.
func RecordBlockUUIDs(ctx context.Context, runner executor.Runner, res *StorageResult) error {
	if runner == nil {
		return fmt.Errorf("result.RecordBlockUUIDs: Runner is required")
	}
	for i := range res.Filesystems {
		fs := &res.Filesystems[i]
		if fs.FS == "tmpfs" || fs.Device == "" || fs.Device == "tmpfs" {
			continue
		}
		uuid, _ := blkidUUID(ctx, runner, fs.Device)
		fs.UUID = uuid
	}
	return nil
}

// blkidUUID runs `blkid -s UUID -o value <device>` and returns the
// trimmed scalar. An error is propagated but most callers ignore it
// (best-effort capture).
func blkidUUID(ctx context.Context, runner executor.Runner, device string) (string, error) {
	res, err := runner.Run(ctx, executor.CommandSpec{
		Path: "blkid", Args: []string{"-s", "UUID", "-o", "value", device},
		CaptureStdout: true, CaptureStderr: true,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// WriteAuto writes the storage-result.json at path: a single object when
// len(results)==1, the multi-set wrapper otherwise. Mirrors
// write_result_output in bin/lpart-storage-prepare.
func WriteAuto(path string, results []StorageResult) error {
	if len(results) == 0 {
		return fmt.Errorf("result.WriteAuto: no results")
	}
	var body []byte
	var err error
	if len(results) == 1 {
		body, err = json.MarshalIndent(results[0], "", "  ")
	} else {
		body, err = json.MarshalIndent(MultiSet{Version: v2.SchemaVersion, Sets: results}, "", "  ")
	}
	if err != nil {
		return fmt.Errorf("marshal storage-result: %w", err)
	}
	return writeFile(path, append(body, '\n'))
}

// ResolvedSet is the per-set resolved record (the JSON lpart-storage-prepare
// gathers into storage-resolved.json). The bash equivalent is produced
// inline in bin/lpart-storage-prepare and then re-aggregated.
type ResolvedSet struct {
	Name          string              `json:"name"`
	Role          string              `json:"role"`
	FirmwareMode  string              `json:"firmware_mode"`
	SelectorType  string              `json:"selector_type"`
	StorageType   string              `json:"storage_type"`
	RAIDMode      string              `json:"raid_mode"`
	SelectedDisks []selector.Resolved `json:"selected_disks"`
}

// ResolvedAggregate is the multi-set wrapper for storage-resolved.json:
//
//	{"version":2,"sets":[...]}
type ResolvedAggregate struct {
	Version int           `json:"version"`
	Sets    []ResolvedSet `json:"sets"`
}

// WriteResolved writes storage-resolved.json at path.
func WriteResolved(path string, sets []ResolvedSet) error {
	body, err := json.MarshalIndent(ResolvedAggregate{
		Version: v2.SchemaVersion,
		Sets:    sets,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal storage-resolved: %w", err)
	}
	return writeFile(path, append(body, '\n'))
}

// --- helpers --------------------------------------------------------------

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func writeFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", path, err)
	}
	//nolint:gosec // operator-supplied output path
	return os.WriteFile(path, body, 0o640)
}
