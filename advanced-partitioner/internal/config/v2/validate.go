package v2

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raidlevel"
)

// Validate checks the Config against the v1.4 disklayout rules. Error
// messages are prefixed with "config_validation: " so tooling can
// pattern-match the class of failure. Unlike the v2-era validator this no
// longer mirrors the NEWTREE Bash json_v2.sh byte-for-byte (see
// decisions/0003); the grammar it validates did not exist in the Bash tree.
//
// Mode-specific rules (e.g. "a diskful mode needs exactly one os set") are
// NOT enforced here — the config is validated independently of install_mode.
// This validator only rejects >1 os set; the decision engine applies the
// rest once it knows the mode.
func (c *Config) Validate() error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("config_validation: re-encode: %w", err)
	}
	return c.validateRaw(raw)
}

// ValidateRaw is the same as Validate but takes the original JSON bytes so
// unknown-field detection catches keys that did not survive the typed
// decode. Prefer this when you have the bytes.
func (c *Config) ValidateRaw(raw []byte) error {
	return c.validateRaw(raw)
}

func (c *Config) validateRaw(raw []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("config_validation: top-level: %w", err)
	}

	if err := unknownKey(top, allowedTopLevel, "top-level"); err != nil {
		return err
	}

	if c.Version != SchemaVersion {
		got := "null"
		if v, ok := top["version"]; ok {
			got = strings.TrimSpace(string(v))
		}
		return fmt.Errorf("config_validation: version must be 2, got: %s", got)
	}

	setsRaw, ok := top["sets"]
	if !ok || !looksLikeJSONArray(setsRaw) {
		return fmt.Errorf("config_validation: sets must be an array")
	}
	if len(c.Sets) == 0 {
		return fmt.Errorf("config_validation: sets[] must be non-empty")
	}

	// At most one os set. Zero is allowed (a data-only disklayout for a
	// memboot node with persistent local data); the decision engine
	// rejects "no os set" only for the diskful modes.
	osCount := 0
	for _, s := range c.Sets {
		if s.Role == "os" {
			osCount++
		}
	}
	if osCount > 1 {
		return fmt.Errorf("config_validation: at most one set may have role=os, found: %d", osCount)
	}

	var setsArr []json.RawMessage
	if err := json.Unmarshal(setsRaw, &setsArr); err != nil {
		return fmt.Errorf("config_validation: sets: %w", err)
	}
	for i := range c.Sets {
		var setRaw json.RawMessage
		if i < len(setsArr) {
			setRaw = setsArr[i]
		}
		if err := c.Sets[i].validate(i, setRaw); err != nil {
			return err
		}
	}
	if err := c.validateSetNameCollisions(); err != nil {
		return err
	}

	// Cross-set device exclusivity (literal level). No device path may
	// appear in more than one set: a duplicated device means one set's
	// create/cleanup destroys another set's (possibly persistent) data.
	// This catches same-style duplicates at parse time; the runtime
	// reservation in lpart-storage-prepare re-checks on CANONICAL kernel
	// paths, which also catches aliases (by-id vs /dev/sdX). (FTA BR-1a.)
	seen := map[string]string{} // cleaned device path -> set name
	for i := range c.Sets {
		s := &c.Sets[i]
		for _, d := range s.Devices {
			key := cleanDevicePath(d)
			if key == "" {
				continue
			}
			if prev, dup := seen[key]; dup {
				return fmt.Errorf(
					"config_validation: device %s appears in both set %q and set %q — a device may belong to exactly one set",
					d, prev, s.Name)
			}
			seen[key] = s.Name
		}
	}
	return nil
}

