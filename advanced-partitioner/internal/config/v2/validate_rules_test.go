package v2

// Table-driven tests for the build-feasibility rules: every layout the
// partition/raid/lvm/fs builders cannot build must be refused at validation
// (exit 3, before any disk is touched), and a nearby buildable layout must
// still pass. A case with want == "" must validate.

import (
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
