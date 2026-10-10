package v2

// Table-driven tests for the build-feasibility rules: every layout the
// partition/raid/lvm/fs builders cannot build must be refused at validation
// (exit 3, before any disk is touched), and a nearby buildable layout must
// still pass. A case with want == "" must validate.

import (
	"strconv"
	"strings"
	"testing"
)

type ruleCase struct {
	name string
	json string
	want string // substring of the refusal; "" = must validate
}

func runRuleCases(t *testing.T, cases []ruleCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.json))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			err = cfg.ValidateRaw([]byte(c.json))
			if c.want == "" {
				if err != nil {
					t.Fatalf("must validate, got: %v", err)
				}
				// The typed round trip (Validate re-encodes) must agree.
				if err := cfg.Validate(); err != nil {
					t.Fatalf("must validate after re-encode, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted; want refusal containing %q", c.want)
			}
			if !strings.HasPrefix(err.Error(), "config_validation: ") {
				t.Fatalf("missing config_validation prefix: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("\n  got:  %v\n  want substring: %s", err, c.want)
			}
		})
	}
}

// lay renders a layout document from set objects.
func lay(sets ...string) string {
	return `{"version":2,"sets":[` + strings.Join(sets, ",") + `]}`
}

// set renders a set object from its fields (without braces) and volumes.
func set(fields string, vols ...string) string {
	return `{` + fields + `,"volumes":[` + strings.Join(vols, ",") + `]}`
}

// vol renders a volume; an empty size is omitted.
func vol(name, mountpoint, fs, provider, size string) string {
	s := `{"name":"` + name + `","mountpoint":"` + mountpoint + `","fs":"` + fs + `","provider":"` + provider + `"`
	if size != "" {
		s += `,"size":"` + size + `"`
	}
	return s + `}`
}

// osFields: a discover raid-none os set named name.
func osFields(name string) string {
	return `"name":"` + name + `","role":"os","selection":"discover","raid":"none"`
}

// osManual: a manual os set on the given devices.
func osManual(name, raid string, devs ...string) string {
	return `"name":"` + name + `","role":"os","selection":"manual","raid":"` + raid + `","devices":["` + strings.Join(devs, `","`) + `"]`
}

// dataManual: a manual data set on the given devices.
func dataManual(name, raid string, devs ...string) string {
	return `"name":"` + name + `","role":"data","selection":"manual","raid":"` + raid + `","devices":["` + strings.Join(devs, `","`) + `"]`
}

func espVol() string  { return vol("uefi", "/boot/efi", "vfat", "partition", "600M") }
func bootVol() string { return vol("boot", "/boot", "xfs", "partition", "1G") }
func rootLV() string  { return vol("root", "/", "xfs", "lvm", "100%") }

// osLayout: a valid single-disk os set with an lvm root (plus extra sets).
func osLayout(extra ...string) string {
	return lay(append([]string{set(osFields("os"), espVol(), bootVol(), rootLV())}, extra...)...)
}

