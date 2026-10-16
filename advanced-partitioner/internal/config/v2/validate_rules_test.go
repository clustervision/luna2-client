package v2

// Table-driven tests for the build-feasibility rules: every layout the
// partition/raid/lvm/fs builders cannot build, or whose cleanup would damage
// the node, must be refused at validation (exit 3, before any disk is
// touched); a layout that installs but looks like a mistake must validate
// with a warning; a nearby buildable layout must validate without one.

import (
	"strconv"
	"strings"
	"testing"
)

// warnPrefix marks a want that is a warning on an accepted layout.
const warnPrefix = "warn: "

type ruleCase struct {
	name string
	json string
	want string // substring of the refusal; "" = must validate with no warning; warnPrefix + substring = must validate with that warning
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
			wantWarn, accepted := strings.CutPrefix(c.want, warnPrefix)
			if c.want == "" || accepted {
				if err != nil {
					t.Fatalf("must validate, got: %v", err)
				}
				warnings := strings.Join(cfg.Warnings, "\n")
				switch {
				case wantWarn == "" && len(cfg.Warnings) > 0:
					t.Fatalf("must validate without a warning, got: %s", warnings)
				case !strings.Contains(warnings, wantWarn):
					t.Fatalf("\n  got warnings: %q\n  want substring: %s", cfg.Warnings, wantWarn)
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
	raidNamed := func(name string) string {
		return osLayout(set(dataManual(name, "1", "/dev/sdb", "/dev/sdc"), vol("d", "/d", "xfs", "lvm", "100%")))
	}
	dataVolNamed := func(name, provider string) string {
		return osLayout(set(dataManual("data", "none", "/dev/sdb"), vol(name, "/d", "xfs", provider, "100%")))
	}
	runRuleCases(t, []ruleCase{
		// set names: a path element leaves /dev under rm -rf; LVM refuses a
		// leading '-'; any other odd character only earns a warning.
		{"set-name-dot", dataNamed("."), `(set "."): name must not be "."`},
		{"set-name-dotdot", raidNamed(".."), `(set ".."): name must not be ".."`},
		{"set-name-slash", dataNamed("a/b"), `(set "a/b"): name must not contain '/'`},
		{"set-name-slash-dotdot", dataNamed("a/.."), `name must not contain '/'`},
		{"set-name-leading-hyphen", dataNamed("-data"), `(set "-data"): name must not start with '-'`},
		{"set-name-folds-to-nothing", dataNamed("__"), `(set "__"): name must contain a letter or digit`},
		{"set-name-space", dataNamed("my data"), warnPrefix + `(set "my data"): name "my data" has characters other than letters, digits, '_', '.' and '-'`},
		{"set-name-ok-dotted", dataNamed("data.1"), ""},
		{"set-name-ok-dots", raidNamed("a..b"), ""},
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
		// the md array name limit binds raid sets only: raid none has no array.
		{"set-name-too-long-raid", raidNamed("scratch-nvme-local-2026"), `(set "scratch-nvme-local-2026"): name is 23 characters, at most 22 are allowed on a raid set`},
		{"set-name-longest-raid-ok", raidNamed("scratch-nvme-local-202"), ""},
		{"set-name-too-long-raid-none-ok", dataNamed("scratch-nvme-local-2026-and-then-some"), ""},
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
		{"vol-name-dot", dataVolNamed(".", "lvm"), `(set "data", volume "."): name must not be "."`},
		{"vol-name-dotdot-partition", dataVolNamed("..", "partition"), `name must not be ".."`},
		{"vol-name-slash", dataVolNamed("a/b", "lvm"), warnPrefix + `(set "data", volume "a/b"): name "a/b" has characters other than`},
		{"vol-name-space", dataVolNamed("my vol", "partition"), warnPrefix + `name "my vol" has characters other than`},
		{"vol-name-leading-hyphen", dataVolNamed("-d", "lvm"), `(set "data", volume "-d"): name must not start with '-'`},
		{"lv-name-leading-underscore", dataVolNamed("_d", "lvm"), `lvm volume name "_d" matches "_", which LVM reserves`},
		{"lv-name-snapshot", dataVolNamed("snapshot", "lvm"), `lvm volume name "snapshot" matches "snapshot", which LVM reserves`},
		{"lv-name-snapshots", dataVolNamed("snapshots", "lvm"), `matches "snapshot", which LVM reserves`},
		{"lv-name-pvmove", dataVolNamed("pvmove", "lvm"), `matches "pvmove", which LVM reserves`},
		{"lv-name-tmeta", dataVolNamed("pool_tmeta", "lvm"), `matches "_tmeta", which LVM reserves`},
		{"lv-name-mlog", dataVolNamed("d_mlog", "lvm"), `matches "_mlog"`},
		{"lv-name-rimage", dataVolNamed("d_rimage_0", "lvm"), `matches "_rimage"`},
		{"lv-name-ok", dataVolNamed("d_1-x", "lvm"), ""},
		{"lv-name-ok-dotted", dataVolNamed("d.1", "lvm"), ""},
		{"partition-name-snapshot-ok", dataVolNamed("snapshot", "partition"), ""},
		{"partition-name-underscore-ok", dataVolNamed("_d", "partition"), ""},
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
		// sgdisk reads a fraction as a wrong size with exit 0; lvcreate reads it correctly.
		{"partition-size-fraction", dataVolSized("partition", "1.5G"), `(set "data", volume "d"): size "1.5G" has a fraction, which sgdisk silently turns into a wrong partition`},
		{"partition-size-fraction-zero", dataVolSized("partition", "0.0G"), `has a fraction`},
		{"lvm-size-fraction-zero", dataVolSized("lvm", "0.0G"), `size "0.0G" must be greater than zero`},
		// match sizes go through the resolver's own parser.
		{"match-min-size-decimal-ok", lay(set(osFields("os")+`,"match":{"min_size":"1.5T"}`, espVol(), rootLV())), ""},
		{"match-max-size-word", lay(set(osFields("os")+`,"match":{"max_size":"big"}`, espVol(), rootLV())),
			warnPrefix + `(set "os"): match.max_size: cannot parse size "big"`},
		{"match-min-above-max", lay(set(osFields("os")+`,"match":{"min_size":"2T","max_size":"1T"}`, espVol(), rootLV())),
			warnPrefix + `match.min_size "2T" is larger than match.max_size "1T", so no disk can match`},
		{"match-sizes-ok", lay(set(osFields("os")+`,"match":{"min_size":"400G","max_size":"8TB"}`, espVol(), rootLV())), ""},
	}
	// Measured: sgdisk makes "20" and "50%" into 20 and 50 sectors with exit 0;
	// lvcreate reads "20" as 20 MiB and fails on the other forms after the wipe.
	for _, bad := range []string{"50%", "20", "-5G", "20 G", " 20G", "G", "100 %"} {
		for _, p := range []string{"partition", "lvm"} {
			cases = append(cases, ruleCase{p + "-size-" + bad, dataVolSized(p, bad), `(set "data", volume "d"): size "` + bad + `" cannot be built`})
		}
	}
	for _, bad := range []string{"0G", "0M", "0gb", "99999999999T", "99999999999999999999G"} {
		for _, p := range []string{"partition", "lvm"} {
			want := "must be greater than zero"
			if strings.HasPrefix(bad, "9") {
				want = "is too large"
			}
			cases = append(cases, ruleCase{p + "-size-" + bad, dataVolSized(p, bad), `(set "data", volume "d"): size "` + bad + `" ` + want})
		}
	}
	// sgdisk and lvcreate both read a lowercase unit and a B/iB suffix as the binary unit.
	for _, good := range []string{"512K", "600M", "600m", "20G", "20g", "20GB", "20gb", "20GiB", "20gib", "2T", "2TiB", "100%"} {
		for _, p := range []string{"partition", "lvm"} {
			cases = append(cases, ruleCase{p + "-size-ok-" + good, dataVolSized(p, good), ""})
		}
	}
	for _, good := range []string{"1.5G", "0.5T", "1.25gib"} {
		cases = append(cases, ruleCase{"lvm-size-ok-" + good, dataVolSized("lvm", good), ""})
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
			`(set "os", volume "root"): size=100% takes the rest of the disk, but direct partition "swap" is created after it; only the last direct partition may use 100%`},
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
			`size=100% takes the rest of the disk, but direct partition "swap" is created after it`},
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
	const ignoredSpares = `is ignored: devices[] lists only the members; add the spare disk(s) to devices[]`
	runRuleCases(t, []ruleCase{
		// raid none / raid 0 have no member a spare could replace: the spares
		// are dropped, and the member count is judged as it always was.
		{"spares-raid-none-manual", data(withSpares(dataManual("data", "none", devs(1)...), 1)),
			warnPrefix + `(set "data"): raid=none takes no spares, so spares=1 is ignored`},
		{"spares-raid-none-manual-two-disks", data(withSpares(dataManual("data", "none", devs(2)...), 1)),
			`raid=none needs exactly 1 device(s), but devices[] lists 2`},
		{"spares-raid-none-discover", data(withSpares(`"name":"data","role":"data","selection":"discover","raid":"none"`, 1)),
			warnPrefix + `raid=none takes no spares`},
		{"spares-raid0-discover", data(discover("0", 2, 1)), warnPrefix + `(set "data"): raid=0 takes no spares, so spares=1 is ignored`},
		{"spares-os-raid-none", osRaid(withSpares(osFields("os"), 1)), warnPrefix + `(set "os"): raid=none takes no spares`},
		// manual: devices[] lists every member, spares included.
		{"spares-raid1-3dev-1spare-ok", data(withSpares(dataManual("data", "1", devs(3)...), 1)), ""},
		{"spares-raid1-3dev-no-spare", data(dataManual("data", "1", devs(3)...)), "raid=1 needs exactly 2 device(s), but devices[] lists 3"},
		{"spares-raid1-4dev-1spare", data(withSpares(dataManual("data", "1", devs(4)...), 1)),
			`(set "data"): raid=1 needs exactly 2 device(s), but devices[] lists 3 active (4 devices minus spares=1)`},
		{"spares-raid10-5dev-1spare-ok", data(withSpares(dataManual("data", "10", devs(5)...), 1)), ""},
		{"spares-raid5-5dev-1spare-ok", data(withSpares(dataManual("data", "5", devs(5)...), 1)), ""},
		{"spares-raid6-6dev-1spare-ok", data(withSpares(dataManual("data", "6", devs(6)...), 1)), ""},
		// A layout written when the installer ignored spares lists only the
		// members: devices[] satisfies the level on its own, minus the spares
		// it does not. It installs as it always did, with every disk active.
		{"old-spares-raid1-2dev-2spare", data(withSpares(dataManual("data", "1", devs(2)...), 2)), warnPrefix + `(set "data"): spares=2 ` + ignoredSpares},
		{"old-spares-raid10-6dev-1spare", data(withSpares(dataManual("data", "10", devs(6)...), 1)), warnPrefix + `spares=1 ` + ignoredSpares},
		{"old-spares-raid5-4dev-1spare", data(withSpares(dataManual("data", "5", devs(4)...), 1)), warnPrefix + `spares=1 ` + ignoredSpares},
		{"old-spares-raid6-5dev-1spare", data(withSpares(dataManual("data", "6", devs(5)...), 1)), warnPrefix + `spares=1 ` + ignoredSpares},
		// discover: count active members, spares resolved on top.
		{"spares-raid1-discover-ok", data(discover("1", 2, 1)), ""},
		{"spares-raid10-discover-ok", data(discover("10", 4, 2)), ""},
		// OS policy counts ACTIVE drives: 1, 2 or 4, spares on top.
		{"spares-os-raid1-3dev-1spare-ok", osRaid(withSpares(osManual("os", "1", devs(3)...), 1)), ""},
		{"spares-os-raid10-5dev-1spare-ok", osRaid(withSpares(osManual("os", "10", devs(5)...), 1)), ""},
		{"spares-os-raid10-discover-ok", osRaid(`"name":"os","role":"os","selection":"discover","raid":"10","count":4,"spares":1`), ""},
		{"old-spares-os-raid1-2dev-1spare", osRaid(withSpares(osManual("os", "1", devs(2)...), 1)), warnPrefix + `(set "os"): spares=1 ` + ignoredSpares},
		{"spares-os-raid10-7dev-1spare", osRaid(withSpares(osManual("os", "10", devs(7)...), 1)),
			`(set "os"): the OS set uses exactly 1, 2 or 4 drives (raid none/1/0/10), got 6 active (7 devices minus spares=1)`},
	})
}

// The spares a warning drops are gone from the Config every consumer reads
// (SpareCount, the pinned layout), so the raid builder sees what the
// installer that ignored spares saw: every device active. Warnings are what
// the last validation found, not an accumulation.
func TestOldSparesCountNormalised(t *testing.T) {
	doc := lay(set(osFields("os"), espVol(), bootVol(), rootLV()),
		set(dataManual("data", "1", "/dev/sdb", "/dev/sdc")+`,"spares":1`, vol("d", "/d", "xfs", "lvm", "100%")))
	cfg, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.ValidateRaw([]byte(doc)); err != nil {
		t.Fatalf("must validate: %v", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "spares=1 is ignored") {
		t.Fatalf("warnings = %q", cfg.Warnings)
	}
	s := &cfg.Sets[1]
	if s.Spares != 0 || s.SpareCount() != 0 || s.MemberCount() != 2 {
		t.Fatalf("spares not normalised: Spares=%d MemberCount=%d", s.Spares, s.MemberCount())
	}
	if err := cfg.Validate(); err != nil || len(cfg.Warnings) != 0 {
		t.Fatalf("the normalised layout must validate cleanly: err=%v warnings=%q", err, cfg.Warnings)
	}
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
		{"by-label", dataOn("/dev/disk/by-label/scratch"), warnPrefix + `is a /dev/disk/by-label link`},
		{"by-partuuid", dataOn("/dev/disk/by-partuuid/5e1c-01"), warnPrefix + `is a /dev/disk/by-partuuid link`},
		{"by-partlabel", dataOn("/dev/disk/by-partlabel/DATA"), warnPrefix + `is a /dev/disk/by-partlabel link`},
		{"by-diskseq", dataOn("/dev/disk/by-diskseq/7"), warnPrefix + `is a /dev/disk/by-diskseq link`},
		{"by-id-ok", osOn("/dev/disk/by-id/ata-Samsung_SSD_870_S5Y1NX0R123456"), ""},
		{"by-id-raid1-ok", osOn("/dev/disk/by-id/wwn-0x5002538e40a1b2c3", "/dev/disk/by-id/wwn-0x5002538e40a1b2c4"), ""},
		{"by-path-ok", osOn("/dev/disk/by-path/pci-0000:00:1f.2-ata-1"), ""},
		{"kernel-sd-ok", osOn("/dev/sda"), ""},
		{"kernel-vd-ok", osOn("/dev/vda"), ""},
		{"kernel-nvme-ok", osOn("/dev/nvme0n1"), ""},
		{"mixed-styles", osOn("/dev/sda", "/dev/disk/by-id/wwn-0x1"), "all paths must use the same style (/dev/..., by-id, by-path)"},
		// Paths that can name no disk fail on the node as "device not found".
		{"blank-in-path", osOn("/dev/sda "), warnPrefix + `device "/dev/sda " contains a blank or control character`},
		{"dotdot", osOn("/dev/../etc/passwd"), warnPrefix + `is not a clean path (write it as "/etc/passwd")`},
		{"double-slash", osOn("/dev//sda"), warnPrefix + "is not a clean path"},
		{"trailing-slash", osOn("/dev/sda/"), warnPrefix + "is not a clean path"},
		{"dev-disk-dir", osOn("/dev/disk"), warnPrefix + "is a directory, not a disk"},
		{"by-id-dir", osOn("/dev/disk/by-id"), warnPrefix + "must be /dev/disk/by-id/<link name>"},
		{"by-id-nested", osOn("/dev/disk/by-id/a/b"), warnPrefix + "must be /dev/disk/by-id/<link name>"},
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
		// Every builder compares mountpoints as exact strings.
		{"mount-not-canonical", data(vol("d", "/data/", "xfs", "lvm", "100%")),
			warnPrefix + `(set "data", volume "d"): mountpoint "/data/" is not in canonical form; write it as "/data"`},
		{"mount-not-canonical-boot", lay(set(osFields("os"), espVol(), vol("boot", "/boot/", "xfs", "partition", "1G"), rootLV())),
			warnPrefix + `mountpoint "/boot/" is not in canonical form`},
		// Nearby layouts that must still pass.
		{"data-mounts-ok", data(vol("d", "/data", "xfs", "lvm", "100%"), vol("s", "/data/scratch", "xfs", "lvm", "10G")), ""},
		{"swap-in-two-sets-ok", lay(
			set(osFields("os"), espVol(), bootVol(), vol("swap", "swap", "swap", "lvm", "2G"), rootLV()),
			set(dataManual("data", "none", "/dev/sdb"), vol("swap", "swap", "swap", "lvm", "8G"), vol("d", "/d", "xfs", "lvm", "100%"))), ""},
		{"data-boot-like-path-ok", data(vol("d", "/boot-images", "xfs", "lvm", "100%")), ""},
	})
}

// persistent on the os set protects nothing (every osimage install rewrites
// the root), so the operator is told; the layout installs as before.
func TestRulePersistentDataOnly(t *testing.T) {
	runRuleCases(t, []ruleCase{
		{"persistent-os-set", lay(set(osManual("os", "none", "/dev/sda")+`,"persistent":true`, espVol(), bootVol(), rootLV())),
			warnPrefix + `sets[0] (set "os"): persistent=true protects nothing on the os set`},
		{"persistent-os-set-raid1", lay(set(osManual("os", "1", "/dev/sda", "/dev/sdb")+`,"persistent":true`, espVol(), bootVol(), rootLV())),
			warnPrefix + `persistent=true protects nothing on the os set`},
		{"persistent-data-ok", osLayout(set(dataManual("data", "none", "/dev/sdb")+`,"persistent":true`, vol("d", "/d", "xfs", "lvm", "100%"))), ""},
		{"persistent-data-only-layout-ok", lay(set(dataManual("data", "none", "/dev/sdb")+`,"persistent":true`, vol("d", "/d", "xfs", "lvm", "100%"))), ""},
		{"persistent-data-discover", lay(set(`"name":"data","role":"data","selection":"discover","raid":"none","persistent":true`, vol("d", "/d", "xfs", "lvm", "100%"))),
			"persistent=true requires selection=manual"},
	})
}

// A raid1/raid10 os set without a /boot volume is buildable (its /boot lives
// on the root LV; the raid builder creates the -boot array only for a declared
// /boot volume), so it validates, and BootMirrorDevice over the set's volumes
// never names a -boot array for it: the health probe and the mount predictor
// must not look for a device that was never created.
func TestBootMirrorDeviceOnlyForDeclaredBootVolume(t *testing.T) {
	for _, c := range []struct {
		name string
		json string
	}{
		{"raid1", lay(set(osManual("os", "1", "/dev/sda", "/dev/sdb"), espVol(), rootLV()))},
		{"raid10", lay(set(osManual("os-a", "10", "/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"), espVol(), rootLV()))},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.json))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if err := cfg.ValidateRaw([]byte(c.json)); err != nil {
				t.Fatalf("a mirrored os set without /boot must validate: %v", err)
			}
			s := cfg.OSSet()
			md := s.MDName()
			for _, v := range s.Volumes {
				got := s.BootMirrorDevice(v)
				want := ""
				if v.Mountpoint == "/boot/efi" {
					want = "/dev/md/" + md + "-efi"
				}
				if got != want {
					t.Errorf("BootMirrorDevice(%s) = %q, want %q", v.Name, got, want)
				}
			}
		})
	}

	// With a /boot volume the -boot array is named; raid0 mirrors nothing.
	withBoot := Set{Name: "os", Role: "os", Raid: "1", Volumes: []Volume{
		{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Provider: "partition", Size: "600M"},
		{Name: "boot", Mountpoint: "/boot", FS: "xfs", Provider: "partition", Size: "1G"},
		{Name: "root", Mountpoint: "/", FS: "xfs", Provider: "lvm", Size: "100%"},
	}}
	if got := withBoot.BootMirrorDevice(withBoot.Volumes[1]); got != "/dev/md/os-boot" {
		t.Errorf("raid1 /boot = %q, want /dev/md/os-boot", got)
	}
	if got := withBoot.BootMirrorDevice(withBoot.Volumes[0]); got != "/dev/md/os-efi" {
		t.Errorf("raid1 /boot/efi = %q, want /dev/md/os-efi", got)
	}
	striped := withBoot
	striped.Raid = "0"
	if got := striped.BootMirrorDevice(striped.Volumes[1]); got != "" {
		t.Errorf("raid0 /boot = %q, want \"\" (raid0 does not mirror the boot chain)", got)
	}
}

