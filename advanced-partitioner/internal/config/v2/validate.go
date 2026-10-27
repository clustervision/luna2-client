package v2

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/jsonstrict"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raidlevel"
)

// Validate checks the Config against the v1.4 disklayout rules. Error
// messages are prefixed with "config_validation: " so tooling can
// pattern-match the class of failure. Unlike the v2-era validator this no
// longer mirrors the NEWTREE Bash json_v2.sh byte-for-byte (see
// decisions/0003); the grammar it validates did not exist in the Bash tree.
//
// A rule refuses a layout only when the builders cannot lay it down or
// cleanup would damage the node. A layout that installs but looks like a
// mistake is accepted as written, with a line in Config.Warnings for the
// operator.
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

// warnf records one warning for the operator; the layout stays accepted.
func (c *Config) warnf(format string, args ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
}

func (c *Config) validateRaw(raw []byte) error {
	c.Warnings = nil
	// encoding/json keeps the last of repeated (or case-variant) keys and
	// merges repeated slices, so an earlier value can survive unseen.
	if err := jsonstrict.Check(raw); err != nil {
		c.warnf("%v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("config_validation: top-level: %w", err)
	}
	var comment string
	if v, ok := top["comment"]; ok && json.Unmarshal(v, &comment) != nil {
		c.warnf("top-level: comment is not a string and is ignored")
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
		if err := c.validateSet(i, setRaw); err != nil {
			return err
		}
	}
	if err := c.validateSetNameCollisions(); err != nil {
		return err
	}
	if err := c.validateMountpoints(); err != nil {
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
func (c *Config) validateSet(idx int, raw json.RawMessage) error {
	s := &c.Sets[idx]
	pfx := fmt.Sprintf("sets[%d]", idx)
	ref := setRef(idx, s.Name)

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

	if s.Match != nil {
		c.checkMatch(ref, s.Match)
	}

	// save pins a discovered layout back to the node (decisions/0008); it is
	// meaningless on a manual set, which is already the pinned form.
	if s.Save && s.Selection != "discover" {
		return fmt.Errorf("config_validation: %s.save is only valid on a selection=discover set", pfx)
	}
	if s.IncludeSAN && s.Selection != "discover" {
		return fmt.Errorf("config_validation: %s.include_san is only valid on a selection=discover set", pfx)
	}

	// The osimage install rewrites the os root on every install (excluding
	// /boot), so a "persistent" os set is neither protected nor consistent.
	if s.Persistent && s.Role != "data" {
		c.warnf("%s: persistent=true protects nothing on the os set: every osimage install rewrites its root; only a role=data set can be hands-off", ref)
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
	if _, ok := setRaw["allow_degraded"]; ok && (r == raidlevel.RaidNone || r == raidlevel.Raid0) {
		return fmt.Errorf("config_validation: %s.allow_degraded is valid on a redundant raid set only (raid 1, 5, 6 or 10)", pfx)
	}

	if err := c.checkSetName(ref, s, r); err != nil {
		return err
	}

	if s.Spares < 0 {
		return fmt.Errorf("config_validation: %s.spares must be >= 0, got: %d", pfx, s.Spares)
	}
	// A hot spare only ever replaces a failed member of a redundant array;
	// raid none has no array and raid0 cannot rebuild a member.
	if s.Spares > 0 && (r == raidlevel.RaidNone || r == raidlevel.Raid0) {
		c.warnf("%s: raid=%s takes no spares, so spares=%d is ignored (a hot spare only replaces a failed member of a redundant array: raid 1, 5, 6 or 10)", ref, s.Raid, s.Spares)
		s.Spares = 0
	}
	if s.Count < 0 {
		c.warnf("%s: count=%d must not be negative", ref, s.Count)
	}

	// Active members = members - spares. A manual set lists every member in
	// devices[], spares included; a discover set resolves count active members
	// (0 = the level's minimum) plus spares on top. A manual layout written
	// when the installer ignored spares lists only the members: its spares
	// are dropped, and it installs as it always did.
	active := s.Count
	if s.Selection == "manual" {
		if n := len(s.Devices); s.Spares > 0 && !r.AllowsCount(n-s.Spares) && r.AllowsCount(n) {
			c.warnf("%s: spares=%d is ignored: devices[] lists only the members; add the spare disk(s) to devices[]", ref, s.Spares)
			s.Spares = 0
		}
		active = len(s.Devices) - s.Spares
	}

	// OS-set RAID policy (operator decision 2026-07-16): the OS lives on
	// exactly 1, 2 or 4 active drives -- raid none / raid1 / raid0 / raid10
	// (hot spares come on top). Parity RAID (5/6) is REFUSED for the OS role:
	// GRUB cannot reliably boot a degraded parity array (BE-B10/NB-10 -- only
	// raid1 is degraded-bootable, so parity on the OS trades real bootability
	// for imagined redundancy). DATA sets keep the full palette including
	// raid5/6.
	if s.Role == "os" {
		switch r {
		case raidlevel.Raid5, raidlevel.Raid6:
			return fmt.Errorf(
				"config_validation: %s: raid=%s is not supported for the OS set -- the OS uses 1, 2 or 4 drives with raid none/1/0/10 (parity RAID cannot boot degraded, NB-10); raid%s remains available for data sets",
				pfx, s.Raid, s.Raid)
		}
		switch active {
		case 0, 1, 2, 4: // 0 = discover count unset (defaults per raid mode)
		default:
			return fmt.Errorf("config_validation: %s: the OS set uses exactly 1, 2 or 4 drives (raid none/1/0/10), got %s", ref, activeSpec(s, active))
		}
	}

	// Member-count rules differ by selection; they apply to active members.
	switch s.Selection {
	case "manual":
		if len(s.Devices) == 0 {
			return fmt.Errorf("config_validation: %s.devices[] is required for selection=manual", pfx)
		}
		if !r.AllowsCount(active) {
			return fmt.Errorf("config_validation: %s: raid=%s needs %s, but devices[] lists %s", ref, s.Raid, countSpec(r), activeSpec(s, active))
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
	names := make(map[string]int, len(s.Volumes))
	for j := range s.Volumes {
		v := &s.Volumes[j]
		var volRaw json.RawMessage
		if j < len(volRaws) {
			volRaw = volRaws[j]
		}
		if err := v.validate(idx, j, volRaw); err != nil {
			return err
		}
		// lvcreate refuses a second LV of the same name, and the partition
		// number predictor identifies a volume by its name.
		if prev, dup := names[v.Name]; dup {
			return fmt.Errorf(
				"config_validation: %s: volume name %q is used by volumes[%d] and volumes[%d]; volume names must be unique within a set (they are the LV names of the volume group)",
				ref, v.Name, prev, j)
		}
		names[v.Name] = j
		vref := volRef(idx, s.Name, j, v.Name)
		if err := c.checkVolumeName(vref, v); err != nil {
			return err
		}
		if err := c.checkVolumeSize(vref, v); err != nil {
			return err
		}
	}

	if err := validateLayout(idx, s, r); err != nil {
		return err
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

	return c.validateDevices(idx, s)
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

// activeSpec renders a manual set's active member count, naming the spares
// when they account for the difference with devices[].
func activeSpec(s *Set, active int) string {
	if s.Spares == 0 {
		return strconv.Itoa(active)
	}
	return fmt.Sprintf("%d active (%d devices minus spares=%d)", active, len(s.Devices), s.Spares)
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

// validateDevices enforces that every device path in a manual set is a
// supported style and that all paths in a set share one style.
//
// Device-mapper / multipath paths (/dev/mapper/*, /dev/dm-*) are REFUSED as
// set devices: partition naming on dm devices has no reliable convention
// (kpartx emits mpathap1, multipathd setups vary, /dev/dm-N partitions get
// unrelated dm-M nodes), so the partition-path chokepoint (internal/devices
// PartitionPath) cannot derive member partitions safely. Fail closed until
// multipath is a designed, tested feature. (FTA v2 BE-N3 / CCF-8, barrier
// NB-4.) This check also runs for SINGLE-device sets, which previously
// skipped style classification entirely.
//
// A by-uuid link of a whole disk names the filesystem ON it: partitioning
// destroys that filesystem, so the link is gone and every later boot fails
// "device not found"; it is refused. The other /dev/disk links (by-label,
// by-partuuid, by-partlabel, by-diskseq) and a path that can name no disk
// (a blank, an unclean path, a directory) fail the same way on the node and
// are warned about.
func (c *Config) validateDevices(idx int, s *Set) error {
	ref := setRef(idx, s.Name)
	var styleOf SelectorType
	for i, d := range s.Devices {
		if strings.HasPrefix(d, "/dev/mapper/") || strings.HasPrefix(d, "/dev/dm-") {
			return fmt.Errorf(
				"config_validation: sets[%d].devices: device-mapper/multipath path %q is not supported as a set device (dm partition naming has no reliable convention); use the underlying disk's by-id, by-path, or kernel name",
				idx, d)
		}
		st, err := selectorTypeOf(d)
		if err != nil {
			return fmt.Errorf("config_validation: %w", err)
		}
		if rest, ok := strings.CutPrefix(d, "/dev/disk/"); ok {
			dir, name, _ := strings.Cut(rest, "/")
			switch {
			case dir == "by-uuid":
				return fmt.Errorf(
					"config_validation: %s: device %q is a by-uuid link, which names the filesystem on the disk, not the disk: partitioning removes it, so the next boot fails \"device not found\"; use the disk's /dev/disk/by-id or /dev/disk/by-path link or its kernel name",
					ref, d)
			case dir != "by-id" && dir != "by-path":
				c.warnf("%s: device %q is a /dev/disk/%s link, which does not name the whole disk stably across partitioning and reboots; use the disk's /dev/disk/by-id or /dev/disk/by-path link or its kernel name", ref, d, dir)
			case name == "" || strings.Contains(name, "/"):
				c.warnf("%s: device %q must be /dev/disk/%s/<link name>", ref, d, dir)
			}
		}
		switch {
		case strings.ContainsFunc(d, func(r rune) bool { return r <= ' ' || r == 0x7f }):
			c.warnf("%s: device %q contains a blank or control character", ref, d)
		case path.Clean(d) != d:
			c.warnf("%s: device %q is not a clean path (write it as %q)", ref, d, path.Clean(d))
		case d == "/dev/disk":
			c.warnf("%s: device %q is a directory, not a disk", ref, d)
		}
		if i == 0 {
			styleOf = st
			continue
		}
		if st != styleOf {
			return fmt.Errorf(
				"config_validation: sets[%d].devices: all paths must use the same style (/dev/..., by-id, by-path)",
				idx)
		}
	}
	return nil
}
