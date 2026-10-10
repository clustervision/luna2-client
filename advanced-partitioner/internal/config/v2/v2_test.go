package v2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidCorpus parses and validates every JSON under testdata/valid/.
// These double as worked examples of the v1.4 disklayout grammar.
func TestValidCorpus(t *testing.T) {
	dir := "testdata/valid"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			cfg, raw, err := LoadFile(path)
			if err != nil {
				t.Fatalf("LoadFile: %v", err)
			}
			if err := cfg.ValidateRaw(raw); err != nil {
				t.Fatalf("ValidateRaw: %v", err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate (round-trip): %v", err)
			}
		})
	}
}

// TestInvalidCases drives inline invalid configs and asserts validation
// fails with the expected "config_validation:" message substring. Inline
// JSON keeps each rule's fixture next to its assertion.
func TestInvalidCases(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			"unknown-top-level",
			`{"version":2,"extra_field":1,"sets":[]}`,
			"top-level: unknown field 'extra_field'",
		},
		{
			"version-wrong",
			`{"version":1,"sets":[]}`,
			"version must be 2, got: 1",
		},
		{
			"sets-empty",
			`{"version":2,"sets":[]}`,
			"sets[] must be non-empty",
		},
		{
			"sets-not-array",
			`{"version":2,"sets":{}}`,
			"sets must be an array",
		},
		{
			"role-os-duplicate",
			`{"version":2,"sets":[` + osSet("os") + `,` + osSet("os2") + `]}`,
			"at most one set may have role=os, found: 2",
		},
		{
			"set-unknown-field",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none","stray":1,"volumes":[]}]}`,
			"sets[0]: unknown field 'stray'",
		},
		{
			"role-bad",
			`{"version":2,"sets":[{"name":"x","role":"scratch","selection":"discover","raid":"none","volumes":[]}]}`,
			"role unsupported: 'scratch'",
		},
		{
			"selection-bad",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"pick","raid":"none","volumes":[]}]}`,
			"selection unsupported: 'pick'",
		},
		{
			"selection-missing",
			`{"version":2,"sets":[{"name":"x","role":"data","raid":"none","volumes":[]}]}`,
			"selection is required",
		},
		{
			"raid-missing",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"discover","volumes":[]}]}`,
			"raid is required",
		},
		{
			"raid-bad",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"discover","raid":"7","volumes":[]}]}`,
			"raid unsupported raid \"7\"",
		},
		{
			"manual-no-devices",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","volumes":[` + dataVol() + `]}]}`,
			"devices[] is required for selection=manual",
		},
		{
			"manual-count-mismatch-raid1",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"1","devices":["/dev/sda","/dev/sdb","/dev/sdc"],"volumes":[` + dataVol() + `]}]}`,
			"raid=1 needs exactly 2 device(s), but devices[] lists 3",
		},
		{
			"auto-count-mismatch-raid5",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"discover","raid":"5","count":2,"volumes":[` + dataVol() + `]}]}`,
			"raid=5 needs at least 4 device(s), but count=2",
		},
		{
			"os-raid5-refused",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"manual","raid":"5","devices":["/dev/sda","/dev/sdb","/dev/sdc","/dev/sdd"],"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			"raid=5 is not supported for the OS set",
		},
		{
			"os-raid6-refused",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"6","count":5,"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			"raid=6 is not supported for the OS set",
		},
		{
			"os-3-drives-refused",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"manual","raid":"0","devices":["/dev/sda","/dev/sdb","/dev/sdc"],"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			"the OS set uses exactly 1, 2 or 4 drives",
		},
		{
			"os-no-esp",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none","volumes":[{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			"must declare a /boot/efi vfat partition",
		},
		{
			"os-root-missing",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none","volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"}]}]}`,
			"must have exactly one root volume",
		},
		{
			"volume-fs-bad",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"/d","fs":"btrfs","provider":"lvm","size":"100%"}]}]}`,
			"fs unsupported: 'btrfs'",
		},
		{
			"volume-fs-tmpfs",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"/d","fs":"tmpfs","provider":"lvm","size":"100%"}]}]}`,
			"fs=tmpfs is reserved",
		},
		{
			"volume-provider-memory",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"/d","fs":"xfs","provider":"memory","size":"100%"}]}]}`,
			"provider=memory is reserved",
		},
		{
			"volume-provider-zfs",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"/d","fs":"xfs","provider":"zfs","size":"100%"}]}]}`,
			"provider=zfs is reserved",
		},
		{
			"volume-provider-missing",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"/d","fs":"xfs","size":"100%"}]}]}`,
			"provider is required",
		},
		{
			"volume-mountpoint-bad",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"data","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			"mountpoint must be an absolute path or swap",
		},
		{
			"volume-partition-no-size",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"/d","fs":"xfs","provider":"partition"}]}]}`,
			"size is required for provider=partition",
		},
		{
			"volume-raid-deprecated",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"d","mountpoint":"/d","fs":"xfs","provider":"lvm","size":"100%","raid":"1"}]}]}`,
			"volumes[0]: unknown field 'raid'",
		},
		{
			"partition-raid-mixed",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"1","devices":["/dev/sda","/dev/sdb"],"volumes":[{"name":"d","mountpoint":"/d","fs":"xfs","provider":"partition","size":"100%"}]}]}`,
			"direct partition volumes require raid=none",
		},
		{
			"lvm-double-100pct",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"volumes":[{"name":"a","mountpoint":"/a","fs":"xfs","provider":"lvm","size":"100%"},{"name":"b","mountpoint":"/b","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			"at most one lvm volume per set may use size=100%",
		},
		{
			"devices-mixed-styles",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"1","devices":["/dev/sda","/dev/disk/by-path/pci-x"],"volumes":[` + dataVol() + `]}]}`,
			"all paths must use the same style",
		},
		{
			"devices-bad-path",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"1","devices":["sda","sdb"],"volumes":[` + dataVol() + `]}]}`,
			"device path must start with /dev/",
		},
		{
			"match-unknown-field",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"discover","raid":"none","match":{"speed":"fast"},"volumes":[` + dataVol() + `]}]}`,
			"match: unknown field 'speed'",
		},
		{
			"save-on-manual",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"save":true,"volumes":[` + dataVol() + `]}]}`,
			"save is only valid on a selection=discover set",
		},
		{
			"origin-unknown-field",
			`{"version":2,"sets":[{"name":"x","role":"data","selection":"manual","raid":"none","devices":["/dev/sda"],"origin":{"bogus":"x"},"volumes":[` + dataVol() + `]}]}`,
			"origin: unknown field 'bogus'",
		},
		{
			"bootloader-bad-value",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none","bootloader":"lilo","volumes":[` + osVols() + `]}]}`,
			"bootloader unsupported: 'lilo'",
		},
		{
			"bootloader-empty",
			`{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none","bootloader":"","volumes":[` + osVols() + `]}]}`,
			"bootloader unsupported: ''",
		},
		{
			"bootloader-on-data-set",
			`{"version":2,"sets":[{"name":"d","role":"data","selection":"manual","raid":"none","devices":["/dev/sdc"],"bootloader":"none","volumes":[` + dataVol() + `]}]}`,
			"bootloader is valid on the os set only",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.json))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			err = cfg.ValidateRaw([]byte(c.json))
			if err == nil {
				t.Fatalf("Validate accepted %s; expected substring %q", c.name, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s:\n  got:  %s\n  want substring: %s", c.name, err.Error(), c.want)
			}
			if !strings.HasPrefix(err.Error(), "config_validation: ") {
				t.Fatalf("error string missing required prefix: %s", err.Error())
			}
		})
	}
}