func TestRuleSetAndVolumeNames(t *testing.T) {
	dataNamed := func(name string) string {
		return osLayout(set(dataManual(name, "none", "/dev/sdb"), vol("d", "/d", "xfs", "lvm", "100%")))
	}
	dataVolNamed := func(name, provider string) string {
		return osLayout(set(dataManual("data", "none", "/dev/sdb"), vol(name, "/d", "xfs", provider, "100%")))
	}
	runRuleCases(t, []ruleCase{
		// set names: characters
		{"set-name-space", dataNamed("my data"), `(set "my data"): name may only contain letters, digits, '_' and '-'`},
		{"set-name-dot", dataNamed("data.1"), `(set "data.1"): name may only contain`},
		{"set-name-slash", dataNamed("a/b"), `(set "a/b"): name may only contain`},
		{"set-name-leading-hyphen", dataNamed("-data"), `(set "-data"): name must not start with '-'`},
		{"set-name-folds-to-nothing", dataNamed("__"), `(set "__"): name must contain a letter or digit`},
		// set names: /dev entries cleanup would rm -rf
		{"set-name-disk", dataNamed("disk"), `(set "disk"): name is reserved: /dev/disk exists`},
		{"set-name-md", dataNamed("md"), `name is reserved: /dev/md exists`},
		{"set-name-mapper", dataNamed("mapper"), `name is reserved: /dev/mapper exists`},
		{"set-name-lvm", dataNamed("lvm"), `name is reserved`},
		{"set-name-shm", dataNamed("shm"), `name is reserved`},
		{"set-name-log", dataNamed("log"), `name is reserved: /dev/log exists`},
		{"set-name-sda", dataNamed("sda"), `name is reserved: /dev/sda exists`},
		{"set-name-nvme0n1", dataNamed("nvme0n1"), `name is reserved`},
		{"set-name-md0", dataNamed("md0"), `name is reserved`},
		{"set-name-ok-underscore", dataNamed("data_1"), ""},
		{"set-name-ok-hyphen", dataNamed("data-1"), ""},
		{"set-name-ok-nvme", dataNamed("nvme"), ""},
		{"set-name-ok-hdd", dataNamed("hdd"), ""},
		{"set-name-ok-sdata", dataNamed("sdata"), ""},
		// set names: collisions between sets
		{"set-name-duplicate", osLayout(
			set(dataManual("data", "none", "/dev/sdb"), vol("a", "/a", "xfs", "lvm", "100%")),
			set(dataManual("data", "none", "/dev/sdc"), vol("b", "/b", "xfs", "lvm", "100%"))),
			`sets[1] and sets[2] are both named "data"`},
		{"set-name-md-fold-collision", osLayout(
			set(dataManual("data-1", "none", "/dev/sdb"), vol("a", "/a", "xfs", "lvm", "100%")),
			set(dataManual("data_1", "none", "/dev/sdc"), vol("b", "/b", "xfs", "lvm", "100%"))),
			`set names "data-1" (sets[1]) and "data_1" (sets[2]) both map to the md array /dev/md/data_1`},
		{"set-name-md-trailing-underscore-collision", osLayout(
			set(dataManual("data", "none", "/dev/sdb"), vol("a", "/a", "xfs", "lvm", "100%")),
			set(dataManual("data_", "none", "/dev/sdc"), vol("b", "/b", "xfs", "lvm", "100%"))),
			`both map to the md array /dev/md/data`},
		{"set-name-dm-prefix-clash", osLayout(
			set(dataManual("os-a", "none", "/dev/sdb"), vol("x", "/x", "xfs", "lvm", "100%"))),
			`cleanup of set "os" removes every /dev/mapper/os-* entry, which includes /dev/mapper/os--a-x (volume "x" of set "os-a")`},
		{"set-name-dm-prefix-clash-via-lv-name", lay(
			set(osFields("a"), espVol(), vol("b-x", "/", "xfs", "lvm", "100%")),
			set(dataManual("a-b", "none", "/dev/sdb"), vol("d", "/d", "xfs", "partition", "100%"))),
			`cleanup of set "a-b" removes every /dev/mapper/a-b-* entry, which includes /dev/mapper/a-b--x`},
		{"set-name-ok-no-dm-clash", osLayout(
			set(dataManual("os_a", "none", "/dev/sdb"), vol("x", "/x", "xfs", "lvm", "100%"))), ""},
		{"set-name-ok-prefix-without-lvm", osLayout(
			set(dataManual("os-a", "none", "/dev/sdb"), vol("x", "/x", "xfs", "partition", "100%"))), ""},
		// volume names
		{"vol-name-slash", dataVolNamed("a/b", "lvm"), `(set "data", volume "a/b"): name may only contain`},
		{"vol-name-space", dataVolNamed("my vol", "partition"), `name may only contain`},
		{"vol-name-leading-hyphen", dataVolNamed("-d", "lvm"), `(set "data", volume "-d"): name must not start with '-'`},
		{"lv-name-leading-underscore", dataVolNamed("_d", "lvm"), `an lvm volume name must not start with '_'`},
		{"lv-name-snapshot", dataVolNamed("snapshot", "lvm"), `starting with "snapshot" are reserved by LVM`},
		{"lv-name-snapshots", dataVolNamed("snapshots", "lvm"), `starting with "snapshot" are reserved by LVM`},
		{"lv-name-pvmove", dataVolNamed("pvmove", "lvm"), `starting with "pvmove" are reserved by LVM`},
		{"lv-name-tmeta", dataVolNamed("pool_tmeta", "lvm"), `containing "_tmeta" are reserved by LVM`},
		{"lv-name-mlog", dataVolNamed("d_mlog", "lvm"), `containing "_mlog" are reserved`},
		{"lv-name-rimage", dataVolNamed("d_rimage_0", "lvm"), `containing "_rimage" are reserved`},
		{"lv-name-ok", dataVolNamed("d_1-x", "lvm"), ""},
		{"partition-name-snapshot-ok", dataVolNamed("snapshot", "partition"), ""},
		{"vol-name-duplicate-in-set", osLayout(set(dataManual("data", "none", "/dev/sdb"),
			vol("d", "/a", "xfs", "lvm", "10G"), vol("d", "/b", "xfs", "lvm", "100%"))),
			`(set "data"): volume name "d" is used by volumes[0] and volumes[1]`},
		{"vol-name-same-in-two-sets-ok", osLayout(set(dataManual("data", "none", "/dev/sdb"),
			vol("root", "/data", "xfs", "lvm", "100%"))), ""},
	})
}

