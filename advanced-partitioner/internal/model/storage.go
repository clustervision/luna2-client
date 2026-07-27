package model

// DeviceKind narrows what kind of install candidate a row represents.
// Anything that is not in this enum (loop, ram, sr0, mapper internals, …)
// is filtered out by the discovery layer before reaching the TUI.
type DeviceKind string

const (
	KindTmpfs  DeviceKind = "tmpfs"
	KindNVMe   DeviceKind = "NVMe"
	KindSSD    DeviceKind = "SSD"
	KindHDD    DeviceKind = "HDD"
	KindHWRAID DeviceKind = "RAID"
	KindPMem   DeviceKind = "PMEM"
)

// StorageCandidate is a single row in the middle target panel.
// Path is the optional bay/slot/topology hint shown alongside Model.
type StorageCandidate struct {
	Name  string
	Kind  DeviceKind
	Size  string
	Model string
	Path  string
	// UID is a stable, human-readable disk identifier (serial, else
	// by-path tail, else model) shown in its own column so two otherwise
	// identical-looking rows (e.g. several "Virtual disk") stay
	// distinguishable.
	UID string
	// Set is the install set this disk belongs to in the resolved layout:
	// "OS", "DATA" (single data pool), or "DATA1"/"DATA2"/… (multiple data
	// pools), and "" when the disk is not part of the layout. RaidType is
	// that set's RAID level ("RAID1"/"RAID5"/"RAID6"/"RAID10") or "" when
	// the set is not RAID.
	Set      string
	RaidType string
	// InUse marks a device the resolved layout actually writes to (so the
	// Use column ticks every disk in a multi-disk RAID set, not just one).
	InUse bool
}

// Storage is the aggregate state for the middle panel.
// Method is a short human summary of the resolved install method
// (e.g. "RAID1 OS · UEFI" or "tmpfs OS · UEFI"), shown in the panel header.
// Summary is the one-line layout description shown under the table
// (e.g. "OS → sda,sdb (RAID1)   DATA → sdc,sdd (RAID5)   Boot: UEFI/GRUB + NVRAM").
type Storage struct {
	NVMe       int
	SSD        int
	Spindles   int
	Memory     string
	TmpfsAvail bool
	Selected   string
	Method     string
	Summary    string
	Candidates []StorageCandidate
}

// SelectTarget picks the named target. tmpfs is treated specially:
// when selected, every physical device shows ☐ - only tmpfs is ticked.
// When a physical device is selected, tmpfs shows ☐ and the device ☑.
// When nothing is selected, every row shows ☐.
func (s *Storage) SelectTarget(name string) {
	s.Selected = name
}

// IsSelected returns true if the row for cand should render with ☑.
func (s *Storage) IsSelected(cand StorageCandidate) bool {
	if s.Selected == "" {
		return false
	}
	return cand.Name == s.Selected
}