func TestSaveAcceptedOnDiscoverSet(t *testing.T) {
	j := `{"version":2,"sets":[{"name":"x","role":"data","selection":"discover","raid":"none","save":true,"volumes":[` + dataVol() + `]}]}`
	cfg, err := Parse([]byte(j))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.ValidateRaw([]byte(j)); err != nil {
		t.Fatalf("save on a discover set must validate: %v", err)
	}
	if !cfg.Sets[0].Save {
		t.Errorf("Save field not decoded")
	}
}

func TestOriginRoundTrips(t *testing.T) {
	// A pinned layout the installer wrote (selection=manual + origin) must
	// validate and decode unchanged when read back on a later boot.
	j := `{"version":2,"sets":[{"name":"os","role":"os","selection":"manual","raid":"1",` +
		`"devices":["/dev/sda","/dev/sdb"],` +
		`"origin":{"from":"discover","at":"2026-06-07T00:00:00Z","by":"lpart-storage-prepare 2.0-39","host":"node001"},` +
		`"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},` +
		`{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]}]}`
	cfg, err := Parse([]byte(j))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.ValidateRaw([]byte(j)); err != nil {
		t.Fatalf("pinned layout with origin must validate: %v", err)
	}
	o := cfg.Sets[0].Origin
	if o == nil {
		t.Fatal("origin not decoded")
	}
	if o.From != "discover" || o.Host != "node001" || o.By != "lpart-storage-prepare 2.0-39" || o.At != "2026-06-07T00:00:00Z" {
		t.Errorf("origin fields not round-tripped: %+v", o)
	}
}

