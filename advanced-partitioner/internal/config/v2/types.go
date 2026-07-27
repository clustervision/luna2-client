// Package v2 models the storage configuration JSON (the Luna `disklayout`
// attribute) consumed by the node-side installer under the v1.4 storage and
// provisioning model.
//
// v1.4 reframes the grammar (see decisions/0003): there is no install/boot
// block (boot is UEFI only), no set-level provider block, and no safety
// wrapper. A set declares how its disks are chosen (selection + raid + count
// + spares + match, or an explicit devices list), whether it is persistent,
// and its volumes. The Bash tree at /root/NEWTREE is no longer the authority
// for this grammar; it remains the oracle only for storage mechanics
// (partition/raid/lvm/fs).
//
// What this package does NOT do:
//   - It does not resolve device paths or talk to the kernel;
//     selection=discover resolution lives in internal/diskresolve and the
//     decision engine.
//   - It does not know the install_mode; mode-specific rules (e.g. "a diskful
//     mode needs exactly one os set") live in the decision engine.
package v2

// SchemaVersion is the only supported value of Config.Version.
const SchemaVersion = 2

// Config is the top-level shape of a disklayout document.
type Config struct {
	Version int   `json:"version"`
	Sets    []Set `json:"sets"`
	// Comment is free-text operator documentation. It is carried as data
	// (round-trips through parse/marshal) and ignored by all validation and
	// decision logic. Allowed at the layout, set, and volume levels.
	Comment string `json:"comment,omitempty"`
}

// Set is one storage set: a group of disks (chosen automatically by match or
// pinned by devices) carrying a coherent group of volumes. At most one set
// has Role=="os".
type Set struct {
	Name string `json:"name"`
	Role string `json:"role"` // "os" | "data"
	// Selection is "discover" (choose disks by match + raid/count) or
	// "manual" (use the explicit Devices list).
	Selection string `json:"selection"`
	// Raid is the per-set topology: "none" | "0" | "1" | "5" | "6" | "10".
	Raid string `json:"raid"`
	// Count is the number of array members for selection=discover. Zero means
	// "use the raid level's minimum". Ignored for selection=manual (the
	// member count is len(Devices)).
	Count int `json:"count,omitempty"`
	// Spares is the number of hot-spare disks to allocate in addition to
	// Count (selection=discover only). Zero means none.
	Spares int `json:"spares,omitempty"`
	// Match is the selection=discover disk filter (tags + size range + model).
	Match *Match `json:"match,omitempty"`
	// Devices is the explicit disk list for selection=manual.
	Devices []string `json:"devices,omitempty"`
	// Save, on a selection=discover set, asks the installer to write the
	// resolved concrete layout back to the node-level disklayout as a
	// selection=manual document, pinning the node to what it discovered for
	// the next boot (see decisions/0008). Valid only on discover sets; the
	// validator rejects it on a manual set. Default false.
	Save bool `json:"save,omitempty"`
	// Persistent, when true, is a hands-off guarantee: the set is never
	// partitioned, formatted, or written, only mounted. It survives auto,
	// sync, and full; only sanitize erases it. Operator-set only.
	Persistent bool `json:"persistent,omitempty"`
	// Origin is provenance written by the installer when it pins a discovered
	// layout (Save). It is not operator input; it round-trips when the pinned
	// layout is read back on a later boot. nil on operator-authored configs.
	Origin *Origin `json:"origin,omitempty"`
	// Volumes are the mountpoints/filesystems laid down on the set.
	Volumes []Volume `json:"volumes"`
	// Comment is free-text operator documentation, carried as data and ignored
	// by all logic (see Config.Comment).
	Comment string `json:"comment,omitempty"`
}

// Origin records how a set's pinned manual layout came to be: stamped by
// lpart-storage-prepare at write-back time when a selection=discover set with
// Save=true resolves to concrete disks (decisions/0008).
type Origin struct {
	// From is the selection mode that produced the pin, always "discover".
	From string `json:"from"`
	// At is the RFC3339 timestamp of the write-back.
	At string `json:"at"`
	// By is the tool + version that wrote it, e.g. "lpart-storage-prepare 2.0-39".
	By string `json:"by"`
	// Host is the node the layout was resolved on.
	Host string `json:"host"`
}

// Match is the selection=discover disk filter. A disk qualifies when it carries
// every listed tag, falls within [MinSize, MaxSize] (empty bound = no bound),
// and — when Model is set — its model contains Model as a substring. Sizes
// are authored strings like "8T" / "400G"; they are parsed by
// internal/diskresolve.ParseMatch.
type Match struct {
	Tags    []string `json:"tags,omitempty"`
	MinSize string   `json:"min_size,omitempty"`
	MaxSize string   `json:"max_size,omitempty"`
	Model   string   `json:"model,omitempty"`
}

// Volume is one mountpoint / filesystem within a set.
//
// Provider is "partition" or "lvm" in an operator-authored config. The
// "memory" provider (tmpfs) is reserved for the decision engine's internally
// synthesized memboot RAM root and is rejected by Validate in operator input.
type Volume struct {
	Name           string `json:"name"`
	Mountpoint     string `json:"mountpoint"`
	FS             string `json:"fs"`             // vfat|xfs|ext4|swap (tmpfs is internal-only)
	Size           string `json:"size,omitempty"` // e.g. "600M", "1G", "100%"
	Provider       string `json:"provider"`       // partition|lvm (memory is internal-only)
	Options        string `json:"options,omitempty"`
	ClearUEFINVRAM bool   `json:"clear_uefi_nvram,omitempty"`
	// Comment is free-text operator documentation, carried as data and ignored
	// by all logic (see Config.Comment).
	Comment string `json:"comment,omitempty"`
}

// allowed key sets for unknown-field detection.
var (
	allowedTopLevel = []string{"version", "sets", "comment"}
	allowedSet      = []string{
		"name", "role", "selection", "raid", "count", "spares",
		"match", "devices", "save", "persistent", "origin", "volumes", "comment",
	}
	allowedMatch  = []string{"tags", "min_size", "max_size", "model"}
	allowedOrigin = []string{"from", "at", "by", "host"}
	allowedVolume = []string{
		"name", "mountpoint", "fs", "size", "provider", "options", "clear_uefi_nvram", "comment",
	}
)