func TestRuleVolumeSizes(t *testing.T) {
	dataVolSized := func(provider, size string) string {
		return osLayout(set(dataManual("data", "none", "/dev/sdb"), vol("d", "/d", "xfs", provider, size)))
	}
	cases := []ruleCase{
		// lvm.Build fails "lv has no size" for an lvm volume without one.
		{"lvm-no-size", dataVolSized("lvm", ""), `(set "data", volume "d"): size is required for provider=lvm`},
		{"lvm-no-size-os-set", lay(set(osFields("os"), espVol(), vol("root", "/", "xfs", "lvm", ""))), `(set "os", volume "root"): size is required for provider=lvm`},
		// lvm.go sorts the 100% LV last itself: JSON position does not matter.
		{"lvm-100pct-listed-first-ok", osLayout(set(dataManual("data", "none", "/dev/sdb"),
			vol("a", "/a", "xfs", "lvm", "100%"), vol("b", "/b", "xfs", "lvm", "10G"))), ""},
		{"lvm-two-100pct", osLayout(set(dataManual("data", "none", "/dev/sdb"),
			vol("a", "/a", "xfs", "lvm", "100%"), vol("b", "/b", "xfs", "lvm", "100%"))),
			"at most one lvm volume per set may use size=100%"},
		// match sizes go through the resolver's own parser.
		{"match-min-size-decimal", lay(set(osFields("os")+`,"match":{"min_size":"1.5T"}`, espVol(), rootLV())),
			`(set "os"): match.min_size: cannot parse size "1.5T"`},
		{"match-max-size-word", lay(set(osFields("os")+`,"match":{"max_size":"big"}`, espVol(), rootLV())),
			`(set "os"): match.max_size: cannot parse size "big"`},
		{"match-min-above-max", lay(set(osFields("os")+`,"match":{"min_size":"2T","max_size":"1T"}`, espVol(), rootLV())),
			`match.min_size "2T" is larger than match.max_size "1T", so no disk can match`},
		{"match-sizes-ok", lay(set(osFields("os")+`,"match":{"min_size":"400G","max_size":"8TB"}`, espVol(), rootLV())), ""},
	}
	for _, bad := range []string{"1.5G", "50%", "20GB", "20", "-5G", "0G", "0M", "20g", "20 G", " 20G", "G", "100 %", "99999999999T", "99999999999999999999G"} {
		for _, p := range []string{"partition", "lvm"} {
			want := "is not valid"
			switch bad {
			case "0G", "0M":
				want = "must be greater than zero"
			case "99999999999T", "99999999999999999999G":
				want = "is too large"
			}
			cases = append(cases, ruleCase{p + "-size-" + bad, dataVolSized(p, bad), `(set "data", volume "d"): size "` + bad + `" ` + want})
		}
	}
	for _, good := range []string{"512K", "600M", "20G", "2T", "100%"} {
		for _, p := range []string{"partition", "lvm"} {
			cases = append(cases, ruleCase{p + "-size-ok-" + good, dataVolSized(p, good), ""})
		}
	}
	runRuleCases(t, cases)
}