// A layout without an os set stays valid at config level: memboot nodes use
// data-only layouts. The diskful modes refuse it at runtime, where the
// install mode is known.
func TestRuleDataOnlyLayoutStaysValid(t *testing.T) {
	runRuleCases(t, []ruleCase{
		{"data-only-manual", lay(set(dataManual("data", "none", "/dev/sdb"), vol("d", "/data", "xfs", "lvm", "100%"))), ""},
		{"data-only-discover-raid1", lay(set(`"name":"scratch","role":"data","selection":"discover","raid":"1","count":2,"spares":1`,
			vol("s", "/scratch", "xfs", "lvm", "100%"))), ""},
		{"data-only-two-sets", lay(
			set(dataManual("a", "none", "/dev/sdb"), vol("x", "/a", "xfs", "partition", "100%")),
			set(dataManual("b", "0", "/dev/sdc", "/dev/sdd"), vol("y", "/b", "xfs", "lvm", "100%"))), ""},
	})
}

// bootloader is a property of the os set; every spelling is refused on a
// data set (it would silently do nothing there).
func TestRuleBootloaderOsSetOnly(t *testing.T) {
	var cases []ruleCase
	for _, v := range []string{`"first"`, `"last"`, `"none"`, `true`, `false`, `"grub"`} {
		cases = append(cases,
			ruleCase{"data-" + v, osLayout(set(dataManual("data", "none", "/dev/sdb")+`,"bootloader":`+v, vol("d", "/d", "xfs", "lvm", "100%"))),
				"sets[1]: bootloader is valid on the os set only"},
			ruleCase{"os-" + v, lay(set(osFields("os")+`,"bootloader":`+v, espVol(), bootVol(), rootLV())), ""})
	}
	runRuleCases(t, cases)
}

