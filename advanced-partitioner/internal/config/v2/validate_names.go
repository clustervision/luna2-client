package v2

import (
	"fmt"
	"regexp"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/raidlevel"
)

// Set and volume names are not labels: they become kernel and LVM object
// names, and one of them is handed to `rm -rf`.
//
//   - set name: the LVM VG name (/dev/<set>/<lv>, /dev/mapper/<set>-<lv>), the
//     md array name (/dev/md/<MDName>, -efi, -boot: non-alphanumerics folded
//     to '_'), and the cleanup path `rm -rf /dev/<set>`
//     (internal/cleanup.teardownVG).
//   - volume name: the LV name of an lvm volume and the GPT partition label of
//     a partition volume.
//
// A name that would make cleanup leave /dev, or that the stack refuses after
// the disks were wiped, is refused up front; a merely odd name is a warning.

// nameRE is the character set LVM accepts for VG and LV names; md folds
// anything else to '_'.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// reservedSetNames are entries that exist directly under /dev on RHEL (and the
// other supported installer initramfs). Cleanup runs `rm -rf /dev/<set name>`,
// so a set named after one of them deletes it: "disk" removes every by-id /
// by-path link the installer resolves devices through, "md" and "mapper" the
// arrays and LVs being built, "log" the journal socket. LVM would also place
// the VG directory on top of the entry.
var reservedSetNames = map[string]bool{
	// directories
	"block": true, "bsg": true, "bus": true, "char": true, "cpu": true,
	"disk": true, "dma_heap": true, "dri": true, "fd": true, "hugepages": true,
	"infiniband": true, "input": true, "lvm": true, "mapper": true, "md": true,
	"mqueue": true, "net": true, "pts": true, "raw": true, "serial": true, "shm": true,
	"snd": true, "usb": true, "vfio": true,
	// device nodes, sockets and links every installer environment carries
	"autofs": true, "btrfs-control": true, "console": true, "core": true,
	"cpu_dma_latency": true, "cuse": true, "full": true, "fuse": true, "hpet": true,
	"initctl": true, "kmsg": true, "kvm": true, "log": true, "loop-control": true,
	"mcelog": true, "mem": true, "nvme-fabrics": true, "nvram": true, "null": true,
	"port": true, "ppp": true, "ptmx": true, "random": true, "rfkill": true, "rtc": true,
	"snapshot": true, "stderr": true, "stdin": true, "stdout": true, "tty": true,
	"udmabuf": true, "uhid": true, "uinput": true, "urandom": true, "userfaultfd": true,
	"vga_arbiter": true, "vhost-net": true, "vhost-vsock": true, "vsock": true,
	"watchdog": true, "zero": true,
}

// kernelDevNameRE matches kernel block/char device node names (with an
// optional partition suffix). A set named "sda" would make cleanup run
// `rm -rf /dev/sda` — the node of a disk the installer is about to partition.
var kernelDevNameRE = regexp.MustCompile(
	`^((sd|vd|xvd)[a-z]{1,2}|nvme[0-9]+(n[0-9]+)?|md[0-9]+|dm-[0-9]+|loop[0-9]+|` +
		`sr[0-9]+|sg[0-9]+|nbd[0-9]+|ram[0-9]+|zram[0-9]+|mmcblk[0-9]+|` +
		`tty([A-Z]+[0-9]*|[0-9]+)|vcs[au]?[0-9]*|hidraw[0-9]+|rtc[0-9]+|watchdog[0-9]+|tpm(rm)?[0-9]+)` +
		`(p?[0-9]+)?$`)

// lvmReservedLVRE matches the LV names lvcreate refuses (lvm2
// apply_lvname_restrictions): a leading '_', "snapshot" or "pvmove", and the
// component suffix of an internal sub-LV anywhere in the name.
var lvmReservedLVRE = regexp.MustCompile(
	`^(_|snapshot|pvmove)|_(cdata|cmeta|corig|cpool|cvol|imeta|iorig|mimage|mlog|pmspare|rimage|rmeta|tdata|tmeta|vdata|vorigin|wcorig)`)

// maxSetNameLen bounds a raid set's name by its longest md array name. mdadm
// keeps at most 32 characters of array name in a v1 superblock, and the raid
// builder's --homehost=any prefixes "any:"; a name that does not fit is stored
// without the prefix (or truncated), so the array assembles as a foreign
// /dev/md127 instead of /dev/md/<name> and the health probe and cleanup look
// for it in vain. The longest array is "<set>-boot": 4 + 22 + 5 < 32.
const maxSetNameLen = 22

// setRef names a set in an operator-facing message: index and name.
func setRef(idx int, name string) string {
	return fmt.Sprintf("sets[%d] (set %q)", idx, name)
}

// volRef names a volume in an operator-facing message: indexes and names.
func volRef(setIdx int, setName string, volIdx int, volName string) string {
	return fmt.Sprintf("sets[%d].volumes[%d] (set %q, volume %q)", setIdx, volIdx, setName, volName)
}