// TestCommentRoundTrips: a comment at the layout, set, and volume levels
// validates, decodes into the typed struct, and survives a re-marshal (persists
// as data). It is ignored by validation — no comment value can make a layout
// valid or invalid.
func TestCommentRoundTrips(t *testing.T) {
	j := `{"version":2,"comment":"layout note","sets":[{"name":"os","role":"os",` +
		`"selection":"discover","raid":"none","comment":"set note",` +
		`"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},` +
		`{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%","comment":"vol note"}]}]}`
	cfg, err := Parse([]byte(j))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.ValidateRaw([]byte(j)); err != nil {
		t.Fatalf("commented layout must validate: %v", err)
	}
	if cfg.Comment != "layout note" || cfg.Sets[0].Comment != "set note" || cfg.Sets[0].Volumes[1].Comment != "vol note" {
		t.Errorf("comments not decoded: %q / %q / %q", cfg.Comment, cfg.Sets[0].Comment, cfg.Sets[0].Volumes[1].Comment)
	}
	// Persists through a re-marshal (Validate re-encodes the typed struct).
	if err := cfg.Validate(); err != nil {
		t.Fatalf("re-marshal validate: %v", err)
	}
	remarshaled, _ := json.Marshal(cfg)
	for _, want := range []string{"layout note", "set note", "vol note"} {
		if !strings.Contains(string(remarshaled), want) {
			t.Errorf("comment %q dropped on re-marshal: %s", want, remarshaled)
		}
	}
}

// osSet renders a minimal valid os set with the given name (discover, raid none).
func osSet(name string) string {
	return `{"name":"` + name + `","role":"os","selection":"discover","raid":"none","volumes":[` + osVols() + `]}`
}

// osVols renders the ESP + root pair every valid os set needs.
func osVols() string {
	return `{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},` +
		`{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}`
}

// TestBootloaderSpellings pins the accepted spellings of the os set's
// `bootloader` key and what each one means for the two accessors the installer
// reads: where the disk sits in the UEFI boot order, and whether a bootloader
// is installed at all.
func TestBootloaderSpellings(t *testing.T) {
	cases := []struct {
		json      string // the raw value, or "" for an absent key
		wantOrder string
		wantBoot  string
	}{
		{"", "last", "grub"},
		{`"first"`, "first", "grub"},
		{`"grub"`, "first", "grub"},
		{`"yes"`, "first", "grub"},
		{`"true"`, "first", "grub"},
		{`true`, "first", "grub"},
		{`"last"`, "last", "grub"},
		{`"none"`, "last", "none"},
		{`"no"`, "last", "none"},
		{`"false"`, "last", "none"},
		{`false`, "last", "none"},
	}
	for _, c := range cases {
		name := c.json
		if name == "" {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			key := ""
			if c.json != "" {
				key = `"bootloader":` + c.json + `,`
			}
			j := `{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none",` +
				key + `"volumes":[` + osVols() + `]}]}`
			cfg, err := Parse([]byte(j))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if err := cfg.ValidateRaw([]byte(j)); err != nil {
				t.Fatalf("ValidateRaw: %v", err)
			}
			s := cfg.OSSet()
			if got := s.BootOrderOrLast(); got != c.wantOrder {
				t.Errorf("BootOrderOrLast() = %q, want %q", got, c.wantOrder)
			}
			if got := s.BootMethodOrNone(); got != c.wantBoot {
				t.Errorf("BootMethodOrNone() = %q, want %q", got, c.wantBoot)
			}
		})
	}
}