func TestRunsDegradedWith(t *testing.T) {
	devs := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "/dev/disk/by-id/d" + string(rune('a'+i))
		}
		return out
	}
	cases := []struct {
		raid    string
		devices int
		spares  int
		present int
		want    bool
	}{
		{"1", 2, 0, 1, true},
		{"1", 2, 0, 0, false},
		{"none", 1, 0, 0, false},
		{"0", 2, 0, 1, false},
		{"5", 4, 0, 3, true},
		{"5", 4, 0, 2, false},
		{"5", 5, 1, 3, true}, // spare gone and one active gone
		{"6", 5, 0, 3, true},
		{"6", 5, 0, 2, false},
		{"10", 4, 0, 2, true},
		{"10", 4, 0, 1, false},
	}
	for _, c := range cases {
		s := &Set{Name: "data", Role: "data", Raid: c.raid, Spares: c.spares, Devices: devs(c.devices)}
		if got := s.RunsDegradedWith(c.present); got != c.want {
			t.Errorf("raid%s %d dev %d spare, %d present: got %v want %v", c.raid, c.devices, c.spares, c.present, got, c.want)
		}
	}
}

// Documents encoding/json reads differently from how they look: a repeated
// or case-variant key keeps only its last value, a non-string comment is
// dropped, a negative count means nothing to a manual set. They install as
// they always did, and the operator is told what was read.
func TestRuleStrictDocument(t *testing.T) {
	base := lay(set(osManual("os", "none", "/dev/vda"), espVol(), bootVol(), rootLV()))
	runRuleCases(t, []ruleCase{
		{"ok", base, ""},
		{"dup-key", `{"version":2,"version":2,"sets":[` + set(osFields("os"), espVol(), rootLV()) + `]}`, warnPrefix + `top-level: key "version" appears twice`},
		{"case-variant-key", strings.Replace(base, `"raid":"none"`, `"raid":"none","RAID":"none"`, 1), `sets[0]: unknown field 'RAID'`},
		{"repeated-slice", strings.Replace(base, `"devices":["/dev/vda"]`, `"devices":["/dev/vda","/dev/vdb"],"devices":["/dev/vdc"]`, 1), warnPrefix + `sets[0]: key "devices" appears twice`},
		{"comment-not-string", strings.Replace(base, `{"version":2,`, `{"version":2,"comment":{"x":1},`, 1), warnPrefix + "top-level: comment is not a string and is ignored"},
		{"negative-count-manual", strings.Replace(base, `"raid":"none",`, `"raid":"none","count":-1,`, 1), warnPrefix + `(set "os"): count=-1 must not be negative`},
	})
	if _, err := Parse([]byte(base + ` {}`)); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("data after the document must not parse, got %v", err)
	}
}