func TestRulePartitionOrder(t *testing.T) {
	os1 := osManual("os", "none", "/dev/sda")
	rootPart := vol("root", "/", "xfs", "partition", "100%")
	swapPart := vol("swap", "swap", "swap", "partition", "2G")
	runRuleCases(t, []ruleCase{
		// The reported triggers: a 100% root followed by another partition,
		// and a 100% root next to an lvm volume (which needs a storage
		// partition after the direct partitions).
		{"part-100pct-then-swap", lay(set(os1, espVol(), bootVol(), rootPart, swapPart)),
			`(set "os", volume "root"): size=100% takes the rest of the disk, but direct partition "swap" is created after it (partition order: uefi (/boot/efi, 600M) -> boot (/boot, 1G) -> root (/, 100%) -> swap (swap, 2G))`},
		{"part-100pct-plus-lvm", lay(set(os1, espVol(), bootVol(), rootPart, vol("var", "/var", "xfs", "lvm", "20G"))),
			`(set "os", volume "root"): size=100% takes the rest of the disk, but lvm volume(s) "var" need a storage partition after the direct partitions`},
		{"part-100pct-plus-lvm-100pct", lay(set(os1, espVol(), rootPart, vol("var", "/var", "xfs", "lvm", "100%"))),
			`but lvm volume(s) "var" need a storage partition`},
		{"part-two-100pct", osLayout(set(dataManual("data", "none", "/dev/sdb"),
			vol("a", "/a", "xfs", "partition", "100%"), vol("b", "/b", "xfs", "partition", "100%"))),
			`(set "data", volume "a"): size=100% takes the rest of the disk, but direct partition "b" is created after it`},
		{"part-data-100pct-plus-lvm", osLayout(set(dataManual("data", "none", "/dev/sdb"),
			vol("a", "/a", "xfs", "partition", "100%"), vol("b", "/b", "xfs", "lvm", "10G"))),
			`(set "data", volume "a"): size=100% takes the rest of the disk, but lvm volume(s) "b"`},
		// The os set's ESP and /boot are created first whatever their JSON
		// position, so a 100% root listed before them is still last.
		{"part-100pct-last-ok", lay(set(os1, espVol(), bootVol(), swapPart, rootPart)), ""},
		{"part-100pct-json-first-swap-after", lay(set(os1, rootPart, swapPart, espVol(), bootVol())),
			`size=100% takes the rest of the disk, but direct partition "swap" is created after it (partition order: uefi (/boot/efi, 600M) -> boot (/boot, 1G) -> root (/, 100%) -> swap (swap, 2G))`},
		{"part-100pct-json-first-boot-after-ok", lay(set(os1, rootPart, espVol(), bootVol())), ""},
		{"part-fixed-plus-lvm-ok", lay(set(os1, espVol(), bootVol(), vol("root", "/", "xfs", "partition", "40G"), vol("var", "/var", "xfs", "lvm", "100%"))), ""},
		{"part-data-fixed-plus-lvm-ok", osLayout(set(dataManual("data", "none", "/dev/sdb"),
			vol("a", "/a", "xfs", "partition", "100G"), vol("b", "/b", "xfs", "lvm", "100%"))), ""},
		// ESP and /boot are created with sgdisk +<size>: 100% is not a size.
		{"esp-100pct", lay(set(os1, vol("uefi", "/boot/efi", "vfat", "partition", "100%"), rootLV())),
			`(set "os", volume "uefi"): the /boot/efi partition needs a fixed size, not 100%`},
		{"boot-100pct", lay(set(os1, espVol(), vol("boot", "/boot", "xfs", "partition", "100%"), rootLV())),
			`(set "os", volume "boot"): the /boot partition needs a fixed size, not 100%`},
		{"part-on-raid", lay(set(osManual("os", "1", "/dev/sda", "/dev/sdb"), espVol(), vol("root", "/", "xfs", "partition", "40G"))),
			`direct partition volumes require raid=none (volume "root" is provider=partition`},
	})
}