// cleanDevicePath normalises a configured device path for duplicate
// detection: trims whitespace and cleans the path (trailing slashes,
// double slashes, dot segments).
func cleanDevicePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func looksLikeJSONArray(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

//nolint:gocyclo,revive // a flat schema validator reads better than a fragmented one
func (s *Set) validate(idx int, raw json.RawMessage) error {
	pfx := fmt.Sprintf("sets[%d]", idx)

	if len(raw) == 0 {
		var err error
		raw, err = json.Marshal(s)
		if err != nil {
			return fmt.Errorf("config_validation: %s: re-encode: %w", pfx, err)
		}
	}
	setRaw, err := objectFromRaw(raw)
	if err != nil {
		return fmt.Errorf("config_validation: %s: %w", pfx, err)
	}
	if err := unknownKey(setRaw, allowedSet, pfx); err != nil {
		return err
	}
	if _, ok := setRaw["match"]; ok {
		matchRaw, err := objectFromRaw(setRaw["match"])
		if err != nil {
			return fmt.Errorf("config_validation: %s.match: %w", pfx, err)
		}
		if err := unknownKey(matchRaw, allowedMatch, pfx+".match"); err != nil {
			return err
		}
	}
	if _, ok := setRaw["origin"]; ok {
		originRaw, err := objectFromRaw(setRaw["origin"])
		if err != nil {
			return fmt.Errorf("config_validation: %s.origin: %w", pfx, err)
		}
		if err := unknownKey(originRaw, allowedOrigin, pfx+".origin"); err != nil {
			return err
		}
	}

	if s.Name == "" {
		return fmt.Errorf("config_validation: %s.name is required", pfx)
	}
	if err := validateSetName(idx, s.Name); err != nil {
		return err
	}

	switch s.Role {
	case "os", "data":
	case "":
		return fmt.Errorf("config_validation: %s.role is required", pfx)
	default:
		return fmt.Errorf("config_validation: %s.role unsupported: '%s' (allowed: os, data)", pfx, s.Role)
	}

	switch s.Selection {
	case "discover", "manual":
	case "":
		return fmt.Errorf("config_validation: %s.selection is required (discover|manual)", pfx)
	default:
		return fmt.Errorf("config_validation: %s.selection unsupported: '%s' (allowed: discover, manual)", pfx, s.Selection)
	}

	if err := s.validateMatch(idx); err != nil {
		return err
	}

	// save pins a discovered layout back to the node (decisions/0008); it is
	// meaningless on a manual set, which is already the pinned form.
	if s.Save && s.Selection != "discover" {
		return fmt.Errorf("config_validation: %s.save is only valid on a selection=discover set", pfx)
	}

	// A persistent set must be pinned to explicit devices: "hands-off" is a
	// guarantee about specific disks, and a discover set has no devices to
	// guarantee anything about until an installer run picks some. (Pin a
	// discovered layout with save:true first, then mark it persistent.)
	if s.Persistent && s.Selection != "manual" {
		return fmt.Errorf(
			"config_validation: %s: persistent=true requires selection=manual — a hands-off guarantee needs pinned devices (use save:true to pin a discovered layout first)",
			pfx)
	}

	// bootloader is a property of the system that boots, so it belongs to the
	// os set; on a data set it would silently do nothing.
	if _, ok := setRaw["bootloader"]; ok {
		switch s.Bootloader {
		case BootloaderFirst, BootloaderLast, BootloaderNone:
		default:
			return fmt.Errorf(
				"config_validation: %s.bootloader unsupported: '%s' (allowed: first (or grub, yes, true), last (the default), none (or no, false))",
				pfx, s.Bootloader)
		}
		if s.Role != "os" {
			return fmt.Errorf("config_validation: %s: bootloader is valid on the os set only", pfx)
		}
	}

	if s.Raid == "" {
		return fmt.Errorf("config_validation: %s.raid is required (none|0|1|5|6|10)", pfx)
	}
	r, err := raidlevel.ParseRaid(s.Raid)
	if err != nil {
		return fmt.Errorf("config_validation: %s.raid %v", pfx, err)
	}

	// OS-set RAID policy (operator decision 2026-07-16): the OS lives on
	// exactly 1, 2 or 4 drives -- raid none / raid1 / raid0 / raid10. Parity
	// RAID (5/6) is REFUSED for the OS role: GRUB cannot reliably boot a
	// degraded parity array (BE-B10/NB-10 -- only raid1 is degraded-bootable,
	// so parity on the OS trades real bootability for imagined redundancy).
	// DATA sets keep the full palette including raid5/6.
	if s.Role == "os" {
		switch r {
		case raidlevel.Raid5, raidlevel.Raid6:
			return fmt.Errorf(
				"config_validation: %s: raid=%s is not supported for the OS set -- the OS uses 1, 2 or 4 drives with raid none/1/0/10 (parity RAID cannot boot degraded, NB-10); raid%s remains available for data sets",
				pfx, s.Raid, s.Raid)
		}
		osCount := 0
		switch s.Selection {
		case "manual":
			osCount = len(s.Devices)
		case "discover":
			osCount = s.Count
		}
		switch osCount {
		case 0, 1, 2, 4: // 0 = discover count unset (defaults per raid mode)
		default:
			return fmt.Errorf(
				"config_validation: %s: the OS set uses exactly 1, 2 or 4 drives (raid none/1/0/10), got %d",
				pfx, osCount)
		}
	}

	if s.Spares < 0 {
		return fmt.Errorf("config_validation: %s.spares must be >= 0, got: %d", pfx, s.Spares)
	}

	// Member-count rules differ by selection.
	switch s.Selection {
	case "manual":
		if len(s.Devices) == 0 {
			return fmt.Errorf("config_validation: %s.devices[] is required for selection=manual", pfx)
		}
		if !r.AllowsCount(len(s.Devices)) {
			return fmt.Errorf(
				"config_validation: %s: raid=%s needs %s, but devices[] lists %d",
				pfx, s.Raid, countSpec(r), len(s.Devices))
		}
	case "discover":
		if s.Count != 0 && !r.AllowsCount(s.Count) {
			return fmt.Errorf(
				"config_validation: %s: raid=%s needs %s, but count=%d",
				pfx, s.Raid, countSpec(r), s.Count)
		}
	}

	if len(s.Volumes) == 0 {
		return fmt.Errorf("config_validation: %s.volumes[] must be non-empty", pfx)
	}

	volumesArr := setRaw["volumes"]
	var volRaws []json.RawMessage
	if len(volumesArr) > 0 {
		if err := json.Unmarshal(volumesArr, &volRaws); err != nil {
			return fmt.Errorf("config_validation: %s.volumes: %w", pfx, err)
		}
	}
	for j := range s.Volumes {
		var volRaw json.RawMessage
		if j < len(volRaws) {
			volRaw = volRaws[j]
		}
		if err := s.Volumes[j].validate(idx, j, volRaw); err != nil {
			return err
		}
		v := &s.Volumes[j]
		if err := validateVolumeName(idx, s.Name, j, v.Name, v.Provider == "lvm"); err != nil {
			return err
		}
		if err := validateVolumeSize(idx, s.Name, j, v); err != nil {
			return err
		}
	}
	if err := s.validateVolumeNamesUnique(idx); err != nil {
		return err
	}

	// volumes[].raid is not a thing — raid is a set property.
	for _, vol := range volRaws {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(vol, &m); err != nil {
			continue
		}
		if _, ok := m["raid"]; ok {
			return fmt.Errorf("config_validation: %s: volumes[].raid is not supported; use the set raid field", pfx)
		}
	}

	// Direct-partition cross-volume rules. A set using generic direct
	// partition volumes (anything other than /boot or /boot/efi) must be a
	// single, un-RAIDed disk; at most one of them may take size=100%, and
	// it may not be mixed with a 100% LVM volume.
	genericPart, genericPartHundred, lvmHundred := 0, 0, 0
	for _, v := range s.Volumes {
		if v.Provider == "lvm" && v.Size == "100%" {
			lvmHundred++
		}
		if v.Provider != "partition" {
			continue
		}
		if v.Mountpoint == "/boot" || v.Mountpoint == "/boot/efi" {
			continue
		}
		genericPart++
		if v.Size == "100%" {
			genericPartHundred++
		}
	}
	if genericPart > 0 {
		if r != raidlevel.RaidNone {
			return fmt.Errorf("config_validation: %s: direct partition volumes require raid=none", pfx)
		}
		if genericPartHundred > 1 {
			return fmt.Errorf("config_validation: %s: at most one direct partition volume may use size=100%%", pfx)
		}
		if lvmHundred > 0 && genericPartHundred > 0 {
			return fmt.Errorf("config_validation: %s: direct partition size=100%% cannot be mixed with lvm volumes in the same set", pfx)
		}
	}
	if lvmHundred > 1 {
		return fmt.Errorf("config_validation: %s: at most one lvm volume per set may use size=100%%", pfx)
	}

	// OS set: exactly one root volume, and a UEFI ESP.
	if s.Role == "os" {
		rootCount := 0
		hasESP := false
		for _, v := range s.Volumes {
			if v.Mountpoint == "/" && (v.Provider == "partition" || v.Provider == "lvm") {
				rootCount++
			}
			if v.Provider == "partition" && v.Mountpoint == "/boot/efi" && v.FS == "vfat" {
				hasESP = true
			}
		}
		if rootCount != 1 {
			return fmt.Errorf(
				"config_validation: %s: an os set must have exactly one root volume (mountpoint=/ provider=partition|lvm), found: %d",
				pfx, rootCount)
		}
		if !hasESP {
			return fmt.Errorf(
				"config_validation: %s: an os set must declare a /boot/efi vfat partition (UEFI ESP)", pfx)
		}
	}

	return s.validateDevicesConsistent(idx)
}

// countSpec renders the member-count requirement for a raid level for use in
// error messages.
func countSpec(r raidlevel.Raid) string {
	min, max := r.MinDisks(), r.MaxDisks()
	if max == 0 {
		return fmt.Sprintf("at least %d device(s)", min)
	}
	if min == max {
		return fmt.Sprintf("exactly %d device(s)", min)
	}
	return fmt.Sprintf("%d-%d device(s)", min, max)
}

func (v *Volume) validate(setIdx, volIdx int, raw json.RawMessage) error {
	vpfx := fmt.Sprintf("sets[%d].volumes[%d]", setIdx, volIdx)

	if len(raw) == 0 {
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			return fmt.Errorf("config_validation: %s: re-encode: %w", vpfx, err)
		}
	}
	volRaw, err := objectFromRaw(raw)
	if err != nil {
		return fmt.Errorf("config_validation: %s: %w", vpfx, err)
	}
	if err := unknownKey(volRaw, allowedVolume, vpfx); err != nil {
		return err
	}

	if v.Name == "" {
		return fmt.Errorf("config_validation: %s.name is required", vpfx)
	}
	if v.Mountpoint == "" {
		return fmt.Errorf("config_validation: %s.mountpoint is required", vpfx)
	}

	switch v.FS {
	case "vfat", "xfs", "ext4", "swap":
	case "":
		return fmt.Errorf("config_validation: %s.fs is required", vpfx)
	case "tmpfs":
		return fmt.Errorf("config_validation: %s.fs=tmpfs is reserved for the memboot RAM root and not valid in a disklayout", vpfx)
	default:
		return fmt.Errorf("config_validation: %s.fs unsupported: '%s' (allowed: vfat, xfs, ext4, swap)", vpfx, v.FS)
	}

	switch v.Provider {
	case "partition", "lvm":
	case "":
		return fmt.Errorf("config_validation: %s.provider is required (partition|lvm)", vpfx)
	case "memory":
		return fmt.Errorf("config_validation: %s.provider=memory is reserved for the memboot RAM root and not valid in a disklayout", vpfx)
	case "zfs":
		return fmt.Errorf("config_validation: %s.provider=zfs is reserved for future implementation", vpfx)
	default:
		return fmt.Errorf("config_validation: %s.provider unsupported: '%s' (allowed: partition, lvm)", vpfx, v.Provider)
	}

	switch {
	case v.Mountpoint == "swap":
	case strings.HasPrefix(v.Mountpoint, "/"):
	default:
		return fmt.Errorf("config_validation: %s.mountpoint must be an absolute path or swap, got: %s", vpfx, v.Mountpoint)
	}

	if v.Provider == "partition" {
		if v.Size == "" {
			return fmt.Errorf("config_validation: %s.size is required for provider=partition", vpfx)
		}
		switch {
		case v.Mountpoint == "/boot/efi" && v.FS == "vfat":
		case v.Mountpoint == "swap" && v.FS == "swap":
		case v.FS == "xfs", v.FS == "ext4", v.FS == "vfat":
		default:
			return fmt.Errorf("config_validation: %s: unsupported provider=partition combination mountpoint=%s fs=%s", vpfx, v.Mountpoint, v.FS)
		}
	}

	if v.Provider == "lvm" {
		switch v.FS {
		case "xfs", "ext4", "swap":
		default:
			return fmt.Errorf("config_validation: %s: lvm volume fs must be xfs, ext4, or swap, got: %s", vpfx, v.FS)
		}
	}

	if v.Mountpoint == "swap" && v.FS != "swap" {
		return fmt.Errorf("config_validation: %s: mountpoint=swap requires fs=swap", vpfx)
	}
	if v.FS == "swap" && v.Mountpoint != "swap" {
		return fmt.Errorf("config_validation: %s: fs=swap requires mountpoint=swap", vpfx)
	}

	return nil
}