// A memboot RAM root installs no bootloader whatever the key says: the guard
// that keeps memboot out of the new field's reach.
func TestBootloaderMemorySetStaysNone(t *testing.T) {
	s := Set{Role: "os", Bootloader: BootloaderFirst, Volumes: []Volume{
		{Name: "root", Mountpoint: "/", FS: "tmpfs", Provider: "memory"},
	}}
	if got := s.BootMethodOrNone(); got != "none" {
		t.Fatalf("BootMethodOrNone() = %q, want none for a memory set", got)
	}
}

// TestBootloaderRoundTrips: the operator's key survives a re-marshal in its
// normalised spelling (the pinned layout a save:true install writes back must
// still carry it), and a layout without the key marshals without it, so
// existing stored layouts are untouched.
func TestBootloaderRoundTrips(t *testing.T) {
	j := `{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none",` +
		`"bootloader":"yes","volumes":[` + osVols() + `]}]}`
	cfg, err := Parse([]byte(j))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("re-marshal validate: %v", err)
	}
	out, _ := json.Marshal(cfg)
	if !strings.Contains(string(out), `"bootloader":"first"`) {
		t.Errorf("bootloader dropped or not normalised on re-marshal: %s", out)
	}

	plain := `{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none",` +
		`"volumes":[` + osVols() + `]}]}`
	cfg, err = Parse([]byte(plain))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, _ = json.Marshal(cfg)
	if strings.Contains(string(out), "bootloader") {
		t.Errorf("a layout without the key must not gain one: %s", out)
	}
}

// The accepted spellings are exactly the lowercase words the daemon's
// store-time validator accepts. Case-folding here would make a layout that
// installs on a node but cannot be saved on the controller, so a capitalised
// value is rejected on both sides.
func TestBootloaderIsCaseSensitive(t *testing.T) {
	for _, bad := range []string{"None", "First", "LAST", "Yes", "TRUE"} {
		j := `{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none",` +
			`"bootloader":"` + bad + `","volumes":[` + osVols() + `]}]}`
		cfg, err := Parse([]byte(j))
		if err != nil {
			continue // a decode refusal is just as good
		}
		if err := cfg.ValidateRaw([]byte(j)); err == nil {
			t.Errorf("bootloader:%q must be rejected; the daemon rejects it too", bad)
		}
	}
}

// A value of the wrong JSON type is a decode error, not a silent default: a
// number or an object cannot be a spelling of anything.
func TestBootloaderWrongTypeRejected(t *testing.T) {
	for _, bad := range []string{`1`, `{}`, `[]`} {
		j := `{"version":2,"sets":[{"name":"os","role":"os","selection":"discover","raid":"none",` +
			`"bootloader":` + bad + `,"volumes":[` + osVols() + `]}]}`
		if _, err := Parse([]byte(j)); err == nil {
			t.Errorf("bootloader:%s must not parse", bad)
		}
	}
}

// dataVol renders one generic lvm data volume.
func dataVol() string {
	return `{"name":"d","mountpoint":"/d","fs":"xfs","provider":"lvm","size":"100%"}`
}