func TestRuleSpares(t *testing.T) {
	dataVolume := vol("d", "/d", "xfs", "lvm", "100%")
	data := func(fields string) string { return osLayout(set(fields, dataVolume)) }
	discover := func(raid string, count, spares int) string {
		return `"name":"data","role":"data","selection":"discover","raid":"` + raid + `","count":` + strconv.Itoa(count) + `,"spares":` + strconv.Itoa(spares)
	}
	withSpares := func(fields string, spares int) string { return fields + `,"spares":` + strconv.Itoa(spares) }
	osRaid := func(fields string) string { return lay(set(fields, espVol(), bootVol(), rootLV())) }
	devs := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "/dev/sd" + string(rune('b'+i))
		}
		return out
	}
	runRuleCases(t, []ruleCase{
		// raid none / raid 0 have no member a spare could replace.
		{"spares-raid-none-manual", data(withSpares(dataManual("data", "none", devs(2)...), 1)),
			`(set "data"): raid=none takes no spares, got spares=1`},
		{"spares-raid-none-discover", data(withSpares(`"name":"data","role":"data","selection":"discover","raid":"none"`, 1)),
			`raid=none takes no spares`},
		{"spares-raid0-discover", data(discover("0", 2, 1)), `(set "data"): raid=0 takes no spares, got spares=1`},
		{"spares-os-raid-none", osRaid(withSpares(osFields("os"), 1)), `(set "os"): raid=none takes no spares`},
		// manual: devices[] lists every member, spares included.
		{"spares-raid1-3dev-1spare-ok", data(withSpares(dataManual("data", "1", devs(3)...), 1)), ""},
		{"spares-raid1-3dev-no-spare", data(dataManual("data", "1", devs(3)...)), "raid=1 needs exactly 2 device(s), but devices[] lists 3"},
		{"spares-raid1-4dev-1spare", data(withSpares(dataManual("data", "1", devs(4)...), 1)),
			`(set "data"): raid=1 needs exactly 2 device(s) active, but devices[] lists 4 with spares=1 (3 active)`},
		{"spares-raid1-2dev-2spare", data(withSpares(dataManual("data", "1", devs(2)...), 2)),
			`(set "data"): spares=2 leaves 0 active device(s) of the 2 in devices[]`},
		{"spares-raid10-5dev-1spare-ok", data(withSpares(dataManual("data", "10", devs(5)...), 1)), ""},
		{"spares-raid10-6dev-1spare-odd", data(withSpares(dataManual("data", "10", devs(6)...), 1)),
			"raid=10 needs at least 4 device(s) active, but devices[] lists 6 with spares=1 (5 active)"},
		{"spares-raid5-5dev-1spare-ok", data(withSpares(dataManual("data", "5", devs(5)...), 1)), ""},
		{"spares-raid5-4dev-1spare", data(withSpares(dataManual("data", "5", devs(4)...), 1)),
			"raid=5 needs at least 4 device(s) active, but devices[] lists 4 with spares=1 (3 active)"},
		{"spares-raid6-6dev-1spare-ok", data(withSpares(dataManual("data", "6", devs(6)...), 1)), ""},
		{"spares-raid6-5dev-1spare", data(withSpares(dataManual("data", "6", devs(5)...), 1)), "raid=6 needs at least 5 device(s) active"},
		// discover: count active members, spares resolved on top.
		{"spares-raid1-discover-ok", data(discover("1", 2, 1)), ""},
		{"spares-raid10-discover-ok", data(discover("10", 4, 2)), ""},
		// OS policy counts ACTIVE drives: 1, 2 or 4, spares on top.
		{"spares-os-raid1-3dev-1spare-ok", osRaid(withSpares(osManual("os", "1", devs(3)...), 1)), ""},
		{"spares-os-raid10-5dev-1spare-ok", osRaid(withSpares(osManual("os", "10", devs(5)...), 1)), ""},
		{"spares-os-raid10-discover-ok", osRaid(`"name":"os","role":"os","selection":"discover","raid":"10","count":4,"spares":1`), ""},
		{"spares-os-raid1-2dev-1spare", osRaid(withSpares(osManual("os", "1", devs(2)...), 1)),
			`(set "os"): raid=1 needs exactly 2 device(s) active, but devices[] lists 2 with spares=1 (1 active)`},
		{"spares-os-raid10-7dev-1spare", osRaid(withSpares(osManual("os", "10", devs(7)...), 1)),
			`(set "os"): the OS set uses exactly 1, 2 or 4 drives (raid none/1/0/10), got 6 active (7 devices minus spares=1)`},
	})
}

