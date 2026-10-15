package main

import (
	"reflect"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

func TestBuildPinnableConfig_DiscoverBecomesManual(t *testing.T) {
	// Point the by-id lookup at an empty dir so stableDevicePath does not
	// "upgrade" /dev/sda to a wwn link on a host that has a real /dev/sda
	// (bare-metal CI runners). The pin logic under test is path-preference,
	// not by-id resolution, which has its own tests.
	orig0 := byIDDir
	byIDDir = t.TempDir()
	defer func() { byIDDir = orig0 }()

	orig := &v2.Config{
		Version: v2.SchemaVersion,
		Sets: []v2.Set{{
			Name:      "os",
			Role:      "os",
			Selection: "discover",
			Raid:      "1",
			Count:     2,
			Save:      true,
			Match:     &v2.Match{Tags: []string{"ssd"}},
			Volumes: []v2.Volume{
				{Name: "esp", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
				{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
			},
		}},
	}
	resolved := []result.ResolvedSet{{
		Name: "os",
		Role: "os",
		SelectedDisks: []selector.Resolved{
			{ConfiguredPath: "/dev/sda", ResolvedPath: "/dev/sda"},
			{ConfiguredPath: "", ResolvedPath: "/dev/sdb"},
		},
	}}

	origin := &v2.Origin{From: "discover", At: "2026-06-07T00:00:00Z", By: "lpart-storage-prepare test", Host: "node001"}
	pin := buildPinnableConfig(orig, resolved, origin)
	if pin == nil {
		t.Fatal("expected a pinnable config, got nil")
	}
	if len(pin.Sets) != 1 {
		t.Fatalf("want 1 set, got %d", len(pin.Sets))
	}
	s := pin.Sets[0]
	if s.Selection != "manual" {
		t.Errorf("selection: want manual, got %q", s.Selection)
	}
	if s.Count != 0 || s.Match != nil || s.Save {
		t.Errorf("discover-only fields must be dropped: count=%d match=%v save=%v", s.Count, s.Match, s.Save)
	}
	if s.Origin == nil || s.Origin.Host != "node001" || s.Origin.From != "discover" {
		t.Errorf("origin must be stamped on a discovered set: %+v", s.Origin)
	}
	if s.Raid != "1" || s.Role != "os" {
		t.Errorf("structural fields not preserved: raid=%q role=%q", s.Raid, s.Role)
	}
	if len(s.Devices) != 2 || s.Devices[0] != "/dev/sda" || s.Devices[1] != "/dev/sdb" {
		t.Errorf("devices: want [/dev/sda /dev/sdb] (ConfiguredPath then ResolvedPath fallback), got %v", s.Devices)
	}
	if len(s.Volumes) != 2 {
		t.Errorf("volumes must be reproduced verbatim, got %d", len(s.Volumes))
	}
}

// A layout that mixes a discovered+saved os set with an operator-authored
// manual data set: both are rendered manual in the pinnable doc, but origin is
// stamped only on the set the installer discovered.
func TestBuildPinnableConfig_MultiSetStampsOnlyDiscovered(t *testing.T) {
	orig := &v2.Config{
		Version: v2.SchemaVersion,
		Sets: []v2.Set{
			{
				Name: "os", Role: "os", Selection: "discover", Raid: "1", Count: 2, Save: true,
				Match:   &v2.Match{Tags: []string{"ssd"}},
				Volumes: []v2.Volume{{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"}},
			},
			{
				Name: "data", Role: "data", Selection: "manual", Raid: "0",
				Devices: []string{"/dev/sdc", "/dev/sdd"},
				Volumes: []v2.Volume{{Name: "d", Mountpoint: "/d", FS: "xfs", Provider: "lvm", Size: "100%"}},
			},
		},
	}
	resolved := []result.ResolvedSet{
		{Name: "os", Role: "os", SelectedDisks: []selector.Resolved{
			{ResolvedPath: "/dev/sda"}, {ResolvedPath: "/dev/sdb"}}},
		{Name: "data", Role: "data", SelectedDisks: []selector.Resolved{
			{ResolvedPath: "/dev/sdc"}, {ResolvedPath: "/dev/sdd"}}},
	}
	origin := &v2.Origin{From: "discover", At: "t", By: "test", Host: "node001"}
	pin := buildPinnableConfig(orig, resolved, origin)
	if pin == nil || len(pin.Sets) != 2 {
		t.Fatalf("want 2 pinned sets, got %v", pin)
	}
	byName := map[string]v2.Set{}
	for _, s := range pin.Sets {
		byName[s.Name] = s
		if s.Selection != "manual" {
			t.Errorf("set %q must be rendered manual, got %q", s.Name, s.Selection)
		}
	}
	if byName["os"].Origin == nil {
		t.Errorf("discovered os set must carry origin")
	}
	if byName["data"].Origin != nil {
		t.Errorf("operator-authored manual data set must NOT carry origin: %+v", byName["data"].Origin)
	}
}

func TestAnySetSaves(t *testing.T) {
	disc := func(save bool) v2.Set { return v2.Set{Name: "os", Selection: "discover", Save: save} }
	if anySetSaves(nil) {
		t.Error("nil config: want false")
	}
	if anySetSaves(&v2.Config{Sets: []v2.Set{disc(false)}}) {
		t.Error("discover without save: want false")
	}
	if !anySetSaves(&v2.Config{Sets: []v2.Set{disc(true)}}) {
		t.Error("discover with save: want true")
	}
	// save on a manual set never triggers (validate rejects it; defensive here).
	if anySetSaves(&v2.Config{Sets: []v2.Set{{Name: "x", Selection: "manual", Save: true}}}) {
		t.Error("manual+save must not trigger write-back")
	}
}

func TestBuildPinnableConfig_SkipsDisklessSets(t *testing.T) {
	orig := &v2.Config{Version: v2.SchemaVersion, Sets: []v2.Set{{Name: "os", Role: "os"}}}
	// memboot-style resolution: a record with no selected disks.
	if got := buildPinnableConfig(orig, []result.ResolvedSet{{Name: "os"}}, nil); got != nil {
		t.Errorf("diskless resolution must yield nil (nothing to pin), got %+v", got)
	}
	if got := buildPinnableConfig(nil, nil, nil); got != nil {
		t.Errorf("nil config must yield nil, got %+v", got)
	}
}

// The operator's bootloader choice is operator intent, like persistent: a
// save:true install must pin it alongside the devices, or the next boot of a
// pinned node silently reverts to the default boot order.
func TestBuildPinnableConfig_CarriesBootloader(t *testing.T) {
	orig0 := byIDDir
	byIDDir = t.TempDir()
	defer func() { byIDDir = orig0 }()

	orig := &v2.Config{
		Version: v2.SchemaVersion,
		Sets: []v2.Set{{
			Name: "os", Role: "os", Selection: "discover", Raid: "none",
			Save:       true,
			Bootloader: v2.BootloaderFirst,
			Volumes: []v2.Volume{
				{Name: "esp", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
				{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
			},
		}},
	}
	resolved := []result.ResolvedSet{{
		Name: "os", Role: "os",
		SelectedDisks: []selector.Resolved{{ConfiguredPath: "/dev/sda", ResolvedPath: "/dev/sda"}},
	}}
	pin := buildPinnableConfig(orig, resolved, nil)
	if pin == nil {
		t.Fatal("expected a pinnable config, got nil")
	}
	if pin.Sets[0].Bootloader != v2.BootloaderFirst {
		t.Fatalf("bootloader = %q, want first", pin.Sets[0].Bootloader)
	}
}

// A discovered set with hot spares pins its spare count: the device list is
// members followed by spares, and dropping the count would make the next
// create turn the spares into active members.
func TestBuildPinnableConfig_KeepsSpares(t *testing.T) {
	orig0 := byIDDir
	byIDDir = t.TempDir()
	defer func() { byIDDir = orig0 }()
	orig := &v2.Config{Version: v2.SchemaVersion, Sets: []v2.Set{{
		Name: "data", Role: "data", Selection: "discover", Raid: "5", Count: 4, Spares: 1,
		Volumes: []v2.Volume{{Name: "d", Mountpoint: "/data", FS: "xfs", Size: "100%", Provider: "lvm"}},
	}}}
	var disks []selector.Resolved
	for _, d := range []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde"} {
		disks = append(disks, selector.Resolved{ConfiguredPath: d, ResolvedPath: d})
	}
	pin := buildPinnableConfig(orig, []result.ResolvedSet{{Name: "data", Role: "data", SelectedDisks: disks}}, nil)
	if pin == nil || len(pin.Sets) != 1 {
		t.Fatalf("want one pinned set, got %+v", pin)
	}
	if s := pin.Sets[0]; s.Spares != 1 || len(s.Devices) != 5 {
		t.Fatalf("pinned set must keep spares=1 and all 5 devices, got spares=%d devices=%v", s.Spares, s.Devices)
	}
}

// Every per-set key the operator wrote survives the pin unless it is listed
// here as deliberately rewritten or dropped: a key added to v2.Set and missed
// in buildPinnableConfig would otherwise vanish from a save:true node silently.
func TestBuildPinnableConfig_CarriesEveryOperatorKey(t *testing.T) {
	orig0 := byIDDir
	byIDDir = t.TempDir()
	defer func() { byIDDir = orig0 }()

	notCarried := map[string]bool{
		"Selection": true, "Count": true, "Match": true, "Save": true, "IncludeSAN": true, // discover-only
		"Devices": true, // taken from the resolution
		"Origin":  true, // stamped by the installer
		"Comment": true, // not carried today
	}
	src := v2.Set{Selection: "discover"}
	sv := reflect.ValueOf(&src).Elem()
	for i := 0; i < sv.NumField(); i++ {
		f := sv.Field(i)
		if !f.IsZero() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("x-" + sv.Type().Field(i).Name)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Int:
			f.SetInt(1)
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
		default:
			t.Fatalf("field %s: kind %s has no test value", sv.Type().Field(i).Name, f.Kind())
		}
	}
	orig := &v2.Config{Version: v2.SchemaVersion, Sets: []v2.Set{src}}
	resolved := []result.ResolvedSet{{
		Name:          src.Name,
		SelectedDisks: []selector.Resolved{{ConfiguredPath: "/dev/sda", ResolvedPath: "/dev/sda"}},
	}}
	pin := buildPinnableConfig(orig, resolved, nil)
	if pin == nil || len(pin.Sets) != 1 {
		t.Fatalf("expected one pinned set, got %+v", pin)
	}
	pv := reflect.ValueOf(pin.Sets[0])
	for i := 0; i < sv.NumField(); i++ {
		name := sv.Type().Field(i).Name
		if notCarried[name] {
			continue
		}
		if !reflect.DeepEqual(pv.Field(i).Interface(), sv.Field(i).Interface()) {
			t.Errorf("%s not carried into the pinned set: got %v, want %v", name, pv.Field(i).Interface(), sv.Field(i).Interface())
		}
	}
}