func TestSelectorTypeOf(t *testing.T) {
	cases := []struct {
		in   string
		want SelectorType
		fail bool
	}{
		{"/dev/sda", SelectorDevicePaths, false},
		{"/dev/nvme0n1", SelectorDevicePaths, false},
		{"/dev/disk/by-path/pci-0000:00:1f.2-ata-1.0", SelectorPhysical, false},
		{"/dev/disk/by-id/ata-Crucial_CT500MX500SSD1_x", SelectorByID, false},
		{"/dev/disk/by-uuid/abcd-efgh", SelectorByUUID, false},
		{"sda", "", true},
		{"/proc/cpuinfo", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := SelectorTypeOf(tc.in)
			if tc.fail {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("SelectorTypeOf(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAccessors covers the convenience methods on Set / Config.
func TestAccessors(t *testing.T) {
	cfg, raw, err := LoadFile("testdata/valid/os-raid1-plus-data-raid0.json")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if err := cfg.ValidateRaw(raw); err != nil {
		t.Fatalf("ValidateRaw: %v", err)
	}

	// OS set is first in JSON and first in ExecutionOrder.
	if got := cfg.ExecutionOrder(); len(got) != 2 || cfg.Sets[got[0]].Role != "os" {
		t.Fatalf("ExecutionOrder did not put OS first: %v", got)
	}
	if cfg.OSSet() == nil || cfg.OSSet().Name != "os" {
		t.Fatalf("OSSet wrong: %+v", cfg.OSSet())
	}
	// UEFI-only constants.
	if cfg.BootMethod() != "grub" || cfg.BootFirmware() != "uefi" || cfg.RegisterNVRAM() != "auto" {
		t.Fatalf("boot accessors wrong: %s/%s/%s", cfg.BootMethod(), cfg.BootFirmware(), cfg.RegisterNVRAM())
	}

	osSet := cfg.OSSet()
	if osSet.RAIDMode() != "raid1" {
		t.Fatalf("os RAIDMode = %s", osSet.RAIDMode())
	}
	if osSet.VGName() != "os" {
		t.Fatalf("os VGName = %s", osSet.VGName())
	}
	if osSet.StorageType() != StorageLVM {
		t.Fatalf("os StorageType = %s", osSet.StorageType())
	}
	if osSet.MemberCount() != 2 {
		t.Fatalf("raid1 MemberCount = %d, want 2", osSet.MemberCount())
	}
	if osSet.BootEFISize() != "600M" || osSet.BootSize() != "1G" || osSet.BootFS() != "xfs" {
		t.Fatalf("boot accessors wrong")
	}

	scratch := &cfg.Sets[1]
	if scratch.RAIDMode() != "raid0" || scratch.MatchFilter().Tags[0] != "nvme" {
		t.Fatalf("scratch accessors wrong: %s tags=%v", scratch.RAIDMode(), scratch.MatchFilter().Tags)
	}
}

// TestPersistentAndManualAccessors covers the persistent flag and manual
// member-count derivation.
func TestPersistentAndManualAccessors(t *testing.T) {
	cfg, raw, err := LoadFile("testdata/valid/data-manual-persistent.json")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if err := cfg.ValidateRaw(raw); err != nil {
		t.Fatalf("ValidateRaw: %v", err)
	}
	s := &cfg.Sets[0]
	if !s.Persistent {
		t.Fatal("expected persistent=true")
	}
	if s.MemberCount() != 1 {
		t.Fatalf("manual raid none MemberCount = %d, want 1", s.MemberCount())
	}
	if cfg.OSSet() != nil {
		t.Fatal("data-only config should have no OS set")
	}
}

func TestUnknownKeySortDeterministic(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"sets": [` + osSet("os") + `],
		"zeta": 1,
		"alpha": 2
	}`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = cfg.ValidateRaw(raw)
	if err == nil {
		t.Fatalf("expected validation failure")
	}
	if !strings.Contains(err.Error(), "unknown field 'alpha'") {
		t.Fatalf("expected lexically-smallest key first; got: %s", err.Error())
	}
}

func TestMDNameAndMirrorsBoot(t *testing.T) {
	cases := []struct{ name, in, wantMD string }{
		{"plain", "os", "os"},
		{"hyphen", "os-1", "os_1"},
		{"trailing", "data_", "data"},
	}
	for _, c := range cases {
		if got := (&Set{Name: c.in}).MDName(); got != c.wantMD {
			t.Errorf("%s: MDName(%q)=%q want %q", c.name, c.in, got, c.wantMD)
		}
	}
	// Same rule as the partition builder: every redundant os raid mode mirrors
	// the boot chain (FTA C2); raid0, none and data sets do not.
	for _, r := range []string{"1", "5", "6", "10"} {
		if !(&Set{Role: "os", Raid: r}).MirrorsBoot() {
			t.Errorf("os+raid%s must mirror boot (the builder mirrors it)", r)
		}
	}
	for _, s := range []*Set{{Role: "os", Raid: "0"}, {Role: "data", Raid: "1"}, {Role: "data", Raid: "10"}, {Role: "os", Raid: "none"}} {
		if s.MirrorsBoot() {
			t.Errorf("role=%s raid=%s must NOT mirror boot", s.Role, s.Raid)
		}
	}
}