// validateDevicesConsistent enforces that every device path in a manual set
// is a supported style and that all paths in a set share one style.
//
// Device-mapper / multipath paths (/dev/mapper/*, /dev/dm-*) are REFUSED as
// set devices: partition naming on dm devices has no reliable convention
// (kpartx emits mpathap1, multipathd setups vary, /dev/dm-N partitions get
// unrelated dm-M nodes), so the partition-path chokepoint (internal/devices
// PartitionPath) cannot derive member partitions safely. Fail closed until
// multipath is a designed, tested feature. (FTA v2 BE-N3 / CCF-8, barrier
// NB-4.) This check also runs for SINGLE-device sets, which previously
// skipped style classification entirely.
func (s *Set) validateDevicesConsistent(idx int) error {
	var styleOf SelectorType
	for i, d := range s.EffectiveDevices() {
		if strings.HasPrefix(d, "/dev/mapper/") || strings.HasPrefix(d, "/dev/dm-") {
			return fmt.Errorf(
				"config_validation: sets[%d].devices: device-mapper/multipath path %q is not supported as a set device (dm partition naming has no reliable convention); use the underlying disk's by-id, by-path, or kernel name",
				idx, d)
		}
		st, err := selectorTypeOf(d)
		if err != nil {
			return fmt.Errorf("config_validation: %w", err)
		}
		if i == 0 {
			styleOf = st
			continue
		}
		if st != styleOf {
			return fmt.Errorf(
				"config_validation: sets[%d].devices: all paths must use the same style (/dev/..., by-id, by-path, by-uuid)",
				idx)
		}
	}
	return nil
}