func TestRuleDevicePathStyle(t *testing.T) {
	osOn := func(devs ...string) string {
		raid := "none"
		if len(devs) == 2 {
			raid = "1"
		}
		return lay(set(osManual("os", raid, devs...), espVol(), bootVol(), rootLV()))
	}
	dataOn := func(dev string) string {
		return osLayout(set(dataManual("data", "none", dev), vol("d", "/d", "xfs", "lvm", "100%")))
	}
	runRuleCases(t, []ruleCase{
		// A whole disk's by-uuid link disappears once the disk is
		// partitioned: the second boot always fails "device not found".
		{"by-uuid-os", osOn("/dev/disk/by-uuid/0b6c1f9e-4d0a-4c4e-9a43-6f5d7c2b1a10"),
			`(set "os"): device "/dev/disk/by-uuid/0b6c1f9e-4d0a-4c4e-9a43-6f5d7c2b1a10" is a by-uuid link`},
		{"by-uuid-data", dataOn("/dev/disk/by-uuid/1234-ABCD"), `(set "data"): device "/dev/disk/by-uuid/1234-ABCD" is a by-uuid link`},
		{"by-uuid-raid1", osOn("/dev/disk/by-uuid/a", "/dev/disk/by-uuid/b"), `is a by-uuid link`},
		{"by-label", dataOn("/dev/disk/by-label/scratch"), `is a /dev/disk/by-label link`},
		{"by-partuuid", dataOn("/dev/disk/by-partuuid/5e1c-01"), `is a /dev/disk/by-partuuid link`},
		{"by-partlabel", dataOn("/dev/disk/by-partlabel/DATA"), `is a /dev/disk/by-partlabel link`},
		{"by-diskseq", dataOn("/dev/disk/by-diskseq/7"), `is a /dev/disk/by-diskseq link`},
		{"by-id-ok", osOn("/dev/disk/by-id/ata-Samsung_SSD_870_S5Y1NX0R123456"), ""},
		{"by-id-raid1-ok", osOn("/dev/disk/by-id/wwn-0x5002538e40a1b2c3", "/dev/disk/by-id/wwn-0x5002538e40a1b2c4"), ""},
		{"by-path-ok", osOn("/dev/disk/by-path/pci-0000:00:1f.2-ata-1"), ""},
		{"kernel-sd-ok", osOn("/dev/sda"), ""},
		{"kernel-vd-ok", osOn("/dev/vda"), ""},
		{"kernel-nvme-ok", osOn("/dev/nvme0n1"), ""},
		{"mixed-styles", osOn("/dev/sda", "/dev/disk/by-id/wwn-0x1"), "all paths must use the same style (/dev/..., by-id, by-path)"},
	})
}