// checkName applies the rules shared by set and volume names; becomes says
// what the name turns into, for the warning.
func (c *Config) checkName(ref, name, becomes string) error {
	switch {
	case name == "." || name == "..":
		return fmt.Errorf("config_validation: %s: name must not be %q: cleanup runs rm -rf /dev/<set name>, and LVM refuses it", ref, name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("config_validation: %s: name must not start with '-' (LVM reads it as an option and refuses it)", ref)
	case !nameRE.MatchString(name):
		c.warnf("%s: name %q has characters other than letters, digits, '_', '.' and '-'; it becomes %s, and LVM refuses most other characters", ref, name, becomes)
	}
	return nil
}

// checkSetName checks one set name in isolation (the cross-set collisions
// are validateSetNameCollisions).
func (c *Config) checkSetName(ref string, s *Set, r raidlevel.Raid) error {
	if strings.Contains(s.Name, "/") {
		return fmt.Errorf("config_validation: %s: name must not contain '/': cleanup runs rm -rf /dev/<set name>, which must stay inside /dev", ref)
	}
	if err := c.checkName(ref, s.Name, "the LVM volume group, the md array name and a path under /dev"); err != nil {
		return err
	}
	switch {
	case r != raidlevel.RaidNone && len(s.Name) > maxSetNameLen:
		return fmt.Errorf(
			"config_validation: %s: name is %d characters, at most %d are allowed on a raid set: the set's md arrays (/dev/md/<name>, <name>-efi, <name>-boot) must fit mdadm's 32-character array name together with the \"any:\" homehost prefix",
			ref, len(s.Name), maxSetNameLen)
	case s.MDName() == "":
		return fmt.Errorf(
			"config_validation: %s: name must contain a letter or digit (the md array name /dev/md/<name> is the name with every other character folded to '_' and trailing '_' trimmed, which leaves nothing)",
			ref)
	case reservedSetNames[s.Name] || kernelDevNameRE.MatchString(s.Name):
		return fmt.Errorf(
			"config_validation: %s: name is reserved: /dev/%s exists on the installer, and cleanup removes /dev/<set name> (rm -rf) while LVM puts the volume group there; choose another set name",
			ref, s.Name)
	}
	return nil
}

// checkVolumeName checks one volume name; LVM's reserved names apply only to
// a volume that becomes a logical volume.
func (c *Config) checkVolumeName(ref string, v *Volume) error {
	if err := c.checkName(ref, v.Name, "the LV name or the GPT partition label"); err != nil {
		return err
	}
	if m := lvmReservedLVRE.FindString(v.Name); v.Provider == "lvm" && m != "" {
		return fmt.Errorf("config_validation: %s: lvm volume name %q matches %q, which LVM reserves for its internal volumes (lvcreate refuses it)", ref, v.Name, m)
	}
	return nil
}

// validateSetNameCollisions refuses set names that end up naming the same
// kernel or LVM object:
//   - the same name twice (one VG, one md array, one cleanup target);
//   - two names that fold to the same md name (data-1 and data_1 both become
//     /dev/md/data_1);
//   - a name whose device-mapper prefix covers another set's LVs: cleanup
//     removes every /dev/mapper entry starting with "<set>-", and the mapper
//     name of LV "x" in VG "os-a" is "os--a-x", so cleaning set "os" would
//     remove set "os-a"'s volumes.
func (c *Config) validateSetNameCollisions() error {
	byName := make(map[string]int, len(c.Sets))
	byMD := make(map[string]int, len(c.Sets))
	for i := range c.Sets {
		s := &c.Sets[i]
		if prev, dup := byName[s.Name]; dup {
			return fmt.Errorf(
				"config_validation: sets[%d] and sets[%d] are both named %q; set names must be unique (each is a volume group, an md array and a cleanup target)",
				prev, i, s.Name)
		}
		if prev, dup := byMD[s.MDName()]; dup {
			return fmt.Errorf(
				"config_validation: set names %q (sets[%d]) and %q (sets[%d]) both map to the md array /dev/md/%s (non-alphanumerics fold to '_'); rename one of them",
				c.Sets[prev].Name, prev, s.Name, i, s.MDName())
		}
		byName[s.Name], byMD[s.MDName()] = i, i
	}
	for i := range c.Sets {
		prefix := c.Sets[i].Name + "-"
		for j := range c.Sets {
			b := &c.Sets[j]
			for _, v := range b.Volumes {
				if i == j || v.Provider != "lvm" || !strings.HasPrefix(dmMapperName(b.Name, v.Name), prefix) {
					continue
				}
				return fmt.Errorf(
					"config_validation: set names %q (sets[%d]) and %q (sets[%d]) clash: cleanup of set %q removes every /dev/mapper/%s* entry, which includes /dev/mapper/%s (volume %q of set %q); rename one of the sets",
					c.Sets[i].Name, i, b.Name, j, c.Sets[i].Name, prefix, dmMapperName(b.Name, v.Name), v.Name, b.Name)
			}
		}
	}
	return nil
}

// dmMapperName is the device-mapper name of LV lv in VG vg (the entry under
// /dev/mapper): each '-' inside a name is doubled and a single '-' joins the
// two, exactly as internal/devices.VGLVPath builds it.
func dmMapperName(vg, lv string) string {
	return strings.ReplaceAll(vg, "-", "--") + "-" + strings.ReplaceAll(lv, "-", "--")
}