func TestRuleDataSetMountpoints(t *testing.T) {
	data := func(vols ...string) string { return osLayout(set(dataManual("data", "none", "/dev/sdb"), vols...)) }
	dataOnly := func(vols ...string) string { return lay(set(dataManual("data", "none", "/dev/sdb"), vols...)) }
	runRuleCases(t, []ruleCase{
		// The builder creates /boot and /boot/efi for the os set only; on a
		// data set they vanished without a word.
		{"data-boot-partition", data(vol("boot", "/boot", "xfs", "partition", "1G"), vol("d", "/d", "xfs", "lvm", "100%")),
			`sets[1].volumes[0] (set "data", volume "boot"): a data set cannot carry /boot`},
		{"data-esp-partition", dataOnly(vol("uefi", "/boot/efi", "vfat", "partition", "600M"), vol("d", "/d", "xfs", "lvm", "100%")),
			`(set "data", volume "uefi"): a data set cannot carry /boot/efi`},
		{"data-boot-lvm", dataOnly(vol("boot", "/boot", "xfs", "lvm", "1G"), vol("d", "/d", "xfs", "lvm", "100%")),
			`a data set cannot carry /boot`},
		{"data-root", dataOnly(vol("root", "/", "xfs", "lvm", "100%")),
			`(set "data", volume "root"): a data set cannot be mounted at "/"`},
		{"data-root-partition", dataOnly(vol("root", "/", "xfs", "partition", "100%")), `a data set cannot be mounted at "/"`},
		// Duplicate mountpoints, across sets and within one.
		{"dup-mount-across-sets", lay(
			set(osFields("os"), espVol(), bootVol(), rootLV(), vol("scratch", "/scratch", "xfs", "lvm", "10G")),
			set(dataManual("data", "none", "/dev/sdb"), vol("d", "/scratch", "xfs", "lvm", "100%"))),
			`mountpoint "/scratch" is declared twice: set "os" volume "scratch" (sets[0].volumes[3]) and set "data" volume "d" (sets[1].volumes[0])`},
		{"dup-mount-two-data-sets", osLayout(
			set(dataManual("a", "none", "/dev/sdb"), vol("x", "/data", "xfs", "lvm", "100%")),
			set(dataManual("b", "none", "/dev/sdc"), vol("y", "/data", "xfs", "lvm", "100%"))),
			`mountpoint "/data" is declared twice: set "a" volume "x"`},
		{"dup-mount-in-set", data(vol("a", "/d", "xfs", "lvm", "10G"), vol("b", "/d", "xfs", "lvm", "100%")),
			`mountpoint "/d" is declared twice: set "data" volume "a"`},
		{"dup-esp-in-os-set", lay(set(osFields("os"), espVol(), vol("uefi2", "/boot/efi", "vfat", "partition", "600M"), rootLV())),
			`mountpoint "/boot/efi" is declared twice`},
		{"mount-not-canonical", data(vol("d", "/data/", "xfs", "lvm", "100%")),
			`(set "data", volume "d"): mountpoint "/data/" is not in canonical form; write it as "/data"`},
		{"mount-not-canonical-boot", lay(set(osFields("os"), espVol(), vol("boot", "/boot/", "xfs", "partition", "1G"), rootLV())),
			`mountpoint "/boot/" is not in canonical form`},
		// Nearby layouts that must still pass.
		{"data-mounts-ok", data(vol("d", "/data", "xfs", "lvm", "100%"), vol("s", "/data/scratch", "xfs", "lvm", "10G")), ""},
		{"swap-in-two-sets-ok", lay(
			set(osFields("os"), espVol(), bootVol(), vol("swap", "swap", "swap", "lvm", "2G"), rootLV()),
			set(dataManual("data", "none", "/dev/sdb"), vol("swap", "swap", "swap", "lvm", "8G"), vol("d", "/d", "xfs", "lvm", "100%"))), ""},
		{"data-boot-like-path-ok", data(vol("d", "/boot-images", "xfs", "lvm", "100%")), ""},
	})
}

func TestRulePersistentDataOnly(t *testing.T) {
	runRuleCases(t, []ruleCase{
		{"persistent-os-set", lay(set(osManual("os", "none", "/dev/sda")+`,"persistent":true`, espVol(), bootVol(), rootLV())),
			`sets[0] (set "os"): persistent=true is only valid on a role=data set`},
		{"persistent-os-set-raid1", lay(set(osManual("os", "1", "/dev/sda", "/dev/sdb")+`,"persistent":true`, espVol(), bootVol(), rootLV())),
			`persistent=true is only valid on a role=data set`},
		{"persistent-data-ok", osLayout(set(dataManual("data", "none", "/dev/sdb")+`,"persistent":true`, vol("d", "/d", "xfs", "lvm", "100%"))), ""},
		{"persistent-data-only-layout-ok", lay(set(dataManual("data", "none", "/dev/sdb")+`,"persistent":true`, vol("d", "/d", "xfs", "lvm", "100%"))), ""},
		{"persistent-data-discover", lay(set(`"name":"data","role":"data","selection":"discover","raid":"none","persistent":true`, vol("d", "/d", "xfs", "lvm", "100%"))),
			"persistent=true requires selection=manual"},
	})
}
